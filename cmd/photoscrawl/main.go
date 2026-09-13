package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/openclaw/crawlkit/output"
	"github.com/openclaw/photoscrawl/internal/archive"
	"github.com/openclaw/photoscrawl/internal/evalcard"
	"github.com/openclaw/photoscrawl/internal/photos"
	"github.com/openclaw/photoscrawl/internal/place"
)

var version = "dev"

func commandContext() (context.Context, context.CancelFunc) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	go func() {
		<-ctx.Done()
		// A second signal must still terminate commands blocked in native calls or stdin.
		stop()
	}()
	return ctx, stop
}

func main() {
	ctx, stop := commandContext()
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		if output.IsUsage(err) {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return usage()
	}
	if len(args) == 1 && (args[0] == "--version" || args[0] == "-v") {
		return writeVersion(os.Stdout)
	}
	paths, err := archive.DefaultPaths()
	if err != nil {
		return err
	}
	switch args[0] {
	case "people":
		fs := newCommandFlags("people", &paths)
		format, err := fs.parse(args[1:], false)
		if err != nil {
			return err
		}
		result, err := archive.People(ctx, paths)
		if err != nil {
			return err
		}
		return output.Write(os.Stdout, format, "people", result)
	case "find":
		fs := newCommandFlags("find", &paths)
		opts := findFlags(fs)
		format, err := fs.parse(args[1:], false)
		if err != nil {
			return err
		}
		result, err := archive.Find(ctx, paths, *opts)
		if err != nil {
			return err
		}
		return output.Write(os.Stdout, format, "find", result)
	case "rank":
		fs := newCommandFlags("rank", &paths)
		find := findFlags(fs)
		ids := fs.String("ids-file", "", "JSON array or line-separated asset ids")
		group := fs.String("group", "none", "group: none, burst, time, or day")
		gap := fs.Int("gap-seconds", 90, "split time groups after this gap")
		per := fs.Int("per-group", 0, "top assets per group; 0 keeps all")
		format, err := fs.parse(args[1:], false)
		if err != nil {
			return err
		}
		if *ids != "" && hasFindFilters(*find) {
			return output.UsageError{Err: errors.New("--ids-file cannot be combined with find filters")}
		}
		result, err := archive.Rank(ctx, paths, archive.RankOptions{FindOptions: *find, IDsFile: *ids, Group: *group, GapSeconds: *gap, PerGroup: *per})
		if err != nil {
			return err
		}
		return output.Write(os.Stdout, format, "rank", result)
	case "junk":
		fs := newCommandFlags("junk", &paths)
		kind := fs.String("kind", "all", "screenshots, blurry, eyes-closed, duplicates, or all")
		older := fs.String("older-than", "30d", "age for screenshots, such as 30d or 12h")
		limit := fs.Int("limit", 200, "max candidates")
		blurMax := fs.Float64("blur-max", 0.3, "highest Photos blurriness score flagged as blurry (1 is sharp)")
		exclude := fs.String("exclude-ids-file", "", "JSON array or line-separated ids to exclude")
		format, err := fs.parse(args[1:], false)
		if err != nil {
			return err
		}
		result, err := archive.Junk(ctx, paths, archive.JunkOptions{Kind: *kind, OlderThan: *older, Limit: *limit, ExcludeIDsFile: *exclude, BlurMax: *blurMax})
		if err != nil {
			return err
		}
		return output.Write(os.Stdout, format, "junk", result)
	case "sheet":
		fs := newCommandFlags("sheet", &paths)
		ids := fs.String("ids-file", "", "JSON array or line-separated asset ids")
		files := fs.String("files-file", "", "JSON array or line-separated image paths")
		title := fs.String("title", "", "title included in JSON output")
		perSheet := fs.Int("per-sheet", 16, "tiles per contact sheet")
		tile := fs.Int("tile", 480, "tile size in pixels")
		format, err := fs.parse(args[1:], false)
		if err != nil {
			return err
		}
		result, err := archive.Sheet(ctx, paths, archive.SheetOptions{IDsFile: *ids, FilesFile: *files, Title: *title, PerSheet: *perSheet, Tile: *tile})
		if err != nil {
			return err
		}
		return output.Write(os.Stdout, format, "sheet", result)
	case "similar":
		fs := newCommandFlags("similar", &paths)
		id := fs.String("id", "", "seed asset id; finds label similarity, not pixel similarity")
		limit := fs.Int("limit", 30, "max results")
		includeSameEvent := fs.Bool("include-same-event", false, "include photos from the seed's day, 12-hour window, and burst")
		exclude := fs.String("exclude-ids-file", "", "JSON array or line-separated ids to exclude")
		format, err := fs.parse(args[1:], false)
		if err != nil {
			return err
		}
		result, err := archive.Similar(ctx, paths, archive.SimilarOptions{ID: *id, Limit: *limit, IncludeSameEvent: *includeSameEvent, ExcludeIDsFile: *exclude})
		if err != nil {
			return err
		}
		return output.Write(os.Stdout, format, "similar", result)
	case "forgotten":
		fs := newCommandFlags("forgotten", &paths)
		from := fs.String("from", "", "inclusive RFC 3339 timestamp or YYYY-MM-DD")
		to := fs.String("to", "", "inclusive RFC 3339 timestamp or YYYY-MM-DD")
		limit := fs.Int("limit", 12, "max results")
		gapHours := fs.Float64("gap-hours", 3, "split moments after this many hours")
		exclude := fs.String("exclude-ids-file", "", "JSON array or line-separated ids to exclude")
		format, err := fs.parse(args[1:], false)
		if err != nil {
			return err
		}
		result, err := archive.Forgotten(ctx, paths, archive.ForgottenOptions{From: *from, To: *to, Limit: *limit, GapHours: *gapHours, ExcludeIDsFile: *exclude})
		if err != nil {
			return err
		}
		return output.Write(os.Stdout, format, "forgotten", result)
	case "share-check":
		fs := newCommandFlags("share-check", &paths)
		ids := fs.String("ids-file", "", "JSON array or line-separated asset ids")
		exclude := fs.String("exclude-ids-file", "", "JSON array or line-separated ids to exclude")
		format, err := fs.parse(args[1:], false)
		if err != nil {
			return err
		}
		result, err := archive.ShareCheck(ctx, paths, archive.ShareCheckOptions{IDsFile: *ids, ExcludeIDsFile: *exclude})
		if err != nil {
			return err
		}
		return output.Write(os.Stdout, format, "share_check", result)
	case "version":
		if len(args) != 1 {
			return output.UsageError{Err: errors.New("version takes no arguments")}
		}
		return writeVersion(os.Stdout)
	case "metadata":
		fs := newCommandFlags("metadata", nil)
		format, err := fs.parse(args[1:], true)
		if err != nil {
			return err
		}
		return output.Write(os.Stdout, format, "metadata", archive.ControlManifest(paths))
	case "init":
		fs := newCommandFlags("init", &paths)
		format, err := fs.parse(args[1:], false)
		if err != nil {
			return err
		}
		result, err := archive.Init(ctx, paths)
		if err != nil {
			return err
		}
		return output.Write(os.Stdout, format, "init", result)
	case "status":
		fs := newCommandFlags("status", &paths)
		format, err := fs.parse(args[1:], false)
		if err != nil {
			return err
		}
		status, err := archive.Status(ctx, paths)
		if err != nil {
			return err
		}
		return output.Write(os.Stdout, format, "status", status)
	case "crawl":
		fs := newCommandFlags("crawl", &paths)
		libraryPath := fs.String("library", "", "Photos Library.photoslibrary path")
		providerName := fs.String("provider", "auto", "photos provider: auto or sqlite")
		format, err := fs.parse(args[1:], false)
		if err != nil {
			return err
		}
		provider, err := photos.ProviderByName(*providerName)
		if err != nil {
			return output.UsageError{Err: err}
		}
		result, err := archive.Crawl(ctx, paths, archive.CrawlOptions{
			LibraryPath: *libraryPath,
			Provider:    provider,
		})
		if err != nil {
			return err
		}
		return output.Write(os.Stdout, format, "crawl", result)
	case "import-apple":
		fs := newCommandFlags("import-apple", &paths)
		libraryPath := fs.String("library", "", "Photos Library.photoslibrary path")
		format, err := fs.parse(args[1:], false)
		if err != nil {
			return err
		}
		result, err := archive.ImportApple(ctx, paths, archive.ImportAppleOptions{LibraryPath: *libraryPath})
		if err != nil {
			return err
		}
		return output.Write(os.Stdout, format, "import_apple", result)
	case "classify":
		fs := newCommandFlags("classify", &paths)
		all := fs.Bool("all", false, "classify all pending assets")
		limit := fs.Int("limit", 100, "max pending assets to classify")
		localModel := fs.String("local-model", "", "local vision model to use for content observations")
		localModelAPI := fs.String("local-model-api", "", "local model API: ollama or openai")
		localModelURL := fs.String("local-model-url", "", "local model endpoint URL")
		allowICloud := fs.Bool("allow-icloud-downloads", false, "download bounded PhotoKit previews when local image content is missing")
		format, err := fs.parse(args[1:], false)
		if err != nil {
			return err
		}
		result, err := archive.Classify(ctx, paths, archive.ClassifyOptions{
			All:                  *all,
			Limit:                *limit,
			LocalModel:           *localModel,
			LocalModelAPI:        *localModelAPI,
			LocalModelURL:        *localModelURL,
			AllowICloudDownloads: *allowICloud,
		})
		if err != nil {
			return err
		}
		return output.Write(os.Stdout, format, "classify", result)
	case "search":
		fs := newCommandFlags("search", &paths)
		query := fs.String("query", "", "search query")
		limit := fs.Int("limit", 20, "max results")
		format, err := fs.parse(args[1:], false)
		if err != nil {
			return err
		}
		result, err := archive.Search(ctx, paths, archive.SearchOptions{Query: joinedQuery(*query, fs.Args()), Limit: *limit})
		if err != nil {
			return err
		}
		return output.Write(os.Stdout, format, "search", result)
	case "timeline":
		fs := newCommandFlags("timeline", &paths)
		from := fs.String("from", "", "inclusive ISO 8601 timestamp with offset")
		to := fs.String("to", "", "exclusive ISO 8601 timestamp with offset")
		includeUnlocated := fs.Bool("include-unlocated", false, "include assets without embedded coordinates")
		format, err := fs.parse(args[1:], true)
		if err != nil {
			return err
		}
		result, err := archive.Timeline(ctx, paths, archive.TimelineOptions{From: *from, To: *to, IncludeUnlocated: *includeUnlocated})
		if err != nil {
			return err
		}
		return output.Write(os.Stdout, format, "timeline", result)
	case "open":
		fs := newCommandFlags("open", &paths)
		id := fs.String("id", "", "asset id")
		format, err := fs.parse(args[1:], false)
		if err != nil {
			return err
		}
		result, err := archive.Open(ctx, paths, *id)
		if err != nil {
			return err
		}
		return output.Write(os.Stdout, format, "open", result)
	case "export":
		fs := newCommandFlags("export", &paths)
		id := fs.String("id", "", "asset id")
		outputDir := fs.String("output", "", "destination directory")
		timeout := fs.Duration("timeout", 0, "export timeout (for example 2m); 0 waits until completion or cancellation")
		format, err := fs.parse(args[1:], false)
		if err != nil {
			return err
		}
		if *timeout < 0 {
			return output.UsageError{Err: errors.New("timeout must not be negative")}
		}
		if *timeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, *timeout)
			defer cancel()
		}
		result, err := archive.Export(ctx, paths, *id, *outputDir)
		if err != nil {
			return err
		}
		return output.Write(os.Stdout, format, "export", result)
	case "evidence":
		fs := newCommandFlags("evidence", &paths)
		rowID := fs.String("row-id", "", "row id")
		format, err := fs.parse(args[1:], false)
		if err != nil {
			return err
		}
		result, err := archive.Evidence(ctx, paths, *rowID)
		if err != nil {
			return err
		}
		return output.Write(os.Stdout, format, "evidence", result)
	case "neighbors":
		fs := newCommandFlags("neighbors", &paths)
		id := fs.String("id", "", "asset id")
		limit := fs.Int("limit", 20, "max results")
		format, err := fs.parse(args[1:], false)
		if err != nil {
			return err
		}
		result, err := archive.Neighbors(ctx, paths, archive.NeighborOptions{ID: *id, Limit: *limit})
		if err != nil {
			return err
		}
		return output.Write(os.Stdout, format, "neighbors", result)
	case "place-context":
		fs := newCommandFlags("place-context", nil)
		inputPath := fs.String("input", "-", "JSON place input path, or stdin")
		radius := fs.Float64("radius", 150, "nearby POI search radius in meters")
		format, err := fs.parse(args[1:], false)
		if err != nil {
			return err
		}
		result, err := place.Run(ctx, place.Options{
			InputPath:    *inputPath,
			RadiusMeters: *radius,
			CacheDir:     paths.PlaceContextCacheDir(),
		})
		if err != nil {
			return err
		}
		return output.Write(os.Stdout, format, "place_context", result)
	case place.RawContextCommand:
		// This is the place-backfill subprocess bridge.
		fs := flag.NewFlagSet(place.RawContextCommand, flag.ContinueOnError)
		fs.SetOutput(os.Stderr)
		inputPath := fs.String("input", "-", "JSON place input path, or stdin")
		radius := fs.Float64("radius", 150, "nearby POI search radius in meters")
		if err := fs.Parse(args[1:]); err != nil {
			return output.UsageError{Err: err}
		}
		result, err := place.RunRaw(ctx, place.RawOptions{
			InputPath:    *inputPath,
			RadiusMeters: *radius,
		})
		if err != nil {
			return err
		}
		return output.Write(os.Stdout, output.JSON, "place_context_raw", result)
	case "place-card":
		fs := flag.NewFlagSet("place-card", flag.ContinueOnError)
		fs.SetOutput(os.Stderr)
		inputPath := fs.String("input", "-", "JSON place-context result path, or stdin")
		if err := fs.Parse(args[1:]); err != nil {
			return output.UsageError{Err: err}
		}
		result, err := place.LoadResult(*inputPath)
		if err != nil {
			return err
		}
		fmt.Fprintln(os.Stdout, place.RenderCard(result))
		return nil
	case "place-backfill":
		fs := newCommandFlags("place-backfill", &paths)
		outDir := fs.String("out", "", "private place backfill output directory")
		format, err := fs.parse(args[1:], false)
		if err != nil {
			return err
		}
		if *outDir == "" {
			*outDir = paths.PlaceBackfillDir()
		}
		result, err := place.Backfill(ctx, place.BackfillOptions{
			DatabasePath: paths.Database,
			OutputDir:    *outDir,
		})
		if err != nil {
			return err
		}
		return output.Write(os.Stdout, format, "place_backfill", result)
	case "eval-card":
		fs := newCommandFlags("eval-card", nil)
		libraryPath := fs.String("library", "", "Photos Library.photoslibrary path")
		outDir := fs.String("out", "", "private eval output directory")
		cacheDir := fs.String("cache-dir", "", "private original cache directory")
		promptPath := fs.String("prompt", "", "photo-card prompt file")
		models := fs.String("models", "", "comma-separated Ollama models")
		ollamaURL := fs.String("ollama-url", "", "Ollama generate URL or base URL")
		allowICloud := fs.Bool("allow-icloud-downloads", false, "allow PhotoKit to download missing originals")
		limit := fs.Int("limit", 15, "max images to prepare")
		concurrency := fs.Int("concurrency", 4, "max concurrent model calls")
		sample := fs.String("sample", "latest", "sample mode: latest or random")
		seed := fs.Uint64("seed", 1, "random sample seed")
		format, err := fs.parse(args[1:], false)
		if err != nil {
			return err
		}
		result, err := evalcard.Run(ctx, evalcard.Options{
			LibraryPath:          *libraryPath,
			OutputDir:            *outDir,
			CacheDir:             *cacheDir,
			DefaultOutputRoot:    paths.EvalRootDir(),
			DefaultCacheDir:      paths.OriginalsCacheDir(),
			PromptPath:           *promptPath,
			Models:               splitList(*models),
			OllamaGenerateURL:    *ollamaURL,
			OllamaAPIKey:         os.Getenv("OLLAMA_API_KEY"),
			Limit:                *limit,
			Concurrency:          *concurrency,
			Sample:               *sample,
			Seed:                 *seed,
			AllowICloudDownloads: *allowICloud,
			Provider:             photos.NewProvider(),
		})
		if err != nil {
			return err
		}
		return output.Write(os.Stdout, format, "eval_card", result)
	default:
		return usage()
	}
}

