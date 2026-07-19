package archive

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/openclaw/crawlkit/store"
)

func openArchiveStore(ctx context.Context, path string) (*store.Store, error) {
	db, err := store.Open(ctx, store.Options{Path: path})
	if err != nil {
		return nil, err
	}
	current, err := db.SchemaVersion(ctx)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("read archive schema version: %w", err)
	}
	if current > SchemaVersion {
		_ = db.Close()
		return nil, fmt.Errorf("database schema version %d is newer than supported version %d", current, SchemaVersion)
	}
	isArchive, err := archiveTablesPresent(ctx, db.DB())
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	empty, err := archiveDatabaseEmpty(ctx, db.DB())
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	if !isArchive && (!empty || current != 0) {
		_ = db.Close()
		return nil, errors.New("database is not a photoscrawl archive")
	}
	if _, err := db.DB().ExecContext(ctx, Schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("apply archive schema: %w", err)
	}
	if err := migrateArchiveSchema(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func openArchiveReadOnly(ctx context.Context, path string) (*store.Store, error) {
	db, err := store.OpenReadOnly(ctx, path)
	if err != nil {
		return nil, err
	}
	current, err := db.SchemaVersion(ctx)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("read archive schema version: %w", err)
	}
	if current > SchemaVersion {
		_ = db.Close()
		return nil, fmt.Errorf("database schema version %d is newer than supported version %d", current, SchemaVersion)
	}
	isArchive, err := archiveTablesPresent(ctx, db.DB())
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	if !isArchive {
		_ = db.Close()
		return nil, errors.New("database is not a photoscrawl archive")
	}
	if current < SchemaVersion {
		_ = db.Close()
		return nil, fmt.Errorf("archive schema version %d requires upgrade to %d; run photoscrawl init --db %q", current, SchemaVersion, path)
	}
	return db, nil
}

func archiveTablesPresent(ctx context.Context, db *sql.DB) (bool, error) {
	var count int
	if err := db.QueryRowContext(ctx, `
select count(*)
from sqlite_master
where type = 'table' and name in ('asset', 'source_library', 'crawl_snapshot')
`).Scan(&count); err != nil {
		return false, fmt.Errorf("inspect archive tables: %w", err)
	}
	return count == 3, nil
}

func archiveDatabaseEmpty(ctx context.Context, db *sql.DB) (bool, error) {
	var count int
	if err := db.QueryRowContext(ctx, `select count(*) from sqlite_master where name not like 'sqlite_%'`).Scan(&count); err != nil {
		return false, fmt.Errorf("inspect archive tables: %w", err)
	}
	return count == 0, nil
}

func migrateArchiveSchema(ctx context.Context, db *store.Store) error {
	current, err := db.SchemaVersion(ctx)
	if err != nil {
		return fmt.Errorf("read archive schema version: %w", err)
	}
	if current > SchemaVersion {
		return fmt.Errorf("database schema version %d is newer than supported version %d", current, SchemaVersion)
	}
	if err := db.WithTx(ctx, func(tx *sql.Tx) error {
		columns := []struct {
			table      string
			name       string
			definition string
		}{
			{"asset", "deleted_at", "text"},
			{"asset", "deletion_source", "text"},
			{"asset", "deletion_reason", "text"},
			{"asset_resource", "deleted_at", "text"},
			{"asset_resource", "deletion_source", "text"},
			{"asset_resource", "deletion_reason", "text"},
			{"asset_resource", "source_identifier", "text"},
		}
		for _, column := range columns {
			if err := ensureArchiveColumn(ctx, tx, column.table, column.name, column.definition); err != nil {
				return err
			}
		}
		for _, statement := range []string{
			`create index if not exists asset_deleted_idx on asset(deleted_at)`,
			`create index if not exists resource_deleted_idx on asset_resource(deleted_at)`,
			`create index if not exists resource_source_identifier_idx on asset_resource(asset_id, source_identifier)`,
		} {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("create tombstone index: %w", err)
			}
		}
		if _, err := tx.ExecContext(ctx, `
update asset
set deletion_source = coalesce(nullif(deletion_source, ''), 'unknown'),
    deletion_reason = coalesce(nullif(deletion_reason, ''), 'legacy_asset_tombstone')
where deleted_at is not null
`); err != nil {
			return fmt.Errorf("normalize asset tombstones: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
update asset_resource
set deleted_at = coalesce(deleted_at, (select deleted_at from asset where asset.id = asset_resource.asset_id)),
    deletion_source = coalesce(nullif(deletion_source, ''), (select deletion_source from asset where asset.id = asset_resource.asset_id), 'unknown'),
    deletion_reason = coalesce(nullif(deletion_reason, ''), 'parent_asset_deleted')
where exists (select 1 from asset where asset.id = asset_resource.asset_id and asset.deleted_at is not null)
`); err != nil {
			return fmt.Errorf("reconcile asset resource tombstones: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
update asset_resource
set deletion_source = coalesce(nullif(deletion_source, ''), 'unknown'),
    deletion_reason = coalesce(nullif(deletion_reason, ''), 'legacy_resource_tombstone')
where deleted_at is not null
`); err != nil {
			return fmt.Errorf("normalize asset resource tombstones: %w", err)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("migrate archive schema: %w", err)
	}
	if err := db.EnsureSchemaVersion(ctx, SchemaVersion); err != nil {
		return fmt.Errorf("write archive schema version: %w", err)
	}
	return nil
}

func ensureArchiveColumn(ctx context.Context, tx *sql.Tx, table, name, definition string) error {
	rows, err := tx.QueryContext(ctx, "pragma table_info("+store.QuoteIdent(table)+")")
	if err != nil {
		return fmt.Errorf("inspect %s columns: %w", table, err)
	}
	found := false
	for rows.Next() {
		var cid, notNull, primaryKey int
		var columnName, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &columnName, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			return fmt.Errorf("scan %s columns: %w", table, err)
		}
		if columnName == name {
			found = true
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if found {
		return nil
	}
	if _, err := tx.ExecContext(ctx, "alter table "+store.QuoteIdent(table)+" add column "+store.QuoteIdent(name)+" "+definition); err != nil {
		return fmt.Errorf("add %s.%s: %w", table, name, err)
	}
	return nil
}
