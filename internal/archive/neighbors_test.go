package archive

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/openclaw/photoscrawl/internal/photos"
)

func TestNeighborsReturnsDeterministicSourceReasons(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	paths := testPaths(t)
	libraryPath := filepath.Join(t.TempDir(), "Fixture Photos Library.photoslibrary")
	if err := mkdirLibrary(libraryPath); err != nil {
		t.Fatal(err)
	}

	if _, err := Crawl(ctx, paths, CrawlOptions{
		LibraryPath: libraryPath,
		Provider:    fakeProvider{snapshot: fakeNeighborSnapshot()},
		Now:         fixedClock("2026-05-28T12:00:00Z"),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := Classify(ctx, paths, ClassifyOptions{
		All: true,
		Now: fixedClock("2026-05-28T12:05:00Z"),
	}); err != nil {
		t.Fatal(err)
	}

	sourceID := stableID("source_library", libraryPath)
	assetID := stableID("asset", sourceID, "neighbor-asset-1")
	result, err := Neighbors(ctx, paths, NeighborOptions{ID: assetID, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if result.ID != assetID || result.Limit != 10 {
		t.Fatalf("neighbor result header = %#v", result)
	}
	if len(result.Neighbors) != 1 {
		t.Fatalf("neighbors = %#v, want exactly one neighbor", result.Neighbors)
	}
	got := result.Neighbors[0]
	if got.ID != stableID("asset", sourceID, "neighbor-asset-2") {
		t.Fatalf("neighbor id = %q", got.ID)
	}
	if got.Score != 1 {
		t.Fatalf("neighbor score = %f, want capped score 1", got.Score)
	}
	if len(got.EvidenceIDs) == 0 {
		t.Fatal("expected evidence ids backing neighbor reasons")
	}
	reasonTypes := map[string]bool{}
	for _, reason := range got.Reasons {
		reasonTypes[reason.Type] = true
		if reason.Method == "" || reason.Weight <= 0 {
			t.Fatalf("bad reason: %#v", reason)
		}
	}
	for _, want := range []string{"same_resource_hash", "same_burst", "same_album", "nearby_time", "nearby_location", "shared_observation"} {
		if !reasonTypes[want] {
			t.Fatalf("missing neighbor reason %q in %#v", want, got.Reasons)
		}
	}
}

func fakeNeighborSnapshot() photos.LibrarySnapshot {
	altitude := 12.5
	accuracy := 8.25
	return photos.LibrarySnapshot{
		Provider:            "fake",
		PhotosVersion:       "fixture",
		AuthorizationStatus: "authorized",
		Assets: []photos.Asset{
			{
				LocalIdentifier:  "neighbor-asset-1",
				MediaType:        "image",
				MediaSubtypes:    "0",
				CreationDate:     "2026-05-27T10:00:00Z",
				ModificationDate: "2026-05-27T10:01:00Z",
				AddedDate:        "2026-05-27T10:02:00Z",
				TimezoneName:     "Europe/Amsterdam",
				Width:            4032,
				Height:           3024,
				BurstIdentifier:  "fixture-burst-1",
				Location: &photos.Location{
					Latitude:           52.3676,
					Longitude:          4.9041,
					Altitude:           &altitude,
					HorizontalAccuracy: &accuracy,
				},
				Resources: []photos.Resource{
					{Type: "photo", UTI: "public.heic", OriginalFilename: "Screenshot Neighbor One.heic", Availability: "local", StableHash: "same-fixture-resource-hash", AvailableLocally: true},
				},
				Albums: []photos.AlbumMembership{
					{AlbumID: "fixture-shared-album", AlbumTitle: "Shared Fixture Album", AlbumKind: "album:fixture"},
				},
			},
			{
				LocalIdentifier:  "neighbor-asset-2",
				MediaType:        "image",
				MediaSubtypes:    "0",
				CreationDate:     "2026-05-27T10:05:00Z",
				ModificationDate: "2026-05-27T10:06:00Z",
				AddedDate:        "2026-05-27T10:07:00Z",
				TimezoneName:     "Europe/Amsterdam",
				Width:            4032,
				Height:           3024,
				BurstIdentifier:  "fixture-burst-1",
				Location: &photos.Location{
					Latitude:           52.3677,
					Longitude:          4.9042,
					Altitude:           &altitude,
					HorizontalAccuracy: &accuracy,
				},
				Resources: []photos.Resource{
					{Type: "photo", UTI: "public.heic", OriginalFilename: "Screenshot Neighbor Two.heic", Availability: "local", StableHash: "same-fixture-resource-hash", AvailableLocally: true},
				},
				Albums: []photos.AlbumMembership{
					{AlbumID: "fixture-shared-album", AlbumTitle: "Shared Fixture Album", AlbumKind: "album:fixture"},
				},
			},
		},
	}
}
