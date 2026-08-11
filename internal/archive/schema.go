package archive

const SchemaVersion = 2

const Schema = `
create table if not exists source_library (
  id text primary key,
  library_path text not null,
  snapshot_path text not null,
  snapshot_created_at text not null,
  photos_version text not null,
  metadata_json text not null
);

create table if not exists crawl_snapshot (
  id text primary key,
  source_library_id text not null references source_library(id),
  started_at text not null,
  completed_at text not null,
  provider text not null,
  asset_count integer not null,
  resource_count integer not null,
  album_membership_count integer not null,
  location_count integer not null,
  metadata_json text not null
);

create table if not exists crawl_seen_asset (
  source_library_id text not null references source_library(id),
  asset_id text not null,
  first_seen_snapshot_id text not null references crawl_snapshot(id),
  last_seen_snapshot_id text not null references crawl_snapshot(id),
  source_fingerprint text not null,
  last_seen_at text not null,
  primary key (source_library_id, asset_id)
);

create table if not exists sync_state (
  source text not null,
  entity_type text not null,
  entity_id text not null,
  cursor text not null,
  synced_at text not null,
  primary key (source, entity_type, entity_id)
);

create table if not exists classification_queue (
  id text primary key,
  asset_id text not null unique references asset(id),
  source_library_id text not null references source_library(id),
  state text not null,
  reason text not null,
  needs_download integer not null,
  updated_at text not null
);

create table if not exists asset (
  id text primary key,
  local_identifier text not null unique,
  media_type text not null,
  media_subtypes text not null,
  creation_date text not null,
  modification_date text not null,
  added_date text not null,
  timezone_name text not null,
  width integer not null,
  height integer not null,
  duration_seconds real not null,
  favorite integer not null,
  hidden integer not null,
  burst_identifier text not null,
  represents_burst integer not null,
  source_library_id text not null references source_library(id),
  metadata_json text not null,
  deleted_at text,
  deletion_source text,
  deletion_reason text
);

create table if not exists asset_resource (
  id text primary key,
  asset_id text not null references asset(id),
  source_identifier text not null,
  resource_type text not null,
  uti text not null,
  original_filename text not null,
  local_path text not null,
  file_size integer not null,
  sha256 text not null,
  available_locally integer not null,
  needs_download integer not null,
  deleted_at text,
  deletion_source text,
  deletion_reason text
);

create table if not exists album_membership (
  id text primary key,
  asset_id text not null references asset(id),
  album_id text not null,
  album_title text not null,
  album_kind text not null
);

create table if not exists location_observation (
  id text primary key,
  asset_id text not null references asset(id),
  latitude real not null,
  longitude real not null,
  altitude real,
  horizontal_accuracy real,
  source text not null,
  evidence_id text not null
);

create table if not exists visual_observation (
  id text primary key,
  asset_id text not null references asset(id),
  observation_type text not null,
  label text not null,
  confidence real not null,
  bounding_box_json text not null,
  source text not null,
  model_id text not null,
  evidence_id text not null
);

create table if not exists text_observation (
  id text primary key,
  asset_id text not null references asset(id),
  text text not null,
  confidence real not null,
  bounding_box_json text not null,
  language text not null,
  source text not null,
  evidence_id text not null
);

create table if not exists face_observation (
  id text primary key,
  asset_id text not null references asset(id),
  face_local_id text not null,
  person_label text not null,
  confidence real not null,
  bounding_box_json text not null,
  source text not null,
  evidence_id text not null
);

create table if not exists model_run (
  id text primary key,
  source text not null,
  model_id text not null,
  prompt_version text not null,
  started_at text not null,
  completed_at text not null,
  input_count integer not null,
  metadata_json text not null
);

create table if not exists model_observation (
  id text primary key,
  asset_id text not null references asset(id),
  observation_type text not null,
  value_text text not null,
  value_json text not null,
  confidence real not null,
  source text not null,
  model_id text not null,
  prompt_version text not null,
  evidence_id text not null
);

create table if not exists observation_term (
  id text primary key,
  asset_id text not null references asset(id),
  observation_id text not null,
  term text not null,
  term_type text not null,
  source text not null,
  model_id text not null
);

create table if not exists evidence_ref (
  id text primary key,
  asset_id text,
  evidence_kind text not null,
  source text not null,
  pointer text not null,
  value_json text not null
);

create table if not exists edge (
  id text primary key,
  edge_type text not null,
  from_id text not null,
  to_id text not null,
  method text not null,
  confidence real not null,
  reason_json text not null,
  evidence_id text not null
);

create virtual table if not exists asset_fts using fts5(id unindexed, title, body);
create virtual table if not exists observation_fts using fts5(id unindexed, asset_id unindexed, title, body);

create index if not exists asset_creation_idx on asset(creation_date);
create index if not exists asset_burst_idx on asset(burst_identifier);
create index if not exists crawl_snapshot_source_idx on crawl_snapshot(source_library_id, completed_at desc);
create index if not exists crawl_seen_asset_snapshot_idx on crawl_seen_asset(last_seen_snapshot_id);
create index if not exists sync_state_synced_at_idx on sync_state(synced_at desc);
create index if not exists classification_queue_state_idx on classification_queue(state, needs_download);
create index if not exists resource_asset_idx on asset_resource(asset_id);
create index if not exists resource_sha_idx on asset_resource(sha256);
create index if not exists album_asset_idx on album_membership(asset_id);
create index if not exists location_asset_idx on location_observation(asset_id);
create index if not exists visual_asset_idx on visual_observation(asset_id);
create index if not exists text_asset_idx on text_observation(asset_id);
create index if not exists face_asset_idx on face_observation(asset_id);
create index if not exists model_observation_asset_idx on model_observation(asset_id);
create index if not exists model_observation_type_idx on model_observation(observation_type);
create index if not exists observation_term_asset_idx on observation_term(asset_id);
create index if not exists observation_term_term_idx on observation_term(term);
create index if not exists edge_from_idx on edge(from_id);
create index if not exists edge_to_idx on edge(to_id);
`
