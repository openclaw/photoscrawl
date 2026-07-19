package archive

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

type SearchOptions struct {
	Query string
	Limit int
}

type SearchResult struct {
	Query   string      `json:"query"`
	Limit   int         `json:"limit"`
	Results []SearchHit `json:"results"`
}

type SearchHit struct {
	ID            string `json:"id"`
	HitType       string `json:"hit_type"`
	ObservationID string `json:"observation_id,omitempty"`
	MediaType     string `json:"media_type"`
	CreationDate  string `json:"creation_date"`
	Title         string `json:"title"`
	Snippet       string `json:"snippet"`
}

type OpenResult struct {
	Asset              map[string]any   `json:"asset"`
	Resources          []map[string]any `json:"resources"`
	Albums             []map[string]any `json:"albums"`
	Locations          []map[string]any `json:"locations"`
	VisualObservations []map[string]any `json:"visual_observations"`
	TextObservations   []map[string]any `json:"text_observations"`
	FaceObservations   []map[string]any `json:"face_observations"`
	ModelObservations  []map[string]any `json:"model_observations"`
	ObservationTerms   []map[string]any `json:"observation_terms"`
	Edges              []map[string]any `json:"edges"`
	Evidence           []map[string]any `json:"evidence"`
}

type EvidenceResult struct {
	RowID    string           `json:"row_id"`
	Evidence []map[string]any `json:"evidence"`
}

