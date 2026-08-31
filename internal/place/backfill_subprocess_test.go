package place

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == RawContextCommand && os.Getenv("PHOTOSCRAWL_TEST_PLACE_RAW_CHILD") == "1" {
		if err := runRawContextTestChild(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func runRawContextTestChild() error {
	fs := flag.NewFlagSet(RawContextCommand, flag.ContinueOnError)
	inputPath := fs.String("input", "", "")
	radius := fs.Float64("radius", 0, "")
	if err := fs.Parse(os.Args[2:]); err != nil {
		return err
	}
	if fs.NArg() != 0 || *inputPath != "-" || *radius != backfillRadiusMeters {
		return fmt.Errorf("expected stdin input and explicit backfill radius, got %v", os.Args[2:])
	}
	var input Input
	if err := json.NewDecoder(os.Stdin).Decode(&input); err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(Result{
		Input:        input,
		Provider:     "synthetic",
		RadiusMeters: *radius,
		Address:      &Address{Formatted: "Synthetic address"},
		POIStatus:    POIStatusNone,
	})
}

func TestBackfillRawSubprocess(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "photos.sqlite")
	outDir := filepath.Join(dir, "backfill")
	if err := writeBackfillFixtureDB(dbPath, 2); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	_, insertErr := db.Exec(`
insert into asset values ('asset:3', '2026-05-31T12:00:00Z');
insert into location_observation
select 'asset:3', latitude, longitude, horizontal_accuracy
from location_observation where asset_id = 'asset:1';
`)
	closeErr := db.Close()
	if insertErr != nil {
		t.Fatal(insertErr)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}

	t.Setenv("PHOTOSCRAWL_TEST_PLACE_RAW_CHILD", "1")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	summary, backfillErr := Backfill(ctx, BackfillOptions{DatabasePath: dbPath, OutputDir: outDir})
	for _, name := range []string{"attempts", "errors"} {
		paths, err := filepath.Glob(filepath.Join(outDir, name, "*"))
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range paths {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(data), "usage:") {
				t.Errorf("%s contains a usage error: %s", path, data)
			}
		}
		if name == "attempts" && len(paths) != 2 {
			t.Errorf("attempt files = %d, want 2", len(paths))
		}
		if name == "errors" && len(paths) != 0 {
			t.Errorf("error files = %d, want 0", len(paths))
		}
	}
	if backfillErr != nil {
		t.Fatal(backfillErr)
	}
	if summary.LocatedAssets != 3 || summary.LocationKeys != 2 || summary.Successes != 2 || summary.FinalFailures != 0 || summary.ProviderAttempts != 2 || summary.AttemptFailures != 0 {
		t.Fatalf("unexpected backfill summary: %+v", summary)
	}

	data, err := os.ReadFile(filepath.Join(outDir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var keys []backfillKey
	if err := json.Unmarshal(data, &keys); err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 {
		t.Fatalf("manifest keys = %d, want 2", len(keys))
	}
	want := []Coordinate{
		{Latitude: 52.379189, Longitude: 4.899431},
		{Latitude: 52.389189, Longitude: 4.899431},
	}
	for i, key := range keys {
		path := filepath.Join(outDir, "outputs", key.filename()+".json")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var result Result
		if err := json.Unmarshal(data, &result); err != nil {
			t.Fatal(err)
		}
		if !sameCoordinate(result.Input.Location.Latitude, want[i].Latitude) || !sameCoordinate(result.Input.Location.Longitude, want[i].Longitude) || result.Input.AccuracyMeters != 8.5 || result.RadiusMeters != backfillRadiusMeters {
			t.Fatalf("unexpected raw result: %+v", result)
		}
		if !outputMatches(path, key) {
			t.Errorf("output does not match manifest key: %+v", key)
		}
	}
	data, err = os.ReadFile(filepath.Join(outDir, "summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	var persisted BackfillResult
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted != summary {
		t.Fatalf("persisted summary = %+v, want %+v", persisted, summary)
	}
}
