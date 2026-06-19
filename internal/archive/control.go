package archive

import "github.com/openclaw/crawlkit/control"

func ControlManifest(paths Paths) control.Manifest {
	manifest := control.NewManifest("photoscrawl", "Apple Photos", "photoscrawl")
	manifest.Description = "Local-first, read-only Apple Photos archive crawler."
	manifest.Branding = control.Branding{
		SymbolName:       "photo.on.rectangle.angled",
		AccentColor:      "#ff2d55",
		BundleIdentifier: "com.apple.Photos",
	}
	manifest.Paths = control.Paths{
		DefaultConfig:   paths.ConfigPath,
		DefaultDatabase: paths.Database,
		DefaultCache:    paths.CacheDir,
		DefaultLogs:     paths.LogDir,
		DefaultShare:    paths.ShareDir,
	}
	manifest.Capabilities = []string{"metadata", "status", "init", "search"}
	manifest.Privacy = control.Privacy{
		ExportsSecrets: false,
		LocalOnlyScopes: []string{
			"apple-photos",
			"sqlite",
			"media-metadata",
			"location-observations",
			"local-model-observations",
		},
	}
	manifest.Commands = map[string]control.Command{
		"metadata": {Title: "Metadata", Argv: []string{"photoscrawl", "metadata", "--json"}, JSON: true},
		"status":   {Title: "Status", Argv: []string{"photoscrawl", "status", "--json"}, JSON: true},
		"init":     {Title: "Initialize archive", Argv: []string{"photoscrawl", "init", "--json"}, JSON: true, Mutates: true},
		"query":    {Title: "Search", Argv: []string{"photoscrawl", "search", "--json", "--query"}, JSON: true},
	}
	return manifest
}
