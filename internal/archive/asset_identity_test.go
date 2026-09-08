package archive

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openclaw/photoscrawl/internal/photos"
)

const identityFixtureUUID = "92D69D06-27F0-4831-AEC7-C6F640F075BA"

func identityFixtureAsset(provider, identifier string) photos.Asset {
	metadata := map[string]any{"schema_source": "ZASSET"}
	if provider == "photokit" {
		metadata = map[string]any{"photokit_local_identifier": identifier}
	}
	return photos.Asset{
		LocalIdentifier: identifier, MediaType: "image", Metadata: metadata,
		Resources: []photos.Resource{{SourceIdentifier: "synthetic-original", Type: "photo", OriginalFilename: "synthetic.jpg"}},
	}
}

func identityFixtureCrawl(t *testing.T, paths Paths, library, provider string, sequence int, assets ...photos.Asset) (CrawlResult, error) {
	t.Helper()
	return Crawl(context.Background(), paths, CrawlOptions{
		LibraryPath: library,
		Provider:    fakeProvider{snapshot: photos.LibrarySnapshot{Provider: provider, Assets: assets}},
		Now:         fixedClock(fmt.Sprintf("2026-09-08T12:00:%02dZ", sequence)),
	})
}

func TestCrawlIdentityPreservesReferencesAcrossProviderCycles(t *testing.T) {
	for _, first := range []string{"photokit", "photos_sqlite_snapshot"} {
		t.Run(first, func(t *testing.T) {
			paths := testPaths(t)
			library := filepath.Join(t.TempDir(), "Synthetic.photoslibrary")
			if err := mkdirLibrary(library); err != nil {
				t.Fatal(err)
			}
			second := "photokit"
			if first == second {
				second = "photos_sqlite_snapshot"
			}
			var retainedID, resourceID, queueID, evidenceID, retainedFirstSnapshot string
			for i, provider := range []string{first, second, first, second, first} {
				identifier := identityFixtureUUID
				if provider == "photokit" {
					identifier += "/L0/001"
				}
				asset := identityFixtureAsset(provider, identifier)
				result, err := identityFixtureCrawl(t, paths, library, provider, i, asset)
				if err != nil {
					t.Fatal(err)
				}
				if result.PreviouslySeenMissing != 0 || (i > 0 && result.AssetsNew != 0) {
					t.Fatalf("provider switch duplicated/missed asset: %#v", result)
				}
				db := identityFixtureDB(t, paths)
				var id, canonical, fingerprint, firstSnapshot string
				if err := db.QueryRow(`select a.id, a.local_identifier, seen.source_fingerprint, seen.first_seen_snapshot_id
from asset a join crawl_seen_asset seen on seen.asset_id=a.id`).Scan(&id, &canonical, &fingerprint, &firstSnapshot); err != nil {
					t.Fatal(err)
				}
				wantFingerprint, err := assetFingerprint(asset)
				if err != nil || fingerprint != wantFingerprint {
					t.Fatalf("fingerprint must describe observed provider value, not retained canonical ID: %v", err)
				}
				if i == 0 {
					retainedID = id
					retainedFirstSnapshot = firstSnapshot
					if err := db.QueryRow("select id from asset_resource").Scan(&resourceID); err != nil {
						t.Fatal(err)
					}
					if err := db.QueryRow("select id from classification_queue").Scan(&queueID); err != nil {
						t.Fatal(err)
					}
					if err := db.QueryRow("select id from evidence_ref where evidence_kind='asset_metadata'").Scan(&evidenceID); err != nil {
						t.Fatal(err)
					}
					if _, err := db.Exec(`insert into text_observation values
('retained-observation', ?, 'synthetic observation', 1, '{}', 'en', 'synthetic', ?)`, id, evidenceID); err != nil {
						t.Fatal(err)
					}
				}
				if id != retainedID || firstSnapshot != retainedFirstSnapshot || (i > 0 && canonical != identityFixtureUUID+"/L0/001") {
					t.Fatalf("identity changed: id=%s canonical=%s", id, canonical)
				}
				assertCount(t, db, "select count(*) from asset", 1)
				assertCount(t, db, "select count(*) from classification_queue where id='"+queueID+"'", 1)
				assertCount(t, db, "select count(*) from asset_resource where id='"+resourceID+"' and asset_id='"+retainedID+"'", 1)
				assertCount(t, db, "select count(*) from evidence_ref where id='"+evidenceID+"' and asset_id='"+retainedID+"'", 1)
				assertCount(t, db, "select count(*) from text_observation where id='retained-observation' and asset_id='"+retainedID+"' and evidence_id='"+evidenceID+"'", 1)
				assertCount(t, db, "select count(*) from evidence_ref where evidence_kind='asset_metadata' and source='"+provider+"' and pointer='asset:"+identifier+"'", 1)
				db.Close()
			}
		})
	}
}

