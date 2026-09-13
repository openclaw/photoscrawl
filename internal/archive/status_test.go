package archive

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestStatusPreservesFilesystemErrors(t *testing.T) {
	t.Parallel()
	file := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(file, []byte("synthetic"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Status(context.Background(), Paths{Database: filepath.Join(file, "photos.sqlite")})
	var pathErr *os.PathError
	if !errors.As(err, &pathErr) {
		t.Fatalf("filesystem error reported as missing: %v", err)
	}
}

func TestStatusInspectsTheInitializedFilename(t *testing.T) {
	t.Parallel()
	filename := filepath.Join(t.TempDir(), "photos.sqlite")
	paths := Paths{Database: filename + " "}
	if _, err := Init(context.Background(), paths); err != nil {
		t.Fatal(err)
	}
	result, err := Status(context.Background(), paths)
	if err != nil {
		t.Fatal(err)
	}
	if result.State != "ready" || result.DatabasePath != filename || result.DatabaseBytes == 0 {
		t.Fatalf("status disagrees with init: %+v", result)
	}
	if len(result.Databases) != 1 || result.Databases[0].Path != filename || result.Databases[0].Bytes != result.DatabaseBytes {
		t.Fatalf("inconsistent database metadata: %+v", result.Databases)
	}
}
