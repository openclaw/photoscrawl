// Synthetic native eval proof: local PNG rendering and a loopback model server.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"

	"github.com/openclaw/photoscrawl/internal/evalcard"
	"github.com/openclaw/photoscrawl/internal/photos"
)

const identifier = "AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE"

type syntheticProvider struct{}

func (syntheticProvider) Snapshot(context.Context, string) (photos.LibrarySnapshot, error) {
	return photos.LibrarySnapshot{Assets: []photos.Asset{{LocalIdentifier: identifier, MediaType: "image"}}}, nil
}

func main() {
	mode := "default"
	if len(os.Args) > 1 {
		mode = os.Args[1]
	}
	if err := run(mode); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(mode string) error {
	root, err := os.MkdirTemp("", "photoscrawl-eval-proof-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	originals := filepath.Join(root, "library", "originals")
	if err := os.MkdirAll(originals, 0o700); err != nil {
		return err
	}
	file, err := os.Create(filepath.Join(originals, identifier+".png"))
	if err != nil {
		return err
	}
	pixels := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	pixels.SetNRGBA(0, 0, color.NRGBA{R: 255, A: 255})
	err = png.Encode(file, pixels)
	if err := errors.Join(err, file.Close()); err != nil {
		return err
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"response":"Synthetic photo card","done":true}`))
	}))
	defer server.Close()
	opts := evalcard.Options{
		LibraryPath: filepath.Join(root, "library"), OutputDir: filepath.Join(root, "out"),
		CacheDir: filepath.Join(root, "cache"), Provider: syntheticProvider{},
		Models: []string{"fixture"}, OllamaGenerateURL: server.URL, Limit: 1, Concurrency: 1,
	}
	if mode == "collisions" {
		opts.Models = []string{"org/vision:latest", "org_vision:latest", "Vision:latest", "vision:latest"}
		opts.Concurrency = len(opts.Models)
	}
	if mode == "blocked" {
		opts.PromptPath = filepath.Join(root, "prompt.md")
		if err := os.WriteFile(opts.PromptPath, []byte("Synthetic prompt {{.MetadataJSON}}"), 0o600); err != nil {
			return err
		}
		path := filepath.Join(opts.OutputDir, "raw", fmt.Sprintf("E001__ollama__%x__%s.json", sha256.Sum256([]byte("fixture")), evalcard.PromptVersion))
		if err := os.MkdirAll(path, 0o700); err != nil {
			return err
		}
	}
	if err := os.Chdir(root); err != nil {
		return err
	}
	result, runErr := evalcard.Run(context.Background(), opts)
	if mode == "blocked" {
		if runErr == nil || calls.Load() != 1 {
			return fmt.Errorf("model output failure was lost: calls=%d result=%+v err=%v", calls.Load(), result, runErr)
		}
		if _, err := os.Stat(filepath.Join(opts.OutputDir, "summary.json")); !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("summary exists after model evidence failure: %v", err)
		}
		fmt.Println("PASS: native eval reports model evidence write failure and omits summary")
		return nil
	}
	if runErr != nil {
		return runErr
	}
	if result.AssetsPrepared != 1 || result.ModelCallsSucceeded != len(opts.Models) || int(calls.Load()) != len(opts.Models) {
		return fmt.Errorf("native fixture was not evaluated: %+v", result)
	}
	files, err := os.ReadDir(filepath.Join(opts.OutputDir, "raw"))
	if err != nil {
		return err
	}
	if len(files) != len(opts.Models) {
		return fmt.Errorf("retained %d outputs after %d successful calls", len(files), result.ModelCallsSucceeded)
	}
	seen := map[string]bool{}
	for _, file := range files {
		data, err := os.ReadFile(filepath.Join(opts.OutputDir, "raw", file.Name()))
		if err != nil {
			return err
		}
		var out struct{ Model, Response string }
		if err := json.Unmarshal(data, &out); err != nil {
			return err
		}
		if out.Response != "Synthetic photo card" || seen[out.Model] {
			return fmt.Errorf("corrupt or duplicate model output: %+v", out)
		}
		seen[out.Model] = true
	}
	for _, model := range opts.Models {
		if !seen[model] {
			return fmt.Errorf("missing model output: %s", model)
		}
	}
	if mode == "collisions" {
		fmt.Println("PASS: native eval retains four distinct model results for punctuation and case collisions")
		return nil
	}
	fmt.Println("PASS: native eval outside checkout renders synthetic PNG, uses built-in prompt and persists model output")
	return nil
}
