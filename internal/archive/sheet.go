package archive

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	_ "image/png"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/openclaw/photoscrawl/internal/photos"
)

const (
	defaultSheetSize = 16
	defaultTileSize  = 480
	maxSheetColumns  = 4
)

type CanonicalJPEGRenderer func(context.Context, string, string, float64) error

type SheetOptions struct {
	IDsFile   string
	FilesFile string
	Title     string
	PerSheet  int
	Tile      int
	Renderer  CanonicalJPEGRenderer
}

type SheetTile struct {
	N               int    `json:"n"`
	ID              string `json:"id,omitempty"`
	File            string `json:"file,omitempty"`
	LocalIdentifier string `json:"local_identifier"`
	Source          string `json:"source"`
}

type SheetResult struct {
	Title  string      `json:"title,omitempty"`
	Sheets []string    `json:"sheets"`
	Tiles  []SheetTile `json:"tiles"`
}

type sheetInput struct {
	id              string
	file            string
	localIdentifier string
	resources       []sheetResource
}

type sheetResource struct {
	path     string
	source   string
	priority int
}

func Sheet(ctx context.Context, paths Paths, opts SheetOptions) (SheetResult, error) {
	if (strings.TrimSpace(opts.IDsFile) == "") == (strings.TrimSpace(opts.FilesFile) == "") {
		return SheetResult{}, errors.New("exactly one of ids file or files file is required")
	}
	if opts.PerSheet == 0 {
		opts.PerSheet = defaultSheetSize
	}
	if opts.Tile == 0 {
		opts.Tile = defaultTileSize
	}
	if opts.PerSheet < 1 {
		return SheetResult{}, errors.New("per-sheet must be positive")
	}
	if opts.Tile < 1 {
		return SheetResult{}, errors.New("tile must be positive")
	}
	if opts.Renderer == nil {
		opts.Renderer = photos.RenderCanonicalJPEG
	}

	inputs, err := loadSheetInputs(ctx, paths, opts)
	if err != nil {
		return SheetResult{}, err
	}
	if len(inputs) == 0 {
		return SheetResult{}, errors.New("input file contains no entries")
	}
	if err := os.MkdirAll(paths.CacheDir, 0o700); err != nil {
		return SheetResult{}, fmt.Errorf("create cache directory: %w", err)
	}
	outputDir, err := os.MkdirTemp(paths.CacheDir, "sheets-")
	if err != nil {
		return SheetResult{}, fmt.Errorf("create sheet directory: %w", err)
	}
	if err := os.Chmod(outputDir, 0o700); err != nil {
		return SheetResult{}, fmt.Errorf("secure sheet directory: %w", err)
	}
	keepOutput := false
	defer func() {
		if !keepOutput {
			_ = os.RemoveAll(outputDir)
		}
	}()

	result := SheetResult{
		Title:  opts.Title,
		Sheets: []string{},
		Tiles:  []SheetTile{},
	}
	for start := 0; start < len(inputs); start += opts.PerSheet {
		end := min(start+opts.PerSheet, len(inputs))
		pageInputs := inputs[start:end]
		canvas, tiles, err := renderSheetPage(ctx, outputDir, pageInputs, start+1, opts.Tile, opts.Renderer)
		if err != nil {
			return SheetResult{}, err
		}
		pageNumber := len(result.Sheets) + 1
		path := filepath.Join(outputDir, fmt.Sprintf("sheet-%03d.jpg", pageNumber))
		if err := writeSheetJPEG(path, canvas); err != nil {
			return SheetResult{}, err
		}
		result.Sheets = append(result.Sheets, path)
		result.Tiles = append(result.Tiles, tiles...)
	}
	keepOutput = true
	return result, nil
}

func loadSheetInputs(ctx context.Context, paths Paths, opts SheetOptions) ([]sheetInput, error) {
	if strings.TrimSpace(opts.FilesFile) != "" {
		files, err := readIDs(opts.FilesFile)
		if err != nil {
			return nil, fmt.Errorf("read files file: %w", err)
		}
		inputs := make([]sheetInput, 0, len(files))
		for _, path := range files {
			inputs = append(inputs, sheetInput{file: path})
		}
		return inputs, nil
	}

	ids, err := readIDs(opts.IDsFile)
	if err != nil {
		return nil, fmt.Errorf("read ids file: %w", err)
	}
	db, err := openArchiveReadOnly(ctx, paths.Database)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	inputs := make([]sheetInput, 0, len(ids))
	for _, id := range ids {
		input, err := loadSheetAsset(ctx, db.DB(), id)
		if err != nil {
			return nil, err
		}
		inputs = append(inputs, input)
	}
	return inputs, nil
}

