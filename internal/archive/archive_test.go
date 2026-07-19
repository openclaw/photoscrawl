package archive

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/openclaw/crawlkit/store"
)

func TestInitAndStatus(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := t.TempDir()
	paths := Paths{DataDir: root, Database: filepath.Join(root, "photos.sqlite")}

	before, err := Status(ctx, paths)
	if err != nil {
		t.Fatal(err)
	}
	if before.State != "missing" {
		t.Fatalf("state before init = %q, want missing", before.State)
	}

	result, err := Init(ctx, paths)
	if err != nil {
		t.Fatal(err)
	}
	if result.Database != paths.Database {
		t.Fatalf("database = %q, want %q", result.Database, paths.Database)
	}

	after, err := Status(ctx, paths)
	if err != nil {
		t.Fatal(err)
	}
	if after.State != "ready" {
		t.Fatalf("state after init = %q, want ready", after.State)
	}
	if len(after.Counts) == 0 {
		t.Fatal("status returned no counts")
	}
}

func TestSearchDoesNotCreateOrRewriteDatabase(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	missingPath := filepath.Join(t.TempDir(), "missing.sqlite")
	if _, err := Search(ctx, Paths{Database: missingPath}, SearchOptions{Query: "fixture"}); err == nil {
		t.Fatal("search on missing database should fail")
	}
	if _, err := os.Stat(missingPath); !os.IsNotExist(err) {
		t.Fatalf("search created missing database: %v", err)
	}
	unrelatedPath := filepath.Join(t.TempDir(), "unrelated.sqlite")
	unrelated, err := store.Open(ctx, store.Options{Path: unrelatedPath})
	if err != nil {
		t.Fatal(err)
	}
	if err := unrelated.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Search(ctx, Paths{Database: unrelatedPath}, SearchOptions{Query: "fixture"}); err == nil {
		t.Fatal("search on unrelated database should fail")
	}
	unrelated, err = store.OpenReadOnly(ctx, unrelatedPath)
	if err != nil {
		t.Fatal(err)
	}
	var unrelatedTables int
	if err := unrelated.DB().QueryRowContext(ctx, `select count(*) from sqlite_master where type = 'table'`).Scan(&unrelatedTables); err != nil {
		unrelated.Close()
		t.Fatal(err)
	}
	if err := unrelated.Close(); err != nil {
		t.Fatal(err)
	}
	if unrelatedTables != 0 {
		t.Fatalf("search mutated unrelated database with %d tables", unrelatedTables)
	}
	versionedPath := filepath.Join(t.TempDir(), "versioned-unrelated.sqlite")
	versioned, err := store.Open(ctx, store.Options{
		Path:          versionedPath,
		Schema:        `create table sentinel(value text);`,
		SchemaVersion: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := versioned.DB().ExecContext(ctx, `insert into sentinel values ('preserved')`); err != nil {
		versioned.Close()
		t.Fatal(err)
	}
	if err := versioned.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Search(ctx, Paths{Database: versionedPath}, SearchOptions{Query: "fixture"}); err == nil {
		t.Fatal("search on unrelated versioned database should fail")
	}
	if _, err := Init(ctx, Paths{Database: versionedPath}); err == nil {
		t.Fatal("init on unrelated versioned database should fail")
	}
	versioned, err = store.OpenReadOnly(ctx, versionedPath)
	if err != nil {
		t.Fatal(err)
	}
	version, err := versioned.SchemaVersion(ctx)
	if err != nil {
		versioned.Close()
		t.Fatal(err)
	}
	var sentinel string
	if err := versioned.DB().QueryRowContext(ctx, `select value from sentinel`).Scan(&sentinel); err != nil {
		versioned.Close()
		t.Fatal(err)
	}
	var photoscrawlTables int
	if err := versioned.DB().QueryRowContext(ctx, `select count(*) from sqlite_master where type = 'table' and name = 'asset'`).Scan(&photoscrawlTables); err != nil {
		versioned.Close()
		t.Fatal(err)
	}
	if err := versioned.Close(); err != nil {
		t.Fatal(err)
	}
	if version != 1 || sentinel != "preserved" || photoscrawlTables != 0 {
		t.Fatalf("search mutated versioned database: version=%d sentinel=%q photoscrawl_tables=%d", version, sentinel, photoscrawlTables)
	}

	paths := testPaths(t)
	if _, err := Init(ctx, paths); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(paths.Database, 0o400); err != nil {
		t.Fatal(err)
	}
	if _, err := Search(ctx, paths, SearchOptions{Query: "fixture"}); err != nil {
		t.Fatalf("search read-only database: %v", err)
	}
	info, err := os.Stat(paths.Database)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o400 {
		t.Fatalf("search changed database mode to %o", info.Mode().Perm())
	}
}

func TestInitRejectsUnrelatedCurrentVersionDatabase(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "unrelated-v2.sqlite")
	db, err := store.Open(ctx, store.Options{Path: dbPath, Schema: `create table sentinel(value text);`, SchemaVersion: SchemaVersion})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB().ExecContext(ctx, `insert into sentinel values ('preserved')`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Init(ctx, Paths{Database: dbPath}); err == nil {
		t.Fatal("init on unrelated current-version database should fail")
	}
	if _, err := Search(ctx, Paths{Database: dbPath}, SearchOptions{Query: "fixture"}); err == nil {
		t.Fatal("search on unrelated current-version database should fail")
	}
	db, err = store.OpenReadOnly(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var sentinel string
	if err := db.DB().QueryRowContext(ctx, `select value from sentinel`).Scan(&sentinel); err != nil {
		t.Fatal(err)
	}
	var photoscrawlTables int
	if err := db.DB().QueryRowContext(ctx, `select count(*) from sqlite_master where type = 'table' and name = 'asset'`).Scan(&photoscrawlTables); err != nil {
		t.Fatal(err)
	}
	if sentinel != "preserved" || photoscrawlTables != 0 {
		t.Fatalf("unrelated v2 database mutated: sentinel=%q photoscrawl_tables=%d", sentinel, photoscrawlTables)
	}
}

func TestInitOnlyAdoptsTrulyEmptyUnversionedDatabase(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tests := []struct {
		name          string
		schema        string
		schemaVersion int
	}{
		{name: "view-only", schema: `create view sentinel as select 'preserved' as value;`},
		{name: "versioned-empty", schemaVersion: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "unrelated.sqlite")
			db, err := store.Open(ctx, store.Options{Path: dbPath, Schema: test.schema, SchemaVersion: test.schemaVersion})
			if err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := Init(ctx, Paths{Database: dbPath}); err == nil {
				t.Fatal("init on unowned database should fail")
			}
			db, err = store.OpenReadOnly(ctx, dbPath)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			version, err := db.SchemaVersion(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var photoscrawlTables int
			if err := db.DB().QueryRowContext(ctx, `select count(*) from sqlite_master where type = 'table' and name = 'asset'`).Scan(&photoscrawlTables); err != nil {
				t.Fatal(err)
			}
			if version != test.schemaVersion || photoscrawlTables != 0 {
				t.Fatalf("unowned database mutated: version=%d tables=%d", version, photoscrawlTables)
			}
		})
	}
}
