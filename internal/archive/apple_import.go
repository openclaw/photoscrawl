package archive

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/openclaw/crawlkit/store"
	"github.com/openclaw/photoscrawl/internal/photos"
)

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

func ImportApple(ctx context.Context, paths Paths, opts ImportAppleOptions) (ImportAppleResult, error) {
	importedAt := time.Now().UTC()
	libraryPath, err := resolveFacesLibraryPath(ctx, paths, opts.LibraryPath)
	if err != nil {
		return ImportAppleResult{}, err
	}
	facesInput, metadataInput, err := preflightPhotosImports(ctx, libraryPath)
	if err != nil {
		return ImportAppleResult{}, err
	}
	searchInput, err := preflightSearchImport(ctx, libraryPath)
	if err != nil {
		return ImportAppleResult{}, err
	}
	defer searchInput.cleanup()
	archiveDB, err := openAppleArchive(ctx, paths.Database, libraryPath)
	if err != nil {
		return ImportAppleResult{}, err
	}
	defer archiveDB.Close()
	tx, err := archiveDB.DB().BeginTx(ctx, nil)
	if err != nil {
		return ImportAppleResult{}, err
	}
	defer tx.Rollback()
	assetByUUID, err := archiveAssetMap(ctx, tx, libraryPath)
	if err != nil {
		return ImportAppleResult{}, err
	}
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
	return photos.CopySQLite(ctx, liveDBPath, "photoscrawl-faces-")
}
