package archive

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultPathsUseCrawlkitPlatformDirs(t *testing.T) {
	for _, tc := range []struct {
		name   string
		legacy bool
	}{
		{"without_legacy_archive", false},
		{"with_legacy_archive", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			home := filepath.Join(root, "home")
			configHome := filepath.Join(root, "config")
			dataHome := filepath.Join(root, "data")
			cacheHome := filepath.Join(root, "cache")
			stateHome := filepath.Join(root, "state")
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", configHome)
			t.Setenv("XDG_DATA_HOME", dataHome)
			t.Setenv("XDG_CACHE_HOME", cacheHome)
			t.Setenv("XDG_STATE_HOME", stateHome)
			if tc.legacy {
				legacy := filepath.Join(home, "."+"photoscrawl")
				if err := os.MkdirAll(legacy, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(legacy, "photos.sqlite"), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}

			got, err := DefaultPaths()
			if err != nil {
				t.Fatal(err)
			}
			want := Paths{
				ConfigPath: filepath.Join(configHome, "photoscrawl", "config.toml"),
				DataDir:    filepath.Join(dataHome, "photoscrawl"),
				Database:   filepath.Join(dataHome, "photoscrawl", "photos.sqlite"),
				CacheDir:   filepath.Join(cacheHome, "photoscrawl"),
				LogDir:     filepath.Join(stateHome, "photoscrawl", "logs"),
				ShareDir:   filepath.Join(dataHome, "photoscrawl", "share"),
			}
			if got != want {
				t.Fatalf("DefaultPaths = %#v, want %#v", got, want)
			}
		})
	}
}

func TestDerivedRuntimeDirs(t *testing.T) {
	paths := Paths{
		DataDir:  filepath.Join("data", "photoscrawl"),
		CacheDir: filepath.Join("cache", "photoscrawl"),
	}
	if got := paths.EvalRootDir(); got != filepath.Join("data", "photoscrawl", "evals") {
		t.Fatalf("EvalRootDir = %q", got)
	}
	if got := paths.OriginalsCacheDir(); got != filepath.Join("cache", "photoscrawl", "originals") {
		t.Fatalf("OriginalsCacheDir = %q", got)
	}
	if got := paths.ClassificationPreviewCacheDir(); got != filepath.Join("cache", "photoscrawl", "classification-previews") {
		t.Fatalf("ClassificationPreviewCacheDir = %q", got)
	}
	if got := paths.PlaceContextCacheDir(); got != filepath.Join("cache", "photoscrawl", "place-context") {
		t.Fatalf("PlaceContextCacheDir = %q", got)
	}
	if got := paths.PlaceBackfillDir(); got != filepath.Join("data", "photoscrawl", "backfills", "place-context-full", "apple-ingest") {
		t.Fatalf("PlaceBackfillDir = %q", got)
	}
}
