package archive

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/openclaw/crawlkit/store"
)

func TestArchiveWhitespaceTargetBoundAlias(t *testing.T) {
	for _, caller := range []string{"archive", "apple"} {
		t.Run(caller, func(t *testing.T) {
			ctx := context.Background()
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			ordinary := filepath.Join(root, "ordinary.db")
			target := filepath.Join(root, "bound.db ")
			alias := filepath.Join(root, "alias.db")
			library := filepath.Join(root, "synthetic.photoslibrary")
			db, err := openArchiveStore(ctx, ordinary)
			if err != nil {
				t.Fatal(err)
			}
			execTestSQL(t, db.DB(), "pragma journal_mode=DELETE")
			execTestSQL(t, db.DB(), `insert into source_library values('retained-library', ?, '', '', '', '{}')`, library)
			execTestSQL(t, db.DB(), "create table retained_marker(value text)")
			execTestSQL(t, db.DB(), "insert into retained_marker values('bound-row')")
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			bundle := pathFixtureFiles(t, ordinary)
			if len(bundle[""].data) == 0 {
				t.Fatal("fixture archive is empty")
			}
			for suffix := range bundle {
				if err := os.Rename(ordinary+suffix, target+suffix); err != nil {
					t.Fatal(err)
				}
			}
			if !reflect.DeepEqual(bundle, pathFixtureFiles(t, target)) || len(pathFixtureFiles(t, ordinary)) != 0 {
				t.Fatal("fixture bundle move was incomplete")
			}
			check, err := sql.Open("sqlite", target)
			if err != nil {
				t.Fatal(err)
			}
			requireLiteralArchiveTarget(t, check, target, "DELETE")
			requireBoundMarker(t, ctx, check, library)
			if err := check.Close(); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, alias); err != nil {
				t.Fatal(err)
			}
			link := requireArchiveAlias(t, alias, target, nil)
			t.Log("moved literal archive, binding, marker and alias verified before subject")
			var opened *store.Store
			if caller == "apple" {
				opened, err = openAppleArchive(ctx, alias, library)
			} else {
				opened, err = openArchiveStore(ctx, alias)
			}
			if err != nil {
				t.Fatal(err)
			}
			defer opened.Close()
			requireBoundMarker(t, ctx, opened.DB(), library)
			requireArchiveAlias(t, alias, target, link)
			requireArchivePathAbsent(t, strings.TrimSpace(target))
			requireArchiveAliasSidecarsAbsent(t, alias)
		})
	}
}

func TestArchiveWhitespaceTargetForeignRefusal(t *testing.T) {
	for _, journal := range []string{"DELETE", "WAL"} {
		for _, caller := range []string{"archive", "init", "apple"} {
			t.Run(journal+"/"+caller, func(t *testing.T) {
				ctx := context.Background()
				root, err := filepath.EvalSymlinks(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				target := filepath.Join(root, "foreign.db ")
				alias := filepath.Join(root, "alias.db")
				writer, err := sql.Open("sqlite", target)
				if err != nil {
					t.Fatal(err)
				}
				defer writer.Close()
				writer.SetMaxOpenConns(1)
				execTestSQL(t, writer, "pragma journal_mode="+journal)
				execTestSQL(t, writer, "pragma wal_autocheckpoint=0")
				execTestSQL(t, writer, "create table foreign_marker(value text)")
				execTestSQL(t, writer, "insert into foreign_marker values('committed-foreign-row')")
				requireLiteralArchiveTarget(t, writer, target, journal)
				var marker string
				if err := writer.QueryRow("select value from foreign_marker").Scan(&marker); err != nil || marker != "committed-foreign-row" {
					t.Fatalf("fixture marker = %q: %v", marker, err)
				}
				if err := os.Chmod(target, 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, alias); err != nil {
					t.Fatal(err)
				}
				link := requireArchiveAlias(t, alias, target, nil)
				before := pathFixtureFiles(t, target)
				if journal == "WAL" && len(before["-wal"].data) == 0 {
					t.Fatal("fixture has no committed WAL")
				}
				t.Log("literal foreign filename, identity, journal, marker and alias verified before subject")
				var opened *store.Store
				switch caller {
				case "archive":
					opened, err = openArchiveStore(ctx, alias)
				case "init":
					_, err = Init(ctx, Paths{Database: alias})
				case "apple":
					opened, err = openAppleArchive(ctx, alias, filepath.Join(root, "synthetic.photoslibrary"))
				}
				if opened != nil {
					opened.Close()
				}
				if err == nil {
					t.Error("foreign target accepted")
				}
				// Compare while the source connection still owns its committed WAL.
				after := pathFixtureFiles(t, target)
				if !reflect.DeepEqual(before, after) {
					for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
						want, had := before[suffix]
						got, has := after[suffix]
						if had != has || !reflect.DeepEqual(want, got) {
							t.Errorf("foreign %q changed: present %t -> %t, mode %v -> %v, bytes %d -> %d",
								suffix, had, has, want.mode, got.mode, len(want.data), len(got.data))
						}
					}
				}
				requireArchiveAlias(t, alias, target, link)
				requireArchiveAliasSidecarsAbsent(t, alias)
				requireArchivePathAbsent(t, strings.TrimSpace(target))
			})
		}
	}
}

