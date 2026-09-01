package archive

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/openclaw/crawlkit/store"
)

const photosLibraryDBMetadataModelID = "apple.photos.metadata"
const photosLibraryDBMetadataVersion = "photos.sqlite.v1"

type ImportPhotoMetadataResult struct {
	Source                      string            `json:"source"`
	ModelID                     string            `json:"model_id"`
	PhotosDatabase              string            `json:"photos_database"`
	Schema                      map[string]string `json:"schema"`
	AssetsRead                  int               `json:"assets_read"`
	AssetsResolved              int               `json:"assets_resolved"`
	AssetsUnresolved            int               `json:"assets_unresolved"`
	ExifObservationsInserted    int               `json:"exif_observations_inserted"`
	QualityObservationsInserted int               `json:"quality_observations_inserted"`
	EditObservationsInserted    int               `json:"edit_observations_inserted"`
	AssetsTouched               int               `json:"assets_touched"`
}

type applePhotoMetadataRow struct {
	assetUUID       string
	exif            map[string]any
	quality         map[string]any
	adjustmentState any
	hasAdjustments  bool
}

type photoMetadataImportInput struct {
	schema map[string]string
	rows   []applePhotoMetadataRow
}

var appleExifColumns = map[string]string{
	"ZFLASHFIRED":     "flash_fired",
	"ZISO":            "iso",
	"ZMETERINGMODE":   "metering_mode",
	"ZSAMPLERATE":     "sample_rate",
	"ZTRACKFORMAT":    "track_format",
	"ZWHITEBALANCE":   "white_balance",
	"ZAPERTURE":       "aperture",
	"ZBITRATE":        "bit_rate",
	"ZDURATION":       "duration",
	"ZEXPOSUREBIAS":   "exposure_bias",
	"ZFOCALLENGTH":    "focal_length",
	"ZFPS":            "frames_per_second",
	"ZLATITUDE":       "latitude",
	"ZLONGITUDE":      "longitude",
	"ZSHUTTERSPEED":   "shutter_speed",
	"ZCAMERAMAKE":     "camera_make",
	"ZCAMERAMODEL":    "camera_model",
	"ZCODEC":          "codec",
	"ZLENSMODEL":      "lens_model",
	"ZDATECREATED":    "date_created",
	"ZTIMEZONEOFFSET": "timezone_offset",
	"ZTIMEZONENAME":   "timezone_name",
}

var appleAssetScoreColumns = map[string]string{
	"ZOVERALLAESTHETICSCORE":    "overall_aesthetic",
	"ZCURATIONSCORE":            "curation",
	"ZPROMOTIONSCORE":           "promotion",
	"ZHIGHLIGHTVISIBILITYSCORE": "highlight_visibility",
}

var appleComputedScoreColumns = map[string]string{
	"ZBEHAVIORALSCORE":              "behavioral",
	"ZFAILURESCORE":                 "failure",
	"ZHARMONIOUSCOLORSCORE":         "harmonious_color",
	"ZIMMERSIVENESSSCORE":           "immersiveness",
	"ZINTERACTIONSCORE":             "interaction",
	"ZINTERESTINGSUBJECTSCORE":      "interesting_subject",
	"ZINTRUSIVEOBJECTPRESENCESCORE": "intrusive_object_presence",
	"ZLIVELYCOLORSCORE":             "lively_color",
	"ZLOWLIGHT":                     "low_light",
	"ZNOISESCORE":                   "noise",
	"ZPLEASANTCAMERATILTSCORE":      "pleasant_camera_tilt",
	"ZPLEASANTCOMPOSITIONSCORE":     "pleasant_composition",
	"ZPLEASANTLIGHTINGSCORE":        "pleasant_lighting",
	"ZPLEASANTPATTERNSCORE":         "pleasant_pattern",
	"ZPLEASANTPERSPECTIVESCORE":     "pleasant_perspective",
	"ZPLEASANTPOSTPROCESSINGSCORE":  "pleasant_post_processing",
	"ZPLEASANTREFLECTIONSSCORE":     "pleasant_reflection",
	"ZPLEASANTSYMMETRYSCORE":        "pleasant_symmetry",
	"ZSHARPLYFOCUSEDSUBJECTSCORE":   "sharply_focused_subject",
	"ZTASTEFULLYBLURREDSCORE":       "tastefully_blurred",
	"ZWELLCHOSENSUBJECTSCORE":       "well_chosen_subject",
	"ZWELLFRAMEDSUBJECTSCORE":       "well_framed_subject",
	"ZWELLTIMEDSHOTSCORE":           "well_timed_shot",
}

