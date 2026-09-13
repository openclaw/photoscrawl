package archive

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/openclaw/crawlkit/store"
)

const photosLibraryDBFaceSource = "photos_library_db"

type ImportFacesResult struct {
	Database                 string            `json:"database"`
	LibraryPath              string            `json:"library_path"`
	Source                   string            `json:"source"`
	PhotosDatabase           string            `json:"photos_database"`
	Schema                   map[string]string `json:"schema"`
	NamedPersonsFound        int               `json:"named_persons_found"`
	NamedFaceRowsFound       int               `json:"named_face_rows_found"`
	ResolvedFaceRows         int               `json:"resolved_face_rows"`
	UnresolvedFaceRows       int               `json:"unresolved_face_rows"`
	FaceObservationsInserted int               `json:"face_observations_inserted"`
	AssetsTouched            int               `json:"assets_touched"`
	UnnamedFacesSkipped      int               `json:"unnamed_faces_skipped"`
	FacesWithEyeState        int               `json:"faces_with_eye_state"`
	FacesWithSmile           int               `json:"faces_with_smile"`
	FacesWithQuality         int               `json:"faces_with_quality"`
	FacesWithBlurScore       int               `json:"faces_with_blur_score"`
	FacesWithPersonKind      int               `json:"faces_with_person_kind"`
}

type photosFaceRow struct {
	facePK            int64
	faceUUID          string
	assetUUID         string
	personLabel       string
	personUUID        string
	nameSource        int64
	cloudSource       int64
	quality           sql.NullFloat64
	blurScore         sql.NullFloat64
	leftEyeClosed     sql.NullInt64
	rightEyeClosed    sql.NullInt64
	smile             sql.NullInt64
	detectionType     sql.NullInt64
	hasLeftEyeClosed  bool
	hasRightEyeClosed bool
	hasSmile          bool
	hasDetectionType  bool
	centerX           sql.NullFloat64
	centerY           sql.NullFloat64
	size              sql.NullFloat64
	sourceWidth       sql.NullInt64
	sourceHeight      sql.NullInt64
}

type photosPerson struct {
	uuid        string
	displayName string
	fullName    string
}

type facesImportInput struct {
	libraryPath  string
	schema       map[string]string
	rows         []photosFaceRow
	namedPersons int
	unnamedFaces int
	peopleByUUID map[string]photosPerson
}

type ImportFacesOptions struct {
	LibraryPath string
}

func ImportFaces(ctx context.Context, paths Paths, opts ImportFacesOptions) (ImportFacesResult, error) {
	libraryPath, err := resolveFacesLibraryPath(ctx, paths, opts.LibraryPath)
	if err != nil {
		return ImportFacesResult{}, err
	}
	input, err := preflightFacesImport(ctx, libraryPath)
	if err != nil {
		return ImportFacesResult{}, err
	}
	archiveDB, err := openAppleArchive(ctx, paths.Database, libraryPath)
	if err != nil {
		return ImportFacesResult{}, err
	}
	defer archiveDB.Close()

	tx, err := archiveDB.DB().BeginTx(ctx, nil)
	if err != nil {
		return ImportFacesResult{}, err
	}
	defer tx.Rollback()
	assetByUUID, err := archiveAssetMap(ctx, tx, libraryPath)
	if err != nil {
		return ImportFacesResult{}, err
	}
	result, _, err := writeFacesImport(ctx, tx, paths, input, assetByUUID, time.Now().UTC())
	if err != nil {
		return ImportFacesResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return ImportFacesResult{}, err
	}
	return result, nil
}

