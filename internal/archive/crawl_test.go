package archive

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
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

func TestCrawlResourceIdentityDoesNotDependOnEnumerationPosition(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	paths := testPaths(t)
	libraryPath := filepath.Join(t.TempDir(), "Fixture Photos Library.photoslibrary")
	if err := mkdirLibrary(libraryPath); err != nil {
		t.Fatal(err)
	}
	snapshot := fakeSnapshot(false, false)
	snapshot.Assets[0].Resources = append(snapshot.Assets[0].Resources, photos.Resource{
		SourceIdentifier: "fixture-thumbnail", Type: "thumbnail", UTI: "public.jpeg", OriginalFilename: "thumb.jpg", Availability: "local", AvailableLocally: true,
	})
	provider := fakeProvider{snapshot: snapshot}
	if _, err := Crawl(ctx, paths, CrawlOptions{LibraryPath: libraryPath, Provider: provider, Now: fixedClock("2026-07-18T09:00:00Z")}); err != nil {
		t.Fatal(err)
	}
	db, err := openArchiveStore(ctx, paths.Database)
	if err != nil {
		t.Fatal(err)
	}
	var thumbnailID string
	if err := db.DB().QueryRowContext(ctx, `select id from asset_resource where resource_type = 'thumbnail'`).Scan(&thumbnailID); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	snapshot.Assets[0].Resources = snapshot.Assets[0].Resources[1:]
	snapshot.Assets[0].Resources[0].OriginalFilename = "renamed-thumb.jpg"
	snapshot.Assets[0].Resources[0].UTI = "public.heic"
	provider.snapshot = snapshot
	if _, err := Crawl(ctx, paths, CrawlOptions{LibraryPath: libraryPath, Provider: provider, Now: fixedClock("2026-07-18T09:30:00Z")}); err != nil {
		t.Fatal(err)
	}
	db, err = openArchiveStore(ctx, paths.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var resources, sourceIdentifiers int
	var mergedThumbnailID, mergedFilename string
	if err := db.DB().QueryRowContext(ctx, `select count(*), count(distinct source_identifier) from asset_resource`).Scan(&resources, &sourceIdentifiers); err != nil {
		t.Fatal(err)
	}
	if err := db.DB().QueryRowContext(ctx, `select id, original_filename from asset_resource where resource_type = 'thumbnail'`).Scan(&mergedThumbnailID, &mergedFilename); err != nil {
		t.Fatal(err)
	}
	if resources != 2 || sourceIdentifiers != 2 || mergedThumbnailID != thumbnailID || mergedFilename != "renamed-thumb.jpg" {
		t.Fatalf("resource merge = rows %d identities %d thumbnail %q/%q filename %q", resources, sourceIdentifiers, mergedThumbnailID, thumbnailID, mergedFilename)
	}
}

func TestCrawlRejectsMissingOrDuplicateResourceSourceIdentifiers(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	libraryPath := filepath.Join(t.TempDir(), "Fixture Photos Library.photoslibrary")
	if err := mkdirLibrary(libraryPath); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name      string
		resources []photos.Resource
		want      string
	}{
		{name: "missing", resources: []photos.Resource{{Type: "photo"}}, want: "missing a stable source identifier"},
		{name: "duplicate", resources: []photos.Resource{{SourceIdentifier: "same", Type: "photo"}, {SourceIdentifier: "same", Type: "thumbnail"}}, want: "duplicate resource source identifier"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := fakeSnapshot(false, false)
			snapshot.Assets[0].Resources = test.resources
			paths := testPaths(t)
			_, err := Crawl(ctx, paths, CrawlOptions{LibraryPath: libraryPath, Provider: fakeProvider{snapshot: snapshot}, Now: fixedClock("2026-07-18T09:00:00Z")})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("crawl error = %v, want %q", err, test.want)
			}
			if _, statErr := os.Stat(paths.Database); !os.IsNotExist(statErr) {
				t.Fatalf("invalid snapshot created database: %v", statErr)
			}
		})
	}
}

