package archive

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openclaw/photoscrawl/internal/photos"
)

func TestExportOriginalUsesPhotoKitAndSafeOriginalFilename(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	paths := testPaths(t)
	libraryPath := filepath.Join(t.TempDir(), "Fixture Photos Library.photoslibrary")
	if err := mkdirLibrary(libraryPath); err != nil {
		t.Fatal(err)
	}
	snapshot := fakeSnapshot(false, false)
	snapshot.Assets[0].Resources = []photos.Resource{
		{SourceIdentifier: "fixture-photo", Type: "photo", UTI: "public.heic", OriginalFilename: "../nested/Fixture Original.heic"},
		{SourceIdentifier: "fixture-original", Type: "original", UTI: "public.jpeg", OriginalFilename: "..\\preferred\\Preferred Original.jpg"},
	}
	if _, err := Crawl(ctx, paths, CrawlOptions{LibraryPath: libraryPath, Provider: fakeProvider{snapshot: snapshot}}); err != nil {
		t.Fatal(err)
	}

	assetID := stableID("asset", stableID("source_library", libraryPath), "fixture-asset-1")
	outputDir := filepath.Join(t.TempDir(), "exports")
	var gotIdentifier, gotDestination string
	var gotAllowNetwork bool
	result, err := exportOriginal(ctx, paths, assetID, outputDir, func(_ context.Context, localIdentifier, destinationPath string, allowNetwork bool) error {
		gotIdentifier = localIdentifier
		gotDestination = destinationPath
		gotAllowNetwork = allowNetwork
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Join(outputDir, "Preferred Original.jpg")
	if gotIdentifier != "fixture-asset-1" || gotDestination != wantPath || !gotAllowNetwork {
		t.Fatalf("PhotoKit call = identifier %q destination %q allowNetwork %v", gotIdentifier, gotDestination, gotAllowNetwork)
	}
	if result.AssetID != assetID || result.Path != wantPath || result.Source != photoKitOriginalExportSource || !result.Original {
		t.Fatalf("export result = %#v", result)
	}
}

func TestExportOriginalFallsBackToAssetHexFilename(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	paths := testPaths(t)
	libraryPath := filepath.Join(t.TempDir(), "Fixture Photos Library.photoslibrary")
	if err := mkdirLibrary(libraryPath); err != nil {
		t.Fatal(err)
	}
	snapshot := fakeSnapshot(false, false)
	snapshot.Assets[0].Resources = nil
	if _, err := Crawl(ctx, paths, CrawlOptions{LibraryPath: libraryPath, Provider: fakeProvider{snapshot: snapshot}}); err != nil {
		t.Fatal(err)
	}

	assetID := stableID("asset", stableID("source_library", libraryPath), "fixture-asset-1")
	var gotDestination string
	_, err := exportOriginal(ctx, paths, assetID, t.TempDir(), func(_ context.Context, _, destinationPath string, _ bool) error {
		gotDestination = destinationPath
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.TrimPrefix(assetID, "asset:") + ".bin"; filepath.Base(gotDestination) != want {
		t.Fatalf("fallback filename = %q, want %q", filepath.Base(gotDestination), want)
	}
}

func TestExportOriginalReportsValidationLookupAndPhotoKitErrors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	paths := testPaths(t)
	if _, err := Init(ctx, paths); err != nil {
		t.Fatal(err)
	}
	exporter := func(context.Context, string, string, bool) error { return nil }
	if _, err := exportOriginal(ctx, paths, "", t.TempDir(), exporter); err == nil || err.Error() != "id is required" {
		t.Fatalf("empty id error = %v", err)
	}
	if _, err := exportOriginal(ctx, paths, "asset:missing", "", exporter); err == nil || err.Error() != "output is required" {
		t.Fatalf("empty output error = %v", err)
	}
	if _, err := exportOriginal(ctx, paths, "asset:missing", t.TempDir(), exporter); err == nil || err.Error() != "asset not found: asset:missing" {
		t.Fatalf("missing asset error = %v", err)
	}

	libraryPath := filepath.Join(t.TempDir(), "Fixture Photos Library.photoslibrary")
	if err := os.MkdirAll(libraryPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Crawl(ctx, paths, CrawlOptions{LibraryPath: libraryPath, Provider: fakeProvider{snapshot: fakeSnapshot(false, false)}}); err != nil {
		t.Fatal(err)
	}
	assetID := stableID("asset", stableID("source_library", libraryPath), "fixture-asset-1")
	photoKitErr := errors.New("PhotoKit denied")
	_, err := exportOriginal(ctx, paths, assetID, t.TempDir(), func(context.Context, string, string, bool) error { return photoKitErr })
	if !errors.Is(err, photoKitErr) {
		t.Fatalf("PhotoKit error = %v", err)
	}
}
