package archive

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	crawlconfig "github.com/openclaw/crawlkit/config"
	"github.com/openclaw/crawlkit/state"
	"github.com/openclaw/photoscrawl/internal/photos"
)

type CrawlOptions struct {
	LibraryPath string
	Provider    photos.Provider
	Now         func() time.Time
}

type CrawlResult struct {
	Database              string `json:"database"`
	Provider              string `json:"provider"`
	SnapshotID            string `json:"snapshot_id"`
	SourceLibraryID       string `json:"source_library_id"`
	AssetsSeen            int    `json:"assets_seen"`
	AssetsNew             int    `json:"assets_new"`
	AssetsChanged         int    `json:"assets_changed"`
	AssetsUnchanged       int    `json:"assets_unchanged"`
	AssetsDeleted         int    `json:"assets_deleted"`
	AssetsRestored        int    `json:"assets_restored"`
	ResourcesSeen         int    `json:"resources_seen"`
	AlbumMembershipsSeen  int    `json:"album_memberships_seen"`
	LocationsSeen         int    `json:"locations_seen"`
	QueuedForClassify     int    `json:"queued_for_classify"`
	QueuedNeedsDownload   int    `json:"queued_needs_download"`
	PreviouslySeenMissing int    `json:"previously_seen_missing"`
}

func Crawl(ctx context.Context, paths Paths, opts CrawlOptions) (CrawlResult, error) {
	if opts.Provider == nil {
		return CrawlResult{}, errors.New("photos provider is required")
	}
	libraryPath := crawlconfig.ExpandHome(strings.TrimSpace(opts.LibraryPath))
	if libraryPath == "" {
		return CrawlResult{}, errors.New("library path is required")
	}
	absLibraryPath, err := filepath.Abs(libraryPath)
	if err != nil {
		return CrawlResult{}, err
	}
	now := opts.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	startedAt := now().UTC()
	snapshot, err := opts.Provider.Snapshot(ctx, absLibraryPath)
	if err != nil {
		return CrawlResult{}, err
	}
	completedAt := now().UTC()
	if snapshot.Provider == "" {
		snapshot.Provider = "unknown"
	}
	if snapshot.LibraryPath == "" {
		snapshot.LibraryPath = absLibraryPath
	}
	if err := photos.AttachLocalMediaPaths(&snapshot, absLibraryPath); err != nil {
		return CrawlResult{}, fmt.Errorf("resolve local Photos media paths: %w", err)
	}
	if err := validateSnapshotResourceSourceIdentifiers(snapshot); err != nil {
		return CrawlResult{}, err
	}

	db, err := openArchiveStore(ctx, paths.Database)
	if err != nil {
		return CrawlResult{}, err
	}
	defer db.Close()

	importer := crawlImporter{
		ctx:         ctx,
		snapshot:    snapshot,
		libraryPath: absLibraryPath,
		startedAt:   startedAt,
		completedAt: completedAt,
	}
	if err := db.WithTx(ctx, importer.run); err != nil {
		return CrawlResult{}, err
	}
	importer.result.Database = paths.Database
	return importer.result, nil
}

type crawlImporter struct {
	ctx         context.Context
	snapshot    photos.LibrarySnapshot
	libraryPath string
	startedAt   time.Time
	completedAt time.Time
	stmts       *crawlStatements
	result      CrawlResult
}

