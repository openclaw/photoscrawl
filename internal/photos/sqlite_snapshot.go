package photos

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

type SQLiteSnapshotProvider struct{}

func (SQLiteSnapshotProvider) Snapshot(ctx context.Context, libraryPath string) (LibrarySnapshot, error) {
	dbPath := filepath.Join(strings.TrimSpace(libraryPath), "database", "Photos.sqlite")
	if _, err := os.Stat(dbPath); err != nil {
		return LibrarySnapshot{}, fmt.Errorf("open Photos sqlite snapshot: %w", err)
	}
	db, err := store.OpenReadOnly(ctx, dbPath)
	if err != nil {
		return LibrarySnapshot{}, fmt.Errorf("open Photos sqlite snapshot: %w", err)
	}
	defer db.Close()

	resources, err := sqliteResources(ctx, db.DB())
	if err != nil {
		return LibrarySnapshot{}, err
	}
	albums, err := sqliteAlbums(ctx, db.DB())
	if err != nil {
		return LibrarySnapshot{}, err
	}
	assets, err := sqliteAssets(ctx, db.DB(), resources, albums)
	if err != nil {
		return LibrarySnapshot{}, err
	}

	return LibrarySnapshot{
		LibraryPath:   libraryPath,
		Provider:      "photos_sqlite_snapshot",
		PhotosVersion: "unknown",
		Metadata: map[string]any{
			"source":        "Photos.sqlite",
			"snapshot":      "read_only_sqlite_transaction",
			"database_path": "database/Photos.sqlite",
			"warning":       "Private Photos Core Data schema; use as fallback evidence, not as the only source strategy.",
		},
		Assets: assets,
	}, nil
}

