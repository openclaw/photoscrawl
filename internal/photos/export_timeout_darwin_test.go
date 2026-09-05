//go:build darwin

package photos

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestExportOriginalResourceReturnsCanceledContextBeforePhotoKit(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := ExportOriginalResource(ctx, "unused", filepath.Join(t.TempDir(), "out.bin"), false)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ExportOriginalResource = %v, want context.Canceled", err)
	}
}

func TestExportOriginalResourceExpiredDeadlineDoesNotWaitForever(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	time.Sleep(2 * time.Millisecond)
	started := time.Now()
	err := ExportOriginalResource(ctx, "unused", filepath.Join(t.TempDir(), "out.bin"), false)
	if err == nil {
		t.Fatal("expected expired deadline to fail")
	}
	if elapsed := time.Since(started); elapsed >= 2*time.Second {
		t.Fatalf("expired export wait took %s", elapsed)
	}
}
