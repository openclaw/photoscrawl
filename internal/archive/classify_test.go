package archive

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

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
