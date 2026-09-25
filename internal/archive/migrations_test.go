package archive

import (
	"context"
	"database/sql"
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

func TestOpenArchiveStoreConfiguresWriterWait(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "photos.sqlite")
	db, err := openArchiveStore(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var timeout int
	if err := db.DB().QueryRowContext(ctx, `pragma busy_timeout`).Scan(&timeout); err != nil {
		t.Fatal(err)
	}
	if timeout != archiveBusyTimeoutMillis {
		t.Fatalf("busy timeout = %d, want %d", timeout, archiveBusyTimeoutMillis)
	}
}

func TestSchemaMigrationPreservesV1AssetRows(t *testing.T) {
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

func TestSchemaV3MigrationAddsAlbumFolderPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "photos.sqlite")
	schemaV2 := strings.Replace(Schema, ",\n  folder_path text not null default ''", "", 1)
	legacy, err := store.Open(ctx, store.Options{Path: dbPath, Schema: schemaV2, SchemaVersion: 2})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.DB().ExecContext(ctx, `
insert into source_library values ('library-1', '/fixture', 'snapshot', '2026-07-18T00:00:00Z', 'fixture', '{}');
insert into asset values ('asset-1', 'local-1', 'image', '', '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z', '', '', 10, 20, 0, 0, 0, '', 0, 'library-1', '{"kept":true}', null, null, null);
insert into album_membership(id, asset_id, album_id, album_title, album_kind) values ('membership-1', 'asset-1', 'album-1', 'Trip', 'album:1:2');
`); err != nil {
		legacy.Close()
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	migrated, err := openArchiveStore(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	var folderPath string
	if err := migrated.DB().QueryRowContext(ctx, `select folder_path from album_membership where id = 'membership-1'`).Scan(&folderPath); err != nil {
		t.Fatal(err)
	}
	if folderPath != "" {
		t.Fatalf("migrated folder_path = %q", folderPath)
	}
}

func TestFaceSignalMigrationIsIdempotent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "photos.sqlite")
	legacySchema := strings.Replace(Schema, `
  person_uuid text,
  person_kind text,
  confidence real not null,
  quality real,
  blur_score real,
  eyes_closed integer,
  smile integer,`, `
  confidence real not null,`, 1)
	legacy, err := store.Open(ctx, store.Options{Path: dbPath, Schema: legacySchema, SchemaVersion: 3})
	if err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	migrated, err := openArchiveStore(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	if err := migrateArchiveSchema(ctx, migrated); err != nil {
		t.Fatal(err)
	}
	columns, err := archiveTableColumns(ctx, migrated.DB(), "face_observation")
	if err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{"person_uuid", "person_kind", "quality", "blur_score", "eyes_closed", "smile"} {
		if !columns[column] {
			t.Fatalf("face_observation missing migrated column %q", column)
		}
	}
}

func archiveTableColumns(ctx context.Context, db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, "pragma table_info("+table+")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns := map[string]bool{}
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return nil, err
		}
		columns[name] = true
	}
	return columns, rows.Err()
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

