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
	"github.com/openclaw/crawlkit/store"
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

	db, err := store.Open(ctx, store.Options{
		Path:          paths.Database,
		Schema:        Schema,
		SchemaVersion: SchemaVersion,
	})
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
		assetID := stableID("asset", sourceID, asset.LocalIdentifier)
		fingerprint, err := assetFingerprint(asset)
		if err != nil {
			return err
		}
		previousFingerprint, seenBefore, err := c.previousAssetFingerprint(ctx, sourceID, assetID)
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
		if err := c.upsertAsset(ctx, tx, sourceID, snapshotID, assetID, fingerprint, seenBefore, asset); err != nil {
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

func (c *crawlImporter) upsertAsset(ctx context.Context, tx *sql.Tx, sourceID, snapshotID, assetID, fingerprint string, seenBefore bool, asset photos.Asset) error {
	metadataJSON, err := jsonText(asset.Metadata)
	if err != nil {
		return err
	}
	if _, err := c.stmts.asset.ExecContext(ctx, assetID, asset.LocalIdentifier, asset.MediaType, asset.MediaSubtypes, asset.CreationDate, asset.ModificationDate, asset.AddedDate, asset.TimezoneName, asset.Width, asset.Height, asset.DurationSeconds, boolInt(asset.Favorite), boolInt(asset.Hidden), asset.BurstIdentifier, boolInt(asset.RepresentsBurst), sourceID, metadataJSON); err != nil {
		return fmt.Errorf("upsert asset %s: %w", assetID, err)
	}

	if seenBefore {
		if err := resetAssetDerivedRows(ctx, tx, assetID); err != nil {
			return err
		}
	}
	if err := c.insertEvidence(ctx, tx, assetID, "asset_metadata", c.snapshot.Provider, "asset:"+asset.LocalIdentifier, map[string]any{
		"media_type":        asset.MediaType,
		"media_subtypes":    asset.MediaSubtypes,
		"creation_date":     asset.CreationDate,
		"modification_date": asset.ModificationDate,
		"favorite":          asset.Favorite,
		"hidden":            asset.Hidden,
		"width":             asset.Width,
		"height":            asset.Height,
	}); err != nil {
		return err
	}
	for i, resource := range asset.Resources {
		if err := c.insertResource(ctx, tx, assetID, asset.LocalIdentifier, i, resource); err != nil {
			return err
		}
	}
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
	if err := c.insertFTS(ctx, tx, assetID, asset); err != nil {
		return err
	}
	if err := c.upsertClassifyQueue(ctx, tx, sourceID, assetID, asset); err != nil {
		return err
	}
	return c.upsertSeenAsset(ctx, tx, sourceID, assetID, snapshotID, fingerprint)
}

func (c *crawlImporter) insertResource(ctx context.Context, tx *sql.Tx, assetID, localIdentifier string, index int, resource photos.Resource) error {
	evidenceValue := map[string]any{
		"availability":      resource.Availability,
		"available_locally": resource.AvailableLocally,
		"needs_download":    resource.NeedsDownload,
		"file_size":         resource.FileSize,
		"stable_hash":       resource.StableHash,
		"local_path":        resource.LocalPath,
		"metadata":          resource.Metadata,
	}
	resourceID := stableID("asset_resource", assetID, fmt.Sprintf("%06d", index), resource.Type, resource.UTI, resource.OriginalFilename)
	if _, err := c.stmts.resource.ExecContext(ctx, resourceID, assetID, resource.Type, resource.UTI, resource.OriginalFilename, resource.LocalPath, resource.FileSize, resource.StableHash, boolInt(resource.AvailableLocally), boolInt(resource.NeedsDownload)); err != nil {
		return fmt.Errorf("insert asset resource: %w", err)
	}
	return c.insertEvidence(ctx, tx, assetID, "asset_resource", c.snapshot.Provider, "asset:"+localIdentifier+"/resource:"+resourceID, evidenceValue)
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
	for _, resource := range asset.Resources {
		bodyParts = append(bodyParts, resource.Type, resource.UTI, resource.OriginalFilename)
	}
	for _, album := range asset.Albums {
		bodyParts = append(bodyParts, album.AlbumTitle, album.AlbumKind)
	}
	body := strings.Join(nonEmpty(bodyParts...), " ")
	if _, err := c.stmts.fts.ExecContext(ctx, assetID, title, body); err != nil {
		return fmt.Errorf("insert asset fts: %w", err)
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

func (c *crawlImporter) upsertClassifyQueue(ctx context.Context, tx *sql.Tx, sourceID, assetID string, asset photos.Asset) error {
	hasLocalContent := false
	needsDownload := false
	for _, resource := range asset.Resources {
		if resource.AvailableLocally || strings.TrimSpace(resource.LocalPath) != "" {
			hasLocalContent = true
		}
		if resource.NeedsDownload {
			needsDownload = true
		}
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

func resetAssetDerivedRows(ctx context.Context, tx *sql.Tx, assetID string) error {
	tables := []string{
		"asset_resource", "album_membership", "location_observation",
		"visual_observation", "text_observation", "face_observation",
		"model_observation", "observation_term",
		"asset_fts", "observation_fts", "edge", "evidence_ref",
	}
	for _, table := range tables {
		column := "asset_id"
		if table == "asset_fts" {
			column = "id"
		}
		query := "delete from " + store.QuoteIdent(table) + " where " + store.QuoteIdent(column) + " = ?"
		if table == "edge" {
			query = "delete from edge where from_id = ? or to_id = ?"
		}
		var err error
		if table == "edge" {
			_, err = tx.ExecContext(ctx, query, assetID, assetID)
		} else {
			_, err = tx.ExecContext(ctx, query, assetID)
		}
		if err != nil {
			return fmt.Errorf("clear %s for asset: %w", table, err)
		}
	}
	return nil
}

func (c *crawlImporter) previousAssetFingerprint(ctx context.Context, sourceID, assetID string) (string, bool, error) {
	var fingerprint string
	err := c.stmts.previousFingerprint.QueryRowContext(ctx, sourceID, assetID).Scan(&fingerprint)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read previous asset state: %w", err)
	}
	return fingerprint, true, nil
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