func Search(ctx context.Context, paths Paths, opts SearchOptions) (SearchResult, error) {
	query := strings.TrimSpace(opts.Query)
	if query == "" {
		return SearchResult{}, errors.New("query is required")
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	db, err := openArchiveReadOnly(ctx, paths.Database)
	if err != nil {
		return SearchResult{}, err
	}
	defer db.Close()

	fts := ftsQuery(query)
	rows, err := db.DB().QueryContext(ctx, `
select asset.id, asset.media_type, asset.creation_date, asset_fts.title,
       snippet(asset_fts, 2, '[', ']', ' ... ', 12) as snippet
from asset_fts
join asset on asset.id = asset_fts.id
where asset_fts match ? and asset.deleted_at is null
order by rank
limit ?
`, fts, limit)
	if err != nil {
		return SearchResult{}, fmt.Errorf("search assets: %w", err)
	}
	defer rows.Close()

	result := SearchResult{Query: query, Limit: limit, Results: []SearchHit{}}
	for rows.Next() {
		var hit SearchHit
		if err := rows.Scan(&hit.ID, &hit.MediaType, &hit.CreationDate, &hit.Title, &hit.Snippet); err != nil {
			return SearchResult{}, err
		}
		hit.HitType = "asset"
		result.Results = append(result.Results, hit)
	}
	if err := rows.Err(); err != nil {
		return SearchResult{}, err
	}

	if len(result.Results) < limit {
		observationLimit := limit - len(result.Results)
		observationRows, err := db.DB().QueryContext(ctx, `
select asset.id, observation_fts.id, asset.media_type, asset.creation_date, observation_fts.title,
       snippet(observation_fts, 3, '[', ']', ' ... ', 12) as snippet
from observation_fts
join asset on asset.id = observation_fts.asset_id
where observation_fts match ? and asset.deleted_at is null
order by rank
limit ?
`, fts, observationLimit)
		if err != nil {
			return SearchResult{}, fmt.Errorf("search observations: %w", err)
		}
		defer observationRows.Close()
		for observationRows.Next() {
			var hit SearchHit
			if err := observationRows.Scan(&hit.ID, &hit.ObservationID, &hit.MediaType, &hit.CreationDate, &hit.Title, &hit.Snippet); err != nil {
				return SearchResult{}, err
			}
			hit.HitType = "observation"
			result.Results = append(result.Results, hit)
		}
		if err := observationRows.Err(); err != nil {
			return SearchResult{}, err
		}
	}
	return result, nil
}

func Open(ctx context.Context, paths Paths, rowID string) (OpenResult, error) {
	rowID = strings.TrimSpace(rowID)
	if rowID == "" {
		return OpenResult{}, errors.New("id is required")
	}
	db, err := openArchiveReadOnly(ctx, paths.Database)
	if err != nil {
		return OpenResult{}, err
	}
	defer db.Close()

	asset, err := oneRow(ctx, db.DB(), `
select id, local_identifier, media_type, media_subtypes, creation_date, modification_date, added_date,
       timezone_name, width, height, duration_seconds, favorite, hidden, burst_identifier,
       represents_burst, source_library_id, metadata_json, deleted_at, deletion_source, deletion_reason
from asset
where id = ?
`, rowID)
	if errors.Is(err, sql.ErrNoRows) {
		return OpenResult{}, fmt.Errorf("asset not found: %s", rowID)
	}
	if err != nil {
		return OpenResult{}, err
	}
	resources, err := rows(ctx, db.DB(), `
select id, source_identifier, resource_type, uti, original_filename, local_path, file_size, sha256, available_locally, needs_download,
       deleted_at, deletion_source, deletion_reason
from asset_resource
where asset_id = ?
order by resource_type, original_filename
`, rowID)
	if err != nil {
		return OpenResult{}, err
	}
	albums, err := rows(ctx, db.DB(), `
select id, album_id, album_title, album_kind
from album_membership
where asset_id = ?
order by album_title, album_id
`, rowID)
	if err != nil {
		return OpenResult{}, err
	}
	locations, err := rows(ctx, db.DB(), `
select id, latitude, longitude, altitude, horizontal_accuracy, source, evidence_id
from location_observation
where asset_id = ?
`, rowID)
	if err != nil {
		return OpenResult{}, err
	}
	visualObservations, err := rows(ctx, db.DB(), `
select id, observation_type, label, confidence, bounding_box_json, source, model_id, evidence_id
from visual_observation
where asset_id = ?
order by observation_type, confidence desc, label
`, rowID)
	if err != nil {
		return OpenResult{}, err
	}
	textObservations, err := rows(ctx, db.DB(), `
select id, text, confidence, bounding_box_json, language, source, evidence_id
from text_observation
where asset_id = ?
order by confidence desc, id
`, rowID)
	if err != nil {
		return OpenResult{}, err
	}
	faceObservations, err := rows(ctx, db.DB(), `
select id, face_local_id, person_label, confidence, bounding_box_json, source, evidence_id
from face_observation
where asset_id = ?
order by confidence desc, id
`, rowID)
	if err != nil {
		return OpenResult{}, err
	}
	modelObservations, err := rows(ctx, db.DB(), `
select id, observation_type, value_text, value_json, confidence, source, model_id, prompt_version, evidence_id
from model_observation
where asset_id = ?
order by observation_type, confidence desc, value_text
`, rowID)
	if err != nil {
		return OpenResult{}, err
	}
	observationTerms, err := rows(ctx, db.DB(), `
select id, observation_id, term, term_type, source, model_id
from observation_term
where asset_id = ?
order by term_type, term
`, rowID)
	if err != nil {
		return OpenResult{}, err
	}
	edges, err := rows(ctx, db.DB(), `
select id, edge_type, from_id, to_id, method, confidence, reason_json, evidence_id
from edge
where from_id = ? or to_id = ?
order by confidence desc, edge_type, id
`, rowID, rowID)
	if err != nil {
		return OpenResult{}, err
	}
	evidence, err := evidenceRows(ctx, db.DB(), rowID)
	if err != nil {
		return OpenResult{}, err
	}
	return OpenResult{
		Asset:              asset,
		Resources:          resources,
		Albums:             albums,
		Locations:          locations,
		VisualObservations: visualObservations,
		TextObservations:   textObservations,
		FaceObservations:   faceObservations,
		ModelObservations:  modelObservations,
		ObservationTerms:   observationTerms,
		Edges:              edges,
		Evidence:           evidence,
	}, nil
}

func Evidence(ctx context.Context, paths Paths, rowID string) (EvidenceResult, error) {
	rowID = strings.TrimSpace(rowID)
	if rowID == "" {
		return EvidenceResult{}, errors.New("row id is required")
	}
	db, err := openArchiveReadOnly(ctx, paths.Database)
	if err != nil {
		return EvidenceResult{}, err
	}
	defer db.Close()
	evidence, err := evidenceRows(ctx, db.DB(), rowID)
	if err != nil {
		return EvidenceResult{}, err
	}
	return EvidenceResult{RowID: rowID, Evidence: evidence}, nil
}

func evidenceRows(ctx context.Context, db *sql.DB, rowID string) ([]map[string]any, error) {
	return rows(ctx, db, `
select id, asset_id, evidence_kind, source, pointer, value_json
from evidence_ref
where asset_id = ? or id = ? or id in (
  select evidence_id from location_observation where id = ?
  union
  select evidence_id from visual_observation where id = ?
  union
  select evidence_id from text_observation where id = ?
  union
  select evidence_id from face_observation where id = ?
  union
  select evidence_id from model_observation where id = ?
  union
  select evidence_id from edge where id = ?
)
order by evidence_kind, id
`, rowID, rowID, rowID, rowID, rowID, rowID, rowID, rowID)
}

func oneRow(ctx context.Context, db *sql.DB, query string, args ...any) (map[string]any, error) {
	result, err := rows(ctx, db, query, args...)
	if err != nil {
		return nil, err
	}
	if len(result) == 0 {
		return nil, sql.ErrNoRows
	}
	return result[0], nil
}

func rows(ctx context.Context, db *sql.DB, query string, args ...any) ([]map[string]any, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for rows.Next() {
		values := make([]any, len(columns))
		ptrs := make([]any, len(columns))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := make(map[string]any, len(columns))
		for i, column := range columns {
			row[column] = normalizeSQLValue(values[i])
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func normalizeSQLValue(value any) any {
	switch typed := value.(type) {
	case []byte:
		return string(typed)
	default:
		return typed
	}
}

func ftsQuery(query string) string {
	terms := strings.Fields(query)
	quoted := make([]string, 0, len(terms))
	for _, term := range terms {
		term = strings.TrimSpace(term)
		if term == "" {
			continue
		}
		term = strings.ReplaceAll(term, `"`, `""`)
		quoted = append(quoted, `"`+term+`"`)
	}
	if len(quoted) == 0 {
		return `""`
	}
	return strings.Join(quoted, " AND ")
}
