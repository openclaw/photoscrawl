package archive

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/openclaw/crawlkit/store"
)

func TestSimilarScoringReasonsAndExclusions(t *testing.T) {
	ctx := context.Background()
	paths := similarFixture(t)

	result, err := Similar(ctx, paths, SimilarOptions{ID: "seed", Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if result.ID != "seed" || result.Limit != 50 {
		t.Fatalf("result metadata = %#v", result)
	}
	if !similarIDPrefix(result.Assets, "strong", "tie-high", "tie-low", "common") {
		t.Fatalf("similar order and exclusions = %#v", result.Assets)
	}
	if result.Assets[0].Score <= result.Assets[1].Score {
		t.Fatalf("strong score %.3f must exceed tie score %.3f", result.Assets[0].Score, result.Assets[1].Score)
	}
	if result.Assets[2].Score <= result.Assets[3].Score {
		t.Fatalf("rare-term score %.3f must exceed common-term score %.3f", result.Assets[2].Score, result.Assets[3].Score)
	}
	for _, reason := range []string{"sunset", "beach", "Coast", "Harbor", "Walking"} {
		if !containsText(result.Assets[0].Shared, reason) {
			t.Fatalf("strong shared = %#v, want %q", result.Assets[0].Shared, reason)
		}
	}
	if len(result.Assets[0].Shared) != 5 {
		t.Fatalf("shared reason count = %d, want 5", len(result.Assets[0].Shared))
	}
	peoplePlace := similarAssetByID(result.Assets, "people-place")
	if peoplePlace.Score != similarPersonWeight+similarPlaceWeight || !sameStrings(peoplePlace.Shared, "Alex", "Italy") {
		t.Fatalf("people and place bonuses = %#v", peoplePlace)
	}
	for _, excluded := range []string{"seed", "same-burst", "same-event", "screenshot", "blurry", "video", "privacy-only", "deleted"} {
		if hasSimilarID(result.Assets, excluded) {
			t.Fatalf("unexpected excluded asset %q in %#v", excluded, result.Assets)
		}
	}
}

func TestSimilarIncludeSameEventStillExcludesBurst(t *testing.T) {
	ctx := context.Background()
	paths := similarFixture(t)
	result, err := Similar(ctx, paths, SimilarOptions{ID: "seed", Limit: 50, IncludeSameEvent: true})
	if err != nil {
		t.Fatal(err)
	}
	if !hasSimilarID(result.Assets, "same-event") {
		t.Fatalf("same-event missing from %#v", result.Assets)
	}
	if hasSimilarID(result.Assets, "same-burst") {
		t.Fatalf("same burst must remain excluded: %#v", result.Assets)
	}
}

func TestSimilarExcludeIDsFileAndLimit(t *testing.T) {
	ctx := context.Background()
	paths := similarFixture(t)
	excludePath := filepath.Join(paths.DataDir, "exclude.txt")
	if err := os.WriteFile(excludePath, []byte("strong-local/L0/001\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := Similar(ctx, paths, SimilarOptions{ID: "seed", Limit: 1, ExcludeIDsFile: excludePath})
	if err != nil {
		t.Fatal(err)
	}
	if result.Limit != 1 || !sameSimilarIDs(result.Assets, "tie-high") {
		t.Fatalf("excluded and limited result = %#v", result)
	}
}

func TestSimilarRequiresExistingID(t *testing.T) {
	ctx := context.Background()
	paths := similarFixture(t)
	if _, err := Similar(ctx, paths, SimilarOptions{}); err == nil {
		t.Fatal("missing id was accepted")
	}
	if _, err := Similar(ctx, paths, SimilarOptions{ID: "missing"}); err == nil {
		t.Fatal("unknown id was accepted")
	}
}

func similarFixture(t *testing.T) Paths {
	t.Helper()
	paths := testPaths(t)
	db, err := store.Open(context.Background(), store.Options{Path: paths.Database, Schema: Schema, SchemaVersion: SchemaVersion})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	execTestSQL(t, db.DB(), `insert into source_library values ('similar-library', '/fixture', 'snapshot', '2025-01-01T00:00:00Z', 'test', '{}')`)
	add := func(id, local, media, created, burst, subtype string, deleted bool) {
		var deletedAt any
		if deleted {
			deletedAt = "2025-02-01T00:00:00Z"
		}
		execTestSQL(t, db.DB(), `
insert into asset(id,local_identifier,media_type,media_subtypes,creation_date,modification_date,added_date,timezone_name,width,height,duration_seconds,favorite,hidden,burst_identifier,represents_burst,source_library_id,metadata_json,deleted_at)
values(?,?,?,?,?,'','','UTC',100,100,0,0,0,?,0,'similar-library','{}',?)
`, id, local, media, subtype, created, burst, deletedAt)
	}
	term := func(id, termType, term string) {
		execTestSQL(t, db.DB(), `insert into observation_term values(?,?,?,?,?,'fixture','fixture')`, "term-"+id+"-"+termType+"-"+term, id, "observation-"+id+"-"+termType, term, termType)
	}
	visual := func(id, observationType, label string) {
		execTestSQL(t, db.DB(), `insert into visual_observation values(?,?,?,?,1,'{}','fixture','fixture',?)`, "visual-"+id+"-"+observationType+"-"+label, id, observationType, label, "evidence-"+id+"-"+observationType+"-"+label)
	}
	person := func(id, label string) {
		execTestSQL(t, db.DB(), `insert into face_observation(id,asset_id,face_local_id,person_label,person_uuid,person_kind,confidence,quality,blur_score,eyes_closed,smile,bounding_box_json,source,evidence_id) values(?,?,?,?,?,'person',1,null,null,0,null,'{}',?,?)`, "face-"+id+"-"+label, id, id+"-"+label, label, "uuid-"+label, photosLibraryDBFaceSource, "evidence-face-"+id+"-"+label)
	}
	quality := func(id string, aesthetic, blur float64) {
		value, err := json.Marshal(map[string]float64{"overall_aesthetic": aesthetic, "media_blurriness": blur})
		if err != nil {
			t.Fatal(err)
		}
		execTestSQL(t, db.DB(), `insert into model_observation values(?,?,'apple_quality_scores','',?,1,'fixture','fixture','fixture',?)`, "quality-"+id, id, string(value), "evidence-quality-"+id)
	}

	add("seed", "seed-local", "image", "2025-01-10T12:00:00Z", "burst-a", "0", false)
	add("strong", "strong-local/L0/001", "image", "2024-01-01T12:00:00Z", "", "0", false)
	add("common", "common-local", "image", "2023-01-01T12:00:00Z", "", "0", false)
	add("tie-low", "tie-low-local", "image", "2022-01-01T12:00:00Z", "", "0", false)
	add("tie-high", "tie-high-local", "image", "2021-01-01T12:00:00Z", "", "0", false)
	add("same-burst", "same-burst-local", "image", "2020-01-01T12:00:00Z", "burst-a", "0", false)
	add("same-event", "same-event-local", "image", "2025-01-10T13:00:00Z", "", "0", false)
	add("screenshot", "screenshot-local", "image", "2020-02-01T12:00:00Z", "", "4", false)
	add("blurry", "blurry-local", "image", "2020-03-01T12:00:00Z", "", "0", false)
	add("video", "video-local", "video", "2020-04-01T12:00:00Z", "", "0", false)
	add("people-place", "people-place-local", "image", "2020-04-15T12:00:00Z", "", "0", false)
	add("privacy-only", "privacy-local", "image", "2020-05-01T12:00:00Z", "", "0", false)
	add("deleted", "deleted-local", "image", "2020-06-01T12:00:00Z", "", "0", true)
	for i := 0; i < 4; i++ {
		id := fmt.Sprintf("corpus-%d", i)
		add(id, id+"-local", "image", fmt.Sprintf("2019-01-0%dT12:00:00Z", i+1), "", "0", false)
		term(id, "scene", "beach")
	}
	for _, id := range []string{"seed", "strong", "common", "same-burst", "same-event", "screenshot", "blurry", "video", "deleted"} {
		term(id, "scene", "beach")
	}
	for _, id := range []string{"seed", "strong", "same-burst", "same-event", "screenshot", "blurry", "video", "deleted"} {
		term(id, "cluster_feature", "sunset")
	}
	for _, id := range []string{"seed", "tie-low", "tie-high"} {
		term(id, "object_or_food", "portrait")
	}
	term("seed", "privacy_sensitivity", "secret")
	term("privacy-only", "privacy_sensitivity", "secret")
	for _, id := range []string{"seed", "strong"} {
		visual(id, "apple_label", "Coast")
		visual(id, "apple_activity", "Walking")
		visual(id, "apple_venue", "Harbor")
		visual(id, "apple_place", "Italy")
		person(id, "Alex")
	}
	visual("people-place", "apple_place", "Italy")
	person("people-place", "Alex")
	quality("strong", .8, .9)
	quality("common", .8, .9)
	quality("tie-low", .1, .9)
	quality("tie-high", .9, .9)
	quality("blurry", .9, .3)
	return paths
}

func sameSimilarIDs(assets []SimilarAsset, ids ...string) bool {
	if len(assets) != len(ids) {
		return false
	}
	for i, id := range ids {
		if assets[i].ID != id {
			return false
		}
	}
	return true
}

func similarIDPrefix(assets []SimilarAsset, ids ...string) bool {
	if len(assets) < len(ids) {
		return false
	}
	return sameSimilarIDs(assets[:len(ids)], ids...)
}

func hasSimilarID(assets []SimilarAsset, id string) bool {
	for _, asset := range assets {
		if asset.ID == id {
			return true
		}
	}
	return false
}

func similarAssetByID(assets []SimilarAsset, id string) SimilarAsset {
	for _, asset := range assets {
		if asset.ID == id {
			return asset
		}
	}
	return SimilarAsset{}
}

func sameStrings(values []string, wants ...string) bool {
	if len(values) != len(wants) {
		return false
	}
	for i, want := range wants {
		if values[i] != want {
			return false
		}
	}
	return true
}
