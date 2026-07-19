package archive

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openclaw/crawlkit/store"
	"github.com/openclaw/photoscrawl/internal/photos"
)

func TestClassifyLocalModelWritesTypedObservations(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	paths := testPaths(t)
	libraryPath := filepath.Join(t.TempDir(), "Fixture Photos Library.photoslibrary")
	if err := mkdirLibrary(libraryPath); err != nil {
		t.Fatal(err)
	}
	imagePath := filepath.Join(t.TempDir(), "fixture.jpeg")
	if err := os.WriteFile(imagePath, []byte("fixture image bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request ollamaGenerateRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if request.Model != "fixture-vision" || len(request.Images) != 1 {
			t.Fatalf("request = %#v", request)
		}
		_ = json.NewEncoder(w).Encode(ollamaGenerateResponse{
			Response: `{
				"scene_summary":"Outdoor street-food meal with satay skewers, prawns, sauces, and a shared table.",
				"visible_text_summary":"A small receipt-like slip is visible.",
				"place_candidates":["hawker centre"],
				"landmark_candidates":[],
				"merchant_or_venue_candidates":["satay stall candidate"],
				"food_or_objects":["satay skewers","grilled prawns","peanut sauce"],
				"people_presence":"hands only, no identity",
				"privacy_sensitivity":["receipt","hands"],
				"cluster_terms":["street_food","satay","shared_table"],
				"uncertainties":["exact venue is not proven"]
			}`,
			Done: true,
		})
	}))
	defer server.Close()

	provider := fakeProvider{snapshot: photos.LibrarySnapshot{
		Provider:            "fake",
		PhotosVersion:       "fixture",
		AuthorizationStatus: "authorized",
		Assets: []photos.Asset{
			{
				LocalIdentifier: "fixture-local-model-asset",
				MediaType:       "image",
				MediaSubtypes:   "0",
				CreationDate:    "2026-05-27T12:00:00Z",
				Width:           100,
				Height:          80,
				Resources: []photos.Resource{
					{
						SourceIdentifier: "fixture-local-photo",
						Type:             "photo",
						UTI:              "public.jpeg",
						OriginalFilename: "fixture.jpeg",
						LocalPath:        imagePath,
						Availability:     "local",
						AvailableLocally: true,
					},
				},
			},
		},
	}}
	if _, err := Crawl(ctx, paths, CrawlOptions{
		LibraryPath: libraryPath,
		Provider:    provider,
		Now:         fixedClock("2026-05-28T10:00:00Z"),
	}); err != nil {
		t.Fatal(err)
	}
	metadataOnly, err := Classify(ctx, paths, ClassifyOptions{
		All: true,
		Now: fixedClock("2026-05-28T10:05:00Z"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if metadataOnly.MetadataClassified != 1 || metadataOnly.ContentClassified != 0 {
		t.Fatalf("metadata classify result = %#v", metadataOnly)
	}

	result, err := Classify(ctx, paths, ClassifyOptions{
		All:           true,
		LocalModel:    "fixture-vision",
		LocalModelURL: server.URL,
		Now:           fixedClock("2026-05-28T10:15:00Z"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ContentClassified != 1 || result.ContentObservationsWritten == 0 || result.ContentClassificationFailures != 0 || result.WaitingForLocalContent != 0 {
		t.Fatalf("classify result = %#v", result)
	}
	if result.LocalModelAPI != localModelAPIOllama || result.LocalModelRequestedEndpoint != server.URL || len(result.LocalModelResponseEndpoints) != 1 || result.LocalModelResponseEndpoints[0] != server.URL || result.LocalModelNetworkScope != "loopback" || !result.TransmitsImageBytes || result.LocalModelHTTPRequestAttempts != 1 || result.LocalModelHTTPResponses != 1 {
		t.Fatalf("local model provenance = %#v", result)
	}

	search, err := Search(ctx, paths, SearchOptions{Query: "satay", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(search.Results) == 0 || search.Results[0].ObservationID == "" {
		t.Fatalf("search = %#v", search.Results)
	}
	opened, err := Open(ctx, paths, search.Results[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(opened.ModelObservations) == 0 || len(opened.ObservationTerms) == 0 {
		t.Fatalf("opened model observations=%d terms=%d", len(opened.ModelObservations), len(opened.ObservationTerms))
	}
	evidence, err := Evidence(ctx, paths, search.Results[0].ObservationID)
	if err != nil {
		t.Fatal(err)
	}
	if len(evidence.Evidence) == 0 {
		t.Fatal("expected local model evidence")
	}
	var foundProvenance bool
	for _, row := range evidence.Evidence {
		if row["source"] != localModelClassifierSource {
			continue
		}
		var value map[string]any
		if err := json.Unmarshal([]byte(row["value_json"].(string)), &value); err != nil {
			t.Fatal(err)
		}
		if value["endpoint"] == server.URL && value["network_scope"] == "loopback" && value["image_transmitted"] == true && value["transmitted_image_bytes"] == float64(len("fixture image bytes")) {
			foundProvenance = true
		}
	}
	if !foundProvenance {
		t.Fatalf("evidence provenance = %#v", evidence.Evidence)
	}
	db, err := store.OpenReadOnly(ctx, paths.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var runMetadata string
	if err := db.DB().QueryRowContext(ctx, `select metadata_json from model_run where id = ?`, result.ModelRunID).Scan(&runMetadata); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(runMetadata, `"requested_endpoint":"`+server.URL+`"`) || !strings.Contains(runMetadata, `"response_endpoints":["`+server.URL+`"]`) || !strings.Contains(runMetadata, `"network_scope":"loopback"`) || !strings.Contains(runMetadata, `"http_request_attempts":1`) || !strings.Contains(runMetadata, `"http_responses_received":1`) {
		t.Fatalf("model run metadata = %s", runMetadata)
	}
}

func TestPromptLeakageCreatesQualityIssue(t *testing.T) {
	t.Parallel()
	observations := observationsFromPayload(map[string]any{
		"scene_summary":        "A retail display.",
		"visible_text_summary": "Return only valid compact JSON",
	})
	var found bool
	for _, observation := range observations {
		if observation.ObservationType == "quality_issue" && observation.ValueText == "model_prompt_leakage" {
			found = true
		}
	}
	if !found {
		t.Fatalf("observations = %#v", observations)
	}
}

func TestLocalModelOpenAIChatCompletion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	imagePath := filepath.Join(t.TempDir(), "fixture.jpeg")
	if err := os.WriteFile(imagePath, []byte("fixture image bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		var request openAIChatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if request.Model != "fixture-openai-vision" || len(request.Messages) != 1 || len(request.Messages[0].Content) != 2 {
			t.Fatalf("request = %#v", request)
		}
		if request.Messages[0].Content[1].ImageURL == nil || !strings.HasPrefix(request.Messages[0].Content[1].ImageURL.URL, "data:") {
			t.Fatalf("image part = %#v", request.Messages[0].Content[1])
		}
		_ = json.NewEncoder(w).Encode(openAIChatCompletionResponse{
			Choices: []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			}{{
				Message: struct {
					Content string `json:"content"`
				}{Content: `{"scene_summary":"A plate of food.","food_or_objects":["plate"],"cluster_terms":["food"]}`},
			}},
		})
	}))
	defer server.Close()

	classifier, err := newLocalModelClassifier(ctx, "fixture-openai-vision", server.URL, localModelAPIOpenAI)
	if err != nil {
		t.Fatal(err)
	}
	result, err := classifier.classify(ctx, imagePath)
	if err != nil {
		t.Fatal(err)
	}
	if result.Payload["scene_summary"] != "A plate of food." || len(result.Observations) == 0 || result.Endpoint != server.URL+"/v1/chat/completions" || result.HTTPRequests != 1 || result.HTTPResponses != 1 {
		t.Fatalf("result = %#v", result)
	}
}

func TestClassifyLocalModelTerminatesRemoteNonImageRows(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	paths := testPaths(t)
	libraryPath := filepath.Join(t.TempDir(), "Fixture Photos Library.photoslibrary")
	if err := mkdirLibrary(libraryPath); err != nil {
		t.Fatal(err)
	}
	provider := fakeProvider{snapshot: photos.LibrarySnapshot{
		Provider:            "fake",
		PhotosVersion:       "fixture",
		AuthorizationStatus: "authorized",
		Assets: []photos.Asset{
			{
				LocalIdentifier: "fixture-remote-video-asset",
				MediaType:       "video",
				MediaSubtypes:   "0",
				CreationDate:    "2026-05-27T12:00:00Z",
				Width:           100,
				Height:          80,
				Resources: []photos.Resource{
					{
						SourceIdentifier: "fixture-remote-video",
						Type:             "video",
						UTI:              "com.apple.quicktime-movie",
						OriginalFilename: "fixture.mov",
						Availability:     "remote",
						NeedsDownload:    true,
					},
				},
			},
		},
	}}
	if _, err := Crawl(ctx, paths, CrawlOptions{
		LibraryPath: libraryPath,
		Provider:    provider,
		Now:         fixedClock("2026-05-28T10:00:00Z"),
	}); err != nil {
		t.Fatal(err)
	}
	first, err := Classify(ctx, paths, ClassifyOptions{
		All:        true,
		LocalModel: "fixture-vision",
		Now:        fixedClock("2026-05-28T10:15:00Z"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Processed != 1 || first.WaitingForLocalContent != 0 || first.ContentClassified != 0 || first.ContentClassificationFailures != 0 {
		t.Fatalf("first classify result = %#v", first)
	}
	second, err := Classify(ctx, paths, ClassifyOptions{
		All:        true,
		LocalModel: "fixture-vision",
		Now:        fixedClock("2026-05-28T10:20:00Z"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.Processed != 0 {
		t.Fatalf("second classify result = %#v", second)
	}
}

func TestClassifyLocalModelKeepsRemoteImagesRetryable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	paths := testPaths(t)
	libraryPath := filepath.Join(t.TempDir(), "Fixture Photos Library.photoslibrary")
	if err := mkdirLibrary(libraryPath); err != nil {
		t.Fatal(err)
	}
	provider := fakeProvider{snapshot: photos.LibrarySnapshot{
		Provider:            "fake",
		PhotosVersion:       "fixture",
		AuthorizationStatus: "authorized",
		Assets: []photos.Asset{
			{
				LocalIdentifier: "fixture-remote-image-asset",
				MediaType:       "image",
				MediaSubtypes:   "0",
				CreationDate:    "2026-05-27T12:00:00Z",
				Width:           100,
				Height:          80,
				Resources: []photos.Resource{
					{
						SourceIdentifier: "fixture-remote-photo",
						Type:             "photo",
						UTI:              "public.jpeg",
						OriginalFilename: "fixture.jpeg",
						Availability:     "remote",
						NeedsDownload:    true,
					},
				},
			},
		},
	}}
	if _, err := Crawl(ctx, paths, CrawlOptions{
		LibraryPath: libraryPath,
		Provider:    provider,
		Now:         fixedClock("2026-05-28T10:00:00Z"),
	}); err != nil {
		t.Fatal(err)
	}
	first, err := Classify(ctx, paths, ClassifyOptions{
		All:        true,
		LocalModel: "fixture-vision",
		Now:        fixedClock("2026-05-28T10:15:00Z"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Processed != 1 || first.WaitingForLocalContent != 1 || first.ContentClassified != 0 {
		t.Fatalf("first classify result = %#v", first)
	}
	second, err := Classify(ctx, paths, ClassifyOptions{
		All:        true,
		LocalModel: "fixture-vision",
		Now:        fixedClock("2026-05-28T10:20:00Z"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.Processed != 1 || second.WaitingForLocalContent != 1 {
		t.Fatalf("second classify result = %#v", second)
	}
}

func TestClassifyLocalModelPrioritizesPendingRowsBeforeWaitingRows(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	paths := testPaths(t)
	libraryPath := filepath.Join(t.TempDir(), "Fixture Photos Library.photoslibrary")
	if err := mkdirLibrary(libraryPath); err != nil {
		t.Fatal(err)
	}
	imagePath := filepath.Join(t.TempDir(), "fixture.jpeg")
	if err := os.WriteFile(imagePath, []byte("fixture image bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(ollamaGenerateResponse{
			Response: `{"scene_summary":"A local image.","cluster_terms":["local_image"]}`,
			Done:     true,
		})
	}))
	defer server.Close()
	provider := fakeProvider{snapshot: photos.LibrarySnapshot{
		Provider:            "fake",
		PhotosVersion:       "fixture",
		AuthorizationStatus: "authorized",
		Assets: []photos.Asset{
			{
				LocalIdentifier: "newer-remote-image",
				MediaType:       "image",
				MediaSubtypes:   "0",
				CreationDate:    "2026-05-28T12:00:00Z",
				Width:           100,
				Height:          80,
				Resources: []photos.Resource{
					{
						SourceIdentifier: "newer-remote-photo",
						Type:             "photo",
						UTI:              "public.jpeg",
						OriginalFilename: "remote.jpeg",
						Availability:     "remote",
						NeedsDownload:    true,
					},
				},
			},
			{
				LocalIdentifier: "older-local-image",
				MediaType:       "image",
				MediaSubtypes:   "0",
				CreationDate:    "2026-05-27T12:00:00Z",
				Width:           100,
				Height:          80,
				Resources: []photos.Resource{
					{
						SourceIdentifier: "older-local-photo",
						Type:             "photo",
						UTI:              "public.jpeg",
						OriginalFilename: "local.jpeg",
						LocalPath:        imagePath,
						Availability:     "local",
						AvailableLocally: true,
					},
				},
			},
		},
	}}
	if _, err := Crawl(ctx, paths, CrawlOptions{
		LibraryPath: libraryPath,
		Provider:    provider,
		Now:         fixedClock("2026-05-28T10:00:00Z"),
	}); err != nil {
		t.Fatal(err)
	}
	first, err := Classify(ctx, paths, ClassifyOptions{
		Limit:         1,
		LocalModel:    "fixture-vision",
		LocalModelURL: server.URL,
		Now:           fixedClock("2026-05-28T10:15:00Z"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Processed != 1 || first.WaitingForLocalContent != 1 || first.ContentClassified != 0 {
		t.Fatalf("first classify result = %#v", first)
	}
	second, err := Classify(ctx, paths, ClassifyOptions{
		Limit:         1,
		LocalModel:    "fixture-vision",
		LocalModelURL: server.URL,
		Now:           fixedClock("2026-05-28T10:20:00Z"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.Processed != 1 || second.ContentClassified != 1 || second.WaitingForLocalContent != 0 {
		t.Fatalf("second classify result = %#v", second)
	}
}