func (c *crawlImporter) run(tx *sql.Tx) error {
	ctx := c.ctx
	sourceID := stableID("source_library", c.libraryPath)
	snapshotID := stableID("crawl_snapshot", sourceID, c.completedAt.Format(time.RFC3339Nano), c.sourceFingerprint())

	resourceCount, albumCount, locationCount := snapshotCounts(c.snapshot)
	metadataJSON, err := jsonText(map[string]any{
		"provider":             c.snapshot.Provider,
		"authorization_status": c.snapshot.AuthorizationStatus,
		"snapshot_metadata":    c.snapshot.Metadata,
	})
	if err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx, `
insert into source_library(id, library_path, snapshot_path, snapshot_created_at, photos_version, metadata_json)
values (?, ?, ?, ?, ?, ?)
on conflict(id) do update set
  library_path = excluded.library_path,
  snapshot_path = excluded.snapshot_path,
  snapshot_created_at = excluded.snapshot_created_at,
  photos_version = excluded.photos_version,
  metadata_json = excluded.metadata_json
`, sourceID, c.libraryPath, "sqlite:crawl_snapshot/"+snapshotID, c.completedAt.Format(time.RFC3339Nano), c.snapshot.PhotosVersion, metadataJSON); err != nil {
		return fmt.Errorf("upsert source library: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
insert into crawl_snapshot(id, source_library_id, started_at, completed_at, provider, asset_count, resource_count, album_membership_count, location_count, metadata_json)
values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, snapshotID, sourceID, c.startedAt.Format(time.RFC3339Nano), c.completedAt.Format(time.RFC3339Nano), c.snapshot.Provider, len(c.snapshot.Assets), resourceCount, albumCount, locationCount, metadataJSON); err != nil {
		return fmt.Errorf("insert crawl snapshot: %w", err)
	}

	c.result = CrawlResult{
		Provider:             c.snapshot.Provider,
		SnapshotID:           snapshotID,
		SourceLibraryID:      sourceID,
		AssetsSeen:           len(c.snapshot.Assets),
		ResourcesSeen:        resourceCount,
		AlbumMembershipsSeen: albumCount,
		LocationsSeen:        locationCount,
	}
	stmts, err := prepareCrawlStatements(ctx, tx)
	if err != nil {
		return err
	}
	defer stmts.close()
	c.stmts = stmts

	for _, asset := range c.snapshot.Assets {
		if strings.TrimSpace(asset.LocalIdentifier) == "" {
			continue
		}
		asset, deleted, err := c.normalizeAssetDeletion(asset)
		if err != nil {
			return err
		}
		assetID := stableID("asset", sourceID, asset.LocalIdentifier)
		fingerprint, err := assetFingerprint(asset)
		if err != nil {
			return err
		}
		previousFingerprint, previouslyDeleted, seenBefore, err := c.previousAssetState(ctx, sourceID, assetID)
		if err != nil {
			return err
		}
		switch {
		case !seenBefore:
			c.result.AssetsNew++
		case previousFingerprint != fingerprint:
			c.result.AssetsChanged++
		default:
			c.result.AssetsUnchanged++
		}
		if deleted && !previouslyDeleted {
			c.result.AssetsDeleted++
		}
		if !deleted && previouslyDeleted {
			c.result.AssetsRestored++
		}
		if err := c.upsertAsset(ctx, tx, sourceID, snapshotID, assetID, fingerprint, deleted, asset); err != nil {
			return err
		}
	}

	var missing int
	if err := tx.QueryRowContext(ctx, `
select count(*) from crawl_seen_asset
where source_library_id = ? and last_seen_snapshot_id <> ?
`, sourceID, snapshotID).Scan(&missing); err != nil {
		return fmt.Errorf("count missing seen assets: %w", err)
	}
	c.result.PreviouslySeenMissing = missing

	cursor, err := state.NewCursorMapped(tx, state.CursorMapping{
		Table:      "sync_state",
		Source:     "source",
		EntityType: "entity_type",
		EntityID:   "entity_id",
		Cursor:     "cursor",
		SyncedAt:   "synced_at",
	})
	if err != nil {
		return err
	}
	if err := cursor.Set(ctx, c.snapshot.Provider, "source_library", sourceID, snapshotID); err != nil {
		return err
	}

	return nil
}

