package archive

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/openclaw/crawlkit/store"
)

func TestForgottenFiltersGroupsOrdersAndLimits(t *testing.T) {
	ctx := context.Background()
	paths := forgottenFixture(t)

	got, err := Forgotten(ctx, paths, ForgottenOptions{GapHours: 3, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if got.Threshold != .9 {
		t.Fatalf("threshold = %v, want 0.9", got.Threshold)
	}
	if got.GroupsConsidered != 4 {
		t.Fatalf("groups considered = %d, want 4", got.GroupsConsidered)
	}
	if !sameForgottenIDs(got.Assets, "group-best", "group-second", "group-third", "smart-album") {
		t.Fatalf("filtered and ordered assets = %#v", got.Assets)
	}
	if !containsText(got.Assets[0].Reasons, "overall aesthetic 0.90 (90th percentile 0.90)") {
		t.Fatalf("threshold reason = %#v", got.Assets[0].Reasons)
	}
	if !containsText(got.Assets[0].Reasons, "best of 2 qualifying photos in moment") {
		t.Fatalf("group reason = %#v", got.Assets[0].Reasons)
	}

	limited, err := Forgotten(ctx, paths, ForgottenOptions{GapHours: 3, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if limited.GroupsConsidered != 4 || !sameForgottenIDs(limited.Assets, "group-best", "group-second") {
		t.Fatalf("limited result = %#v", limited)
	}
}

func TestForgottenDateAndExcludeFilters(t *testing.T) {
	ctx := context.Background()
	paths := forgottenFixture(t)

	got, err := Forgotten(ctx, paths, ForgottenOptions{From: "2025-01-10", To: "2025-01-10", GapHours: 3})
	if err != nil {
		t.Fatal(err)
	}
	if got.Threshold != .9 || got.GroupsConsidered != 2 || !sameForgottenIDs(got.Assets, "group-best", "group-second") {
		t.Fatalf("date-filtered result = %#v", got)
	}

	excludePath := filepath.Join(paths.DataDir, "exclude.txt")
	if err := os.WriteFile(excludePath, []byte("group-best\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = Forgotten(ctx, paths, ForgottenOptions{ExcludeIDsFile: excludePath, GapHours: 3})
	if err != nil {
		t.Fatal(err)
	}
	if got.GroupsConsidered != 4 || !sameForgottenIDs(got.Assets, "group-second", "group-third", "smart-album", "group-weaker") {
		t.Fatalf("excluded result = %#v", got)
	}
}

func TestForgottenRejectsNegativeGap(t *testing.T) {
	if _, err := Forgotten(context.Background(), Paths{}, ForgottenOptions{GapHours: -1}); err == nil {
		t.Fatal("negative gap hours accepted")
	}
}

func forgottenFixture(t *testing.T) Paths {
	t.Helper()
	paths := testPaths(t)
	db, err := store.Open(context.Background(), store.Options{Path: paths.Database, Schema: Schema, SchemaVersion: SchemaVersion})
	if err != nil {
		t.Fatal(err)
	}
	execTestSQL(t, db.DB(), `insert into source_library(id,library_path,snapshot_path,snapshot_created_at,photos_version,metadata_json) values('forgotten-library','/fixture','snapshot','2025-01-01T00:00:00Z','test','{}')`)
	add := func(id, media, created, subtype string, hidden, favorite int, aesthetic, focus, blurriness *float64) {
		execTestSQL(t, db.DB(), `insert into asset(id,local_identifier,media_type,media_subtypes,creation_date,modification_date,added_date,timezone_name,width,height,duration_seconds,favorite,hidden,burst_identifier,represents_burst,source_library_id,metadata_json) values(?,?,?,?,?,'','','UTC',100,100,0,?,?,'',0,'forgotten-library','{}')`, id, id+"-local", media, subtype, created, favorite, hidden)
		values := map[string]float64{}
		if aesthetic != nil {
			values["overall_aesthetic"] = *aesthetic
		}
		if focus != nil {
			values["sharply_focused_subject"] = *focus
		}
		if blurriness != nil {
			values["media_blurriness"] = *blurriness
		}
		if len(values) == 0 {
			return
		}
		body, err := json.Marshal(values)
		if err != nil {
			t.Fatal(err)
		}
		execTestSQL(t, db.DB(), `insert into model_observation(id,asset_id,observation_type,value_text,value_json,confidence,source,model_id,prompt_version,evidence_id) values(?,?,'apple_quality_scores','',?,1,'fixture','fixture','fixture',?)`, "quality-"+id, id, string(body), "evidence-quality-"+id)
	}
	addAlbum := func(id, kind string) {
		execTestSQL(t, db.DB(), `insert into album_membership(id,asset_id,album_id,album_title,album_kind,folder_path) values(?,?,?,?,?,'')`, "membership-"+id, id, "album-"+id, "Fixture", kind)
	}
	addClosedFace := func(id string) {
		execTestSQL(t, db.DB(), `insert into face_observation(id,asset_id,face_local_id,person_label,person_uuid,person_kind,confidence,quality,blur_score,eyes_closed,smile,bounding_box_json,source,evidence_id) values(?,?,?,?,?,'person',1,.8,null,1,null,'{}',?,?)`, "face-"+id, id, "face-local-"+id, "Person", "person-uuid", photosLibraryDBFaceSource, "evidence-face-"+id)
	}

	low := .5
	for i := 0; i < 20; i++ {
		id := "low-" + string(rune('a'+i))
		add(id, "image", "2024-12-01T00:00:00Z", "0", 0, 0, &low, nil, nil)
	}
	high := .9
	weakFocus := .1
	bestFocus := .95
	secondFocus := .9
	thirdFocus := .8
	smartFocus := .7
	add("group-weaker", "image", "2025-01-10T10:00:00Z", "0", 0, 0, &high, &weakFocus, nil)
	add("group-best", "image", "2025-01-10T11:00:00Z", "0", 0, 0, &high, &bestFocus, nil)
	add("group-second", "image", "2025-01-10T20:00:00Z", "0", 0, 0, &high, &secondFocus, nil)
	add("group-third", "image", "2025-01-11T02:00:00Z", "0", 0, 0, &high, &thirdFocus, nil)
	add("smart-album", "image", "2025-01-11T10:00:00Z", "0", 0, 0, &high, &smartFocus, nil)
	addAlbum("smart-album", "album:2:1")

	add("user-album", "image", "2025-01-12T00:00:00Z", "0", 0, 0, &high, nil, nil)
	addAlbum("user-album", "album:1:2")
	add("shared-album", "image", "2025-01-12T06:00:00Z", "0", 0, 0, &high, nil, nil)
	addAlbum("shared-album", "album:1:101")
	add("screenshot", "image", "2025-01-12T12:00:00Z", "4", 0, 0, &high, nil, nil)
	blurry := .3
	add("blurry", "image", "2025-01-12T18:00:00Z", "0", 0, 0, &high, nil, &blurry)
	add("hidden", "image", "2025-01-13T00:00:00Z", "0", 1, 0, &high, nil, nil)
	add("favorite", "image", "2025-01-13T06:00:00Z", "0", 0, 1, &high, nil, nil)
	add("closed-eyes", "image", "2025-01-13T12:00:00Z", "0", 0, 0, &high, nil, nil)
	addClosedFace("closed-eyes")
	add("video", "video", "2025-01-13T18:00:00Z", "0", 0, 0, &high, nil, nil)
	add("deleted", "image", "2025-01-14T00:00:00Z", "0", 0, 0, &high, nil, nil)
	execTestSQL(t, db.DB(), `update asset set deleted_at='2025-01-15T00:00:00Z' where id='deleted'`)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return paths
}

func sameForgottenIDs(assets []ForgottenAsset, ids ...string) bool {
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
