package archive

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/openclaw/crawlkit/store"
)

func TestAppleImportReplacementIsLibraryScoped(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	paths := Paths{DataDir: root, Database: filepath.Join(root, "archive.sqlite")}
	db, err := openArchiveStore(ctx, paths.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	libraries := []string{filepath.Join(root, "A.photoslibrary"), filepath.Join(root, "B.photoslibrary")}
	for i, library := range libraries {
		sourceID := stableID("source_library", library)
		identifier := "92D69D06-27F0-4831-AEC7-C6F640F075BA/L0/001"
		if i == 1 {
			identifier = "92D69D06-27F0-4831-AEC7-C6F640F075BA"
		}
		seedAppleAsset(t, db.DB(), sourceID, library, sourceID+"-asset", identifier)
		createTestPhotosDB(t, filepath.Join(library, "database", "Photos.sqlite"))
		createTestPSIDB(t, filepath.Join(library, "database", "search", "psi.sqlite"))
		if _, err := ImportApple(ctx, paths, ImportAppleOptions{LibraryPath: library}); err != nil {
			t.Fatal(err)
		}
	}
	assertCount(t, db.DB(), `select count(*) from face_observation where source = 'photos_library_db'`, 2)
	aID, bID := stableID("source_library", libraries[0])+"-asset", stableID("source_library", libraries[1])+"-asset"
	beforeB := appleRows(t, db.DB(), bID)
	if _, err := ImportApple(ctx, paths, ImportAppleOptions{LibraryPath: libraries[0]}); err != nil {
		t.Fatal(err)
	}
	assertAppleRows(t, db.DB(), bID, beforeB)

	source, err := sql.Open("sqlite", filepath.Join(libraries[0], "database", "Photos.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	execTestSQL(t, source, `delete from ZDETECTEDFACE; delete from ZASSET; delete from ZEXTENDEDATTRIBUTES; delete from ZCOMPUTEDASSETATTRIBUTES`)
	_ = source.Close()
	search, err := sql.Open("sqlite", filepath.Join(libraries[0], "database", "search", "psi.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	execTestSQL(t, search, `delete from ga`)
	_ = search.Close()
	if _, err := ImportApple(ctx, paths, ImportAppleOptions{LibraryPath: libraries[0]}); err != nil {
		t.Fatalf("valid empty library: %v", err)
	}
	for table, count := range appleRows(t, db.DB(), aID) {
		if count != 0 {
			t.Fatalf("empty library retained %s rows: %d", table, count)
		}
	}
	assertAppleRows(t, db.DB(), bID, beforeB)

	unknown := filepath.Join(root, "Unknown.photoslibrary")
	createTestPhotosDB(t, filepath.Join(unknown, "database", "Photos.sqlite"))
	createTestPSIDB(t, filepath.Join(unknown, "database", "search", "psi.sqlite"))
	if _, err := ImportApple(ctx, paths, ImportAppleOptions{LibraryPath: unknown}); err == nil {
		t.Fatal("unknown library accepted")
	}
	assertAppleRows(t, db.DB(), bID, beforeB)

	// A late metadata failure must roll back earlier face/search replacement.
	execTestSQL(t, db.DB(), `create trigger fail_metadata before insert on model_observation begin select raise(abort, 'synthetic metadata failure'); end`)
	if _, err := ImportApple(ctx, paths, ImportAppleOptions{LibraryPath: libraries[1]}); err == nil {
		t.Fatal("injected late failure accepted")
	}
	assertAppleRows(t, db.DB(), bID, beforeB)
}

func TestAppleScopeRejectsAmbiguousAssetsAndAllowsZeroAssets(t *testing.T) {
	ctx := context.Background()
	db, err := openArchiveStore(ctx, filepath.Join(t.TempDir(), "archive.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	library := filepath.Join(t.TempDir(), "Library.photoslibrary")
	seedAppleAsset(t, db.DB(), "library", library, "one", "same/L0/001")
	execTestSQL(t, db.DB(), `insert into asset select 'two', 'same', media_type, media_subtypes, creation_date, modification_date,
added_date, timezone_name, width, height, duration_seconds, favorite, hidden, burst_identifier, represents_burst,
source_library_id, metadata_json, deleted_at, deletion_source, deletion_reason from asset where id='one'`)
	tx, err := db.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := archiveAssetMap(ctx, tx, library); err == nil {
		t.Fatal("ambiguous normalized identifiers accepted")
	}
	_ = tx.Rollback()
	execTestSQL(t, db.DB(), `delete from asset`)
	tx, err = db.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	scope, err := archiveAssetMap(ctx, tx, library)
	if err != nil || scope.libraryID != "library" || len(scope.byUUID) != 0 {
		t.Fatalf("bound empty scope = %#v, %v", scope, err)
	}
}

func seedAppleAsset(t *testing.T, db *sql.DB, sourceID, library, assetID, identifier string) {
	t.Helper()
	execTestSQL(t, db, `insert into source_library values(?, ?, '', '', '', '{}')`, sourceID, library)
	execTestSQL(t, db, `insert into asset(id, local_identifier, media_type, media_subtypes, creation_date, modification_date,
added_date, timezone_name, width, height, duration_seconds, favorite, hidden, burst_identifier, represents_burst, source_library_id, metadata_json)
values(?, ?, 'image', '', '', '', '', '', 100, 100, 0, 0, 0, '', 0, ?, '{}')`, assetID, identifier, sourceID)
}

func appleRows(t *testing.T, db *sql.DB, assetID string) map[string]int {
	t.Helper()
	result := map[string]int{}
	for _, table := range []string{"face_observation", "visual_observation", "model_observation", "observation_fts", "observation_term", "evidence_ref"} {
		var count int
		if err := db.QueryRow(`select count(*) from `+store.QuoteIdent(table)+` where asset_id = ?`, assetID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		result[table] = count
	}
	return result
}

func assertAppleRows(t *testing.T, db *sql.DB, assetID string, want map[string]int) {
	t.Helper()
	for table, count := range appleRows(t, db, assetID) {
		if count != want[table] {
			t.Fatalf("%s rows = %d, want %d", table, count, want[table])
		}
	}
}