func (c *crawlImporter) upsertAsset(ctx context.Context, tx *sql.Tx, sourceID, snapshotID, assetID, fingerprint string, deleted bool, asset photos.Asset) error {
	metadataJSON, err := jsonText(asset.Metadata)
	if err != nil {
		return err
	}
	assetArgs := []any{assetID, asset.LocalIdentifier, asset.MediaType, asset.MediaSubtypes, asset.CreationDate, asset.ModificationDate, asset.AddedDate, asset.TimezoneName, asset.Width, asset.Height, asset.DurationSeconds, boolInt(asset.Favorite), boolInt(asset.Hidden), asset.BurstIdentifier, boolInt(asset.RepresentsBurst), sourceID, metadataJSON}
	assetStatement := c.stmts.assetLive
	if deleted {
		assetStatement = c.stmts.assetTombstone
		assetArgs = append(assetArgs, asset.DeletedAt, asset.DeletionSource, asset.DeletionReason)
	}
	if _, err := assetStatement.ExecContext(ctx, assetArgs...); err != nil {
		return fmt.Errorf("upsert asset %s: %w", assetID, err)
	}
	evidenceKind := "asset_metadata"
	evidencePointer := "asset:" + asset.LocalIdentifier
	deletionTimestampSource := ""
	if deleted {
		evidenceKind = "asset_tombstone"
		evidencePointer += "/tombstone:" + asset.DeletedAt
		if value, ok := asset.Metadata["deletion_timestamp_source"].(string); ok {
			deletionTimestampSource = strings.TrimSpace(value)
		}
	}
	if err := c.insertEvidence(ctx, tx, assetID, evidenceKind, c.snapshot.Provider, evidencePointer, map[string]any{
		"media_type":                asset.MediaType,
		"media_subtypes":            asset.MediaSubtypes,
		"creation_date":             asset.CreationDate,
		"modification_date":         asset.ModificationDate,
		"favorite":                  asset.Favorite,
		"hidden":                    asset.Hidden,
		"width":                     asset.Width,
		"height":                    asset.Height,
		"deleted_at":                asset.DeletedAt,
		"deletion_source":           asset.DeletionSource,
		"deletion_reason":           asset.DeletionReason,
		"deletion_timestamp_source": deletionTimestampSource,
	}); err != nil {
		return err
	}
	for resourceIndex, resource := range asset.Resources {
		if err := c.insertResource(ctx, tx, assetID, asset.LocalIdentifier, resourceIndex, resource, deleted, asset); err != nil {
			return err
		}
	}
	if !deleted {
		for _, album := range asset.Albums {
			if err := c.insertAlbum(ctx, tx, assetID, album); err != nil {
				return err
			}
		}
		if asset.Location != nil {
			if err := c.insertLocation(ctx, tx, assetID, asset.LocalIdentifier, *asset.Location); err != nil {
				return err
			}
		}
	}
	if deleted {
		if err := c.tombstoneAssetSubordinates(ctx, tx, assetID, asset); err != nil {
			return err
		}
	} else {
		if err := c.insertFTS(ctx, tx, assetID, asset); err != nil {
			return err
		}
		if err := c.upsertClassifyQueue(ctx, tx, sourceID, assetID); err != nil {
			return err
		}
	}
	return c.upsertSeenAsset(ctx, tx, sourceID, assetID, snapshotID, fingerprint)
}

func (c *crawlImporter) insertResource(ctx context.Context, tx *sql.Tx, assetID, localIdentifier string, resourceIndex int, resource photos.Resource, deleted bool, asset photos.Asset) error {
	evidenceValue := map[string]any{
		"availability":      resource.Availability,
		"available_locally": resource.AvailableLocally,
		"needs_download":    resource.NeedsDownload,
		"file_size":         resource.FileSize,
		"stable_hash":       resource.StableHash,
		"local_path":        resource.LocalPath,
		"metadata":          resource.Metadata,
	}
	sourceIdentifier := strings.TrimSpace(resource.SourceIdentifier)
	resourceID, err := resolveResourceID(ctx, tx, assetID, sourceIdentifier, resourceIndex, resource)
	if err != nil {
		return err
	}
	args := []any{resourceID, assetID, sourceIdentifier, resource.Type, resource.UTI, resource.OriginalFilename, resource.LocalPath, resource.FileSize, resource.StableHash, boolInt(resource.AvailableLocally), boolInt(resource.NeedsDownload)}
	statement := c.stmts.resource
	evidenceKind := "asset_resource"
	pointer := "asset:" + localIdentifier + "/resource:" + resourceID
	if deleted {
		statement = c.stmts.resourceTombstone
		args = append(args, asset.DeletedAt, asset.DeletionSource)
		evidenceKind = "asset_resource_tombstone"
		pointer += "/tombstone:" + asset.DeletedAt
	}
	if _, err := statement.ExecContext(ctx, args...); err != nil {
		return fmt.Errorf("insert asset resource: %w", err)
	}
	return c.insertEvidence(ctx, tx, assetID, evidenceKind, c.snapshot.Provider, pointer, evidenceValue)
}

