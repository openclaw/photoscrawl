package archive

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/openclaw/crawlkit/store"
)

func TestBlockedCategoriesInPhrase(t *testing.T) {
	tests := []struct {
		phrase string
		want   []string
	}{
		{phrase: "document", want: []string{"document"}},
		{phrase: "no documents or receipts", want: []string{}},
		{phrase: "passport visible, no faces", want: []string{"passport"}},
		{phrase: "document, no faces", want: []string{"document"}},
		{phrase: "face not visible", want: []string{}},
		{phrase: "license_plate", want: []string{"license plate"}},
		{phrase: "license plate", want: []string{"license plate"}},
		{phrase: "without a license plate", want: []string{}},
		{phrase: "not a receipt", want: []string{}},
		{phrase: "no document but passport", want: []string{"passport"}},
		{phrase: "child", want: []string{}},
		{phrase: "driver's license visible", want: []string{"driver's license"}},
		{phrase: "drivers license visible", want: []string{"driver's license"}},
		{phrase: "driver license visible", want: []string{"driver's license"}},
		{phrase: "driving licence visible", want: []string{"driver's license"}},
		{phrase: "driving license visible", want: []string{"driver's license"}},
		{phrase: "social security card visible", want: []string{"social security"}},
		{phrase: "social security number visible", want: []string{"social security"}},
		{phrase: "ssn visible", want: []string{"social security"}},
		{phrase: "password visible, no faces", want: []string{"password"}},
		{phrase: "passcode visible", want: []string{"password"}},
		{phrase: "pin code visible", want: []string{"password"}},
		{phrase: "pin number visible", want: []string{"password"}},
		{phrase: "api key visible", want: []string{"access credential"}},
		{phrase: "access token visible", want: []string{"access credential"}},
		{phrase: "recovery code visible", want: []string{"access credential"}},
		{phrase: "seed phrase visible", want: []string{"access credential"}},
		{phrase: "account number visible", want: []string{"bank account"}},
		{phrase: "iban visible", want: []string{"bank account"}},
		{phrase: "routing number visible", want: []string{"bank account"}},
		{phrase: "swift visible", want: []string{"bank account"}},
		{phrase: "sort code visible", want: []string{"bank account"}},
		{phrase: "card number visible", want: []string{"payment card"}},
		{phrase: "debit card visible", want: []string{"payment card"}},
		{phrase: "bank card visible", want: []string{"payment card"}},
		{phrase: "cvv visible", want: []string{"payment card"}},
		{phrase: "cvc visible", want: []string{"payment card"}},
		{phrase: "tax id visible", want: []string{"identity document"}},
		{phrase: "tax return visible", want: []string{"identity document"}},
		{phrase: "national id visible", want: []string{"identity document"}},
		{phrase: "residence permit visible", want: []string{"identity document"}},
		{phrase: "travel visa visible", want: []string{"identity document"}},
		{phrase: "work permit visible", want: []string{"identity document"}},
		{phrase: "insurance card visible", want: []string{"identity document"}},
		{phrase: "contract visible", want: []string{"sensitive document"}},
		{phrase: "bank statement visible", want: []string{"sensitive document"}},
		{phrase: "pay slip visible", want: []string{"sensitive document"}},
		{phrase: "payslip visible", want: []string{"sensitive document"}},
		{phrase: "qr code visible", want: []string{"qr code"}},
		{phrase: "no driver's license visible", want: []string{}},
		{phrase: "no social security card visible", want: []string{}},
		{phrase: "no password visible", want: []string{}},
		{phrase: "no api key visible", want: []string{}},
		{phrase: "no account number visible", want: []string{}},
		{phrase: "no card number visible", want: []string{}},
		{phrase: "no tax id visible", want: []string{}},
		{phrase: "no contract visible", want: []string{}},
		{phrase: "no qr code visible", want: []string{}},
	}
	for _, test := range tests {
		t.Run(test.phrase, func(t *testing.T) {
			got := blockedCategoriesInPhrase(test.phrase)
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("blockedCategoriesInPhrase(%q) = %#v, want %#v", test.phrase, got, test.want)
			}
		})
	}
}

