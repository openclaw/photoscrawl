package archive

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/openclaw/crawlkit/store"
	"github.com/openclaw/photoscrawl/internal/photos"
)

type ExportOptions struct {
	ID                   string
	Output               string
	AllowICloudDownloads bool
	ExportOriginal       func(context.Context, string, string, bool) error
}

type ExportResult struct {
	ID                   string `json:"id"`
	LocalIdentifier      string `json:"local_identifier"`
	Output               string `json:"output"`
	Bytes                int64  `json:"bytes"`
	AllowICloudDownloads bool   `json:"allow_icloud_downloads"`
	Source               string `json:"source"`
	Original             bool   `json:"original"`
}

func Export(ctx context.Context, paths Paths, opts ExportOptions) (ExportResult, error) {
	id := strings.TrimSpace(opts.ID)
	if id == "" {
		return ExportResult{}, errors.New("id is required")
	}
	outputPath := strings.TrimSpace(opts.Output)
	if outputPath == "" {
		return ExportResult{}, errors.New("output is required")
	}
	exportOriginal := opts.ExportOriginal
	if exportOriginal == nil {
		exportOriginal = photos.ExportOriginalResource
	}

	db, err := store.OpenReadOnly(ctx, paths.Database)
	if err != nil {
		return ExportResult{}, err
	}
	defer db.Close()

	asset, err := exportAsset(ctx, db.DB(), id)
	if err != nil {
		return ExportResult{}, err
	}
	if asset.MediaType != "image" {
		return ExportResult{}, fmt.Errorf("export currently supports image assets only: %s", id)
	}
	resolvedOutput, err := resolveExportOutput(outputPath, asset)
	if err != nil {
		return ExportResult{}, err
	}
	source := "photokit_original_export"
	original := true
	if asset.OriginalLocalPath != "" {
		if err := copyLocalResource(ctx, asset.OriginalLocalPath, resolvedOutput); err != nil {
			if exportErr := exportOriginalAtomic(ctx, exportOriginal, asset.LocalIdentifier, resolvedOutput, opts.AllowICloudDownloads); exportErr != nil {
				if asset.LocalPath == "" || asset.LocalPath == asset.OriginalLocalPath {
					return ExportResult{}, fmt.Errorf("copy local original failed: %v; PhotoKit original export failed: %w", err, exportErr)
				}
				if copyErr := copyLocalResource(ctx, asset.LocalPath, resolvedOutput); copyErr != nil {
					return ExportResult{}, fmt.Errorf("copy local original failed: %v; PhotoKit original export failed: %v; fallback local resource copy failed: %w", err, exportErr, copyErr)
				}
				source = "local_package_resource_copy"
				original = false
			}
		} else {
			source = "local_original_copy"
		}
	} else {
		if err := exportOriginalAtomic(ctx, exportOriginal, asset.LocalIdentifier, resolvedOutput, opts.AllowICloudDownloads); err != nil {
			if asset.LocalPath == "" {
				return ExportResult{}, err
			}
			if copyErr := copyLocalResource(ctx, asset.LocalPath, resolvedOutput); copyErr != nil {
				return ExportResult{}, fmt.Errorf("%w; fallback local resource copy failed: %v", err, copyErr)
			}
			source = "local_package_resource_copy"
			original = false
		}
	}
	info, err := os.Stat(resolvedOutput)
	if err != nil {
		return ExportResult{}, err
	}
	return ExportResult{
		ID:                   id,
		LocalIdentifier:      asset.LocalIdentifier,
		Output:               resolvedOutput,
		Bytes:                info.Size(),
		AllowICloudDownloads: opts.AllowICloudDownloads,
		Source:               source,
		Original:             original,
	}, nil
}

type exportAssetRow struct {
	LocalIdentifier   string
	MediaType         string
	OriginalFilename  string
	OriginalLocalPath string
	LocalPath         string
}

func exportAsset(ctx context.Context, db *sql.DB, id string) (exportAssetRow, error) {
	var asset exportAssetRow
	err := db.QueryRowContext(ctx, `
select asset.local_identifier, asset.media_type, coalesce((
  select original_filename
  from asset_resource
  where asset_id = asset.id and original_filename <> ''
  order by case
    when lower(resource_type) like '%photo%' then 0
    when lower(resource_type) like '%full%' then 1
    else 2
  end, original_filename
  limit 1
), ''), coalesce((
  select local_path
  from asset_resource
  where asset_id = asset.id and local_path <> '' and local_path like '%/originals/%'
  order by available_locally desc, case
    when lower(resource_type) like '%photo%' then 0
    when lower(resource_type) like '%full%' then 1
    else 2
  end, original_filename
  limit 1
), ''), coalesce((
  select local_path
  from asset_resource
  where asset_id = asset.id and local_path <> ''
  order by available_locally desc, case
    when lower(resource_type) like '%photo%' then 0
    when lower(resource_type) like '%full%' then 1
    else 2
  end, original_filename
  limit 1
), '')
from asset
where asset.id = ?
`, id).Scan(&asset.LocalIdentifier, &asset.MediaType, &asset.OriginalFilename, &asset.OriginalLocalPath, &asset.LocalPath)
	if errors.Is(err, sql.ErrNoRows) {
		return exportAssetRow{}, fmt.Errorf("asset not found: %s", id)
	}
	return asset, err
}

func resolveExportOutput(outputPath string, asset exportAssetRow) (string, error) {
	if strings.HasSuffix(outputPath, string(os.PathSeparator)) {
		return filepath.Join(outputPath, exportFilename(asset)), nil
	}
	info, err := os.Stat(outputPath)
	if err == nil && info.IsDir() {
		return filepath.Join(outputPath, exportFilename(asset)), nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return outputPath, nil
}

func copyLocalResource(ctx context.Context, sourcePath, destinationPath string) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if err := os.MkdirAll(filepath.Dir(destinationPath), 0o755); err != nil {
		return err
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		return fmt.Errorf("open local photo resource: %w", err)
	}
	defer source.Close()
	tempPath := destinationPath + ".tmp"
	destination, err := os.OpenFile(tempPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("create export file: %w", err)
	}
	_, copyErr := io.Copy(destination, source)
	closeErr := destination.Close()
	if copyErr != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("copy local photo resource: %w", copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("close export file: %w", closeErr)
	}
	if err := os.Rename(tempPath, destinationPath); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("replace export file: %w", err)
	}
	return nil
}

func exportOriginalAtomic(ctx context.Context, exportOriginal func(context.Context, string, string, bool) error, localIdentifier, destinationPath string, allowNetwork bool) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if err := os.MkdirAll(filepath.Dir(destinationPath), 0o755); err != nil {
		return err
	}
	tempPath := destinationPath + ".tmp"
	_ = os.Remove(tempPath)
	if err := exportOriginal(ctx, localIdentifier, tempPath, allowNetwork); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	if err := os.Rename(tempPath, destinationPath); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("replace export file: %w", err)
	}
	return nil
}

func exportFilename(asset exportAssetRow) string {
	name := strings.TrimSpace(filepath.Base(asset.OriginalFilename))
	if name != "" && name != "." && name != string(os.PathSeparator) {
		return safeFilename(asset.LocalIdentifier) + "-" + name
	}
	return safeFilename(asset.LocalIdentifier) + ".jpg"
}

func safeFilename(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "photo"
	}
	replacer := strings.NewReplacer(
		"/", "_",
		":", "_",
		"\x00", "_",
	)
	return replacer.Replace(value)
}