func validateSnapshotResourceSourceIdentifiers(snapshot photos.LibrarySnapshot) error {
	for _, asset := range snapshot.Assets {
		seen := make(map[string]struct{}, len(asset.Resources))
		for _, resource := range asset.Resources {
			sourceIdentifier := strings.TrimSpace(resource.SourceIdentifier)
			if sourceIdentifier == "" {
				return fmt.Errorf("asset %q resource %q is missing a stable source identifier", asset.LocalIdentifier, resource.Type)
			}
			if _, exists := seen[sourceIdentifier]; exists {
				return fmt.Errorf("asset %q has duplicate resource source identifier %q", asset.LocalIdentifier, sourceIdentifier)
			}
			seen[sourceIdentifier] = struct{}{}
		}
	}
	return nil
}

func resolveResourceID(ctx context.Context, tx *sql.Tx, assetID, sourceIdentifier string, resourceIndex int, resource photos.Resource) (string, error) {
	var resourceID string
	err := tx.QueryRowContext(ctx, `
select id from asset_resource
where asset_id = ? and source_identifier = ?
limit 1
`, assetID, sourceIdentifier).Scan(&resourceID)
	if err == nil {
		return resourceID, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("resolve asset resource source identity: %w", err)
	}
	legacyID := stableID("asset_resource", assetID, fmt.Sprintf("%06d", resourceIndex), resource.Type, resource.UTI, resource.OriginalFilename)
	err = tx.QueryRowContext(ctx, `
select id from asset_resource
where id = ? and asset_id = ? and coalesce(source_identifier, '') = ''
`, legacyID, assetID).Scan(&resourceID)
	if err == nil {
		return resourceID, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("resolve positional legacy asset resource identity: %w", err)
	}
	err = tx.QueryRowContext(ctx, `
select id from asset_resource
where asset_id = ?
  and coalesce(source_identifier, '') = ''
  and resource_type = ?
  and uti = ?
  and original_filename = ?
order by id
limit 1
`, assetID, resource.Type, resource.UTI, resource.OriginalFilename).Scan(&resourceID)
	if err == nil {
		return resourceID, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("resolve legacy asset resource identity: %w", err)
	}
	return stableID("asset_resource", assetID, sourceIdentifier), nil
}

func (c *crawlImporter) insertAlbum(ctx context.Context, tx *sql.Tx, assetID string, album photos.AlbumMembership) error {
	membershipID := stableID("album_membership", assetID, album.AlbumID)
	if _, err := c.stmts.album.ExecContext(ctx, membershipID, assetID, album.AlbumID, album.AlbumTitle, album.AlbumKind); err != nil {
		return fmt.Errorf("insert album membership: %w", err)
	}
	return c.insertEvidence(ctx, tx, assetID, "album_membership", c.snapshot.Provider, "album:"+album.AlbumID, album)
}

func (c *crawlImporter) insertLocation(ctx context.Context, tx *sql.Tx, assetID, localIdentifier string, location photos.Location) error {
	evidenceID := stableID("evidence", assetID, "location", localIdentifier)
	valueJSON, err := jsonText(location)
	if err != nil {
		return err
	}
	if _, err := c.stmts.evidence.ExecContext(ctx, evidenceID, assetID, "location", c.snapshot.Provider, "asset:"+localIdentifier+"/location", valueJSON); err != nil {
		return fmt.Errorf("insert location evidence: %w", err)
	}
	locationID := stableID("location_observation", assetID, localIdentifier)
	if _, err := c.stmts.location.ExecContext(ctx, locationID, assetID, location.Latitude, location.Longitude, nullableFloat(location.Altitude), nullableFloat(location.HorizontalAccuracy), c.snapshot.Provider, evidenceID); err != nil {
		return fmt.Errorf("insert location observation: %w", err)
	}
	return nil
}

