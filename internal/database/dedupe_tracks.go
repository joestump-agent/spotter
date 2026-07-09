// Governing: SPEC metadata-enrichment-pipeline (catalog uniqueness), ADR-0004 (Ent ORM),
// ADR-0023 (PostgreSQL)
package database

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
)

// trackDupeKey identifies a Track by its unique (artist, name) identity.
type trackDupeKey struct {
	artistID int64
	name     string
}

// DedupeTracks merges duplicate (artist, name) Track rows so the unique index
// on tracks (name, artist_tracks) can be created by the Ent auto-migration.
// It MUST run before Schema.Create: existing duplicate rows would otherwise
// make index creation fail. For each duplicate group the oldest row (by
// created_at, then id) is kept, listens and playlist_tracks are re-pointed to
// the keeper, tag links for the removed rows are deleted, and the duplicate
// rows are removed. Returns the number of duplicate rows deleted.
// The function is a no-op on fresh databases where the tracks table does not
// exist yet.
func DedupeTracks(ctx context.Context, driver string, db *sql.DB, logger *slog.Logger) (int, error) {
	exists, err := tableExists(ctx, driver, db, "tracks")
	if err != nil {
		return 0, fmt.Errorf("failed to check tracks table existence: %w", err)
	}
	if !exists {
		return 0, nil
	}

	// Scan tracks oldest-first so the first row seen per (artist, name) group
	// is the keeper.
	rows, err := db.QueryContext(ctx,
		`SELECT id, artist_tracks, name FROM tracks WHERE artist_tracks IS NOT NULL ORDER BY created_at ASC, id ASC`)
	if err != nil {
		return 0, fmt.Errorf("failed to query tracks for dedupe: %w", err)
	}
	defer func() { _ = rows.Close() }()

	keepers := make(map[trackDupeKey]int64)
	dupsByKeeper := make(map[int64][]int64)
	for rows.Next() {
		var id, artistID int64
		var name string
		if err := rows.Scan(&id, &artistID, &name); err != nil {
			return 0, fmt.Errorf("failed to scan track row: %w", err)
		}
		key := trackDupeKey{artistID: artistID, name: name}
		if keeper, ok := keepers[key]; ok {
			dupsByKeeper[keeper] = append(dupsByKeeper[keeper], id)
		} else {
			keepers[key] = id
		}
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("failed to iterate track rows: %w", err)
	}
	if len(dupsByKeeper) == 0 {
		return 0, nil
	}

	// Optional tables that may not exist on older schemas.
	hasPlaylistTracks, err := tableExists(ctx, driver, db, "playlist_tracks")
	if err != nil {
		return 0, fmt.Errorf("failed to check playlist_tracks table existence: %w", err)
	}
	hasListens, err := tableExists(ctx, driver, db, "listens")
	if err != nil {
		return 0, fmt.Errorf("failed to check listens table existence: %w", err)
	}
	hasTagTracks, err := tableExists(ctx, driver, db, "tag_tracks")
	if err != nil {
		return 0, fmt.Errorf("failed to check tag_tracks table existence: %w", err)
	}
	hasEntityTags, err := tableExists(ctx, driver, db, "entity_tags")
	if err != nil {
		return 0, fmt.Errorf("failed to check entity_tags table existence: %w", err)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("failed to begin track dedupe transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	removed := 0
	for keeper, dups := range dupsByKeeper {
		in := int64List(dups)
		// Re-point edges from the duplicates to the keeper. IDs are integers
		// built via strconv, so string interpolation is placeholder-safe
		// across drivers.
		if hasListens {
			if _, err := tx.ExecContext(ctx,
				fmt.Sprintf(`UPDATE listens SET track_listens = %d WHERE track_listens IN (%s)`, keeper, in)); err != nil {
				return 0, fmt.Errorf("failed to re-point listens to track %d: %w", keeper, err)
			}
		}
		if hasPlaylistTracks {
			if _, err := tx.ExecContext(ctx,
				fmt.Sprintf(`UPDATE playlist_tracks SET track_playlist_tracks = %d WHERE track_playlist_tracks IN (%s)`, keeper, in)); err != nil {
				return 0, fmt.Errorf("failed to re-point playlist_tracks to track %d: %w", keeper, err)
			}
		}
		// Drop tag links owned by the duplicates; the keeper's own links are
		// untouched and merging would risk unique-constraint conflicts.
		if hasTagTracks {
			if _, err := tx.ExecContext(ctx,
				fmt.Sprintf(`DELETE FROM tag_tracks WHERE track_id IN (%s)`, in)); err != nil {
				return 0, fmt.Errorf("failed to delete tag_tracks for duplicate tracks: %w", err)
			}
		}
		if hasEntityTags {
			if _, err := tx.ExecContext(ctx,
				fmt.Sprintf(`DELETE FROM entity_tags WHERE entity_type = 'track' AND entity_id IN (%s)`, in)); err != nil {
				return 0, fmt.Errorf("failed to delete entity_tags for duplicate tracks: %w", err)
			}
		}
		if _, err := tx.ExecContext(ctx,
			fmt.Sprintf(`DELETE FROM tracks WHERE id IN (%s)`, in)); err != nil {
			return 0, fmt.Errorf("failed to delete duplicate tracks: %w", err)
		}
		removed += len(dups)
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("failed to commit track dedupe transaction: %w", err)
	}

	logger.Info("merged duplicate tracks before schema migration",
		"duplicates_removed", removed,
		"groups_merged", len(dupsByKeeper))
	return removed, nil
}

// tableExists reports whether the named table exists in the connected database.
func tableExists(ctx context.Context, driver string, db *sql.DB, table string) (bool, error) {
	var query string
	switch driver {
	case driverPostgres, "mysql":
		query = `SELECT COUNT(*) FROM information_schema.tables WHERE table_name = $1`
		if driver == "mysql" {
			query = `SELECT COUNT(*) FROM information_schema.tables WHERE table_name = ?`
		}
	default:
		query = `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`
	}

	var count int
	if err := db.QueryRowContext(ctx, query, table).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

// int64List renders ids as a comma-separated list for SQL IN clauses.
func int64List(ids []int64) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.FormatInt(id, 10)
	}
	return strings.Join(parts, ", ")
}