func TestCrawlIdentityAmbiguityRollsBackEvenWithExactCandidate(t *testing.T) {
	paths := testPaths(t)
	library := filepath.Join(t.TempDir(), "Synthetic.photoslibrary")
	if err := mkdirLibrary(library); err != nil {
		t.Fatal(err)
	}
	full := identityFixtureAsset("photokit", identityFixtureUUID+"/L0/001")
	if _, err := identityFixtureCrawl(t, paths, library, "photokit", 0, full); err != nil {
		t.Fatal(err)
	}
	// Seed the historical duplicate with the old SQL contract, not with the
	// repaired resolver, preserving its independent ID, seen row and queue.
	db := identityFixtureDB(t, paths)
	var sourceID, snapshotID string
	if err := db.QueryRow("select id, snapshot_path from source_library").Scan(&sourceID, &snapshotID); err != nil {
		t.Fatal(err)
	}
	snapshotID = strings.TrimPrefix(snapshotID, "sqlite:crawl_snapshot/")
	duplicateID := stableID("asset", sourceID, identityFixtureUUID)
	if _, err := db.Exec(`insert into asset(id,local_identifier,media_type,media_subtypes,creation_date,modification_date,added_date,timezone_name,width,height,duration_seconds,favorite,hidden,burst_identifier,represents_burst,source_library_id,metadata_json)
select ?,?,'image','','','','','',0,0,0,0,0,'',0,source_library_id,'{}' from asset limit 1`, duplicateID, identityFixtureUUID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`insert into crawl_snapshot
select 'legacy-sqlite', source_library_id, started_at, completed_at, 'photos_sqlite_snapshot',
asset_count, resource_count, album_membership_count, location_count, metadata_json
from crawl_snapshot where id = ?;
insert into crawl_seen_asset values (?, ?, 'legacy-sqlite', 'legacy-sqlite', 'legacy-fingerprint', '2026-09-08T12:00:00Z');
insert into evidence_ref values ('legacy-evidence', ?, 'asset_metadata', 'photos_sqlite_snapshot', ?, '{}');
insert into classification_queue values ('legacy-queue', ?, ?, 'pending', 'metadata_ingested', 0, '2026-09-08T12:00:00Z')`,
		snapshotID, sourceID, duplicateID, duplicateID, "asset:"+identityFixtureUUID, duplicateID, sourceID); err != nil {
		t.Fatal(err)
	}
	db.Close()
	for i, provider := range []string{"photokit", "photos_sqlite_snapshot"} {
		identifier := identityFixtureUUID
		if provider == "photokit" {
			identifier += "/L0/001"
		}
		before := identityFixtureState(t, paths)
		_, err := identityFixtureCrawl(t, paths, library, provider, i+1, identityFixtureAsset(provider, identifier))
		if err == nil || !strings.Contains(err.Error(), "ambiguous asset identity") {
			t.Fatalf("exact+alternate ambiguity = %v", err)
		}
		if after := identityFixtureState(t, paths); after != before {
			t.Fatal("ambiguous crawl changed archive state")
		}
	}
}

