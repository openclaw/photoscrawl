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
	query, ok := manifest.Commands["query"]
	if !ok || !slices.Contains(manifest.Capabilities, "search") || query.Mutates {
		t.Fatalf("search contract = %#v", manifest)
	}
	timeline, ok := manifest.Commands["timeline"]
	if !ok || !slices.Contains(manifest.Capabilities, "timeline") || timeline.Mutates {
		t.Fatalf("timeline contract = %#v", manifest)
	}
	if manifest.Privacy.ExportsSecrets || !slices.Contains(manifest.Privacy.LocalOnlyScopes, "apple-photos") {
		t.Fatalf("privacy = %#v", manifest.Privacy)
	}
}
