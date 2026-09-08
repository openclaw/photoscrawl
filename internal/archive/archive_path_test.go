package archive

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/openclaw/crawlkit/store"
)

func TestArchiveFilenameWhitespace(t *testing.T) {
	t.Chdir(t.TempDir())
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"archive.db ", "archive.db\t", "archive.db\u00a0", " archive.db "} {
		got, err := archiveFilename(raw)
		want := filepath.Join(root, "archive.db")
		if raw[0] == ' ' {
			want = filepath.Join(root, " archive.db")
		}
		if err != nil || got != want {
			t.Fatalf("filename %q = %q, %v; want %q", raw, got, err, want)
		}
		if again, err := archiveFilename(got); err != nil || again != got {
			t.Fatalf("filename is not stable: %q, %v", again, err)
		}
	}
}

func TestArchiveWhitespaceRefusesUnstableFilenameBeforeMkdir(t *testing.T) {
	for _, final := range []string{". ", ".. ", " \t"} {
		for _, operation := range []string{"writable", "readonly", "apple", "init"} {
			t.Run(final+"/"+operation, func(t *testing.T) {
				parent := filepath.Join(t.TempDir(), "new-parent")
				raw := parent + string(filepath.Separator) + final
				if err := openWhitespaceArchive(t, operation, raw, "synthetic.photoslibrary"); err == nil {
					t.Fatal("unstable archive filename accepted")
				}
				if _, err := os.Stat(parent); !os.IsNotExist(err) {
					t.Fatalf("unstable filename created a parent: %v", err)
				}
			})
		}
	}
}

func TestArchiveWhitespaceRejectsForeignTarget(t *testing.T) {
	for _, journal := range []string{"DELETE", "WAL"} {
		for _, suffix := range []string{"", " ", "\t", "\u00a0"} {
			for _, decoy := range []bool{false, true} {
				if suffix == "" && decoy {
					continue
				}
				for _, operation := range []string{"writable", "readonly", "apple", "init"} {
					name := journal + "/" + suffix + "/" + map[bool]string{false: "absent", true: "decoy"}[decoy] + "/" + operation
					t.Run(name, func(t *testing.T) {
						root := t.TempDir()
						target := filepath.Join(root, "foreign.db")
						library := filepath.Join(root, "Synthetic.photoslibrary")
						db := createWhitespaceForeignFixture(t, target, journal)
						defer db.Close()
						raw := target + suffix
						if decoy {
							createWhitespaceArchiveFixture(t, raw, library)
						}
						before, decoyBefore := pathFixtureFiles(t, target), pathFixtureFiles(t, raw)
						if err := openWhitespaceArchive(t, operation, raw, library); err == nil {
							t.Fatal("foreign effective filename accepted")
						}
						if operation == "readonly" && journal == "WAL" {
							assertReadonlyWALTarget(t, target, before)
						} else {
							assertWhitespaceFiles(t, target, before)
						}
						if raw != target {
							assertWhitespaceFiles(t, raw, decoyBefore)
						}
					})
				}
			}
		}
	}
}

func TestAppleArchiveWhitespaceChecksEffectiveBinding(t *testing.T) {
	for _, suffix := range []string{" ", "\t", "\u00a0"} {
		t.Run(suffix, func(t *testing.T) {
			root := t.TempDir()
			target := filepath.Join(root, "archive.db")
			otherLibrary := filepath.Join(root, "Other.photoslibrary")
			requestedLibrary := filepath.Join(root, "Requested.photoslibrary")
			createWhitespaceArchiveFixture(t, target, otherLibrary)
			raw := target + suffix
			createWhitespaceArchiveFixture(t, raw, requestedLibrary)
			before, decoyBefore := pathFixtureFiles(t, target), pathFixtureFiles(t, raw)
			if err := openWhitespaceArchive(t, "apple", raw, requestedLibrary); err == nil {
				t.Fatal("decoy binding authorized a different effective archive")
			}
			assertWhitespaceFiles(t, target, before)
			assertWhitespaceFiles(t, raw, decoyBefore)
		})
	}
}

func TestArchiveWhitespacePositiveOpen(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "archive.db")
	library := filepath.Join(root, "Synthetic.photoslibrary")
	createWhitespaceArchiveFixture(t, target, library)
	for _, raw := range []string{target + " ", target, target + "\t", target + "\u00a0"} {
		for _, operation := range []string{"writable", "readonly", "apple", "init"} {
			if err := openWhitespaceArchive(t, operation, raw, library); err != nil {
				t.Fatalf("%s through %q: %v", operation, raw, err)
			}
		}
		if raw != target {
			if _, err := os.Stat(raw); !os.IsNotExist(err) {
				t.Fatalf("literal whitespace archive unexpectedly created: %v", err)
			}
		}
	}
}