func preflightPhotoMetadataImport(ctx context.Context, libraryPath string) (photoMetadataImportInput, error) {
	liveDBPath := filepath.Join(libraryPath, "database", "Photos.sqlite")
	snapshotPath, cleanup, err := copyPhotosSQLite(ctx, liveDBPath)
	if err != nil {
		return photoMetadataImportInput{}, err
	}
	defer cleanup()
	photosDB, err := store.OpenReadOnly(ctx, snapshotPath)
	if err != nil {
		return photoMetadataImportInput{}, fmt.Errorf("open copied Photos sqlite for metadata: %w", err)
	}
	defer photosDB.Close()
	return loadPhotoMetadataInput(ctx, photosDB.DB())
}

func loadPhotoMetadataInput(ctx context.Context, db *sql.DB) (photoMetadataImportInput, error) {
	assetTable, err := firstExistingTable(ctx, db, "ZASSET", "ZGENERICASSET")
	if err != nil {
		return photoMetadataImportInput{}, err
	}
	assetColumns, err := tableColumns(ctx, db, assetTable)
	if err != nil {
		return photoMetadataImportInput{}, err
	}
	if !assetColumns["Z_PK"] || !assetColumns["ZUUID"] {
		return photoMetadataImportInput{}, fmt.Errorf("unsupported Apple Photos metadata schema: %s lacks Z_PK or ZUUID", assetTable)
	}
	adjustmentColumn := "ZHASADJUSTMENTS"
	if assetColumns["ZADJUSTMENTSSTATE"] {
		adjustmentColumn = "ZADJUSTMENTSSTATE"
	} else if !assetColumns[adjustmentColumn] {
		return photoMetadataImportInput{}, fmt.Errorf("unsupported Apple Photos metadata schema: %s lacks adjustment state", assetTable)
	}
	for _, table := range []string{"ZEXTENDEDATTRIBUTES", "ZCOMPUTEDASSETATTRIBUTES"} {
		columns, err := tableColumns(ctx, db, table)
		if err != nil {
			return photoMetadataImportInput{}, err
		}
		if !columns["ZASSET"] {
			return photoMetadataImportInput{}, fmt.Errorf("unsupported Apple Photos metadata schema: %s lacks ZASSET", table)
		}
	}

	byUUID := map[string]*applePhotoMetadataRow{}
	ensure := func(uuid string) *applePhotoMetadataRow {
		uuid = normalizeAssetLocalIdentifier(uuid)
		row := byUUID[uuid]
		if row == nil {
			row = &applePhotoMetadataRow{assetUUID: uuid}
			byUUID[uuid] = row
		}
		return row
	}

	editRows, err := metadataQueryRows(ctx, db, fmt.Sprintf("select a.ZUUID, a.%s as ZEDITSTATE from %s a", store.QuoteIdent(adjustmentColumn), store.QuoteIdent(assetTable)))
	if err != nil {
		return photoMetadataImportInput{}, fmt.Errorf("read Apple edit states: %w", err)
	}
	for _, values := range editRows {
		row := ensure(stringValue(values["ZUUID"]))
		row.adjustmentState = normalizedSQLiteValue(values["ZEDITSTATE"])
		row.hasAdjustments = sqliteTruthy(values["ZEDITSTATE"])
	}

	exifRows, err := metadataQueryRows(ctx, db, fmt.Sprintf("select a.ZUUID, e.* from %s a join ZEXTENDEDATTRIBUTES e on e.ZASSET = a.Z_PK", store.QuoteIdent(assetTable)))
	if err != nil {
		return photoMetadataImportInput{}, fmt.Errorf("read Apple EXIF metadata: %w", err)
	}
	for _, values := range exifRows {
		row := ensure(stringValue(values["ZUUID"]))
		row.exif = selectMetadataValues(values, appleExifColumns)
	}

	assetScoreSelect := []string{"a.ZUUID"}
	for _, column := range sortedMappedColumns(appleAssetScoreColumns) {
		if assetColumns[column] {
			assetScoreSelect = append(assetScoreSelect, "a."+store.QuoteIdent(column))
		}
	}
	qualityRows, err := metadataQueryRows(ctx, db, fmt.Sprintf("select %s, c.* from %s a join ZCOMPUTEDASSETATTRIBUTES c on c.ZASSET = a.Z_PK", strings.Join(assetScoreSelect, ", "), store.QuoteIdent(assetTable)))
	if err != nil {
		return photoMetadataImportInput{}, fmt.Errorf("read Apple quality scores: %w", err)
	}
	qualityMap := map[string]string{}
	for key, value := range appleAssetScoreColumns {
		qualityMap[key] = value
	}
	for key, value := range appleComputedScoreColumns {
		qualityMap[key] = value
	}
	for _, values := range qualityRows {
		row := ensure(stringValue(values["ZUUID"]))
		row.quality = selectMetadataValues(values, qualityMap)
	}

	rows := make([]applePhotoMetadataRow, 0, len(byUUID))
	for uuid, row := range byUUID {
		if uuid != "" {
			rows = append(rows, *row)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].assetUUID < rows[j].assetUUID })
	return photoMetadataImportInput{
		schema: map[string]string{
			"asset_table":               assetTable,
			"extended_attributes_table": "ZEXTENDEDATTRIBUTES",
			"computed_attributes_table": "ZCOMPUTEDASSETATTRIBUTES",
			"adjustment_column":         adjustmentColumn,
		},
		rows: rows,
	}, nil
}

