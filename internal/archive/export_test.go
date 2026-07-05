package archive

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/openclaw/photoscrawl/internal/photos"
)

func TestExportWritesOriginalThroughPhotoKitExporter(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	paths := testPaths(t)
	libraryPath := filepath.Join(t.TempDir(), "Fixture Photos Library.photoslibrary")
	if err := mkdirLibrary(libraryPath); err != nil {
		t.Fatal(err)
	}
	if _, err := Crawl(ctx, paths, CrawlOptions{
		LibraryPath: libraryPath,
		Provider:    fakeProvider{snapshot: fakeSnapshot(false, false)},
		Now:         fixedClock("2026-05-28T10:00:00Z"),
	}); err != nil {
		t.Fatal(err)
	}
	search, err := Search(ctx, paths, SearchOptions{Query: "beach", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(search.Results) != 1 {
		t.Fatalf("search results = %#v", search.Results)
	}

	var gotIdentifier string
	var gotAllowNetwork bool
	outDir := t.TempDir()
	result, err := Export(ctx, paths, ExportOptions{
		ID:                   search.Results[0].ID,
		Output:               outDir,
		AllowICloudDownloads: true,
		ExportOriginal: func(_ context.Context, localIdentifier, destinationPath string, allowNetwork bool) error {
			gotIdentifier = localIdentifier
			gotAllowNetwork = allowNetwork
			return os.WriteFile(destinationPath, []byte("exported image"), 0o600)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotIdentifier != "fixture-asset-1" || !gotAllowNetwork {
		t.Fatalf("exporter identifier=%q allow=%v", gotIdentifier, gotAllowNetwork)
	}
	if result.Output != filepath.Join(outDir, "fixture-asset-1-Screenshot Beach Fixture.heic") || result.Bytes != int64(len("exported image")) || !result.Original || result.Source != "photokit_original_export" {
		t.Fatalf("result = %#v", result)
	}
}

func TestExportRejectsNonImageAssets(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	paths := testPaths(t)
	libraryPath := filepath.Join(t.TempDir(), "Fixture Photos Library.photoslibrary")
	if err := mkdirLibrary(libraryPath); err != nil {
		t.Fatal(err)
	}
	if _, err := Crawl(ctx, paths, CrawlOptions{
		LibraryPath: libraryPath,
		Provider:    fakeProvider{snapshot: fakeSnapshot(false, true)},
		Now:         fixedClock("2026-05-28T10:00:00Z"),
	}); err != nil {
		t.Fatal(err)
	}
	search, err := Search(ctx, paths, SearchOptions{Query: "kitchen", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(search.Results) != 1 {
		t.Fatalf("search results = %#v", search.Results)
	}

	_, err = Export(ctx, paths, ExportOptions{
		ID:     search.Results[0].ID,
		Output: filepath.Join(t.TempDir(), "video.mov"),
		ExportOriginal: func(context.Context, string, string, bool) error {
			t.Fatal("exporter should not be called for non-image assets")
			return nil
		},
	})
	if err == nil {
		t.Fatal("expected non-image export to fail")
	}
}

func TestExportCopiesIndexedLocalOriginalResource(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	paths := testPaths(t)
	libraryPath := filepath.Join(t.TempDir(), "Fixture Photos Library.photoslibrary")
	if err := mkdirLibrary(libraryPath); err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join(libraryPath, "originals", "local.jpeg")
	if err := os.MkdirAll(filepath.Dir(sourcePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sourcePath, []byte("local image bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Crawl(ctx, paths, CrawlOptions{
		LibraryPath: libraryPath,
		Provider: fakeProvider{snapshot: photos.LibrarySnapshot{
			Provider:            "fake",
			PhotosVersion:       "fixture",
			AuthorizationStatus: "authorized",
			Assets: []photos.Asset{
				{
					LocalIdentifier: "fixture-local-image",
					MediaType:       "image",
					MediaSubtypes:   "0",
					CreationDate:    "2026-05-27T12:00:00Z",
					Width:           100,
					Height:          80,
					Resources: []photos.Resource{
						{
							Type:             "photo",
							UTI:              "public.jpeg",
							OriginalFilename: "local.jpeg",
							LocalPath:        sourcePath,
							Availability:     "local",
							AvailableLocally: true,
						},
					},
				},
			},
		}},
		Now: fixedClock("2026-05-28T10:00:00Z"),
	}); err != nil {
		t.Fatal(err)
	}
	search, err := Search(ctx, paths, SearchOptions{Query: "local", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(search.Results) != 1 {
		t.Fatalf("search results = %#v", search.Results)
	}
	outDir := t.TempDir()
	result, err := Export(ctx, paths, ExportOptions{
		ID:     search.Results[0].ID,
		Output: outDir,
		ExportOriginal: func(context.Context, string, string, bool) error {
			t.Fatal("PhotoKit exporter should not be called when an indexed local original is readable")
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Source != "local_original_copy" || !result.Original || result.Output != filepath.Join(outDir, "fixture-local-image-local.jpeg") {
		t.Fatalf("result = %#v", result)
	}
	exported, err := os.ReadFile(result.Output)
	if err != nil {
		t.Fatal(err)
	}
	if string(exported) != "local image bytes" {
		t.Fatalf("exported = %q", string(exported))
	}
}

func TestExportFallsBackToPhotoKitWhenIndexedOriginalIsUnreadable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	paths := testPaths(t)
	libraryPath := filepath.Join(t.TempDir(), "Fixture Photos Library.photoslibrary")
	if err := mkdirLibrary(libraryPath); err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join(libraryPath, "originals", "missing.jpeg")
	if _, err := Crawl(ctx, paths, CrawlOptions{
		LibraryPath: libraryPath,
		Provider: fakeProvider{snapshot: photos.LibrarySnapshot{
			Provider:            "fake",
			PhotosVersion:       "fixture",
			AuthorizationStatus: "authorized",
			Assets: []photos.Asset{
				{
					LocalIdentifier: "fixture-stale-original-image",
					MediaType:       "image",
					MediaSubtypes:   "0",
					CreationDate:    "2026-05-27T12:00:00Z",
					Width:           100,
					Height:          80,
					Resources: []photos.Resource{
						{
							Type:             "photo",
							UTI:              "public.jpeg",
							OriginalFilename: "missing.jpeg",
							LocalPath:        sourcePath,
							Availability:     "local",
							AvailableLocally: true,
						},
					},
				},
			},
		}},
		Now: fixedClock("2026-05-28T10:00:00Z"),
	}); err != nil {
		t.Fatal(err)
	}
	search, err := Search(ctx, paths, SearchOptions{Query: "missing", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(search.Results) != 1 {
		t.Fatalf("search results = %#v", search.Results)
	}
	result, err := Export(ctx, paths, ExportOptions{
		ID:     search.Results[0].ID,
		Output: t.TempDir(),
		ExportOriginal: func(_ context.Context, _ string, destinationPath string, _ bool) error {
			return os.WriteFile(destinationPath, []byte("photokit original"), 0o600)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Source != "photokit_original_export" || !result.Original {
		t.Fatalf("result = %#v", result)
	}
	exported, err := os.ReadFile(result.Output)
	if err != nil {
		t.Fatal(err)
	}
	if string(exported) != "photokit original" {
		t.Fatalf("exported = %q", string(exported))
	}
}

func TestExportDirectoryModeAvoidsFilenameCollisions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	paths := testPaths(t)
	libraryPath := filepath.Join(t.TempDir(), "Fixture Photos Library.photoslibrary")
	if err := mkdirLibrary(libraryPath); err != nil {
		t.Fatal(err)
	}
	if _, err := Crawl(ctx, paths, CrawlOptions{
		LibraryPath: libraryPath,
		Provider: fakeProvider{snapshot: photos.LibrarySnapshot{
			Provider:            "fake",
			PhotosVersion:       "fixture",
			AuthorizationStatus: "authorized",
			Assets: []photos.Asset{
				{
					LocalIdentifier: "fixture-collision-a",
					MediaType:       "image",
					MediaSubtypes:   "0",
					CreationDate:    "2026-05-27T12:00:00Z",
					Width:           100,
					Height:          80,
					Resources: []photos.Resource{
						{Type: "photo", UTI: "public.jpeg", OriginalFilename: "IMG_0001.jpeg", Availability: "remote", NeedsDownload: true},
					},
				},
				{
					LocalIdentifier: "fixture-collision-b",
					MediaType:       "image",
					MediaSubtypes:   "0",
					CreationDate:    "2026-05-27T12:01:00Z",
					Width:           100,
					Height:          80,
					Resources: []photos.Resource{
						{Type: "photo", UTI: "public.jpeg", OriginalFilename: "IMG_0001.jpeg", Availability: "remote", NeedsDownload: true},
					},
				},
			},
		}},
		Now: fixedClock("2026-05-28T10:00:00Z"),
	}); err != nil {
		t.Fatal(err)
	}
	search, err := Search(ctx, paths, SearchOptions{Query: "IMG_0001", Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(search.Results) != 2 {
		t.Fatalf("search results = %#v", search.Results)
	}
	outDir := t.TempDir()
	seen := map[string]bool{}
	for _, hit := range search.Results {
		result, err := Export(ctx, paths, ExportOptions{
			ID:     hit.ID,
			Output: outDir,
			ExportOriginal: func(_ context.Context, localIdentifier, destinationPath string, _ bool) error {
				return os.WriteFile(destinationPath, []byte(localIdentifier), 0o600)
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if seen[result.Output] {
			t.Fatalf("duplicate export output path: %s", result.Output)
		}
		seen[result.Output] = true
	}
	for _, name := range []string{
		"fixture-collision-a-IMG_0001.jpeg",
		"fixture-collision-b-IMG_0001.jpeg",
	} {
		if _, err := os.Stat(filepath.Join(outDir, name)); err != nil {
			t.Fatalf("missing export %s: %v", name, err)
		}
	}
}

func TestExportPhotoKitFailureDoesNotDeleteExistingOutput(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	paths := testPaths(t)
	libraryPath := filepath.Join(t.TempDir(), "Fixture Photos Library.photoslibrary")
	if err := mkdirLibrary(libraryPath); err != nil {
		t.Fatal(err)
	}
	if _, err := Crawl(ctx, paths, CrawlOptions{
		LibraryPath: libraryPath,
		Provider:    fakeProvider{snapshot: fakeSnapshot(false, false)},
		Now:         fixedClock("2026-05-28T10:00:00Z"),
	}); err != nil {
		t.Fatal(err)
	}
	search, err := Search(ctx, paths, SearchOptions{Query: "beach", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(search.Results) != 1 {
		t.Fatalf("search results = %#v", search.Results)
	}
	outputPath := filepath.Join(t.TempDir(), "existing.jpeg")
	if err := os.WriteFile(outputPath, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = Export(ctx, paths, ExportOptions{
		ID:     search.Results[0].ID,
		Output: outputPath,
		ExportOriginal: func(_ context.Context, _ string, destinationPath string, _ bool) error {
			if err := os.Remove(destinationPath); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			return os.ErrPermission
		},
	})
	if err == nil {
		t.Fatal("expected export failure")
	}
	existing, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(existing) != "keep me" {
		t.Fatalf("existing output = %q", string(existing))
	}
}

func TestExportUsesPhotoKitForIndexedDerivativeResource(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	paths := testPaths(t)
	libraryPath := filepath.Join(t.TempDir(), "Fixture Photos Library.photoslibrary")
	if err := mkdirLibrary(libraryPath); err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join(libraryPath, "resources", "derivatives", "local.jpeg")
	if err := os.MkdirAll(filepath.Dir(sourcePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sourcePath, []byte("derivative image bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Crawl(ctx, paths, CrawlOptions{
		LibraryPath: libraryPath,
		Provider: fakeProvider{snapshot: photos.LibrarySnapshot{
			Provider:            "fake",
			PhotosVersion:       "fixture",
			AuthorizationStatus: "authorized",
			Assets: []photos.Asset{
				{
					LocalIdentifier: "fixture-derivative-image",
					MediaType:       "image",
					MediaSubtypes:   "0",
					CreationDate:    "2026-05-27T12:00:00Z",
					Width:           100,
					Height:          80,
					Resources: []photos.Resource{
						{
							Type:             "photo",
							UTI:              "public.jpeg",
							OriginalFilename: "local.jpeg",
							LocalPath:        sourcePath,
							Availability:     "local",
							AvailableLocally: true,
						},
					},
				},
			},
		}},
		Now: fixedClock("2026-05-28T10:00:00Z"),
	}); err != nil {
		t.Fatal(err)
	}
	search, err := Search(ctx, paths, SearchOptions{Query: "local", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(search.Results) != 1 {
		t.Fatalf("search results = %#v", search.Results)
	}
	result, err := Export(ctx, paths, ExportOptions{
		ID:     search.Results[0].ID,
		Output: t.TempDir(),
		ExportOriginal: func(_ context.Context, _ string, destinationPath string, _ bool) error {
			return os.WriteFile(destinationPath, []byte("photokit original"), 0o600)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Source != "photokit_original_export" {
		t.Fatalf("result = %#v", result)
	}
	exported, err := os.ReadFile(result.Output)
	if err != nil {
		t.Fatal(err)
	}
	if string(exported) != "photokit original" {
		t.Fatalf("exported = %q", string(exported))
	}
}

func TestExportFallsBackToIndexedDerivativeWhenPhotoKitDenied(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	paths := testPaths(t)
	libraryPath := filepath.Join(t.TempDir(), "Fixture Photos Library.photoslibrary")
	if err := mkdirLibrary(libraryPath); err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join(libraryPath, "resources", "derivatives", "local.jpeg")
	if err := os.MkdirAll(filepath.Dir(sourcePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sourcePath, []byte("derivative image bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Crawl(ctx, paths, CrawlOptions{
		LibraryPath: libraryPath,
		Provider: fakeProvider{snapshot: photos.LibrarySnapshot{
			Provider:            "fake",
			PhotosVersion:       "fixture",
			AuthorizationStatus: "authorized",
			Assets: []photos.Asset{
				{
					LocalIdentifier: "fixture-fallback-image",
					MediaType:       "image",
					MediaSubtypes:   "0",
					CreationDate:    "2026-05-27T12:00:00Z",
					Width:           100,
					Height:          80,
					Resources: []photos.Resource{
						{
							Type:             "photo",
							UTI:              "public.jpeg",
							OriginalFilename: "local.jpeg",
							LocalPath:        sourcePath,
							Availability:     "local",
							AvailableLocally: true,
						},
					},
				},
			},
		}},
		Now: fixedClock("2026-05-28T10:00:00Z"),
	}); err != nil {
		t.Fatal(err)
	}
	search, err := Search(ctx, paths, SearchOptions{Query: "local", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(search.Results) != 1 {
		t.Fatalf("search results = %#v", search.Results)
	}
	result, err := Export(ctx, paths, ExportOptions{
		ID:     search.Results[0].ID,
		Output: t.TempDir(),
		ExportOriginal: func(context.Context, string, string, bool) error {
			return os.ErrPermission
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Source != "local_package_resource_copy" || result.Original {
		t.Fatalf("result = %#v", result)
	}
	exported, err := os.ReadFile(result.Output)
	if err != nil {
		t.Fatal(err)
	}
	if string(exported) != "derivative image bytes" {
		t.Fatalf("exported = %q", string(exported))
	}
}
