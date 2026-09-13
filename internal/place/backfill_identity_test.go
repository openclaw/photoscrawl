package place

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func backfillIdentityFixture(t *testing.T, count int) (string, string, []backfillKey) {
	t.Helper()
	root := t.TempDir()
	dbPath, out := filepath.Join(root, "photos.sqlite"), filepath.Join(root, "out")
	if err := writeBackfillFixtureDB(dbPath, count); err != nil {
		t.Fatal(err)
	}
	if err := ensureBackfillDirs(out); err != nil {
		t.Fatal(err)
	}
	keys, _, err := loadBackfillKeys(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	return dbPath, out, keys
}

func writeBackfillSuccess(t *testing.T, out string, key backfillKey) {
	t.Helper()
	result := Result{Input: Input{Location: Coordinate{Latitude: key.Latitude, Longitude: key.Longitude}, AccuracyMeters: key.AccuracyMeters}, Address: &Address{Formatted: "Synthetic address"}, POIStatus: POIStatusNone}
	if err := writeJSONFile(filepath.Join(out, "outputs", key.filename()+".json"), result); err != nil {
		t.Fatal(err)
	}
}

func TestBackfillPreservesRetryIdentityAfterKeyInsertion(t *testing.T) {
	dbPath, out, keys := backfillIdentityFixture(t, 2)
	previous := keys[1]
	previous.Index = 0
	if err := writeJSONFile(filepath.Join(out, "manifest.json"), []backfillKey{previous}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "attempts", "000000.jsonl"), []byte(strings.Repeat("{}\n", backfillMaxAttempts)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(filepath.Join(out, "errors", "000000.json"), backfillError{Index: 0, Final: true, Attempts: backfillMaxAttempts}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PHOTOSCRAWL_TEST_PLACE_RAW_CHILD", "1")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := Backfill(ctx, BackfillOptions{DatabasePath: dbPath, OutputDir: out})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(out, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var current []backfillKey
	if err := json.Unmarshal(data, &current); err != nil {
		t.Fatal(err)
	}
	if len(current) != 2 || current[0].Index != 1 || current[1].Index != 0 {
		t.Fatalf("retry identity changed with row positions: %+v", current)
	}
	if !outputMatches(filepath.Join(out, "outputs", "000001.json"), current[0]) || result.ProviderAttempts != 1 || result.Successes != 1 || result.FinalFailures != 1 {
		t.Fatalf("wrong location retried: %+v", result)
	}
}

func TestBackfillSummaryCountsOnlyCurrentKeys(t *testing.T) {
	dbPath, out, keys := backfillIdentityFixture(t, 1)
	retired := backfillKey{Index: 5, Latitude: 70, Longitude: 10}
	if err := writeJSONFile(filepath.Join(out, "manifest.json"), append(keys, retired)); err != nil {
		t.Fatal(err)
	}
	writeBackfillSuccess(t, out, keys[0])
	writeBackfillSuccess(t, out, retired)
	if err := writeJSONFile(filepath.Join(out, "errors", "000004.json"), backfillError{Index: 4, Final: true}); err != nil {
		t.Fatal(err)
	}
	result, err := Backfill(context.Background(), BackfillOptions{DatabasePath: dbPath, OutputDir: out})
	if err != nil || result.ExistingSuccesses != 1 || result.Successes != 1 || result.FinalFailures != 0 {
		t.Fatalf("stale artifacts counted: %+v, %v", result, err)
	}
	if _, err := os.Stat(filepath.Join(out, "outputs", retired.filename()+".json")); err != nil {
		t.Fatal(err)
	}
}

func TestBackfillKeepsDistinctCoordinatePrecision(t *testing.T) {
	dbPath, out, keys := backfillIdentityFixture(t, 1)
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("insert into asset values ('asset:extra', '2026-05-30T12:00:00Z')"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("insert into location_observation values ('asset:extra', ?, ?, ?)", keys[0].Latitude+1e-9, keys[0].Longitude, keys[0].AccuracyMeters); err != nil {
		t.Fatal(err)
	}
	current, _, err := loadBackfillKeys(context.Background(), dbPath)
	if err != nil || len(current) != 2 {
		t.Fatalf("distinct coordinates merged: %+v, %v", current, err)
	}
	writeBackfillSuccess(t, out, keys[0])
	nearby := keys[0]
	nearby.AccuracyMeters += .01
	if outputMatches(filepath.Join(out, "outputs", "000000.json"), nearby) {
		t.Fatal("different accuracy reused cached evidence")
	}
}

func TestBackfillReservesRetiredArtifactIndexes(t *testing.T) {
	dbPath, out, _ := backfillIdentityFixture(t, 1)
	if err := writeJSONFile(filepath.Join(out, "manifest.json"), []backfillKey{}); err != nil {
		t.Fatal(err)
	}
	retired := backfillKey{Index: 5, Latitude: 70, Longitude: 10}
	writeBackfillSuccess(t, out, retired)
	t.Setenv("PHOTOSCRAWL_TEST_PLACE_RAW_CHILD", "1")
	result, err := Backfill(context.Background(), BackfillOptions{DatabasePath: dbPath, OutputDir: out})
	if err != nil || result.Successes != 1 {
		t.Fatalf("new key result = %+v, %v", result, err)
	}
	data, err := os.ReadFile(filepath.Join(out, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var keys []backfillKey
	if err := json.Unmarshal(data, &keys); err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || keys[0].Index != 6 {
		t.Fatalf("retired index reused: %+v", keys)
	}
	if !outputMatches(filepath.Join(out, "outputs", "000005.json"), retired) {
		t.Fatal("retired evidence changed")
	}
}

func TestBackfillRejectsMissingOrAmbiguousManifest(t *testing.T) {
	for _, kind := range []string{"missing", "duplicate-index", "duplicate-coordinate"} {
		t.Run(kind, func(t *testing.T) {
			dbPath, out, keys := backfillIdentityFixture(t, 1)
			t.Setenv("PHOTOSCRAWL_TEST_PLACE_RAW_CHILD", "1")
			if kind == "missing" {
				if err := os.WriteFile(filepath.Join(out, "attempts", "000000.jsonl"), []byte("{}\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				duplicate := keys[0]
				if kind == "duplicate-index" {
					duplicate.Latitude++
				} else {
					duplicate.Index++
				}
				if err := writeJSONFile(filepath.Join(out, "manifest.json"), append(keys, duplicate)); err != nil {
					t.Fatal(err)
				}
			}
			_, err := Backfill(context.Background(), BackfillOptions{DatabasePath: dbPath, OutputDir: out})
			if err == nil || !strings.Contains(err.Error(), "manifest") {
				t.Fatalf("ambiguous state accepted: %v", err)
			}
		})
	}
}

func TestBackfillRetainsIdentityAcrossRemoveAndRestore(t *testing.T) {
	dbPath, out, keys := backfillIdentityFixture(t, 1)
	if err := writeJSONFile(filepath.Join(out, "manifest.json"), keys); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "attempts", "000000.jsonl"), []byte(strings.Repeat("{}\n", backfillMaxAttempts)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(filepath.Join(out, "errors", "000000.json"), backfillError{Index: 0, Final: true, Attempts: backfillMaxAttempts}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PHOTOSCRAWL_TEST_PLACE_RAW_CHILD", "1")
	run := func(wantFailures int) {
		t.Helper()
		result, err := Backfill(context.Background(), BackfillOptions{DatabasePath: dbPath, OutputDir: out})
		if err != nil || result.ProviderAttempts != 0 || result.FinalFailures != wantFailures {
			t.Fatalf("retired identity received a new retry budget: %+v, %v", result, err)
		}
	}
	run(1)
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("delete from location_observation"); err != nil {
		t.Fatal(err)
	}
	run(0)
	if _, err := db.Exec("insert into location_observation values ('asset:1', ?, ?, ?)", keys[0].Latitude, keys[0].Longitude, keys[0].AccuracyMeters); err != nil {
		t.Fatal(err)
	}
	run(1)
}

func TestBackfillRejectsContradictoryOutputIdentity(t *testing.T) {
	for _, filename := range []string{"manifest.json", "identities.json"} {
		t.Run(filename, func(t *testing.T) {
			dbPath, out, keys := backfillIdentityFixture(t, 2)
			path := filepath.Join(out, filename)
			if err := writeJSONFile(path, keys); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			original := keys[1]
			original.Index = 0
			writeBackfillSuccess(t, out, original)
			if err := os.WriteFile(filepath.Join(out, "attempts", "000000.jsonl"), []byte(strings.Repeat("{}\n", backfillMaxAttempts)), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PHOTOSCRAWL_TEST_PLACE_RAW_CHILD", "1")
			_, err = Backfill(context.Background(), BackfillOptions{DatabasePath: dbPath, OutputDir: out})
			if err == nil || !strings.Contains(err.Error(), "identity") {
				t.Fatalf("contradictory identity accepted: %v", err)
			}
			after, err := os.ReadFile(path)
			if err != nil || string(before) != string(after) {
				t.Fatal("identity data changed before validation")
			}
			if filename == "manifest.json" {
				if _, err := os.Stat(filepath.Join(out, "identities.json")); !os.IsNotExist(err) {
					t.Fatalf("ambiguous ledger published: %v", err)
				}
			}
		})
	}
}

func TestBackfillRejectsIncompleteMappedOutput(t *testing.T) {
	dbPath, out, keys := backfillIdentityFixture(t, 1)
	if err := writeJSONFile(filepath.Join(out, "manifest.json"), keys); err != nil {
		t.Fatal(err)
	}
	result := Result{Input: Input{Location: Coordinate{Latitude: keys[0].Latitude, Longitude: keys[0].Longitude}, AccuracyMeters: keys[0].AccuracyMeters}}
	if err := writeJSONFile(filepath.Join(out, "outputs", "000000.json"), result); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "attempts", "000000.jsonl"), []byte(strings.Repeat("{}\n", backfillMaxAttempts)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Backfill(context.Background(), BackfillOptions{DatabasePath: dbPath, OutputDir: out}); err == nil {
		t.Fatal("incomplete output accepted before identity publication")
	}
	if _, err := os.Stat(filepath.Join(out, "identities.json")); !os.IsNotExist(err) {
		t.Fatalf("ledger published for incomplete evidence: %v", err)
	}
}
