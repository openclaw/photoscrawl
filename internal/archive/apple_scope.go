package archive

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/openclaw/crawlkit/store"
	"github.com/openclaw/photoscrawl/internal/photos"
)

type appleAssetScope struct {
	libraryID string
	byUUID    map[string]string
}

type libraryQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func libraryIdentity(ctx context.Context, db libraryQuerier, requested string) (string, error) {
	requested, err := libraryComparisonPath(requested)
	if err != nil {
		return "", err
	}
	rows, err := db.QueryContext(ctx, `select id, library_path from source_library`)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	match := ""
	for rows.Next() {
		var id, path string
		if err := rows.Scan(&id, &path); err != nil {
			return "", err
		}
		path, err = libraryComparisonPath(path)
		if err != nil {
			return "", err
		}
		if path != requested {
			continue
		}
		if match != "" {
			return "", errors.New("multiple archived libraries match the requested library")
		}
		match = id
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if match == "" {
		return "", errors.New("requested library is not in the archive; crawl it before importing Apple observations")
	}
	return match, nil
}

func libraryComparisonPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(absolute); err == nil {
		return resolved, nil
	}
	return absolute, nil
}

func openAppleArchive(ctx context.Context, path, libraryPath string) (*store.Store, error) {
	// Check the binding on a private copy before a writable open can migrate or
	// change permissions. The transaction resolves it again before replacement.
	private, cleanup, err := photos.CopySQLite(ctx, path, "photoscrawl-library-preflight-")
	if err != nil {
		return nil, err
	}
	defer cleanup()
	db, err := store.OpenReadOnly(ctx, private)
	if err != nil {
		return nil, err
	}
	_, bindingErr := libraryIdentity(ctx, db.DB(), libraryPath)
	_ = db.Close()
	if bindingErr != nil {
		return nil, bindingErr
	}
	return openArchiveStore(ctx, path)
}

func archiveAssetMap(ctx context.Context, tx *sql.Tx, libraryPath string) (appleAssetScope, error) {
	libraryID, err := libraryIdentity(ctx, tx, libraryPath)
	if err != nil {
		return appleAssetScope{}, err
	}
	rows, err := tx.QueryContext(ctx, `select id, local_identifier from asset where source_library_id = ?`, libraryID)
	if err != nil {
		return appleAssetScope{}, fmt.Errorf("load archive assets: %w", err)
	}
	defer rows.Close()
	out := appleAssetScope{libraryID: libraryID, byUUID: map[string]string{}}
	for rows.Next() {
		var assetID, localIdentifier string
		if err := rows.Scan(&assetID, &localIdentifier); err != nil {
			return appleAssetScope{}, err
		}
		uuid := normalizeAssetLocalIdentifier(localIdentifier)
		if uuid == "" {
			continue
		}
		if previous, exists := out.byUUID[uuid]; exists && previous != assetID {
			return appleAssetScope{}, errors.New("ambiguous asset identifiers in the requested library")
		}
		out.byUUID[uuid] = assetID
	}
	return out, rows.Err()
}
