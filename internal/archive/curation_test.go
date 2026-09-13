package archive

import (
	"database/sql"
	"testing"
	"time"
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
