// Governing: SPEC-0016 REQ "Schema Migration", ADR-0004 (Ent ORM), ADR-0023 (PostgreSQL)
package database

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
)

// authTimestampTables are the tables that gained created_at/updated_at via
// spmixin.Timestamps{} after they already shipped without those columns.
var authTimestampTables = []string{"spotify_auths", "navidrome_auths", "last_fm_auths"}

// authTimestampColumns are the columns added by spmixin.Timestamps{}.
var authTimestampColumns = []string{"created_at", "updated_at"}

// BackfillAuthTimestamps prepares pre-existing auth tables for the Ent
// auto-migration that adds created_at/updated_at (spmixin.Timestamps{}) to
// SpotifyAuth, NavidromeAuth, and LastFMAuth. It MUST run before
// Schema.Create: field.Time("created_at").Default(time.Now) is a Go-level
// default only, so Ent emits `ADD COLUMN ... NOT NULL` with no SQL DEFAULT,
// which every dialect rejects when the table already has rows (PR #39
// adversarial-review finding). For each auth table that exists and lacks a
// timestamp column, the column is added as nullable via raw ALTER TABLE and
// backfilled with CURRENT_TIMESTAMP for existing rows; Schema.Create then
// sees populated columns and can safely tighten them to NOT NULL.
//
// The function is a no-op on fresh databases (tables absent) and on re-runs
// (columns already present), and is dialect-aware for sqlite3, postgres, and
// mysql like DedupeTracks and CreateEntityTagsTable.
func BackfillAuthTimestamps(ctx context.Context, driver string, db *sql.DB, logger *slog.Logger) error {
	for _, table := range authTimestampTables {
		exists, err := tableExists(ctx, driver, db, table)
		if err != nil {
			return fmt.Errorf("failed to check %s table existence: %w", table, err)
		}
		if !exists {
			continue
		}

		for _, column := range authTimestampColumns {
			hasColumn, err := columnExists(ctx, driver, db, table, column)
			if err != nil {
				return fmt.Errorf("failed to check %s.%s column existence: %w", table, column, err)
			}

			if !hasColumn {
				// Add the column nullable (no DEFAULT) so the ALTER succeeds
				// on non-empty tables. Identifiers come from the
				// package-level allowlists above, so interpolation is safe
				// across drivers.
				addColumn := fmt.Sprintf(`ALTER TABLE %s ADD COLUMN %s %s NULL`,
					table, column, timestampColumnType(driver))
				if _, err := db.ExecContext(ctx, addColumn); err != nil {
					return fmt.Errorf("failed to add %s.%s column: %w", table, column, err)
				}
			}

			// Backfill unconditionally, even when the column already existed:
			// a crash between the ALTER and this UPDATE would otherwise leave
			// NULL rows behind forever (the column-exists check would skip
			// them on restart) and Schema.Create's NOT NULL tightening would
			// crash-loop. The UPDATE touches zero rows when already
			// backfilled, so re-runs stay idempotent and never overwrite
			// existing values.
			backfill := fmt.Sprintf(`UPDATE %s SET %s = CURRENT_TIMESTAMP WHERE %s IS NULL`,
				table, column, column)
			if _, err := db.ExecContext(ctx, backfill); err != nil {
				return fmt.Errorf("failed to backfill %s.%s: %w", table, column, err)
			}

			if !hasColumn {
				logger.Info("backfilled timestamp column before schema migration",
					"table", table,
					"column", column)
			}
		}
	}
	return nil
}

// timestampColumnType returns the dialect-specific column type matching what
// Ent uses for field.Time on each backend, so the subsequent auto-migration
// only has to tighten nullability rather than change types.
func timestampColumnType(driver string) string {
	switch driver {
	case driverPostgres:
		return "TIMESTAMPTZ"
	case "mysql":
		return "TIMESTAMP"
	default:
		return "DATETIME"
	}
}

// columnExists reports whether the named column exists on the named table.
func columnExists(ctx context.Context, driver string, db *sql.DB, table, column string) (bool, error) {
	var query string
	switch driver {
	case driverPostgres:
		query = `SELECT COUNT(*) FROM information_schema.columns
			WHERE table_schema = ANY (current_schemas(false)) AND table_name = $1 AND column_name = $2`
	case "mysql":
		query = `SELECT COUNT(*) FROM information_schema.columns
			WHERE table_schema = DATABASE() AND table_name = ? AND column_name = ?`
	default:
		query = `SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`
	}

	var count int
	if err := db.QueryRowContext(ctx, query, table, column).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}
