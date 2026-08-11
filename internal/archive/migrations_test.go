package archive

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openclaw/crawlkit/store"
	"github.com/openclaw/photoscrawl/internal/photos"
)

const schemaV1Fixture = `
create table source_library (
  id text primary key, library_path text not null, snapshot_path text not null,
  snapshot_created_at text not null, photos_version text not null, metadata_json text not null
);
create table crawl_snapshot (
  id text primary key, source_library_id text not null references source_library(id),
  started_at text not null, completed_at text not null, provider text not null,
  asset_count integer not null, resource_count integer not null,
  album_membership_count integer not null, location_count integer not null,
  metadata_json text not null
);
create table asset (
  id text primary key, local_identifier text not null unique, media_type text not null,
  media_subtypes text not null, creation_date text not null, modification_date text not null,
  added_date text not null, timezone_name text not null, width integer not null,
  height integer not null, duration_seconds real not null, favorite integer not null,
  hidden integer not null, burst_identifier text not null, represents_burst integer not null,
  source_library_id text not null references source_library(id), metadata_json text not null
);
create table asset_resource (
  id text primary key, asset_id text not null references asset(id), resource_type text not null,
  uti text not null, original_filename text not null, local_path text not null,
  file_size integer not null, sha256 text not null, available_locally integer not null,
  needs_download integer not null
);
`

