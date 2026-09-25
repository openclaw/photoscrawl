package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

const (
	appleRecordFace     = "face"
	appleRecordSearch   = "search"
	appleRecordMetadata = "metadata"
)

type stagedAppleRecord interface {
	recordKey() (assetID, evidenceID, primaryID, secondaryID string)
	stableRecord() any
	payloadRecord() any
}

type stagedFaceRecord struct {
	AssetID            string   `json:"asset_id"`
	EvidenceID         string   `json:"evidence_id"`
	EvidencePointer    string   `json:"evidence_pointer"`
	EvidenceJSON       string   `json:"evidence_json"`
	EvidenceStableJSON string   `json:"evidence_stable_json"`
	ObservationID      string   `json:"observation_id"`
	FaceLocalID        string   `json:"face_local_id"`
	PersonLabel        string   `json:"person_label"`
	PersonUUID         string   `json:"person_uuid"`
	PersonKind         string   `json:"person_kind"`
	Confidence         float64  `json:"confidence"`
	Quality            *float64 `json:"quality"`
	BlurScore          *float64 `json:"blur_score"`
	EyesClosed         *int64   `json:"eyes_closed"`
	Smile              *int64   `json:"smile"`
	BoundingBoxJSON    string   `json:"bounding_box_json"`
	FTSTitle           string   `json:"fts_title"`
	FTSBody            string   `json:"fts_body"`
}

func (r stagedFaceRecord) recordKey() (string, string, string, string) {
	return r.AssetID, r.EvidenceID, r.ObservationID, ""
}

func (r stagedFaceRecord) stableRecord() any {
	r.EvidenceJSON = ""
	return r
}

func (r stagedFaceRecord) payloadRecord() any {
	r.EvidenceStableJSON = ""
	return r
}

type stagedSearchRecord struct {
	AssetID            string   `json:"asset_id"`
	EvidenceID         string   `json:"evidence_id"`
	EvidencePointer    string   `json:"evidence_pointer"`
	EvidenceJSON       string   `json:"evidence_json"`
	EvidenceStableJSON string   `json:"evidence_stable_json"`
	VisualID           string   `json:"visual_id"`
	ObservationType    string   `json:"observation_type"`
	Label              string   `json:"label"`
	Confidence         float64  `json:"confidence"`
	VisualFTSTitle     string   `json:"visual_fts_title"`
	VisualFTSBody      string   `json:"visual_fts_body"`
	FaceID             string   `json:"face_id"`
	FaceLocalID        string   `json:"face_local_id"`
	FaceLabel          string   `json:"face_label"`
	FaceConfidence     *float64 `json:"face_confidence"`
	FaceFTSTitle       string   `json:"face_fts_title"`
	FaceFTSBody        string   `json:"face_fts_body"`
}

func (r stagedSearchRecord) recordKey() (string, string, string, string) {
	return r.AssetID, r.EvidenceID, r.VisualID, r.FaceID
}

func (r stagedSearchRecord) stableRecord() any {
	r.EvidenceJSON = ""
	return r
}

func (r stagedSearchRecord) payloadRecord() any {
	r.EvidenceStableJSON = ""
	return r
}

type stagedMetadataRecord struct {
	AssetID            string  `json:"asset_id"`
	EvidenceID         string  `json:"evidence_id"`
	EvidencePointer    string  `json:"evidence_pointer"`
	EvidenceJSON       string  `json:"evidence_json"`
	EvidenceStableJSON string  `json:"evidence_stable_json"`
	ObservationID      string  `json:"observation_id"`
	ObservationType    string  `json:"observation_type"`
	ValueText          string  `json:"value_text"`
	ValueJSON          string  `json:"value_json"`
	Confidence         float64 `json:"confidence"`
	PromptVersion      string  `json:"prompt_version"`
	FTSTitle           string  `json:"fts_title"`
	FTSBody            string  `json:"fts_body"`
}

