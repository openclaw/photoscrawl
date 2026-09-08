package archive

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

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
