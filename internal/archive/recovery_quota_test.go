package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openclaw/photoscrawl/internal/photos"
)

func TestRecoveryPendingQuotaWithActualClassifier(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	paths := testPaths(t)
	library := filepath.Join(t.TempDir(), "Synthetic.photoslibrary")
	if err := mkdirLibrary(library); err != nil {
		t.Fatal(err)
	}
	good := writeModelTestImage(t)
	assets := []photos.Asset{
		{LocalIdentifier: "quota-good", MediaType: "image", Resources: []photos.Resource{{Type: "photo", UTI: "public.jpeg", LocalPath: good, AvailableLocally: true}}},
		{LocalIdentifier: "quota-unreadable", MediaType: "image", Resources: []photos.Resource{{Type: "photo", UTI: "public.jpeg", LocalPath: filepath.Join(t.TempDir(), "missing.jpeg"), AvailableLocally: true}}},
		{LocalIdentifier: "quota-unavailable", MediaType: "image"},
		{LocalIdentifier: "quota-waiting", MediaType: "image", Resources: []photos.Resource{{Type: "photo", UTI: "public.jpeg", NeedsDownload: true}}},
		{LocalIdentifier: "outside-metadata", MediaType: "image", Resources: []photos.Resource{{Type: "photo", UTI: "public.jpeg", LocalPath: good, AvailableLocally: true}}},
		{LocalIdentifier: "outside-old-failure", MediaType: "image", Resources: []photos.Resource{{Type: "photo", UTI: "public.jpeg", LocalPath: good, AvailableLocally: true}}},
	}
	for i := range assets {
		assets[i].CreationDate = "2026-08-12T12:00:00Z"
		for j := range assets[i].Resources {
			assets[i].Resources[j].SourceIdentifier = assets[i].LocalIdentifier + "-resource"
		}
	}
	if _, err := Crawl(ctx, paths, CrawlOptions{LibraryPath: library, Provider: fakeProvider{snapshot: photos.LibrarySnapshot{Provider: "fake", AuthorizationStatus: "authorized", Assets: assets}}, Now: fixedClock("2026-08-12T12:05:00Z")}); err != nil {
		t.Fatal(err)
	}
	db, err := openArchiveStore(ctx, paths.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, item := range []struct{ id, state string }{{"outside-metadata", "metadata_classified"}, {"outside-old-failure", "content_failed"}} {
		if _, err := db.DB().ExecContext(ctx, `update classification_queue set state=?,reason='original outside',updated_at='2026-01-01T00:00:00Z' where asset_id in (select id from asset where local_identifier=?)`, item.state, item.id); err != nil {
			t.Fatal(err)
		}
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": `{"scene_summary":"Synthetic."}`}, "finish_reason": "stop"}}})
	}))
	defer server.Close()
	results := make(chan ClassifyResult, 2)
	errors := make(chan error, 2)
	var workers sync.WaitGroup
	for i := 0; i < 2; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			result, err := Classify(ctx, paths, ClassifyOptions{Limit: 2, LocalModel: "fixture", LocalModelAPI: "openai", LocalModelURL: server.URL, Now: fixedClock("2026-08-12T12:10:00Z")})
			results <- result
			errors <- err
		}()
	}
	workers.Wait()
	close(results)
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	processed, attempts := 0, 0
	for result := range results {
		processed += result.Processed
		attempts += result.LocalModelHTTPRequestAttempts
	}
	if processed != 4 || attempts != 1 || calls.Load() != 1 {
		t.Fatalf("processed=%d attempts=%d calls=%d", processed, attempts, calls.Load())
	}
	for _, item := range []struct{ id, state string }{{"quota-good", "content_classified"}, {"quota-unreadable", "content_failed"}, {"quota-unavailable", "content_unavailable"}, {"quota-waiting", "metadata_classified"}, {"outside-metadata", "metadata_classified"}, {"outside-old-failure", "content_failed"}} {
		var state, reason, updated, owner string
		if err := db.DB().QueryRowContext(ctx, `select q.state,q.reason,q.updated_at,q.claim_owner from classification_queue q join asset a on a.id=q.asset_id where a.local_identifier=?`, item.id).Scan(&state, &reason, &updated, &owner); err != nil {
			t.Fatal(err)
		}
		if state != item.state || owner != "" {
			t.Fatalf("%s state=%s owner=%s", item.id, state, owner)
		}
		if item.id == "outside-metadata" || item.id == "outside-old-failure" {
			if reason != "original outside" || updated != "2026-01-01T00:00:00Z" {
				t.Fatalf("outside row changed: %s %s", reason, updated)
			}
		}
	}
}

func TestRecoveryLiveClaimCannotBeDuplicated(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	paths := testPaths(t)
	library := filepath.Join(t.TempDir(), "Synthetic.photoslibrary")
	if err := mkdirLibrary(library); err != nil {
		t.Fatal(err)
	}
	if _, err := Crawl(ctx, paths, CrawlOptions{LibraryPath: library, Provider: fakeProvider{snapshot: photos.LibrarySnapshot{Provider: "fake", AuthorizationStatus: "authorized", Assets: []photos.Asset{{LocalIdentifier: "claim-fixture", MediaType: "image"}}}}}); err != nil {
		t.Fatal(err)
	}
	db, err := openArchiveStore(ctx, paths.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now().UTC()
	if err := db.WithTx(ctx, func(tx *sql.Tx) error {
		input, err := claimClassifyInput(ctx, tx, "owner-a", now, true, false)
		if err == nil && input == nil {
			t.Error("first claim missing")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.WithTx(ctx, func(tx *sql.Tx) error {
		input, err := claimClassifyInput(ctx, tx, "owner-b", now, true, false)
		if input != nil {
			t.Error("live claim stolen")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var owner string
	if err := db.DB().QueryRowContext(ctx, `select claim_owner from classification_queue`).Scan(&owner); err != nil || owner != "owner-a" {
		t.Fatalf("owner=%s error=%v", owner, err)
	}
}
