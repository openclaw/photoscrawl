//go:build darwin

package photos

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
	"unsafe"
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

func TestExportImagePreviewCancellationCancelsInFlightRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	bridge := &blockingPreviewExportBridge{started: make(chan struct{}), canceled: make(chan struct{})}
	destination := filepath.Join(t.TempDir(), "preview.jpg")
	done := make(chan error, 1)
	go func() {
		done <- exportImagePreview(ctx, "synthetic", destination, 1600, true, bridge)
	}()
	select {
	case <-bridge.started:
	case <-time.After(2 * time.Second):
		t.Fatal("preview export did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ExportImagePreview = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("preview export did not return after one cancellation")
	}
}

type blockingPreviewExportBridge struct {
	started  chan struct{}
	canceled chan struct{}
}

func (bridge *blockingPreviewExportBridge) create() unsafe.Pointer {
	return unsafe.Pointer(new(byte))
}

func (bridge *blockingPreviewExportBridge) cancel(unsafe.Pointer) {
	close(bridge.canceled)
}

func (bridge *blockingPreviewExportBridge) release(unsafe.Pointer) {}

func (bridge *blockingPreviewExportBridge) export(string, string, int, bool, unsafe.Pointer) error {
	close(bridge.started)
	<-bridge.canceled
	return errors.New("synthetic cancellation")
}
