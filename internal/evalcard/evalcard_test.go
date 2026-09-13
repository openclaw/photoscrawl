package evalcard

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/openclaw/photoscrawl/internal/photos"
)

func TestNormalizeOllamaGenerateURL(t *testing.T) {
	tests := map[string]string{
		"":                                    DefaultOllamaGenerateURL,
		"http://127.0.0.1:11434":              "http://127.0.0.1:11434/api/generate",
		"http://127.0.0.1:11434/api":          "http://127.0.0.1:11434/api/generate",
		"http://127.0.0.1:11434/api/generate": "http://127.0.0.1:11434/api/generate",
	}
	for input, want := range tests {
		if got := normalizeOllamaGenerateURL(input); got != want {
			t.Fatalf("normalizeOllamaGenerateURL(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestDefaultOutputDirIsPrivate(t *testing.T) {
	root := t.TempDir()
	got, err := defaultedOutputDir("", root, func() time.Time {
		return time.Date(2026, 5, 30, 12, 30, 0, 0, time.UTC)
	})
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "2026-05-30-123000-photo-card")
	if got != want {
		t.Fatalf("defaultedOutputDir = %q, want %q", got, want)
	}
}

func TestRejectRepoPath(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)

	if err := rejectRepoPath(filepath.Join(root, "evals")); err == nil {
		t.Fatal("rejectRepoPath accepted a repo-local output dir")
	}
	if err := rejectRepoPath(filepath.Join(t.TempDir(), "evals")); err != nil {
		t.Fatalf("rejectRepoPath rejected external output dir: %v", err)
	}
}

func TestPromptWithMetadataUsesTemplateFileText(t *testing.T) {
	got, err := promptWithMetadata("Prompt\n\n{{.MetadataJSON}}", []byte(`{"asset":"A"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got != "Prompt\n\n{\"asset\":\"A\"}" {
		t.Fatalf("promptWithMetadata = %q", got)
	}
}

func TestFinishManifestReturnsFlushError(t *testing.T) {
	dir := t.TempDir()
	f, err := os.Create(filepath.Join(dir, "manifest.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	want := errors.New("no space left on device")
	writer := bufio.NewWriterSize(errWriter{err: want}, 64)
	if _, err := writer.WriteString(`{"eval_id":"E001"}` + "\n"); err != nil {
		t.Fatal(err)
	}
	err = finishManifest(writer, f)
	if !errors.Is(err, want) {
		t.Fatalf("finishManifest = %v, want %v", err, want)
	}
}

func TestFinishManifestReturnsCloseError(t *testing.T) {
	dir := t.TempDir()
	f, err := os.Create(filepath.Join(dir, "manifest.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	err = finishManifest(bufio.NewWriter(io.Discard), f)
	if err == nil {
		t.Fatal("finishManifest succeeded on an already-closed file")
	}
}

func TestRunReturnsManifestFlushErrorBeforeWritingSummary(t *testing.T) {
	orig := commitManifest
	want := errors.New("no space left on device")
	commitManifest = func(*bufio.Writer, *os.File) error {
		return want
	}
	t.Cleanup(func() { commitManifest = orig })

	opts, summaryPath := evalRunOptions(t)
	_, err := Run(context.Background(), opts)
	if !errors.Is(err, want) {
		t.Fatalf("Run = %v, want %v", err, want)
	}
	if _, statErr := os.Stat(summaryPath); !os.IsNotExist(statErr) {
		t.Fatalf("summary.json exists after manifest flush failure: %v", statErr)
	}
}

func TestRunWritesSummaryAfterManifestCommit(t *testing.T) {
	opts, summaryPath := evalRunOptions(t)
	result, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(result.ManifestPath); err != nil {
		t.Fatalf("manifest.jsonl: %v", err)
	}
	if _, err := os.Stat(summaryPath); err != nil {
		t.Fatalf("summary.json: %v", err)
	}
}

func evalRunOptions(t *testing.T) (Options, string) {
	t.Helper()
	dir := t.TempDir()
	lib := filepath.Join(dir, "lib")
	if err := os.Mkdir(lib, 0o755); err != nil {
		t.Fatal(err)
	}
	prompt := filepath.Join(dir, "prompt.md")
	if err := os.WriteFile(prompt, []byte("prompt"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out")
	return Options{
		LibraryPath: lib,
		OutputDir:   out,
		CacheDir:    filepath.Join(dir, "cache"),
		PromptPath:  prompt,
		Provider:    stubProvider{},
		Limit:       1,
	}, filepath.Join(out, "summary.json")
}

type stubProvider struct{}

func (stubProvider) Snapshot(context.Context, string) (photos.LibrarySnapshot, error) {
	return photos.LibrarySnapshot{}, nil
}

type errWriter struct{ err error }

func (w errWriter) Write([]byte) (int, error) {
	return 0, w.err
}
