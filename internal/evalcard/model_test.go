package evalcard

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/openclaw/photoscrawl/internal/photos"
)

func TestRunUsesBuiltInPromptOutsideCheckout(t *testing.T) {
	opts, _ := evalRunOptions(t)
	opts.PromptPath = ""
	t.Chdir(t.TempDir())
	if _, err := Run(context.Background(), opts); err != nil {
		t.Fatalf("default prompt depends on checkout: %v", err)
	}
}

func preparedModelOptions(t *testing.T) Options {
	t.Helper()
	opts, _ := evalRunOptions(t)
	asset := photos.Asset{LocalIdentifier: "synthetic", MediaType: "image"}
	opts.Provider = budgetProvider{[]photos.Asset{asset}}
	opts.Models = []string{"fixture"}
	opts.Concurrency = 1
	if err := os.MkdirAll(opts.CacheDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(opts.CacheDir, cacheName(asset)), []byte("synthetic original"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldRender, oldMetadata := renderEvalImage, readEvalMetadata
	t.Cleanup(func() { renderEvalImage, readEvalMetadata = oldRender, oldMetadata })
	renderEvalImage = func(_ context.Context, _, dest string, _ float64) error {
		return os.WriteFile(dest, []byte("synthetic image"), 0o600)
	}
	readEvalMetadata = func(context.Context, string) (map[string]any, error) {
		return map[string]any{"synthetic": true}, nil
	}
	return opts
}

func TestRunStopsWhenModelOutputCannotBePersisted(t *testing.T) {
	opts := preparedModelOptions(t)
	opts.Models = []string{"fixture", "second", "third"}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"response":"synthetic card","done":true}`))
	}))
	defer server.Close()
	opts.OllamaGenerateURL = server.URL
	blocked := filepath.Join(opts.OutputDir, "raw", "E001__ollama__fixture__"+PromptVersion+".json")
	if err := os.MkdirAll(blocked, 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := Run(context.Background(), opts)
	if err == nil || !strings.Contains(err.Error(), "write model output") {
		t.Fatalf("lost persistence failure: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("continued after evidence write failure: %d calls", calls.Load())
	}
	if _, err := os.Stat(filepath.Join(opts.OutputDir, "summary.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("summary exists after evidence write failure: %v", err)
	}
}

func TestRunRetainsProviderFailuresAsEvidence(t *testing.T) {
	opts := preparedModelOptions(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "synthetic provider failure", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	opts.OllamaGenerateURL = server.URL
	result, err := Run(context.Background(), opts)
	if err != nil || result.ModelCallsFailed != 1 || result.ModelCallsSucceeded != 0 {
		t.Fatalf("provider failure result = %#v, %v", result, err)
	}
	data, err := os.ReadFile(filepath.Join(opts.OutputDir, "raw", "E001__ollama__fixture__"+PromptVersion+".json"))
	if err != nil || !strings.Contains(string(data), "synthetic provider failure") {
		t.Fatalf("missing failure evidence: %s, %v", data, err)
	}
}

func TestRunReturnsCancellationFromLastPreparation(t *testing.T) {
	opts := preparedModelOptions(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	renderEvalImage = func(context.Context, string, string, float64) error {
		cancel()
		return context.Canceled
	}
	if _, err := Run(ctx, opts); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation became a successful empty run: %v", err)
	}
}

func TestRunReturnsCancellationDuringModelCall(t *testing.T) {
	opts := preparedModelOptions(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		cancel()
		_, _ = w.Write([]byte(`{"response":"synthetic card","done":true}`))
	}))
	defer server.Close()
	opts.OllamaGenerateURL = server.URL
	if _, err := Run(ctx, opts); !errors.Is(err, context.Canceled) {
		t.Fatalf("model cancellation became a completed run: %v", err)
	}
}
