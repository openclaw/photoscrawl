package photos

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/openclaw/crawlkit/store"
)

func TestCopySQLiteWhitespaceSource(t *testing.T) {
	for _, journal := range []string{"DELETE", "WAL"} {
		for _, spelling := range []string{"direct", "symlink"} {
			t.Run(journal+"/"+spelling, func(t *testing.T) {
				ctx := context.Background()
				root, err := filepath.EvalSymlinks(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				source := filepath.Join(root, "source.db ")
				writer, err := sql.Open("sqlite", source)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { writer.Close() })
				writer.SetMaxOpenConns(1)
				if _, err := writer.Exec("pragma journal_mode=" + journal + "; pragma wal_autocheckpoint=0; create table marker(value text); insert into marker values ('committed-copy-row');"); err != nil {
					t.Fatal(err)
				}
				requireLiteralCopySource(t, writer, source, journal)
				var marker string
				if err := writer.QueryRow("select value from marker").Scan(&marker); err != nil || marker != "committed-copy-row" {
					t.Fatalf("fixture marker = %q: %v", marker, err)
				}
				selected := source
				if spelling == "symlink" {
					selected = filepath.Join(filepath.Dir(source), "alias.db")
					if err := os.Symlink(source, selected); err != nil {
						t.Fatal(err)
					}
					requireCopyAlias(t, selected, source)
				}
				before := copyFixtureFiles(t, source)
				if journal == "WAL" && len(before["-wal"].data) == 0 {
					t.Fatal("fixture has no committed WAL")
				}
				t.Cleanup(func() {
					if !reflect.DeepEqual(before, copyFixtureFiles(t, source)) {
						t.Error("source bytes, modes or sidecar inventory changed before connection close")
					}
					if spelling == "symlink" {
						requireCopyAlias(t, selected, source)
						requireCopySidecarsAbsent(t, selected)
					}
					requireCopyPathAbsent(t, strings.TrimSpace(source))
				})
				t.Log("literal filename, identity, journal and committed row verified before CopySQLite")
				private, cleanup, err := CopySQLite(ctx, selected, "photoscrawl-whitespace-test-")
				if err != nil {
					t.Fatal(err)
				}
				defer cleanup()
				reader, err := store.OpenReadOnly(ctx, private)
				if err != nil {
					t.Fatal(err)
				}
				defer reader.Close()
				if err := reader.DB().QueryRow("select value from marker").Scan(&marker); err != nil || marker != "committed-copy-row" {
					t.Fatalf("copied committed row = %q: %v", marker, err)
				}
				if filepath.Base(private) != "snapshot.db" || filepath.Dir(private) == filepath.Dir(source) {
					t.Fatalf("unexpected private path: %q", private)
				}
				for _, basename := range []string{filepath.Base(source), strings.TrimSpace(filepath.Base(source))} {
					requireCopyPathAbsent(t, filepath.Join(filepath.Dir(private), basename))
				}
				info, err := os.Stat(filepath.Dir(private))
				if err != nil || info.Mode().Perm() != 0o700 {
					t.Fatalf("private directory permissions: %v %v", info, err)
				}
				for suffix, file := range copyFixtureFiles(t, private) {
					if file.mode.Perm()&0o077 != 0 {
						t.Errorf("private %q permissions = %v", suffix, file.mode)
					}
				}
				if err := reader.Close(); err != nil {
					t.Fatal(err)
				}
				cleanup()
				cleanup()
				requireCopyPathAbsent(t, filepath.Dir(private))
			})
		}
	}
}

func requireLiteralCopySource(t *testing.T, db *sql.DB, source, journal string) {
	t.Helper()
	var seq int
	var name, filename, mode string
	if err := db.QueryRow("pragma database_list").Scan(&seq, &name, &filename); err != nil {
		t.Fatal(err)
	}
	if name != "main" || filename != source {
		t.Fatalf("fixture SQLite filename = %q (%q), want literal %q", filename, name, source)
	}
	selected, err := os.Stat(filename)
	if err != nil {
		t.Fatal(err)
	}
	literal, err := os.Stat(source)
	if err != nil || !os.SameFile(selected, literal) {
		t.Fatalf("fixture literal identity mismatch: %v", err)
	}
	if err := db.QueryRow("pragma journal_mode").Scan(&mode); err != nil || !strings.EqualFold(mode, journal) {
		t.Fatalf("fixture journal mode = %q, want %q: %v", mode, journal, err)
	}
	requireCopyPathAbsent(t, strings.TrimSpace(source))
}

