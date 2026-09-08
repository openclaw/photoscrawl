package evalcard

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/openclaw/photoscrawl/internal/photos"
)

const maxOriginalBytes int64 = 256 << 20
const maxRunOriginalBytes int64 = 512 << 20

type originalBudget struct {
	remaining int64
}

var exportEvalOriginal = photos.ExportOriginalResourceLimited
var renderEvalImage = photos.RenderCanonicalJPEG
var readEvalMetadata = photos.ImageMetadata

func (b *originalBudget) export(ctx context.Context, cacheDir string, asset photos.Asset) (string, func(), error) {
	limit := min(maxOriginalBytes, b.remaining)
	if limit <= 0 {
		return "", func() {}, errors.New("original_byte_budget")
	}
	// PhotoKit selects one original resource. A larger paired video must not
	// exclude its still image; enforce the limit on the selected byte stream.
	root, err := os.MkdirTemp(cacheDir, "eval-original-")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(root) }
	path := filepath.Join(root, cacheName(asset))
	// Reserve before streaming; failed exports may have consumed the full
	// allowance. Only successful, verified sizes can refund unused bytes.
	b.remaining -= limit
	if err := exportEvalOriginal(ctx, asset.LocalIdentifier, path, true, limit); err != nil {
		cleanup()
		return "", func() {}, errors.New("export_original")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > limit {
		cleanup()
		return "", func() {}, errors.New("invalid_exported_original")
	}
	b.remaining += limit - info.Size()
	return path, cleanup, nil
}
