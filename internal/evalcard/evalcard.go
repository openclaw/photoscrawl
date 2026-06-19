package evalcard

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/openclaw/photoscrawl/internal/photos"
	repoPrompts "github.com/openclaw/photoscrawl/prompts"
)

const (
	DefaultOllamaGenerateURL = "http://127.0.0.1:11434/api/generate"
)

const PromptVersion = repoPrompts.PhotoCardV1Version

type Options struct {
	LibraryPath          string
	OutputDir            string
	CacheDir             string
	DefaultOutputRoot    string
	DefaultCacheDir      string
	PromptPath           string
	Models               []string
	OllamaGenerateURL    string
	OllamaAPIKey         string
	Limit                int
	Concurrency          int
	Sample               string
	Seed                 uint64
	AllowICloudDownloads bool
	Provider             photos.Provider
	Now                  func() time.Time
}

type Result struct {
	OutputDir             string         `json:"output_dir"`
	CacheDir              string         `json:"cache_dir"`
	PromptPath            string         `json:"prompt_path"`
	PromptSHA256          string         `json:"prompt_sha256"`
	PromptVersion         string         `json:"prompt_version"`
	Models                []string       `json:"models,omitempty"`
	Limit                 int            `json:"limit"`
	Sample                string         `json:"sample"`
	AllowICloudDownloads  bool           `json:"allow_icloud_downloads"`
	AssetsSeen            int            `json:"assets_seen"`
	AssetsPrepared        int            `json:"assets_prepared"`
	AssetsSkipped         map[string]int `json:"assets_skipped,omitempty"`
	ModelCallsAttempted   int            `json:"model_calls_attempted"`
	ModelCallsSucceeded   int            `json:"model_calls_succeeded"`
	ModelCallsFailed      int            `json:"model_calls_failed"`
	ManifestPath          string         `json:"manifest_path"`
	SummaryPath           string         `json:"summary_path"`
	OllamaGenerateURLUsed string         `json:"ollama_generate_url_used,omitempty"`
}

type preparedInput struct {
	ID           string
	ImagePath    string
	MetadataPath string
	MetadataJSON []byte
}

type metadataPack struct {
	EvalID        string         `json:"eval_id"`
	PromptVersion string         `json:"prompt_version"`
	Asset         photos.Asset   `json:"asset"`
	Original      originalSource `json:"original"`
	ImageIO       map[string]any `json:"imageio"`
}

type originalSource struct {
	Source string `json:"source"`
	Path   string `json:"path"`
}

type manifestRow struct {
	EvalID       string `json:"eval_id"`
	ImagePath    string `json:"image_path"`
	MetadataPath string `json:"metadata_path"`
	OriginalPath string `json:"original_path"`
	OriginalMode string `json:"original_mode"`
}