func loadSheetAsset(ctx context.Context, db *sql.DB, id string) (sheetInput, error) {
	input := sheetInput{id: id}
	err := db.QueryRowContext(ctx, `
select local_identifier
from asset
where id = ?
  and deleted_at is null
`, id).Scan(&input.localIdentifier)
	if errors.Is(err, sql.ErrNoRows) {
		return sheetInput{}, fmt.Errorf("asset not found: %s", id)
	}
	if err != nil {
		return sheetInput{}, fmt.Errorf("load sheet asset %s: %w", id, err)
	}
	rows, err := db.QueryContext(ctx, `
select resource_type, local_path
from asset_resource
where asset_id = ?
  and deleted_at is null
  and available_locally <> 0
  and trim(local_path) <> ''
`, id)
	if err != nil {
		return sheetInput{}, fmt.Errorf("load sheet resources for %s: %w", id, err)
	}
	defer rows.Close()
	for rows.Next() {
		var resourceType string
		var localPath string
		if err := rows.Scan(&resourceType, &localPath); err != nil {
			return sheetInput{}, err
		}
		source, priority := sheetResourceSource(resourceType, localPath)
		if priority == 0 {
			continue
		}
		input.resources = append(input.resources, sheetResource{
			path:     localPath,
			source:   source,
			priority: priority,
		})
	}
	if err := rows.Err(); err != nil {
		return sheetInput{}, err
	}
	sort.SliceStable(input.resources, func(i, j int) bool {
		if input.resources[i].priority != input.resources[j].priority {
			return input.resources[i].priority < input.resources[j].priority
		}
		return input.resources[i].path < input.resources[j].path
	})
	return input, nil
}

func sheetResourceSource(resourceType, path string) (string, int) {
	typ := strings.ToLower(strings.TrimSpace(resourceType))
	normalizedPath := "/" + strings.Trim(strings.ToLower(filepath.ToSlash(path)), "/") + "/"
	if strings.Contains(normalizedPath, "/resources/derivatives/") ||
		strings.Contains(normalizedPath, "/resources/renders/") ||
		strings.Contains(typ, "derivative") ||
		strings.Contains(typ, "render") ||
		strings.Contains(typ, "thumbnail") ||
		strings.Contains(typ, "preview") {
		return "derivative", 1
	}
	if strings.Contains(normalizedPath, "/originals/") ||
		typ == "original" ||
		typ == "photo" ||
		strings.Contains(typ, "original") {
		return "original", 2
	}
	return "", 0
}

func renderSheetPage(ctx context.Context, outputDir string, inputs []sheetInput, firstNumber, tileSize int, renderer CanonicalJPEGRenderer) (*image.RGBA, []SheetTile, error) {
	columns := min(maxSheetColumns, len(inputs))
	rows := (len(inputs) + columns - 1) / columns
	canvas := image.NewRGBA(image.Rect(0, 0, columns*tileSize, rows*tileSize))
	fillRect(canvas, canvas.Bounds(), color.RGBA{R: 26, G: 26, B: 26, A: 255})
	tiles := make([]SheetTile, 0, len(inputs))
	for index, input := range inputs {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		tileImage, source := renderSheetInput(ctx, outputDir, input, renderer)
		x := index % columns * tileSize
		y := index / columns * tileSize
		tileRect := image.Rect(x, y, x+tileSize, y+tileSize)
		drawFitted(canvas, tileRect, tileImage)
		number := firstNumber + index
		drawTileNumber(canvas, image.Pt(x, y), number, tileSize)
		tiles = append(tiles, SheetTile{
			N:               number,
			ID:              input.id,
			File:            input.file,
			LocalIdentifier: input.localIdentifier,
			Source:          source,
		})
	}
	return canvas, tiles, nil
}