func TestSchemaV2MigrationPreservesV1AssetRows(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "photos.sqlite")
	legacy, err := store.Open(ctx, store.Options{Path: dbPath, Schema: schemaV1Fixture, SchemaVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.DB().ExecContext(ctx, `
insert into source_library values ('library-1', '/fixture', 'snapshot', '2026-07-18T00:00:00Z', 'fixture', '{}');
insert into asset values ('asset-1', 'local-1', 'image', '', '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z', '', '', 10, 20, 0, 0, 0, '', 0, 'library-1', '{"kept":true}');
insert into asset_resource values ('resource-1', 'asset-1', 'thumbnail', 'public.jpeg', 'thumb.jpg', '/tmp/thumb.jpg', 42, 'hash', 1, 0);
`); err != nil {
		legacy.Close()
		t.Fatal(err)
	}
	var assetRowID, resourceRowID int64
	if err := legacy.DB().QueryRowContext(ctx, `select rowid from asset where id = 'asset-1'`).Scan(&assetRowID); err != nil {
		legacy.Close()
		t.Fatal(err)
	}
	if err := legacy.DB().QueryRowContext(ctx, `select rowid from asset_resource where id = 'resource-1'`).Scan(&resourceRowID); err != nil {
		legacy.Close()
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := openArchiveReadOnly(ctx, dbPath); err == nil || !strings.Contains(err.Error(), "requires upgrade") {
		t.Fatalf("read-only v1 open error = %v, want upgrade-required error", err)
	}
	unchanged, err := store.OpenReadOnly(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	unchangedVersion, err := unchanged.SchemaVersion(ctx)
	if err != nil {
		unchanged.Close()
		t.Fatal(err)
	}
	if err := unchanged.Close(); err != nil {
		t.Fatal(err)
	}
	if unchangedVersion != 1 {
		t.Fatalf("read-only open changed schema version to %d", unchangedVersion)
	}

	migrated, err := openArchiveStore(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	version, err := migrated.SchemaVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if version != SchemaVersion {
		t.Fatalf("schema version = %d, want %d", version, SchemaVersion)
	}
	var gotAssetRowID, gotResourceRowID int64
	var metadata string
	var assetDeleted, resourceDeleted any
	if err := migrated.DB().QueryRowContext(ctx, `select rowid, metadata_json, deleted_at from asset where id = 'asset-1'`).Scan(&gotAssetRowID, &metadata, &assetDeleted); err != nil {
		t.Fatal(err)
	}
	if err := migrated.DB().QueryRowContext(ctx, `select rowid, deleted_at from asset_resource where id = 'resource-1'`).Scan(&gotResourceRowID, &resourceDeleted); err != nil {
		t.Fatal(err)
	}
	if gotAssetRowID != assetRowID || gotResourceRowID != resourceRowID || metadata != `{"kept":true}` {
		t.Fatalf("migration changed rows: asset rowid %d/%d resource rowid %d/%d metadata %q", gotAssetRowID, assetRowID, gotResourceRowID, resourceRowID, metadata)
	}
	if assetDeleted != nil || resourceDeleted != nil {
		t.Fatalf("migration invented tombstones: asset=%v resource=%v", assetDeleted, resourceDeleted)
	}
}

func TestSchemaV2CrawlAdoptsExactLegacyResourceRows(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	paths := testPaths(t)
	libraryPath := filepath.Join(t.TempDir(), "Fixture Photos Library.photoslibrary")
	if err := os.MkdirAll(libraryPath, 0o755); err != nil {
		t.Fatal(err)
	}
	sourceID := stableID("source_library", libraryPath)
	assetID := stableID("asset", sourceID, "fixture-asset-1")
	resource := photos.Resource{Type: "photo", UTI: "public.heic", OriginalFilename: "same.heic", Availability: "local", AvailableLocally: true}
	legacyIDs := []string{
		stableID("asset_resource", assetID, fmt.Sprintf("%06d", 0), resource.Type, resource.UTI, resource.OriginalFilename),
		stableID("asset_resource", assetID, fmt.Sprintf("%06d", 1), resource.Type, resource.UTI, resource.OriginalFilename),
	}
	legacy, err := store.Open(ctx, store.Options{Path: paths.Database, Schema: schemaV1Fixture, SchemaVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	statements := []struct {
		query string
		args  []any
	}{
		{`insert into source_library values (?, ?, 'snapshot', '2026-07-18T00:00:00Z', 'fixture', '{}')`, []any{sourceID, libraryPath}},
		{`insert into asset values (?, 'fixture-asset-1', 'image', '', '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z', '', '', 10, 20, 0, 0, 0, '', 0, ?, '{}')`, []any{assetID, sourceID}},
		{`insert into asset_resource values (?, ?, 'photo', 'public.heic', 'same.heic', '/tmp/first.heic', 10, 'first-hash', 1, 0)`, []any{legacyIDs[0], assetID}},
		{`insert into asset_resource values (?, ?, 'photo', 'public.heic', 'same.heic', '/tmp/second.heic', 20, 'second-hash', 1, 0)`, []any{legacyIDs[1], assetID}},
	}
	for _, statement := range statements {
		if _, err := legacy.DB().ExecContext(ctx, statement.query, statement.args...); err != nil {
			legacy.Close()
			t.Fatal(err)
		}
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	snapshot := fakeSnapshot(false, false)
	snapshot.Assets[0].Resources = []photos.Resource{
		{SourceIdentifier: "canonical-first", Type: resource.Type, UTI: resource.UTI, OriginalFilename: resource.OriginalFilename, LocalPath: "/tmp/first.heic", StableHash: "first-hash", Availability: "local", AvailableLocally: true},
		{SourceIdentifier: "canonical-second", Type: resource.Type, UTI: resource.UTI, OriginalFilename: resource.OriginalFilename, LocalPath: "/tmp/second.heic", StableHash: "second-hash", Availability: "local", AvailableLocally: true},
	}
	if _, err := Crawl(ctx, paths, CrawlOptions{LibraryPath: libraryPath, Provider: fakeProvider{snapshot: snapshot}, Now: fixedClock("2026-07-18T12:00:00Z")}); err != nil {
		t.Fatal(err)
	}
	migrated, err := openArchiveReadOnly(ctx, paths.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	for index, sourceIdentifier := range []string{"canonical-first", "canonical-second"} {
		var gotID, gotHash string
		if err := migrated.DB().QueryRowContext(ctx, `select id, sha256 from asset_resource where source_identifier = ?`, sourceIdentifier).Scan(&gotID, &gotHash); err != nil {
			t.Fatal(err)
		}
		if gotID != legacyIDs[index] || gotHash != []string{"first-hash", "second-hash"}[index] {
			t.Fatalf("resource %s adopted id/hash %q/%q, want %q", sourceIdentifier, gotID, gotHash, legacyIDs[index])
		}
	}
	var resourceRows int
	if err := migrated.DB().QueryRowContext(ctx, `select count(*) from asset_resource`).Scan(&resourceRows); err != nil {
		t.Fatal(err)
	}
	if resourceRows != 2 {
		t.Fatalf("resource rows = %d, want 2", resourceRows)
	}
}
