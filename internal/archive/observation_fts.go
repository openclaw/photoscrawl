package archive

import (
	"context"
	"database/sql"
	"fmt"
)

func insertObservationFTS(ctx context.Context, tx *sql.Tx, observationID, assetID, title, body string) error {
	if _, err := tx.ExecContext(ctx, `
insert into observation_fts(id, asset_id, title, body)
values (?, ?, ?, ?)
`, observationID, assetID, title, body); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
insert into observation_fts_rowid(fts_rowid, observation_id)
values (last_insert_rowid(), ?)
`, observationID); err != nil {
		return fmt.Errorf("map observation FTS row: %w", err)
	}
	return nil
}
