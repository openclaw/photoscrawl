package archive

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/openclaw/crawlkit/control"
	"github.com/openclaw/photoscrawl/internal/photos"
)

func TestCrawlImportsSnapshotAndTracksDelta(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	paths := testPaths(t)
	libraryPath := filepath.Join(t.TempDir(), "Fixture Photos Library.photoslibrary")
	if err := mkdirLibrary(libraryPath); err != nil {
		t.Fatal(err)
	}

	provider := fakeProvider{snapshot: fakeSnapshot(false, true)}
	result, err := Crawl(ctx, paths, CrawlOptions{
		LibraryPath: libraryPath,
		Provider:    provider,
		Now:         fixedClock("2026-05-28T10:00:00Z"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.AssetsSeen != 2 || result.AssetsNew != 2 || result.AssetsChanged != 0 || result.AssetsUnchanged != 0 {
		t.Fatalf("first crawl delta = new %d changed %d unchanged %d seen %d", result.AssetsNew, result.AssetsChanged, result.AssetsUnchanged, result.AssetsSeen)
	}
	if result.ResourcesSeen != 2 || result.AlbumMembershipsSeen != 2 || result.LocationsSeen != 1 {
		t.Fatalf("first crawl counts = resources %d albums %d locations %d", result.ResourcesSeen, result.AlbumMembershipsSeen, result.LocationsSeen)
	}
	if result.QueuedForClassify != 2 || result.QueuedNeedsDownload != 1 {
		t.Fatalf("first crawl queue = classify %d download %d", result.QueuedForClassify, result.QueuedNeedsDownload)
	}

	search, err := Search(ctx, paths, SearchOptions{Query: "beach", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(search.Results) != 1 {
		t.Fatalf("search results = %d, want 1", len(search.Results))
	}

	opened, err := Open(ctx, paths, search.Results[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(opened.Resources) != 1 || len(opened.Albums) != 1 || len(opened.Locations) != 1 || len(opened.Evidence) == 0 {
		t.Fatalf("open returned resources=%d albums=%d locations=%d evidence=%d", len(opened.Resources), len(opened.Albums), len(opened.Locations), len(opened.Evidence))
	}
	evidence, err := Evidence(ctx, paths, search.Results[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(evidence.Evidence) == 0 {
		t.Fatal("expected asset evidence")
	}

	classified, err := Classify(ctx, paths, ClassifyOptions{
		All: true,
		Now: fixedClock("2026-05-28T10:15:00Z"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if classified.Processed != 2 || classified.MetadataClassified != 2 || classified.WaitingForLocalContent != 1 || classified.VisualObservationsWritten == 0 {
		t.Fatalf("classify result = processed %d metadata %d waiting %d visual %d", classified.Processed, classified.MetadataClassified, classified.WaitingForLocalContent, classified.VisualObservationsWritten)
	}
	observationSearch, err := Search(ctx, paths, SearchOptions{Query: "screenshot_candidate", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(observationSearch.Results) != 1 || observationSearch.Results[0].HitType != "observation" || observationSearch.Results[0].ObservationID == "" {
		t.Fatalf("observation search = %#v", observationSearch.Results)
	}
	opened, err = Open(ctx, paths, observationSearch.Results[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(opened.VisualObservations) == 0 {
		t.Fatal("expected visual observations on open")
	}
	observationEvidence, err := Evidence(ctx, paths, observationSearch.Results[0].ObservationID)
	if err != nil {
		t.Fatal(err)
	}
	if len(observationEvidence.Evidence) == 0 {
		t.Fatal("expected observation evidence")
	}
	status, err := Status(ctx, paths)
	if err != nil {
		t.Fatal(err)
	}
	if status.Summary == "" || status.LastImportAt == "" {
		t.Fatalf("status summary=%q last_import_at=%q", status.Summary, status.LastImportAt)
	}
	for _, want := range []string{
		"asset.media_type.image",
		"asset.with_location",
		"asset.with_observation",
		"resource.availability.local",
		"resource.availability.remote_needs_download",
		"observation.type.document_signal",
	} {
		if !hasStatusCount(status.Counts, want) {
			t.Fatalf("missing useful status count %q in %#v", want, status.Counts)
		}
	}

	provider.snapshot = fakeSnapshot(true, false)
	result, err = Crawl(ctx, paths, CrawlOptions{
		LibraryPath: libraryPath,
		Provider:    provider,
		Now:         fixedClock("2026-05-28T11:00:00Z"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.AssetsSeen != 1 || result.AssetsNew != 0 || result.AssetsChanged != 1 || result.AssetsUnchanged != 0 || result.PreviouslySeenMissing != 1 {
		t.Fatalf("second crawl delta = seen %d new %d changed %d unchanged %d missing %d", result.AssetsSeen, result.AssetsNew, result.AssetsChanged, result.AssetsUnchanged, result.PreviouslySeenMissing)
	}
}

func TestCrawlExpandsHomeInLibraryPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	libraryPath := filepath.Join(home, "Pictures", "Fixture Photos Library.photoslibrary")
	if err := mkdirLibrary(libraryPath); err != nil {
		t.Fatal(err)
	}
	provider := &pathRecordingProvider{snapshot: fakeSnapshot(false, false)}
	if _, err := Crawl(context.Background(), testPaths(t), CrawlOptions{
		LibraryPath: "~/Pictures/Fixture Photos Library.photoslibrary",
		Provider:    provider,
		Now:         fixedClock("2026-05-28T10:00:00Z"),
	}); err != nil {
		t.Fatal(err)
	}
	if provider.path != libraryPath {
		t.Fatalf("provider library path = %q, want %q", provider.path, libraryPath)
	}
}

func testPaths(t *testing.T) Paths {
	t.Helper()
	root := t.TempDir()
	return Paths{DataDir: root, Database: filepath.Join(root, "photos.sqlite")}
}

func mkdirLibrary(path string) error {
	return os.MkdirAll(path, 0o755)
}

func fixedClock(value string) func() time.Time {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		panic(err)
	}
	return func() time.Time { return parsed }
}

type fakeProvider struct {
	snapshot photos.LibrarySnapshot
}

func (f fakeProvider) Snapshot(context.Context, string) (photos.LibrarySnapshot, error) {
	return f.snapshot, nil
}

type pathRecordingProvider struct {
	path     string
	snapshot photos.LibrarySnapshot
}

func (p *pathRecordingProvider) Snapshot(_ context.Context, path string) (photos.LibrarySnapshot, error) {
	p.path = path
	return p.snapshot, nil
}

func fakeSnapshot(changed, includeSecond bool) photos.LibrarySnapshot {
	altitude := 12.5
	accuracy := 8.25
	snapshot := photos.LibrarySnapshot{
		Provider:            "fake",
		PhotosVersion:       "fixture",
		AuthorizationStatus: "authorized",
		Metadata: map[string]any{
			"fixture": true,
		},
		Assets: []photos.Asset{
			{
				LocalIdentifier:  "fixture-asset-1",
				MediaType:        "image",
				MediaSubtypes:    "0",
				CreationDate:     "2026-05-27T10:00:00Z",
				ModificationDate: pick(changed, "2026-05-28T10:30:00Z", "2026-05-27T10:05:00Z"),
				AddedDate:        "2026-05-27T10:01:00Z",
				TimezoneName:     "Europe/Amsterdam",
				Width:            4032,
				Height:           3024,
				Favorite:         changed,
				Location: &photos.Location{
					Latitude:           52.3676,
					Longitude:          4.9041,
					Altitude:           &altitude,
					HorizontalAccuracy: &accuracy,
				},
				Resources: []photos.Resource{
					{Type: "photo", UTI: "public.heic", OriginalFilename: "Screenshot Beach Fixture.heic", Availability: "remote", NeedsDownload: true},
				},
				Albums: []photos.AlbumMembership{
					{AlbumID: "fixture-album-1", AlbumTitle: "Beach", AlbumKind: "album:1:2"},
				},
			},
		},
	}
	if includeSecond {
		snapshot.Assets = append(snapshot.Assets, photos.Asset{
			LocalIdentifier:  "fixture-asset-2",
			MediaType:        "video",
			MediaSubtypes:    "0",
			CreationDate:     "2026-05-27T11:00:00Z",
			ModificationDate: "2026-05-27T11:05:00Z",
			AddedDate:        "2026-05-27T11:01:00Z",
			TimezoneName:     "Europe/Amsterdam",
			Width:            1920,
			Height:           1080,
			DurationSeconds:  7.5,
			Resources: []photos.Resource{
				{Type: "video", UTI: "public.mpeg-4", OriginalFilename: "kitchen-fixture.mp4", Availability: "local", AvailableLocally: true},
			},
			Albums: []photos.AlbumMembership{
				{AlbumID: "fixture-album-2", AlbumTitle: "Kitchen", AlbumKind: "album:1:2"},
			},
		})
	}
	return snapshot
}

func pick(changed bool, ifChanged, otherwise string) string {
	if changed {
		return ifChanged
	}
	return otherwise
}

func hasStatusCount(counts []control.Count, id string) bool {
	for _, count := range counts {
		if count.ID == id {
			return true
		}
	}
	return false
}
