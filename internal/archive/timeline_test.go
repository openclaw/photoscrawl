package archive

import (
	"context"
	"testing"

	"github.com/openclaw/photoscrawl/internal/photos"
)

func TestTimelineReturnsLocatedAssetsInHalfOpenRange(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	paths := testPaths(t)
	accuracy := 8.25
	provider := fakeProvider{snapshot: photos.LibrarySnapshot{
		Provider:            "fake",
		PhotosVersion:       "fixture",
		AuthorizationStatus: "authorized",
		Assets: []photos.Asset{
			{
				LocalIdentifier:  "timeline-located",
				MediaType:        "image",
				CreationDate:     "2026-05-27T10:00:00Z",
				ModificationDate: "2026-05-27T10:01:00Z",
				AddedDate:        "2026-05-27T10:02:00Z",
				TimezoneName:     "Europe/Amsterdam",
				Width:            4032,
				Height:           3024,
				Location: &photos.Location{
					Latitude:           52.3676,
					Longitude:          4.9041,
					HorizontalAccuracy: &accuracy,
				},
			},
			{
				LocalIdentifier:  "timeline-unlocated",
				MediaType:        "image",
				CreationDate:     "2026-05-27T11:00:00Z",
				ModificationDate: "2026-05-27T11:01:00Z",
				AddedDate:        "2026-05-27T11:02:00Z",
				TimezoneName:     "Europe/Amsterdam",
				Width:            4032,
				Height:           3024,
			},
		},
	}}
	if _, err := Crawl(ctx, paths, CrawlOptions{
		LibraryPath: t.TempDir(),
		Provider:    provider,
		Now:         fixedClock("2026-05-28T10:00:00Z"),
	}); err != nil {
		t.Fatal(err)
	}

	result, err := Timeline(ctx, paths, TimelineOptions{
		From: "2026-05-27T12:00:00+02:00",
		To:   "2026-05-28T00:00:00+02:00",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.From != "2026-05-27T10:00:00Z" || result.To != "2026-05-27T22:00:00Z" {
		t.Fatalf("normalized bounds = %q through %q", result.From, result.To)
	}
	if result.Count != 1 || len(result.Observations) != 1 {
		t.Fatalf("timeline count = %d, observations = %d", result.Count, len(result.Observations))
	}
	observation := result.Observations[0]
	if observation.AssetID == "" || observation.LocationObservationID == "" || observation.SourceRef != observation.LocationObservationID {
		t.Fatalf("identity = %#v", observation)
	}
	if observation.CreatedAt != "2026-05-27T10:00:00Z" || observation.MediaType != "image" {
		t.Fatalf("asset metadata = %#v", observation)
	}
	if observation.Latitude == nil || observation.Longitude == nil || observation.AccuracyMeters == nil || *observation.AccuracyMeters != accuracy || observation.IsPrecise == nil || !*observation.IsPrecise {
		t.Fatalf("location quality = %#v", observation)
	}

	all, err := Timeline(ctx, paths, TimelineOptions{
		From:             "2026-05-27T12:00:00+02:00",
		To:               "2026-05-28T00:00:00+02:00",
		IncludeUnlocated: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if all.Count != 2 || all.Observations[1].LocationObservationID != "" || all.Observations[1].Latitude != nil || all.Observations[1].SourceRef != all.Observations[1].AssetID {
		t.Fatalf("timeline with unlocated assets = %#v", all.Observations)
	}

	empty, err := Timeline(ctx, paths, TimelineOptions{
		From: "2026-05-27T09:00:00Z",
		To:   "2026-05-27T10:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	if empty.Count != 0 {
		t.Fatalf("half-open boundary returned %d observations", empty.Count)
	}
}

func TestTimelineRejectsInvalidBoundsBeforeOpeningDatabase(t *testing.T) {
	t.Parallel()
	for name, opts := range map[string]TimelineOptions{
		"missing from": {To: "2026-05-28T00:00:00Z"},
		"missing to":   {From: "2026-05-27T00:00:00Z"},
		"invalid from": {From: "2026-05-27", To: "2026-05-28T00:00:00Z"},
		"reverse":      {From: "2026-05-28T00:00:00Z", To: "2026-05-27T00:00:00Z"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Timeline(context.Background(), Paths{}, opts); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}
