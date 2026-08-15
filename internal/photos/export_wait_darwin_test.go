//go:build darwin

package photos

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestBoundedPhotoKitWaitReturnsAfterDeadline(t *testing.T) {
	timeout := 150 * time.Millisecond
	started := time.Now()
	ok := waitBounded(timeout)
	elapsed := time.Since(started)
	if ok {
		t.Fatal("expected bounded wait to time out")
	}
	if elapsed < 100*time.Millisecond || elapsed >= 2*time.Second {
		t.Fatalf("bounded wait elapsed %s, want about %s", elapsed, timeout)
	}
}

func TestExportOriginalResourceEntersBridgeWithLiveDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	started := time.Now()
	err := ExportOriginalResource(ctx, "unused-local-id", filepath.Join(t.TempDir(), "out.bin"), false)
	elapsed := time.Since(started)
	if err == nil {
		t.Fatal("expected PhotoKit lookup or access error")
	}
	if elapsed >= 2*time.Second {
		t.Fatalf("export hang after entering PhotoKit: %s err=%v", elapsed, err)
	}
}
