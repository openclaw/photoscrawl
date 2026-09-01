package archive

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/openclaw/crawlkit/store"
)

const photosLibraryDBFaceSource = "photos_library_db"
const photosSearchIndexSource = "photos_search_index"
const photosSearchIndexModelID = "apple.photos.search-index"

type ImportAppleOptions struct {
	LibraryPath string
}

type ImportAppleResult struct {
	Database            string                    `json:"database"`
	LibraryPath         string                    `json:"library_path"`
	Faces               ImportFacesResult         `json:"faces"`
	SearchIndex         ImportSearchIndexResult   `json:"search_index"`
	PhotoMetadata       ImportPhotoMetadataResult `json:"photo_metadata"`
	TotalAssetsTouched  int                       `json:"total_assets_touched"`
	SyncStrategy        string                    `json:"sync_strategy"`
	ImportedAt          string                    `json:"imported_at"`
	OsxphotosReferences []string                  `json:"osxphotos_references"`
}

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
}

type ImportSearchIndexResult struct {
	Source                            string            `json:"source"`
	Variant                           string            `json:"variant"`
	SearchDatabase                    string            `json:"search_database"`
	Schema                            map[string]string `json:"schema"`
	GroupsRead                        int               `json:"groups_read"`
	GroupsResolved                    int               `json:"groups_resolved"`
	GroupsUnresolved                  int               `json:"groups_unresolved"`
	KnownCategoryRows                 int               `json:"known_category_rows"`
	UnknownCategoryRows               int               `json:"unknown_category_rows"`
	CategoriesSeen                    map[string]int    `json:"categories_seen"`
	UnknownCategories                 []int             `json:"unknown_categories"`
	VisualObservationsInserted        int               `json:"visual_observations_inserted"`
	PersonFaceObservationsInserted    int               `json:"person_face_observations_inserted"`
	AssetsTouched                     int               `json:"assets_touched"`
	PersonLookupIdentifiersResolved   int               `json:"person_lookup_identifiers_resolved"`
	PersonLookupIdentifiersUnresolved int               `json:"person_lookup_identifiers_unresolved"`
}

type photosFaceRow struct {
	facePK       int64
	faceUUID     string
	assetUUID    string
	personLabel  string
	personUUID   string
	nameSource   int64
	cloudSource  int64
	quality      sql.NullFloat64
	centerX      sql.NullFloat64
	centerY      sql.NullFloat64
	size         sql.NullFloat64
	sourceWidth  sql.NullInt64
	sourceHeight sql.NullInt64
}

type photosPerson struct {
	uuid        string
	displayName string
	fullName    string
}

type appleSearchRow struct {
	searchVariant    string
	gaRowID          int64
	uuid0            int64
	uuid1            int64
	assetUUID        string
	groupID          int64
	category         int
	owningGroupID    sql.NullInt64
	contentString    string
	normalizedString string
	lookupIdentifier string
	score            sql.NullFloat64
}

type appleSearchCategory struct {
	ID           int
	Name         string
	SemanticKind string
}

type facesImportInput struct {
	libraryPath  string
	schema       map[string]string
	rows         []photosFaceRow
	namedPersons int
	unnamedFaces int
	peopleByUUID map[string]photosPerson
}

type searchImportInput struct {
	variant string
	relPath string
	schema  map[string]string
	rows    []appleSearchRow
}

func ImportApple(ctx context.Context, paths Paths, opts ImportAppleOptions) (ImportAppleResult, error) {
	importedAt := time.Now().UTC()
	libraryPath, err := resolveFacesLibraryPath(ctx, paths, opts.LibraryPath)
	if err != nil {
		return ImportAppleResult{}, err
	}
	facesInput, err := preflightFacesImport(ctx, libraryPath)
	if err != nil {
		return ImportAppleResult{}, err
	}
	searchInput, err := preflightSearchImport(ctx, libraryPath)
	if err != nil {
		return ImportAppleResult{}, err
	}
	metadataInput, err := preflightPhotoMetadataImport(ctx, libraryPath)
	if err != nil {
		return ImportAppleResult{}, err
	}
	archiveDB, err := openArchiveStore(ctx, paths.Database)
	if err != nil {
		return ImportAppleResult{}, err
	}
	defer archiveDB.Close()
	assetByUUID, err := archiveAssetMap(ctx, archiveDB.DB())
	if err != nil {
		return ImportAppleResult{}, err
	}
	tx, err := archiveDB.DB().BeginTx(ctx, nil)
	if err != nil {
		return ImportAppleResult{}, err
	}
	defer tx.Rollback()
	faces, faceAssets, err := writeFacesImport(ctx, tx, paths, facesInput, assetByUUID, importedAt)
	if err != nil {
		return ImportAppleResult{}, err
	}
	searchIndex, searchAssets, err := writeSearchImport(ctx, tx, searchInput, assetByUUID, facesInput.peopleByUUID, importedAt)
	if err != nil {
		return ImportAppleResult{}, err
	}
	photoMetadata, metadataAssets, err := writePhotoMetadataImport(ctx, tx, metadataInput, assetByUUID, importedAt)
	if err != nil {
		return ImportAppleResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return ImportAppleResult{}, err
	}
	for assetID := range searchAssets {
		faceAssets[assetID] = true
	}
	for assetID := range metadataAssets {
		faceAssets[assetID] = true
	}
	return ImportAppleResult{
		Database:           paths.Database,
		LibraryPath:        libraryPath,
		Faces:              faces,
		SearchIndex:        searchIndex,
		PhotoMetadata:      photoMetadata,
		TotalAssetsTouched: len(faceAssets),
		SyncStrategy:       "atomic_authoritative_replace_by_source",
		ImportedAt:         importedAt.Format(time.RFC3339Nano),
		OsxphotosReferences: []string{
			"osxphotos/photosdb/_photosdb_process_searchinfo.py:_process_searchinfo",
			"osxphotos/photosdb/_photosdb_process_searchinfo.py:_process_psi_searchinfo",
			"osxphotos/photosdb/_photosdb_process_searchinfo.py:_process_leo_searchinfo",
			"osxphotos/photosdb/_photosdb_process_searchinfo.py:decode_leo_lexeme_ids",
			"osxphotos/photosdb/_photosdb_process_searchinfo.py:ints_to_uuid",
			"osxphotos/_constants.py:SearchCategory_Photos8",
			"osxphotos/photosdb/_photosdb_process_exif.py:_process_exifinfo_5",
			"osxphotos/photosdb/_photosdb_process_scoreinfo.py:_process_scoreinfo_5",
			"osxphotos/_constants.py:HAS_ADJUSTMENTS",
		},
	}, nil
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
	archiveDB, err := openArchiveStore(ctx, paths.Database)
	if err != nil {
		return ImportFacesResult{}, err
	}
	defer archiveDB.Close()

	assetByUUID, err := archiveAssetMap(ctx, archiveDB.DB())
	if err != nil {
		return ImportFacesResult{}, err
	}
	tx, err := archiveDB.DB().BeginTx(ctx, nil)
	if err != nil {
		return ImportFacesResult{}, err
	}
	defer tx.Rollback()
	result, _, err := writeFacesImport(ctx, tx, paths, input, assetByUUID, time.Now().UTC())
	if err != nil {
		return ImportFacesResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return ImportFacesResult{}, err
	}
	return result, nil
}