func Run(ctx context.Context, opts Options) (Result, error) {
	if opts.Provider == nil {
		return Result{}, errors.New("photos provider is required")
	}
	if strings.TrimSpace(opts.LibraryPath) == "" {
		return Result{}, errors.New("library path is required")
	}
	now := opts.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 15
	}
	concurrency := opts.Concurrency
	if concurrency <= 0 {
		concurrency = 4
	}
	sample := strings.TrimSpace(opts.Sample)
	if sample == "" {
		sample = "latest"
	}
	if sample != "latest" && sample != "random" {
		return Result{}, fmt.Errorf("unsupported sample %q", sample)
	}

	outputDir, err := defaultedOutputDir(opts.OutputDir, opts.DefaultOutputRoot, now)
	if err != nil {
		return Result{}, err
	}
	cacheDir, err := defaultedCacheDir(opts.CacheDir, opts.DefaultCacheDir)
	if err != nil {
		return Result{}, err
	}
	if err := rejectRepoPath(outputDir); err != nil {
		return Result{}, fmt.Errorf("output dir: %w", err)
	}
	if err := rejectRepoPath(cacheDir); err != nil {
		return Result{}, fmt.Errorf("cache dir: %w", err)
	}
	for _, dir := range []string{
		outputDir,
		filepath.Join(outputDir, "images"),
		filepath.Join(outputDir, "metadata"),
		filepath.Join(outputDir, "raw"),
		cacheDir,
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return Result{}, err
		}
	}

	promptPath := strings.TrimSpace(opts.PromptPath)
	if promptPath == "" {
		promptPath = repoPrompts.DefaultPhotoCardV1Path
	}
	promptBytes, err := os.ReadFile(promptPath)
	if err != nil {
		return Result{}, fmt.Errorf("read prompt: %w", err)
	}
	promptSum := sha256.Sum256(promptBytes)

	libraryPath, err := filepath.Abs(opts.LibraryPath)
	if err != nil {
		return Result{}, err
	}
	snapshot, err := opts.Provider.Snapshot(ctx, libraryPath)
	if err != nil {
		return Result{}, err
	}
	if err := photos.AttachLocalMediaPaths(&snapshot, libraryPath); err != nil {
		return Result{}, fmt.Errorf("resolve local media: %w", err)
	}
	localMedia, err := photos.BuildLocalMediaIndex(libraryPath)
	if err != nil {
		return Result{}, fmt.Errorf("index local media: %w", err)
	}

	result := Result{
		OutputDir:             outputDir,
		CacheDir:              cacheDir,
		PromptPath:            promptPath,
		PromptSHA256:          hex.EncodeToString(promptSum[:]),
		PromptVersion:         PromptVersion,
		Models:                append([]string(nil), opts.Models...),
		Limit:                 limit,
		Sample:                sample,
		AllowICloudDownloads:  opts.AllowICloudDownloads,
		AssetsSkipped:         map[string]int{},
		ManifestPath:          filepath.Join(outputDir, "manifest.jsonl"),
		SummaryPath:           filepath.Join(outputDir, "summary.json"),
		OllamaGenerateURLUsed: normalizeOllamaGenerateURL(opts.OllamaGenerateURL),
	}

	assets := imageAssets(snapshot.Assets, sample, opts.Seed)
	result.AssetsSeen = len(assets)
	manifest, err := os.Create(result.ManifestPath)
	if err != nil {
		return Result{}, err
	}
	defer manifest.Close()
	writer := bufio.NewWriter(manifest)
	defer writer.Flush()

	inputs := []preparedInput{}
	for _, asset := range assets {
		if len(inputs) >= limit {
			break
		}
		id := fmt.Sprintf("E%03d", len(inputs)+1)
		input, row, err := prepareInput(ctx, localMedia, outputDir, cacheDir, id, asset, opts.AllowICloudDownloads)
		if err != nil {
			result.AssetsSkipped[classifySkip(err)]++
			continue
		}
		rowJSON, err := json.Marshal(row)
		if err != nil {
			return Result{}, err
		}
		if _, err := writer.Write(append(rowJSON, '\n')); err != nil {
			return Result{}, err
		}
		inputs = append(inputs, input)
	}
	result.AssetsPrepared = len(inputs)

	if len(opts.Models) > 0 && len(inputs) > 0 {
		succeeded, failed := runModelCalls(ctx, outputDir, string(promptBytes), inputs, opts.Models, result.OllamaGenerateURLUsed, opts.OllamaAPIKey, concurrency)
		result.ModelCallsAttempted = len(inputs) * len(opts.Models)
		result.ModelCallsSucceeded = succeeded
		result.ModelCallsFailed = failed
	}

	summary, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return Result{}, err
	}
	if err := os.WriteFile(result.SummaryPath, append(summary, '\n'), 0o644); err != nil {
		return Result{}, err
	}
	return result, nil
}

