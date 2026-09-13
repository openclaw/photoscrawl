package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/openclaw/crawlkit/store"
)

func TestQualityComparatorSkipsSignalsMissingOnOneSide(t *testing.T) {
	landscape := fullAsset{AssetRow: AssetRow{ID: "landscape"}, created: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
	portrait := fullAsset{AssetRow: AssetRow{ID: "portrait"}, created: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)}
	s := signals{faces: map[string][]faceSignal{}, aesthetic: map[string]sql.NullFloat64{}, focus: map[string]sql.NullFloat64{}}
	// With neither asset carrying a face signal, the comparator skips it.
	if got := compareQuality(landscape, portrait, s, nil); got != 0 {
		t.Fatalf("compareQuality with signal on one side = %d, want 0", got)
	}
	s.faces[portrait.ID] = []faceSignal{{quality: sql.NullFloat64{Float64: 0.1, Valid: true}}}
	// A signal available to only one asset is skipped.
	if got := compareQuality(landscape, portrait, s, nil); got != 0 {
		t.Fatalf("one-sided face quality must be skipped: %d", got)
	}
	s.faces = map[string][]faceSignal{}
	s.aesthetic[landscape.ID] = sql.NullFloat64{Float64: .9, Valid: true}
	s.aesthetic[portrait.ID] = sql.NullFloat64{Float64: .2, Valid: true}
	if got := compareQuality(landscape, portrait, s, nil); got >= 0 {
		t.Fatalf("aesthetic signal did not rank landscape first: %d", got)
	}
}

func TestQualityComparatorUsesSharedKeysInOrder(t *testing.T) {
	a := fullAsset{AssetRow: AssetRow{ID: "a"}}
	b := fullAsset{AssetRow: AssetRow{ID: "b"}}
	s := signals{faces: map[string][]faceSignal{}, aesthetic: map[string]sql.NullFloat64{}, focus: map[string]sql.NullFloat64{}}
	s.faces[a.ID] = []faceSignal{{label: "A", closed: sql.NullInt64{Valid: true}}, {quality: sql.NullFloat64{Float64: .2, Valid: true}}}
	s.faces[b.ID] = []faceSignal{{label: "A", closed: sql.NullInt64{Int64: 1, Valid: true}}, {quality: sql.NullFloat64{Float64: .9, Valid: true}}}
	if got := compareQuality(a, b, s, []string{"A"}); got >= 0 {
		t.Fatalf("requested person key = %d, want a first", got)
	}
	if got := compareQuality(a, b, s, nil); got >= 0 {
		t.Fatalf("eyes-closed key = %d, want a first", got)
	}
	s.faces[b.ID][0].closed = sql.NullInt64{Valid: true}
	if got := compareQuality(a, b, s, nil); got <= 0 {
		t.Fatalf("face-quality key = %d, want b first", got)
	}
	s.faces = map[string][]faceSignal{}
	s.aesthetic[a.ID] = sql.NullFloat64{Float64: .1, Valid: true}
	s.aesthetic[b.ID] = sql.NullFloat64{Float64: .9, Valid: true}
	if got := compareQuality(a, b, s, nil); got <= 0 {
		t.Fatalf("aesthetic key = %d, want b first", got)
	}
	s.aesthetic = map[string]sql.NullFloat64{}
	s.focus[a.ID] = sql.NullFloat64{Float64: .1, Valid: true}
	s.focus[b.ID] = sql.NullFloat64{Float64: .9, Valid: true}
	if got := compareQuality(a, b, s, nil); got <= 0 {
		t.Fatalf("focus key = %d, want b first", got)
	}
}

func TestCurationDateAndPercentile(t *testing.T) {
	from, err := parseDate("2025-02-03", false)
	if err != nil || !from.Valid || from.Time.Hour() != 0 {
		t.Fatalf("date parse: %#v, %v", from, err)
	}
	to, err := parseDate("2025-02-03", true)
	if err != nil || to.Time.Hour() != 23 {
		t.Fatalf("inclusive date parse: %#v, %v", to, err)
	}
	if got := percentile([]float64{4, 1, 3, 2}, .25); got != 1 {
		t.Fatalf("p25 = %v, want 1", got)
	}
}

