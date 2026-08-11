package archive

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/openclaw/crawlkit/control"
	"github.com/openclaw/crawlkit/store"
)

type InitResult struct {
	Database string `json:"database"`
}

func Init(ctx context.Context, paths Paths) (InitResult, error) {
	if err := os.MkdirAll(filepath.Dir(paths.Database), 0o700); err != nil {
		return InitResult{}, err
	}
	db, err := openArchiveStore(ctx, paths.Database)
	if err != nil {
		return InitResult{}, err
	}
	defer db.Close()
	return InitResult{Database: paths.Database}, nil
}

func Status(ctx context.Context, paths Paths) (control.Status, error) {
	status := control.NewStatus("photoscrawl", "photos archive")
	status.DatabasePath = paths.Database
	if info, err := os.Stat(paths.Database); err == nil {
		status.State = "ready"
		status.DatabaseBytes = info.Size()
		counts, summary, lastImportAt, warnings, err := counts(ctx, paths.Database)
		if err != nil {
			return control.Status{}, err
		}
		status.Counts = counts
		status.Summary = summary
		status.LastImportAt = lastImportAt
		status.Warnings = warnings
		status.Databases = []control.Database{
			control.SQLiteDatabase("photos", "photos.sqlite", "primary", paths.Database, true, counts),
		}
		return status, nil
	}
	status.State = "missing"
	status.Summary = "photos.sqlite has not been initialized"
	return status, nil
}

func counts(ctx context.Context, dbPath string) ([]control.Count, string, string, []string, error) {
	db, err := openArchiveReadOnly(ctx, dbPath)
	if err != nil {
		return nil, "", "", nil, err
	}
	defer db.Close()
	tables := []string{
		"source_library", "crawl_snapshot", "crawl_seen_asset", "sync_state",
		"classification_queue", "asset", "asset_resource", "album_membership",
		"location_observation", "visual_observation", "text_observation",
		"face_observation", "model_run", "model_observation",
		"observation_term", "evidence_ref", "edge",
	}
	out := make([]control.Count, 0, len(tables))
	for _, table := range tables {
		var n int64
		if err := db.DB().QueryRowContext(ctx, "select count(*) from "+store.QuoteIdent(table)).Scan(&n); err != nil {
			return nil, "", "", nil, err
		}
		out = append(out, control.NewCount(table, table, n))
	}
	useful, err := usefulCounts(ctx, db.DB())
	if err != nil {
		return nil, "", "", nil, err
	}
	out = append(out, useful...)
	summary, err := statusSummary(ctx, db.DB())
	if err != nil {
		return nil, "", "", nil, err
	}
	lastImportAt, err := lastImportAt(ctx, db.DB())
	if err != nil {
		return nil, "", "", nil, err
	}
	warnings, err := statusWarnings(ctx, db.DB())
	if err != nil {
		return nil, "", "", nil, err
	}
	return out, summary, lastImportAt, warnings, nil
}