func requireCopyAlias(t *testing.T, alias, source string) {
	t.Helper()
	link, err := os.Lstat(alias)
	if err != nil || link.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("fixture alias is not a symlink: %v", err)
	}
	resolved, err := filepath.EvalSymlinks(alias)
	if err != nil || resolved != source {
		t.Fatalf("fixture alias resolves to %q, want %q: %v", resolved, source, err)
	}
	selected, err := os.Stat(alias)
	if err != nil {
		t.Fatal(err)
	}
	literal, err := os.Stat(source)
	if err != nil || !os.SameFile(selected, literal) {
		t.Fatalf("fixture alias identity mismatch: %v", err)
	}
}

type copyFixtureFile struct {
	mode os.FileMode
	data []byte
}

func copyFixtureFiles(t *testing.T, path string) map[string]copyFixtureFile {
	t.Helper()
	files := map[string]copyFixtureFile{}
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		info, err := os.Stat(path + suffix)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path + suffix)
		if err != nil {
			t.Fatal(err)
		}
		files[suffix] = copyFixtureFile{info.Mode(), data}
	}
	return files
}

func requireCopyPathAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("unexpected path %q: %v", path, err)
	}
}

func requireCopySidecarsAbsent(t *testing.T, path string) {
	t.Helper()
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		requireCopyPathAbsent(t, path+suffix)
	}
}

func TestCopySQLitePrivateRollbackRecovery(t *testing.T) {
	ctx := context.Background()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "source.db ")
	writer, err := sql.Open("sqlite", source)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	writer.SetMaxOpenConns(1)
	if _, err := writer.Exec(`pragma journal_mode=DELETE; pragma cache_size=1;
create table item(id integer primary key, value integer, payload blob);
with recursive n(i) as (select 1 union all select i+1 from n where i<32)
insert into item select i, 1, zeroblob(4096) from n;`); err != nil {
		t.Fatal(err)
	}
	requireLiteralCopySource(t, writer, source, "DELETE")
	var originalCount, originalMinimum, originalMaximum int
	if err := writer.QueryRow("select count(*), min(value), max(value) from item").Scan(&originalCount, &originalMinimum, &originalMaximum); err != nil || originalCount != 32 || originalMinimum != 1 || originalMaximum != 1 {
		t.Fatalf("fixture original rows = %d/%d/%d: %v", originalCount, originalMinimum, originalMaximum, err)
	}
	alias := filepath.Join(filepath.Dir(source), "alias.db")
	if err := os.Symlink(source, alias); err != nil {
		t.Fatal(err)
	}
	requireCopyAlias(t, alias, source)
	tx, err := writer.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`update item set value=2, payload=zeroblob(8192)`); err != nil {
		t.Fatal(err)
	}
	before := copyFixtureFiles(t, source)
	if len(before["-journal"].data) == 0 {
		t.Fatal("fixture has no active rollback journal")
	}
	defer func() {
		if !reflect.DeepEqual(before, copyFixtureFiles(t, source)) {
			t.Error("source rollback recovery changed bytes, modes or sidecar inventory")
		}
		requireCopyAlias(t, alias, source)
		requireCopySidecarsAbsent(t, alias)
		requireCopyPathAbsent(t, strings.TrimSpace(source))
	}()
	t.Log("literal filename, original rows and active rollback journal verified before CopySQLite")
	private, cleanup, err := CopySQLite(ctx, alias, "photoscrawl-journal-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	reader, err := store.OpenReadOnly(ctx, private)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	var count, minimum, maximum int
	if err := reader.DB().QueryRow("select count(*), min(value), max(value) from item").Scan(&count, &minimum, &maximum); err != nil {
		t.Fatal(err)
	}
	if count != 32 || minimum != 1 || maximum != 1 {
		t.Fatalf("uncommitted state leaked: count=%d min=%d max=%d", count, minimum, maximum)
	}
}

func TestCopySQLiteFinalVerificationCancellation(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source.db")
	if err := os.WriteFile(source, []byte("synthetic verification input"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	attempts := 0
	var private string
	_, cleanup, err := copySQLiteWithVerifier(ctx, source, "photoscrawl-cancel-test-", func(_ context.Context, path string) error {
		private = path
		attempts++
		if attempts == 5 {
			cancel()
			return ctx.Err()
		}
		return errors.New("synthetic retry")
	})
	cleanup()
	if attempts != 5 || !errors.Is(err, context.Canceled) {
		t.Fatalf("attempts=%d cancellation=%v", attempts, err)
	}
	if !strings.Contains(err.Error(), source) {
		t.Fatalf("cancellation lacks source path: %v", err)
	}
	if private == "" {
		t.Fatal("verifier was not called")
	}
	requireCopyPathAbsent(t, filepath.Dir(private))
}

func TestCopySQLiteCanceledBeforeCopy(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source.db ")
	if err := os.WriteFile(source, []byte("synthetic cancellation input"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	private, cleanup, err := CopySQLite(ctx, source, "photoscrawl-early-cancel-test-")
	cleanup()
	if private != "" || !errors.Is(err, context.Canceled) {
		t.Fatalf("private = %q, error = %v", private, err)
	}
}
