package archive

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/openclaw/crawlkit/store"
)

const photosLibraryDBMetadataModelID = "apple.photos.metadata"
const photosLibraryDBMetadataVersion = "photos.sqlite.v1"

type ImportPhotoMetadataResult struct {
	Source                        string            `json:"source"`
	ModelID                       string            `json:"model_id"`
	PhotosDatabase                string            `json:"photos_database"`
	Schema                        map[string]string `json:"schema"`
	AssetsRead                    int               `json:"assets_read"`
	AssetsResolved                int               `json:"assets_resolved"`
	AssetsUnresolved              int               `json:"assets_unresolved"`
	ExifObservationsInserted      int               `json:"exif_observations_inserted"`
	QualityObservationsInserted   int               `json:"quality_observations_inserted"`
	EditObservationsInserted      int               `json:"edit_observations_inserted"`
	DuplicateObservationsInserted int               `json:"duplicate_observations_inserted"`
	AssetsTouched                 int               `json:"assets_touched"`
}

type applePhotoMetadataRow struct {
	assetUUID       string
	exif            map[string]any
	quality         map[string]any
	adjustmentState any
	hasAdjustments  bool
	duplicate       map[string]any
	hasDuplicate    bool
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

	editRows, err := rows(ctx, db, fmt.Sprintf("select a.ZUUID, a.%s as ZEDITSTATE from %s a", store.QuoteIdent(adjustmentColumn), store.QuoteIdent(assetTable)))
	if err != nil {
		return photoMetadataImportInput{}, fmt.Errorf("read Apple edit states: %w", err)
	}
	for _, values := range editRows {
		row := ensure(stringValue(values["ZUUID"]))
		row.adjustmentState = normalizeSQLValue(values["ZEDITSTATE"])
		row.hasAdjustments = sqliteTruthy(values["ZEDITSTATE"])
	}

	duplicateColumn := func(column string) string {
		if !assetColumns[column] {
			return "null"
		}
		return "a." + store.QuoteIdent(column)
	}
	duplicateRows, err := rows(ctx, db, fmt.Sprintf(`select a.ZUUID,
       %s as ZDUPLICATEASSETVISIBILITYSTATE,
       %s as ZDUPLICATEMETADATAMATCHINGALBUM,
       %s as ZDUPLICATEPERCEPTUALMATCHINGALBUM
from %s a`,
		duplicateColumn("ZDUPLICATEASSETVISIBILITYSTATE"),
		duplicateColumn("ZDUPLICATEMETADATAMATCHINGALBUM"),
		duplicateColumn("ZDUPLICATEPERCEPTUALMATCHINGALBUM"),
		store.QuoteIdent(assetTable)))
	if err != nil {
		return photoMetadataImportInput{}, fmt.Errorf("read Apple duplicate metadata: %w", err)
	}
	for _, values := range duplicateRows {
		row := ensure(stringValue(values["ZUUID"]))
		state := int64Value(values["ZDUPLICATEASSETVISIBILITYSTATE"])
		metadataGroup := nullableInt64Value(values["ZDUPLICATEMETADATAMATCHINGALBUM"])
		perceptualGroup := nullableInt64Value(values["ZDUPLICATEPERCEPTUALMATCHINGALBUM"])
		row.duplicate = map[string]any{
			"state":            state,
			"metadata_group":   metadataGroup,
			"perceptual_group": perceptualGroup,
		}
		row.hasDuplicate = state != 0 || metadataGroup != nil || perceptualGroup != nil
	}

	exifRows, err := rows(ctx, db, fmt.Sprintf("select a.ZUUID, e.* from %s a join ZEXTENDEDATTRIBUTES e on e.ZASSET = a.Z_PK", store.QuoteIdent(assetTable)))
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
	qualityRows, err := rows(ctx, db, fmt.Sprintf("select %s, c.* from %s a join ZCOMPUTEDASSETATTRIBUTES c on c.ZASSET = a.Z_PK", strings.Join(assetScoreSelect, ", "), store.QuoteIdent(assetTable)))
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
	if err := loadMediaAnalysisBlurriness(ctx, db, assetTable, ensure); err != nil {
		return photoMetadataImportInput{}, err
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

func writePhotoMetadataImport(ctx context.Context, tx *sql.Tx, input photoMetadataImportInput, assetByUUID appleAssetScope, importedAt time.Time) (ImportPhotoMetadataResult, map[string]bool, error) {
	stage, err := beginAppleStage(ctx, tx, appleRecordMetadata)
	if err != nil {
		return ImportPhotoMetadataResult{}, nil, err
	}
	defer stage.close()
	result := ImportPhotoMetadataResult{
		Source:         photosLibraryDBFaceSource,
		ModelID:        photosLibraryDBMetadataModelID,
		PhotosDatabase: "database/Photos.sqlite",
		Schema:         input.schema,
		AssetsRead:     len(input.rows),
	}
	touched := map[string]bool{}
	for _, row := range input.rows {
		assetID, ok := assetByUUID.byUUID[row.assetUUID]
		if !ok {
			result.AssetsUnresolved++
			continue
		}
		result.AssetsResolved++
		if len(row.exif) > 0 {
			if err := stagePhotoMetadataObservation(ctx, stage, assetID, row.assetUUID, "apple_exif", row.exif, exifSummary(row.exif), "ZEXTENDEDATTRIBUTES", importedAt); err != nil {
				return ImportPhotoMetadataResult{}, nil, err
			}
			result.ExifObservationsInserted++
		}
		if len(row.quality) > 0 {
			if err := stagePhotoMetadataObservation(ctx, stage, assetID, row.assetUUID, "apple_quality_scores", row.quality, "Apple Photos quality scores", "ZCOMPUTEDASSETATTRIBUTES", importedAt); err != nil {
				return ImportPhotoMetadataResult{}, nil, err
			}
			result.QualityObservationsInserted++
		}
		if row.hasDuplicate {
			if err := stagePhotoMetadataObservation(ctx, stage, assetID, row.assetUUID, "apple_duplicate", row.duplicate, fmt.Sprintf("duplicate state %d", row.duplicate["state"]), input.schema["asset_table"], importedAt); err != nil {
				return ImportPhotoMetadataResult{}, nil, err
			}
			result.DuplicateObservationsInserted++
		}
		edit := map[string]any{"has_adjustments": row.hasAdjustments, "adjustment_state": row.adjustmentState}
		editText := "not edited"
		if row.hasAdjustments {
			editText = "edited"
		}
		if err := stagePhotoMetadataObservation(ctx, stage, assetID, row.assetUUID, "apple_edit_state", edit, editText, input.schema["asset_table"], importedAt); err != nil {
			return ImportPhotoMetadataResult{}, nil, err
		}
		result.EditObservationsInserted++
		touched[assetID] = true
	}
	if err := stage.apply(ctx, assetByUUID.libraryID); err != nil {
		return ImportPhotoMetadataResult{}, nil, err
	}
	result.AssetsTouched = len(touched)
	return result, touched, nil
}

func stagePhotoMetadataObservation(ctx context.Context, stage *appleStage, assetID, assetUUID, observationType string, value map[string]any, valueText, table string, importedAt time.Time) error {
	valueJSON, err := jsonText(value)
	if err != nil {
		return err
	}
	observationID := stableID("model_observation", assetID, photosLibraryDBFaceSource, photosLibraryDBMetadataModelID, observationType)
	evidenceID := stableID("evidence", assetID, photosLibraryDBFaceSource, photosLibraryDBMetadataModelID, observationType)
	evidence := map[string]any{
		"schema_source": "Photos.sqlite",
		"table":         table,
		"asset_uuid":    assetUUID,
		"values":        value,
		"read_only":     true,
	}
	evidenceStableJSON, err := jsonText(evidence)
	if err != nil {
		return err
	}
	evidence["imported_at"] = importedAt.Format(time.RFC3339Nano)
	evidenceJSON, err := jsonText(evidence)
	if err != nil {
		return err
	}
	return stage.add(ctx, stagedMetadataRecord{
		AssetID:            assetID,
		EvidenceID:         evidenceID,
		EvidencePointer:    table + ":" + assetUUID,
		EvidenceJSON:       evidenceJSON,
		EvidenceStableJSON: evidenceStableJSON,
		ObservationID:      observationID,
		ObservationType:    observationType,
		ValueText:          valueText,
		ValueJSON:          valueJSON,
		Confidence:         1,
		PromptVersion:      photosLibraryDBMetadataVersion,
		FTSTitle:           valueText,
		FTSBody:            metadataSearchText(observationType, valueText, value),
	})
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

func selectMetadataValues(values map[string]any, mapping map[string]string) map[string]any {
	out := map[string]any{}
	for column, key := range mapping {
		value, ok := values[column]
		if !ok || value == nil {
			continue
		}
		out[key] = normalizeSQLValue(value)
	}
	return out
}

func stringValue(value any) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(normalizeSQLValue(value)))
}

func int64Value(value any) int64 {
	value = normalizeSQLValue(value)
	switch typed := value.(type) {
	case int64:
		return typed
	case int:
		return int64(typed)
	case float64:
		return int64(typed)
	default:
		return 0
	}
}

func nullableInt64Value(value any) any {
	if value == nil || int64Value(value) <= 0 {
		return nil
	}
	return int64Value(value)
}

func sqliteTruthy(value any) bool {
	switch typed := normalizeSQLValue(value).(type) {
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

// loadMediaAnalysisBlurriness adds Photos' media-analysis blurriness score to
// each asset's quality values as "media_blurriness". Despite Apple's column
// name, 1 means sharp and values near 0 mean visibly blurred. The table is
// optional; older libraries without it import unchanged.
func loadMediaAnalysisBlurriness(ctx context.Context, db *sql.DB, assetTable string, ensure func(string) *applePhotoMetadataRow) error {
	columns, err := tableColumns(ctx, db, "ZMEDIAANALYSISASSETATTRIBUTES")
	if err != nil {
		return err
	}
	if !columns["ZASSET"] || !columns["ZBLURRINESSSCORE"] {
		return nil
	}
	blurRows, err := rows(ctx, db, fmt.Sprintf("select a.ZUUID, m.ZBLURRINESSSCORE from %s a join ZMEDIAANALYSISASSETATTRIBUTES m on m.ZASSET = a.Z_PK where m.ZBLURRINESSSCORE is not null", store.QuoteIdent(assetTable)))
	if err != nil {
		return fmt.Errorf("read Apple media analysis blurriness: %w", err)
	}
	for _, values := range blurRows {
		row := ensure(stringValue(values["ZUUID"]))
		if row.quality == nil {
			row.quality = map[string]any{}
		}
		row.quality["media_blurriness"] = normalizeSQLValue(values["ZBLURRINESSSCORE"])
	}
	return nil
}
