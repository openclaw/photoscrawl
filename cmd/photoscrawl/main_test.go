package main

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openclaw/crawlkit/output"
	"github.com/openclaw/photoscrawl/internal/place"
	_ "modernc.org/sqlite"
)

func TestPlaceContextRawDispatchParsesInput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "invalid.json")
	if err := os.WriteFile(path, []byte("{invalid"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := run(context.Background(), []string{place.RawContextCommand, "--input", path})
	if output.IsUsage(err) {
		t.Fatalf("raw context command returned a usage error: %v", err)
	}
	var syntaxErr *json.SyntaxError
	if !errors.As(err, &syntaxErr) || !strings.HasPrefix(err.Error(), "read place input:") {
		t.Fatalf("raw context error = %v, want an input JSON parse error", err)
	}
}

func TestCommandContextSecondInterruptTerminates(t *testing.T) {
	if os.Getenv("PHOTOSCRAWL_TEST_SIGNAL_CHILD") == "1" {
		ctx, stop := commandContext()
		defer stop()
		fmt.Println("ready")
		<-ctx.Done()
		fmt.Println("canceled")
		select {}
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestCommandContextSecondInterruptTerminates$")
	cmd.Env = append(os.Environ(), "PHOTOSCRAWL_TEST_SIGNAL_CHILD=1")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() || scanner.Text() != "ready" {
		t.Fatal("child did not install signal handling")
	}
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	if !scanner.Scan() || scanner.Text() != "canceled" {
		t.Fatal("first interrupt did not cancel the context")
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	// Allow the cancellation goroutine to unregister before the next signal.
	time.Sleep(50 * time.Millisecond)
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil || ctx.Err() != nil {
			t.Fatalf("second interrupt did not terminate child: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second interrupt was swallowed")
	}
}

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
