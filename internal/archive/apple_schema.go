package archive

import (
	"context"
	"database/sql"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/openclaw/crawlkit/store"
)

func validatePhotosFacesSchema(ctx context.Context, db *sql.DB) (map[string]string, error) {
	return validateAppleSchema(ctx, db, "Photos schema", map[string][]string{
		"ZPERSON":       {"Z_PK", "ZDISPLAYNAME", "ZFULLNAME", "ZPERSONUUID"},
		"ZDETECTEDFACE": {"Z_PK", "ZUUID", "ZPERSONFORFACE", "ZASSETFORFACE", "ZCENTERX", "ZCENTERY", "ZSIZE", "ZQUALITY", "ZNAMESOURCE", "ZCLOUDNAMESOURCE", "ZSOURCEWIDTH", "ZSOURCEHEIGHT"},
		"ZASSET":        {"Z_PK", "ZUUID"},
	})
}

func validatePSISearchSchema(ctx context.Context, db *sql.DB) (map[string]string, error) {
	return validateAppleSchema(ctx, db, "Apple search index", map[string][]string{
		"assets": {"rowid", "uuid_0", "uuid_1"},
		"groups": {"rowid", "category", "owning_groupid", "content_string", "normalized_string", "lookup_identifier", "score"},
		"ga":     {"rowid", "assetid", "groupid"},
	})
}

func validateLeoSearchSchema(ctx context.Context, db *sql.DB) (map[string]string, error) {
	return validateAppleSchema(ctx, db, "Apple search index", map[string][]string{
		"lexicon": {"lexeme_id", "type", "category", "content"},
		"items":   {"identifier", "type", "lexeme_ids"},
	})
}

func validateAppleSchema(ctx context.Context, db *sql.DB, source string, required map[string][]string) (map[string]string, error) {
	out := make(map[string]string, len(required))
	for _, table := range slices.Sorted(maps.Keys(required)) {
		columns := required[table]
		existing, err := tableColumns(ctx, db, table)
		if err != nil {
			return nil, err
		}
		var missing []string
		for _, column := range columns {
			// PSI uses SQLite's implicit rowid, which table_info does not list.
			if column != "rowid" && !existing[column] {
				missing = append(missing, column)
			}
		}
		if len(missing) > 0 {
			return nil, fmt.Errorf("%s table %s missing columns: %s", source, table, strings.Join(missing, ", "))
		}
		out[table] = strings.Join(columns, ",")
	}
	return out, nil
}

func tableColumns(ctx context.Context, db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, "pragma table_info("+store.QuoteIdent(table)+")")
	if err != nil {
		return nil, fmt.Errorf("inspect Photos schema %s: %w", table, err)
	}
	defer rows.Close()
	columns := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, pk int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			return nil, err
		}
		columns[name] = true
	}
	return columns, rows.Err()
}

func nullableSQLFloat(value sql.NullFloat64) any {
	if !value.Valid {
		return nil
	}
	return value.Float64
}

func nullableSQLInt(value sql.NullInt64) any {
	if !value.Valid {
		return nil
	}
	return value.Int64
}