func (r stagedMetadataRecord) recordKey() (string, string, string, string) {
	return r.AssetID, r.EvidenceID, r.ObservationID, ""
}

func (r stagedMetadataRecord) stableRecord() any {
	r.EvidenceJSON = ""
	return r
}

func (r stagedMetadataRecord) payloadRecord() any {
	r.EvidenceStableJSON = ""
	return r
}

type appleStage struct {
	tx     *sql.Tx
	kind   string
	insert *sql.Stmt
}

func beginAppleStage(ctx context.Context, tx *sql.Tx, kind string) (*appleStage, error) {
	for _, statement := range []string{
		`create temp table if not exists apple_import_stage (kind text not null, evidence_id text not null, asset_id text not null, primary_id text not null, secondary_id text not null, fingerprint text not null, payload_json text not null, primary key(kind, evidence_id)) without rowid`,
		`create temp table if not exists apple_import_current (kind text not null, evidence_id text not null, asset_id text not null, primary_id text not null, secondary_id text not null, fingerprint text not null, primary key(kind, evidence_id)) without rowid`,
		`create temp table if not exists apple_import_changed (kind text not null, evidence_id text not null, primary key(kind, evidence_id)) without rowid`,
		`create temp table if not exists apple_import_fts_refresh (id text primary key) without rowid`,
		`create temp table if not exists apple_import_current_fts (id text primary key, title text not null, body text not null) without rowid`,
		`delete from temp.apple_import_stage`,
		`delete from temp.apple_import_current`,
		`delete from temp.apple_import_changed`,
		`delete from temp.apple_import_fts_refresh`,
		`delete from temp.apple_import_current_fts`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return nil, fmt.Errorf("prepare Apple import stage: %w", err)
		}
	}
	insert, err := tx.PrepareContext(ctx, `insert into temp.apple_import_stage(kind, evidence_id, asset_id, primary_id, secondary_id, fingerprint, payload_json) values (?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return nil, fmt.Errorf("prepare Apple stage insert: %w", err)
	}
	return &appleStage{tx: tx, kind: kind, insert: insert}, nil
}

func (s *appleStage) add(ctx context.Context, record stagedAppleRecord) error {
	assetID, evidenceID, primaryID, secondaryID := record.recordKey()
	payload, err := json.Marshal(record.payloadRecord())
	if err != nil {
		return err
	}
	stable, err := json.Marshal(record.stableRecord())
	if err != nil {
		return err
	}
	if _, err := s.insert.ExecContext(ctx, s.kind, evidenceID, assetID, primaryID, secondaryID, stableID("apple_import_content", string(stable)), string(payload)); err != nil {
		return fmt.Errorf("stage Apple %s record: %w", s.kind, err)
	}
	return nil
}

func (s *appleStage) close() error {
	if s.insert == nil {
		return nil
	}
	err := s.insert.Close()
	s.insert = nil
	return err
}

func (s *appleStage) apply(ctx context.Context, libraryID string) error {
	if err := s.close(); err != nil {
		return err
	}
	if err := loadCurrentAppleRecords(ctx, s.tx, s.kind, libraryID); err != nil {
		return err
	}
	if _, err := s.tx.ExecContext(ctx, `
insert into temp.apple_import_changed(kind, evidence_id)
select s.kind, s.evidence_id
from temp.apple_import_stage s
left join temp.apple_import_current c on c.kind = s.kind and c.evidence_id = s.evidence_id
where c.evidence_id is null or c.fingerprint <> s.fingerprint
union all
select c.kind, c.evidence_id
from temp.apple_import_current c
left join temp.apple_import_stage s on s.kind = c.kind and s.evidence_id = c.evidence_id
where s.evidence_id is null
`); err != nil {
		return fmt.Errorf("find changed Apple %s records: %w", s.kind, err)
	}
	if _, err := s.tx.ExecContext(ctx, `
insert or ignore into temp.apple_import_fts_refresh(id)
select primary_id from temp.apple_import_stage s join temp.apple_import_changed c using(kind, evidence_id) where primary_id <> ''
union
select secondary_id from temp.apple_import_stage s join temp.apple_import_changed c using(kind, evidence_id) where secondary_id <> ''
union
select primary_id from temp.apple_import_current s join temp.apple_import_changed c using(kind, evidence_id) where primary_id <> ''
union
select secondary_id from temp.apple_import_current s join temp.apple_import_changed c using(kind, evidence_id) where secondary_id <> ''
`); err != nil {
		return fmt.Errorf("collect Apple %s FTS refresh rows: %w", s.kind, err)
	}
	if _, err := s.tx.ExecContext(ctx, `
delete from observation_fts
where rowid in (
  select mapped.fts_rowid
  from observation_fts_rowid mapped
  join temp.apple_import_fts_refresh refresh on refresh.id = mapped.observation_id
)
`); err != nil {
		return fmt.Errorf("clear changed Apple %s FTS rows: %w", s.kind, err)
	}
	if _, err := s.tx.ExecContext(ctx, `delete from observation_fts_rowid where observation_id in (select id from temp.apple_import_fts_refresh)`); err != nil {
		return fmt.Errorf("clear changed Apple %s FTS mappings: %w", s.kind, err)
	}
	if _, err := s.tx.ExecContext(ctx, `delete from observation_term where observation_id in (select id from temp.apple_import_fts_refresh)`); err != nil {
		return fmt.Errorf("clear changed Apple %s terms: %w", s.kind, err)
	}
	changed := `select evidence_id from temp.apple_import_changed where kind = ?`
	switch s.kind {
	case appleRecordFace:
		if _, err := s.tx.ExecContext(ctx, `delete from face_observation where source = ? and evidence_id in (`+changed+`)`, photosLibraryDBFaceSource, s.kind); err != nil {
			return fmt.Errorf("clear changed imported faces: %w", err)
		}
	case appleRecordSearch:
		if _, err := s.tx.ExecContext(ctx, `delete from visual_observation where source = ? and evidence_id in (`+changed+`)`, photosSearchIndexSource, s.kind); err != nil {
			return fmt.Errorf("clear changed Apple search observations: %w", err)
		}
		if _, err := s.tx.ExecContext(ctx, `delete from face_observation where source = ? and evidence_id in (`+changed+`)`, photosSearchIndexSource, s.kind); err != nil {
			return fmt.Errorf("clear changed Apple search faces: %w", err)
		}
	case appleRecordMetadata:
		if _, err := s.tx.ExecContext(ctx, `delete from model_observation where source = ? and model_id = ? and evidence_id in (`+changed+`)`, photosLibraryDBFaceSource, photosLibraryDBMetadataModelID, s.kind); err != nil {
			return fmt.Errorf("clear changed Apple metadata observations: %w", err)
		}
	default:
		return fmt.Errorf("unsupported Apple record kind %q", s.kind)
	}
	if _, err := s.tx.ExecContext(ctx, `delete from evidence_ref where id in (`+changed+`)`, s.kind); err != nil {
		return fmt.Errorf("clear changed Apple %s evidence: %w", s.kind, err)
	}
	return insertChangedAppleRecords(ctx, s.tx, s.kind)
}

func stageCurrentAppleRecord(ctx context.Context, stmt *sql.Stmt, kind string, record stagedAppleRecord) error {
	assetID, evidenceID, primaryID, secondaryID := record.recordKey()
	stable, err := json.Marshal(record.stableRecord())
	if err != nil {
		return err
	}
	_, err = stmt.ExecContext(ctx, kind, evidenceID, assetID, primaryID, secondaryID, stableID("apple_import_content", string(stable)))
	return err
}

func floatPointer(value sql.NullFloat64) *float64 {
	if !value.Valid {
		return nil
	}
	return &value.Float64
}

func intPointer(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	return &value.Int64
}

func pointerInt(value any) *int64 {
	switch typed := value.(type) {
	case int:
		v := int64(typed)
		return &v
	case int64:
		return &typed
	case nil:
		return nil
	default:
		return nil
	}
}

func loadCurrentAppleRecords(ctx context.Context, tx *sql.Tx, kind, libraryID string) error {
	if _, err := tx.ExecContext(ctx, `
insert or replace into temp.apple_import_current_fts(id, title, body)
select id, title, body
from observation_fts
where asset_id in (select id from asset where source_library_id = ?)
`, libraryID); err != nil {
		return fmt.Errorf("stage current Apple FTS rows: %w", err)
	}
	insert, err := tx.PrepareContext(ctx, `insert into temp.apple_import_current(kind, evidence_id, asset_id, primary_id, secondary_id, fingerprint) values (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare current Apple record stage: %w", err)
	}
	defer insert.Close()
	switch kind {
	case appleRecordFace:
		rows, err := tx.QueryContext(ctx, `
select f.asset_id, f.evidence_id, e.pointer, json_remove(e.value_json, '$.imported_at'),
       f.id, f.face_local_id, f.person_label, coalesce(f.person_uuid, ''), coalesce(f.person_kind, ''),
       f.confidence, f.quality, f.blur_score, f.eyes_closed, f.smile, f.bounding_box_json,
       coalesce(o.title, ''), coalesce(o.body, '')
from face_observation f
left join evidence_ref e on e.id = f.evidence_id
left join temp.apple_import_current_fts o on o.id = f.id
where f.source = ? and f.asset_id in (select id from asset where source_library_id = ?)
`, photosLibraryDBFaceSource, libraryID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r stagedFaceRecord
			var quality, blur sql.NullFloat64
			var eyes, smile sql.NullInt64
			if err := rows.Scan(&r.AssetID, &r.EvidenceID, &r.EvidencePointer, &r.EvidenceStableJSON, &r.ObservationID, &r.FaceLocalID, &r.PersonLabel, &r.PersonUUID, &r.PersonKind, &r.Confidence, &quality, &blur, &eyes, &smile, &r.BoundingBoxJSON, &r.FTSTitle, &r.FTSBody); err != nil {
				return err
			}
			r.Quality, r.BlurScore, r.EyesClosed, r.Smile = floatPointer(quality), floatPointer(blur), intPointer(eyes), intPointer(smile)
			if err := stageCurrentAppleRecord(ctx, insert, kind, r); err != nil {
				return err
			}
		}
		return rows.Err()
	case appleRecordSearch:
		rows, err := tx.QueryContext(ctx, `
select v.asset_id, v.evidence_id, e.pointer, json_remove(e.value_json, '$.imported_at'),
       v.id, v.observation_type, v.label, v.confidence, coalesce(vo.title, ''), coalesce(vo.body, ''),
       coalesce(f.id, ''), coalesce(f.face_local_id, ''), coalesce(f.person_label, ''), f.confidence,
       coalesce(fo.title, ''), coalesce(fo.body, '')
from visual_observation v
left join evidence_ref e on e.id = v.evidence_id
left join temp.apple_import_current_fts vo on vo.id = v.id
left join face_observation f on f.evidence_id = v.evidence_id and f.source = ?
left join temp.apple_import_current_fts fo on fo.id = f.id
where v.source = ? and v.asset_id in (select id from asset where source_library_id = ?)
`, photosSearchIndexSource, photosSearchIndexSource, libraryID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r stagedSearchRecord
			var confidence sql.NullFloat64
			if err := rows.Scan(&r.AssetID, &r.EvidenceID, &r.EvidencePointer, &r.EvidenceStableJSON, &r.VisualID, &r.ObservationType, &r.Label, &r.Confidence, &r.VisualFTSTitle, &r.VisualFTSBody, &r.FaceID, &r.FaceLocalID, &r.FaceLabel, &confidence, &r.FaceFTSTitle, &r.FaceFTSBody); err != nil {
				return err
			}
			r.FaceConfidence = floatPointer(confidence)
			if err := stageCurrentAppleRecord(ctx, insert, kind, r); err != nil {
				return err
			}
		}
		return rows.Err()
	case appleRecordMetadata:
		rows, err := tx.QueryContext(ctx, `
select m.asset_id, m.evidence_id, e.pointer, json_remove(e.value_json, '$.imported_at'),
       m.id, m.observation_type, m.value_text, m.value_json, m.confidence, m.prompt_version,
       coalesce(o.title, ''), coalesce(o.body, '')
from model_observation m
left join evidence_ref e on e.id = m.evidence_id
left join temp.apple_import_current_fts o on o.id = m.id
where m.source = ? and m.model_id = ? and m.asset_id in (select id from asset where source_library_id = ?)
`, photosLibraryDBFaceSource, photosLibraryDBMetadataModelID, libraryID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r stagedMetadataRecord
			if err := rows.Scan(&r.AssetID, &r.EvidenceID, &r.EvidencePointer, &r.EvidenceStableJSON, &r.ObservationID, &r.ObservationType, &r.ValueText, &r.ValueJSON, &r.Confidence, &r.PromptVersion, &r.FTSTitle, &r.FTSBody); err != nil {
				return err
			}
			if err := stageCurrentAppleRecord(ctx, insert, kind, r); err != nil {
				return err
			}
		}
		return rows.Err()
	default:
		return fmt.Errorf("unsupported Apple record kind %q", kind)
	}
}

