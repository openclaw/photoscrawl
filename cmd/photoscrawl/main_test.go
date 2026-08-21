package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestWriteVersion(t *testing.T) {
	previous := version
	version = "0.1.0-test"
	t.Cleanup(func() { version = previous })

	var out bytes.Buffer
	if err := writeVersion(&out); err != nil {
		t.Fatalf("writeVersion failed: %v", err)
	}
	if got := out.String(); got != "0.1.0-test\n" {
		t.Fatalf("version output = %q", got)
	}
}

func TestJoinedQueryPreservesLauncherArguments(t *testing.T) {
	if got := joinedQuery("hello", []string{"world", "photos"}); got != "hello world photos" {
		t.Fatalf("joined query = %q", got)
	}
	if got := joinedQuery("", []string{"hello", "world"}); got != "hello world" {
		t.Fatalf("positional query = %q", got)
	}
}

func TestPlaceBackfillRunReturnsCanceledDuringRetrySleep(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "photos.sqlite")
	outDir := filepath.Join(dir, "backfill")
	if err := writePlaceBackfillRetryFixture(dbPath, outDir); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	started := time.Now()
	go func() {
		errCh <- run(ctx, []string{"place-backfill", "--db", dbPath, "--out", outDir, "--json"})
	}()
	time.Sleep(80 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("place-backfill run error = %v, want context.Canceled", err)
		}
		if elapsed := time.Since(started); elapsed >= 2*time.Second {
			t.Fatalf("place-backfill ignored cancel for %s", elapsed)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("place-backfill did not return after cancel during retry sleep")
	}
}

func writePlaceBackfillRetryFixture(dbPath, outDir string) error {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.Exec(`
create table asset (id text primary key, creation_date text);
create table location_observation (
  asset_id text,
  latitude real,
  longitude real,
  horizontal_accuracy real
);
insert into asset values ('asset:1', '2026-05-30T12:00:00Z');
insert into location_observation values ('asset:1', 52.379189, 4.899431, 8.5);
`); err != nil {
		return err
	}
	for _, name := range []string{"outputs", "errors", "attempts", "logs"} {
		if err := os.MkdirAll(filepath.Join(outDir, name), 0o700); err != nil {
			return err
		}
	}
	return os.WriteFile(filepath.Join(outDir, "attempts", "000000.jsonl"), []byte("{}\n"), 0o600)
}
