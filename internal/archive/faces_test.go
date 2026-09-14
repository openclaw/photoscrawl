package archive

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/openclaw/crawlkit/store"
	"github.com/openclaw/photoscrawl/internal/photos"
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
	if got := facePersonKind(photosFaceRow{hasDetectionType: true, detectionType: sql.NullInt64{Int64: 3, Valid: true}}); got != "pet" {
		t.Fatalf("pet detection type = %q", got)
	}
	if got := facePersonKind(photosFaceRow{hasDetectionType: true, detectionType: sql.NullInt64{Int64: 2, Valid: true}}); got != "unknown" {
		t.Fatalf("unknown detection type = %q", got)
	}
}

func TestFaceSignalDerivations(t *testing.T) {
	tests := []struct {
		name string
		face photosFaceRow
		want any
	}{
		{"both eyes closed", photosFaceRow{hasLeftEyeClosed: true, hasRightEyeClosed: true, leftEyeClosed: sql.NullInt64{Int64: 1, Valid: true}, rightEyeClosed: sql.NullInt64{Int64: 1, Valid: true}}, 1},
		{"one eye closed", photosFaceRow{hasLeftEyeClosed: true, hasRightEyeClosed: true, leftEyeClosed: sql.NullInt64{Int64: 1, Valid: true}, rightEyeClosed: sql.NullInt64{Valid: true}}, 1},
		{"neither eye closed", photosFaceRow{hasLeftEyeClosed: true, hasRightEyeClosed: true, leftEyeClosed: sql.NullInt64{Valid: true}, rightEyeClosed: sql.NullInt64{Valid: true}}, 0},
		{"one eye open and one uncomputed", photosFaceRow{hasLeftEyeClosed: true, hasRightEyeClosed: true, leftEyeClosed: sql.NullInt64{Valid: true}}, nil},
		{"source columns absent", photosFaceRow{}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := faceEyesClosed(tt.face); got != tt.want {
				t.Fatalf("faceEyesClosed() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestPhotoMetadataWritesDuplicateOnlyForFlaggedAsset(t *testing.T) {
	ctx := context.Background()
	paths := testPaths(t)
	db, err := store.Open(ctx, store.Options{Path: paths.Database, Schema: Schema, SchemaVersion: SchemaVersion})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	sourceID := "library"
	execTestSQL(t, db.DB(), `insert into source_library values (?, '/fixture', 'snapshot', '2026-07-06T00:00:00Z', 'test', '{}')`, sourceID)
	for _, asset := range []struct{ id, uuid string }{{"flagged", "asset-flagged"}, {"clear", "asset-clear"}} {
		execTestSQL(t, db.DB(), `insert into asset(id, local_identifier, media_type, media_subtypes, creation_date, modification_date, added_date, timezone_name, width, height, duration_seconds, favorite, hidden, burst_identifier, represents_burst, source_library_id, metadata_json) values (?, ?, 'image', '', '', '', '', '', 0, 0, 0, 0, 0, '', 0, ?, '{}')`, asset.id, asset.uuid, sourceID)
	}
	tx, err := db.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	result, _, err := writePhotoMetadataImport(ctx, tx, photoMetadataImportInput{schema: map[string]string{"asset_table": "ZASSET"}, rows: []applePhotoMetadataRow{
		{assetUUID: "asset-flagged", duplicate: map[string]any{"state": int64(1), "metadata_group": nil, "perceptual_group": nil}, hasDuplicate: true},
		{assetUUID: "asset-clear", duplicate: map[string]any{"state": int64(0), "metadata_group": nil, "perceptual_group": nil}},
	}}, appleAssetScope{libraryID: sourceID, byUUID: map[string]string{"asset-flagged": "flagged", "asset-clear": "clear"}}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if result.DuplicateObservationsInserted != 1 {
		t.Fatalf("duplicate observations = %d", result.DuplicateObservationsInserted)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	assertCount(t, db.DB(), `select count(*) from model_observation where observation_type = 'apple_duplicate' and asset_id = 'flagged'`, 1)
	assertCount(t, db.DB(), `select count(*) from model_observation where observation_type = 'apple_duplicate' and asset_id = 'clear'`, 0)
}

func TestImportFacesWithoutSignalColumnsStoresNulls(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	paths := Paths{DataDir: root, Database: filepath.Join(root, "photos.sqlite")}
	libraryPath := filepath.Join(root, "Library.photoslibrary")
	createTestPhotosDBWithoutFaceSignals(t, filepath.Join(libraryPath, "database", "Photos.sqlite"))

	archiveDB, err := store.Open(ctx, store.Options{Path: paths.Database, Schema: Schema, SchemaVersion: SchemaVersion})
	if err != nil {
		t.Fatal(err)
	}
	sourceID := stableID("source_library", libraryPath)
	assetID := stableID("asset", sourceID, "92D69D06-27F0-4831-AEC7-C6F640F075BA/L0/001")
	execTestSQL(t, archiveDB.DB(), `insert into source_library values (?, ?, 'snapshot', '2026-07-06T00:00:00Z', 'test', '{}')`, sourceID, libraryPath)
	execTestSQL(t, archiveDB.DB(), `insert into asset(id, local_identifier, media_type, media_subtypes, creation_date, modification_date, added_date, timezone_name, width, height, duration_seconds, favorite, hidden, burst_identifier, represents_burst, source_library_id, metadata_json) values (?, ?, 'image', '', '', '', '', '', 0, 0, 0, 0, 0, '', 0, ?, '{}')`, assetID, "92D69D06-27F0-4831-AEC7-C6F640F075BA/L0/001", sourceID)
	if err := archiveDB.Close(); err != nil {
		t.Fatal(err)
	}

	result, err := ImportFaces(ctx, paths, ImportFacesOptions{LibraryPath: libraryPath})
	if err != nil {
		t.Fatal(err)
	}
	if result.FaceObservationsInserted != 1 || result.FacesWithEyeState != 0 || result.FacesWithSmile != 0 || result.FacesWithBlurScore != 0 || result.FacesWithPersonKind != 0 {
		t.Fatalf("import without face signals = %#v", result)
	}
	archiveDB, err = store.OpenReadOnly(ctx, paths.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer archiveDB.Close()
	var eyes, smile, blur, kind any
	if err := archiveDB.DB().QueryRowContext(ctx, `select eyes_closed, smile, blur_score, person_kind from face_observation`).Scan(&eyes, &smile, &blur, &kind); err != nil {
		t.Fatal(err)
	}
	if eyes != nil || smile != nil || blur != nil || kind != "unknown" {
		t.Fatalf("missing source signal values = eyes=%v smile=%v blur=%v kind=%v", eyes, smile, blur, kind)
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
	ctx := context.Background()
	root := t.TempDir()
	sourcePath := filepath.Join(root, "live.sqlite")
	db, err := sql.Open("sqlite", "file:"+sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// Keep the connection-local WAL and cache settings on the writer connection.
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, `
pragma journal_mode = wal;
pragma wal_autocheckpoint = 0;
pragma cache_size = 1;
create table paired(batch integer not null, side integer not null, payload blob, primary key(batch, side));
with recursive batches(value) as (select 1 union all select value + 1 from batches where value < 8)
insert into paired(batch, side, payload)
select value, side, zeroblob(2048) from batches cross join (select 1 as side union all select 2);
`); err != nil {
		t.Fatal(err)
	}
	committedWAL, err := os.Stat(sourcePath + "-wal")
	if err != nil {
		t.Fatal(err)
	}
	if committedWAL.Size() == 0 {
		t.Fatal("fixture has no committed WAL data")
	}

	// Cancel on cleanup only. copySQLite is I/O bound, so a wall-clock budget
	// here would assert how fast the machine is rather than whether the
	// snapshot is consistent; go test's own timeout is the hang backstop.
	writeCtx, cancel := context.WithCancel(ctx)
	writerDone := make(chan error, 1)
	writerStarted := make(chan struct{})
	finishWrite := make(chan struct{})
	go func() {
		defer close(writerDone)
		writerDone <- func() error {
			tx, err := db.BeginTx(writeCtx, nil)
			if err != nil {
				return err
			}
			defer tx.Rollback()
			// Spill incomplete pairs to the WAL, then hold the transaction open
			// throughout the copy so overlap does not depend on scheduling.
			if _, err := tx.ExecContext(writeCtx, `insert into paired(batch, side, payload) select batch + 8, 1, payload from paired where side = 1 and batch <= 8`); err != nil {
				return err
			}
			close(writerStarted)
			select {
			case <-finishWrite:
			case <-writeCtx.Done():
				return writeCtx.Err()
			}
			if _, err := tx.ExecContext(writeCtx, `insert into paired(batch, side, payload) select batch + 8, 2, payload from paired where side = 2 and batch <= 8`); err != nil {
				return err
			}
			return tx.Commit()
		}()
	}()
	defer func() {
		cancel()
		<-writerDone
	}()
	select {
	case <-writerStarted:
	case err := <-writerDone:
		t.Fatalf("writer failed before snapshot: %v", err)
	}
	pendingWAL, err := os.Stat(sourcePath + "-wal")
	if err != nil {
		t.Fatal(err)
	}
	if pendingWAL.Size() <= committedWAL.Size() {
		t.Fatal("writer did not spill uncommitted data to the WAL")
	}

	snapshotPath, cleanup, err := photos.CopySQLite(writeCtx, sourcePath, "photoscrawl-snapshot-test-")
	defer cleanup()
	if err != nil {
		t.Fatal(err)
	}
	close(finishWrite)
	if err := <-writerDone; err != nil {
		t.Fatal(err)
	}
	assertCount(t, db, `select count(*) from paired`, 32)

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
	assertCount(t, snapshot.DB(), `select count(*) from paired where batch <= 8`, 16)
	assertCount(t, snapshot.DB(), `select count(*) from paired where batch > 8`, 0)
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
	if faces.FaceObservationsInserted != 1 || faces.AssetsTouched != 1 || faces.NamedPersonsFound != 1 || faces.FacesWithEyeState != 1 || faces.FacesWithSmile != 1 || faces.FacesWithQuality != 1 || faces.FacesWithBlurScore != 1 || faces.FacesWithPersonKind != 1 {
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
	if combined.PhotoMetadata.ExifObservationsInserted != 1 || combined.PhotoMetadata.QualityObservationsInserted != 1 || combined.PhotoMetadata.EditObservationsInserted != 1 || combined.PhotoMetadata.DuplicateObservationsInserted != 1 {
		t.Fatalf("photo metadata import = %#v", combined.PhotoMetadata)
	}
	if combinedAgain.PhotoMetadata.ExifObservationsInserted != combined.PhotoMetadata.ExifObservationsInserted ||
		combinedAgain.PhotoMetadata.QualityObservationsInserted != combined.PhotoMetadata.QualityObservationsInserted ||
		combinedAgain.PhotoMetadata.EditObservationsInserted != combined.PhotoMetadata.EditObservationsInserted ||
		combinedAgain.PhotoMetadata.DuplicateObservationsInserted != combined.PhotoMetadata.DuplicateObservationsInserted ||
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
	assertCount(t, archiveDB.DB(), `select count(*) from observation_fts where asset_id = ?`, 9, assetID)
	assertCount(t, archiveDB.DB(), `select count(*) from model_observation where asset_id = ? and source = ? and model_id = ?`, 4, assetID, photosLibraryDBFaceSource, photosLibraryDBMetadataModelID)
	assertCount(t, archiveDB.DB(), `select count(*) from evidence_ref where asset_id = ? and evidence_kind = 'apple_photo_metadata'`, 4, assetID)
	var personUUID, personKind string
	var quality, blurScore float64
	var eyesClosed, smile int
	if err := archiveDB.DB().QueryRowContext(ctx, `select person_uuid, person_kind, quality, blur_score, eyes_closed, smile from face_observation where source = ?`, photosLibraryDBFaceSource).Scan(&personUUID, &personKind, &quality, &blurScore, &eyesClosed, &smile); err != nil {
		t.Fatal(err)
	}
	if personUUID != "08EA22FD-06A7-4145-829F-D724B9DD1BB6" || personKind != "human" || quality != 0.9 || blurScore != 0.12 || eyesClosed != 1 || smile != 1 {
		t.Fatalf("imported face signals = uuid=%q kind=%q quality=%v blur=%v eyes=%d smile=%d", personUUID, personKind, quality, blurScore, eyesClosed, smile)
	}
	var duplicateJSON string
	if err := archiveDB.DB().QueryRowContext(ctx, `select value_json from model_observation where asset_id = ? and observation_type = 'apple_duplicate'`, assetID).Scan(&duplicateJSON); err != nil {
		t.Fatal(err)
	}
	if duplicateJSON != `{"metadata_group":101,"perceptual_group":null,"state":1}` {
		t.Fatalf("duplicate observation = %s", duplicateJSON)
	}
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
	execTestSQL(t, db, `create table ZPERSON (Z_PK integer primary key, ZDISPLAYNAME text, ZFULLNAME text, ZPERSONUUID text, ZDETECTIONTYPE integer)`)
	execTestSQL(t, db, `create table ZDETECTEDFACE (Z_PK integer primary key, ZUUID text, ZPERSONFORFACE integer, ZASSETFORFACE integer, ZCENTERX real, ZCENTERY real, ZSIZE real, ZQUALITY real, ZBLURSCORE real, ZISLEFTEYECLOSED integer, ZISRIGHTEYECLOSED integer, ZHASSMILE integer, ZNAMESOURCE integer, ZCLOUDNAMESOURCE integer, ZSOURCEWIDTH integer, ZSOURCEHEIGHT integer)`)
	execTestSQL(t, db, `create table ZASSET (Z_PK integer primary key, ZUUID text, ZADJUSTMENTSSTATE integer, ZDUPLICATEASSETVISIBILITYSTATE integer, ZDUPLICATEMETADATAMATCHINGALBUM integer, ZDUPLICATEPERCEPTUALMATCHINGALBUM integer, ZOVERALLAESTHETICSCORE real, ZCURATIONSCORE real, ZPROMOTIONSCORE real, ZHIGHLIGHTVISIBILITYSCORE real)`)
	execTestSQL(t, db, `create table ZEXTENDEDATTRIBUTES (Z_PK integer primary key, ZASSET integer, ZISO integer, ZFLASHFIRED integer, ZAPERTURE real, ZFOCALLENGTH real, ZCAMERAMAKE text, ZCAMERAMODEL text, ZLENSMODEL text, ZCODEC text)`)
	execTestSQL(t, db, `create table ZCOMPUTEDASSETATTRIBUTES (Z_PK integer primary key, ZASSET integer, ZFAILURESCORE real, ZHARMONIOUSCOLORSCORE real, ZINTERESTINGSUBJECTSCORE real, ZPLEASANTCOMPOSITIONSCORE real, ZSHARPLYFOCUSEDSUBJECTSCORE real, ZWELLFRAMEDSUBJECTSCORE real)`)
	execTestSQL(t, db, `insert into ZPERSON(Z_PK, ZDISPLAYNAME, ZFULLNAME, ZPERSONUUID, ZDETECTIONTYPE) values (1, 'Alex', 'Alex', '08EA22FD-06A7-4145-829F-D724B9DD1BB6', 1)`)
	execTestSQL(t, db, `insert into ZASSET(Z_PK, ZUUID, ZADJUSTMENTSSTATE, ZDUPLICATEASSETVISIBILITYSTATE, ZDUPLICATEMETADATAMATCHINGALBUM, ZDUPLICATEPERCEPTUALMATCHINGALBUM, ZOVERALLAESTHETICSCORE, ZCURATIONSCORE, ZPROMOTIONSCORE, ZHIGHLIGHTVISIBILITYSCORE) values (1, '92D69D06-27F0-4831-AEC7-C6F640F075BA', 1, 1, 101, null, 0.91, 0.82, 0.73, 0.64)`)
	execTestSQL(t, db, `insert into ZEXTENDEDATTRIBUTES(Z_PK, ZASSET, ZISO, ZFLASHFIRED, ZAPERTURE, ZFOCALLENGTH, ZCAMERAMAKE, ZCAMERAMODEL, ZLENSMODEL, ZCODEC) values (1, 1, 100, 0, 1.8, 26, 'Apple', 'iPhone 15 Pro', 'iPhone 15 Pro back camera', 'HEVC')`)
	execTestSQL(t, db, `insert into ZCOMPUTEDASSETATTRIBUTES(Z_PK, ZASSET, ZFAILURESCORE, ZHARMONIOUSCOLORSCORE, ZINTERESTINGSUBJECTSCORE, ZPLEASANTCOMPOSITIONSCORE, ZSHARPLYFOCUSEDSUBJECTSCORE, ZWELLFRAMEDSUBJECTSCORE) values (1, 1, 0.01, 0.75, 0.88, 0.92, 0.86, 0.94)`)
	execTestSQL(t, db, `insert into ZDETECTEDFACE(Z_PK, ZUUID, ZPERSONFORFACE, ZASSETFORFACE, ZCENTERX, ZCENTERY, ZSIZE, ZQUALITY, ZBLURSCORE, ZISLEFTEYECLOSED, ZISRIGHTEYECLOSED, ZHASSMILE, ZNAMESOURCE, ZCLOUDNAMESOURCE, ZSOURCEWIDTH, ZSOURCEHEIGHT) values (1, 'face-1', 1, 1, 0.5, 0.5, 0.2, 0.9, 0.12, 1, 0, 1, 1, 0, 100, 100)`)
	execTestSQL(t, db, `create table ZMEDIAANALYSISASSETATTRIBUTES (Z_PK integer primary key, ZASSET integer, ZBLURRINESSSCORE real)`)
	execTestSQL(t, db, `insert into ZMEDIAANALYSISASSETATTRIBUTES(Z_PK, ZASSET, ZBLURRINESSSCORE) values (1, 1, 0.12)`)
}

func TestPhotoMetadataReadsMediaAnalysisBlurriness(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "Photos.sqlite")
	createTestPhotosDB(t, path)
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	input, err := loadPhotoMetadataInput(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if len(input.rows) != 1 || input.rows[0].quality["media_blurriness"] != 0.12 {
		t.Fatalf("media blurriness = %#v", input.rows)
	}
	execTestSQL(t, db, `drop table ZMEDIAANALYSISASSETATTRIBUTES`)
	input, err = loadPhotoMetadataInput(ctx, db)
	if err != nil {
		t.Fatalf("import without the media analysis table failed: %v", err)
	}
	if _, ok := input.rows[0].quality["media_blurriness"]; ok {
		t.Fatalf("media blurriness present without its table: %#v", input.rows[0].quality)
	}
}

func createTestPhotosDBWithoutFaceSignals(t *testing.T, path string) {
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
	execTestSQL(t, db, `create table ZASSET (Z_PK integer primary key, ZUUID text)`)
	execTestSQL(t, db, `insert into ZPERSON values (1, 'Alex', 'Alex', '08EA22FD-06A7-4145-829F-D724B9DD1BB6')`)
	execTestSQL(t, db, `insert into ZASSET values (1, '92D69D06-27F0-4831-AEC7-C6F640F075BA')`)
	execTestSQL(t, db, `insert into ZDETECTEDFACE values (1, 'face-1', 1, 1, 0.5, 0.5, 0.2, 0.9, 1, 0, 100, 100)`)
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

func TestFaceQualityTreatsPhotosSentinelAsUnknown(t *testing.T) {
	cases := map[string]struct {
		in   sql.NullFloat64
		want sql.NullFloat64
	}{
		"not computed": {sql.NullFloat64{Float64: -1, Valid: true}, sql.NullFloat64{}},
		"missing":      {sql.NullFloat64{}, sql.NullFloat64{}},
		"zero":         {sql.NullFloat64{Float64: 0, Valid: true}, sql.NullFloat64{Float64: 0, Valid: true}},
		"scored":       {sql.NullFloat64{Float64: 0.42, Valid: true}, sql.NullFloat64{Float64: 0.42, Valid: true}},
	}
	for name, c := range cases {
		if got := faceQuality(photosFaceRow{quality: c.in}); got != c.want {
			t.Fatalf("%s: faceQuality(%v) = %v, want %v", name, c.in, got, c.want)
		}
	}
}

func TestPhotoMetadataIgnoresNonpositiveDuplicateGroups(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "Photos.sqlite")
	createTestPhotosDB(t, path)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	execTestSQL(t, db, `update ZASSET set ZDUPLICATEASSETVISIBILITYSTATE=0, ZDUPLICATEMETADATAMATCHINGALBUM=0, ZDUPLICATEPERCEPTUALMATCHINGALBUM=-1`)
	input, err := loadPhotoMetadataInput(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if len(input.rows) != 1 || input.rows[0].hasDuplicate || input.rows[0].duplicate["metadata_group"] != nil || input.rows[0].duplicate["perceptual_group"] != nil {
		t.Fatalf("duplicate sentinels: %#v", input.rows)
	}
}