func (c *crawlImporter) insertFTS(ctx context.Context, tx *sql.Tx, assetID string, asset photos.Asset) error {
	title := strings.Join(nonEmpty(asset.MediaType, asset.CreationDate), " ")
	bodyParts := []string{
		asset.MediaType,
		asset.MediaSubtypes,
		asset.CreationDate,
		asset.ModificationDate,
		asset.BurstIdentifier,
		fmt.Sprintf("%dx%d", asset.Width, asset.Height),
	}
	resourceRows, err := tx.QueryContext(ctx, `
select resource_type, uti, original_filename
from asset_resource
where asset_id = ? and deleted_at is null
order by resource_type, original_filename, id
`, assetID)
	if err != nil {
		return fmt.Errorf("load merged asset resources for fts: %w", err)
	}
	for resourceRows.Next() {
		var resourceType, uti, filename string
		if err := resourceRows.Scan(&resourceType, &uti, &filename); err != nil {
			resourceRows.Close()
			return err
		}
		bodyParts = append(bodyParts, resourceType, uti, filename)
	}
	if err := resourceRows.Close(); err != nil {
		return err
	}
	if err := resourceRows.Err(); err != nil {
		return err
	}
	albumRows, err := tx.QueryContext(ctx, `
select album_title, album_kind
from album_membership
where asset_id = ?
order by album_title, album_kind, id
`, assetID)
	if err != nil {
		return fmt.Errorf("load merged album memberships for fts: %w", err)
	}
	for albumRows.Next() {
		var title, kind string
		if err := albumRows.Scan(&title, &kind); err != nil {
			albumRows.Close()
			return err
		}
		bodyParts = append(bodyParts, title, kind)
	}
	if err := albumRows.Close(); err != nil {
		return err
	}
	if err := albumRows.Err(); err != nil {
		return err
	}
	body := strings.Join(nonEmpty(bodyParts...), " ")
	if _, err := c.stmts.deleteFTS.ExecContext(ctx, assetID); err != nil {
		return fmt.Errorf("clear asset fts: %w", err)
	}
	if _, err := c.stmts.fts.ExecContext(ctx, assetID, title, body); err != nil {
		return fmt.Errorf("insert asset fts: %w", err)
	}
	return nil
}

func (c *crawlImporter) tombstoneAssetSubordinates(ctx context.Context, tx *sql.Tx, assetID string, asset photos.Asset) error {
	if _, err := tx.ExecContext(ctx, `
update asset_resource
set deleted_at = coalesce(deleted_at, ?),
    deletion_source = coalesce(nullif(deletion_source, ''), ?),
    deletion_reason = coalesce(nullif(deletion_reason, ''), 'parent_asset_deleted')
where asset_id = ?
`, asset.DeletedAt, asset.DeletionSource, assetID); err != nil {
		return fmt.Errorf("tombstone asset resources: %w", err)
	}
	if _, err := c.stmts.deleteFTS.ExecContext(ctx, assetID); err != nil {
		return fmt.Errorf("clear tombstoned asset fts: %w", err)
	}
	queueID := stableID("classification_queue", assetID)
	if _, err := c.stmts.queue.ExecContext(ctx, queueID, assetID, stableID("source_library", c.libraryPath), "deleted", "parent_asset_deleted", 0, c.completedAt.Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("retire classification queue: %w", err)
	}
	return nil
}

func (c *crawlImporter) insertEvidence(ctx context.Context, tx *sql.Tx, assetID, kind, source, pointer string, value any) error {
	valueJSON, err := jsonText(value)
	if err != nil {
		return err
	}
	evidenceID := stableID("evidence", assetID, kind, pointer)
	if _, err := c.stmts.evidence.ExecContext(ctx, evidenceID, assetID, kind, source, pointer, valueJSON); err != nil {
		return fmt.Errorf("insert evidence ref: %w", err)
	}
	return nil
}