func usefulCounts(ctx context.Context, db *sql.DB) ([]control.Count, error) {
	out := []control.Count{}
	groupQueries := []struct {
		prefix string
		label  string
		query  string
	}{
		{"asset.media_type", "assets media type", `select media_type, count(*) from asset where deleted_at is null group by media_type order by count(*) desc, media_type`},
		{"resource.availability", "resources availability", `
select case
  when available_locally <> 0 then 'local'
  when needs_download <> 0 then 'remote_needs_download'
  else 'unknown'
end as availability, count(*)
from asset_resource
where deleted_at is null and asset_id in (select id from asset where deleted_at is null)
group by availability
order by count(*) desc, availability
`},
		{"classification_queue.state", "classification queue state", `select state, count(*) from classification_queue group by state order by count(*) desc, state`},
		{"observation.type", "observation type", `
select observation_type, count(*)
from visual_observation
join asset on asset.id = visual_observation.asset_id and asset.deleted_at is null
group by observation_type
order by count(*) desc, observation_type
`},
		{"model_observation.type", "model observation type", `
select observation_type, count(*)
from model_observation
join asset on asset.id = model_observation.asset_id and asset.deleted_at is null
group by observation_type
order by count(*) desc, observation_type
`},
		{"observation_term.type", "observation term type", `
select term_type, count(*)
from observation_term
join asset on asset.id = observation_term.asset_id and asset.deleted_at is null
group by term_type
order by count(*) desc, term_type
`},
	}
	for _, group := range groupQueries {
		counts, err := groupedCounts(ctx, db, group.prefix, group.label, group.query)
		if err != nil {
			return nil, err
		}
		out = append(out, counts...)
	}
	scalarQueries := []struct {
		id    string
		label string
		query string
	}{
		{"asset.deleted", "deleted assets", `select count(*) from asset where deleted_at is not null`},
		{"asset.active", "active assets", `select count(*) from asset where deleted_at is null`},
		{"resource.deleted", "deleted resources", `select count(*) from asset_resource where deleted_at is not null`},
		{"asset.with_location", "assets with location", `select count(distinct location_observation.asset_id) from location_observation join asset on asset.id = location_observation.asset_id where asset.deleted_at is null`},
		{"asset.without_location", "assets without location", `select count(*) from asset where deleted_at is null and id not in (select asset_id from location_observation)`},
		{"asset.with_observation", "assets with local observations", `select count(distinct asset_id) from (
  select asset_id from visual_observation
  union
  select asset_id from model_observation
) where asset_id in (select id from asset where deleted_at is null)`},
		{"asset.without_observation", "assets without local observations", `select count(*) from asset where deleted_at is null and id not in (
  select asset_id from visual_observation
  union
  select asset_id from model_observation
)`},
		{"asset.with_local_resource", "assets with local resource", `select count(distinct asset_resource.asset_id) from asset_resource join asset on asset.id = asset_resource.asset_id where asset_resource.deleted_at is null and asset.deleted_at is null and available_locally <> 0`},
		{"asset.needing_download", "assets needing download", `select count(distinct asset_resource.asset_id) from asset_resource join asset on asset.id = asset_resource.asset_id where asset_resource.deleted_at is null and asset.deleted_at is null and needs_download <> 0`},
	}
	for _, scalar := range scalarQueries {
		var value int64
		if err := db.QueryRowContext(ctx, scalar.query).Scan(&value); err != nil {
			return nil, fmt.Errorf("status count %s: %w", scalar.id, err)
		}
		out = append(out, control.NewCount(scalar.id, scalar.label, value))
	}
	return out, nil
}

func groupedCounts(ctx context.Context, db *sql.DB, prefix, labelPrefix, query string) ([]control.Count, error) {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("status group %s: %w", prefix, err)
	}
	defer rows.Close()
	out := []control.Count{}
	for rows.Next() {
		var key string
		var value int64
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		key = strings.TrimSpace(key)
		if key == "" {
			key = "unknown"
		}
		out = append(out, control.NewCount(prefix+"."+safeCountID(key), labelPrefix+" "+key, value))
	}
	return out, rows.Err()
}

func statusSummary(ctx context.Context, db *sql.DB) (string, error) {
	var assets, located, observations, pending int64
	if err := db.QueryRowContext(ctx, `select count(*) from asset where deleted_at is null`).Scan(&assets); err != nil {
		return "", err
	}
	if err := db.QueryRowContext(ctx, `select count(distinct location_observation.asset_id) from location_observation join asset on asset.id = location_observation.asset_id where asset.deleted_at is null`).Scan(&located); err != nil {
		return "", err
	}
	if err := db.QueryRowContext(ctx, `select count(distinct asset_id) from (
  select asset_id from visual_observation
  union
  select asset_id from model_observation
) where asset_id in (select id from asset where deleted_at is null)`).Scan(&observations); err != nil {
		return "", err
	}
	if err := db.QueryRowContext(ctx, `select count(*) from classification_queue where state = 'pending'`).Scan(&pending); err != nil {
		return "", err
	}
	if assets == 0 {
		return "photos.sqlite is initialized but has no active crawled assets", nil
	}
	return fmt.Sprintf("%d assets; %d with raw GPS; %d with local observations; %d pending classification", assets, located, observations, pending), nil
}

func lastImportAt(ctx context.Context, db *sql.DB) (string, error) {
	var last sql.NullString
	if err := db.QueryRowContext(ctx, `select max(completed_at) from crawl_snapshot`).Scan(&last); err != nil {
		return "", err
	}
	if last.Valid {
		return last.String, nil
	}
	return "", nil
}

func statusWarnings(ctx context.Context, db *sql.DB) ([]string, error) {
	var pending int64
	if err := db.QueryRowContext(ctx, `select count(*) from classification_queue where state = 'pending'`).Scan(&pending); err != nil {
		return nil, err
	}
	warnings := []string{}
	if pending > 0 {
		warnings = append(warnings, "classification queue has pending assets")
	}
	return warnings, nil
}

func safeCountID(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			builder.WriteRune(r)
		case r >= '0' && r <= '9':
			builder.WriteRune(r)
		default:
			builder.WriteByte('_')
		}
	}
	out := strings.Trim(builder.String(), "_")
	if out == "" {
		return "unknown"
	}
	return out
}