func preflightFacesImport(ctx context.Context, libraryPath string) (facesImportInput, error) {
	liveDBPath := filepath.Join(libraryPath, "database", "Photos.sqlite")
	if _, err := os.Stat(liveDBPath); err != nil {
		return facesImportInput{}, fmt.Errorf("find Photos sqlite: %w", err)
	}
	snapshotPath, cleanup, err := copyPhotosSQLite(ctx, liveDBPath)
	if err != nil {
		return facesImportInput{}, err
	}
	defer cleanup()
	photosDB, err := store.OpenReadOnly(ctx, snapshotPath)
	if err != nil {
		return facesImportInput{}, fmt.Errorf("open copied Photos sqlite: %w", err)
	}
	defer photosDB.Close()
	schema, err := validatePhotosFacesSchema(ctx, photosDB.DB())
	if err != nil {
		return facesImportInput{}, err
	}
	faceRows, namedPersons, unnamedFaces, err := loadPhotosFaceRows(ctx, photosDB.DB())
	if err != nil {
		return facesImportInput{}, err
	}
	peopleByUUID, err := loadPhotosPeopleByUUIDFromDB(ctx, photosDB.DB())
	if err != nil {
		return facesImportInput{}, err
	}
	return facesImportInput{
		libraryPath:  libraryPath,
		schema:       schema,
		rows:         faceRows,
		namedPersons: namedPersons,
		unnamedFaces: unnamedFaces,
		peopleByUUID: peopleByUUID,
	}, nil
}

func writeFacesImport(ctx context.Context, tx *sql.Tx, paths Paths, input facesImportInput, assetByUUID appleAssetScope, importedAt time.Time) (ImportFacesResult, map[string]bool, error) {
	if err := clearImportedFaces(ctx, tx, assetByUUID.libraryID); err != nil {
		return ImportFacesResult{}, nil, err
	}
	inserted := 0
	unresolved := 0
	facesWithEyeState := 0
	facesWithSmile := 0
	facesWithQuality := 0
	facesWithBlurScore := 0
	facesWithPersonKind := 0
	touched := map[string]bool{}
	for _, face := range input.rows {
		assetID, ok := assetByUUID.byUUID[face.assetUUID]
		if !ok {
			unresolved++
			continue
		}
		if err := insertImportedFace(ctx, tx, assetID, face, importedAt); err != nil {
			return ImportFacesResult{}, nil, err
		}
		inserted++
		if faceEyesClosed(face) != nil {
			facesWithEyeState++
		}
		if faceSmile(face) != nil {
			facesWithSmile++
		}
		if faceQuality(face).Valid {
			facesWithQuality++
		}
		if face.blurScore.Valid {
			facesWithBlurScore++
		}
		if face.hasDetectionType {
			facesWithPersonKind++
		}
		touched[assetID] = true
	}
	return ImportFacesResult{
		Database:                 paths.Database,
		LibraryPath:              input.libraryPath,
		Source:                   photosLibraryDBFaceSource,
		PhotosDatabase:           "database/Photos.sqlite",
		Schema:                   input.schema,
		NamedPersonsFound:        input.namedPersons,
		NamedFaceRowsFound:       len(input.rows),
		ResolvedFaceRows:         inserted,
		UnresolvedFaceRows:       unresolved,
		FaceObservationsInserted: inserted,
		AssetsTouched:            len(touched),
		UnnamedFacesSkipped:      input.unnamedFaces,
		FacesWithEyeState:        facesWithEyeState,
		FacesWithSmile:           facesWithSmile,
		FacesWithQuality:         facesWithQuality,
		FacesWithBlurScore:       facesWithBlurScore,
		FacesWithPersonKind:      facesWithPersonKind,
	}, touched, nil
}

func normalizeAssetLocalIdentifier(localIdentifier string) string {
	localIdentifier = strings.TrimSpace(localIdentifier)
	if before, _, ok := strings.Cut(localIdentifier, "/"); ok {
		return before
	}
	return localIdentifier
}

