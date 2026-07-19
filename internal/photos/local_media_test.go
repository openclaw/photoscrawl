package photos

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAttachLocalMediaPathsUsesPackageDerivative(t *testing.T) {
	libraryPath := filepath.Join(t.TempDir(), "Fixture Photos Library.photoslibrary")
	derivativePath := filepath.Join(libraryPath, "resources", "derivatives", "A", "AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE_1_105_c.jpeg")
	if err := os.MkdirAll(filepath.Dir(derivativePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(derivativePath, []byte("fixture image"), 0o644); err != nil {
		t.Fatal(err)
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

	if err := AttachLocalMediaPaths(&snapshot, libraryPath); err != nil {
		t.Fatal(err)
	}
	resource := snapshot.Assets[0].Resources[0]
	if resource.LocalPath != derivativePath {
		t.Fatalf("local path = %q, want %q", resource.LocalPath, derivativePath)
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
