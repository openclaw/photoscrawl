package archive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestClassifyLocalModelRetriesContentFailure(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	paths := testPaths(t)
	libraryPath := filepath.Join(t.TempDir(), "Fixture Photos Library.photoslibrary")
	if err := mkdirLibrary(libraryPath); err != nil {
		t.Fatal(err)
	}
	imagePath := writeModelTestImage(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Retry-Proof") == "ready" {
			openAIResponseHandler(w, r)
			return
		}
		http.Error(w, "not ready", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	provider := fakeProvider{snapshot: photos.LibrarySnapshot{
		Provider: "fake", PhotosVersion: "fixture", AuthorizationStatus: "authorized",
		Assets: []photos.Asset{{
			LocalIdentifier: "retry-local-model-asset", MediaType: "image", CreationDate: "2026-08-12T12:00:00Z",
			Resources: []photos.Resource{{SourceIdentifier: "retry-photo", Type: "photo", UTI: "public.jpeg", LocalPath: imagePath, AvailableLocally: true}},
		}},
	}}
	if _, err := Crawl(ctx, paths, CrawlOptions{LibraryPath: libraryPath, Provider: provider, Now: fixedClock("2026-08-12T12:05:00Z")}); err != nil {
		t.Fatal(err)
	}
	failed, err := Classify(ctx, paths, ClassifyOptions{All: true, LocalModel: "fixture-vision", LocalModelAPI: localModelAPIOpenAI, LocalModelURL: server.URL, Now: fixedClock("2026-08-12T12:10:00Z")})
	if err != nil {
		t.Fatal(err)
	}
	if failed.ContentClassificationFailures != 1 {
		t.Fatalf("failed classify = %#v", failed)
	}

	server.Config.Handler = http.HandlerFunc(openAIResponseHandler)
	retried, err := Classify(ctx, paths, ClassifyOptions{All: true, LocalModel: "fixture-vision", LocalModelAPI: localModelAPIOpenAI, LocalModelURL: server.URL, Now: fixedClock("2026-08-12T12:15:00Z")})
	if err != nil {
		t.Fatal(err)
	}
	if retried.ContentClassified != 1 || retried.ContentClassificationFailures != 0 {
		t.Fatalf("retried classify = %#v", retried)
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

func TestClassifyLocalModelRetriesUnavailableImagesOnlyWithDownloadsEnabled(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	paths := testPaths(t)
	libraryPath := filepath.Join(t.TempDir(), "Fixture Photos Library.photoslibrary")
	if err := mkdirLibrary(libraryPath); err != nil {
		t.Fatal(err)
	}
	provider := fakeProvider{snapshot: photos.LibrarySnapshot{
		Provider: "fake",
		Assets: []photos.Asset{{
			LocalIdentifier: "fixture-unavailable-image",
			MediaType:       "image",
			CreationDate:    "2026-05-27T12:00:00Z",
			Resources: []photos.Resource{{
				SourceIdentifier: "shared-photo",
				Type:             "photo",
				OriginalFilename: "shared.jpeg",
				Availability:     "unknown",
			}},
		}},
	}}
	if _, err := Crawl(ctx, paths, CrawlOptions{LibraryPath: libraryPath, Provider: provider, Now: fixedClock("2026-05-28T10:00:00Z")}); err != nil {
		t.Fatal(err)
	}
	first, err := Classify(ctx, paths, ClassifyOptions{All: true, LocalModel: "fixture-vision", Now: fixedClock("2026-05-28T10:15:00Z")})
	if err != nil {
		t.Fatal(err)
	}
	if first.Processed != 1 || first.WaitingForLocalContent != 0 {
		t.Fatalf("first classify result = %#v", first)
	}
	second, err := Classify(ctx, paths, ClassifyOptions{All: true, LocalModel: "fixture-vision", Now: fixedClock("2026-05-28T10:20:00Z")})
	if err != nil {
		t.Fatal(err)
	}
	if second.Processed != 0 {
		t.Fatalf("unavailable image stayed retryable = %#v", second)
	}
	archiveDB, err := store.Open(ctx, store.Options{Path: paths.Database, Schema: Schema, SchemaVersion: SchemaVersion})
	if err != nil {
		t.Fatal(err)
	}
	defer archiveDB.Close()
	tx, err := archiveDB.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	withoutDownloads, err := loadClassifyInputs(ctx, tx, 0, true, false)
	if err != nil {
		t.Fatal(err)
	}
	withDownloads, err := loadClassifyInputs(ctx, tx, 0, true, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(withoutDownloads) != 0 || len(withDownloads) != 1 || withDownloads[0].MediaType != "image" {
		t.Fatalf("unavailable queue selection without=%#v with=%#v", withoutDownloads, withDownloads)
	}
}

func TestClassifyLocalModelDownloadsTemporaryICloudPreview(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	paths := testPaths(t)
	libraryPath := filepath.Join(t.TempDir(), "Fixture Photos Library.photoslibrary")
	if err := mkdirLibrary(libraryPath); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(ollamaGenerateResponse{
			Response: `{"scene_summary":"Dinner at a shared table.","cluster_terms":["dinner","shared_table"]}`,
			Done:     true,
		})
	}))
	defer server.Close()
	provider := fakeProvider{snapshot: photos.LibrarySnapshot{
		Provider: "fake",
		Assets: []photos.Asset{{
			LocalIdentifier: "fixture-cloud-shared-image",
			MediaType:       "image",
			CreationDate:    "2026-07-30T20:00:00Z",
			Width:           4032,
			Height:          3024,
			Resources: []photos.Resource{{
				SourceIdentifier: "cloud-shared-photo",
				Type:             "photo",
				OriginalFilename: "shared.jpeg",
				Availability:     "unknown",
			}},
		}},
	}}
	if _, err := Crawl(ctx, paths, CrawlOptions{LibraryPath: libraryPath, Provider: provider, Now: fixedClock("2026-07-31T10:00:00Z")}); err != nil {
		t.Fatal(err)
	}

	var exportedIdentifier, exportedPath string
	var exportedDimension int
	var exportedWithNetwork bool
	result, err := Classify(ctx, paths, ClassifyOptions{
		All:                  true,
		LocalModel:           "fixture-vision",
		LocalModelURL:        server.URL,
		AllowICloudDownloads: true,
		Now:                  fixedClock("2026-07-31T10:05:00Z"),
		previewExporter: func(_ context.Context, identifier, destination string, maxDimension int, allowNetwork bool) error {
			exportedIdentifier = identifier
			exportedPath = destination
			exportedDimension = maxDimension
			exportedWithNetwork = allowNetwork
			return os.WriteFile(destination, []byte("bounded preview bytes"), 0o600)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if exportedIdentifier != "fixture-cloud-shared-image" || exportedDimension != 1600 || !exportedWithNetwork {
		t.Fatalf("preview request = identifier %q dimension %d network %v", exportedIdentifier, exportedDimension, exportedWithNetwork)
	}
	if result.ContentClassified != 1 || result.ICloudPreviewsDownloaded != 1 || result.ICloudPreviewDownloadFailures != 0 {
		t.Fatalf("classify result = %#v", result)
	}
	if _, err := os.Stat(exportedPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary preview was not removed: %v", err)
	}
	previewDir := filepath.Dir(exportedPath)
	if previewDir == paths.ClassificationPreviewCacheDir() {
		t.Fatalf("preview was written directly into the shared cache: %q", exportedPath)
	}
	if _, err := os.Stat(previewDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invocation preview directory was not removed: %v", err)
	}

	db, err := store.OpenReadOnly(ctx, paths.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var evidenceJSON string
	if err := db.DB().QueryRowContext(ctx, `
select value_json
from evidence_ref
where asset_id in (select id from asset where local_identifier = ?)
  and evidence_kind = 'content_classification'
`, "fixture-cloud-shared-image").Scan(&evidenceJSON); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(evidenceJSON, `"image_path_class":"classification_preview"`) {
		t.Fatalf("preview evidence = %s", evidenceJSON)
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

func TestPrepareClassificationPreviewGivesEachInvocationItsOwnFile(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	paths := testPaths(t)
	input := classifyInput{AssetID: "asset:one", LocalIdentifier: "ONE/L0/001"}
	exporter := func(_ context.Context, _, destination string, _ int, _ bool) error {
		return os.WriteFile(destination, []byte("preview"), 0o600)
	}
	first, err := prepareClassificationPreview(ctx, paths, &input, ClassifyOptions{previewExporter: exporter})
	if err != nil {
		t.Fatal(err)
	}
	second, err := prepareClassificationPreview(ctx, paths, &input, ClassifyOptions{previewExporter: exporter})
	if err != nil {
		t.Fatal(err)
	}
	if first == second || filepath.Dir(first) == filepath.Dir(second) {
		t.Fatalf("two invocations shared a preview path: %q and %q", first, second)
	}
	// Removing one invocation's directory must leave the other's file intact.
	if err := os.RemoveAll(filepath.Dir(first)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(second); err != nil {
		t.Fatalf("second invocation preview was disturbed: %v", err)
	}
	info, err := os.Stat(filepath.Dir(second))
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("preview directory mode = %v, %v", info.Mode(), err)
	}
}

func TestClassifyCancelledPreviewLeavesQueueRowForRetry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	paths := testPaths(t)
	libraryPath := filepath.Join(t.TempDir(), "Fixture Photos Library.photoslibrary")
	if err := mkdirLibrary(libraryPath); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(ollamaGenerateResponse{Response: `{"scene_summary":"unused"}`, Done: true})
	}))
	defer server.Close()
	provider := fakeProvider{snapshot: photos.LibrarySnapshot{
		Provider: "fake",
		Assets: []photos.Asset{{
			LocalIdentifier: "fixture-cloud-image",
			MediaType:       "image",
			CreationDate:    "2026-07-30T20:00:00Z",
			Width:           4032,
			Height:          3024,
			Resources: []photos.Resource{{
				SourceIdentifier: "cloud-photo",
				Type:             "photo",
				OriginalFilename: "cloud.jpeg",
				Availability:     "unknown",
			}},
		}},
	}}
	if _, err := Crawl(ctx, paths, CrawlOptions{LibraryPath: libraryPath, Provider: provider, Now: fixedClock("2026-07-31T10:00:00Z")}); err != nil {
		t.Fatal(err)
	}
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	_, err := Classify(runCtx, paths, ClassifyOptions{
		All:                  true,
		LocalModel:           "fixture-vision",
		LocalModelURL:        server.URL,
		AllowICloudDownloads: true,
		Now:                  fixedClock("2026-07-31T10:05:00Z"),
		previewExporter: func(context.Context, string, string, int, bool) error {
			// SIGINT cancels the run's context while the preview is in flight.
			cancelRun()
			return context.Canceled
		},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("classify error = %v, want context.Canceled", err)
	}
	db, err := store.OpenReadOnly(ctx, paths.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var state, reason string
	if err := db.DB().QueryRowContext(ctx, `select state, coalesce(reason, '') from classification_queue`).Scan(&state, &reason); err != nil {
		t.Fatal(err)
	}
	if state == "content_failed" {
		t.Fatalf("cancelled preview marked the asset failed: state=%q reason=%q", state, reason)
	}
}

func TestClassifyStalledPreviewTimesOutWithoutAbortingRun(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	paths := testPaths(t)
	libraryPath := filepath.Join(t.TempDir(), "Fixture Photos Library.photoslibrary")
	if err := mkdirLibrary(libraryPath); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(ollamaGenerateResponse{Response: `{"scene_summary":"unused"}`, Done: true})
	}))
	defer server.Close()
	provider := fakeProvider{snapshot: photos.LibrarySnapshot{
		Provider: "fake",
		Assets: []photos.Asset{{
			LocalIdentifier: "fixture-stalled-cloud-image",
			MediaType:       "image",
			CreationDate:    "2026-07-30T20:00:00Z",
			Width:           4032,
			Height:          3024,
			Resources: []photos.Resource{{
				SourceIdentifier: "stalled-cloud-photo",
				Type:             "photo",
				OriginalFilename: "stalled.jpeg",
				Availability:     "unknown",
			}},
		}},
	}}
	if _, err := Crawl(ctx, paths, CrawlOptions{LibraryPath: libraryPath, Provider: provider, Now: fixedClock("2026-07-31T10:00:00Z")}); err != nil {
		t.Fatal(err)
	}
	result, err := Classify(ctx, paths, ClassifyOptions{
		All:                  true,
		LocalModel:           "fixture-vision",
		LocalModelURL:        server.URL,
		AllowICloudDownloads: true,
		PreviewTimeout:       20 * time.Millisecond,
		Now:                  fixedClock("2026-07-31T10:05:00Z"),
		previewExporter: func(ctx context.Context, _, _ string, _ int, _ bool) error {
			<-ctx.Done()
			return ctx.Err()
		},
	})
	if err != nil {
		t.Fatalf("classify error = %v, want the run to finish", err)
	}
	if result.ICloudPreviewDownloadFailures != 1 {
		t.Fatalf("preview download failures = %d, want 1", result.ICloudPreviewDownloadFailures)
	}
	db, err := store.OpenReadOnly(ctx, paths.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var state, reason string
	if err := db.DB().QueryRowContext(ctx, `select state, coalesce(reason, '') from classification_queue`).Scan(&state, &reason); err != nil {
		t.Fatal(err)
	}
	if state != "content_failed" || !strings.Contains(reason, "timed out") {
		t.Fatalf("stalled preview queue row = state %q reason %q, want content_failed with a timeout reason", state, reason)
	}
}

func TestClassifyRequestTimeoutFailsAssetAndContinuesRun(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	paths := testPaths(t)
	libraryPath := filepath.Join(t.TempDir(), "Fixture Photos Library.photoslibrary")
	if err := mkdirLibrary(libraryPath); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(ollamaGenerateResponse{Response: `{"scene_summary":"Dinner at a shared table.","cluster_terms":["dinner"]}`, Done: true})
	}))
	defer server.Close()
	cloudImage := func(identifier, created string) photos.Asset {
		return photos.Asset{
			LocalIdentifier: identifier,
			MediaType:       "image",
			CreationDate:    created,
			Width:           4032,
			Height:          3024,
			Resources: []photos.Resource{{
				SourceIdentifier: identifier + "-photo",
				Type:             "photo",
				OriginalFilename: identifier + ".jpeg",
				Availability:     "unknown",
			}},
		}
	}
	provider := fakeProvider{snapshot: photos.LibrarySnapshot{
		Provider: "fake",
		Assets: []photos.Asset{
			cloudImage("fixture-timeout-image", "2026-07-30T21:00:00Z"),
			cloudImage("fixture-later-image", "2026-07-30T20:00:00Z"),
		},
	}}
	if _, err := Crawl(ctx, paths, CrawlOptions{LibraryPath: libraryPath, Provider: provider, Now: fixedClock("2026-07-31T10:00:00Z")}); err != nil {
		t.Fatal(err)
	}
	result, err := Classify(ctx, paths, ClassifyOptions{
		All:                  true,
		LocalModel:           "fixture-vision",
		LocalModelURL:        server.URL,
		AllowICloudDownloads: true,
		Now:                  fixedClock("2026-07-31T10:05:00Z"),
		previewExporter: func(_ context.Context, identifier, destination string, _ int, _ bool) error {
			if identifier == "fixture-timeout-image" {
				// An http.Client timeout matches context.DeadlineExceeded while
				// the run's own context is still live.
				return fmt.Errorf("request timed out: %w", context.DeadlineExceeded)
			}
			return os.WriteFile(destination, []byte("bounded preview bytes"), 0o600)
		},
	})
	if err != nil {
		t.Fatalf("classify error = %v, want the run to continue past a request timeout", err)
	}
	if result.ContentClassified != 1 || result.ContentClassificationFailures != 1 {
		t.Fatalf("classify result = classified %d failures %d, want 1 and 1", result.ContentClassified, result.ContentClassificationFailures)
	}
	db, err := store.OpenReadOnly(ctx, paths.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	states := map[string]string{}
	rows, err := db.DB().QueryContext(ctx, `select a.local_identifier, q.state from classification_queue q join asset a on a.id = q.asset_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var identifier, state string
		if err := rows.Scan(&identifier, &state); err != nil {
			t.Fatal(err)
		}
		states[identifier] = state
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if states["fixture-timeout-image"] != "content_failed" || states["fixture-later-image"] != "content_classified" {
		t.Fatalf("queue states = %v, want the timed-out asset failed and the later asset classified", states)
	}
}
