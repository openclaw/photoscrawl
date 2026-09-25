package archive

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"

	"github.com/openclaw/crawlkit/store"
	"github.com/openclaw/photoscrawl/internal/photos"
	sqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

const archiveBusyTimeoutMillis = 120000

func openArchiveStore(ctx context.Context, path string) (*store.Store, error) {
	return openArchiveStoreWithValidation(ctx, path, nil)
}

func openArchiveStoreWithValidation(ctx context.Context, path string, validate func(*sql.DB) error) (*store.Store, error) {
	sqlitePath, err := archiveFilename(path)
	if err != nil {
		return nil, err
	}
	if err := preflightArchive(ctx, sqlitePath, validate); err != nil {
		return nil, err
	}
	db, err := store.Open(ctx, store.Options{Path: sqlitePath})
	if err != nil {
		return nil, err
	}
	// Crawls and classifiers are separate processes that share this archive.
	// Their write transactions are bounded, so wait for the current writer
	// instead of failing a multi-hour crawl after CrawlKit's five-second default.
	if _, err := db.DB().ExecContext(ctx, fmt.Sprintf("pragma busy_timeout = %d", archiveBusyTimeoutMillis)); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("configure archive busy timeout: %w", err)
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
	// A current archive must stay read/write without reopening a migration
	// transaction. Classifiers and crawlers are separate bounded processes;
	// taking a no-op write lock here made healthy crawls fail while a classifier
	// was committing a batch.
	if current == SchemaVersion && isArchive {
		return db, nil
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

func preflightArchive(ctx context.Context, path string, validate func(*sql.DB) error) error {
	return preflightArchiveWithMode(ctx, path, validate, false)
}

func preflightArchiveWithMode(ctx context.Context, path string, validate func(*sql.DB) error, forceSnapshot bool) error {
	// Reject accidental foreign-file selection without opening it writable.
	// As with SQLite's pathname API, callers must keep the path stable through open.
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) && validate == nil {
		return inspectArchiveSidecars(path, true)
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("archive must be a regular file")
	}
	if err := inspectArchiveSidecars(path, info.Size() == 0); err != nil {
		return err
	}
	linked, err := archiveHardlinked(path, info)
	if err != nil {
		return err
	}
	if linked {
		return errors.New("cannot write a hardlinked archive: SQLite sidecars belong to a single filename")
	}
	db, cleanup, snapshot, err := openArchiveInspection(ctx, path, forceSnapshot)
	if err != nil {
		return err
	}
	defer cleanup()
	defer db.Close()
	if err := inspectArchiveDatabase(ctx, db, validate); err != nil {
		if !snapshot && archiveReadOnlyNeedsSnapshot(err) {
			_ = db.Close()
			cleanup()
			return preflightArchiveWithMode(ctx, path, validate, true)
		}
		return err
	}
	after, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !os.SameFile(info, after) {
		return errors.New("archive changed identity during validation")
	}
	if !snapshot {
		if err := inspectArchiveSidecars(path, false); err != nil {
			return err
		}
		journal, err := archiveRollbackJournalPresent(path)
		if err != nil {
			return err
		}
		if journal {
			_ = db.Close()
			cleanup()
			return preflightArchiveWithMode(ctx, path, validate, true)
		}
	}
	return nil
}

func openArchiveInspection(ctx context.Context, path string, forceSnapshot bool) (*store.Store, func(), bool, error) {
	needsSnapshot := forceSnapshot
	if !needsSnapshot {
		var err error
		needsSnapshot, err = archiveRollbackJournalPresent(path)
		if err != nil {
			return nil, func() {}, false, err
		}
	}
	if !needsSnapshot {
		db, err := store.OpenReadOnly(ctx, path)
		if err == nil {
			return db, func() {}, false, nil
		}
		if !archiveReadOnlyNeedsSnapshot(err) {
			return nil, func() {}, false, err
		}
	}
	private, cleanup, err := photos.CopySQLite(ctx, path, "photoscrawl-preflight-")
	if err != nil {
		return nil, func() {}, false, err
	}
	db, err := store.OpenReadOnly(ctx, private)
	if err != nil {
		cleanup()
		return nil, func() {}, false, err
	}
	return db, cleanup, true, nil
}

func inspectArchiveDatabase(ctx context.Context, db *store.Store, validate func(*sql.DB) error) error {
	current, err := db.SchemaVersion(ctx)
	if err != nil {
		return err
	}
	if current > SchemaVersion {
		return fmt.Errorf("database schema version %d is newer than supported version %d", current, SchemaVersion)
	}
	isArchive, err := archiveTablesPresent(ctx, db.DB())
	if err != nil {
		return err
	}
	empty, err := archiveDatabaseEmpty(ctx, db.DB())
	if err != nil {
		return err
	}
	if !isArchive && (!empty || current != 0) {
		return errors.New("database is not a photoscrawl archive")
	}
	if validate != nil {
		return validate(db.DB())
	}
	return nil
}

func archiveReadOnlyNeedsSnapshot(err error) bool {
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) {
		return false
	}
	code := sqliteErr.Code()
	primary := code & 0xff
	return primary == sqlite3.SQLITE_BUSY ||
		primary == sqlite3.SQLITE_LOCKED ||
		code == sqlite3.SQLITE_READONLY_RECOVERY
}

// CheckIntegrity snapshots the archive consistently and runs SQLite's quick
// check before applying the same schema and identity checks as writable opens.
func CheckIntegrity(ctx context.Context, paths Paths) error {
	path, err := archiveFilename(paths.Database)
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); err != nil {
		return err
	}
	return preflightArchiveWithMode(ctx, path, nil, true)
}

func openArchiveReadOnly(ctx context.Context, path string) (*store.Store, error) {
	sqlitePath, err := archiveFilename(path)
	if err != nil {
		return nil, err
	}
	db, err := store.OpenReadOnly(ctx, sqlitePath)
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
			{"album_membership", "folder_path", "text not null default ''"},
			{"face_observation", "person_uuid", "text"},
			{"face_observation", "person_kind", "text"},
			{"face_observation", "quality", "real"},
			{"face_observation", "blur_score", "real"},
			{"face_observation", "eyes_closed", "integer"},
			{"face_observation", "smile", "integer"},
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
			// Label lookups for similar; without it each shared label scans
			// every visual observation.
			`create index if not exists visual_type_label_idx on visual_observation(observation_type, label collate nocase)`,
		} {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("create archive index: %w", err)
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
