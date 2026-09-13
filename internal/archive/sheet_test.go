package archive

import (
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/openclaw/crawlkit/store"
)

func TestSheetNumbersAcrossPagesAndSelectsSources(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	paths := Paths{
		DataDir:  filepath.Join(root, "data"),
		Database: filepath.Join(root, "data", "photos.sqlite"),
		CacheDir: filepath.Join(root, "cache", "photoscrawl"),
	}
	derivative := filepath.Join(root, "Synthetic.photoslibrary", "resources", "derivatives", "asset-one.png")
	original := filepath.Join(root, "Synthetic.photoslibrary", "originals", "asset-two.heic")
	writeSyntheticPNG(t, derivative, color.RGBA{R: 220, A: 255})
	if err := os.MkdirAll(filepath.Dir(original), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(original, []byte("synthetic non-JPEG original"), 0o600); err != nil {
		t.Fatal(err)
	}
	createSheetFixture(t, paths, []sheetFixtureAsset{
		{id: "one", localIdentifier: "local-one", resourceType: "photo", localPath: derivative, fallbackOriginalPath: original},
		{id: "two", localIdentifier: "local-two", resourceType: "photo", localPath: original},
		{id: "three", localIdentifier: "local-three"},
		{id: "four", localIdentifier: "local-four"},
		{id: "five", localIdentifier: "local-five"},
	})
	idsFile := writeSheetList(t, root, "ids.txt", "one", "two", "three", "four", "five")
	renderedSources := []string{}
	renderer := func(_ context.Context, sourcePath, destinationPath string, _ float64) error {
		renderedSources = append(renderedSources, sourcePath)
		return writeSyntheticJPEG(destinationPath, color.RGBA{B: 220, A: 255})
	}

	got, err := Sheet(ctx, paths, SheetOptions{
		IDsFile:  idsFile,
		Title:    "Synthetic title is metadata only",
		PerSheet: 3,
		Tile:     80,
		Renderer: renderer,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "Synthetic title is metadata only" {
		t.Fatalf("title = %q", got.Title)
	}
	if len(got.Sheets) != 2 {
		t.Fatalf("sheets = %v", got.Sheets)
	}
	if len(got.Tiles) != 5 {
		t.Fatalf("tiles = %#v", got.Tiles)
	}
	for index, tile := range got.Tiles {
		if tile.N != index+1 {
			t.Fatalf("tile %d number = %d", index, tile.N)
		}
	}
	if got.Tiles[0].Source != "derivative" || got.Tiles[0].LocalIdentifier != "local-one" {
		t.Fatalf("derivative tile = %#v", got.Tiles[0])
	}
	if got.Tiles[1].Source != "original" || got.Tiles[1].LocalIdentifier != "local-two" {
		t.Fatalf("original tile = %#v", got.Tiles[1])
	}
	if got.Tiles[2].Source != "placeholder" || got.Tiles[2].LocalIdentifier != "local-three" {
		t.Fatalf("placeholder tile = %#v", got.Tiles[2])
	}
	if !slices.Equal(renderedSources, []string{original}) {
		t.Fatalf("rendered sources = %v, want only original", renderedSources)
	}
	assertSheetMode(t, filepath.Dir(got.Sheets[0]), 0o700)
	for index, path := range got.Sheets {
		assertSheetMode(t, path, 0o600)
		file, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := jpeg.Decode(file)
		if closeErr := file.Close(); closeErr != nil {
			t.Fatal(closeErr)
		}
		if err != nil {
			t.Fatal(err)
		}
		wantWidth := 3 * 80
		if index == 1 {
			wantWidth = 2 * 80
		}
		if decoded.Bounds().Dx() != wantWidth || decoded.Bounds().Dy() != 80 {
			t.Fatalf("sheet %d bounds = %v", index, decoded.Bounds())
		}
	}
}

func TestSheetFilesUseOriginalOrPlaceholderAndDoNotDrawTitle(t *testing.T) {
	root := t.TempDir()
	paths := Paths{CacheDir: filepath.Join(root, "cache")}
	file := filepath.Join(root, "input.png")
	missing := filepath.Join(root, "missing.png")
	writeSyntheticPNG(t, file, color.RGBA{G: 220, A: 255})
	filesFile := writeSheetList(t, root, "files.txt", file, missing)
	renderer := func(context.Context, string, string, float64) error {
		return os.ErrInvalid
	}

	first, err := Sheet(context.Background(), paths, SheetOptions{
		FilesFile: filesFile,
		Title:     "First title",
		Tile:      80,
		Renderer:  renderer,
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := Sheet(context.Background(), paths, SheetOptions{
		FilesFile: filesFile,
		Title:     "A completely different title",
		Tile:      80,
		Renderer:  renderer,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Tiles) != 2 || first.Tiles[0].File != file || first.Tiles[0].Source != "original" || first.Tiles[1].File != missing || first.Tiles[1].Source != "placeholder" {
		t.Fatalf("file tiles = %#v", first.Tiles)
	}
	firstJPEG, err := os.ReadFile(first.Sheets[0])
	if err != nil {
		t.Fatal(err)
	}
	secondJPEG, err := os.ReadFile(second.Sheets[0])
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(firstJPEG, secondJPEG) {
		t.Fatal("title changed rendered sheet pixels")
	}
}

type sheetFixtureAsset struct {
	id                   string
	localIdentifier      string
	resourceType         string
	localPath            string
	fallbackOriginalPath string
}

func createSheetFixture(t *testing.T, paths Paths, assets []sheetFixtureAsset) {
	t.Helper()
	db, err := store.Open(context.Background(), store.Options{
		Path:          paths.Database,
		Schema:        Schema,
		SchemaVersion: SchemaVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB().Exec(`insert into source_library values ('sheet-library', '/synthetic', 'snapshot', '2026-01-01T00:00:00Z', 'test', '{}')`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	for _, asset := range assets {
		_, err := db.DB().Exec(`
insert into asset(
  id, local_identifier, media_type, media_subtypes, creation_date,
  modification_date, added_date, timezone_name, width, height,
  duration_seconds, favorite, hidden, burst_identifier, represents_burst,
  source_library_id, metadata_json
) values (?, ?, 'image', '0', '2026-01-01T00:00:00Z', '', '', 'UTC', 100, 100, 0, 0, 0, '', 0, 'sheet-library', '{}')
`, asset.id, asset.localIdentifier)
		if err != nil {
			db.Close()
			t.Fatal(err)
		}
		if asset.localPath == "" {
			continue
		}
		_, err = db.DB().Exec(`
insert into asset_resource(
  id, asset_id, source_identifier, resource_type, uti, original_filename,
  local_path, file_size, sha256, available_locally, needs_download
) values (?, ?, ?, ?, 'public.image', 'synthetic', ?, 1, '', 1, 0)
`, "resource-"+asset.id, asset.id, "source-"+asset.id, asset.resourceType, asset.localPath)
		if err != nil {
			db.Close()
			t.Fatal(err)
		}
		if asset.fallbackOriginalPath == "" {
			continue
		}
		_, err = db.DB().Exec(`
insert into asset_resource(
  id, asset_id, source_identifier, resource_type, uti, original_filename,
  local_path, file_size, sha256, available_locally, needs_download
) values (?, ?, ?, 'original', 'public.heic', 'synthetic.heic', ?, 1, '', 1, 0)
`, "resource-original-"+asset.id, asset.id, "source-original-"+asset.id, asset.fallbackOriginalPath)
		if err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func writeSheetList(t *testing.T, root, name string, entries ...string) string {
	t.Helper()
	path := filepath.Join(root, name)
	contents := []byte(stringsJoinLines(entries))
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func stringsJoinLines(entries []string) string {
	result := ""
	for _, entry := range entries {
		result += entry + "\n"
	}
	return result
}

func writeSyntheticPNG(t *testing.T, path string, fill color.Color) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	image := image.NewRGBA(image.Rect(0, 0, 12, 8))
	fillRect(image, image.Bounds(), fill)
	if err := png.Encode(file, image); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func writeSyntheticJPEG(path string, fill color.Color) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	image := image.NewRGBA(image.Rect(0, 0, 8, 12))
	fillRect(image, image.Bounds(), fill)
	if err := jpeg.Encode(file, image, &jpeg.Options{Quality: 90}); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

func assertSheetMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %#o, want %#o", path, got, want)
	}
}