func renderSheetInput(ctx context.Context, outputDir string, input sheetInput, renderer CanonicalJPEGRenderer) (image.Image, string) {
	if input.file != "" {
		decoded, err := decodeSheetImage(ctx, outputDir, input.file, renderer)
		if err == nil {
			return decoded, "original"
		}
		return placeholderImage(), "placeholder"
	}
	for _, resource := range input.resources {
		info, err := os.Stat(resource.path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		decoded, err := decodeSheetImage(ctx, outputDir, resource.path, renderer)
		if err == nil {
			return decoded, resource.source
		}
	}
	return placeholderImage(), "placeholder"
}

func decodeSheetImage(ctx context.Context, outputDir, sourcePath string, renderer CanonicalJPEGRenderer) (image.Image, error) {
	file, err := os.Open(sourcePath)
	if err != nil {
		return nil, err
	}
	decoded, format, decodeErr := image.Decode(file)
	closeErr := file.Close()
	if decodeErr == nil && (format == "jpeg" || format == "png") {
		return decoded, closeErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	tempDir, err := os.MkdirTemp(outputDir, ".render-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tempDir)
	renderedPath := filepath.Join(tempDir, "rendered.jpg")
	if err := renderer(ctx, sourcePath, renderedPath, 0.9); err != nil {
		return nil, err
	}
	if err := os.Chmod(renderedPath, 0o600); err != nil {
		return nil, err
	}
	rendered, err := os.Open(renderedPath)
	if err != nil {
		return nil, err
	}
	defer rendered.Close()
	decoded, format, err = image.Decode(rendered)
	if err != nil {
		return nil, err
	}
	if format != "jpeg" && format != "png" {
		return nil, fmt.Errorf("renderer returned unsupported image format %q", format)
	}
	return decoded, nil
}

func placeholderImage() image.Image {
	const size = 480
	placeholder := image.NewRGBA(image.Rect(0, 0, size, size))
	fillRect(placeholder, placeholder.Bounds(), color.RGBA{R: 92, G: 92, B: 92, A: 255})
	phrase := "NOT ON THIS MAC"
	scale := 5
	width := bitmapTextWidth(phrase, scale)
	drawBitmapText(placeholder, (size-width)/2, (size-7*scale)/2, phrase, scale, color.White, placeholderGlyphs)
	return placeholder
}

func drawFitted(destination *image.RGBA, bounds image.Rectangle, source image.Image) {
	sourceBounds := source.Bounds()
	if sourceBounds.Empty() {
		return
	}
	scale := math.Min(float64(bounds.Dx())/float64(sourceBounds.Dx()), float64(bounds.Dy())/float64(sourceBounds.Dy()))
	width := max(1, int(math.Round(float64(sourceBounds.Dx())*scale)))
	height := max(1, int(math.Round(float64(sourceBounds.Dy())*scale)))
	x := bounds.Min.X + (bounds.Dx()-width)/2
	y := bounds.Min.Y + (bounds.Dy()-height)/2
	resizeBilinear(destination, image.Rect(x, y, x+width, y+height), source)
}

func resizeBilinear(destination *image.RGBA, bounds image.Rectangle, source image.Image) {
	sourceBounds := source.Bounds()
	for y := 0; y < bounds.Dy(); y++ {
		sourceY := float64(sourceBounds.Min.Y) + (float64(y)+0.5)*float64(sourceBounds.Dy())/float64(bounds.Dy()) - 0.5
		y0 := clamp(int(math.Floor(sourceY)), sourceBounds.Min.Y, sourceBounds.Max.Y-1)
		y1 := clamp(y0+1, sourceBounds.Min.Y, sourceBounds.Max.Y-1)
		yWeight := sourceY - math.Floor(sourceY)
		for x := 0; x < bounds.Dx(); x++ {
			sourceX := float64(sourceBounds.Min.X) + (float64(x)+0.5)*float64(sourceBounds.Dx())/float64(bounds.Dx()) - 0.5
			x0 := clamp(int(math.Floor(sourceX)), sourceBounds.Min.X, sourceBounds.Max.X-1)
			x1 := clamp(x0+1, sourceBounds.Min.X, sourceBounds.Max.X-1)
			xWeight := sourceX - math.Floor(sourceX)
			destination.SetRGBA(bounds.Min.X+x, bounds.Min.Y+y, bilinearColor(source, x0, y0, x1, y1, xWeight, yWeight))
		}
	}
}

func bilinearColor(source image.Image, x0, y0, x1, y1 int, xWeight, yWeight float64) color.RGBA {
	r00, g00, b00, a00 := source.At(x0, y0).RGBA()
	r10, g10, b10, a10 := source.At(x1, y0).RGBA()
	r01, g01, b01, a01 := source.At(x0, y1).RGBA()
	r11, g11, b11, a11 := source.At(x1, y1).RGBA()
	channel := func(c00, c10, c01, c11 uint32) uint8 {
		top := float64(c00)*(1-xWeight) + float64(c10)*xWeight
		bottom := float64(c01)*(1-xWeight) + float64(c11)*xWeight
		return uint8(math.Round((top*(1-yWeight) + bottom*yWeight) / 257))
	}
	return color.RGBA{
		R: channel(r00, r10, r01, r11),
		G: channel(g00, g10, g01, g11),
		B: channel(b00, b10, b01, b11),
		A: channel(a00, a10, a01, a11),
	}
}

func drawTileNumber(destination *image.RGBA, origin image.Point, number, tileSize int) {
	text := fmt.Sprintf("%d", number)
	scale := max(1, tileSize/60)
	padding := max(3, scale)
	width := bitmapTextWidth(text, scale)
	box := image.Rect(origin.X, origin.Y, origin.X+width+2*padding, origin.Y+7*scale+2*padding)
	fillRect(destination, box, color.RGBA{R: 18, G: 18, B: 18, A: 255})
	drawBitmapText(destination, origin.X+padding, origin.Y+padding, text, scale, color.White, digitGlyphs)
}

func drawBitmapText(destination *image.RGBA, x, y int, text string, scale int, ink color.Color, glyphs map[rune][7]byte) {
	for _, character := range text {
		glyph, ok := glyphs[character]
		if !ok {
			x += 6 * scale
			continue
		}
		for row, bits := range glyph {
			for column := 0; column < 5; column++ {
				if bits&(1<<uint(4-column)) == 0 {
					continue
				}
				fillRect(destination, image.Rect(x+column*scale, y+row*scale, x+(column+1)*scale, y+(row+1)*scale), ink)
			}
		}
		x += 6 * scale
	}
}

func bitmapTextWidth(text string, scale int) int {
	characters := len([]rune(text))
	if characters == 0 {
		return 0
	}
	return (characters*6 - 1) * scale
}

func fillRect(destination *image.RGBA, bounds image.Rectangle, fill color.Color) {
	bounds = bounds.Intersect(destination.Bounds())
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			destination.Set(x, y, fill)
		}
	}
}

func writeSheetJPEG(path string, source image.Image) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create sheet image: %w", err)
	}
	encodeErr := jpeg.Encode(file, source, &jpeg.Options{Quality: 88})
	closeErr := file.Close()
	if encodeErr != nil {
		return fmt.Errorf("encode sheet image: %w", encodeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close sheet image: %w", closeErr)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("secure sheet image: %w", err)
	}
	return nil
}