func TestSchemaV5MigrationAddsVisualLabelIndex(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "photos.sqlite")
	legacySchema := strings.Replace(Schema, "create index if not exists visual_type_label_idx on visual_observation(observation_type, label collate nocase);\n", "", 1)
	if legacySchema == Schema {
		t.Fatal("fixture did not remove the visual label index from the schema")
	}
	legacy, err := store.Open(ctx, store.Options{Path: dbPath, Schema: legacySchema, SchemaVersion: 4})
	if err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	migrated, err := openArchiveStore(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	var count int
	if err := migrated.DB().QueryRowContext(ctx, `select count(*) from sqlite_master where type = 'index' and name = 'visual_type_label_idx'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("visual_type_label_idx count = %d, want 1", count)
	}
	version, err := migrated.SchemaVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if version != SchemaVersion {
		t.Fatalf("schema version = %d, want %d", version, SchemaVersion)
	}
}

func TestSchemaV6MigrationPreservesFTSRowsAndAddsQueryIndexes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "photos.sqlite")
	v5Schema := strings.Replace(Schema, `
  updated_at text not null,
  input_fingerprint text not null default '',
  claim_owner text not null default '',
  claim_expires_at text
`, `
  updated_at text not null
`, 1)
	v5Schema = strings.Replace(v5Schema, `
create table if not exists observation_fts_rowid (
  fts_rowid integer primary key,
  observation_id text not null
);
`, "", 1)
	for _, statement := range []string{
		"create index if not exists asset_source_library_idx on asset(source_library_id, id);\n",
		"create index if not exists evidence_ref_asset_kind_source_idx on evidence_ref(asset_id, evidence_kind, source);\n",
		"create index if not exists observation_term_observation_idx on observation_term(observation_id);\n",
		"create index if not exists observation_fts_observation_idx on observation_fts_rowid(observation_id);\n",
	} {
		v5Schema = strings.Replace(v5Schema, statement, "", 1)
	}
	if v5Schema == Schema {
		t.Fatal("fixture did not remove schema v6 objects")
	}
	legacy, err := store.Open(ctx, store.Options{Path: dbPath, Schema: v5Schema, SchemaVersion: 5})
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`insert into source_library values ('lib', '/fixture', 'snapshot', '2026-09-25T00:00:00Z', 'fixture', '{}')`,
		`insert into asset(id,local_identifier,media_type,media_subtypes,creation_date,modification_date,added_date,timezone_name,width,height,duration_seconds,favorite,hidden,burst_identifier,represents_burst,source_library_id,metadata_json) values('asset:one','ONE/L0/001','image','0','2026-09-25T00:00:00Z','','','UTC',10,10,0,0,0,'',0,'lib','{}')`,
		`insert into crawl_snapshot values ('snapshot','lib','2026-09-25T00:00:00Z','2026-09-25T00:00:00Z','photokit',1,0,0,0,'{}')`,
		`insert into evidence_ref values ('evidence:one','asset:one','asset_metadata','photokit','asset:ONE/L0/001','{}')`,
		`insert into observation_term values ('term:one','asset:one','observation:one','beach','scene','fixture','fixture')`,
		`insert into classification_queue values ('queue:one','asset:one','lib','pending','fixture',0,'2026-09-25T00:00:00Z')`,
		`insert into observation_fts(rowid,id,asset_id,title,body) values (41,'observation:one','asset:one','Beach','beach scene')`,
		`insert into observation_fts(rowid,id,asset_id,title,body) values (99,'observation:two','asset:one','Dinner','shared table')`,
	} {
		if _, err := legacy.DB().ExecContext(ctx, statement); err != nil {
			legacy.Close()
			t.Fatal(err)
		}
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
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
	if version != 6 {
		t.Fatalf("schema version = %d, want 6", version)
	}
	var rows, mapped, mismatched int
	if err := migrated.DB().QueryRowContext(ctx, `select count(*) from observation_fts`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if err := migrated.DB().QueryRowContext(ctx, `select count(*) from observation_fts_rowid`).Scan(&mapped); err != nil {
		t.Fatal(err)
	}
	if err := migrated.DB().QueryRowContext(ctx, `
select count(*)
from observation_fts f
left join observation_fts_rowid m on m.fts_rowid = f.rowid and m.observation_id = f.id
where m.fts_rowid is null
`).Scan(&mismatched); err != nil {
		t.Fatal(err)
	}
	if rows != 2 || mapped != rows || mismatched != 0 {
		t.Fatalf("migrated FTS rows=%d mapped=%d mismatched=%d", rows, mapped, mismatched)
	}
	var title, body string
	if err := migrated.DB().QueryRowContext(ctx, `select title, body from observation_fts where rowid = 99`).Scan(&title, &body); err != nil {
		t.Fatal(err)
	}
	if title != "Dinner" || body != "shared table" {
		t.Fatalf("migrated FTS content = %q / %q", title, body)
	}
	columns, err := archiveTableColumns(ctx, migrated.DB(), "classification_queue")
	if err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{"input_fingerprint", "claim_owner", "claim_expires_at"} {
		if !columns[column] {
			t.Fatalf("classification_queue missing %q", column)
		}
	}
	plans := []struct {
		name, query string
		indexes     []string
	}{
		{
			"identity evidence",
			`select e.asset_id, e.source, e.pointer, e.evidence_kind
from evidence_ref e join asset a on a.id = e.asset_id
where a.source_library_id = 'lib' and e.evidence_kind in ('asset_metadata', 'asset_tombstone')
  and exists (select 1 from crawl_snapshot s where s.source_library_id = a.source_library_id and s.provider = e.source)`,
			[]string{"asset_source_library_idx", "evidence_ref_asset_kind_source_idx"},
		},
		{"observation cleanup", `select id from observation_term where observation_id = 'observation:one'`, []string{"observation_term_observation_idx"}},
		{"FTS row delete", `delete from observation_fts where rowid in (select fts_rowid from observation_fts_rowid where observation_id = 'observation:one')`, []string{"observation_fts_observation_idx"}},
	}
	for _, test := range plans {
		plan, err := explainQueryPlan(ctx, migrated.DB(), test.query)
		if err != nil {
			t.Fatal(err)
		}
		for _, index := range test.indexes {
			if !strings.Contains(plan, index) {
				t.Errorf("%s plan = %q, want %s", test.name, plan, index)
			}
		}
	}
}

func explainQueryPlan(ctx context.Context, db *sql.DB, query string) (string, error) {
	rows, err := db.QueryContext(ctx, "explain query plan "+query)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var details []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			return "", err
		}
		details = append(details, detail)
	}
	return strings.Join(details, " | "), rows.Err()
}

func TestPopulatedSchemaV3ArchiveUpgradeAndSnapshotRecovery(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "photos.sqlite")
	v3Schema := strings.Replace(Schema, `
  person_uuid text,
  person_kind text,
  confidence real not null,
  quality real,
  blur_score real,
  eyes_closed integer,
  smile integer,`, `
  confidence real not null,`, 1)
	v3Schema = strings.Replace(v3Schema, "create index if not exists visual_type_label_idx on visual_observation(observation_type, label collate nocase);\n", "", 1)
	legacy, err := store.Open(ctx, store.Options{Path: dbPath, Schema: v3Schema, SchemaVersion: 3})
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`insert into source_library values ('lib', '/fixture', 'snapshot', '2025-01-01T00:00:00Z', 'test', '{}')`,
		`insert into asset(id,local_identifier,media_type,media_subtypes,creation_date,modification_date,added_date,timezone_name,width,height,duration_seconds,favorite,hidden,burst_identifier,represents_burst,source_library_id,metadata_json) values('asset:one','ONE/L0/001','image','0','2025-01-01T00:00:00Z','','','UTC',10,10,0,1,0,'',0,'lib','{}')`,
		`insert into evidence_ref(id,asset_id,evidence_kind,source,pointer,value_json) values('evidence:face','asset:one','face_observation','photos_library_db','ZDETECTEDFACE:1','{}')`,
		`insert into face_observation(id,asset_id,face_local_id,person_label,confidence,bounding_box_json,source,evidence_id) values('face:one','asset:one','face-1','Alex',1,'{}','photos_library_db','evidence:face')`,
		`insert into model_observation values('model:one','asset:one','apple_quality_scores','','{"overall_aesthetic":0.8}',1,'photos_library_db','apple','v1','evidence:face')`,
		`insert into visual_observation values('visual:one','asset:one','apple_label','Beach',1,'{}','photos_search_index','apple','evidence:face')`,
	} {
		if _, err := legacy.DB().ExecContext(ctx, statement); err != nil {
			legacy.Close()
			t.Fatal(err)
		}
	}
	// Include committed WAL rows in the recovery snapshot before closing the writer.
	backup, cleanup, err := photos.CopySQLite(ctx, dbPath, "photoscrawl-migration-backup-")
	if err != nil {
		legacy.Close()
		t.Fatal(err)
	}
	defer cleanup()
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	upgraded, err := openArchiveStore(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	version, err := upgraded.SchemaVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if version != SchemaVersion {
		t.Fatalf("schema version = %d, want %d", version, SchemaVersion)
	}
	for table, want := range map[string]int{"asset": 1, "evidence_ref": 1, "face_observation": 1, "model_observation": 1, "visual_observation": 1} {
		assertCount(t, upgraded.DB(), "select count(*) from "+table, want)
	}
	var label string
	var quality, eyesClosed sql.NullFloat64
	if err := upgraded.DB().QueryRowContext(ctx, `select person_label, quality, eyes_closed from face_observation where id = 'face:one'`).Scan(&label, &quality, &eyesClosed); err != nil {
		t.Fatal(err)
	}
	if label != "Alex" || quality.Valid || eyesClosed.Valid {
		t.Fatalf("upgraded face = %q quality=%v eyes=%v, want the original label and null new signals", label, quality, eyesClosed)
	}
	assertCount(t, upgraded.DB(), `select count(*) from sqlite_master where type = 'index' and name = 'visual_type_label_idx'`, 1)

	// Recover to a fresh filename: never mix a backup with an upgraded archive's WAL.
	restoredPath := filepath.Join(t.TempDir(), "restored.sqlite")
	contents, err := os.ReadFile(backup)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(restoredPath, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	restored, err := store.OpenReadOnly(ctx, restoredPath)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	version, err = restored.SchemaVersion(ctx)
	if err != nil || version != 3 {
		t.Fatalf("restored version = %d, %v; want 3", version, err)
	}
	for _, table := range []string{"asset", "evidence_ref", "face_observation", "model_observation", "visual_observation"} {
		assertCount(t, restored.DB(), "select count(*) from "+table, 1)
	}
	assertCount(t, restored.DB(), `select count(*) from pragma_table_info('face_observation') where name in ('person_uuid','person_kind','quality','blur_score','eyes_closed','smile')`, 0)
	assertCount(t, restored.DB(), `select count(*) from sqlite_master where name = 'visual_type_label_idx'`, 0)
	if err := restored.DB().QueryRowContext(ctx, `select person_label from face_observation where id = 'face:one'`).Scan(&label); err != nil || label != "Alex" {
		t.Fatalf("restored face = %q, %v; want Alex", label, err)
	}
}
