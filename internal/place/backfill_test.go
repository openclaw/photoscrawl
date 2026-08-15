package place

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestSleepContextReturnsCanceledDuringWait(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	started := time.Now()
	errCh := make(chan error, 1)
	go func() {
		errCh <- sleepContext(ctx, 2*time.Minute)
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("sleepContext error = %v, want context.Canceled", err)
		}
		if elapsed := time.Since(started); elapsed >= 2*time.Second {
			t.Fatalf("sleepContext ignored cancel for %s", elapsed)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("sleepContext did not return after cancel during backoff")
	}
}

func TestSleepContextCompletesWhenContextStaysOpen(t *testing.T) {
	t.Parallel()
	if err := sleepContext(context.Background(), 15*time.Millisecond); err != nil {
		t.Fatalf("sleepContext = %v, want nil", err)
	}
}

func TestBackfillRetryDelayKeepsBackoffSchedule(t *testing.T) {
	t.Parallel()
	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{2, 2 * time.Minute},
		{3, 10 * time.Minute},
		{4, 30 * time.Minute},
		{5, 30 * time.Minute},
	}
	for _, tc := range cases {
		if got := backfillRetryDelay(tc.attempt); got != tc.want {
			t.Fatalf("backfillRetryDelay(%d) = %s, want %s", tc.attempt, got, tc.want)
		}
	}
}

func TestBackfillHonorsCancelDuringRetrySleep(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "photos.sqlite")
	outDir := filepath.Join(dir, "backfill")
	if err := writeBackfillFixtureDB(dbPath); err != nil {
		t.Fatal(err)
	}
	if err := ensureBackfillDirs(outDir); err != nil {
		t.Fatal(err)
	}
	// One recorded attempt makes round 2 eligible, so Backfill hits the retry sleep.
	attemptPath := filepath.Join(outDir, "attempts", "000000.jsonl")
	if err := os.WriteFile(attemptPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	started := time.Now()
	go func() {
		_, err := Backfill(ctx, BackfillOptions{DatabasePath: dbPath, OutputDir: outDir})
		errCh <- err
	}()
	time.Sleep(40 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Backfill error = %v, want context.Canceled", err)
		}
		if elapsed := time.Since(started); elapsed >= 2*time.Second {
			t.Fatalf("Backfill ignored cancel during retry sleep for %s", elapsed)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Backfill did not return after cancel during retry sleep")
	}
}

func writeBackfillFixtureDB(path string) error {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.Exec(`
create table asset (id text primary key, creation_date text);
create table location_observation (
  asset_id text,
  latitude real,
  longitude real,
  horizontal_accuracy real
);
insert into asset values ('asset:1', '2026-05-30T12:00:00Z');
insert into location_observation values ('asset:1', 52.379189, 4.899431, 8.5);
`)
	return err
}
