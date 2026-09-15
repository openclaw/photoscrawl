package archive

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"testing"

	"github.com/openclaw/photoscrawl/internal/photos"
)

func TestHashNeighborsCiteOnlyMatchingResources(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	paths := testPaths(t)
	library := t.TempDir()
	snapshot := photos.LibrarySnapshot{Provider: "fake"}
	for _, name := range []string{"source", "target"} {
		snapshot.Assets = append(snapshot.Assets, photos.Asset{
			LocalIdentifier: name, MediaType: "image",
			Resources: []photos.Resource{
				{SourceIdentifier: "original", Type: "photo", StableHash: name + "-unique"},
				{SourceIdentifier: "preview", Type: "thumbnail", StableHash: "shared-preview"},
				{SourceIdentifier: "render", Type: "render", StableHash: "shared-render"},
			},
		})
	}
	if _, err := Crawl(ctx, paths, CrawlOptions{LibraryPath: library, Provider: fakeProvider{snapshot: snapshot}}); err != nil {
		t.Fatal(err)
	}
	sourceID := stableID("source_library", library)
	assetID := stableID("asset", sourceID, "source")
	result, err := Neighbors(ctx, paths, NeighborOptions{ID: assetID})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Neighbors) != 1 || len(result.Neighbors[0].Reasons) != 1 || result.Neighbors[0].Reasons[0].Type != "same_resource_hash" {
		t.Fatalf("expected one hash neighbor: %#v", result)
	}
	var want []string
	for _, name := range []string{"source", "target"} {
		evidence, err := Evidence(ctx, paths, stableID("asset", sourceID, name))
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range evidence.Evidence {
			if row["evidence_kind"] != "asset_resource" {
				continue
			}
			var resource photos.Resource
			if err := json.Unmarshal([]byte(row["value_json"].(string)), &resource); err != nil {
				t.Fatal(err)
			}
			if resource.StableHash == "shared-preview" || resource.StableHash == "shared-render" {
				want = append(want, row["id"].(string))
			}
		}
	}
	slices.Sort(want)
	if len(want) != 4 || !slices.Equal(result.Neighbors[0].EvidenceIDs, want) {
		t.Fatalf("hash evidence = %v, want all four matching resource references %v", result.Neighbors[0].EvidenceIDs, want)
	}
}

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
					{SourceIdentifier: "neighbor-one-photo", Type: "photo", UTI: "public.heic", OriginalFilename: "Screenshot Neighbor One.heic", Availability: "local", StableHash: "same-fixture-resource-hash", AvailableLocally: true},
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
					{SourceIdentifier: "neighbor-two-photo", Type: "photo", UTI: "public.heic", OriginalFilename: "Screenshot Neighbor Two.heic", Availability: "local", StableHash: "same-fixture-resource-hash", AvailableLocally: true},
				},
				Albums: []photos.AlbumMembership{
					{AlbumID: "fixture-shared-album", AlbumTitle: "Shared Fixture Album", AlbumKind: "album:fixture"},
				},
			},
		},
	}
}
