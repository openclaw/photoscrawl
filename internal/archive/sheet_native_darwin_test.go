//go:build darwin

package archive

import (
	"context"
	"errors"
	"image/color"
	"os"
	"path/filepath"
	"testing"
)

func TestNativeSheetRendererChecksSourceDimensions(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source.png")
	writeSyntheticPNG(t, source, color.RGBA{R: 255, A: 255})
	if err := renderSheetSource(context.Background(), source, filepath.Join(root, "small.jpg"), 0.9); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, oversizedSheetImage(t, "png"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := renderSheetSource(context.Background(), source, filepath.Join(root, "large.jpg"), 0.9); !errors.Is(err, errSheetSourceSize) {
		t.Fatalf("oversized native source error = %v", err)
	}
}
