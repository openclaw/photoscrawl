package photos

import (
	"context"
	"time"
)

// defaultOriginalExportTimeout bounds PhotoKit original downloads.
// This is intentionally larger than the 20s place-context wait because
// iCloud originals can be large videos or raw files.
const defaultOriginalExportTimeout = 5 * time.Minute

func originalExportTimeout(ctx context.Context) time.Duration {
	if ctx.Err() != nil {
		return 0
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		return defaultOriginalExportTimeout
	}
	remaining := time.Until(deadline)
	if remaining < 0 {
		return 0
	}
	return remaining
}

func originalExportTimeoutNanoseconds(d time.Duration) int64 {
	if d <= 0 {
		return 0
	}
	return d.Nanoseconds()
}
