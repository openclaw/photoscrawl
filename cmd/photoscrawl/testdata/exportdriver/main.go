// This test driver keeps the real native bridge alive across late callbacks.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/openclaw/photoscrawl/internal/photos"
)

func main() {
	root := os.Getenv("PHOTOSCRAWL_NATIVE_FIXTURE_DIR")
	if _, err := os.Stat(filepath.Join(root, "loaded")); err != nil {
		panic("native fixture not loaded")
	}
	destination := os.Args[1]
	mode := os.Getenv("PHOTOSCRAWL_NATIVE_FIXTURE_MODE")
	if strings.HasPrefix(mode, "limited-") {
		runLimited(root, destination, mode)
		return
	}
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

func runLimited(root, destination, mode string) {
	var limit int64
	switch mode {
	case "limited-exact", "limited-cancel":
		limit = 18
	case "limited-over-single", "limited-over-chunked":
		limit = 17
	default:
		panic("unknown limited fixture mode")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- photos.ExportOriginalResourceLimited(ctx, "synthetic", destination, false, limit)
	}()
	if mode == "limited-cancel" {
		waitMarker(root, "partial")
		staging, err := filepath.Glob(filepath.Join(filepath.Dir(destination), ".photoscrawl-export-*"))
		if err != nil || len(staging) != 1 {
			panic(fmt.Sprintf("partial staging files: %v (%v)", staging, err))
		}
		partial, err := os.ReadFile(staging[0])
		if err != nil || string(partial) != "partial" {
			panic(fmt.Sprintf("partial write: %q (%v)", partial, err))
		}
		cancel()
	}
	var err error
	select {
	case err = <-result:
	case <-time.After(5 * time.Second):
		panic("limited export did not return")
	}
	switch mode {
	case "limited-exact":
		if err != nil {
			panic(fmt.Sprintf("exact budget: %v", err))
		}
	case "limited-over-single", "limited-over-chunked":
		if err == nil || err.Error() != "original resource exceeds byte limit" {
			panic(fmt.Sprintf("byte limit error: %v", err))
		}
	case "limited-cancel":
		if !errors.Is(err, context.Canceled) {
			panic(fmt.Sprintf("cancellation error: %v", err))
		}
		contents, readErr := os.ReadFile(destination)
		staging, globErr := filepath.Glob(filepath.Join(filepath.Dir(destination), ".photoscrawl-export-*"))
		if readErr != nil || string(contents) != "existing-original" || globErr != nil || len(staging) != 0 {
			panic("cancelled export did not preserve destination and remove staging before returning")
		}
		if _, statErr := os.Stat(filepath.Join(root, "late")); !errors.Is(statErr, os.ErrNotExist) {
			panic("late callbacks ran before the post-return barrier")
		}
		if writeErr := os.WriteFile(filepath.Join(root, "export-returned"), nil, 0600); writeErr != nil {
			panic(writeErr)
		}
		waitMarker(root, "late")
	}
	fmt.Printf("limited export %s: expected result; callbacks complete\n", mode)
}

func waitMarker(root, name string) {
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, err := os.Stat(filepath.Join(root, name))
		if err == nil {
			return
		}
		if !errors.Is(err, os.ErrNotExist) {
			panic(err)
		}
		if time.Now().After(deadline) {
			panic("native fixture marker did not arrive: " + name)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
