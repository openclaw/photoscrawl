package archive

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/openclaw/crawlkit/store"
	"github.com/openclaw/photoscrawl/internal/photos"
)

const photosSearchIndexSource = "photos_search_index"

const photosSearchIndexModelID = "apple.photos.search-index"

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

type searchImportInput struct {
	variant      string
	relPath      string
	schema       map[string]string
	snapshotPath string
	cleanup      func()
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
	defer input.cleanup()
	archiveDB, err := openAppleArchive(ctx, paths.Database, libraryPath)
	if err != nil {
		return ImportSearchIndexResult{}, err
	}
	defer archiveDB.Close()

	peopleByUUID, err := loadPhotosPeopleByUUID(ctx, libraryPath)
	if err != nil {
		return ImportSearchIndexResult{}, err
	}
	tx, err := archiveDB.DB().BeginTx(ctx, nil)
	if err != nil {
		return ImportSearchIndexResult{}, err
	}
	defer tx.Rollback()
	assetByUUID, err := archiveAssetMap(ctx, tx, libraryPath)
	if err != nil {
		return ImportSearchIndexResult{}, err
	}
	result, _, err := writeSearchImport(ctx, tx, input, assetByUUID, peopleByUUID, time.Now().UTC())
	if err != nil {
		return ImportSearchIndexResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return ImportSearchIndexResult{}, err
	}
	return result, nil
}

func preflightSearchImport(ctx context.Context, libraryPath string) (searchImportInput, error) {
	searchDBPath, variant, relPath, err := resolveAppleSearchDB(libraryPath)
	if err != nil {
		return searchImportInput{}, err
	}
	snapshotPath, cleanup, err := photos.CopySQLite(ctx, searchDBPath, "photoscrawl-search-index-")
	if err != nil {
		return searchImportInput{}, err
	}
	searchDB, err := store.OpenReadOnly(ctx, snapshotPath)
	if err != nil {
		cleanup()
		return searchImportInput{}, fmt.Errorf("open copied Apple search index: %w", err)
	}
	input := searchImportInput{variant: variant, relPath: relPath, snapshotPath: snapshotPath, cleanup: cleanup}
	switch variant {
	case "psi":
		input.schema, err = validatePSISearchSchema(ctx, searchDB.DB())
	case "leo":
		input.schema, err = validateLeoSearchSchema(ctx, searchDB.DB())
	default:
		err = fmt.Errorf("unsupported Apple search index variant: %s", variant)
	}
	if closeErr := searchDB.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		cleanup()
		return searchImportInput{}, err
	}
	return input, nil
}

func writeSearchImport(ctx context.Context, tx *sql.Tx, input searchImportInput, assetByUUID appleAssetScope, peopleByUUID map[string]photosPerson, importedAt time.Time) (ImportSearchIndexResult, map[string]bool, error) {
	stage, err := beginAppleStage(ctx, tx, appleRecordSearch)
	if err != nil {
		return ImportSearchIndexResult{}, nil, err
	}
	defer stage.close()
	searchDB, err := store.OpenReadOnly(ctx, input.snapshotPath)
	if err != nil {
		return ImportSearchIndexResult{}, nil, fmt.Errorf("open copied Apple search index: %w", err)
	}
	defer searchDB.Close()
	result := ImportSearchIndexResult{
		Source:         photosSearchIndexSource,
		Variant:        input.variant,
		SearchDatabase: input.relPath,
		Schema:         input.schema,
		CategoriesSeen: map[string]int{},
	}
	touched := map[string]bool{}
	unknownCategories := map[int]bool{}
	err = visitAppleSearchRows(ctx, searchDB.DB(), input.variant, func(row appleSearchRow) error {
		result.GroupsRead++
		category := appleSearchCategoryForID(row.category)
		result.CategoriesSeen[fmt.Sprint(row.category)]++
		if category.Name == "UNKNOWN" {
			result.UnknownCategoryRows++
			unknownCategories[row.category] = true
		} else {
			result.KnownCategoryRows++
		}
		assetID, ok := assetByUUID.byUUID[row.assetUUID]
		if !ok {
			result.GroupsUnresolved++
			return nil
		}
		person, hasPerson := peopleByUUID[strings.ToUpper(row.lookupIdentifier)]
		if category.Name == "PERSON" {
			if hasPerson {
				result.PersonLookupIdentifiersResolved++
			} else {
				result.PersonLookupIdentifiersUnresolved++
			}
		}
		record, err := importedSearchRecord(assetID, row, category, person, hasPerson, importedAt)
		if err != nil {
			return err
		}
		if err := stage.add(ctx, record); err != nil {
			return err
		}
		result.GroupsResolved++
		result.VisualObservationsInserted++
		if category.Name == "PERSON" {
			result.PersonFaceObservationsInserted++
		}
		touched[assetID] = true
		return nil
	})
	if err != nil {
		return ImportSearchIndexResult{}, nil, err
	}
	if err := stage.apply(ctx, assetByUUID.libraryID); err != nil {
		return ImportSearchIndexResult{}, nil, err
	}
	result.AssetsTouched = len(touched)
	for id := range unknownCategories {
		result.UnknownCategories = append(result.UnknownCategories, id)
	}
	sort.Ints(result.UnknownCategories)
	return result, touched, nil
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

func importedSearchRecord(assetID string, row appleSearchRow, category appleSearchCategory, person photosPerson, hasPerson bool, importedAt time.Time) (stagedSearchRecord, error) {
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
	evidence := map[string]any{
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
		"read_only":                  true,
	}
	evidenceStableJSON, err := jsonText(evidence)
	if err != nil {
		return stagedSearchRecord{}, err
	}
	evidence["imported_at"] = importedAt.Format(time.RFC3339Nano)
	evidenceJSON, err := jsonText(evidence)
	if err != nil {
		return stagedSearchRecord{}, err
	}
	observationID := stableID("visual_observation", assetID, photosSearchIndexSource, fmt.Sprint(row.gaRowID), fmt.Sprint(row.groupID), label)
	body := strings.Join(nonEmpty(category.SemanticKind, category.Name, label, row.normalizedString, row.lookupIdentifier, photosSearchIndexSource), " ")
	record := stagedSearchRecord{AssetID: assetID, EvidenceID: evidenceID, EvidencePointer: fmt.Sprintf("%s:%d:group:%d:category:%d", searchDatabase, row.gaRowID, row.groupID, row.category), EvidenceJSON: evidenceJSON, EvidenceStableJSON: evidenceStableJSON, VisualID: observationID, ObservationType: category.SemanticKind, Label: label, Confidence: confidence, VisualFTSTitle: label, VisualFTSBody: body}
	if category.Name != "PERSON" {
		return record, nil
	}
	faceLocalID := "psi:person:" + row.lookupIdentifier
	if strings.TrimSpace(row.lookupIdentifier) == "" {
		faceLocalID = fmt.Sprintf("psi:person:group:%d", row.groupID)
	}
	faceObservationID := stableID("face_observation", assetID, photosSearchIndexSource, faceLocalID, label, fmt.Sprint(row.gaRowID))
	record.FaceID = faceObservationID
	record.FaceLocalID = faceLocalID
	record.FaceLabel = label
	record.FaceConfidence = &confidence
	record.FaceFTSTitle = label
	record.FaceFTSBody = strings.Join(nonEmpty("person", "face", "apple_search_index", label, row.lookupIdentifier), " ")
	return record, nil
}
