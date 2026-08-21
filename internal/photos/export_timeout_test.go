package photos

import (
	"context"
	"testing"
	"time"
)

func TestOriginalExportTimeoutUsesDefaultWithoutDeadline(t *testing.T) {
	t.Parallel()
	got := originalExportTimeout(context.Background())
	if got != defaultOriginalExportTimeout {
		t.Fatalf("timeout = %s, want %s", got, defaultOriginalExportTimeout)
	}
}

func TestOriginalExportTimeoutDefaultLeavesRoomForLargeOriginals(t *testing.T) {
	t.Parallel()
	// Place-context waits 20s. Original iCloud downloads can be much larger.
	if defaultOriginalExportTimeout <= 20*time.Second {
		t.Fatalf("default original export timeout %s reuses the 20s place-context wait", defaultOriginalExportTimeout)
	}
	if defaultOriginalExportTimeout < 5*time.Minute {
		t.Fatalf("default original export timeout %s is shorter than 5m", defaultOriginalExportTimeout)
	}
}

func TestOriginalExportTimeoutUsesRemainingDeadline(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	got := originalExportTimeout(ctx)
	if got <= 80*time.Second || got > 90*time.Second {
		t.Fatalf("timeout = %s, want remaining deadline near 90s", got)
	}
}

func TestOriginalExportTimeoutZeroWhenAlreadyExpired(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := originalExportTimeout(ctx); got != 0 {
		t.Fatalf("timeout = %s, want 0 after cancel", got)
	}
}

func TestOriginalExportTimeoutNanosecondsMatchesDuration(t *testing.T) {
	t.Parallel()
	if got := originalExportTimeoutNanoseconds(5 * time.Minute); got != (5 * time.Minute).Nanoseconds() {
		t.Fatalf("nanoseconds = %d, want %d", got, (5 * time.Minute).Nanoseconds())
	}
	if got := originalExportTimeoutNanoseconds(0); got != 0 {
		t.Fatalf("nanoseconds = %d, want 0", got)
	}
	if got := originalExportTimeoutNanoseconds(-time.Second); got != 0 {
		t.Fatalf("negative nanoseconds = %d, want 0", got)
	}
}