type ImportSearchIndexOptions struct {
	LibraryPath string
}

func ImportSearchIndex(ctx context.Context, paths Paths, opts ImportSearchIndexOptions) (ImportSearchIndexResult, error) {
	libraryPath, err := resolveFacesLibraryPath(ctx, paths, opts.LibraryPath)
	if err != nil {
		return ImportSearchIndexResult{}, err
	}
	input, err := preflightSearchImport(ctx, libraryPath)
	if err != nil {
		return ImportSearchIndexResult{}, err
	}
	archiveDB, err := openArchiveStore(ctx, paths.Database)
	if err != nil {
		return ImportSearchIndexResult{}, err
	}
	defer archiveDB.Close()

	assetByUUID, err := archiveAssetMap(ctx, archiveDB.DB())
	if err != nil {
		return ImportSearchIndexResult{}, err
	}
	peopleByUUID, err := loadPhotosPeopleByUUID(ctx, libraryPath)
	if err != nil {
		return ImportSearchIndexResult{}, err
	}
	tx, err := archiveDB.DB().BeginTx(ctx, nil)
	if err != nil {
		return ImportSearchIndexResult{}, err
	}
	defer tx.Rollback()
	result, _, err := writeSearchImport(ctx, tx, input, assetByUUID, peopleByUUID, time.Now().UTC())
	if err != nil {
		return ImportSearchIndexResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return ImportSearchIndexResult{}, err
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

func preflightSearchImport(ctx context.Context, libraryPath string) (searchImportInput, error) {
	searchDBPath, variant, relPath, err := resolveAppleSearchDB(libraryPath)
	if err != nil {
		return searchImportInput{}, err
	}
	snapshotPath, cleanup, err := copySQLite(ctx, searchDBPath, "photoscrawl-search-index-")
	if err != nil {
		return searchImportInput{}, err
	}
	defer cleanup()
	searchDB, err := store.OpenReadOnly(ctx, snapshotPath)
	if err != nil {
		return searchImportInput{}, fmt.Errorf("open copied Apple search index: %w", err)
	}
	defer searchDB.Close()
	input := searchImportInput{variant: variant, relPath: relPath}
	switch variant {
	case "psi":
		input.schema, err = validatePSISearchSchema(ctx, searchDB.DB())
		if err == nil {
			input.rows, err = loadPSISearchRows(ctx, searchDB.DB())
		}
	case "leo":
		input.schema, err = validateLeoSearchSchema(ctx, searchDB.DB())
		if err == nil {
			input.rows, err = loadLeoSearchRows(ctx, searchDB.DB())
		}
	default:
		err = fmt.Errorf("unsupported Apple search index variant: %s", variant)
	}
	if err != nil {
		return searchImportInput{}, err
	}
	return input, nil
}

func writeFacesImport(ctx context.Context, tx *sql.Tx, paths Paths, input facesImportInput, assetByUUID map[string]string, importedAt time.Time) (ImportFacesResult, map[string]bool, error) {
	if err := clearImportedFaces(ctx, tx); err != nil {
		return ImportFacesResult{}, nil, err
	}
	inserted := 0
	unresolved := 0
	touched := map[string]bool{}
	for _, face := range input.rows {
		assetID, ok := assetByUUID[face.assetUUID]
		if !ok {
			unresolved++
			continue
		}
		if err := insertImportedFace(ctx, tx, assetID, face, importedAt); err != nil {
			return ImportFacesResult{}, nil, err
		}
		inserted++
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
	}, touched, nil
}

func writeSearchImport(ctx context.Context, tx *sql.Tx, input searchImportInput, assetByUUID map[string]string, peopleByUUID map[string]photosPerson, importedAt time.Time) (ImportSearchIndexResult, map[string]bool, error) {
	if err := clearImportedSearchIndex(ctx, tx); err != nil {
		return ImportSearchIndexResult{}, nil, err
	}
	result := ImportSearchIndexResult{
		Source:         photosSearchIndexSource,
		Variant:        input.variant,
		SearchDatabase: input.relPath,
		Schema:         input.schema,
		CategoriesSeen: map[string]int{},
	}
	touched := map[string]bool{}
	unknownCategories := map[int]bool{}
	for _, row := range input.rows {
		result.GroupsRead++
		category := appleSearchCategoryForID(row.category)
		result.CategoriesSeen[fmt.Sprint(row.category)]++
		if category.Name == "UNKNOWN" {
			result.UnknownCategoryRows++
			unknownCategories[row.category] = true
		} else {
			result.KnownCategoryRows++
		}
		assetID, ok := assetByUUID[row.assetUUID]
		if !ok {
			result.GroupsUnresolved++
			continue
		}
		person, hasPerson := peopleByUUID[strings.ToUpper(row.lookupIdentifier)]
		if category.Name == "PERSON" {
			if hasPerson {
				result.PersonLookupIdentifiersResolved++
			} else {
				result.PersonLookupIdentifiersUnresolved++
			}
		}
		if err := insertImportedSearchObservation(ctx, tx, assetID, row, category, person, hasPerson, importedAt); err != nil {
			return ImportSearchIndexResult{}, nil, err
		}
		result.GroupsResolved++
		result.VisualObservationsInserted++
		if category.Name == "PERSON" {
			result.PersonFaceObservationsInserted++
		}
		touched[assetID] = true
	}
	result.AssetsTouched = len(touched)
	for id := range unknownCategories {
		result.UnknownCategories = append(result.UnknownCategories, id)
	}
	sort.Ints(result.UnknownCategories)
	return result, touched, nil
}

func resolveFacesLibraryPath(ctx context.Context, paths Paths, requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	if requested != "" {
		return requested, nil
	}
	db, err := store.OpenReadOnly(ctx, paths.Database)
	if err == nil {
		defer db.Close()
		var libraryPath string
		err = db.DB().QueryRowContext(ctx, `
select library_path
from source_library
order by snapshot_created_at desc
limit 1
`).Scan(&libraryPath)
		if err == nil && strings.TrimSpace(libraryPath) != "" {
			return libraryPath, nil
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Pictures", "Photos Library.photoslibrary"), nil
}

func copyPhotosSQLite(ctx context.Context, liveDBPath string) (string, func(), error) {
	return copySQLite(ctx, liveDBPath, "photoscrawl-faces-")
}

type sqliteFileState struct {
	exists      bool
	size        int64
	modifiedNS  int64
	contentHash [sha256.Size]byte
}

func copySQLite(ctx context.Context, liveDBPath string, tempPrefix string) (string, func(), error) {
	dir, err := os.MkdirTemp("", tempPrefix)
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	dest := filepath.Join(dir, filepath.Base(liveDBPath))
	for attempt := 1; attempt <= 5; attempt++ {
		if err := ctx.Err(); err != nil {
			cleanup()
			return "", func() {}, err
		}
		_ = os.Remove(dest)
		_ = os.Remove(dest + "-wal")
		before, err := statSQLiteFiles(liveDBPath)
		if err != nil {
			cleanup()
			return "", func() {}, err
		}
		copied, err := copySQLiteFiles(liveDBPath, dest, before)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			cleanup()
			return "", func() {}, err
		}
		after, err := hashSQLiteFiles(liveDBPath)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			cleanup()
			return "", func() {}, err
		}
		if sqliteFilesStable(before, copied, after) {
			if err := verifySQLiteSnapshot(ctx, dest); err == nil {
				return dest, cleanup, nil
			}
		}
	}
	cleanup()
	return "", func() {}, fmt.Errorf("create consistent SQLite snapshot: source kept changing during 5 attempts")
}

func statSQLiteFiles(dbPath string) (map[string]sqliteFileState, error) {
	out := map[string]sqliteFileState{}
	for _, suffix := range []string{"", "-wal"} {
		path := dbPath + suffix
		info, err := os.Stat(path)
		if errors.Is(err, os.ErrNotExist) {
			out[suffix] = sqliteFileState{}
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("inspect SQLite source %s: %w", path, err)
		}
		out[suffix] = sqliteFileState{exists: true, size: info.Size(), modifiedNS: info.ModTime().UnixNano()}
	}
	if !out[""].exists {
		return nil, fmt.Errorf("SQLite source does not exist: %s", dbPath)
	}
	return out, nil
}

func copySQLiteFiles(sourceDB, destDB string, before map[string]sqliteFileState) (map[string]sqliteFileState, error) {
	out := map[string]sqliteFileState{}
	for _, suffix := range []string{"", "-wal"} {
		if !before[suffix].exists {
			out[suffix] = sqliteFileState{}
			continue
		}
		digest, err := copyFileWithHash(sourceDB+suffix, destDB+suffix, 0o600)
		if err != nil {
			return nil, err
		}
		state := before[suffix]
		state.contentHash = digest
		out[suffix] = state
	}
	return out, nil
}

func hashSQLiteFiles(dbPath string) (map[string]sqliteFileState, error) {
	states, err := statSQLiteFiles(dbPath)
	if err != nil {
		return nil, err
	}
	for _, suffix := range []string{"", "-wal"} {
		state := states[suffix]
		if !state.exists {
			continue
		}
		file, err := os.Open(dbPath + suffix)
		if err != nil {
			return nil, fmt.Errorf("open SQLite source %s: %w", dbPath+suffix, err)
		}
		digest := sha256.New()
		_, copyErr := io.Copy(digest, file)
		closeErr := file.Close()
		if copyErr != nil {
			return nil, fmt.Errorf("hash SQLite source %s: %w", dbPath+suffix, copyErr)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("close SQLite source %s: %w", dbPath+suffix, closeErr)
		}
		copy(state.contentHash[:], digest.Sum(nil))
		states[suffix] = state
	}
	return states, nil
}

func sqliteFilesStable(before, copied, after map[string]sqliteFileState) bool {
	for _, suffix := range []string{"", "-wal"} {
		if before[suffix].exists != after[suffix].exists || copied[suffix].exists != after[suffix].exists {
			return false
		}
		if !after[suffix].exists {
			continue
		}
		if before[suffix].size != after[suffix].size || before[suffix].modifiedNS != after[suffix].modifiedNS {
			return false
		}
		if copied[suffix].contentHash != after[suffix].contentHash {
			return false
		}
	}
	return true
}

func verifySQLiteSnapshot(ctx context.Context, path string) error {
	db, err := store.OpenReadOnly(ctx, path)
	if err != nil {
		return err
	}
	defer db.Close()
	var result string
	if err := db.DB().QueryRowContext(ctx, `pragma quick_check`).Scan(&result); err != nil {
		return err
	}
	if result != "ok" {
		return fmt.Errorf("SQLite snapshot quick check: %s", result)
	}
	return nil
}

func copyFileWithHash(source, dest string, mode os.FileMode) ([sha256.Size]byte, error) {
	var outHash [sha256.Size]byte
	in, err := os.Open(source)
	if err != nil {
		return outHash, fmt.Errorf("open %s: %w", source, err)
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return outHash, fmt.Errorf("create %s: %w", dest, err)
	}
	digest := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(out, digest), in)
	closeErr := out.Close()
	if copyErr != nil {
		return outHash, fmt.Errorf("copy %s: %w", source, copyErr)
	}
	if closeErr != nil {
		return outHash, fmt.Errorf("close %s: %w", dest, closeErr)
	}
	copy(outHash[:], digest.Sum(nil))
	return outHash, nil
}

func resolveAppleSearchDB(libraryPath string) (string, string, string, error) {
	searchDir := filepath.Join(libraryPath, "database", "search")
	candidates := []struct {
		name    string
		variant string
	}{
		{name: "psi.sqlite", variant: "psi"},
		{name: "leo.sqlite", variant: "leo"},
	}
	for _, candidate := range candidates {
		path := filepath.Join(searchDir, candidate.name)
		info, err := os.Stat(path)
		if err == nil && info.Size() > 0 {
			return path, candidate.variant, filepath.Join("database", "search", candidate.name), nil
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", "", "", fmt.Errorf("inspect Apple search index %s: %w", path, err)
		}
	}
	return "", "", "", fmt.Errorf("find Apple Photos search index: neither %s nor %s exists with content", filepath.Join(searchDir, "psi.sqlite"), filepath.Join(searchDir, "leo.sqlite"))
}

func validatePhotosFacesSchema(ctx context.Context, db *sql.DB) (map[string]string, error) {
	required := map[string][]string{
		"ZPERSON":       {"Z_PK", "ZDISPLAYNAME", "ZFULLNAME", "ZPERSONUUID"},
		"ZDETECTEDFACE": {"Z_PK", "ZUUID", "ZPERSONFORFACE", "ZASSETFORFACE", "ZCENTERX", "ZCENTERY", "ZSIZE", "ZQUALITY", "ZNAMESOURCE", "ZCLOUDNAMESOURCE", "ZSOURCEWIDTH", "ZSOURCEHEIGHT"},
		"ZASSET":        {"Z_PK", "ZUUID"},
	}
	out := map[string]string{}
	for table, columns := range required {
		existing, err := tableColumns(ctx, db, table)
		if err != nil {
			return nil, err
		}
		missing := []string{}
		for _, column := range columns {
			if !existing[column] {
				missing = append(missing, column)
			}
		}
		if len(missing) > 0 {
			return nil, fmt.Errorf("Photos schema table %s missing columns: %s", table, strings.Join(missing, ", "))
		}
		out[table] = strings.Join(columns, ",")
	}
	return out, nil
}

func validatePSISearchSchema(ctx context.Context, db *sql.DB) (map[string]string, error) {
	required := map[string][]string{
		"assets": {"rowid", "uuid_0", "uuid_1"},
		"groups": {"rowid", "category", "owning_groupid", "content_string", "normalized_string", "lookup_identifier", "score"},
		"ga":     {"rowid", "assetid", "groupid"},
	}
	out := map[string]string{}
	for table, columns := range required {
		existing, err := tableColumns(ctx, db, table)
		if err != nil {
			return nil, err
		}
		missing := []string{}
		for _, column := range columns {
			if column == "rowid" {
				continue
			}
			if !existing[column] {
				missing = append(missing, column)
			}
		}
		if len(missing) > 0 {
			return nil, fmt.Errorf("Apple search index table %s missing columns: %s", table, strings.Join(missing, ", "))
		}
		out[table] = strings.Join(columns, ",")
	}
	return out, nil
}

func validateLeoSearchSchema(ctx context.Context, db *sql.DB) (map[string]string, error) {
	required := map[string][]string{
		"lexicon": {"lexeme_id", "type", "category", "content"},
		"items":   {"identifier", "type", "lexeme_ids"},
	}
	out := map[string]string{}
	for table, columns := range required {
		existing, err := tableColumns(ctx, db, table)
		if err != nil {
			return nil, err
		}
		missing := []string{}
		for _, column := range columns {
			if !existing[column] {
				missing = append(missing, column)
			}
		}
		if len(missing) > 0 {
			return nil, fmt.Errorf("Apple search index table %s missing columns: %s", table, strings.Join(missing, ", "))
		}
		out[table] = strings.Join(columns, ",")
	}
	return out, nil
}

func tableColumns(ctx context.Context, db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, "pragma table_info("+store.QuoteIdent(table)+")")
	if err != nil {
		return nil, fmt.Errorf("inspect Photos schema %s: %w", table, err)
	}
	defer rows.Close()
	columns := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, pk int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			return nil, err
		}
		columns[name] = true
	}
	return columns, rows.Err()
}