func writePhotoMetadataImport(ctx context.Context, tx *sql.Tx, input photoMetadataImportInput, assetByUUID map[string]string, importedAt time.Time) (ImportPhotoMetadataResult, map[string]bool, error) {
	if err := clearImportedPhotoMetadata(ctx, tx); err != nil {
		return ImportPhotoMetadataResult{}, nil, err
	}
	result := ImportPhotoMetadataResult{
		Source:         photosLibraryDBFaceSource,
		ModelID:        photosLibraryDBMetadataModelID,
		PhotosDatabase: "database/Photos.sqlite",
		Schema:         input.schema,
		AssetsRead:     len(input.rows),
	}
	touched := map[string]bool{}
	for _, row := range input.rows {
		assetID, ok := assetByUUID[row.assetUUID]
		if !ok {
			result.AssetsUnresolved++
			continue
		}
		result.AssetsResolved++
		if len(row.exif) > 0 {
			if err := insertPhotoMetadataObservation(ctx, tx, assetID, row.assetUUID, "apple_exif", row.exif, exifSummary(row.exif), "ZEXTENDEDATTRIBUTES", importedAt); err != nil {
				return ImportPhotoMetadataResult{}, nil, err
			}
			result.ExifObservationsInserted++
		}
		if len(row.quality) > 0 {
			if err := insertPhotoMetadataObservation(ctx, tx, assetID, row.assetUUID, "apple_quality_scores", row.quality, "Apple Photos quality scores", "ZCOMPUTEDASSETATTRIBUTES", importedAt); err != nil {
				return ImportPhotoMetadataResult{}, nil, err
			}
			result.QualityObservationsInserted++
		}
		edit := map[string]any{"has_adjustments": row.hasAdjustments, "adjustment_state": row.adjustmentState}
		editText := "not edited"
		if row.hasAdjustments {
			editText = "edited"
		}
		if err := insertPhotoMetadataObservation(ctx, tx, assetID, row.assetUUID, "apple_edit_state", edit, editText, input.schema["asset_table"], importedAt); err != nil {
			return ImportPhotoMetadataResult{}, nil, err
		}
		result.EditObservationsInserted++
		touched[assetID] = true
	}
	result.AssetsTouched = len(touched)
	return result, touched, nil
}

