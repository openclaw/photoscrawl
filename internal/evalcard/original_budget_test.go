package evalcard

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/openclaw/photoscrawl/internal/photos"
)

type budgetProvider struct{ assets []photos.Asset }

func (p budgetProvider) Snapshot(context.Context, string) (photos.LibrarySnapshot, error) {
	return photos.LibrarySnapshot{Assets: p.assets}, nil
}

func TestEvalAttemptsAndOriginalCleanupAreBounded(t *testing.T) {
	oldExport, oldRender := exportEvalOriginal, renderEvalImage
	t.Cleanup(func() { exportEvalOriginal, renderEvalImage = oldExport, oldRender })
	calls := 0
	exportEvalOriginal = func(ctx context.Context, id, path string, network bool, limit int64) error {
		calls++
		if !network || limit <= 0 || limit > maxOriginalBytes {
			t.Fatalf("export allowance = %d, network=%v", limit, network)
		}
		return os.WriteFile(path, []byte("synthetic original"), 0o600)
	}
	renderEvalImage = func(context.Context, string, string, float64) error {
		return errors.New("synthetic render failure")
	}
	opts, _ := evalRunOptions(t)
	assets := make([]photos.Asset, 100)
	for i := range assets {
		assets[i] = photos.Asset{LocalIdentifier: "synthetic", MediaType: "image"}
	}
	opts.Provider = budgetProvider{assets}
	opts.Limit = 2
	opts.AllowICloudDownloads = true
	for run := 0; run < 2; run++ {
		calls = 0
		result, err := Run(context.Background(), opts)
		if err != nil {
			t.Fatal(err)
		}
		if result.AssetsAttempted != 6 || calls != 6 || result.AssetsPrepared != 0 {
			t.Fatalf("attempted=%d exported=%d prepared=%d", result.AssetsAttempted, calls, result.AssetsPrepared)
		}
		entries, err := os.ReadDir(opts.CacheDir)
		if err != nil || len(entries) != 0 {
			t.Fatalf("failed originals retained: %d, %v", len(entries), err)
		}
	}
}

func TestOriginalBudgetRejectsOversizeAndStopsFailedExports(t *testing.T) {
	old := exportEvalOriginal
	t.Cleanup(func() { exportEvalOriginal = old })
	calls := 0
	exportEvalOriginal = func(ctx context.Context, id, path string, network bool, limit int64) error {
		calls++
		return os.WriteFile(path, make([]byte, limit+1), 0o600)
	}
	cache := t.TempDir()
	budget := originalBudget{remaining: 10}
	asset := photos.Asset{LocalIdentifier: "synthetic", MediaType: "image"}
	if _, _, err := budget.export(context.Background(), cache, asset); err == nil {
		t.Fatal("oversized original accepted")
	}
	if _, _, err := budget.export(context.Background(), cache, asset); err == nil || calls != 1 {
		t.Fatalf("exhausted budget performed another export: calls=%d err=%v", calls, err)
	}
	entries, err := os.ReadDir(cache)
	if err != nil || len(entries) != 0 {
		t.Fatalf("oversized original retained: %v", err)
	}
}

func TestOriginalBudgetAllowsSmallStillWithLargePairedVideo(t *testing.T) {
	old := exportEvalOriginal
	t.Cleanup(func() { exportEvalOriginal = old })
	calls := 0
	exportEvalOriginal = func(ctx context.Context, id, path string, network bool, limit int64) error {
		calls++
		if limit != 10 {
			t.Fatalf("streaming limit = %d", limit)
		}
		return os.WriteFile(path, []byte("still"), 0o600)
	}
	cache := t.TempDir()
	budget := originalBudget{remaining: 10}
	asset := photos.Asset{
		LocalIdentifier: "synthetic", MediaType: "image",
		Resources: []photos.Resource{
			{Type: "photo", FileSize: 5},
			{Type: "paired_video", FileSize: maxOriginalBytes + 1},
		},
	}
	path, cleanup, err := budget.export(context.Background(), cache, asset)
	if err != nil {
		t.Fatal(err)
	}
	cleanup()
	if calls != 1 || budget.remaining != 5 {
		t.Fatalf("calls=%d remaining=%d", calls, budget.remaining)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("owned original was not removed: %v", err)
	}
}

func TestOriginalBudgetPreservesExistingCacheAndConsent(t *testing.T) {
	oldExport, oldRender := exportEvalOriginal, renderEvalImage
	t.Cleanup(func() { exportEvalOriginal, renderEvalImage = oldExport, oldRender })
	exportEvalOriginal = func(context.Context, string, string, bool, int64) error {
		t.Fatal("unexpected export")
		return nil
	}
	renderEvalImage = func(context.Context, string, string, float64) error { return errors.New("render") }
	opts, _ := evalRunOptions(t)
	asset := photos.Asset{LocalIdentifier: "synthetic", MediaType: "image"}
	opts.Provider = budgetProvider{[]photos.Asset{asset}}
	opts.AllowICloudDownloads = false
	if _, err := Run(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(opts.CacheDir, cacheName(asset))
	if err := os.WriteFile(path, []byte("caller-owned cached original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "caller-owned cached original" {
		t.Fatalf("existing cached file changed: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Run(ctx, opts); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
}
