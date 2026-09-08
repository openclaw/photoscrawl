package archive

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestArchivePreflightRejectsPopulatedHardlinks(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	source, alias := filepath.Join(root, "archive.db"), filepath.Join(root, "alias.db")
	db, err := openArchiveStore(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	writer, err := sql.Open("sqlite", source)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	execTestSQL(t, writer, "pragma journal_mode=WAL")
	execTestSQL(t, writer, "create table committed_in_wal(value text)")
	execTestSQL(t, writer, "insert into committed_in_wal values ('synthetic')")
	if err := os.Link(source, alias); err != nil {
		t.Fatal(err)
	}
	before, modes := map[string][]byte{}, map[string]os.FileMode{}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		before[suffix], err = os.ReadFile(source + suffix)
		if err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(source + suffix)
		if err != nil {
			t.Fatal(err)
		}
		modes[suffix] = info.Mode()
	}
	for _, candidate := range []string{source, alias} {
		archive, err := openArchiveStore(ctx, candidate)
		if err == nil {
			archive.Close()
			t.Fatalf("populated hardlink %s accepted", filepath.Base(candidate))
		}
		if !strings.Contains(err.Error(), "hardlinked") {
			t.Fatalf("hardlink refusal lost: %v", err)
		}
	}
	for suffix, want := range before {
		got, err := os.ReadFile(source + suffix)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("source %q changed: %v", suffix, err)
		}
		info, err := os.Stat(source + suffix)
		if err != nil || info.Mode() != modes[suffix] {
			t.Fatalf("source %q mode changed: %v", suffix, err)
		}
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Stat(alias + suffix); !os.IsNotExist(err) {
			t.Fatalf("alias sidecar %q created: %v", suffix, err)
		}
	}
}

func TestArchivePreflightRejectsUnownedSidecars(t *testing.T) {
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		t.Run(suffix, func(t *testing.T) {
			root := t.TempDir()
			foreign, path := filepath.Join(root, "foreign.db"), filepath.Join(root, "archive.db")
			if err := os.WriteFile(foreign, []byte("caller-owned bytes"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(foreign, path+suffix); err != nil {
				t.Fatal(err)
			}
			if db, err := openArchiveStore(context.Background(), path); err == nil {
				db.Close()
				t.Fatal("unowned sidecar accepted")
			}
			if got, err := os.ReadFile(foreign); err != nil || string(got) != "caller-owned bytes" {
				t.Fatalf("foreign sidecar target changed: %v", err)
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("archive created before rejection: %v", err)
			}
		})
	}
}

func TestArchivePreflightAllowsOrdinaryEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "archive.db")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := openArchiveStore(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
}

func TestArchivePreflightRejectsSidecarsBesideEmptyArchive(t *testing.T) {
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		t.Run(suffix, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "archive.db")
			if err := os.WriteFile(path, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			orphan := []byte("synthetic orphan sidecar")
			if err := os.WriteFile(path+suffix, orphan, 0o600); err != nil {
				t.Fatal(err)
			}
			if db, err := openArchiveStore(context.Background(), path); err == nil {
				db.Close()
				t.Error("orphan sidecar beside empty archive accepted")
			}
			if info, err := os.Stat(path); err != nil || info.Size() != 0 {
				t.Fatalf("empty archive changed: %v", err)
			}
			if got, err := os.ReadFile(path + suffix); err != nil || !bytes.Equal(got, orphan) {
				t.Fatalf("orphan %s changed: %v", suffix, err)
			}
		})
	}
}

func TestArchivePreflightPreservesForeignSource(t *testing.T) {
	for _, journal := range []string{"DELETE", "WAL"} {
		t.Run(journal, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "Photos.sqlite")
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			execTestSQL(t, db, "pragma journal_mode="+journal)
			execTestSQL(t, db, "create table ZASSET(ZUUID text)")
			if err := os.Chmod(path, 0o644); err != nil {
				t.Fatal(err)
			}
			before, modes := map[string][]byte{}, map[string]os.FileMode{}
			for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
				data, err := os.ReadFile(path + suffix)
				if os.IsNotExist(err) {
					continue
				}
				if err != nil {
					t.Fatal(err)
				}
				info, err := os.Stat(path + suffix)
				if err != nil {
					t.Fatal(err)
				}
				before[suffix], modes[suffix] = data, info.Mode()
			}
			link := filepath.Join(root, "alias.sqlite")
			if err := os.Link(path, link); err != nil {
				t.Fatal(err)
			}
			for _, candidate := range []string{path, link} {
				if archive, err := openArchiveStore(context.Background(), candidate); err == nil {
					_ = archive.Close()
					t.Fatal("foreign source accepted")
				}
			}
			for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
				got, err := os.ReadFile(path + suffix)
				want, existed := before[suffix]
				if !existed && os.IsNotExist(err) {
					continue
				}
				if err != nil || !existed || !bytes.Equal(got, want) {
					t.Fatalf("source %q changed: %v", suffix, err)
				}
				info, err := os.Stat(path + suffix)
				if err != nil || info.Mode() != modes[suffix] {
					t.Fatalf("source %q mode changed: %v", suffix, err)
				}
			}
		})
	}
}