type appleInsertStatements struct {
	evidence *sql.Stmt
	face     *sql.Stmt
	visual   *sql.Stmt
	model    *sql.Stmt
	fts      *sql.Stmt
	ftsMap   *sql.Stmt
}

func prepareAppleInsertStatements(ctx context.Context, tx *sql.Tx) (*appleInsertStatements, error) {
	s := &appleInsertStatements{}
	statements := []struct {
		target **sql.Stmt
		query  string
	}{
		{&s.evidence, `insert into evidence_ref(id, asset_id, evidence_kind, source, pointer, value_json) values (?, ?, ?, ?, ?, ?)`},
		{&s.face, `insert into face_observation(id, asset_id, face_local_id, person_label, person_uuid, person_kind, confidence, quality, blur_score, eyes_closed, smile, bounding_box_json, source, evidence_id) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`},
		{&s.visual, `insert into visual_observation(id, asset_id, observation_type, label, confidence, bounding_box_json, source, model_id, evidence_id) values (?, ?, ?, ?, ?, '{}', ?, ?, ?)`},
		{&s.model, `insert into model_observation(id, asset_id, observation_type, value_text, value_json, confidence, source, model_id, prompt_version, evidence_id) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`},
		{&s.fts, `insert into observation_fts(id, asset_id, title, body) values (?, ?, ?, ?)`},
		{&s.ftsMap, `insert into observation_fts_rowid(fts_rowid, observation_id) values (last_insert_rowid(), ?)`},
	}
	for _, item := range statements {
		stmt, err := tx.PrepareContext(ctx, item.query)
		if err != nil {
			s.close()
			return nil, err
		}
		*item.target = stmt
	}
	return s, nil
}

