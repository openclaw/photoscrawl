package evalcard

import (
	"os"
	"path/filepath"
	"testing"
	"time"
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
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(old)

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