func TestCrawlIdentityRequiresDurableProviderEvidence(t *testing.T) {
	for _, corruption := range []string{"missing-evidence", "missing-seen", "conflicting-evidence", "wrong-library-seen", "incoming-missing", "incoming-conflicting"} {
		t.Run(corruption, func(t *testing.T) {
			paths := testPaths(t)
			library := filepath.Join(t.TempDir(), "Synthetic.photoslibrary")
			if err := mkdirLibrary(library); err != nil {
				t.Fatal(err)
			}
			asset := identityFixtureAsset("photokit", identityFixtureUUID+"/L0/001")
			if _, err := identityFixtureCrawl(t, paths, library, "photokit", 0, asset); err != nil {
				t.Fatal(err)
			}
			db := identityFixtureDB(t, paths)
			statement := ""
			switch corruption {
			case "missing-evidence":
				statement = "delete from evidence_ref where evidence_kind='asset_metadata'"
			case "missing-seen":
				statement = "delete from crawl_seen_asset"
			case "conflicting-evidence":
				statement = "update evidence_ref set pointer='asset:AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE/L0/001' where evidence_kind='asset_metadata'"
			case "wrong-library-seen":
				statement = "update crawl_snapshot set source_library_id='unrelated-library'"
			}
			if statement != "" {
				if _, err := db.Exec(statement); err != nil {
					t.Fatal(err)
				}
			}
			db.Close()
			incoming := identityFixtureAsset("photos_sqlite_snapshot", identityFixtureUUID)
			if corruption == "incoming-missing" {
				incoming.Metadata = nil
			}
			if corruption == "incoming-conflicting" {
				incoming.Metadata["schema_source"] = "unrelated"
			}
			before := identityFixtureState(t, paths)
			if _, err := identityFixtureCrawl(t, paths, library, "photos_sqlite_snapshot", 1, incoming); err == nil {
				t.Fatal("unproved relationship accepted")
			}
			if after := identityFixtureState(t, paths); after != before {
				t.Fatal("unproved relationship changed archive state")
			}
		})
	}
}

func TestCrawlIdentityKeepsOpaqueAndOtherLibraryAssetsSeparate(t *testing.T) {
	paths := testPaths(t)
	first, second := filepath.Join(t.TempDir(), "First.photoslibrary"), filepath.Join(t.TempDir(), "Second.photoslibrary")
	for _, path := range []string{first, second} {
		if err := mkdirLibrary(path); err != nil {
			t.Fatal(err)
		}
	}
	for i, identifier := range []string{"opaque/a", "opaque", identityFixtureUUID + "/unsupported/tail"} {
		if _, err := identityFixtureCrawl(t, paths, first, "photokit", i, identityFixtureAsset("photokit", identifier)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := identityFixtureCrawl(t, paths, first, "photokit", 4, identityFixtureAsset("photokit", identityFixtureUUID+"/L0/001")); err != nil {
		t.Fatal(err)
	}
	if _, err := identityFixtureCrawl(t, paths, second, "photos_sqlite_snapshot", 5, identityFixtureAsset("photos_sqlite_snapshot", identityFixtureUUID)); err != nil {
		t.Fatal(err)
	}
	db := identityFixtureDB(t, paths)
	assertCount(t, db, "select count(*) from asset", 5)
	db.Close()
	// Exact cross-library uniqueness remains the existing constraint, separate
	// from same-library provider resolution; it cannot repoint the first row.
	before := identityFixtureState(t, paths)
	_, err := identityFixtureCrawl(t, paths, first, "photos_sqlite_snapshot", 6,
		identityFixtureAsset("photos_sqlite_snapshot", identityFixtureUUID))
	if err != nil {
		t.Fatal("same-library full ID should remain canonical:", err)
	}
	after := identityFixtureState(t, paths)
	if after == before {
		t.Fatal("valid same-library fallback was blanket-disabled")
	}
	third := filepath.Join(t.TempDir(), "Third.photoslibrary")
	if err := mkdirLibrary(third); err != nil {
		t.Fatal(err)
	}
	before = identityFixtureState(t, paths)
	_, err = identityFixtureCrawl(t, paths, third, "photos_sqlite_snapshot", 7, identityFixtureAsset("photos_sqlite_snapshot", identityFixtureUUID))
	if err == nil || !strings.Contains(err.Error(), "UNIQUE") {
		t.Fatalf("separate global uniqueness behavior changed: %v", err)
	}
	if identityFixtureState(t, paths) != before {
		t.Fatal("cross-library conflict was not rolled back")
	}
}

func identityFixtureDB(t *testing.T, paths Paths) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", paths.Database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func identityFixtureState(t *testing.T, paths Paths) string {
	t.Helper()
	db := identityFixtureDB(t, paths)
	defer db.Close()
	var state strings.Builder
	for _, table := range []string{"asset", "source_library", "crawl_snapshot", "crawl_seen_asset", "asset_resource", "evidence_ref", "text_observation", "classification_queue", "sync_state"} {
		rows, err := db.Query("select * from " + table + " order by 1")
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			values, pointers := make([]any, len(columns)), make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				t.Fatal(err)
			}
			fmt.Fprintf(&state, "%s:%#v\n", table, values)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
	}
	return state.String()
}