func archiveAssetMap(ctx context.Context, db *sql.DB) (map[string]string, error) {
	rows, err := db.QueryContext(ctx, `select id, local_identifier from asset`)
	if err != nil {
		return nil, fmt.Errorf("load archive assets: %w", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var assetID, localIdentifier string
		if err := rows.Scan(&assetID, &localIdentifier); err != nil {
			return nil, err
		}
		uuid := normalizeAssetLocalIdentifier(localIdentifier)
		if uuid != "" {
			out[uuid] = assetID
		}
	}
	return out, rows.Err()
}

func normalizeAssetLocalIdentifier(localIdentifier string) string {
	localIdentifier = strings.TrimSpace(localIdentifier)
	if before, _, ok := strings.Cut(localIdentifier, "/"); ok {
		return before
	}
	return localIdentifier
}

func loadPhotosFaceRows(ctx context.Context, db *sql.DB) ([]photosFaceRow, int, int, error) {
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

	rows, err := db.QueryContext(ctx, `
select f.Z_PK,
       coalesce(f.ZUUID, ''),
       coalesce(a.ZUUID, ''),
       trim(coalesce(p.ZDISPLAYNAME, p.ZFULLNAME, '')),
       coalesce(p.ZPERSONUUID, ''),
       coalesce(f.ZNAMESOURCE, 0),
       coalesce(f.ZCLOUDNAMESOURCE, 0),
       cast(f.ZQUALITY as real),
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
`)
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
			&row.centerX,
			&row.centerY,
			&row.size,
			&row.sourceWidth,
			&row.sourceHeight,
		); err != nil {
			return nil, 0, 0, err
		}
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

func loadPSISearchRows(ctx context.Context, db *sql.DB) ([]appleSearchRow, error) {
	rows, err := db.QueryContext(ctx, `
select ga.rowid,
       assets.uuid_0,
       assets.uuid_1,
       groups.rowid as groupid,
       groups.category,
       groups.owning_groupid,
       coalesce(groups.content_string, ''),
       coalesce(groups.normalized_string, ''),
       coalesce(groups.lookup_identifier, ''),
       cast(groups.score as real)
from ga
join groups on groups.rowid = ga.groupid
join assets on ga.assetid = assets.rowid
order by ga.rowid
`)
	if err != nil {
		return nil, fmt.Errorf("load Apple search index rows: %w", err)
	}
	defer rows.Close()
	out := []appleSearchRow{}
	for rows.Next() {
		var row appleSearchRow
		if err := rows.Scan(
			&row.gaRowID,
			&row.uuid0,
			&row.uuid1,
			&row.groupID,
			&row.category,
			&row.owningGroupID,
			&row.contentString,
			&row.normalizedString,
			&row.lookupIdentifier,
			&row.score,
		); err != nil {
			return nil, err
		}
		row.assetUUID = psiIntsToUUID(row.uuid0, row.uuid1)
		row.searchVariant = "psi"
		row.contentString = stripAppleSearchString(row.contentString)
		row.normalizedString = stripAppleSearchString(row.normalizedString)
		row.lookupIdentifier = stripAppleSearchString(row.lookupIdentifier)
		out = append(out, row)
	}
	return out, rows.Err()
}

type leoLexeme struct {
	category int
	content  string
}

func loadLeoSearchRows(ctx context.Context, db *sql.DB) ([]appleSearchRow, error) {
	lexemeRows, err := db.QueryContext(ctx, `
select lexeme_id, type, category, coalesce(content, '')
from lexicon
order by lexeme_id
`)
	if err != nil {
		return nil, fmt.Errorf("load Apple leo lexicon: %w", err)
	}
	lexemes := map[uint32]leoLexeme{}
	for lexemeRows.Next() {
		var id uint32
		var lexemeType, category int
		var content string
		if err := lexemeRows.Scan(&id, &lexemeType, &category, &content); err != nil {
			lexemeRows.Close()
			return nil, err
		}
		mappedCategory, ok := leoCategoryToPhotos8(category)
		content = stripAppleSearchString(content)
		if ok && lexemeType == 1 && content != "" {
			lexemes[id] = leoLexeme{category: mappedCategory, content: content}
		}
	}
	if err := lexemeRows.Close(); err != nil {
		return nil, err
	}
	if err := lexemeRows.Err(); err != nil {
		return nil, err
	}

	itemRows, err := db.QueryContext(ctx, `
select rowid, coalesce(identifier, ''), lexeme_ids
from items
where type = 1
order by rowid
`)
	if err != nil {
		return nil, fmt.Errorf("load Apple leo items: %w", err)
	}
	defer itemRows.Close()
	out := []appleSearchRow{}
	var observationRowID int64
	for itemRows.Next() {
		var itemRowID int64
		var identifier string
		var lexemeIDs []byte
		if err := itemRows.Scan(&itemRowID, &identifier, &lexemeIDs); err != nil {
			return nil, err
		}
		assetUUID := strings.ToUpper(stripAppleSearchString(identifier))
		if assetUUID == "" {
			continue
		}
		for _, lexemeID := range decodeLeoLexemeIDs(lexemeIDs) {
			lexeme, ok := lexemes[lexemeID]
			if !ok {
				continue
			}
			observationRowID++
			out = append(out, appleSearchRow{
				searchVariant:    "leo",
				gaRowID:          observationRowID,
				assetUUID:        assetUUID,
				groupID:          int64(lexemeID),
				category:         lexeme.category,
				contentString:    lexeme.content,
				normalizedString: strings.ToLower(lexeme.content),
			})
		}
	}
	return out, itemRows.Err()
}

func decodeLeoLexemeIDs(data []byte) []uint32 {
	count := len(data) / 4
	out := make([]uint32, 0, count)
	for offset := 0; offset+4 <= len(data); offset += 4 {
		out = append(out, binary.LittleEndian.Uint32(data[offset:offset+4]))
	}
	return out
}

func leoCategoryToPhotos8(category int) (int, bool) {
	// Apple changed category ids with leo.sqlite. Keep this mapping aligned with
	// osxphotos/photosdb/_photosdb_process_searchinfo.py:LEO_CATEGORY_TO_PHOTOS8.
	mapped, ok := map[int]int{
		1010: 1100,
		1020: 1101,
		1030: 1103,
		1040: 1104,
		1050: 1106,
		1060: 1107,
		2030: 1701,
		2050: 2,
		2060: 1,
		2070: 3,
		2090: 5,
		2110: 7,
		2140: 10,
		2150: 11,
		2160: 12,
		3001: 1300,
		4000: 1500,
		4010: 1510,
		4120: 1203,
		5000: 1900,
		5020: 1902,
		6000: 2300,
		7000: 1201,
		7010: 1400,
		8000: 2000,
		8050: 2100,
		8070: 1200,
		8080: 1202,
	}[category]
	return mapped, ok
}

func clearImportedFaces(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `
delete from observation_fts
where id in (
  select id from face_observation where source = ?
)
`, photosLibraryDBFaceSource); err != nil {
		return fmt.Errorf("clear imported face fts: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `delete from face_observation where source = ?`, photosLibraryDBFaceSource); err != nil {
		return fmt.Errorf("clear imported faces: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `delete from evidence_ref where source = ? and evidence_kind = ?`, photosLibraryDBFaceSource, "face_observation"); err != nil {
		return fmt.Errorf("clear imported face evidence: %w", err)
	}
	return nil
}

func clearImportedSearchIndex(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `
delete from observation_fts
where id in (
  select id from visual_observation where source = ?
  union
  select id from face_observation where source = ?
)
`, photosSearchIndexSource, photosSearchIndexSource); err != nil {
		return fmt.Errorf("clear Apple search index fts: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `delete from visual_observation where source = ?`, photosSearchIndexSource); err != nil {
		return fmt.Errorf("clear Apple search index visual observations: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `delete from face_observation where source = ?`, photosSearchIndexSource); err != nil {
		return fmt.Errorf("clear Apple search index person observations: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `delete from evidence_ref where source = ? and evidence_kind = ?`, photosSearchIndexSource, "apple_search_index"); err != nil {
		return fmt.Errorf("clear Apple search index evidence: %w", err)
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
insert into face_observation(id, asset_id, face_local_id, person_label, confidence, bounding_box_json, source, evidence_id)
values (?, ?, ?, ?, ?, ?, ?, ?)
`, observationID, assetID, faceLocalID, face.personLabel, 1.0, boundingJSON, photosLibraryDBFaceSource, evidenceID); err != nil {
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

func insertImportedSearchObservation(ctx context.Context, tx *sql.Tx, assetID string, row appleSearchRow, category appleSearchCategory, person photosPerson, hasPerson bool, importedAt time.Time) error {
	label := row.contentString
	if strings.TrimSpace(label) == "" {
		label = row.normalizedString
	}
	if strings.TrimSpace(label) == "" {
		label = row.lookupIdentifier
	}
	if strings.TrimSpace(label) == "" {
		label = fmt.Sprintf("category-%d-group-%d", row.category, row.groupID)
	}
	confidence := 1.0
	if row.score.Valid && row.score.Float64 > 0 {
		confidence = row.score.Float64
	}
	searchVariant := row.searchVariant
	if searchVariant == "" {
		searchVariant = "psi"
	}
	searchDatabase := "database/search/" + searchVariant + ".sqlite"
	evidenceID := stableID("evidence", assetID, photosSearchIndexSource, fmt.Sprint(row.gaRowID), fmt.Sprint(row.groupID))
	evidenceJSON, err := jsonText(map[string]any{
		"schema_source":              searchDatabase,
		"table":                      map[string]string{"psi": "ga", "leo": "items"}[searchVariant],
		"ga_rowid":                   row.gaRowID,
		"groupid":                    row.groupID,
		"category_id":                row.category,
		"category_name":              category.Name,
		"semantic_kind":              category.SemanticKind,
		"owning_groupid":             nullableSQLInt(row.owningGroupID),
		"content_string":             row.contentString,
		"normalized_string":          row.normalizedString,
		"lookup_identifier":          row.lookupIdentifier,
		"score":                      nullableSQLFloat(row.score),
		"asset_uuid":                 row.assetUUID,
		"uuid_0":                     row.uuid0,
		"uuid_1":                     row.uuid1,
		"person_lookup_joined":       hasPerson,
		"person_lookup_display_name": person.displayName,
		"person_lookup_full_name":    person.fullName,
		"imported_at":                importedAt.Format(time.RFC3339Nano),
		"read_only":                  true,
	})
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
insert into evidence_ref(id, asset_id, evidence_kind, source, pointer, value_json)
values (?, ?, ?, ?, ?, ?)
`, evidenceID, assetID, "apple_search_index", photosSearchIndexSource, fmt.Sprintf("%s:%d:group:%d:category:%d", searchDatabase, row.gaRowID, row.groupID, row.category), evidenceJSON); err != nil {
		return fmt.Errorf("write Apple search index evidence: %w", err)
	}
	observationID := stableID("visual_observation", assetID, photosSearchIndexSource, fmt.Sprint(row.gaRowID), fmt.Sprint(row.groupID), label)
	if _, err := tx.ExecContext(ctx, `
insert into visual_observation(id, asset_id, observation_type, label, confidence, bounding_box_json, source, model_id, evidence_id)
values (?, ?, ?, ?, ?, '{}', ?, ?, ?)
`, observationID, assetID, category.SemanticKind, label, confidence, photosSearchIndexSource, photosSearchIndexModelID, evidenceID); err != nil {
		return fmt.Errorf("write Apple search index visual observation: %w", err)
	}
	body := strings.Join(nonEmpty(category.SemanticKind, category.Name, label, row.normalizedString, row.lookupIdentifier, photosSearchIndexSource), " ")
	if _, err := tx.ExecContext(ctx, `
insert into observation_fts(id, asset_id, title, body)
values (?, ?, ?, ?)
`, observationID, assetID, label, body); err != nil {
		return fmt.Errorf("write Apple search index fts: %w", err)
	}
	if category.Name != "PERSON" {
		return nil
	}
	faceLocalID := "psi:person:" + row.lookupIdentifier
	if strings.TrimSpace(row.lookupIdentifier) == "" {
		faceLocalID = fmt.Sprintf("psi:person:group:%d", row.groupID)
	}
	faceObservationID := stableID("face_observation", assetID, photosSearchIndexSource, faceLocalID, label, fmt.Sprint(row.gaRowID))
	if _, err := tx.ExecContext(ctx, `
insert into face_observation(id, asset_id, face_local_id, person_label, confidence, bounding_box_json, source, evidence_id)
values (?, ?, ?, ?, ?, '{}', ?, ?)
`, faceObservationID, assetID, faceLocalID, label, confidence, photosSearchIndexSource, evidenceID); err != nil {
		return fmt.Errorf("write Apple search index person observation: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
insert into observation_fts(id, asset_id, title, body)
values (?, ?, ?, ?)
`, faceObservationID, assetID, label, strings.Join(nonEmpty("person", "face", "apple_search_index", label, row.lookupIdentifier), " ")); err != nil {
		return fmt.Errorf("write Apple search index person fts: %w", err)
	}
	return nil
}

func appleSearchCategoryForID(id int) appleSearchCategory {
	if category, ok := appleSearchCategoriesPhotos8()[id]; ok {
		return category
	}
	return appleSearchCategory{ID: id, Name: "UNKNOWN", SemanticKind: fmt.Sprintf("apple_search_category_%d", id)}
}

// Category ids come from osxphotos/_constants.py:SearchCategory_Photos8.
// Missing ids deliberately fall through to UNKNOWN so new Apple categories still import.
func appleSearchCategoriesPhotos8() map[int]appleSearchCategory {
	categories := map[int]appleSearchCategory{
		1:    {ID: 1, Name: "PLACE_NAME", SemanticKind: "apple_place"},
		2:    {ID: 2, Name: "STREET", SemanticKind: "apple_place"},
		3:    {ID: 3, Name: "NEIGHBORHOOD", SemanticKind: "apple_place"},
		4:    {ID: 4, Name: "LOCALITY_4", SemanticKind: "apple_place"},
		5:    {ID: 5, Name: "CITY", SemanticKind: "apple_place"},
		6:    {ID: 6, Name: "SUB_LOCALITY_6", SemanticKind: "apple_place"},
		7:    {ID: 7, Name: "NAMED_AREA", SemanticKind: "apple_place"},
		8:    {ID: 8, Name: "LOCALITY_8", SemanticKind: "apple_place"},
		10:   {ID: 10, Name: "STATE", SemanticKind: "apple_place"},
		11:   {ID: 11, Name: "STATE_ABBREVIATION", SemanticKind: "apple_place"},
		12:   {ID: 12, Name: "COUNTRY", SemanticKind: "apple_place"},
		14:   {ID: 14, Name: "BODY_OF_WATER", SemanticKind: "apple_place"},
		1000: {ID: 1000, Name: "HOME", SemanticKind: "apple_place"},
		1001: {ID: 1001, Name: "WORK", SemanticKind: "apple_place"},
		1100: {ID: 1100, Name: "MONTH", SemanticKind: "apple_date"},
		1101: {ID: 1101, Name: "YEAR", SemanticKind: "apple_date"},
		1103: {ID: 1103, Name: "HOLIDAY", SemanticKind: "apple_date"},
		1104: {ID: 1104, Name: "SEASON", SemanticKind: "apple_date"},
		1106: {ID: 1106, Name: "TIME_OF_DAY", SemanticKind: "apple_date"},
		1107: {ID: 1107, Name: "WEEKPART", SemanticKind: "apple_date"},
		1200: {ID: 1200, Name: "KEYWORDS", SemanticKind: "apple_keyword"},
		1201: {ID: 1201, Name: "TITLE", SemanticKind: "apple_title"},
		1202: {ID: 1202, Name: "DESCRIPTION", SemanticKind: "apple_description"},
		1203: {ID: 1203, Name: "DETECTED_TEXT", SemanticKind: "apple_text"},
		1205: {ID: 1205, Name: "TEXT_FOUND", SemanticKind: "apple_text"},
		1300: {ID: 1300, Name: "PERSON", SemanticKind: "apple_person"},
		1400: {ID: 1400, Name: "ALBUM", SemanticKind: "apple_album"},
		1500: {ID: 1500, Name: "LABEL", SemanticKind: "apple_label"},
		1510: {ID: 1510, Name: "RICH_LABEL", SemanticKind: "apple_label"},
		1600: {ID: 1600, Name: "ACTIVITY", SemanticKind: "apple_activity"},
		1700: {ID: 1700, Name: "VENUE", SemanticKind: "apple_venue"},
		1701: {ID: 1701, Name: "VENUE_TYPE", SemanticKind: "apple_venue"},
		1900: {ID: 1900, Name: "PHOTO_TYPE_PHOTO", SemanticKind: "apple_media_type"},
		1901: {ID: 1901, Name: "PHOTO_TYPE_VIDEO", SemanticKind: "apple_media_type"},
		1902: {ID: 1902, Name: "PHOTO_TYPE_RAW", SemanticKind: "apple_media_type"},
		1905: {ID: 1905, Name: "PHOTO_TYPE_SLOMO", SemanticKind: "apple_media_type"},
		1906: {ID: 1906, Name: "PHOTO_TYPE_LIVE", SemanticKind: "apple_media_type"},
		1907: {ID: 1907, Name: "PHOTO_TYPE_SCREENSHOT", SemanticKind: "apple_media_type"},
		1908: {ID: 1908, Name: "PHOTO_TYPE_PANORAMA", SemanticKind: "apple_media_type"},
		1909: {ID: 1909, Name: "PHOTO_TYPE_TIMELAPSE", SemanticKind: "apple_media_type"},
		1912: {ID: 1912, Name: "PHOTO_TYPE_ANIMATED", SemanticKind: "apple_media_type"},
		1913: {ID: 1913, Name: "PHOTO_TYPE_BURSTS", SemanticKind: "apple_media_type"},
		1914: {ID: 1914, Name: "PHOTO_TYPE_PORTRAIT", SemanticKind: "apple_media_type"},
		1915: {ID: 1915, Name: "PHOTO_TYPE_SELFIES", SemanticKind: "apple_media_type"},
		1916: {ID: 1916, Name: "PHOTO_TYPE_SCREENRECORDINGS", SemanticKind: "apple_media_type"},
		2000: {ID: 2000, Name: "PHOTO_TYPE_FAVORITES", SemanticKind: "apple_media_type"},
		2100: {ID: 2100, Name: "PHOTO_NAME", SemanticKind: "apple_photo_name"},
		2200: {ID: 2200, Name: "SOURCE", SemanticKind: "apple_source"},
		2300: {ID: 2300, Name: "CAMERA", SemanticKind: "apple_camera"},
	}
	return categories
}

func psiIntsToUUID(uuid0, uuid1 int64) string {
	var bytes [16]byte
	binary.LittleEndian.PutUint64(bytes[0:8], uint64(uuid0))
	binary.LittleEndian.PutUint64(bytes[8:16], uint64(uuid1))
	return fmt.Sprintf("%X-%X-%X-%X-%X", bytes[0:4], bytes[4:6], bytes[6:8], bytes[8:10], bytes[10:16])
}

func stripAppleSearchString(value string) string {
	return strings.TrimSpace(strings.ReplaceAll(value, "\x00", ""))
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

func nullableSQLFloat(value sql.NullFloat64) any {
	if !value.Valid {
		return nil
	}
	return value.Float64
}

func nullableSQLInt(value sql.NullInt64) any {
	if !value.Valid {
		return nil
	}
	return value.Int64
}
