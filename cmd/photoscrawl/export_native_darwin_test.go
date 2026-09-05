//go:build darwin

package main

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openclaw/photoscrawl/internal/archive"
	"github.com/openclaw/photoscrawl/internal/photos"
)

type nativeExportFixture struct{ filename string }

func (p nativeExportFixture) Snapshot(context.Context, string) (photos.LibrarySnapshot, error) {
	return photos.LibrarySnapshot{Provider: "synthetic", Assets: []photos.Asset{{
		LocalIdentifier: "synthetic", MediaType: "image",
		Resources: []photos.Resource{{SourceIdentifier: "synthetic-original", Type: "original", OriginalFilename: p.filename}},
	}}}, nil
}

// A required dylib replaces PhotoKit before main, so these binaries cannot fall
// through to the user's library even if DYLD environment injection is disabled.
func TestExportNativeIntegration(t *testing.T) {
	build := t.TempDir()
	fixture := filepath.Join(build, "photokit-fixture.dylib")
	compile := exec.Command("xcrun", "clang", "-dynamiclib", "-fobjc-arc", "-framework", "Foundation", "-framework", "Photos", "-o", fixture, "testdata/photokit_fixture.m")
	if out, err := compile.CombinedOutput(); err != nil {
		t.Fatalf("build native fixture: %v\n%s", err, out)
	}
	cli := filepath.Join(build, "photoscrawl")
	driver := filepath.Join(build, "export-driver")
	for _, target := range []struct{ output, pkg string }{{cli, "."}, {driver, "./testdata/exportdriver"}} {
		cmd := exec.Command("go", "build", "-ldflags", "-linkmode=external -extldflags=-Wl,-needed_library,"+fixture, "-o", target.output, target.pkg)
		cmd.Env = append(os.Environ(), "GOWORK=off")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build native proof binary: %v\n%s", err, out)
		}
	}
	cases := []struct {
		name, mode, wantError                        string
		flags                                        []string
		signal, driver, directory, longName, success bool
	}{
		{name: "unlimited-default", mode: "slow", success: true},
		{name: "explicit-unlimited", mode: "slow", flags: []string{"--timeout", "0"}, success: true},
		{name: "unlimited-authorization", mode: "auth-slow", success: true},
		{name: "timeout", mode: "stall", flags: []string{"--timeout", "1s"}, wantError: "context deadline exceeded"},
		{name: "cancel-download", mode: "stall", signal: true, wantError: "context canceled"},
		{name: "cancel-authorization", mode: "auth", signal: true, wantError: "context canceled"},
		{name: "cancel-before-request-id", mode: "delayed-id", signal: true, wantError: "context canceled"},
		{name: "provider-error-lifetime", mode: "error", wantError: "synthetic provider failure"},
		{name: "write-error", mode: "write-error", wantError: "write exported original"},
		{name: "atomic-replacement-failure", mode: "success", directory: true, wantError: "promote exported original"},
		{name: "long-destination", mode: "success", longName: true, success: true},
		{name: "invalid-timeout", mode: "success", flags: []string{"--timeout", "-1s"}, wantError: "timeout must not be negative"},
		{name: "retry-before-late-callback", mode: "retry", driver: true, success: true},
		{name: "late-authorization-callback", mode: "auth", driver: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			outDir := filepath.Join(dir, "out")
			if err := os.Mkdir(outDir, 0700); err != nil {
				t.Fatal(err)
			}
			filename := "original.bin"
			if tc.longName {
				filename = strings.Repeat("a", 230)
			}
			destination := filepath.Join(outDir, filename)
			preserved := destination
			if tc.directory {
				if err := os.Mkdir(destination, 0700); err != nil {
					t.Fatal(err)
				}
				preserved = filepath.Join(destination, "sentinel")
			}
			if err := os.WriteFile(preserved, []byte("existing-original"), 0600); err != nil {
				t.Fatal(err)
			}
			dbPath := filepath.Join(dir, "archive.sqlite")
			if _, err := archive.Crawl(context.Background(), archive.Paths{Database: dbPath}, archive.CrawlOptions{LibraryPath: dir, Provider: nativeExportFixture{filename}}); err != nil {
				t.Fatal(err)
			}
			db, err := sql.Open("sqlite", dbPath)
			if err != nil {
				t.Fatal(err)
			}
			var id string
			err = db.QueryRow("select id from asset").Scan(&id)
			_ = db.Close()
			if err != nil {
				t.Fatal(err)
			}
			limit := 10 * time.Second
			if tc.mode == "auth-slow" {
				limit = 30 * time.Second
			}
			ctx, cancel := context.WithTimeout(context.Background(), limit)
			defer cancel()
			args := []string{"export", "--db", dbPath, "--id", id, "--output", outDir, "--json"}
			args = append(args, tc.flags...)
			binary := cli
			if tc.driver {
				binary = driver
				args = []string{destination}
			}
			cmd := exec.CommandContext(ctx, binary, args...)
			cmd.Env = append(os.Environ(), "PHOTOSCRAWL_NATIVE_FIXTURE_DIR="+dir, "PHOTOSCRAWL_NATIVE_FIXTURE_MODE="+tc.mode)
			var output bytes.Buffer
			cmd.Stdout = &output
			cmd.Stderr = &output
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = cmd.Process.Kill() })
			if tc.signal {
				marker := "requested"
				if tc.mode == "auth" {
					marker = "authorization"
				}
				waitNativeMarker(t, ctx, filepath.Join(dir, marker))
				if err := cmd.Process.Signal(os.Interrupt); err != nil {
					t.Fatal(err)
				}
			}
			err = cmd.Wait()
			if ctx.Err() != nil {
				t.Fatalf("native proof hung: %v\n%s", ctx.Err(), output.String())
			}
			if tc.wantError != "" {
				if err == nil || !strings.Contains(output.String(), tc.wantError) {
					t.Fatalf("error=%v, want %q\n%s", err, tc.wantError, output.String())
				}
			} else if err != nil {
				t.Fatalf("native proof: %v\n%s", err, output.String())
			}
			if _, err := os.Stat(filepath.Join(dir, "loaded")); err != nil {
				t.Fatal("required PhotoKit fixture was not loaded")
			}
			if tc.mode == "stall" || tc.mode == "delayed-id" || tc.mode == "retry" || tc.mode == "write-error" {
				if _, err := os.Stat(filepath.Join(dir, "cancelled")); err != nil {
					t.Fatal("native request was not cancelled")
				}
			}
			contents, err := os.ReadFile(preserved)
			if err != nil {
				t.Fatal(err)
			}
			want := "existing-original"
			if tc.success {
				want = "synthetic-original"
			}
			if string(contents) != want {
				t.Fatalf("destination=%q, want %q", contents, want)
			}
			staging, err := filepath.Glob(filepath.Join(outDir, ".photoscrawl-export-*"))
			if err != nil || len(staging) != 0 {
				t.Fatalf("staging files remain: %v (%v)", staging, err)
			}
			t.Logf("real native bridge: %s; destination verified; no staging files", tc.name)
		})
	}
}

func waitNativeMarker(t *testing.T, ctx context.Context, path string) {
	t.Helper()
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("native fixture did not reach requested phase")
		case <-time.After(5 * time.Millisecond):
		}
	}
}
