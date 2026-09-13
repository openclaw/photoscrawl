package archive

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openclaw/crawlkit/store"
)

func BenchmarkSimilar(b *testing.B) {
	b.StopTimer()
	paths := similarBenchmarkFixture(b, 50_000, 60)
	ctx := context.Background()

	b.Run("before-whole-table", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if err := benchmarkLegacySimilar(ctx, paths); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("after-candidate-first", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if _, err := Similar(ctx, paths, SimilarOptions{ID: "benchmark-00000", Limit: 30}); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func similarBenchmarkFixture(b *testing.B, assetCount, termsPerAsset int) Paths {
	b.Helper()
	root := b.TempDir()
	paths := Paths{DataDir: root, Database: filepath.Join(root, "photos.sqlite")}
	db, err := store.Open(context.Background(), store.Options{Path: paths.Database, Schema: Schema, SchemaVersion: SchemaVersion})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = db.Close() })
	if _, err := db.DB().Exec(`insert into source_library values ('benchmark-library', '/fixture', 'snapshot', '2025-01-01T00:00:00Z', 'test', '{}')`); err != nil {
		b.Fatal(err)
	}
	tx, err := db.DB().Begin()
	if err != nil {
		b.Fatal(err)
	}
	assetStatement, err := tx.Prepare(`
insert into asset(id,local_identifier,media_type,media_subtypes,creation_date,modification_date,added_date,timezone_name,width,height,duration_seconds,favorite,hidden,burst_identifier,represents_burst,source_library_id,metadata_json)
values(?,?,'image','0',?,'','','UTC',100,100,0,0,0,'',0,'benchmark-library','{}')`)
	if err != nil {
		_ = tx.Rollback()
		b.Fatal(err)
	}
	termStatement, err := tx.Prepare(`insert into observation_term values(?,?,?,?,?,'fixture','fixture')`)
	if err != nil {
		_ = assetStatement.Close()
		_ = tx.Rollback()
		b.Fatal(err)
	}
	base := time.Date(2000, 1, 1, 12, 0, 0, 0, time.UTC)
	for assetIndex := 0; assetIndex < assetCount; assetIndex++ {
		assetID := fmt.Sprintf("benchmark-%05d", assetIndex)
		if _, err := assetStatement.Exec(assetID, assetID+"-local", base.AddDate(0, 0, assetIndex).Format(time.RFC3339)); err != nil {
			_ = termStatement.Close()
			_ = assetStatement.Close()
			_ = tx.Rollback()
			b.Fatal(err)
		}
		group := assetIndex / 1000
		for termIndex := 0; termIndex < termsPerAsset; termIndex++ {
			termType := similarTermTypes[termIndex%len(similarTermTypes)]
			term := fmt.Sprintf("benchmark_term_%02d_%02d", termIndex, group)
			termID := fmt.Sprintf("benchmark-term-%05d-%02d", assetIndex, termIndex)
			if _, err := termStatement.Exec(termID, assetID, "benchmark-observation-"+assetID, term, termType); err != nil {
				_ = termStatement.Close()
				_ = assetStatement.Close()
				_ = tx.Rollback()
				b.Fatal(err)
			}
		}
	}
	if err := termStatement.Close(); err != nil {
		_ = assetStatement.Close()
		_ = tx.Rollback()
		b.Fatal(err)
	}
	if err := assetStatement.Close(); err != nil {
		_ = tx.Rollback()
		b.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}
	return paths
}

func benchmarkLegacySimilar(ctx context.Context, paths Paths) error {
	db, err := openArchiveReadOnly(ctx, paths.Database)
	if err != nil {
		return err
	}
	defer db.Close()
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(similarTermTypes)), ",")
	args := make([]any, 0, len(similarTermTypes))
	for _, termType := range similarTermTypes {
		args = append(args, termType)
	}
	rows, err := db.DB().QueryContext(ctx, `
with eligible as (
  select distinct observation_term.asset_id, observation_term.term_type, observation_term.term
  from observation_term
  join asset on asset.id = observation_term.asset_id
  where observation_term.term_type in (`+placeholders+`)
    and trim(observation_term.term) <> ''
    and asset.deleted_at is null
)
select term_type, term, count(*), (select count(distinct asset_id) from eligible)
from eligible
group by term_type, term
`, args...)
	if err != nil {
		return err
	}
	for rows.Next() {
		var termType, term string
		var frequency, count int
		if err := rows.Scan(&termType, &term, &frequency, &count); err != nil {
			rows.Close()
			return err
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if _, err := loadAssets(ctx, db.DB()); err != nil {
		return err
	}
	_, err = loadSignals(ctx, db.DB())
	return err
}

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
	if !similarIDPrefix(result.Assets, "strong", "tie-high", "tie-low") {
		t.Fatalf("similar order and exclusions = %#v", result.Assets)
	}
	if result.Assets[0].Score <= result.Assets[1].Score {
		t.Fatalf("strong score %.3f must exceed tie score %.3f", result.Assets[0].Score, result.Assets[1].Score)
	}
	for _, reason := range []string{"Coast", "Harbor", "Walking"} {
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
	for _, excluded := range []string{"seed", "same-burst", "same-event", "screenshot", "blurry", "video", "hidden", "privacy-only", "deleted"} {
		if hasSimilarID(result.Assets, excluded) {
			t.Fatalf("unexpected excluded asset %q in %#v", excluded, result.Assets)
		}
	}
}

func TestSimilarExcludesHiddenCandidates(t *testing.T) {
	result, err := Similar(context.Background(), similarFixture(t), SimilarOptions{ID: "seed", Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if hasSimilarID(result.Assets, "hidden") {
		t.Fatalf("hidden candidate returned in %#v", result.Assets)
	}
}

func TestSimilarIncludeSameEventIncludesTimeDayAndBurst(t *testing.T) {
	ctx := context.Background()
	paths := similarFixture(t)
	result, err := Similar(ctx, paths, SimilarOptions{ID: "seed", Limit: 50, IncludeSameEvent: true})
	if err != nil {
		t.Fatal(err)
	}
	if !hasSimilarID(result.Assets, "same-event") {
		t.Fatalf("same-event missing from %#v", result.Assets)
	}
	if !hasSimilarID(result.Assets, "same-burst") {
		t.Fatalf("same burst missing from %#v", result.Assets)
	}
}

func TestSimilarNoisePolicyData(t *testing.T) {
	if similarNoisePolicy.maxDocumentFraction != 0.05 {
		t.Fatalf("frequency cap = %v, want 0.05", similarNoisePolicy.maxDocumentFraction)
	}
	want := []string{"arm", "face", "finger", "hand", "man", "people", "person", "tattoo", "tattooed", "woman"}
	if !sameStrings(similarNoisePolicy.zeroWeightTerms, want...) {
		t.Fatalf("zero-weight terms = %#v, want %#v", similarNoisePolicy.zeroWeightTerms, want)
	}
}

func TestSimilarFoodRankingAndNoiseRules(t *testing.T) {
	ctx := context.Background()
	paths := similarFoodFixture(t)

	result, err := Similar(ctx, paths, SimilarOptions{ID: "food-seed", Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if !similarIDPrefix(result.Assets, "food-other-year", "phone") {
		t.Fatalf("food ranking = %#v", result.Assets)
	}
	if hasSimilarID(result.Assets, "food-same-day") || hasSimilarID(result.Assets, "food-within-12h") {
		t.Fatalf("same-day or 12-hour candidate was included: %#v", result.Assets)
	}
	phone := similarAssetByID(result.Assets, "phone")
	if !containsText(phone.Shared, "cell_phone") || containsText(phone.Shared, "cell") || containsText(phone.Shared, "fork") {
		t.Fatalf("compound component filtering = %#v", phone.Shared)
	}
	for _, asset := range result.Assets {
		if containsText(asset.Shared, "arm") || containsText(asset.Shared, "common_noise") {
			t.Fatalf("noise term survived in %#v", asset)
		}
	}

	included, err := Similar(ctx, paths, SimilarOptions{ID: "food-seed", Limit: 50, IncludeSameEvent: true})
	if err != nil {
		t.Fatal(err)
	}
	if similarAssetIndex(included.Assets, "food-other-year") >= similarAssetIndex(included.Assets, "food-same-day") ||
		similarAssetIndex(included.Assets, "food-other-year") >= similarAssetIndex(included.Assets, "phone") {
		t.Fatalf("other-year food should outrank same-day food: %#v", included.Assets)
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

func TestSimilarFiltersIneligibleCandidatesBeforeShortlist(t *testing.T) {
	ctx := context.Background()
	paths := testPaths(t)
	db, err := store.Open(ctx, store.Options{Path: paths.Database, Schema: Schema, SchemaVersion: SchemaVersion})
	if err != nil {
		t.Fatal(err)
	}
	execTestSQL(t, db.DB(), `insert into source_library values ('shortlist-library', '/fixture', 'snapshot', '2025-01-01T00:00:00Z', 'test', '{}')`)
	addAsset := func(id, created string) {
		execTestSQL(t, db.DB(), `insert into asset(id,local_identifier,media_type,media_subtypes,creation_date,modification_date,added_date,timezone_name,width,height,duration_seconds,favorite,hidden,burst_identifier,represents_burst,source_library_id,metadata_json) values(?,?,'image','0',?,'','','UTC',100,100,0,0,0,'',0,'shortlist-library','{}')`, id, id+"-local", created)
	}
	addVisual := func(id, label string) {
		execTestSQL(t, db.DB(), `insert into visual_observation values(?,?,'apple_label',?,1,'{}','fixture','fixture',?)`, "visual-"+id+"-"+label, id, label, "evidence-"+id+"-"+label)
	}
	addAsset("shortlist-seed", "2025-01-10T12:00:00Z")
	excludedIDs := make([]string, 0, similarShortlist/2)
	for i := 0; i <= similarShortlist; i++ {
		id := fmt.Sprintf("high-%03d", i)
		created := "2025-01-10T13:00:00Z"
		if i >= similarShortlist/2 {
			created = "2020-01-01T12:00:00Z"
			excludedIDs = append(excludedIDs, id)
		}
		addAsset(id, created)
		for feature := 0; feature < 2; feature++ {
			label := fmt.Sprintf("shortlist-%03d-%d", i, feature)
			addVisual("shortlist-seed", label)
			addVisual(id, label)
		}
	}
	addAsset("eligible-lower-score", "2019-01-01T12:00:00Z")
	addVisual("shortlist-seed", "eligible-label")
	addVisual("eligible-lower-score", "eligible-label")
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	excludePath := writeCurationIDs(t, paths.DataDir, "shortlist-exclude.txt", excludedIDs...)
	result, err := Similar(ctx, paths, SimilarOptions{ID: "shortlist-seed", ExcludeIDsFile: excludePath, Limit: 30})
	if err != nil {
		t.Fatal(err)
	}
	if !sameSimilarIDs(result.Assets, "eligible-lower-score") {
		t.Fatalf("eligible candidate below the pre-filter top %d = %#v", similarShortlist, result.Assets)
	}
}

func TestSimilarRefillsShortlistWhenTopCandidatesAreBlurry(t *testing.T) {
	previous := similarShortlist
	similarShortlist = 4
	t.Cleanup(func() { similarShortlist = previous })
	ctx := context.Background()
	paths := testPaths(t)
	db, err := store.Open(ctx, store.Options{Path: paths.Database, Schema: Schema, SchemaVersion: SchemaVersion})
	if err != nil {
		t.Fatal(err)
	}
	execTestSQL(t, db.DB(), `insert into source_library values ('refill-library', '/fixture', 'snapshot', '2025-01-01T00:00:00Z', 'test', '{}')`)
	addAsset := func(id, created string) {
		execTestSQL(t, db.DB(), `insert into asset(id,local_identifier,media_type,media_subtypes,creation_date,modification_date,added_date,timezone_name,width,height,duration_seconds,favorite,hidden,burst_identifier,represents_burst,source_library_id,metadata_json) values(?,?,'image','0',?,'','','UTC',100,100,0,0,0,'',0,'refill-library','{}')`, id, id+"-local", created)
	}
	addVisual := func(id, label string) {
		execTestSQL(t, db.DB(), `insert into visual_observation values(?,?,'apple_label',?,1,'{}','fixture','fixture',?)`, "visual-"+id+"-"+label, id, label, "evidence-"+id+"-"+label)
	}
	addBlur := func(id string, value float64) {
		execTestSQL(t, db.DB(), `insert into model_observation values(?,?,'apple_quality_scores','',?,1,'fixture','fixture','fixture',?)`, "quality-"+id, id, fmt.Sprintf(`{"media_blurriness": %g}`, value), "evidence-quality-"+id)
	}
	addAsset("refill-seed", "2025-01-10T12:00:00Z")
	for i := 0; i < 8; i++ {
		id := fmt.Sprintf("blurry-%d", i)
		addAsset(id, "2020-01-01T12:00:00Z")
		addBlur(id, 0.1)
		for feature := 0; feature < 3; feature++ {
			label := fmt.Sprintf("refill-%d-%d", i, feature)
			addVisual("refill-seed", label)
			addVisual(id, label)
		}
	}
	addAsset("sharp-lower-score", "2019-01-01T12:00:00Z")
	addBlur("sharp-lower-score", 0.95)
	// Unrelated photos keep each shared label under the 5% frequency cap.
	for i := 0; i < 200; i++ {
		addAsset(fmt.Sprintf("filler-%03d", i), "2018-01-01T12:00:00Z")
	}
	addVisual("refill-seed", "sharp-label")
	addVisual("sharp-lower-score", "sharp-label")
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	result, err := Similar(ctx, paths, SimilarOptions{ID: "refill-seed", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if !sameSimilarIDs(result.Assets, "sharp-lower-score") {
		t.Fatalf("sharp candidate behind %d blurry ones = %#v", 8, result.Assets)
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
	add("hidden", "hidden-local", "image", "2020-05-15T12:00:00Z", "", "0", false)
	execTestSQL(t, db.DB(), `update asset set hidden=1 where id='hidden'`)
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
	for _, id := range []string{"seed", "same-burst", "same-event"} {
		term(id, "object_or_food", "event-match")
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
	visual("seed", "apple_label", "Private Landmark")
	visual("hidden", "apple_label", "Private Landmark")
	quality("strong", .8, .9)
	quality("common", .8, .9)
	quality("tie-low", .1, .9)
	quality("tie-high", .9, .9)
	quality("blurry", .9, .3)
	for i := 0; i < 50; i++ {
		id := fmt.Sprintf("filler-%d", i)
		add(id, id+"-local", "image", fmt.Sprintf("2018-02-%02dT12:00:00Z", i%28+1), "", "0", false)
	}
	return paths
}

func TestSimilarAllowsUnknownCreationDates(t *testing.T) {
	ctx := context.Background()
	paths := similarFixture(t)
	db, err := store.Open(ctx, store.Options{Path: paths.Database, Schema: Schema, SchemaVersion: SchemaVersion})
	if err != nil {
		t.Fatal(err)
	}
	execTestSQL(t, db.DB(), `update asset set creation_date='' where id='strong'`)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := Similar(ctx, paths, SimilarOptions{ID: "seed", Limit: 30})
	if err != nil || !hasSimilarID(got.Assets, "strong") {
		t.Fatalf("similar with undated candidate = %#v, %v", got, err)
	}
}

func similarFoodFixture(t *testing.T) Paths {
	t.Helper()
	paths := testPaths(t)
	db, err := store.Open(context.Background(), store.Options{Path: paths.Database, Schema: Schema, SchemaVersion: SchemaVersion})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	execTestSQL(t, db.DB(), `insert into source_library values ('food-library', '/fixture', 'snapshot', '2025-01-01T00:00:00Z', 'test', '{}')`)
	add := func(id, created, burst string) {
		execTestSQL(t, db.DB(), `
insert into asset(id,local_identifier,media_type,media_subtypes,creation_date,modification_date,added_date,timezone_name,width,height,duration_seconds,favorite,hidden,burst_identifier,represents_burst,source_library_id,metadata_json)
values(?,?,'image','0',?,'','','Europe/Madrid',100,100,0,0,0,?,0,'food-library','{}')
`, id, id+"-local", created, burst)
	}
	term := func(id, observation, value string) {
		execTestSQL(t, db.DB(), `insert into observation_term values(?,?,?,?,?,'fixture','fixture')`, "term-"+id+"-"+value, id, observation, value, "object_or_food")
	}

	add("food-seed", "2025-06-01T01:00:00Z", "meal-burst")
	add("food-other-year", "2024-06-01T01:00:00Z", "")
	add("food-same-day", "2025-06-01T21:00:00Z", "")
	add("food-within-12h", "2025-06-02T05:00:00Z", "")
	add("phone", "2023-06-01T01:00:00Z", "")
	for i := 0; i < 60; i++ {
		id := fmt.Sprintf("food-filler-%d", i)
		add(id, fmt.Sprintf("2020-03-%02dT01:00:00Z", i%28+1), "")
		if i < 10 {
			term(id, "noise-"+id, "common_noise")
		}
	}
	for _, value := range []string{"pasta", "tomato", "sauce"} {
		term("food-seed", "food-seed-observation", value)
		term("food-other-year", "food-other-observation", value)
	}
	for _, value := range []string{"pasta", "tomato"} {
		term("food-same-day", "food-same-day-observation", value)
	}
	term("food-within-12h", "food-within-observation", "pasta")
	for _, value := range []string{"cell", "cell_phone", "arm", "common_noise"} {
		term("food-seed", "device-seed-observation", value)
		term("phone", "device-phone-observation", value)
	}
	term("food-seed", "utensil-seed-observation", "fork")
	term("phone", "utensil-phone-observation", "fork")
	term("phone", "utensil-phone-observation", "dinner_fork")
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

func similarAssetIndex(assets []SimilarAsset, id string) int {
	for i, asset := range assets {
		if asset.ID == id {
			return i
		}
	}
	return len(assets)
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

func TestPruneSimilarFeaturesDropsCommonLabelsAndShortlists(t *testing.T) {
	features := map[string]map[string]similarFeature{}
	for i := 0; i < 10; i++ {
		id := fmt.Sprintf("common-%02d", i)
		features[id] = map[string]similarFeature{"apple:apple_label:food": {key: "apple:apple_label:food", weight: 1}}
	}
	features["rare"] = map[string]similarFeature{
		"apple:apple_label:food":       {key: "apple:apple_label:food", weight: 1},
		"term:object_or_food\x00pasta": {key: "term:object_or_food\x00pasta", weight: 5},
	}
	features["second"] = map[string]similarFeature{"term:object_or_food\x00plate": {key: "term:object_or_food\x00plate", weight: 2}}
	pruned := pruneSimilarFeatures(features, 100, 0)
	if _, ok := pruned["common-00"]; ok {
		t.Fatalf("a label on more than 5%% of the library kept a candidate: %#v", pruned["common-00"])
	}
	if _, ok := pruned["rare"]["apple:apple_label:food"]; ok {
		t.Fatalf("common label survived on the rare candidate: %#v", pruned["rare"])
	}
	if len(pruned) != 2 {
		t.Fatalf("pruned candidates = %d, want 2", len(pruned))
	}
	shortlisted := pruneSimilarFeatures(features, 100, 1)
	if _, ok := shortlisted["rare"]; !ok || len(shortlisted) != 1 {
		t.Fatalf("shortlist kept %#v, want only the highest-scoring candidate", shortlisted)
	}
}
