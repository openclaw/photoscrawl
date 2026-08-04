package archive

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/openclaw/photoscrawl/internal/photos"
)

const photoKitOriginalExportSource = "photokit_original_export"

type ExportResult struct {
	AssetID  string `json:"asset_id"`
	Path     string `json:"path"`
	Source   string `json:"source"`
	Original bool   `json:"original"`
}

type originalExporter func(context.Context, string, string, bool) error

func Export(ctx context.Context, paths Paths, rowID, outputDir string) (ExportResult, error) {
	return exportOriginal(ctx, paths, rowID, outputDir, photos.ExportOriginalResource)
}

func exportOriginal(ctx context.Context, paths Paths, rowID, outputDir string, exporter originalExporter) (ExportResult, error) {
	rowID = strings.TrimSpace(rowID)
	if rowID == "" {
		return ExportResult{}, errors.New("id is required")
	}
	outputDir = strings.TrimSpace(outputDir)
	if outputDir == "" {
		return ExportResult{}, errors.New("output is required")
	}

	db, err := openArchiveReadOnly(ctx, paths.Database)
	if err != nil {
		return ExportResult{}, err
	}
	defer db.Close()

	var localIdentifier string
	var originalFilename sql.NullString
	err = db.DB().QueryRowContext(ctx, `
select asset.local_identifier,
       (
         select asset_resource.original_filename
         from asset_resource
         where asset_resource.asset_id = asset.id
           and asset_resource.deleted_at is null
           and trim(asset_resource.original_filename) <> ''
           and asset_resource.resource_type in ('original', 'photo')
         order by case asset_resource.resource_type when 'original' then 0 else 1 end,
                  asset_resource.id
         limit 1
       )
from asset
where asset.id = ?
`, rowID).Scan(&localIdentifier, &originalFilename)
	if errors.Is(err, sql.ErrNoRows) {
		return ExportResult{}, fmt.Errorf("asset not found: %s", rowID)
	}
	if err != nil {
		return ExportResult{}, fmt.Errorf("load asset for export: %w", err)
	}

	absOutputDir, err := filepath.Abs(outputDir)
	if err != nil {
		return ExportResult{}, fmt.Errorf("resolve export output directory: %w", err)
	}
	destinationPath := filepath.Join(absOutputDir, exportFilename(rowID, originalFilename.String))
	if err := exporter(ctx, localIdentifier, destinationPath, true); err != nil {
		return ExportResult{}, fmt.Errorf("export asset %s: %w", rowID, err)
	}

	return ExportResult{
		AssetID:  rowID,
		Path:     destinationPath,
		Source:   photoKitOriginalExportSource,
		Original: true,
	}, nil
}

func exportFilename(rowID, originalFilename string) string {
	filename := filepath.Base(strings.ReplaceAll(strings.TrimSpace(originalFilename), "\\", "/"))
	if filename != "" && filename != "." && filename != string(filepath.Separator) {
		return filename
	}
	fallback := strings.TrimPrefix(rowID, "asset:")
	if fallback = filepath.Base(strings.ReplaceAll(fallback, "\\", "/")); fallback == "" || fallback == "." || fallback == string(filepath.Separator) {
		fallback = "asset"
	}
	return fallback + ".bin"
}
