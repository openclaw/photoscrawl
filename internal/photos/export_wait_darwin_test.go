//go:build darwin

package photos

import (
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