func TestCurationDurationAndScreenshotSubtype(t *testing.T) {
	for _, input := range []string{"-1d", "1.5d", "-2h"} {
		if _, err := parseDuration(input); err == nil {
			t.Fatalf("parseDuration(%q) accepted invalid duration", input)
		}
	}
	if got, err := parseDuration("2d"); err != nil || got != 48*time.Hour {
		t.Fatalf("parseDuration(2d) = %v, %v", got, err)
	}
	if !isScreenshotSubtype("4") || isScreenshotSubtype("16") {
		t.Fatal("screenshot subtype must be bit 2, not portrait bit 4")
	}
}

func TestCurationQueriesEndToEnd(t *testing.T) {
	ctx, paths := context.Background(), curationFixture(t)
	t.Run("people", func(t *testing.T) {
		got, err := People(ctx, paths)
		if err != nil {
			t.Fatal(err)
		}
		if len(got.People) < 4 || got.People[0].Label != "Casey" || got.People[0].AssetCount != 6 || got.People[0].Kind != "person" || got.People[1].Label != "Alex" || got.People[2].Label != "Avery" || got.People[2].AssetCount != 4 || got.People[3].Label != "Sam" || got.People[3].AssetCount != 4 || got.People[3].Kind != "pet" {
			t.Fatalf("people ordering and kinds = %#v", got.People)
		}
	})
	t.Run("find", func(t *testing.T) {
		got, err := Find(ctx, paths, FindOptions{People: []string{"Alex", "Sam"}, From: "2025-01-10", To: "2025-01-10", Place: "central park", Rank: "quality", Limit: 20})
		if err != nil || !sameIDs(got.Assets, "find-both", "find-end") {
			t.Fatalf("date, people, place, default media, hidden, and quality = %#v, %v", got, err)
		}
		got, err = Find(ctx, paths, FindOptions{People: []string{"Alex", "Sam"}, From: "2025-01-10T12:00:00Z", To: "2025-01-10T12:00:00Z", Limit: 20})
		if err != nil || !sameIDs(got.Assets, "find-both") {
			t.Fatalf("RFC3339 bounds = %#v, %v", got, err)
		}
		got, err = Find(ctx, paths, FindOptions{People: []string{"Alex", "Sam"}, From: "2025-01-10", To: "2025-01-10", Media: "any", IncludeHidden: true, Limit: 20})
		if err != nil || !sameIDs(got.Assets, "find-end", "find-both", "find-hidden", "find-video") {
			t.Fatalf("media any and hidden = %#v, %v", got, err)
		}
	})
	t.Run("rank", func(t *testing.T) {
		ids := writeCurationIDs(t, paths.DataDir, "rank.txt", "landscape", "face-low", "burst-closed", "burst-open", "time-one", "time-two", "day-before", "day-after")
		qualityIDs := writeCurationIDs(t, paths.DataDir, "quality-rank.txt", "landscape", "face-low")
		got, err := Rank(ctx, paths, RankOptions{IDsFile: qualityIDs})
		if err != nil || got.Groups[0].BestID != "landscape" || !containsText(got.Groups[0].Assets[0].Reasons, "overall aesthetic 0.90") {
			t.Fatalf("quality rank without people = %#v, %v", got, err)
		}
		got, err = Rank(ctx, paths, RankOptions{IDsFile: ids, Group: "burst"})
		burst := rankGroup(got, "burst-a")
		if err != nil || burst.BestID != "burst-open" || len(burst.Assets) != 2 || !containsText(burst.Assets[0].Reasons, "overall aesthetic 0.50") || !containsText(burst.Assets[1].Reasons, "1 named face have eyes closed") {
			t.Fatalf("burst rank = %#v, %v", got, err)
		}
		got, err = Rank(ctx, paths, RankOptions{IDsFile: ids, Group: "burst", PerGroup: 1})
		if err != nil || len(rankGroup(got, "burst-a").Assets) != 1 {
			t.Fatalf("per-group trim = %#v, %v", got, err)
		}
		got, err = Rank(ctx, paths, RankOptions{IDsFile: ids, Group: "time", GapSeconds: 90})
		if err != nil || len(got.Groups) != 6 {
			t.Fatalf("time grouping = %#v, %v", got, err)
		}
		got, err = Rank(ctx, paths, RankOptions{IDsFile: ids, Group: "day"})
		if err != nil || len(got.Groups) != 5 {
			t.Fatalf("local-midnight day grouping = %#v, %v", got, err)
		}
	})
	t.Run("junk", func(t *testing.T) {
		shots, err := Junk(ctx, paths, JunkOptions{Kind: "screenshots", OlderThan: "30d"})
		if err != nil || !sameCandidateIDs(shots.Candidates, "shot-old") {
			t.Fatalf("screenshots = %#v, %v", shots, err)
		}
		blurry, err := Junk(ctx, paths, JunkOptions{Kind: "blurry"})
		if err != nil || !sameCandidateIDs(blurry.Candidates, "blurry-low") || blurry.Percentiles["media_blurriness_max"] != .3 {
			t.Fatalf("blurry = %#v, %v", blurry, err)
		}
		eyes, err := Junk(ctx, paths, JunkOptions{Kind: "eyes-closed"})
		if err != nil || !sameCandidateIDs(eyes.Candidates, "burst-closed", "time-closed") || eyes.Candidates[0].KeepInstead != "burst-open" || eyes.Candidates[1].KeepInstead != "time-open" {
			t.Fatalf("eyes closed = %#v, %v", eyes, err)
		}
		dups, err := Junk(ctx, paths, JunkOptions{Kind: "duplicates"})
		if err != nil || !sameCandidateIDs(dups.Candidates, "dup-small") || dups.Candidates[0].KeepInstead != "dup-large" {
			t.Fatalf("duplicates = %#v, %v", dups, err)
		}
		limited, err := Junk(ctx, paths, JunkOptions{Kind: "all", Limit: 1})
		if err != nil || len(limited.Candidates) != 1 {
			t.Fatalf("limit = %#v, %v", limited, err)
		}
	})
	t.Run("exclude", func(t *testing.T) {
		exclude := writeCurationIDs(t, paths.DataDir, "exclude.txt", "shot-old", "blurry-local/L0/001")
		found, err := Find(ctx, paths, FindOptions{ExcludeIDsFile: exclude, Limit: 100})
		if err != nil || hasID(found.Assets, "shot-old") || hasID(found.Assets, "blurry-low") {
			t.Fatalf("find exclusions = %#v, %v", found, err)
		}
		ids := writeCurationIDs(t, paths.DataDir, "excluded-rank.txt", "shot-old", "blurry-local/L0/001")
		ranked, err := Rank(ctx, paths, RankOptions{IDsFile: ids, ExcludeIDsFile: exclude})
		if err != nil || len(ranked.Groups) != 0 {
			t.Fatalf("rank exclusions = %#v, %v", ranked, err)
		}
		junk, err := Junk(ctx, paths, JunkOptions{Kind: "all", ExcludeIDsFile: exclude, Limit: 100})
		if err != nil || hasCandidateID(junk.Candidates, "shot-old") || hasCandidateID(junk.Candidates, "blurry-low") {
			t.Fatalf("junk exclusions = %#v, %v", junk, err)
		}
	})
}