func TestInitWhitespaceCreatesEffectiveFilenameAndKeepsDisplay(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "new-parent")
	target := filepath.Join(parent, "archive.db")
	if err := openWhitespaceArchive(t, "init", target+" ", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(target + " "); !os.IsNotExist(err) {
		t.Fatalf("literal whitespace archive unexpectedly created: %v", err)
	}
}

func openWhitespaceArchive(t *testing.T, operation, path, library string) error {
	t.Helper()
	ctx := context.Background()
	if operation == "init" {
		result, err := Init(ctx, Paths{Database: path})
		if err == nil && result.Database != path {
			t.Fatalf("Init changed the display spelling: %q", result.Database)
		}
		return err
	}
	var db *store.Store
	var err error
	switch operation {
	case "writable":
		db, err = openArchiveStore(ctx, path)
	case "readonly":
		db, err = openArchiveReadOnly(ctx, path)
	case "apple":
		db, err = openAppleArchive(ctx, path, library)
	default:
		t.Fatalf("unknown test operation: %s", operation)
	}
	if db != nil {
		defer db.Close()
		if err == nil {
			want, pathErr := archiveFilename(path)
			if pathErr != nil || db.Path() != want {
				t.Fatalf("wrong archive filename: %q, want %q, %v", db.Path(), want, pathErr)
			}
		}
	}
	return err
}

func createWhitespaceForeignFixture(t *testing.T, path, journal string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	execTestSQL(t, db, "pragma journal_mode="+journal)
	execTestSQL(t, db, "create table foreign_marker(value text)")
	execTestSQL(t, db, "insert into foreign_marker values ('synthetic-only')")
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	return db
}

func createWhitespaceArchiveFixture(t *testing.T, path, library string) {
	t.Helper()
	seed := filepath.Join(t.TempDir(), "seed.db")
	db, err := openArchiveStore(context.Background(), seed)
	if err != nil {
		t.Fatal(err)
	}
	execTestSQL(t, db.DB(), `insert into source_library values('synthetic-library', ?, '', '', '', '{}')`, library)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	// Seed through an ordinary filename, then retain the literal decoy spelling.
	if err := os.Rename(seed, path); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertWhitespaceFiles(t *testing.T, path string, before map[string]pathFixtureFile) {
	t.Helper()
	after := pathFixtureFiles(t, path)
	if reflect.DeepEqual(before, after) {
		return
	}
	for suffix, want := range before {
		got, ok := after[suffix]
		if !ok {
			t.Errorf("fixture sidecar %q disappeared", suffix)
			continue
		}
		if want.mode != got.mode || !bytes.Equal(want.data, got.data) {
			offset := 0
			for offset < len(want.data) && offset < len(got.data) && want.data[offset] == got.data[offset] {
				offset++
			}
			t.Errorf("fixture %q changed: mode %v -> %v, bytes %d -> %d, first difference %d",
				suffix, want.mode, got.mode, len(want.data), len(got.data), offset)
		}
	}
	for suffix := range after {
		if _, ok := before[suffix]; !ok {
			t.Errorf("fixture sidecar %q appeared", suffix)
		}
	}
}

func assertReadonlyWALTarget(t *testing.T, path string, before map[string]pathFixtureFile) {
	t.Helper()
	after := pathFixtureFiles(t, path)
	if len(before) != len(after) {
		t.Errorf("read-only target sidecar inventory changed: %d -> %d", len(before), len(after))
	}
	for suffix, want := range before {
		got, ok := after[suffix]
		if !ok {
			t.Errorf("read-only target file %q disappeared", suffix)
			continue
		}
		if want.mode != got.mode || len(want.data) != len(got.data) {
			t.Errorf("read-only target %q changed: mode %v -> %v, bytes %d -> %d",
				suffix, want.mode, got.mode, len(want.data), len(got.data))
			continue
		}
		for offset, value := range want.data {
			// Direct SQLite readers may update WAL reader-mark slots 1-4.
			if suffix == "-shm" && offset >= 104 && offset < 120 {
				continue
			}
			if value != got.data[offset] {
				t.Errorf("read-only target %q changed outside reader marks at offset %d", suffix, offset)
				break
			}
		}
	}
}
