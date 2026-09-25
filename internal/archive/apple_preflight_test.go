package archive

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openclaw/crawlkit/store"
)

func TestAppleArchivePreflightRefusesBeforeMainDatabaseMutation(t *testing.T) {
	for _, fixture := range []string{"missing", "zero", "empty-sqlite", "foreign", "foreign-wal", "wrong-library", "legacy-wrong-library", "future", "ambiguous"} {
		t.Run(fixture, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			path, library := filepath.Join(root, "archive.db"), filepath.Join(root, "Library.photoslibrary")
			switch fixture {
			case "missing":
				path = filepath.Join(root, "absent", "archive.db")
			case "zero":
				if err := os.WriteFile(path, nil, 0o644); err != nil {
					t.Fatal(err)
				}
			case "empty-sqlite", "foreign", "foreign-wal":
				db, err := sql.Open("sqlite", path)
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				execTestSQL(t, db, "vacuum")
				if fixture == "foreign-wal" {
					execTestSQL(t, db, "pragma journal_mode=WAL")
				}
				if fixture != "empty-sqlite" {
					execTestSQL(t, db, "create table foreign_marker(value text)")
				}
			default:
				schema, version := Schema, SchemaVersion
				if fixture == "legacy-wrong-library" {
					schema, version = schemaV1Fixture, 1
				} else if fixture == "future" {
					version++
				}
				db, err := store.Open(ctx, store.Options{Path: path, Schema: schema, SchemaVersion: version})
				if err != nil {
					t.Fatal(err)
				}
				binding := library
				if strings.Contains(fixture, "wrong-library") {
					binding = filepath.Join(root, "Other.photoslibrary")
				}
				execTestSQL(t, db.DB(), `insert into source_library values('one', ?, '', '', '', '{}')`, binding)
				if fixture == "ambiguous" {
					execTestSQL(t, db.DB(), `insert into source_library values('two', ?, '', '', '', '{}')`, binding)
				}
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if fixture != "missing" {
				if err := os.Chmod(path, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			before := pathFixtureFiles(t, path)
			identities := preflightFileIdentities(t, path)
			scratch := t.TempDir()
			t.Setenv("TMPDIR", scratch)
			if db, err := openAppleArchive(ctx, path, library); err == nil {
				db.Close()
				t.Fatal("unbound or unsupported archive accepted")
			}
			assertArchiveMainUnchanged(t, path, before)
			assertPreflightExistingFileIdentities(t, path, identities)
			assertPreflightScratchEmpty(t, scratch)
			if fixture == "missing" {
				requireArchivePathAbsent(t, filepath.Dir(path))
			}
		})
	}
}

func TestAppleArchivePreflightReadsLiveWALAndSnapshotsRollbackJournal(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "current", true: "legacy"}[legacy], func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			path, library := filepath.Join(root, "archive.db"), filepath.Join(root, "Library.photoslibrary")
			schema, version := Schema, SchemaVersion
			if legacy {
				schema, version = schemaV1Fixture, 1
			}
			writer, err := store.Open(ctx, store.Options{Path: path, Schema: schema, SchemaVersion: version})
			if err != nil {
				t.Fatal(err)
			}
			defer writer.Close()
			execTestSQL(t, writer.DB(), "pragma wal_checkpoint(TRUNCATE)")
			execTestSQL(t, writer.DB(), `insert into source_library values('wal-library', ?, '', '', '', '{}')`, library)
			wal, err := os.Stat(path + "-wal")
			if err != nil || wal.Size() == 0 {
				t.Fatalf("binding was not committed to WAL: %v", err)
			}
			if err := os.Chmod(path, 0o644); err != nil {
				t.Fatal(err)
			}
			before := pathFixtureFiles(t, path)
			identities := preflightFileIdentities(t, path)
			blocked := filepath.Join(root, "not-a-directory")
			if err := os.WriteFile(blocked, []byte("synthetic"), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("TMPDIR", blocked)
			if db, err := openAppleArchive(ctx, path, filepath.Join(root, "Other.photoslibrary")); err == nil {
				db.Close()
				t.Fatal("wrong WAL binding accepted")
			} else if !strings.Contains(err.Error(), "requested library is not in the archive") {
				t.Fatalf("WAL binding was not inspected live: %v", err)
			}
			assertPreflightFileIdentities(t, path, identities)

			db, err := openAppleArchive(ctx, path, library)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if got, err := db.SchemaVersion(ctx); err != nil || got != SchemaVersion {
				t.Fatalf("schema version = %d, %v", got, err)
			}
			if got, err := libraryIdentity(ctx, db.DB(), library); err != nil || got != "wal-library" {
				t.Fatalf("binding = %q, %v", got, err)
			}
			if got := pathFixtureFiles(t, path)[""]; string(got.data) != string(before[""].data) {
				t.Fatal("live WAL preflight changed the main database")
			}
		})
	}

	t.Run("empty-wal-and-shm", func(t *testing.T) {
		ctx := context.Background()
		root := t.TempDir()
		path, library := filepath.Join(root, "archive.db"), filepath.Join(root, "Library.photoslibrary")
		writer, err := store.Open(ctx, store.Options{Path: path, Schema: Schema, SchemaVersion: SchemaVersion})
		if err != nil {
			t.Fatal(err)
		}
		defer writer.Close()
		execTestSQL(t, writer.DB(), `insert into source_library values('wal-library', ?, '', '', '', '{}')`, library)
		execTestSQL(t, writer.DB(), "pragma wal_checkpoint(TRUNCATE)")
		if wal, err := os.Stat(path + "-wal"); err != nil || wal.Size() != 0 {
			t.Fatalf("empty WAL fixture = %v, %v", wal, err)
		}
		if shm, err := os.Stat(path + "-shm"); err != nil || shm.Size() == 0 {
			t.Fatalf("SHM fixture = %v, %v", shm, err)
		}
		blocked := filepath.Join(root, "not-a-directory")
		if err := os.WriteFile(blocked, []byte("synthetic"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("TMPDIR", blocked)
		db, err := openAppleArchive(ctx, path, library)
		if err != nil {
			t.Fatalf("empty live WAL required a snapshot: %v", err)
		}
		db.Close()
	})

	t.Run("rollback-journal", func(t *testing.T) {
		ctx := context.Background()
		root := t.TempDir()
		path := filepath.Join(root, "archive.db")
		archive, err := store.Open(ctx, store.Options{Path: path, Schema: Schema, SchemaVersion: SchemaVersion})
		if err != nil {
			t.Fatal(err)
		}
		if err := archive.Close(); err != nil {
			t.Fatal(err)
		}
		writer, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		defer writer.Close()
		execTestSQL(t, writer, "pragma journal_mode=DELETE")
		execTestSQL(t, writer, "create table journal_probe(value text)")
		tx, err := writer.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if _, err := tx.ExecContext(ctx, `insert into journal_probe values('uncommitted')`); err != nil {
			t.Fatal(err)
		}
		if journal, err := os.Stat(path + "-journal"); err != nil || journal.Size() == 0 {
			t.Fatalf("rollback journal fixture = %v, %v", journal, err)
		}
		blocked := filepath.Join(root, "not-a-directory")
		if err := os.WriteFile(blocked, []byte("synthetic"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("TMPDIR", blocked)
		if db, err := openArchiveStore(ctx, path); err == nil {
			db.Close()
			t.Fatal("rollback journal bypassed the snapshot path")
		} else if !strings.Contains(err.Error(), "not a directory") {
			t.Fatalf("rollback journal did not use snapshot scratch space: %v", err)
		}
	})
}

func TestAppleArchivePreflightRejectsFilesystemAliases(t *testing.T) {
	for _, fixture := range []string{"main-hardlink", "sidecar-symlink", "sidecar-hardlink", "orphan"} {
		for _, suffix := range []string{"-wal", "-shm", "-journal"} {
			t.Run(fixture+"/"+suffix, func(t *testing.T) {
				root := t.TempDir()
				path, other := filepath.Join(root, "archive.db"), filepath.Join(root, "other.db")
				library := filepath.Join(root, "Library.photoslibrary")
				if fixture != "orphan" {
					createWhitespaceArchiveFixture(t, path, library)
				}
				if fixture == "main-hardlink" {
					if err := os.Link(path, other); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := os.WriteFile(other, []byte("synthetic sidecar"), 0o644); err != nil {
						t.Fatal(err)
					}
					var err error
					if fixture == "sidecar-symlink" {
						err = os.Symlink(other, path+suffix)
					} else if fixture == "sidecar-hardlink" {
						err = os.Link(other, path+suffix)
					} else {
						err = os.WriteFile(path+suffix, []byte("synthetic orphan"), 0o644)
					}
					if err != nil {
						t.Fatal(err)
					}
				}
				before, otherBefore := pathFixtureFiles(t, path), pathFixtureFiles(t, other)
				identities := preflightFileIdentities(t, path)
				scratch := t.TempDir()
				t.Setenv("TMPDIR", scratch)
				if db, err := openAppleArchive(context.Background(), path, library); err == nil {
					db.Close()
					t.Fatal("unsafe archive or sidecar accepted")
				}
				assertWhitespaceFiles(t, path, before)
				assertWhitespaceFiles(t, other, otherBefore)
				assertPreflightFileIdentities(t, path, identities)
				assertPreflightScratchEmpty(t, scratch)
			})
		}
	}
}

func TestArchivePreflightValidationUsesQuiescentLiveFileAndChecksIdentity(t *testing.T) {
	for _, replace := range []bool{false, true} {
		t.Run(map[bool]string{false: "validator-error", true: "replacement"}[replace], func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			path, library := filepath.Join(root, "archive.db"), filepath.Join(root, "Library.photoslibrary")
			createWhitespaceArchiveFixture(t, path, library)
			original := pathFixtureFiles(t, path)
			foreign, moved := filepath.Join(root, "foreign.db"), filepath.Join(root, "moved.db")
			if err := os.WriteFile(foreign, []byte("synthetic foreign file"), 0o644); err != nil {
				t.Fatal(err)
			}
			foreignBefore := pathFixtureFiles(t, foreign)
			scratch := t.TempDir()
			t.Setenv("TMPDIR", scratch)
			sentinel := errors.New("synthetic validator refusal")
			calls := 0
			db, err := openArchiveStoreWithValidation(ctx, path, func(inspected *sql.DB) error {
				calls++
				if _, err := libraryIdentity(ctx, inspected, library); err != nil {
					t.Fatal(err)
				}
				assertArchiveMainUnchanged(t, path, original)
				entries, err := os.ReadDir(scratch)
				if err != nil || len(entries) != 0 {
					t.Fatalf("quiescent validation scratch entries = %d, %v", len(entries), err)
				}
				if !replace {
					return sentinel
				}
				if err := os.Rename(path, moved); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(foreign, path); err != nil {
					t.Fatal(err)
				}
				return nil
			})
			if db != nil {
				db.Close()
				t.Fatal("writable open succeeded after failed validation")
			}
			if calls != 1 {
				t.Fatalf("validator calls = %d", calls)
			}
			if replace {
				if err == nil || !strings.Contains(err.Error(), "changed identity") {
					t.Fatalf("identity refusal = %v", err)
				}
				assertArchiveMainUnchanged(t, path, foreignBefore)
				assertArchiveMainUnchanged(t, moved, original)
			} else {
				if !errors.Is(err, sentinel) {
					t.Fatalf("validation error = %v", err)
				}
				assertArchiveMainUnchanged(t, path, original)
			}
			assertPreflightScratchEmpty(t, scratch)
		})
	}
}

func preflightFileIdentities(t *testing.T, path string) map[string]os.FileInfo {
	t.Helper()
	result := make(map[string]os.FileInfo)
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		info, err := os.Lstat(path + suffix)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		result[suffix] = info
	}
	return result
}

func assertPreflightFileIdentities(t *testing.T, path string, before map[string]os.FileInfo) {
	t.Helper()
	after := preflightFileIdentities(t, path)
	if len(after) != len(before) {
		t.Fatalf("bundle inventory changed: %d -> %d", len(before), len(after))
	}
	for suffix, want := range before {
		if got := after[suffix]; got == nil || !os.SameFile(want, got) {
			t.Fatalf("bundle identity changed for %q", suffix)
		}
	}
}

func assertPreflightExistingFileIdentities(t *testing.T, path string, before map[string]os.FileInfo) {
	t.Helper()
	after := preflightFileIdentities(t, path)
	for suffix, want := range before {
		if got := after[suffix]; got == nil || !os.SameFile(want, got) {
			t.Fatalf("bundle identity changed for %q", suffix)
		}
	}
}

func assertArchiveMainUnchanged(t *testing.T, path string, before map[string]pathFixtureFile) {
	t.Helper()
	after := pathFixtureFiles(t, path)
	want, exists := before[""]
	got, remains := after[""]
	if exists != remains || (exists && (got.mode != want.mode || string(got.data) != string(want.data))) {
		t.Fatalf("main database changed: before=%v/%d bytes after=%v/%d bytes", want.mode, len(want.data), got.mode, len(got.data))
	}
}

func assertPreflightScratchEmpty(t *testing.T, path string) {
	t.Helper()
	entries, err := os.ReadDir(path)
	if err != nil || len(entries) != 0 {
		t.Fatalf("snapshot cleanup left %d entries: %v", len(entries), err)
	}
}
