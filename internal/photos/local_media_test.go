package photos

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestLocalMediaIndexRanksCandidatesAndAttachesPreferredPath(t *testing.T) {
	libraryPath := filepath.Join(t.TempDir(), "Fixture Photos Library.photoslibrary")
	uuid := "AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE"
	want := []LocalMediaCandidate{
		{Path: filepath.Join(libraryPath, "resources", "derivatives", "A", uuid+"_b.jpeg"), Class: "derivative", Size: 2},
		{Path: filepath.Join(libraryPath, "resources", "derivatives", "A", uuid+"_c.jpeg"), Class: "derivative", Size: 2},
		{Path: filepath.Join(libraryPath, "resources", "derivatives", "A", uuid+"_a.jpeg"), Class: "derivative", Size: 3},
		{Path: filepath.Join(libraryPath, "resources", "renders", "A", uuid+".jpeg"), Class: "render", Size: 1},
		{Path: filepath.Join(libraryPath, "originals", "A", uuid+".jpeg"), Class: "original", Size: 1},
	}
	for _, candidate := range want {
		if err := os.MkdirAll(filepath.Dir(candidate.Path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(candidate.Path, make([]byte, candidate.Size), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	index, err := BuildLocalMediaIndex(libraryPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := index.Candidates(uuid + "/L0/001"); !slices.Equal(got, want) {
		t.Fatalf("candidates = %#v, want %#v", got, want)
	}
	snapshot := LibrarySnapshot{
		Assets: []Asset{
			{
				LocalIdentifier: "AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE/L0/001",
				MediaType:       "image",
				Resources: []Resource{
					{Type: "photo", UTI: "public.heic", OriginalFilename: "IMG_0001.HEIC", Availability: "remote", NeedsDownload: true},
				},
			},
		},
	}

	index.Attach(&snapshot)
	resource := snapshot.Assets[0].Resources[0]
	if resource.LocalPath != want[0].Path {
		t.Fatalf("local path = %q, want %q", resource.LocalPath, want[0].Path)
	}
	if !resource.AvailableLocally || resource.NeedsDownload || resource.Availability != "local" {
		t.Fatalf("availability = %#v", resource)
	}
	if resource.Metadata["local_path_class"] != "derivative" {
		t.Fatalf("metadata = %#v", resource.Metadata)
	}
}

func TestAttachLocalMediaPathsAddsSyntheticResource(t *testing.T) {
	libraryPath := filepath.Join(t.TempDir(), "Fixture Photos Library.photoslibrary")
	originalPath := filepath.Join(libraryPath, "originals", "F", "FFFFFFFF-BBBB-CCCC-DDDD-EEEEEEEEEEEE.jpeg")
	if err := os.MkdirAll(filepath.Dir(originalPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(originalPath, []byte("fixture original"), 0o644); err != nil {
		t.Fatal(err)
	}
	snapshot := LibrarySnapshot{
		Assets: []Asset{
			{LocalIdentifier: "FFFFFFFF-BBBB-CCCC-DDDD-EEEEEEEEEEEE", MediaType: "image"},
		},
	}

	if err := AttachLocalMediaPaths(&snapshot, libraryPath); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Assets[0].Resources) != 1 {
		t.Fatalf("resources = %#v", snapshot.Assets[0].Resources)
	}
	resource := snapshot.Assets[0].Resources[0]
	if resource.Type != "local_original" || resource.LocalPath != originalPath || !resource.AvailableLocally {
		t.Fatalf("resource = %#v", resource)
	}
	if resource.SourceIdentifier != "photos_library_package:original" {
		t.Fatalf("source identifier = %q", resource.SourceIdentifier)
	}
}
