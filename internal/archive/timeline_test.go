package archive

import (
	"context"
	"strings"
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
				Metadata:         map[string]any{"source_type": 2},
				Albums: []photos.AlbumMembership{
					{AlbumID: "shared-trip", AlbumTitle: "Trip", AlbumKind: "album:1:101", FolderPath: "Travel / 2026"},
					{AlbumID: "quoted-album", AlbumTitle: `Album ["two"]`, AlbumKind: "album:1:101"},
				},
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
	if observation.SourceType != 2 || len(observation.Albums) != 2 {
		t.Fatalf("source metadata = %#v", observation)
	}
	albums := make(map[string]TimelineAlbum, len(observation.Albums))
	for _, album := range observation.Albums {
		albums[album.ID] = album
	}
	if albums["shared-trip"] != (TimelineAlbum{ID: "shared-trip", Title: "Trip", Kind: "album:1:101", FolderPath: "Travel / 2026"}) ||
		albums["quoted-album"] != (TimelineAlbum{ID: "quoted-album", Title: `Album ["two"]`, Kind: "album:1:101"}) {
		t.Fatalf("album objects = %#v", albums)
	}
	if observation.Latitude == nil || *observation.Latitude != 52.3676 || observation.Longitude == nil || *observation.Longitude != 4.9041 ||
		observation.AccuracyMeters == nil || *observation.AccuracyMeters != accuracy || observation.IsPrecise == nil || !*observation.IsPrecise {
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
	if all.Count != 2 || len(all.Observations) != 2 || all.Observations[0].AssetID != observation.AssetID ||
		all.Observations[0].LocationObservationID != observation.LocationObservationID {
		t.Fatalf("timeline with unlocated assets = %#v", all.Observations)
	}
	unlocated := all.Observations[1]
	if unlocated.AssetID == "" || unlocated.LocationObservationID != "" || unlocated.SourceRef != unlocated.AssetID ||
		unlocated.CreatedAt != "2026-05-27T11:00:00Z" || unlocated.MediaType != "image" || unlocated.SourceType != 0 ||
		unlocated.Latitude != nil || unlocated.Longitude != nil || unlocated.AccuracyMeters != nil || unlocated.IsPrecise != nil || len(unlocated.Albums) != 0 {
		t.Fatalf("unlocated asset = %#v", unlocated)
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
	for name, tc := range map[string]struct {
		opts        TimelineOptions
		errorPrefix string
	}{
		"missing from": {TimelineOptions{To: "2026-05-28T00:00:00Z"}, "from is required"},
		"missing to":   {TimelineOptions{From: "2026-05-27T00:00:00Z"}, "to is required"},
		"invalid from": {TimelineOptions{From: "2026-05-27", To: "2026-05-28T00:00:00Z"}, "from must be an ISO 8601 timestamp with an offset: "},
		"reverse":      {TimelineOptions{From: "2026-05-28T00:00:00Z", To: "2026-05-27T00:00:00Z"}, "from must be before to"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Timeline(context.Background(), Paths{}, tc.opts); err == nil || !strings.HasPrefix(err.Error(), tc.errorPrefix) {
				t.Fatalf("Timeline error = %v, want prefix %q", err, tc.errorPrefix)
			}
		})
	}
}