func TestCrawlAppliesOnlyExplicitAssetTombstonesAndRestoresLiveRows(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	paths := testPaths(t)
	libraryPath := filepath.Join(t.TempDir(), "Fixture Photos Library.photoslibrary")
	if err := mkdirLibrary(libraryPath); err != nil {
		t.Fatal(err)
	}
	provider := fakeProvider{snapshot: fakeSnapshot(false, true)}
	if _, err := Crawl(ctx, paths, CrawlOptions{LibraryPath: libraryPath, Provider: provider, Now: fixedClock("2026-07-18T10:00:00Z")}); err != nil {
		t.Fatal(err)
	}
	if _, err := Classify(ctx, paths, ClassifyOptions{All: true, Now: fixedClock("2026-07-18T10:05:00Z")}); err != nil {
		t.Fatal(err)
	}
	partial := fakeSnapshot(true, false)
	partial.Assets[0].Resources = nil
	partial.Assets[0].Albums = nil
	provider.snapshot = partial
	if _, err := Crawl(ctx, paths, CrawlOptions{LibraryPath: libraryPath, Provider: provider, Now: fixedClock("2026-07-18T10:10:00Z")}); err != nil {
		t.Fatal(err)
	}
	mergedSearch, err := Search(ctx, paths, SearchOptions{Query: "beach", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(mergedSearch.Results) != 1 {
		t.Fatalf("partial merge dropped retained album from search: %#v", mergedSearch.Results)
	}
	db, err := openArchiveStore(ctx, paths.Database)
	if err != nil {
		t.Fatal(err)
	}
	var deletedID, missingID string
	if err := db.DB().QueryRowContext(ctx, `select id from asset where local_identifier = 'fixture-asset-1'`).Scan(&deletedID); err != nil {
		db.Close()
		t.Fatal(err)
	}
	var retainedNeedsDownload int
	if err := db.DB().QueryRowContext(ctx, `select needs_download from classification_queue where asset_id = ?`, deletedID).Scan(&retainedNeedsDownload); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if retainedNeedsDownload != 1 {
		db.Close()
		t.Fatalf("partial merge dropped retained resource queue state: needs_download=%d", retainedNeedsDownload)
	}
	if err := db.DB().QueryRowContext(ctx, `select id from asset where local_identifier = 'fixture-asset-2'`).Scan(&missingID); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.DB().ExecContext(ctx, `
insert into asset_resource(id, asset_id, source_identifier, resource_type, uti, original_filename, local_path, file_size, sha256, available_locally, needs_download)
values ('fixture-thumbnail', ?, 'fixture-thumbnail-source', 'thumbnail', 'public.jpeg', 'thumb.jpg', '/tmp/thumb.jpg', 12, 'thumb-hash', 1, 0)
`, deletedID); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	tombstone := fakeSnapshot(false, false)
	tombstone.Assets[0].DeletedAt = ""
	tombstone.Assets[0].DeletionSource = "fixture-explicit-feed"
	tombstone.Assets[0].DeletionReason = "trashed_in_photos_library"
	tombstone.Assets[0].Metadata = map[string]any{"deletion_timestamp_source": "crawl_observed_at"}
	provider.snapshot = tombstone
	result, err := Crawl(ctx, paths, CrawlOptions{LibraryPath: libraryPath, Provider: provider, Now: fixedClock("2026-07-18T11:00:00Z")})
	if err != nil {
		t.Fatal(err)
	}
	if result.AssetsDeleted != 1 || result.AssetsRestored != 0 || result.PreviouslySeenMissing != 1 {
		t.Fatalf("tombstone crawl = deleted %d restored %d missing %d", result.AssetsDeleted, result.AssetsRestored, result.PreviouslySeenMissing)
	}
	db, err = openArchiveStore(ctx, paths.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var deletedAt, source, reason string
	if err := db.DB().QueryRowContext(ctx, `select deleted_at, deletion_source, deletion_reason from asset where id = ?`, deletedID).Scan(&deletedAt, &source, &reason); err != nil {
		t.Fatal(err)
	}
	if deletedAt != "2026-07-18T11:00:00Z" || source != "fixture-explicit-feed" || reason != "trashed_in_photos_library" {
		t.Fatalf("asset tombstone = %q %q %q", deletedAt, source, reason)
	}
	var timestampSource string
	if err := db.DB().QueryRowContext(ctx, `select json_extract(value_json, '$.deletion_timestamp_source') from evidence_ref where asset_id = ? and evidence_kind = 'asset_tombstone'`, deletedID).Scan(&timestampSource); err != nil {
		t.Fatal(err)
	}
	if timestampSource != "crawl_observed_at" {
		t.Fatalf("tombstone timestamp source = %q", timestampSource)
	}
	var tombstonedResources int
	if err := db.DB().QueryRowContext(ctx, `select count(*) from asset_resource where asset_id = ? and deleted_at is not null and deletion_reason = 'parent_asset_deleted'`, deletedID).Scan(&tombstonedResources); err != nil {
		t.Fatal(err)
	}
	if tombstonedResources != 2 {
		t.Fatalf("tombstoned resources = %d, want 2", tombstonedResources)
	}
	var missingDeleted sql.NullString
	var missingResources int
	if err := db.DB().QueryRowContext(ctx, `select deleted_at from asset where id = ?`, missingID).Scan(&missingDeleted); err != nil {
		t.Fatal(err)
	}
	if err := db.DB().QueryRowContext(ctx, `select count(*) from asset_resource where asset_id = ? and deleted_at is null`, missingID).Scan(&missingResources); err != nil {
		t.Fatal(err)
	}
	if missingDeleted.Valid || missingResources != 1 {
		t.Fatalf("not-seen asset changed: deleted=%v live resources=%d", missingDeleted, missingResources)
	}
	var observations int
	if err := db.DB().QueryRowContext(ctx, `select count(*) from visual_observation where asset_id = ?`, deletedID).Scan(&observations); err != nil {
		t.Fatal(err)
	}
	if observations == 0 {
		t.Fatal("tombstone erased archived observations")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	tombstone.Assets[0].DeletedAt = "2026-07-18T11:30:00Z"
	tombstone.Assets[0].DeletionReason = "later_duplicate_delete"
	provider.snapshot = tombstone
	result, err = Crawl(ctx, paths, CrawlOptions{LibraryPath: libraryPath, Provider: provider, Now: fixedClock("2026-07-18T11:45:00Z")})
	if err != nil {
		t.Fatal(err)
	}
	if result.AssetsDeleted != 0 {
		t.Fatalf("repeat tombstone counted as a new deletion: %+v", result)
	}
	db, err = openArchiveStore(ctx, paths.Database)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.DB().QueryRowContext(ctx, `select deleted_at, deletion_reason from asset where id = ?`, deletedID).Scan(&deletedAt, &reason); err != nil {
		db.Close()
		t.Fatal(err)
	}
	var thumbnailDeletedAt string
	if err := db.DB().QueryRowContext(ctx, `select deleted_at from asset_resource where id = 'fixture-thumbnail'`).Scan(&thumbnailDeletedAt); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if deletedAt != "2026-07-18T11:00:00Z" || reason != "trashed_in_photos_library" || thumbnailDeletedAt != "2026-07-18T11:00:00Z" {
		db.Close()
		t.Fatalf("repeat tombstone replaced first evidence: asset=%q reason=%q thumbnail=%q", deletedAt, reason, thumbnailDeletedAt)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	search, err := Search(ctx, paths, SearchOptions{Query: "beach", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(search.Results) != 0 {
		t.Fatalf("tombstoned asset remained searchable: %#v", search.Results)
	}

	provider.snapshot = fakeSnapshot(true, false)
	result, err = Crawl(ctx, paths, CrawlOptions{LibraryPath: libraryPath, Provider: provider, Now: fixedClock("2026-07-18T12:00:00Z")})
	if err != nil {
		t.Fatal(err)
	}
	if result.AssetsRestored != 1 || result.AssetsDeleted != 0 {
		t.Fatalf("restore crawl = restored %d deleted %d", result.AssetsRestored, result.AssetsDeleted)
	}
	db, err = openArchiveStore(ctx, paths.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var restoredDeleted sql.NullString
	if err := db.DB().QueryRowContext(ctx, `select deleted_at from asset where id = ?`, deletedID).Scan(&restoredDeleted); err != nil {
		t.Fatal(err)
	}
	var liveResources, retainedTombstones int
	if err := db.DB().QueryRowContext(ctx, `select count(*) from asset_resource where asset_id = ? and deleted_at is null`, deletedID).Scan(&liveResources); err != nil {
		t.Fatal(err)
	}
	if err := db.DB().QueryRowContext(ctx, `select count(*) from asset_resource where asset_id = ? and deleted_at is not null`, deletedID).Scan(&retainedTombstones); err != nil {
		t.Fatal(err)
	}
	if restoredDeleted.Valid || liveResources != 1 || retainedTombstones != 1 {
		t.Fatalf("restored asset = deleted %v live resources %d retained tombstones %d", restoredDeleted, liveResources, retainedTombstones)
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
					{SourceIdentifier: "fixture-photo", Type: "photo", UTI: "public.heic", OriginalFilename: "Screenshot Beach Fixture.heic", Availability: "remote", NeedsDownload: true},
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
				{SourceIdentifier: "fixture-video", Type: "video", UTI: "public.mpeg-4", OriginalFilename: "kitchen-fixture.mp4", Availability: "local", AvailableLocally: true},
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
