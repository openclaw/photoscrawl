package archive

import (
	"path/filepath"
	"slices"
	"testing"
)

func TestControlManifest(t *testing.T) {
	paths := Paths{
		ConfigPath: filepath.Join("config", "config.toml"),
		Database:   filepath.Join("data", "photos.sqlite"),
		CacheDir:   "cache",
		LogDir:     "logs",
		ShareDir:   "share",
	}
	manifest := ControlManifest(paths)
	if manifest.ID != "photoscrawl" || manifest.Binary.Name != "photoscrawl" {
		t.Fatalf("identity = %#v", manifest)
	}
	if manifest.Paths.DefaultDatabase != paths.Database || manifest.Paths.DefaultCache != paths.CacheDir {
		t.Fatalf("paths = %#v", manifest.Paths)
	}
	if !slices.Contains(manifest.Capabilities, "search") || manifest.Commands["query"].Mutates {
		t.Fatalf("search contract = %#v", manifest)
	}
	if manifest.Privacy.ExportsSecrets || !slices.Contains(manifest.Privacy.LocalOnlyScopes, "apple-photos") {
		t.Fatalf("privacy = %#v", manifest.Privacy)
	}
}