func usage() error {
	return output.UsageError{Err: errors.New("usage: photoscrawl [--version] <version|metadata|init|status|crawl|import-apple|classify|search|people|find|rank|junk|sheet|similar|forgotten|share-check|timeline|open|export|neighbors|evidence|place-context|place-context-raw|place-card|place-backfill|eval-card>")}
}

func findFlags(fs *commandFlags) *archive.FindOptions {
	o := &archive.FindOptions{}
	fs.Func("person", "required named person (repeatable)", func(v string) error { o.People = append(o.People, v); return nil })
	fs.StringVar(&o.From, "from", "", "inclusive RFC 3339 timestamp or YYYY-MM-DD")
	fs.StringVar(&o.To, "to", "", "inclusive RFC 3339 timestamp or YYYY-MM-DD")
	fs.StringVar(&o.Place, "place", "", "place or venue text")
	fs.StringVar(&o.Query, "query", "", "existing full-text search query")
	fs.StringVar(&o.Media, "media", "image", "image, video, or any")
	fs.BoolVar(&o.IncludeHidden, "include-hidden", false, "include hidden assets")
	fs.StringVar(&o.Rank, "rank", "date", "quality or date")
	fs.IntVar(&o.Limit, "limit", 50, "max assets")
	fs.StringVar(&o.ExcludeIDsFile, "exclude-ids-file", "", "JSON array or line-separated ids to exclude")
	return o
}

func hasFindFilters(o archive.FindOptions) bool {
	return len(o.People) > 0 || o.From != "" || o.To != "" || o.Place != "" || o.Query != "" || o.Media != "image" || o.IncludeHidden || o.Rank != "date"
}

func writeVersion(w io.Writer) error {
	_, err := fmt.Fprintln(w, version)
	return err
}

func splitList(value string) []string {
	out := []string{}
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func joinedQuery(flagValue string, args []string) string {
	parts := append([]string{strings.TrimSpace(flagValue)}, args...)
	return strings.TrimSpace(strings.Join(parts, " "))
}
