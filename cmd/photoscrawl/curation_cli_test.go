package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/openclaw/photoscrawl/internal/archive"
)

func TestCurationCLIImportsSyntheticPhotosLibrary(t *testing.T) {
	root := t.TempDir()
	library := filepath.Join(root, "Fixture.photoslibrary")
	sources := map[string][]byte{}
	for _, fixture := range []struct{ name, target string }{
		{"curation_photos.sql", "database/Photos.sqlite"},
		{"curation_psi.sql", "database/search/psi.sqlite"},
	} {
		path := filepath.Join(library, fixture.target)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		schema, err := os.ReadFile(filepath.Join("testdata", fixture.name))
		if err != nil {
			t.Fatal(err)
		}
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		_, execErr := db.Exec(string(schema))
		closeErr := db.Close()
		if execErr != nil {
			t.Fatal(execErr)
		}
		if closeErr != nil {
			t.Fatal(closeErr)
		}
		sources[path], err = os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	binary := filepath.Join(root, "photoscrawl")
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, ".")
	build.Env = append(os.Environ(), "GOWORK=off")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v\n%s", err, out)
	}
	database := filepath.Join(root, "archive.sqlite")
	invoke := func(result any, args ...string) {
		t.Helper()
		args = append(args, "--db", database, "--json")
		cmd := exec.CommandContext(ctx, binary, args...)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("CLI %v: %v\n%s", args, err, stderr.String())
		}
		if err := json.Unmarshal(out, result); err != nil {
			t.Fatalf("CLI %v JSON: %v\n%s", args, err, out)
		}
	}
	var crawl map[string]any
	invoke(&crawl, "crawl", "--provider", "sqlite", "--library", library)
	var imported archive.ImportAppleResult
	invoke(&imported, "import-apple", "--library", library)
	if imported.Faces.FaceObservationsInserted != 2 || imported.Faces.FacesWithEyeState != 2 || imported.PhotoMetadata.DuplicateObservationsInserted != 1 {
		t.Fatalf("imported signals: %#v", imported)
	}
	var people archive.PeopleResult
	invoke(&people, "people")
	if len(people.People) != 1 || people.People[0].Label != "Alex" || people.People[0].AssetCount != 2 {
		t.Fatalf("people: %#v", people)
	}
	var found archive.FindResult
	invoke(&found, "find", "--person", "Alex", "--rank", "quality")
	if len(found.Assets) != 2 || found.Assets[0].LocalIdentifier != "fixture-sharp" {
		t.Fatalf("find: %#v", found)
	}
	var ranked archive.RankResult
	invoke(&ranked, "rank", "--person", "Alex")
	if len(ranked.Groups) != 1 || len(ranked.Groups[0].Assets) != 2 || ranked.Groups[0].BestID != found.Assets[0].ID {
		t.Fatalf("rank: %#v", ranked)
	}
	var junk archive.JunkResult
	invoke(&junk, "junk", "--kind", "blurry")
	if len(junk.Candidates) != 1 || junk.Candidates[0].ID != found.Assets[1].ID {
		t.Fatalf("junk: %#v", junk)
	}
	invoke(&junk, "junk", "--kind", "eyes-closed")
	if len(junk.Candidates) != 1 || junk.Candidates[0].ID != found.Assets[1].ID || junk.Candidates[0].KeepInstead != found.Assets[0].ID {
		t.Fatalf("eyes-closed after search-index import: %#v", junk)
	}
	var similar archive.SimilarResult
	invoke(&similar, "similar", "--id", found.Assets[1].ID, "--include-same-event")
	if len(similar.Assets) != 1 || similar.Assets[0].ID != found.Assets[0].ID {
		t.Fatalf("similar in a three-photo library: %#v", similar)
	}
	var all archive.FindResult
	invoke(&all, "find")
	if len(all.Assets) != 3 {
		t.Fatalf("find all: %#v", all)
	}
	archiveDB, err := sql.Open("sqlite", database)
	if err != nil {
		t.Fatal(err)
	}
	var hiddenID string
	if err := archiveDB.QueryRow(`select id from asset where local_identifier = 'fixture-screenshot'`).Scan(&hiddenID); err != nil {
		archiveDB.Close()
		t.Fatal(err)
	}
	if _, err := archiveDB.Exec(`update asset set hidden = 1 where id = ?`, hiddenID); err != nil {
		archiveDB.Close()
		t.Fatal(err)
	}
	if _, err := archiveDB.Exec(`insert into model_observation values('malformed-hidden-quality', ?, 'apple_quality_scores', '', '{malformed', 1, 'fixture', 'fixture', 'fixture', 'malformed-hidden-evidence')`, hiddenID); err != nil {
		archiveDB.Close()
		t.Fatal(err)
	}
	if err := archiveDB.Close(); err != nil {
		t.Fatal(err)
	}
	var visible archive.FindResult
	invoke(&visible, "find", "--rank", "quality")
	if len(visible.Assets) != 2 {
		t.Fatalf("filtered find considered a hidden asset's malformed signal: %#v", visible)
	}
	ids := make([]string, 0, len(all.Assets))
	for _, asset := range all.Assets {
		ids = append(ids, asset.ID)
	}
	encoded, err := json.Marshal(ids)
	if err != nil {
		t.Fatal(err)
	}
	idsFile := filepath.Join(root, "ids.json")
	if err := os.WriteFile(idsFile, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	var sharing archive.ShareCheckResult
	invoke(&sharing, "share-check", "--ids-file", idsFile)
	if len(sharing.Assets) != 3 || sharing.Counts["unreviewed"] != 2 || sharing.Counts["blocked"] != 1 || sharing.Counts["pass"] != 0 {
		t.Fatalf("share-check: %#v", sharing)
	}
	for path, before := range sources {
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatalf("source database changed: %s (%v)", path, err)
		}
	}
}