func (s *appleInsertStatements) close() {
	for _, stmt := range []*sql.Stmt{s.evidence, s.face, s.visual, s.model, s.fts, s.ftsMap} {
		if stmt != nil {
			_ = stmt.Close()
		}
	}
}

func (s *appleInsertStatements) insertFTS(ctx context.Context, observationID, assetID, title, body string) error {
	if _, err := s.fts.ExecContext(ctx, observationID, assetID, title, body); err != nil {
		return err
	}
	if _, err := s.ftsMap.ExecContext(ctx, observationID); err != nil {
		return fmt.Errorf("map Apple observation FTS row: %w", err)
	}
	return nil
}

func insertChangedAppleRecords(ctx context.Context, tx *sql.Tx, kind string) error {
	stmts, err := prepareAppleInsertStatements(ctx, tx)
	if err != nil {
		return fmt.Errorf("prepare changed Apple inserts: %w", err)
	}
	defer stmts.close()
	rows, err := tx.QueryContext(ctx, `select s.payload_json from temp.apple_import_stage s join temp.apple_import_changed c using(kind, evidence_id) where s.kind = ? order by s.evidence_id`, kind)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			return err
		}
		switch kind {
		case appleRecordFace:
			var r stagedFaceRecord
			if err := json.Unmarshal([]byte(payload), &r); err != nil {
				return err
			}
			if _, err := stmts.evidence.ExecContext(ctx, r.EvidenceID, r.AssetID, "face_observation", photosLibraryDBFaceSource, r.EvidencePointer, r.EvidenceJSON); err != nil {
				return err
			}
			if _, err := stmts.face.ExecContext(ctx, r.ObservationID, r.AssetID, r.FaceLocalID, r.PersonLabel, r.PersonUUID, r.PersonKind, r.Confidence, r.Quality, r.BlurScore, r.EyesClosed, r.Smile, r.BoundingBoxJSON, photosLibraryDBFaceSource, r.EvidenceID); err != nil {
				return err
			}
			if err := stmts.insertFTS(ctx, r.ObservationID, r.AssetID, r.FTSTitle, r.FTSBody); err != nil {
				return err
			}
		case appleRecordSearch:
			var r stagedSearchRecord
			if err := json.Unmarshal([]byte(payload), &r); err != nil {
				return err
			}
			if _, err := stmts.evidence.ExecContext(ctx, r.EvidenceID, r.AssetID, "apple_search_index", photosSearchIndexSource, r.EvidencePointer, r.EvidenceJSON); err != nil {
				return err
			}
			if _, err := stmts.visual.ExecContext(ctx, r.VisualID, r.AssetID, r.ObservationType, r.Label, r.Confidence, photosSearchIndexSource, photosSearchIndexModelID, r.EvidenceID); err != nil {
				return err
			}
			if err := stmts.insertFTS(ctx, r.VisualID, r.AssetID, r.VisualFTSTitle, r.VisualFTSBody); err != nil {
				return err
			}
			if r.FaceID != "" {
				if _, err := stmts.face.ExecContext(ctx, r.FaceID, r.AssetID, r.FaceLocalID, r.FaceLabel, nil, nil, r.FaceConfidence, nil, nil, nil, nil, "{}", photosSearchIndexSource, r.EvidenceID); err != nil {
					return err
				}
				if err := stmts.insertFTS(ctx, r.FaceID, r.AssetID, r.FaceFTSTitle, r.FaceFTSBody); err != nil {
					return err
				}
			}
		case appleRecordMetadata:
			var r stagedMetadataRecord
			if err := json.Unmarshal([]byte(payload), &r); err != nil {
				return err
			}
			if _, err := stmts.evidence.ExecContext(ctx, r.EvidenceID, r.AssetID, "apple_photo_metadata", photosLibraryDBFaceSource, r.EvidencePointer, r.EvidenceJSON); err != nil {
				return err
			}
			if _, err := stmts.model.ExecContext(ctx, r.ObservationID, r.AssetID, r.ObservationType, r.ValueText, r.ValueJSON, r.Confidence, photosLibraryDBFaceSource, photosLibraryDBMetadataModelID, r.PromptVersion, r.EvidenceID); err != nil {
				return err
			}
			if err := stmts.insertFTS(ctx, r.ObservationID, r.AssetID, r.FTSTitle, r.FTSBody); err != nil {
				return err
			}
		}
	}
	return rows.Err()
}