func loadPhotosFaceRows(ctx context.Context, db *sql.DB) ([]photosFaceRow, int, int, error) {
	faceColumns, err := tableColumns(ctx, db, "ZDETECTEDFACE")
	if err != nil {
		return nil, 0, 0, err
	}
	personColumns, err := tableColumns(ctx, db, "ZPERSON")
	if err != nil {
		return nil, 0, 0, err
	}
	var namedPersons int
	if err := db.QueryRowContext(ctx, `
select count(*)
from ZPERSON
where trim(coalesce(ZDISPLAYNAME, ZFULLNAME, '')) <> ''
`).Scan(&namedPersons); err != nil {
		return nil, 0, 0, fmt.Errorf("count named Photos people: %w", err)
	}
	var unnamedFaces int
	if err := db.QueryRowContext(ctx, `
select count(*)
from ZDETECTEDFACE f
left join ZPERSON p on p.Z_PK = f.ZPERSONFORFACE
where trim(coalesce(p.ZDISPLAYNAME, p.ZFULLNAME, '')) = ''
`).Scan(&unnamedFaces); err != nil {
		return nil, 0, 0, fmt.Errorf("count unnamed Photos faces: %w", err)
	}

	optionalColumn := func(table, column string, available bool) string {
		if !available {
			return "null"
		}
		return table + "." + store.QuoteIdent(column)
	}
	rows, err := db.QueryContext(ctx, fmt.Sprintf(`
select f.Z_PK,
       coalesce(f.ZUUID, ''),
       coalesce(a.ZUUID, ''),
       trim(coalesce(p.ZDISPLAYNAME, p.ZFULLNAME, '')),
       coalesce(p.ZPERSONUUID, ''),
       coalesce(f.ZNAMESOURCE, 0),
       coalesce(f.ZCLOUDNAMESOURCE, 0),
       cast(f.ZQUALITY as real),
	       cast(%s as real),
	       %s,
	       %s,
	       %s,
	       %s,
       cast(f.ZCENTERX as real),
       cast(f.ZCENTERY as real),
       cast(f.ZSIZE as real),
       f.ZSOURCEWIDTH,
       f.ZSOURCEHEIGHT
from ZDETECTEDFACE f
join ZPERSON p on p.Z_PK = f.ZPERSONFORFACE
join ZASSET a on a.Z_PK = f.ZASSETFORFACE
where trim(coalesce(p.ZDISPLAYNAME, p.ZFULLNAME, '')) <> ''
  and coalesce(a.ZUUID, '') <> ''
order by f.Z_PK
`,
		optionalColumn("f", "ZBLURSCORE", faceColumns["ZBLURSCORE"]),
		optionalColumn("f", "ZISLEFTEYECLOSED", faceColumns["ZISLEFTEYECLOSED"]),
		optionalColumn("f", "ZISRIGHTEYECLOSED", faceColumns["ZISRIGHTEYECLOSED"]),
		optionalColumn("f", "ZHASSMILE", faceColumns["ZHASSMILE"]),
		optionalColumn("p", "ZDETECTIONTYPE", personColumns["ZDETECTIONTYPE"]),
	))
	if err != nil {
		return nil, 0, 0, fmt.Errorf("load Photos named faces: %w", err)
	}
	defer rows.Close()
	out := []photosFaceRow{}
	for rows.Next() {
		var row photosFaceRow
		if err := rows.Scan(
			&row.facePK,
			&row.faceUUID,
			&row.assetUUID,
			&row.personLabel,
			&row.personUUID,
			&row.nameSource,
			&row.cloudSource,
			&row.quality,
			&row.blurScore,
			&row.leftEyeClosed,
			&row.rightEyeClosed,
			&row.smile,
			&row.detectionType,
			&row.centerX,
			&row.centerY,
			&row.size,
			&row.sourceWidth,
			&row.sourceHeight,
		); err != nil {
			return nil, 0, 0, err
		}
		row.hasLeftEyeClosed = faceColumns["ZISLEFTEYECLOSED"]
		row.hasRightEyeClosed = faceColumns["ZISRIGHTEYECLOSED"]
		row.hasSmile = faceColumns["ZHASSMILE"]
		row.hasDetectionType = personColumns["ZDETECTIONTYPE"]
		out = append(out, row)
	}
	return out, namedPersons, unnamedFaces, rows.Err()
}

