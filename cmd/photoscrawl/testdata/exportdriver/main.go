// This test driver keeps the real native bridge alive across late callbacks.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/openclaw/photoscrawl/internal/photos"
)

func main() {
	root := os.Getenv("PHOTOSCRAWL_NATIVE_FIXTURE_DIR")
	if _, err := os.Stat(filepath.Join(root, "loaded")); err != nil {
		panic("native fixture not loaded")
	}
	destination := os.Args[1]
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := photos.ExportOriginalResource(ctx, "synthetic", destination, false); !errors.Is(err, context.DeadlineExceeded) {
		panic(fmt.Sprintf("deadline error: %v", err))
	}
	if os.Getenv("PHOTOSCRAWL_NATIVE_FIXTURE_MODE") == "retry" {
		if err := photos.ExportOriginalResource(context.Background(), "synthetic", destination, false); err != nil {
			panic(err)
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(root, "late")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			panic("late callback did not arrive")
		}
		time.Sleep(5 * time.Millisecond)
	}
	fmt.Println("deadline propagated; late callback completed safely")
}