func requireLiteralArchiveTarget(t *testing.T, db *sql.DB, target, journal string) {
	t.Helper()
	var seq int
	var name, filename, mode string
	if err := db.QueryRow("pragma database_list").Scan(&seq, &name, &filename); err != nil {
		t.Fatal(err)
	}
	if name != "main" || filename != target {
		t.Fatalf("fixture SQLite filename = %q (%q), want literal %q", filename, name, target)
	}
	selected, err := os.Stat(filename)
	if err != nil {
		t.Fatal(err)
	}
	literal, err := os.Stat(target)
	if err != nil || !os.SameFile(selected, literal) {
		t.Fatalf("fixture literal identity mismatch: %v", err)
	}
	if err := db.QueryRow("pragma journal_mode").Scan(&mode); err != nil || !strings.EqualFold(mode, journal) {
		t.Fatalf("fixture journal mode = %q, want %q: %v", mode, journal, err)
	}
	requireArchivePathAbsent(t, strings.TrimSpace(target))
}

func requireBoundMarker(t *testing.T, ctx context.Context, db *sql.DB, library string) {
	t.Helper()
	id, err := libraryIdentity(ctx, db, library)
	if err != nil || id != "retained-library" {
		t.Fatalf("retained binding = %q: %v", id, err)
	}
	var marker string
	if err := db.QueryRow("select value from retained_marker").Scan(&marker); err != nil || marker != "bound-row" {
		t.Fatalf("retained marker = %q: %v", marker, err)
	}
}

func requireArchiveAlias(t *testing.T, alias, target string, previous os.FileInfo) os.FileInfo {
	t.Helper()
	link, err := os.Lstat(alias)
	if err != nil || link.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("fixture alias is not a symlink: %v", err)
	}
	if previous != nil && (!os.SameFile(previous, link) || previous.Mode() != link.Mode()) {
		t.Fatal("alias identity or mode changed")
	}
	resolved, err := filepath.EvalSymlinks(alias)
	if err != nil || resolved != target {
		t.Fatalf("fixture alias resolves to %q, want %q: %v", resolved, target, err)
	}
	selected, err := os.Stat(alias)
	if err != nil {
		t.Fatal(err)
	}
	literal, err := os.Stat(target)
	if err != nil || !os.SameFile(selected, literal) {
		t.Fatalf("fixture alias identity mismatch: %v", err)
	}
	return link
}

func requireArchivePathAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("unexpected path %q: %v", path, err)
	}
}

func requireArchiveAliasSidecarsAbsent(t *testing.T, alias string) {
	t.Helper()
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		requireArchivePathAbsent(t, alias+suffix)
	}
}

func TestArchiveOpenValidatesActualSQLiteFilename(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "a", "b"), 0o700); err != nil {
		t.Fatal(err)
	}
	valid := filepath.Join(root, "a", "archive.db")
	archive, err := openArchiveStore(ctx, valid)
	if err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(root, "archive.db")
	db, err := sql.Open("sqlite", foreign)
	if err != nil {
		t.Fatal(err)
	}
	execTestSQL(t, db, "pragma journal_mode=DELETE")
	execTestSQL(t, db, "create table foreign_marker(value text)")
	execTestSQL(t, db, "insert into foreign_marker values ('synthetic-only')")
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(foreign, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "a", "b"), filepath.Join(root, "alias")); err != nil {
		t.Fatal(err)
	}
	// Join would erase the static spelling whose two interpretations differ.
	raw := root + "/alias/../archive.db"
	actual, err := filepath.Abs(raw)
	if err != nil || actual != foreign {
		t.Fatalf("actual SQLite filename = %q: %v", actual, err)
	}
	rawInfo, err := os.Stat(raw)
	if err != nil {
		t.Fatal(err)
	}
	validInfo, err := os.Stat(valid)
	if err != nil || !os.SameFile(rawInfo, validInfo) {
		t.Fatalf("raw path did not select valid fixture: %v", err)
	}
	before, validBefore := pathFixtureFiles(t, foreign), pathFixtureFiles(t, valid)
	opened, err := openArchiveStore(ctx, raw)
	if opened != nil {
		opened.Close()
	}
	if err == nil {
		t.Error("foreign actual filename accepted")
	}
	if !reflect.DeepEqual(before, pathFixtureFiles(t, foreign)) {
		t.Error("foreign bytes, modes or sidecar inventory changed")
	}
	if !reflect.DeepEqual(validBefore, pathFixtureFiles(t, valid)) {
		t.Error("raw-path valid archive changed")
	}
}

type pathFixtureFile struct {
	mode os.FileMode
	data []byte
}

func pathFixtureFiles(t *testing.T, path string) map[string]pathFixtureFile {
	t.Helper()
	files := map[string]pathFixtureFile{}
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
		files[suffix] = pathFixtureFile{info.Mode(), data}
	}
	return files
}

func TestArchiveOpenPathCompatibility(t *testing.T) {
	for _, kind := range []string{"symlink", "dangling", "literal-uri-name"} {
		t.Run(kind, func(t *testing.T) {
			t.Chdir(t.TempDir())
			path, target := "alias.db", "target.db"
			if kind == "literal-uri-name" {
				path, target = "file:literal.db", "file:literal.db"
			} else {
				if kind == "symlink" {
					db, err := openArchiveStore(context.Background(), target)
					if err != nil {
						t.Fatal(err)
					}
					db.Close()
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			}
			db, err := openArchiveStore(context.Background(), path)
			if err != nil {
				t.Fatal(err)
			}
			db.Close()
			if _, err := os.Stat(target); err != nil {
				t.Fatal(err)
			}
			if kind == "literal-uri-name" {
				if _, err := os.Stat("literal.db"); !os.IsNotExist(err) {
					t.Fatalf("literal filename interpreted as URI: %v", err)
				}
			}
		})
	}
}