func prepareInput(ctx context.Context, localMedia photos.LocalMediaIndex, outputDir, cacheDir, id string, asset photos.Asset, allowICloud bool) (preparedInput, manifestRow, error) {
	originalPath, source, err := resolveOriginal(ctx, localMedia, cacheDir, asset, allowICloud)
	if err != nil {
		return preparedInput{}, manifestRow{}, err
	}
	imagePath := filepath.Join(outputDir, "images", id+".jpg")
	if err := photos.RenderCanonicalJPEG(ctx, originalPath, imagePath, 0.92); err != nil {
		return preparedInput{}, manifestRow{}, fmt.Errorf("canonical_render")
	}
	imageMeta, err := photos.ImageMetadata(ctx, originalPath)
	if err != nil {
		return preparedInput{}, manifestRow{}, fmt.Errorf("image_metadata")
	}
	pack := metadataPack{
		EvalID:        id,
		PromptVersion: PromptVersion,
		Asset:         asset,
		Original: originalSource{
			Source: source,
			Path:   originalPath,
		},
		ImageIO: imageMeta,
	}
	metadataJSON, err := json.MarshalIndent(pack, "", "  ")
	if err != nil {
		return preparedInput{}, manifestRow{}, err
	}
	metadataPath := filepath.Join(outputDir, "metadata", id+".json")
	if err := os.WriteFile(metadataPath, append(metadataJSON, '\n'), 0o600); err != nil {
		return preparedInput{}, manifestRow{}, err
	}
	return preparedInput{
			ID:           id,
			ImagePath:    imagePath,
			MetadataPath: metadataPath,
			MetadataJSON: metadataJSON,
		}, manifestRow{
			EvalID:       id,
			ImagePath:    imagePath,
			MetadataPath: metadataPath,
			OriginalPath: originalPath,
			OriginalMode: source,
		}, nil
}

func resolveOriginal(ctx context.Context, localMedia photos.LocalMediaIndex, cacheDir string, asset photos.Asset, allowICloud bool) (string, string, error) {
	candidates := localMedia.Candidates(asset.LocalIdentifier)
	for _, candidate := range candidates {
		if candidate.Class == "original" {
			return candidate.Path, "photos_package_original", nil
		}
	}
	cachePath := filepath.Join(cacheDir, cacheName(asset))
	if info, err := os.Stat(cachePath); err == nil && info.Size() > 0 {
		return cachePath, "cached_photokit_original", nil
	}
	if !allowICloud {
		return "", "", fmt.Errorf("missing_original")
	}
	if err := photos.ExportOriginalResource(ctx, asset.LocalIdentifier, cachePath, true); err != nil {
		return "", "", fmt.Errorf("export_original")
	}
	return cachePath, "photokit_original_export", nil
}

func imageAssets(assets []photos.Asset, sample string, seed uint64) []photos.Asset {
	out := []photos.Asset{}
	for _, asset := range assets {
		if asset.MediaType == "image" {
			out = append(out, asset)
		}
	}
	if sample == "random" {
		rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
		rng.Shuffle(len(out), func(i, j int) {
			out[i], out[j] = out[j], out[i]
		})
		return out
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].CreationDate != out[j].CreationDate {
			return out[i].CreationDate > out[j].CreationDate
		}
		return out[i].LocalIdentifier < out[j].LocalIdentifier
	})
	return out
}

func cacheName(asset photos.Asset) string {
	sum := sha256.Sum256([]byte(asset.LocalIdentifier))
	return hex.EncodeToString(sum[:]) + originalExtension(asset)
}

func originalExtension(asset photos.Asset) string {
	for _, resource := range asset.Resources {
		if ext := strings.ToLower(filepath.Ext(resource.OriginalFilename)); classifiableExtension(ext) {
			return ext
		}
		if ext := extensionForUTI(resource.UTI); ext != "" {
			return ext
		}
	}
	return ".heic"
}

func classifiableExtension(ext string) bool {
	switch ext {
	case ".heic", ".heif", ".jpg", ".jpeg", ".png", ".tif", ".tiff", ".dng":
		return true
	default:
		return false
	}
}

func extensionForUTI(uti string) string {
	switch strings.ToLower(uti) {
	case "public.heic", "public.heif":
		return ".heic"
	case "public.jpeg", "public.jpg":
		return ".jpg"
	case "public.png":
		return ".png"
	case "public.tiff":
		return ".tiff"
	case "com.adobe.raw-image", "com.adobe.raw":
		return ".dng"
	default:
		return ""
	}
}

func classifySkip(err error) string {
	value := strings.TrimSpace(err.Error())
	if value == "" {
		return "unknown"
	}
	return value
}