func TestShareCheckStatusesNotesCountsAndExclusions(t *testing.T) {
	ctx := context.Background()
	paths := shareCheckFixture(t)
	ids := writeCurationIDs(t, paths.DataDir, "share-ids.txt", "pass", "hidden", "screenshot", "privacy", "unreviewed", "child")
	exclude := writeCurationIDs(t, paths.DataDir, "share-exclude.txt", "screenshot-local")
	got, err := ShareCheck(ctx, paths, ShareCheckOptions{IDsFile: ids, ExcludeIDsFile: exclude})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Assets) != 5 {
		t.Fatalf("assets = %#v", got.Assets)
	}
	if !reflect.DeepEqual(got.Counts, map[string]int{"pass": 2, "blocked": 2, "unreviewed": 1}) {
		t.Fatalf("counts = %#v", got.Counts)
	}
	byID := map[string]ShareCheckAsset{}
	for _, asset := range got.Assets {
		byID[asset.ID] = asset
	}
	if byID["pass"].Status != "pass" || len(byID["pass"].Reasons) != 0 {
		t.Fatalf("pass = %#v", byID["pass"])
	}
	if byID["hidden"].Status != "blocked" || !containsText(byID["hidden"].Reasons, "hidden") {
		t.Fatalf("hidden = %#v", byID["hidden"])
	}
	if byID["privacy"].Status != "blocked" || !containsText(byID["privacy"].Reasons, "privacy category: passport") {
		t.Fatalf("privacy = %#v", byID["privacy"])
	}
	if byID["unreviewed"].Status != "unreviewed" {
		t.Fatalf("unreviewed = %#v", byID["unreviewed"])
	}
	if byID["child"].Status != "pass" || !reflect.DeepEqual(byID["child"].Notes, []string{"child and face visible"}) {
		t.Fatalf("child note = %#v", byID["child"])
	}
}

func TestShareCheckRejectsMissingIDsFileAndUnknownAsset(t *testing.T) {
	paths := shareCheckFixture(t)
	if _, err := ShareCheck(context.Background(), paths, ShareCheckOptions{}); err == nil {
		t.Fatal("missing --ids-file accepted")
	}
	ids := writeCurationIDs(t, paths.DataDir, "unknown.txt", "missing")
	if _, err := ShareCheck(context.Background(), paths, ShareCheckOptions{IDsFile: ids}); err == nil {
		t.Fatal("unknown asset accepted")
	}
}

func shareCheckFixture(t *testing.T) Paths {
	t.Helper()
	root := t.TempDir()
	paths := Paths{DataDir: root, Database: filepath.Join(root, "photos.sqlite")}
	db, err := store.Open(context.Background(), store.Options{Path: paths.Database, Schema: Schema, SchemaVersion: SchemaVersion})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})
	execTestSQL(t, db.DB(), `insert into source_library values ('fixture-library', '/fixture', 'snapshot', '2025-01-01T00:00:00Z', 'test', '{}')`)
	add := func(id, subtype string, hidden bool, state string, phrase string) {
		hiddenValue := 0
		if hidden {
			hiddenValue = 1
		}
		execTestSQL(t, db.DB(), `insert into asset(id,local_identifier,media_type,media_subtypes,creation_date,modification_date,added_date,timezone_name,width,height,duration_seconds,favorite,hidden,burst_identifier,represents_burst,source_library_id,metadata_json) values(?,?, 'image',?,'2025-01-01T00:00:00Z','','','UTC',100,100,0,0,?,'',0,'fixture-library','{}')`, id, id+"-local", subtype, hiddenValue)
		if state != "" {
			execTestSQL(t, db.DB(), `insert into classification_queue values(?,?, 'fixture-library',?,'fixture',0,'2025-01-01T00:00:00Z')`, "queue-"+id, id, state)
		}
		if phrase != "" {
			body, marshalErr := json.Marshal(map[string]string{"text": phrase})
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			execTestSQL(t, db.DB(), `insert into model_observation values(?,?,'privacy_sensitivity','',?,1,'fixture','fixture','fixture',?)`, "privacy-"+id, id, string(body), "evidence-"+id)
		}
	}
	add("pass", "0", false, "content_classified", "no documents or receipts")
	add("hidden", "0", true, "content_classified", "")
	add("screenshot", "4", false, "content_classified", "")
	add("privacy", "0", false, "content_classified", "passport visible, no faces")
	add("unreviewed", "0", false, "pending", "")
	add("child", "0", false, "content_classified", "child and face visible")
	add("sqlite-screenshot", "kind_subtype:10", false, "content_classified", "")
	return paths
}

func TestShareCheckBlocksSQLiteProviderScreenshots(t *testing.T) {
	paths := shareCheckFixture(t)
	ids := writeCurationIDs(t, paths.DataDir, "sqlite-screenshot.txt", "sqlite-screenshot")
	got, err := ShareCheck(context.Background(), paths, ShareCheckOptions{IDsFile: ids})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Assets) != 1 || got.Assets[0].Status != "blocked" || !containsText(got.Assets[0].Reasons, "screenshot") {
		t.Fatalf("SQLite-provider screenshot = %#v", got.Assets)
	}
}
