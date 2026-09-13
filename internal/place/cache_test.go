package place

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCachedPlaceContextKeepsRequestIdentity(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	input := Input{
		AssetID: "current-asset", ImagePath: "current.png", TakenAt: "2026-09-12T12:00:00Z",
		Location: Coordinate{Latitude: 12, Longitude: 34}, AccuracyMeters: 5,
	}
	inputPath := filepath.Join(root, "input.json")
	if err := writeJSONFile(inputPath, input); err != nil {
		t.Fatal(err)
	}
	path, err := cachePath(root, input, defaultRadiusMeters)
	if err != nil {
		t.Fatal(err)
	}
	cached := Result{
		Input:   Input{AssetID: "previous-asset", ImagePath: "previous.png", TakenAt: "2020-01-01T00:00:00Z", Location: input.Location, AccuracyMeters: input.AccuracyMeters},
		Address: &Address{Formatted: "Synthetic address"}, POIStatus: POIStatusNone,
		GeneratedAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
	}
	if err := writeJSONFile(path, cached); err != nil {
		t.Fatal(err)
	}
	result, err := Run(context.Background(), Options{InputPath: inputPath, CacheDir: root})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Cached || result.Input != input {
		t.Fatalf("cache reused another asset's identity: %#v", result)
	}
	if result.Address.Formatted != cached.Address.Formatted || !result.GeneratedAt.Equal(cached.GeneratedAt) {
		t.Fatalf("provider evidence changed: %#v", result)
	}
}

func TestRenderCardWithoutAddress(t *testing.T) {
	t.Parallel()
	card := RenderCard(Result{POIStatus: POIStatusNone, RadiusMeters: 150})
	if !strings.Contains(card, "Address: unavailable") || !strings.Contains(card, "No named nearby POIs within 150m") {
		t.Fatalf("partial card = %q", card)
	}
}
