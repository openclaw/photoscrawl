package archive

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/openclaw/crawlkit/store"
	"github.com/openclaw/photoscrawl/internal/photos"
)

func TestTimelineEnforcesArchiveSchemaVersion(t *testing.T) {
	t.Parallel()
	for _, version := range []int{SchemaVersion - 1, SchemaVersion + 1} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			paths := testPaths(t)
			db, err := store.Open(context.Background(), store.Options{Path: paths.Database, Schema: Schema, SchemaVersion: version})
			if err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			_, err = Timeline(context.Background(), paths, TimelineOptions{From: "2026-01-01T00:00:00Z", To: "2027-01-01T00:00:00Z"})
			if err == nil || !strings.Contains(err.Error(), "schema version") {
				t.Fatalf("version %d accepted or wrong error: %v", version, err)
			}
		})
	}
}

func TestTimelineExcludesTombstonesWithoutQueueRows(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	paths := testPaths(t)
	provider := fakeProvider{snapshot: photos.LibrarySnapshot{Provider: "fake", Assets: []photos.Asset{{
		LocalIdentifier: "deleted-fixture", MediaType: "image", CreationDate: "2026-05-27T10:00:00Z",
		DeletedAt: "2026-05-28T10:00:00Z", DeletionReason: "explicit_fixture_deletion",
	}}}}
	if _, err := Crawl(ctx, paths, CrawlOptions{LibraryPath: t.TempDir(), Provider: provider}); err != nil {
		t.Fatal(err)
	}
	db, err := openArchiveStore(ctx, paths.Database)
	if err != nil {
		t.Fatal(err)
	}
	// Tombstones outlive the derived classification queue.
	execTestSQL(t, db.DB(), "delete from classification_queue")
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	result, err := Timeline(ctx, paths, TimelineOptions{From: "2026-01-01T00:00:00Z", To: "2027-01-01T00:00:00Z", IncludeUnlocated: true})
	if err != nil || result.Count != 0 {
		t.Fatalf("deleted asset returned: %#v, %v", result, err)
	}
}

func TestObservationTruncationPreservesUTF8(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		limit    int
		truncate func(string) string
	}{{"observation", 500, truncateObservationText}, {"reason", 200, truncateReason}} {
		t.Run(tc.name, func(t *testing.T) {
			prefix := strings.Repeat("a", tc.limit-1)
			got := tc.truncate(prefix + "界suffix")
			if !utf8.ValidString(got) || got != prefix {
				t.Fatalf("truncation split a UTF-8 character: %q", got)
			}
			if got := tc.truncate(" hello\n world "); got != "hello world" {
				t.Fatalf("whitespace normalization = %q", got)
			}
		})
	}
}
