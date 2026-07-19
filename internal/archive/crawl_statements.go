package archive

import (
	"context"
	"database/sql"
)

type crawlStatements struct {
	previousFingerprint *sql.Stmt
	assetLive           *sql.Stmt
	assetTombstone      *sql.Stmt
	resource            *sql.Stmt
	resourceTombstone   *sql.Stmt
	album               *sql.Stmt
	evidence            *sql.Stmt
	location            *sql.Stmt
	fts                 *sql.Stmt
	deleteFTS           *sql.Stmt
	queue               *sql.Stmt
	seen                *sql.Stmt
}

func prepareCrawlStatements(ctx context.Context, tx *sql.Tx) (*crawlStatements, error) {
	stmts := &crawlStatements{}
	prepares := []struct {
		target **sql.Stmt
		query  string
	}{
		{&stmts.previousFingerprint, `
select seen.source_fingerprint, asset.deleted_at
from crawl_seen_asset seen
join asset on asset.id = seen.asset_id
where seen.source_library_id = ? and seen.asset_id = ?
`},
		{&stmts.assetLive, `
insert into asset(id, local_identifier, media_type, media_subtypes, creation_date, modification_date, added_date, timezone_name, width, height, duration_seconds, favorite, hidden, burst_identifier, represents_burst, source_library_id, metadata_json)
values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
on conflict(id) do update set
  local_identifier = excluded.local_identifier,
  media_type = excluded.media_type,
  media_subtypes = excluded.media_subtypes,
  creation_date = excluded.creation_date,
  modification_date = excluded.modification_date,
  added_date = excluded.added_date,
  timezone_name = excluded.timezone_name,
  width = excluded.width,
  height = excluded.height,
  duration_seconds = excluded.duration_seconds,
  favorite = excluded.favorite,
  hidden = excluded.hidden,
  burst_identifier = excluded.burst_identifier,
  represents_burst = excluded.represents_burst,
  source_library_id = excluded.source_library_id,
  metadata_json = excluded.metadata_json,
  deleted_at = null,
  deletion_source = null,
  deletion_reason = null
`},
		{&stmts.assetTombstone, `
insert into asset(id, local_identifier, media_type, media_subtypes, creation_date, modification_date, added_date, timezone_name, width, height, duration_seconds, favorite, hidden, burst_identifier, represents_burst, source_library_id, metadata_json, deleted_at, deletion_source, deletion_reason)
values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
on conflict(id) do update set
  deleted_at = coalesce(asset.deleted_at, excluded.deleted_at),
  deletion_source = coalesce(nullif(asset.deletion_source, ''), excluded.deletion_source),
  deletion_reason = coalesce(nullif(asset.deletion_reason, ''), excluded.deletion_reason)
`},
		{&stmts.resource, `
insert into asset_resource(id, asset_id, source_identifier, resource_type, uti, original_filename, local_path, file_size, sha256, available_locally, needs_download)
values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
on conflict(id) do update set
  asset_id = excluded.asset_id,
  source_identifier = excluded.source_identifier,
  resource_type = excluded.resource_type,
  uti = excluded.uti,
  original_filename = excluded.original_filename,
  local_path = excluded.local_path,
  file_size = excluded.file_size,
  sha256 = excluded.sha256,
  available_locally = excluded.available_locally,
  needs_download = excluded.needs_download,
  deleted_at = null,
  deletion_source = null,
  deletion_reason = null
`},
		{&stmts.resourceTombstone, `
insert into asset_resource(id, asset_id, source_identifier, resource_type, uti, original_filename, local_path, file_size, sha256, available_locally, needs_download, deleted_at, deletion_source, deletion_reason)
values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'parent_asset_deleted')
on conflict(id) do update set
  source_identifier = coalesce(nullif(asset_resource.source_identifier, ''), excluded.source_identifier),
  deleted_at = coalesce(asset_resource.deleted_at, excluded.deleted_at),
  deletion_source = coalesce(nullif(asset_resource.deletion_source, ''), excluded.deletion_source),
  deletion_reason = coalesce(nullif(asset_resource.deletion_reason, ''), excluded.deletion_reason)
`},
		{&stmts.album, `
insert into album_membership(id, asset_id, album_id, album_title, album_kind)
values (?, ?, ?, ?, ?)
on conflict(id) do update set
  asset_id = excluded.asset_id,
  album_id = excluded.album_id,
  album_title = excluded.album_title,
  album_kind = excluded.album_kind
`},
		{&stmts.evidence, `
insert into evidence_ref(id, asset_id, evidence_kind, source, pointer, value_json)
values (?, ?, ?, ?, ?, ?)
on conflict(id) do update set
  asset_id = excluded.asset_id,
  evidence_kind = excluded.evidence_kind,
  source = excluded.source,
  pointer = excluded.pointer,
  value_json = excluded.value_json
`},
		{&stmts.location, `
insert into location_observation(id, asset_id, latitude, longitude, altitude, horizontal_accuracy, source, evidence_id)
values (?, ?, ?, ?, ?, ?, ?, ?)
on conflict(id) do update set
  asset_id = excluded.asset_id,
  latitude = excluded.latitude,
  longitude = excluded.longitude,
  altitude = excluded.altitude,
  horizontal_accuracy = excluded.horizontal_accuracy,
  source = excluded.source,
  evidence_id = excluded.evidence_id
`},
		{&stmts.fts, `insert into asset_fts(id, title, body) values (?, ?, ?)`},
		{&stmts.deleteFTS, `delete from asset_fts where id = ?`},
		{&stmts.queue, `
insert into classification_queue(id, asset_id, source_library_id, state, reason, needs_download, updated_at)
values (?, ?, ?, ?, ?, ?, ?)
on conflict(asset_id) do update set
  source_library_id = excluded.source_library_id,
  state = excluded.state,
  reason = excluded.reason,
  needs_download = excluded.needs_download,
  updated_at = excluded.updated_at
`},
		{&stmts.seen, `
insert into crawl_seen_asset(source_library_id, asset_id, first_seen_snapshot_id, last_seen_snapshot_id, source_fingerprint, last_seen_at)
values (?, ?, ?, ?, ?, ?)
on conflict(source_library_id, asset_id) do update set
  last_seen_snapshot_id = excluded.last_seen_snapshot_id,
  source_fingerprint = excluded.source_fingerprint,
  last_seen_at = excluded.last_seen_at
`},
	}
	for _, prepare := range prepares {
		stmt, err := tx.PrepareContext(ctx, prepare.query)
		if err != nil {
			stmts.close()
			return nil, err
		}
		*prepare.target = stmt
	}
	return stmts, nil
}

func (s *crawlStatements) close() {
	if s == nil {
		return
	}
	for _, stmt := range []*sql.Stmt{s.previousFingerprint, s.assetLive, s.assetTombstone, s.resource, s.resourceTombstone, s.album, s.evidence, s.location, s.fts, s.deleteFTS, s.queue, s.seen} {
		if stmt != nil {
			_ = stmt.Close()
		}
	}
}