func sqliteAssets(ctx context.Context, db *sql.DB, resources map[int64][]Resource, albums map[int64][]AlbumMembership) ([]Asset, error) {
	trashDateExpression := "null"
	hasTrashDate, err := sqliteColumnExists(ctx, db, "ZASSET", "ZTRASHEDDATE")
	if err != nil {
		return nil, err
	}
	if hasTrashDate {
		trashDateExpression = "cast(a.ZTRASHEDDATE as real)"
	}
	rows, err := db.QueryContext(ctx, fmt.Sprintf(`
select a.Z_PK,
       coalesce(a.ZUUID, ''),
       coalesce(a.ZKIND, -1),
       coalesce(a.ZKINDSUBTYPE, 0),
       cast(a.ZDATECREATED as real),
       cast(a.ZMODIFICATIONDATE as real),
       cast(a.ZADDEDDATE as real),
       coalesce(aa.ZTIMEZONENAME, ''),
       coalesce(a.ZWIDTH, 0),
       coalesce(a.ZHEIGHT, 0),
       coalesce(a.ZDURATION, 0),
       coalesce(a.ZFAVORITE, 0),
       coalesce(a.ZHIDDEN, 0),
       coalesce(a.ZAVALANCHEUUID, ''),
	       cast(a.ZLATITUDE as real),
	       cast(a.ZLONGITUDE as real),
	       cast(aa.ZGPSHORIZONTALACCURACY as real),
       coalesce(a.ZUNIFORMTYPEIDENTIFIER, ''),
       coalesce(a.ZFILENAME, ''),
       coalesce(aa.ZORIGINALFILENAME, ''),
       coalesce(a.ZTRASHEDSTATE, 0),
       %s
from ZASSET a
left join ZADDITIONALASSETATTRIBUTES aa on aa.ZASSET = a.Z_PK
where coalesce(a.ZUUID, '') <> ''
order by a.ZDATECREATED, a.ZUUID
`, trashDateExpression))
	if err != nil {
		return nil, fmt.Errorf("query sqlite assets: %w", err)
	}
	defer rows.Close()

	assets := []Asset{}
	for rows.Next() {
		var row sqliteAssetRow
		if err := rows.Scan(
			&row.pk,
			&row.uuid,
			&row.kind,
			&row.kindSubtype,
			&row.creationDate,
			&row.modificationDate,
			&row.addedDate,
			&row.timezoneName,
			&row.width,
			&row.height,
			&row.duration,
			&row.favorite,
			&row.hidden,
			&row.burstIdentifier,
			&row.latitude,
			&row.longitude,
			&row.horizontalAccuracy,
			&row.uti,
			&row.filename,
			&row.originalFilename,
			&row.trashedState,
			&row.trashedDate,
		); err != nil {
			return nil, err
		}
		asset := Asset{
			LocalIdentifier:  row.uuid,
			MediaType:        sqliteMediaType(row.kind),
			MediaSubtypes:    fmt.Sprintf("kind_subtype:%d", row.kindSubtype),
			CreationDate:     coreDataTime(row.creationDate),
			ModificationDate: coreDataTime(row.modificationDate),
			AddedDate:        coreDataTime(row.addedDate),
			TimezoneName:     row.timezoneName,
			Width:            row.width,
			Height:           row.height,
			DurationSeconds:  row.duration,
			Favorite:         row.favorite != 0,
			Hidden:           row.hidden != 0,
			BurstIdentifier:  row.burstIdentifier,
			Resources:        resources[row.pk],
			Albums:           albums[row.pk],
			Metadata: map[string]any{
				"sqlite_pk":                  row.pk,
				"uniform_type_identifier":    row.uti,
				"filename":                   row.filename,
				"original_filename":          row.originalFilename,
				"schema_source":              "ZASSET",
				"additional_attributes_join": "ZADDITIONALASSETATTRIBUTES",
			},
		}
		if row.latitude.Valid && row.longitude.Valid && validLocation(row.latitude.Float64, row.longitude.Float64) {
			var accuracy *float64
			if row.horizontalAccuracy.Valid {
				accuracy = &row.horizontalAccuracy.Float64
			}
			asset.Location = &Location{
				Latitude:           row.latitude.Float64,
				Longitude:          row.longitude.Float64,
				HorizontalAccuracy: accuracy,
			}
		}
		if row.trashedState != 0 {
			asset.DeletedAt = coreDataTime(row.trashedDate)
			asset.DeletionSource = "photos_sqlite_snapshot"
			asset.DeletionReason = "trashed_in_photos_library"
			if asset.DeletedAt == "" {
				asset.Metadata["deletion_timestamp_source"] = "crawl_observed_at"
			} else {
				asset.Metadata["deletion_timestamp_source"] = "ZTRASHEDDATE"
			}
		}
		assets = append(assets, asset)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return assets, nil
}

func sqliteResources(ctx context.Context, db *sql.DB) (map[int64][]Resource, error) {
	rows, err := db.QueryContext(ctx, `
select r.Z_PK,
       r.ZASSET,
       coalesce(r.ZRESOURCETYPE, -1),
       coalesce(r.ZCOMPACTUTI, a.ZUNIFORMTYPEIDENTIFIER, ''),
       coalesce(aa.ZORIGINALFILENAME, a.ZFILENAME, ''),
       coalesce(r.ZDATALENGTH, 0),
       coalesce(r.ZSTABLEHASH, r.ZFINGERPRINT, ''),
       coalesce(r.ZLOCALAVAILABILITY, 0),
       coalesce(r.ZREMOTEAVAILABILITY, 0),
       coalesce(r.ZVERSION, 0)
from ZINTERNALRESOURCE r
left join ZASSET a on a.Z_PK = r.ZASSET
left join ZADDITIONALASSETATTRIBUTES aa on aa.ZASSET = a.Z_PK
where r.ZASSET is not null
order by r.ZASSET, r.ZRESOURCETYPE, r.ZVERSION
`)
	if err != nil {
		return nil, fmt.Errorf("query sqlite resources: %w", err)
	}
	defer rows.Close()

	out := map[int64][]Resource{}
	for rows.Next() {
		var resourcePK, assetPK, resourceType, fileSize, localAvailability, remoteAvailability, version int64
		var uti, originalFilename, stableHash string
		if err := rows.Scan(&resourcePK, &assetPK, &resourceType, &uti, &originalFilename, &fileSize, &stableHash, &localAvailability, &remoteAvailability, &version); err != nil {
			return nil, err
		}
		availableLocally := localAvailability > 0
		needsDownload := !availableLocally && remoteAvailability > 0
		out[assetPK] = append(out[assetPK], Resource{
			SourceIdentifier: fmt.Sprintf("sqlite_internal_resource:%d", resourcePK),
			Type:             fmt.Sprintf("internal_resource:%d", resourceType),
			UTI:              uti,
			OriginalFilename: originalFilename,
			Availability:     sqliteAvailability(availableLocally, needsDownload),
			FileSize:         fileSize,
			StableHash:       stableHash,
			AvailableLocally: availableLocally,
			NeedsDownload:    needsDownload,
			Metadata: map[string]any{
				"sqlite_resource_type":    resourceType,
				"sqlite_version":          version,
				"local_availability_raw":  localAvailability,
				"remote_availability_raw": remoteAvailability,
				"schema_source":           "ZINTERNALRESOURCE",
			},
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func sqliteAlbums(ctx context.Context, db *sql.DB) (map[int64][]AlbumMembership, error) {
	rows, err := db.QueryContext(ctx, `
select m.Z_3ASSETS,
       coalesce(g.ZUUID, printf('sqlite_album:%d', g.Z_PK)),
       coalesce(g.ZTITLE, ''),
       coalesce(g.ZKIND, -1),
       coalesce(g.ZCLOUDALBUMSUBTYPE, 0)
from Z_33ASSETS m
join ZGENERICALBUM g on g.Z_PK = m.Z_33ALBUMS
where coalesce(g.ZTRASHEDSTATE, 0) = 0
order by m.Z_3ASSETS, g.ZTITLE
`)
	if err != nil {
		return nil, fmt.Errorf("query sqlite albums: %w", err)
	}
	defer rows.Close()

	out := map[int64][]AlbumMembership{}
	for rows.Next() {
		var assetPK, kind, subtype int64
		var albumID, title string
		if err := rows.Scan(&assetPK, &albumID, &title, &kind, &subtype); err != nil {
			return nil, err
		}
		out[assetPK] = append(out[assetPK], AlbumMembership{
			AlbumID:    albumID,
			AlbumTitle: title,
			AlbumKind:  fmt.Sprintf("generic_album:%d:%d", kind, subtype),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

type sqliteAssetRow struct {
	pk                 int64
	uuid               string
	kind               int64
	kindSubtype        int64
	creationDate       sql.NullFloat64
	modificationDate   sql.NullFloat64
	addedDate          sql.NullFloat64
	timezoneName       string
	width              int64
	height             int64
	duration           float64
	favorite           int64
	hidden             int64
	burstIdentifier    string
	latitude           sql.NullFloat64
	longitude          sql.NullFloat64
	horizontalAccuracy sql.NullFloat64
	uti                string
	filename           string
	originalFilename   string
	trashedState       int64
	trashedDate        sql.NullFloat64
}

func sqliteColumnExists(ctx context.Context, db *sql.DB, table, column string) (bool, error) {
	rows, err := db.QueryContext(ctx, "pragma table_info("+store.QuoteIdent(table)+")")
	if err != nil {
		return false, fmt.Errorf("inspect sqlite table %s: %w", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

func sqliteMediaType(kind int64) string {
	switch kind {
	case 0:
		return "image"
	case 1:
		return "video"
	default:
		return fmt.Sprintf("kind:%d", kind)
	}
}

func sqliteAvailability(local, remote bool) string {
	switch {
	case local:
		return "local"
	case remote:
		return "remote"
	default:
		return "unknown"
	}
}

func validLocation(latitude, longitude float64) bool {
	if latitude < -90 || latitude > 90 || longitude < -180 || longitude > 180 {
		return false
	}
	return latitude != 0 || longitude != 0
}

func coreDataTime(value sql.NullFloat64) string {
	if !value.Valid {
		return ""
	}
	return time.Unix(978307200+int64(value.Float64), 0).UTC().Format(time.RFC3339)
}