func clamp(value, low, high int) int {
	return min(max(value, low), high)
}

var digitGlyphs = map[rune][7]byte{
	'0': {0b01110, 0b10001, 0b10011, 0b10101, 0b11001, 0b10001, 0b01110},
	'1': {0b00100, 0b01100, 0b00100, 0b00100, 0b00100, 0b00100, 0b01110},
	'2': {0b01110, 0b10001, 0b00001, 0b00010, 0b00100, 0b01000, 0b11111},
	'3': {0b11110, 0b00001, 0b00001, 0b01110, 0b00001, 0b00001, 0b11110},
	'4': {0b00010, 0b00110, 0b01010, 0b10010, 0b11111, 0b00010, 0b00010},
	'5': {0b11111, 0b10000, 0b10000, 0b11110, 0b00001, 0b00001, 0b11110},
	'6': {0b01110, 0b10000, 0b10000, 0b11110, 0b10001, 0b10001, 0b01110},
	'7': {0b11111, 0b00001, 0b00010, 0b00100, 0b01000, 0b01000, 0b01000},
	'8': {0b01110, 0b10001, 0b10001, 0b01110, 0b10001, 0b10001, 0b01110},
	'9': {0b01110, 0b10001, 0b10001, 0b01111, 0b00001, 0b00001, 0b01110},
}

var placeholderGlyphs = map[rune][7]byte{
	' ': {},
	'A': {0b01110, 0b10001, 0b10001, 0b11111, 0b10001, 0b10001, 0b10001},
	'C': {0b01110, 0b10001, 0b10000, 0b10000, 0b10000, 0b10001, 0b01110},
	'H': {0b10001, 0b10001, 0b10001, 0b11111, 0b10001, 0b10001, 0b10001},
	'I': {0b01110, 0b00100, 0b00100, 0b00100, 0b00100, 0b00100, 0b01110},
	'M': {0b10001, 0b11011, 0b10101, 0b10101, 0b10001, 0b10001, 0b10001},
	'N': {0b10001, 0b11001, 0b11001, 0b10101, 0b10011, 0b10011, 0b10001},
	'O': {0b01110, 0b10001, 0b10001, 0b10001, 0b10001, 0b10001, 0b01110},
	'S': {0b01111, 0b10000, 0b10000, 0b01110, 0b00001, 0b00001, 0b11110},
	'T': {0b11111, 0b00100, 0b00100, 0b00100, 0b00100, 0b00100, 0b00100},
}