func clearImportedPhotoMetadata(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `delete from observation_fts where id in (select id from model_observation where source = ? and model_id = ?)`, photosLibraryDBFaceSource, photosLibraryDBMetadataModelID); err != nil {
		return fmt.Errorf("clear Apple photo metadata search rows: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `delete from observation_term where observation_id in (select id from model_observation where source = ? and model_id = ?)`, photosLibraryDBFaceSource, photosLibraryDBMetadataModelID); err != nil {
		return fmt.Errorf("clear Apple photo metadata terms: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `delete from model_observation where source = ? and model_id = ?`, photosLibraryDBFaceSource, photosLibraryDBMetadataModelID); err != nil {
		return fmt.Errorf("clear Apple photo metadata observations: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `delete from evidence_ref where source = ? and evidence_kind = 'apple_photo_metadata'`, photosLibraryDBFaceSource); err != nil {
		return fmt.Errorf("clear Apple photo metadata evidence: %w", err)
	}
	return nil
}

func insertPhotoMetadataObservation(ctx context.Context, tx *sql.Tx, assetID, assetUUID, observationType string, value map[string]any, valueText, table string, importedAt time.Time) error {
	valueJSON, err := jsonText(value)
	if err != nil {
		return err
	}
	observationID := stableID("model_observation", assetID, photosLibraryDBFaceSource, photosLibraryDBMetadataModelID, observationType)
	evidenceID := stableID("evidence", assetID, photosLibraryDBFaceSource, photosLibraryDBMetadataModelID, observationType)
	evidenceJSON, err := jsonText(map[string]any{
		"schema_source": "Photos.sqlite",
		"table":         table,
		"asset_uuid":    assetUUID,
		"values":        value,
		"imported_at":   importedAt.Format(time.RFC3339Nano),
		"read_only":     true,
	})
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `insert into evidence_ref(id, asset_id, evidence_kind, source, pointer, value_json) values (?, ?, 'apple_photo_metadata', ?, ?, ?)`, evidenceID, assetID, photosLibraryDBFaceSource, table+":"+assetUUID, evidenceJSON); err != nil {
		return fmt.Errorf("write Apple photo metadata evidence: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `insert into model_observation(id, asset_id, observation_type, value_text, value_json, confidence, source, model_id, prompt_version, evidence_id) values (?, ?, ?, ?, ?, 1, ?, ?, ?, ?)`, observationID, assetID, observationType, valueText, valueJSON, photosLibraryDBFaceSource, photosLibraryDBMetadataModelID, photosLibraryDBMetadataVersion, evidenceID); err != nil {
		return fmt.Errorf("write Apple photo metadata observation: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `insert into observation_fts(id, asset_id, title, body) values (?, ?, ?, ?)`, observationID, assetID, valueText, metadataSearchText(observationType, valueText, value)); err != nil {
		return fmt.Errorf("write Apple photo metadata search row: %w", err)
	}
	return nil
}

func firstExistingTable(ctx context.Context, db *sql.DB, names ...string) (string, error) {
	for _, name := range names {
		var found string
		err := db.QueryRowContext(ctx, `select name from sqlite_master where type = 'table' and name = ?`, name).Scan(&found)
		if err == nil {
			return found, nil
		}
		if err != sql.ErrNoRows {
			return "", err
		}
	}
	return "", fmt.Errorf("unsupported Apple Photos metadata schema: none of %s exist", strings.Join(names, ", "))
}

func metadataQueryRows(ctx context.Context, db *sql.DB, query string) ([]map[string]any, error) {
	rows, err := db.QueryContext(ctx, query)
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
		pointers := make([]any, len(columns))
		for index := range values {
			pointers[index] = &values[index]
		}
		if err := rows.Scan(pointers...); err != nil {
			return nil, err
		}
		row := map[string]any{}
		for index, column := range columns {
			row[column] = normalizedSQLiteValue(values[index])
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func selectMetadataValues(values map[string]any, mapping map[string]string) map[string]any {
	out := map[string]any{}
	for column, key := range mapping {
		value, ok := values[column]
		if !ok || value == nil {
			continue
		}
		out[key] = normalizedSQLiteValue(value)
	}
	return out
}

func normalizedSQLiteValue(value any) any {
	if bytes, ok := value.([]byte); ok {
		return string(bytes)
	}
	return value
}

func stringValue(value any) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(normalizedSQLiteValue(value)))
}

func sqliteTruthy(value any) bool {
	switch typed := normalizedSQLiteValue(value).(type) {
	case int64:
		return typed != 0
	case float64:
		return typed != 0
	case bool:
		return typed
	case string:
		return typed != "" && typed != "0" && !strings.EqualFold(typed, "false")
	default:
		return false
	}
}

func sortedMappedColumns(mapping map[string]string) []string {
	columns := make([]string, 0, len(mapping))
	for column := range mapping {
		columns = append(columns, column)
	}
	sort.Strings(columns)
	return columns
}

func exifSummary(exif map[string]any) string {
	parts := []string{}
	for _, key := range []string{"camera_make", "camera_model", "lens_model", "codec"} {
		if value := stringValue(exif[key]); value != "" {
			parts = append(parts, value)
		}
	}
	if iso, ok := exif["iso"]; ok {
		parts = append(parts, "ISO "+fmt.Sprint(iso))
	}
	if len(parts) == 0 {
		return "Apple camera metadata"
	}
	return strings.Join(parts, " ")
}

func metadataSearchText(observationType, valueText string, values map[string]any) string {
	parts := []string{observationType, strings.ReplaceAll(observationType, "_", " "), valueText, photosLibraryDBFaceSource}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		parts = append(parts, strings.ReplaceAll(key, "_", " "), fmt.Sprint(values[key]))
	}
	return strings.Join(nonEmpty(parts...), " ")
}
