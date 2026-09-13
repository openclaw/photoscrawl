package place

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	_ "modernc.org/sqlite"
)

func TestSleepContextReturnsCanceledDuringWait(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		errCh := make(chan error, 1)
		started := time.Now()
		go func() { errCh <- sleepContext(ctx, 2*time.Minute) }()
		synctest.Wait()
		cancel()
		synctest.Wait()
		err := <-errCh
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("sleepContext error = %v, want context.Canceled", err)
		}
		if elapsed := time.Since(started); elapsed != 0 {
			t.Fatalf("cancellation waited for the timer: %s", elapsed)
		}
	})
}

func TestSleepContextCompletesWhenContextStaysOpen(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		if err := sleepContext(context.Background(), 15*time.Millisecond); err != nil {
			t.Fatalf("sleepContext = %v, want nil", err)
		}
	})
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

func TestBackfillHonorsCancelDuringMultiKeyDispatch(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "photos.sqlite")
	outDir := filepath.Join(dir, "backfill")
	if err := writeBackfillFixtureDB(dbPath, 8); err != nil {
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
	time.Sleep(30 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Backfill error = %v, want context.Canceled", err)
		}
		if elapsed := time.Since(started); elapsed >= 2*time.Second {
			t.Fatalf("Backfill ignored cancel during dispatch for %s", elapsed)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Backfill did not return after cancel during multi-key dispatch")
	}
}

func TestBackfillHonorsCancelDuringRetrySleep(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "photos.sqlite")
	outDir := filepath.Join(dir, "backfill")
	if err := writeBackfillFixtureDB(dbPath, 1); err != nil {
		t.Fatal(err)
	}
	if err := ensureBackfillDirs(outDir); err != nil {
		t.Fatal(err)
	}
	keys, _, err := loadBackfillKeys(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(filepath.Join(outDir, "manifest.json"), keys); err != nil {
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

func writeBackfillFixtureDB(path string, keys int) error {
	if keys < 1 {
		keys = 1
	}
	db, err := sql.Open("sqlite", path)
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
`); err != nil {
		return err
	}
	for i := 0; i < keys; i++ {
		id := fmt.Sprintf("asset:%d", i+1)
		if _, err := db.Exec(`insert into asset values (?, '2026-05-30T12:00:00Z')`, id); err != nil {
			return err
		}
		if _, err := db.Exec(
			`insert into location_observation values (?, ?, 4.899431, 8.5)`,
			id,
			52.379189+float64(i)*0.01,
		); err != nil {
			return err
		}
	}
	return nil
}

func TestRunBackfillJobsWriteErrorDoesNotHangSender(t *testing.T) {
	const extraJobs = 4
	jobCount := backfillWorkers + extraJobs
	jobs := make([]backfillKey, jobCount)
	for i := range jobs {
		jobs[i] = backfillKey{Index: i, key: fmt.Sprintf("k%d", i)}
	}

	var seen atomic.Int32
	writeErr := errors.New("write failed")
	state := &backfillRunState{
		outputDir: t.TempDir(),
		limiter:   &backfillLimiter{interval: time.Hour},
	}

	done := make(chan error, 1)
	go func() {
		done <- runBackfillJobs(context.Background(), jobs, 1, state, func(context.Context, backfillKey, int, *backfillRunState) error {
			seen.Add(1)
			return writeErr
		})
	}()

	select {
	case err := <-done:
		if !errors.Is(err, writeErr) {
			t.Fatalf("err = %v, want %v", err, writeErr)
		}
		if got := seen.Load(); got != 1 {
			t.Fatalf("attempted %d jobs after write error, want 1", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("sender hung after worker write errors; attempted %d of %d jobs", seen.Load(), jobCount)
	}
}

func TestRunBackfillJobsCompletesSuccessfulJobs(t *testing.T) {
	jobs := make([]backfillKey, backfillWorkers*3)
	var seen atomic.Int32
	state := &backfillRunState{limiter: &backfillLimiter{}}
	err := runBackfillJobs(context.Background(), jobs, 1, state, func(context.Context, backfillKey, int, *backfillRunState) error {
		seen.Add(1)
		return nil
	})
	if err != nil || int(seen.Load()) != len(jobs) {
		t.Fatalf("attempted %d of %d jobs: %v", seen.Load(), len(jobs), err)
	}
}

func TestRunBackfillJobsCancelsBlockedDispatchAndAttempts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	jobs := make([]backfillKey, backfillWorkers*3)
	started := make(chan struct{}, backfillWorkers)
	done := make(chan error, 1)
	state := &backfillRunState{limiter: &backfillLimiter{}}
	go func() {
		done <- runBackfillJobs(ctx, jobs, 1, state, func(ctx context.Context, _ backfillKey, _ int, _ *backfillRunState) error {
			started <- struct{}{}
			<-ctx.Done()
			return ctx.Err()
		})
	}()
	for i := 0; i < backfillWorkers; i++ {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("workers did not start")
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation left dispatcher blocked")
	}
}
