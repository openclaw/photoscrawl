package archive

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/openclaw/crawlkit/store"
)

func TestNormalizeAssetLocalIdentifier(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "photokit identifier",
			input: "D4AA1B8D-FABB-428A-A001-1E285D0CC1EC/L0/001",
			want:  "D4AA1B8D-FABB-428A-A001-1E285D0CC1EC",
		},
		{
			name:  "sqlite snapshot uuid",
			input: "D4AA1B8D-FABB-428A-A001-1E285D0CC1EC",
			want:  "D4AA1B8D-FABB-428A-A001-1E285D0CC1EC",
		},
		{
			name:  "trim whitespace",
			input: "  D4AA1B8D-FABB-428A-A001-1E285D0CC1EC/L0/001  ",
			want:  "D4AA1B8D-FABB-428A-A001-1E285D0CC1EC",
		},
		{
			name:  "empty",
			input: " ",
			want:  "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeAssetLocalIdentifier(tt.input); got != tt.want {
				t.Fatalf("normalizeAssetLocalIdentifier(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestAppleSearchCategoryMapping(t *testing.T) {
	person := appleSearchCategoryForID(1300)
	if person.Name != "PERSON" || person.SemanticKind != "apple_person" {
		t.Fatalf("person category = %#v", person)
	}
	label := appleSearchCategoryForID(1500)
	if label.Name != "LABEL" || label.SemanticKind != "apple_label" {
		t.Fatalf("label category = %#v", label)
	}
	unknown := appleSearchCategoryForID(9999)
	if unknown.Name != "UNKNOWN" || unknown.SemanticKind != "apple_search_category_9999" {
		t.Fatalf("unknown category = %#v", unknown)
	}
}

func TestPSIIntsToUUID(t *testing.T) {
	got := psiIntsToUUID(3551352356587034258, -5010834848571013202)
	want := "92D69D06-27F0-4831-AEC7-C6F640F075BA"
	if got != want {
		t.Fatalf("psiIntsToUUID() = %q, want %q", got, want)
	}
}

func TestStripAppleSearchString(t *testing.T) {
	got := stripAppleSearchString(" Antique Car\x00 ")
	if got != "Antique Car" {
		t.Fatalf("stripAppleSearchString() = %q", got)
	}
}

func TestDecodeLeoLexemeIDs(t *testing.T) {
	got := decodeLeoLexemeIDs([]byte{1, 0, 0, 0, 42, 0, 0, 0, 255})
	if len(got) != 2 || got[0] != 1 || got[1] != 42 {
		t.Fatalf("decodeLeoLexemeIDs() = %#v", got)
	}
}

func TestCopySQLiteCreatesConsistentSnapshotWhileSourceIsLive(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	root := t.TempDir()
	sourcePath := filepath.Join(root, "live.sqlite")
	db, err := sql.Open("sqlite", "file:"+sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, `
pragma journal_mode = wal;
pragma wal_autocheckpoint = 0;
create table paired(batch integer not null, side integer not null, payload blob, primary key(batch, side));
with recursive batches(value) as (select 1 union all select value + 1 from batches where value < 2000)
insert into paired(batch, side, payload)
select value, side, zeroblob(2048) from batches cross join (select 1 as side union all select 2);
`); err != nil {
		t.Fatal(err)
	}

	writerDone := make(chan error, 1)
	writerStarted := make(chan struct{})
	go func() {
		defer close(writerDone)
		for batch := 2001; batch <= 2100; batch++ {
			tx, err := db.BeginTx(ctx, nil)
			if err == nil {
				_, err = tx.ExecContext(ctx, `insert into paired(batch, side, payload) values (?, 1, zeroblob(2048)), (?, 2, zeroblob(2048))`, batch, batch)
			}
			if err == nil {
				err = tx.Commit()
			} else if tx != nil {
				_ = tx.Rollback()
			}
			if err != nil {
				writerDone <- err
				return
			}
			if batch == 2001 {
				close(writerStarted)
			}
			time.Sleep(100 * time.Microsecond)
		}
		writerDone <- nil
	}()
	<-writerStarted

	snapshotPath, cleanup, copyErr := copySQLite(ctx, sourcePath, "photoscrawl-snapshot-test-")
	if writerErr := <-writerDone; writerErr != nil {
		t.Fatal(writerErr)
	}
	if copyErr != nil {
		t.Fatal(copyErr)
	}
	defer cleanup()

	snapshot, err := store.OpenReadOnly(ctx, snapshotPath)
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	var integrity string
	if err := snapshot.DB().QueryRowContext(ctx, `pragma integrity_check`).Scan(&integrity); err != nil {
		t.Fatal(err)
	}
	if integrity != "ok" {
		t.Fatalf("snapshot integrity = %q", integrity)
	}
	var incompleteBatches int
	if err := snapshot.DB().QueryRowContext(ctx, `select count(*) from (select batch from paired group by batch having count(*) <> 2)`).Scan(&incompleteBatches); err != nil {
		t.Fatal(err)
	}
	if incompleteBatches != 0 {
		t.Fatalf("snapshot contains %d partial write batches", incompleteBatches)
	}
}

func TestImportApplePreflightFailureLeavesExistingObservations(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	paths := Paths{DataDir: root, Database: filepath.Join(root, "photos.sqlite")}
	archiveDB, err := store.Open(ctx, store.Options{Path: paths.Database, Schema: Schema, SchemaVersion: SchemaVersion})
	if err != nil {
		t.Fatal(err)
	}
	sourceID := stableID("source_library", "test-library")
	assetID := stableID("asset", sourceID, "92D69D06-27F0-4831-AEC7-C6F640F075BA/L0/001")
	execTestSQL(t, archiveDB.DB(), `
insert into source_library(id, library_path, snapshot_path, snapshot_created_at, photos_version, metadata_json)
values (?, ?, ?, ?, ?, '{}')
`, sourceID, filepath.Join(root, "Library.photoslibrary"), "snapshot", "2026-07-06T00:00:00Z", "test")
	execTestSQL(t, archiveDB.DB(), `
insert into asset(id, local_identifier, media_type, media_subtypes, creation_date, modification_date, added_date, timezone_name, width, height, duration_seconds, favorite, hidden, burst_identifier, represents_burst, source_library_id, metadata_json)
values (?, ?, 'image', '', '2026-07-06T00:00:00Z', '', '', '', 100, 100, 0, 0, 0, '', 0, ?, '{}')
`, assetID, "92D69D06-27F0-4831-AEC7-C6F640F075BA/L0/001", sourceID)
	execTestSQL(t, archiveDB.DB(), `
insert into face_observation(id, asset_id, face_local_id, person_label, confidence, bounding_box_json, source, evidence_id)
values ('existing-face', ?, 'existing', 'Existing Person', 1, '{}', ?, 'existing-face-evidence')
`, assetID, photosLibraryDBFaceSource)
	execTestSQL(t, archiveDB.DB(), `
insert into visual_observation(id, asset_id, observation_type, label, confidence, bounding_box_json, source, model_id, evidence_id)
values ('existing-search', ?, 'apple_label', 'Existing Label', 1, '{}', ?, ?, 'existing-search-evidence')
`, assetID, photosSearchIndexSource, photosSearchIndexModelID)
	execTestSQL(t, archiveDB.DB(), `
insert into model_observation(id, asset_id, observation_type, value_text, value_json, confidence, source, model_id, prompt_version, evidence_id)
values ('existing-metadata', ?, 'apple_exif', 'Existing Camera', '{}', 1, ?, ?, ?, 'existing-metadata-evidence')
`, assetID, photosLibraryDBFaceSource, photosLibraryDBMetadataModelID, photosLibraryDBMetadataVersion)
	if err := archiveDB.Close(); err != nil {
		t.Fatal(err)
	}

	libraryPath := filepath.Join(root, "Library.photoslibrary")
	photosDBPath := filepath.Join(libraryPath, "database", "Photos.sqlite")
	createTestPhotosDB(t, photosDBPath)
	createTestPSIDB(t, filepath.Join(libraryPath, "database", "search", "psi.sqlite"))
	brokenDB, err := sql.Open("sqlite", "file:"+photosDBPath)
	if err != nil {
		t.Fatal(err)
	}
	execTestSQL(t, brokenDB, `drop table ZCOMPUTEDASSETATTRIBUTES`)
	if err := brokenDB.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportApple(ctx, paths, ImportAppleOptions{LibraryPath: libraryPath}); err == nil {
		t.Fatal("ImportApple() succeeded with an unsupported metadata schema")
	}

	archiveDB, err = store.OpenReadOnly(ctx, paths.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer archiveDB.Close()
	assertCount(t, archiveDB.DB(), `select count(*) from face_observation where id = 'existing-face' and person_label = 'Existing Person'`, 1)
	assertCount(t, archiveDB.DB(), `select count(*) from visual_observation where id = 'existing-search' and label = 'Existing Label'`, 1)
	assertCount(t, archiveDB.DB(), `select count(*) from model_observation where id = 'existing-metadata' and value_text = 'Existing Camera'`, 1)
}

func TestImportSearchIndexIsIdempotent(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	paths := Paths{DataDir: root, Database: filepath.Join(root, "photos.sqlite")}
	archiveDB, err := store.Open(ctx, store.Options{Path: paths.Database, Schema: Schema, SchemaVersion: SchemaVersion})
	if err != nil {
		t.Fatal(err)
	}
	sourceID := stableID("source_library", "test-library")
	assetID := stableID("asset", sourceID, "92D69D06-27F0-4831-AEC7-C6F640F075BA/L0/001")
	execTestSQL(t, archiveDB.DB(), `
insert into source_library(id, library_path, snapshot_path, snapshot_created_at, photos_version, metadata_json)
values (?, ?, ?, ?, ?, '{}')
`, sourceID, filepath.Join(root, "Library.photoslibrary"), "snapshot", "2026-07-06T00:00:00Z", "test")
	execTestSQL(t, archiveDB.DB(), `
insert into asset(id, local_identifier, media_type, media_subtypes, creation_date, modification_date, added_date, timezone_name, width, height, duration_seconds, favorite, hidden, burst_identifier, represents_burst, source_library_id, metadata_json)
values (?, ?, 'image', '', '2026-07-06T00:00:00Z', '', '', '', 100, 100, 0, 0, 0, '', 0, ?, '{}')
`, assetID, "92D69D06-27F0-4831-AEC7-C6F640F075BA/L0/001", sourceID)
	if err := archiveDB.Close(); err != nil {
		t.Fatal(err)
	}

	libraryPath := filepath.Join(root, "Library.photoslibrary")
	createTestPhotosDB(t, filepath.Join(libraryPath, "database", "Photos.sqlite"))
	createTestPSIDB(t, filepath.Join(libraryPath, "database", "search", "psi.sqlite"))
	faces, err := ImportFaces(ctx, paths, ImportFacesOptions{LibraryPath: libraryPath})
	if err != nil {
		t.Fatal(err)
	}
	if faces.FaceObservationsInserted != 1 || faces.AssetsTouched != 1 || faces.NamedPersonsFound != 1 {
		t.Fatalf("face import counts = %#v", faces)
	}

	first, err := ImportSearchIndex(ctx, paths, ImportSearchIndexOptions{LibraryPath: libraryPath})
	if err != nil {
		t.Fatal(err)
	}
	second, err := ImportSearchIndex(ctx, paths, ImportSearchIndexOptions{LibraryPath: libraryPath})
	if err != nil {
		t.Fatal(err)
	}
	combined, err := ImportApple(ctx, paths, ImportAppleOptions{LibraryPath: libraryPath})
	if err != nil {
		t.Fatal(err)
	}
	combinedAgain, err := ImportApple(ctx, paths, ImportAppleOptions{LibraryPath: libraryPath})
	if err != nil {
		t.Fatal(err)
	}
	if combined.SyncStrategy != "atomic_authoritative_replace_by_source" || combined.TotalAssetsTouched != 1 {
		t.Fatalf("combined import = %#v", combined)
	}
	if combined.PhotoMetadata.ExifObservationsInserted != 1 || combined.PhotoMetadata.QualityObservationsInserted != 1 || combined.PhotoMetadata.EditObservationsInserted != 1 {
		t.Fatalf("photo metadata import = %#v", combined.PhotoMetadata)
	}
	if combinedAgain.PhotoMetadata.ExifObservationsInserted != combined.PhotoMetadata.ExifObservationsInserted ||
		combinedAgain.PhotoMetadata.QualityObservationsInserted != combined.PhotoMetadata.QualityObservationsInserted ||
		combinedAgain.PhotoMetadata.EditObservationsInserted != combined.PhotoMetadata.EditObservationsInserted ||
		combinedAgain.PhotoMetadata.AssetsTouched != combined.PhotoMetadata.AssetsTouched {
		t.Fatalf("second photo metadata import = %#v, first %#v", combinedAgain.PhotoMetadata, combined.PhotoMetadata)
	}
	if first.GroupsRead != 3 || first.VisualObservationsInserted != 3 || first.PersonFaceObservationsInserted != 1 {
		t.Fatalf("first import counts = %#v", first)
	}
	if first.PersonLookupIdentifiersResolved != 1 || first.PersonLookupIdentifiersUnresolved != 0 {
		t.Fatalf("person lookup counts = resolved %d unresolved %d", first.PersonLookupIdentifiersResolved, first.PersonLookupIdentifiersUnresolved)
	}
	if first.KnownCategoryRows != 2 || first.UnknownCategoryRows != 1 || len(first.UnknownCategories) != 1 || first.UnknownCategories[0] != 9999 {
		t.Fatalf("category counts = known %d unknown %d unknown ids %#v", first.KnownCategoryRows, first.UnknownCategoryRows, first.UnknownCategories)
	}
	if first.CategoriesSeen["1500"] != 1 || first.CategoriesSeen["1300"] != 1 || first.CategoriesSeen["9999"] != 1 {
		t.Fatalf("categories seen = %#v", first.CategoriesSeen)
	}
	if second.GroupsRead != first.GroupsRead || second.VisualObservationsInserted != first.VisualObservationsInserted || second.PersonFaceObservationsInserted != first.PersonFaceObservationsInserted {
		t.Fatalf("second import counts = %#v, first %#v", second, first)
	}

	archiveDB, err = store.OpenReadOnly(ctx, paths.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer archiveDB.Close()
	assertCount(t, archiveDB.DB(), `select count(*) from visual_observation where source = ?`, 3, photosSearchIndexSource)
	assertCount(t, archiveDB.DB(), `select count(*) from face_observation where source = ?`, 1, photosSearchIndexSource)
	assertCount(t, archiveDB.DB(), `select count(*) from evidence_ref where source = ? and evidence_kind = 'apple_search_index'`, 3, photosSearchIndexSource)
	assertCount(t, archiveDB.DB(), `select count(*) from observation_fts where asset_id = ?`, 8, assetID)
	assertCount(t, archiveDB.DB(), `select count(*) from model_observation where asset_id = ? and source = ? and model_id = ?`, 3, assetID, photosLibraryDBFaceSource, photosLibraryDBMetadataModelID)
	assertCount(t, archiveDB.DB(), `select count(*) from evidence_ref where asset_id = ? and evidence_kind = 'apple_photo_metadata'`, 3, assetID)
	metadataSearch, err := Search(ctx, paths, SearchOptions{Query: "iPhone", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(metadataSearch.Results) != 1 || metadataSearch.Results[0].ObservationType != "apple_exif" {
		t.Fatalf("metadata search = %#v", metadataSearch.Results)
	}
	status, err := Status(ctx, paths)
	if err != nil {
		t.Fatal(err)
	}
	if !hasStatusCount(status.Counts, "observation.source.photos_search_index") {
		t.Fatalf("status counts missing Apple search source: %#v", status.Counts)
	}
}

func TestImportLeoSearchIndexIsIdempotent(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	paths := Paths{DataDir: root, Database: filepath.Join(root, "photos.sqlite")}
	archiveDB, err := store.Open(ctx, store.Options{Path: paths.Database, Schema: Schema, SchemaVersion: SchemaVersion})
	if err != nil {
		t.Fatal(err)
	}
	sourceID := stableID("source_library", "test-library")
	assetID := stableID("asset", sourceID, "92D69D06-27F0-4831-AEC7-C6F640F075BA/L0/001")
	execTestSQL(t, archiveDB.DB(), `
insert into source_library(id, library_path, snapshot_path, snapshot_created_at, photos_version, metadata_json)
values (?, ?, ?, ?, ?, '{}')
`, sourceID, filepath.Join(root, "Library.photoslibrary"), "snapshot", "2026-07-06T00:00:00Z", "test")
	execTestSQL(t, archiveDB.DB(), `
insert into asset(id, local_identifier, media_type, media_subtypes, creation_date, modification_date, added_date, timezone_name, width, height, duration_seconds, favorite, hidden, burst_identifier, represents_burst, source_library_id, metadata_json)
values (?, ?, 'image', '', '2026-07-06T00:00:00Z', '', '', '', 100, 100, 0, 0, 0, '', 0, ?, '{}')
`, assetID, "92D69D06-27F0-4831-AEC7-C6F640F075BA/L0/001", sourceID)
	if err := archiveDB.Close(); err != nil {
		t.Fatal(err)
	}

	libraryPath := filepath.Join(root, "Library.photoslibrary")
	createTestPhotosDB(t, filepath.Join(libraryPath, "database", "Photos.sqlite"))
	createTestLeoDB(t, filepath.Join(libraryPath, "database", "search", "leo.sqlite"))

	first, err := ImportSearchIndex(ctx, paths, ImportSearchIndexOptions{LibraryPath: libraryPath})
	if err != nil {
		t.Fatal(err)
	}
	second, err := ImportSearchIndex(ctx, paths, ImportSearchIndexOptions{LibraryPath: libraryPath})
	if err != nil {
		t.Fatal(err)
	}
	if first.Variant != "leo" || first.GroupsRead != 2 || first.VisualObservationsInserted != 2 {
		t.Fatalf("first leo import = %#v", first)
	}
	if first.CategoriesSeen["1500"] != 1 || first.CategoriesSeen["1201"] != 1 {
		t.Fatalf("leo categories = %#v", first.CategoriesSeen)
	}
	if second.GroupsRead != first.GroupsRead || second.VisualObservationsInserted != first.VisualObservationsInserted {
		t.Fatalf("second leo import = %#v, first %#v", second, first)
	}

	search, err := Search(ctx, paths, SearchOptions{Query: "ceramics", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(search.Results) != 1 || search.Results[0].Source != photosSearchIndexSource || search.Results[0].ObservationType != "apple_label" {
		t.Fatalf("leo search = %#v", search.Results)
	}
}

func createTestPhotosDB(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	execTestSQL(t, db, `create table ZPERSON (Z_PK integer primary key, ZDISPLAYNAME text, ZFULLNAME text, ZPERSONUUID text)`)
	execTestSQL(t, db, `create table ZDETECTEDFACE (Z_PK integer primary key, ZUUID text, ZPERSONFORFACE integer, ZASSETFORFACE integer, ZCENTERX real, ZCENTERY real, ZSIZE real, ZQUALITY real, ZNAMESOURCE integer, ZCLOUDNAMESOURCE integer, ZSOURCEWIDTH integer, ZSOURCEHEIGHT integer)`)
	execTestSQL(t, db, `create table ZASSET (Z_PK integer primary key, ZUUID text, ZADJUSTMENTSSTATE integer, ZOVERALLAESTHETICSCORE real, ZCURATIONSCORE real, ZPROMOTIONSCORE real, ZHIGHLIGHTVISIBILITYSCORE real)`)
	execTestSQL(t, db, `create table ZEXTENDEDATTRIBUTES (Z_PK integer primary key, ZASSET integer, ZISO integer, ZFLASHFIRED integer, ZAPERTURE real, ZFOCALLENGTH real, ZCAMERAMAKE text, ZCAMERAMODEL text, ZLENSMODEL text, ZCODEC text)`)
	execTestSQL(t, db, `create table ZCOMPUTEDASSETATTRIBUTES (Z_PK integer primary key, ZASSET integer, ZFAILURESCORE real, ZHARMONIOUSCOLORSCORE real, ZINTERESTINGSUBJECTSCORE real, ZPLEASANTCOMPOSITIONSCORE real, ZSHARPLYFOCUSEDSUBJECTSCORE real, ZWELLFRAMEDSUBJECTSCORE real)`)
	execTestSQL(t, db, `insert into ZPERSON(Z_PK, ZDISPLAYNAME, ZFULLNAME, ZPERSONUUID) values (1, 'Alex', 'Alex', '08EA22FD-06A7-4145-829F-D724B9DD1BB6')`)
	execTestSQL(t, db, `insert into ZASSET(Z_PK, ZUUID, ZADJUSTMENTSSTATE, ZOVERALLAESTHETICSCORE, ZCURATIONSCORE, ZPROMOTIONSCORE, ZHIGHLIGHTVISIBILITYSCORE) values (1, '92D69D06-27F0-4831-AEC7-C6F640F075BA', 1, 0.91, 0.82, 0.73, 0.64)`)
	execTestSQL(t, db, `insert into ZEXTENDEDATTRIBUTES(Z_PK, ZASSET, ZISO, ZFLASHFIRED, ZAPERTURE, ZFOCALLENGTH, ZCAMERAMAKE, ZCAMERAMODEL, ZLENSMODEL, ZCODEC) values (1, 1, 100, 0, 1.8, 26, 'Apple', 'iPhone 15 Pro', 'iPhone 15 Pro back camera', 'HEVC')`)
	execTestSQL(t, db, `insert into ZCOMPUTEDASSETATTRIBUTES(Z_PK, ZASSET, ZFAILURESCORE, ZHARMONIOUSCOLORSCORE, ZINTERESTINGSUBJECTSCORE, ZPLEASANTCOMPOSITIONSCORE, ZSHARPLYFOCUSEDSUBJECTSCORE, ZWELLFRAMEDSUBJECTSCORE) values (1, 1, 0.01, 0.75, 0.88, 0.92, 0.86, 0.94)`)
	execTestSQL(t, db, `insert into ZDETECTEDFACE(Z_PK, ZUUID, ZPERSONFORFACE, ZASSETFORFACE, ZCENTERX, ZCENTERY, ZSIZE, ZQUALITY, ZNAMESOURCE, ZCLOUDNAMESOURCE, ZSOURCEWIDTH, ZSOURCEHEIGHT) values (1, 'face-1', 1, 1, 0.5, 0.5, 0.2, 0.9, 1, 0, 100, 100)`)
}

func createTestPSIDB(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	execTestSQL(t, db, `create table assets (uuid_0 integer, uuid_1 integer)`)
	execTestSQL(t, db, `create table groups (category integer, owning_groupid integer, content_string text, normalized_string text, lookup_identifier text, score real)`)
	execTestSQL(t, db, `create table ga (assetid integer, groupid integer)`)
	execTestSQL(t, db, `insert into assets(rowid, uuid_0, uuid_1) values (1, 3551352356587034258, -5010834848571013202)`)
	execTestSQL(t, db, `insert into groups(rowid, category, owning_groupid, content_string, normalized_string, lookup_identifier, score) values (10, 1500, null, 'Antique Car'||char(0), 'antique car'||char(0), '', 0.9)`)
	execTestSQL(t, db, `insert into groups(rowid, category, owning_groupid, content_string, normalized_string, lookup_identifier, score) values (11, 1300, null, 'Alex'||char(0), 'alex'||char(0), '08EA22FD-06A7-4145-829F-D724B9DD1BB6'||char(0), 0.8)`)
	execTestSQL(t, db, `insert into groups(rowid, category, owning_groupid, content_string, normalized_string, lookup_identifier, score) values (12, 9999, null, 'Future Thing', 'future thing', '', 0.7)`)
	execTestSQL(t, db, `insert into ga(rowid, assetid, groupid) values (100, 1, 10), (101, 1, 11), (102, 1, 12)`)
}

func createTestLeoDB(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	execTestSQL(t, db, `create table lexicon (lexeme_id integer primary key, type integer, category integer, content text)`)
	execTestSQL(t, db, `create table items (identifier text, type integer, lexeme_ids blob)`)
	execTestSQL(t, db, `insert into lexicon(lexeme_id, type, category, content) values (1, 1, 4000, 'Ceramics'), (2, 1, 7000, 'Studio Archive'), (3, 1, 9999, 'Unsupported'), (4, 2, 4000, 'Ignored')`)
	execTestSQL(t, db, `insert into items(rowid, identifier, type, lexeme_ids) values (1, ?, 1, ?)`, "92D69D06-27F0-4831-AEC7-C6F640F075BA", []byte{1, 0, 0, 0, 2, 0, 0, 0, 3, 0, 0, 0, 4, 0, 0, 0})
}

func execTestSQL(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

func assertCount(t *testing.T, db *sql.DB, query string, want int, args ...any) {
	t.Helper()
	var got int
	if err := db.QueryRow(query, args...).Scan(&got); err != nil {
		t.Fatalf("count %q: %v", query, err)
	}
	if got != want {
		t.Fatalf("count %q = %d, want %d", query, got, want)
	}
}