func loadPhotosPeopleByUUID(ctx context.Context, libraryPath string) (map[string]photosPerson, error) {
	liveDBPath := filepath.Join(libraryPath, "database", "Photos.sqlite")
	if _, err := os.Stat(liveDBPath); err != nil {
		return nil, fmt.Errorf("find Photos sqlite for people lookup: %w", err)
	}
	snapshotPath, cleanup, err := copyPhotosSQLite(ctx, liveDBPath)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	photosDB, err := store.OpenReadOnly(ctx, snapshotPath)
	if err != nil {
		return nil, fmt.Errorf("open copied Photos sqlite for people lookup: %w", err)
	}
	defer photosDB.Close()
	if _, err := validatePhotosFacesSchema(ctx, photosDB.DB()); err != nil {
		return nil, err
	}
	return loadPhotosPeopleByUUIDFromDB(ctx, photosDB.DB())
}

func loadPhotosPeopleByUUIDFromDB(ctx context.Context, db *sql.DB) (map[string]photosPerson, error) {
	rows, err := db.QueryContext(ctx, `
select coalesce(ZPERSONUUID, ''), coalesce(ZDISPLAYNAME, ''), coalesce(ZFULLNAME, '')
from ZPERSON
where coalesce(ZPERSONUUID, '') <> ''
`)
	if err != nil {
		return nil, fmt.Errorf("load Photos people by UUID: %w", err)
	}
	defer rows.Close()
	out := map[string]photosPerson{}
	for rows.Next() {
		var person photosPerson
		if err := rows.Scan(&person.uuid, &person.displayName, &person.fullName); err != nil {
			return nil, err
		}
		person.uuid = strings.ToUpper(stripAppleSearchString(person.uuid))
		out[person.uuid] = person
	}
	return out, rows.Err()
}

