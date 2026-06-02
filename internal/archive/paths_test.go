package archive

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultPathsUseCrawlkitPlatformDirs(t *testing.T) {
	root := t.TempDir()
	configHome := filepath.Join(root, "config")
	dataHome := filepath.Join(root, "data")
	cacheHome := filepath.Join(root, "cache")
	stateHome := filepath.Join(root, "state")
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_DATA_HOME", dataHome)
	t.Setenv("XDG_CACHE_HOME", cacheHome)
	t.Setenv("XDG_STATE_HOME", stateHome)

	paths, err := DefaultPaths()
	if err != nil {
		t.Fatal(err)
	}
	if paths.ConfigPath != filepath.Join(configHome, "photoscrawl", "config.toml") {
		t.Fatalf("ConfigPath = %q", paths.ConfigPath)
	}
	if paths.DataDir != filepath.Join(dataHome, "photoscrawl") {
		t.Fatalf("DataDir = %q", paths.DataDir)
	}
	if paths.Database != filepath.Join(dataHome, "photoscrawl", "photos.sqlite") {
		t.Fatalf("Database = %q", paths.Database)
	}
	if paths.CacheDir != filepath.Join(cacheHome, "photoscrawl") {
		t.Fatalf("CacheDir = %q", paths.CacheDir)
	}
	if paths.LogDir != filepath.Join(stateHome, "photoscrawl", "logs") {
		t.Fatalf("LogDir = %q", paths.LogDir)
	}
	if paths.ShareDir != filepath.Join(dataHome, "photoscrawl", "share") {
		t.Fatalf("ShareDir = %q", paths.ShareDir)
	}
}

func TestDefaultPathsIgnoreLegacyDotdir(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	dataHome := filepath.Join(root, "data")
	cacheHome := filepath.Join(root, "cache")
	stateHome := filepath.Join(root, "state")
	configHome := filepath.Join(root, "config")
	legacy := filepath.Join(home, "."+"photoscrawl")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "photos.sqlite"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_DATA_HOME", dataHome)
	t.Setenv("XDG_CACHE_HOME", cacheHome)
	t.Setenv("XDG_STATE_HOME", stateHome)

	paths, err := DefaultPaths()
	if err != nil {
		t.Fatal(err)
	}
	if paths.DataDir == legacy || paths.Database == filepath.Join(legacy, "photos.sqlite") {
		t.Fatalf("DefaultPaths used legacy dotdir: %+v", paths)
	}
	if paths.Database != filepath.Join(dataHome, "photoscrawl", "photos.sqlite") {
		t.Fatalf("Database = %q", paths.Database)
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
	if got := paths.PlaceContextCacheDir(); got != filepath.Join("cache", "photoscrawl", "place-context") {
		t.Fatalf("PlaceContextCacheDir = %q", got)
	}
	if got := paths.PlaceBackfillDir(); got != filepath.Join("data", "photoscrawl", "backfills", "place-context-full", "apple-ingest") {
		t.Fatalf("PlaceBackfillDir = %q", got)
	}
}