func (c *crawlImporter) upsertSeenAsset(ctx context.Context, tx *sql.Tx, sourceID, assetID, snapshotID, fingerprint string) error {
	if _, err := c.stmts.seen.ExecContext(ctx, sourceID, assetID, snapshotID, snapshotID, fingerprint, c.completedAt.Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("upsert crawl seen asset: %w", err)
	}
	return nil
}

func (c *crawlImporter) upsertClassifyQueue(ctx context.Context, tx *sql.Tx, sourceID, assetID string) error {
	hasLocalContent := false
	needsDownload := false
	rows, err := tx.QueryContext(ctx, `
select available_locally, local_path, needs_download
from asset_resource
where asset_id = ? and deleted_at is null
`, assetID)
	if err != nil {
		return fmt.Errorf("load merged resources for classification queue: %w", err)
	}
	for rows.Next() {
		var availableLocally, resourceNeedsDownload int
		var localPath string
		if err := rows.Scan(&availableLocally, &localPath, &resourceNeedsDownload); err != nil {
			rows.Close()
			return err
		}
		if availableLocally != 0 || strings.TrimSpace(localPath) != "" {
			hasLocalContent = true
		}
		if resourceNeedsDownload != 0 {
			needsDownload = true
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	needsDownload = needsDownload && !hasLocalContent
	queueID := stableID("classification_queue", assetID)
	if _, err := c.stmts.queue.ExecContext(ctx, queueID, assetID, sourceID, "pending", "metadata_ingested", boolInt(needsDownload), c.completedAt.Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("upsert classification queue: %w", err)
	}
	c.result.QueuedForClassify++
	if needsDownload {
		c.result.QueuedNeedsDownload++
	}
	return nil
}

func (c *crawlImporter) previousAssetState(ctx context.Context, sourceID, assetID string) (string, bool, bool, error) {
	var fingerprint string
	var deletedAt sql.NullString
	err := c.stmts.previousFingerprint.QueryRowContext(ctx, sourceID, assetID).Scan(&fingerprint, &deletedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, false, nil
	}
	if err != nil {
		return "", false, false, fmt.Errorf("read previous asset state: %w", err)
	}
	return fingerprint, deletedAt.Valid, true, nil
}

func (c *crawlImporter) normalizeAssetDeletion(asset photos.Asset) (photos.Asset, bool, error) {
	deletedAt := strings.TrimSpace(asset.DeletedAt)
	reason := strings.TrimSpace(asset.DeletionReason)
	source := strings.TrimSpace(asset.DeletionSource)
	if deletedAt == "" && reason == "" && source == "" {
		return asset, false, nil
	}
	if reason == "" {
		return photos.Asset{}, false, fmt.Errorf("asset %q tombstone has no deletion reason", asset.LocalIdentifier)
	}
	if deletedAt == "" {
		deletedAt = c.completedAt.Format(time.RFC3339Nano)
	} else if _, err := time.Parse(time.RFC3339Nano, deletedAt); err != nil {
		return photos.Asset{}, false, fmt.Errorf("asset %q tombstone deleted_at: %w", asset.LocalIdentifier, err)
	}
	if source == "" {
		source = c.snapshot.Provider
	}
	asset.DeletedAt = deletedAt
	asset.DeletionSource = source
	asset.DeletionReason = reason
	return asset, true, nil
}

func snapshotCounts(snapshot photos.LibrarySnapshot) (resources, albums, locations int) {
	for _, asset := range snapshot.Assets {
		resources += len(asset.Resources)
		albums += len(asset.Albums)
		if asset.Location != nil {
			locations++
		}
	}
	return resources, albums, locations
}

func assetFingerprint(asset photos.Asset) (string, error) {
	data, err := json.Marshal(asset)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func (c *crawlImporter) sourceFingerprint() string {
	hash := sha256.New()
	for _, asset := range c.snapshot.Assets {
		hash.Write([]byte(asset.LocalIdentifier))
		hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func jsonText(value any) (string, error) {
	if value == nil {
		return "{}", nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func nullableFloat(value *float64) any {
	if value == nil {
		return nil
	}
	return *value
}

func nonEmpty(values ...string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}