func clearImportedFaces(ctx context.Context, tx *sql.Tx, libraryID string) error {
	if _, err := tx.ExecContext(ctx, `
delete from observation_fts
where id in (
  select id from face_observation where source = ? and asset_id in (select id from asset where source_library_id = ?)
)
`, photosLibraryDBFaceSource, libraryID); err != nil {
		return fmt.Errorf("clear imported face fts: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `delete from face_observation where source = ? and asset_id in (select id from asset where source_library_id = ?)`, photosLibraryDBFaceSource, libraryID); err != nil {
		return fmt.Errorf("clear imported faces: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `delete from evidence_ref where source = ? and evidence_kind = ? and asset_id in (select id from asset where source_library_id = ?)`, photosLibraryDBFaceSource, "face_observation", libraryID); err != nil {
		return fmt.Errorf("clear imported face evidence: %w", err)
	}
	return nil
}

func insertImportedFace(ctx context.Context, tx *sql.Tx, assetID string, face photosFaceRow, importedAt time.Time) error {
	faceLocalID := face.faceUUID
	if strings.TrimSpace(faceLocalID) == "" {
		faceLocalID = fmt.Sprintf("ZDETECTEDFACE:%d", face.facePK)
	}
	observationID := stableID("face_observation", assetID, photosLibraryDBFaceSource, faceLocalID, face.personLabel)
	evidenceID := stableID("evidence", assetID, photosLibraryDBFaceSource, faceLocalID)
	boundingJSON, err := jsonText(faceBoundingBox(face))
	if err != nil {
		return err
	}
	evidenceJSON, err := jsonText(map[string]any{
		"schema_source":     "Photos.sqlite",
		"face_table":        "ZDETECTEDFACE",
		"person_table":      "ZPERSON",
		"asset_table":       "ZASSET",
		"face_pk":           face.facePK,
		"face_uuid":         face.faceUUID,
		"asset_uuid":        face.assetUUID,
		"person_uuid":       face.personUUID,
		"name_source":       face.nameSource,
		"cloud_name_source": face.cloudSource,
		"quality":           nullableSQLFloat(face.quality),
		"blur_score":        nullableSQLFloat(face.blurScore),
		"left_eye_closed":   nullableSQLInt(face.leftEyeClosed),
		"right_eye_closed":  nullableSQLInt(face.rightEyeClosed),
		"has_smile":         nullableSQLInt(face.smile),
		"detection_type":    nullableSQLInt(face.detectionType),
		"person_kind":       facePersonKind(face),
		"imported_at":       importedAt.Format(time.RFC3339Nano),
		"read_only":         true,
	})
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
insert into evidence_ref(id, asset_id, evidence_kind, source, pointer, value_json)
values (?, ?, ?, ?, ?, ?)
`, evidenceID, assetID, "face_observation", photosLibraryDBFaceSource, fmt.Sprintf("ZDETECTEDFACE:%d", face.facePK), evidenceJSON); err != nil {
		return fmt.Errorf("write imported face evidence: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
insert into face_observation(id, asset_id, face_local_id, person_label, person_uuid, person_kind, confidence, quality, blur_score, eyes_closed, smile, bounding_box_json, source, evidence_id)
values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, observationID, assetID, faceLocalID, face.personLabel, face.personUUID, facePersonKind(face), 1.0, nullableSQLFloat(faceQuality(face)), nullableSQLFloat(face.blurScore), faceEyesClosed(face), faceSmile(face), boundingJSON, photosLibraryDBFaceSource, evidenceID); err != nil {
		return fmt.Errorf("write imported face observation: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
insert into observation_fts(id, asset_id, title, body)
values (?, ?, ?, ?)
`, observationID, assetID, face.personLabel, strings.Join(nonEmpty("person", "face", face.personLabel, photosLibraryDBFaceSource), " ")); err != nil {
		return fmt.Errorf("write imported face fts: %w", err)
	}
	return nil
}

// faceQuality treats Photos' negative quality sentinel (-1, "not computed")
// as unknown so rankings never read it as the worst possible face. The raw
// value stays in the evidence record.
func faceQuality(face photosFaceRow) sql.NullFloat64 {
	if !face.quality.Valid || face.quality.Float64 < 0 {
		return sql.NullFloat64{}
	}
	return face.quality
}

func faceEyesClosed(face photosFaceRow) any {
	if (!face.hasLeftEyeClosed && !face.hasRightEyeClosed) ||
		(!face.leftEyeClosed.Valid && !face.rightEyeClosed.Valid) {
		return nil
	}
	if (face.leftEyeClosed.Valid && face.leftEyeClosed.Int64 != 0) ||
		(face.rightEyeClosed.Valid && face.rightEyeClosed.Int64 != 0) {
		return 1
	}
	if face.hasLeftEyeClosed && face.hasRightEyeClosed {
		return 0
	}
	return nil
}

func faceSmile(face photosFaceRow) any {
	if !face.hasSmile || !face.smile.Valid {
		return nil
	}
	if face.smile.Int64 != 0 {
		return 1
	}
	return 0
}

func facePersonKind(face photosFaceRow) string {
	if !face.hasDetectionType {
		return "unknown"
	}
	switch face.detectionType.Int64 {
	case 1:
		return "human"
	case 3:
		return "pet"
	default:
		return "unknown"
	}
}

func faceBoundingBox(face photosFaceRow) map[string]any {
	out := map[string]any{"coordinate_space": "photos_normalized_center_size"}
	if face.centerX.Valid {
		out["center_x"] = face.centerX.Float64
	}
	if face.centerY.Valid {
		out["center_y"] = face.centerY.Float64
	}
	if face.size.Valid {
		out["size"] = face.size.Float64
	}
	if face.sourceWidth.Valid {
		out["source_width"] = face.sourceWidth.Int64
	}
	if face.sourceHeight.Valid {
		out["source_height"] = face.sourceHeight.Int64
	}
	return out
}