func curationFixture(t *testing.T) Paths {
	t.Helper()
	paths := testPaths(t)
	db, err := store.Open(context.Background(), store.Options{Path: paths.Database, Schema: Schema, SchemaVersion: SchemaVersion})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	execTestSQL(t, db.DB(), `insert into source_library values ('fixture-library', '/fixture', 'snapshot', '2025-01-01T00:00:00Z', 'test', '{}')`)
	add := func(id, local, media, created, tz, burst, subtype string, hidden, favorite, width, height int, aesthetic, focus *float64) {
		execTestSQL(t, db.DB(), `insert into asset(id,local_identifier,media_type,media_subtypes,creation_date,modification_date,added_date,timezone_name,width,height,duration_seconds,favorite,hidden,burst_identifier,represents_burst,source_library_id,metadata_json) values(?,?,?,?,?,'','',?,?,?,0,?,?,?,0,'fixture-library','{}')`, id, local, media, subtype, created, tz, width, height, favorite, hidden, burst)
		if aesthetic == nil && focus == nil {
			return
		}
		values := map[string]float64{}
		if aesthetic != nil {
			values["overall_aesthetic"] = *aesthetic
		}
		if focus != nil {
			values["sharply_focused_subject"] = *focus
		}
		body, e := json.Marshal(values)
		if e != nil {
			t.Fatal(e)
		}
		execTestSQL(t, db.DB(), `insert into model_observation values(?,?,'apple_quality_scores','',?,1,'fixture','fixture','fixture',?)`, "quality-"+id, id, string(body), "evidence-quality-"+id)
	}
	face := func(id, label, kind string, quality float64, closed int) {
		execTestSQL(t, db.DB(), `insert into face_observation(id,asset_id,face_local_id,person_label,person_uuid,person_kind,confidence,quality,blur_score,eyes_closed,smile,bounding_box_json,source,evidence_id) values(?,?,?,?,?,?,1,?,null,?,null,'{}',?,?)`, "face-"+id+"-"+label, id, label, label, "uuid-"+label, kind, quality, closed, photosLibraryDBFaceSource, "evidence-face-"+id+"-"+label)
	}
	venue := func(id, label string) {
		execTestSQL(t, db.DB(), `insert into visual_observation values(?,?,'apple_venue',?,1,'{}','fixture','fixture',?)`, "venue-"+id, id, label, "evidence-venue-"+id)
	}
	dup := func(id string) {
		execTestSQL(t, db.DB(), `insert into model_observation values(?,?,'apple_duplicate','',?,1,'fixture','fixture','fixture',?)`, "duplicate-"+id, id, `{"perceptual_group":"fixture-duplicates"}`, "evidence-duplicate-"+id)
	}
	low, mid, high := .1, .5, .9
	add("find-both", "find-both-local", "image", "2025-01-10T12:00:00Z", "UTC", "", "0", 0, 0, 100, 100, &high, nil)
	add("find-end", "find-end-local", "image", "2025-01-10T23:59:59Z", "UTC", "", "0", 0, 0, 100, 100, &mid, nil)
	add("find-alex", "find-alex-local", "image", "2025-01-10T12:00:00Z", "UTC", "", "0", 0, 0, 100, 100, nil, nil)
	add("find-video", "find-video-local", "video", "2025-01-10T12:00:00Z", "UTC", "", "0", 0, 0, 100, 100, nil, nil)
	add("find-hidden", "find-hidden-local", "image", "2025-01-10T12:00:00Z", "UTC", "", "0", 1, 0, 100, 100, nil, nil)
	for _, id := range []string{"find-both", "find-end", "find-video", "find-hidden"} {
		face(id, "Alex", "person", .8, 0)
		face(id, "Sam", "pet", .7, 0)
	}
	face("find-alex", "Alex", "person", .8, 0)
	venue("find-both", "Central Park")
	venue("find-end", "CENTRAL PARK")
	add("landscape", "landscape-local", "image", "2025-01-11T10:00:00Z", "UTC", "", "0", 0, 0, 500, 400, &high, nil)
	add("face-low", "face-low-local", "image", "2025-01-11T10:01:00Z", "UTC", "", "0", 0, 0, 500, 400, &low, nil)
	face("face-low", "Casey", "person", .3, 0)
	add("burst-closed", "burst-closed-local", "image", "2025-01-12T10:00:00Z", "UTC", "burst-a", "0", 0, 0, 500, 400, &mid, nil)
	add("burst-open", "burst-open-local", "image", "2025-01-12T10:00:01Z", "UTC", "burst-a", "0", 0, 0, 500, 400, &mid, nil)
	face("burst-closed", "Casey", "person", .8, 1)
	face("burst-open", "Casey", "person", .8, 0)
	add("time-one", "time-one-local", "image", "2025-01-13T10:00:00Z", "UTC", "", "0", 0, 0, 500, 400, nil, nil)
	add("time-two", "time-two-local", "image", "2025-01-13T10:10:00Z", "UTC", "", "0", 0, 0, 500, 400, nil, nil)
	add("day-before", "day-before-local", "image", "2025-01-14T22:30:00Z", "Europe/Madrid", "", "0", 0, 0, 500, 400, nil, nil)
	add("day-after", "day-after-local", "image", "2025-01-14T23:30:00Z", "Europe/Madrid", "", "0", 0, 0, 500, 400, nil, nil)
	for _, id := range []string{"time-one", "time-two", "day-before", "day-after"} {
		face(id, "Avery", "unknown", .7, 0)
	}
	add("shot-old", "shot-old-local", "image", "2020-01-01T00:00:00Z", "UTC", "", "4", 0, 0, 100, 100, nil, nil)
	add("shot-portrait", "shot-portrait-local", "image", "2020-01-01T00:00:00Z", "UTC", "", "16", 0, 0, 100, 100, nil, nil)
	add("shot-favorite", "shot-favorite-local", "image", "2020-01-01T00:00:00Z", "UTC", "", "4", 0, 1, 100, 100, nil, nil)
	add("shot-recent", "shot-recent-local", "image", "2099-01-01T00:00:00Z", "UTC", "", "4", 0, 0, 100, 100, nil, nil)
	add("blurry-low", "blurry-local/L0/001", "image", "2025-02-01T00:00:00Z", "UTC", "", "0", 0, 0, 100, 100, &low, &low)
	add("dull-sharp", "dull-sharp-local", "image", "2025-02-01T01:00:00Z", "UTC", "", "0", 0, 0, 100, 100, &low, &low)
	blurriness := func(id string, value float64) {
		execTestSQL(t, db.DB(), `update model_observation set value_json = json_set(value_json, '$.media_blurriness', ?) where id = ?`, value, "quality-"+id)
	}
	blurriness("blurry-low", 0.1)
	blurriness("dull-sharp", 0.95)
	add("time-closed", "time-closed-local", "image", "2025-02-02T00:00:00Z", "UTC", "", "0", 0, 0, 100, 100, nil, nil)
	add("time-open", "time-open-local", "image", "2025-02-02T00:01:00Z", "UTC", "", "0", 0, 0, 100, 100, nil, nil)
	add("closed-alone", "closed-alone-local", "image", "2025-02-03T00:00:00Z", "UTC", "", "0", 0, 0, 100, 100, nil, nil)
	face("time-closed", "Casey", "person", .8, 1)
	face("time-open", "Casey", "person", .8, 0)
	face("closed-alone", "Casey", "person", .8, 1)
	add("dup-small", "dup-small-local", "image", "2025-03-01T00:00:00Z", "UTC", "", "0", 0, 0, 100, 100, nil, nil)
	add("dup-large", "dup-large-local", "image", "2025-03-01T00:00:01Z", "UTC", "", "0", 0, 0, 200, 200, nil, nil)
	dup("dup-small")
	dup("dup-large")
	return paths
}

func writeCurationIDs(t *testing.T, dir, name string, ids ...string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(joinLines(ids)), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
func joinLines(ids []string) string {
	out := ""
	for _, id := range ids {
		out += id + "\n"
	}
	return out
}
func sameIDs(as []AssetRow, ids ...string) bool {
	if len(as) != len(ids) {
		return false
	}
	for i, id := range ids {
		if as[i].ID != id {
			return false
		}
	}
	return true
}
func sameCandidateIDs(as []JunkCandidate, ids ...string) bool {
	if len(as) != len(ids) {
		return false
	}
	for i, id := range ids {
		if as[i].ID != id {
			return false
		}
	}
	return true
}
func hasID(as []AssetRow, id string) bool {
	for _, a := range as {
		if a.ID == id {
			return true
		}
	}
	return false
}
func hasCandidateID(as []JunkCandidate, id string) bool {
	for _, a := range as {
		if a.ID == id {
			return true
		}
	}
	return false
}
func containsText(as []string, want string) bool {
	for _, a := range as {
		if a == want {
			return true
		}
	}
	return false
}
func rankGroup(result RankResult, key string) RankGroup {
	for _, group := range result.Groups {
		if group.Key == key {
			return group
		}
	}
	return RankGroup{}
}
