// Governing: SPEC-0014 REQ "Enricher Integration", SPEC-0014 REQ "Denormalized Entity Tags Table",
// SPEC-0014 REQ "Data Migration", SPEC-0014 REQ "Tag Normalization"
package tags

import (
	"context"
	"database/sql"
	"fmt"

	"spotter/ent"
	entalbum "spotter/ent/album"
	entartist "spotter/ent/artist"
	"spotter/ent/tag"
	enttrack "spotter/ent/track"
	"spotter/ent/user"
	"spotter/internal/database"
)

// TypedTag represents a tag with a specific type classification.
// Governing: SPEC-0014 REQ "Enricher Integration"
type TypedTag struct {
	Name string
	Type string // "id3", "genre", "ai", "label", "source"
}

// UpsertTagsForEntity creates or retrieves Tag entities for the given typed tags,
// associates them with the specified entity via Ent edges, and maintains the
// denormalized entity_tags table. The Ent edge mutations and the raw entity_tags
// writes run inside a single database transaction (via the sql/execquery codegen
// feature on *ent.Tx), so the edge table and entity_tags can never diverge on a
// partial failure. Idempotent: safe to call multiple times.
// Governing: SPEC-0014 REQ "Enricher Integration", SPEC-0014 REQ "Denormalized Entity Tags Table"
func UpsertTagsForEntity(ctx context.Context, client *ent.Client, db *sql.DB, userID int, entityType string, entityID int, typed []TypedTag) error {
	// Governing: SPEC-0016 REQ "Denormalized Entity Tags Table", ADR-0023
	// Resolve the dialect once so the raw entity_tags statements can pick
	// driver-specific placeholder and conflict-handling syntax. The client and
	// db are opened from the same driver/source, so db is a valid proxy for
	// the client's dialect.
	driver := database.DriverName(db)
	tx, err := client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("begin tag upsert tx: %w", err)
	}
	if _, err := upsertTagsTx(ctx, tx, driver, userID, entityType, entityID, typed); err != nil {
		if rerr := tx.Rollback(); rerr != nil {
			err = fmt.Errorf("%w (rollback: %v)", err, rerr)
		}
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit tag upsert tx: %w", err)
	}
	return nil
}

// ReplaceTagsForEntity makes the entity's tag associations exactly match the
// given typed tags: missing tags are upserted and associated, and existing
// associations to tags outside the set are dissociated — both the Ent edge and
// the corresponding entity_tags rows are removed in the same transaction.
// Passing an empty set dissociates all tags from the entity. Tag entities
// themselves are never deleted; only associations are.
// Governing: SPEC-0014 REQ "Denormalized Entity Tags Table" (dissociation path)
func ReplaceTagsForEntity(ctx context.Context, client *ent.Client, db *sql.DB, userID int, entityType string, entityID int, typed []TypedTag) error {
	driver := database.DriverName(db)
	tx, err := client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("begin tag replace tx: %w", err)
	}
	if err := replaceTagsTx(ctx, tx, driver, userID, entityType, entityID, typed); err != nil {
		if rerr := tx.Rollback(); rerr != nil {
			err = fmt.Errorf("%w (rollback: %v)", err, rerr)
		}
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit tag replace tx: %w", err)
	}
	return nil
}

// RemoveAllTagsForEntity dissociates every tag from the entity, clearing both
// the Ent edges and the denormalized entity_tags rows atomically.
// Governing: SPEC-0014 REQ "Denormalized Entity Tags Table" (dissociation path)
func RemoveAllTagsForEntity(ctx context.Context, client *ent.Client, db *sql.DB, userID int, entityType string, entityID int) error {
	return ReplaceTagsForEntity(ctx, client, db, userID, entityType, entityID, nil)
}

// upsertTagsTx performs the tag upserts, edge associations, and entity_tags
// inserts on the given transaction, returning the IDs of all tags that are now
// associated with the entity from the input set.
func upsertTagsTx(ctx context.Context, tx *ent.Tx, driver string, userID int, entityType string, entityID int, typed []TypedTag) ([]int, error) {
	var tagIDs []int
	for _, tt := range typed {
		// Governing: SPEC-0014 REQ "Tag Normalization"
		// Store the trimmed, whitespace-collapsed original casing as the
		// display name; the normalized key is the lowercase form of the same.
		display := DisplayName(tt.Name)
		normalized := Normalize(tt.Name)
		if display == "" || normalized == "" {
			continue
		}

		tagType := tag.TagType(tt.Type)

		// Look up existing tag by (normalized_name, tag_type, user_id)
		t, err := tx.Tag.Query().
			Where(
				tag.NormalizedNameEQ(normalized),
				tag.TagTypeEQ(tagType),
				tag.HasUserWith(user.IDEQ(userID)),
			).
			Only(ctx)

		if ent.IsNotFound(err) {
			// Create new tag
			t, err = tx.Tag.Create().
				SetName(display).
				SetNormalizedName(normalized).
				SetTagType(tagType).
				SetUserID(userID).
				Save(ctx)
			if err != nil {
				return nil, fmt.Errorf("create tag %q (type %s): %w", display, tt.Type, err)
			}
		} else if err != nil {
			return nil, fmt.Errorf("query tag %q (type %s): %w", normalized, tt.Type, err)
		}

		// Add entity to the tag's edge (idempotent — Ent ignores duplicate edges)
		switch entityType {
		case "artist":
			err = t.Update().AddArtistIDs(entityID).Exec(ctx)
		case "album":
			err = t.Update().AddAlbumIDs(entityID).Exec(ctx)
		case "track":
			err = t.Update().AddTrackIDs(entityID).Exec(ctx)
		default:
			return nil, fmt.Errorf("unknown entity type: %s", entityType)
		}
		if err != nil {
			return nil, fmt.Errorf("add %s edge for tag %q: %w", entityType, normalized, err)
		}

		// Insert into denormalized entity_tags table on the SAME transaction
		// (conflict-ignoring insert for idempotency).
		// Governing: SPEC-0016 REQ "Denormalized Entity Tags Table", ADR-0023
		if _, err := tx.ExecContext(ctx,
			entityTagInsertSQL(driver),
			userID, t.ID, tt.Type, normalized, entityType, entityID,
		); err != nil {
			return nil, fmt.Errorf("upsert entity_tag for tag %q: %w", normalized, err)
		}

		tagIDs = append(tagIDs, t.ID)
	}
	return tagIDs, nil
}

// replaceTagsTx upserts the desired tag set, then dissociates every tag that
// is currently linked to the entity but absent from the desired set — removing
// the Ent edge and deleting the matching entity_tags rows in the same
// transaction.
func replaceTagsTx(ctx context.Context, tx *ent.Tx, driver string, userID int, entityType string, entityID int, typed []TypedTag) error {
	keepIDs, err := upsertTagsTx(ctx, tx, driver, userID, entityType, entityID, typed)
	if err != nil {
		return err
	}

	keep := make(map[int]bool, len(keepIDs))
	for _, id := range keepIDs {
		keep[id] = true
	}

	// Current tag associations for this entity, scoped to the user (tags are
	// per-user, so cross-user tags can never appear on this edge anyway).
	currentIDs, err := currentTagIDs(ctx, tx, userID, entityType, entityID)
	if err != nil {
		return fmt.Errorf("query current tags for %s %d: %w", entityType, entityID, err)
	}

	var stale []int
	for _, id := range currentIDs {
		if !keep[id] {
			stale = append(stale, id)
		}
	}
	if len(stale) == 0 {
		return nil
	}

	// Remove the Ent edges for stale tags.
	switch entityType {
	case "artist":
		err = tx.Artist.UpdateOneID(entityID).RemoveTagEntityIDs(stale...).Exec(ctx)
	case "album":
		err = tx.Album.UpdateOneID(entityID).RemoveTagEntityIDs(stale...).Exec(ctx)
	case "track":
		err = tx.Track.UpdateOneID(entityID).RemoveTagEntityIDs(stale...).Exec(ctx)
	default:
		return fmt.Errorf("unknown entity type: %s", entityType)
	}
	if err != nil {
		return fmt.Errorf("remove stale tag edges for %s %d: %w", entityType, entityID, err)
	}

	// Delete the matching denormalized rows on the SAME transaction.
	// Governing: SPEC-0014 REQ "Denormalized Entity Tags Table" (dissociation path)
	for _, id := range stale {
		if _, err := tx.ExecContext(ctx,
			entityTagDeleteSQL(driver),
			id, entityType, entityID,
		); err != nil {
			return fmt.Errorf("delete entity_tag row (tag %d, %s %d): %w", id, entityType, entityID, err)
		}
	}
	return nil
}

// currentTagIDs returns the IDs of all tags currently associated with the
// entity, restricted to the given user's tags.
func currentTagIDs(ctx context.Context, tx *ent.Tx, userID int, entityType string, entityID int) ([]int, error) {
	q := tx.Tag.Query().Where(tag.HasUserWith(user.IDEQ(userID)))
	switch entityType {
	case "artist":
		return q.Where(tag.HasArtistsWith(entartist.IDEQ(entityID))).IDs(ctx)
	case "album":
		return q.Where(tag.HasAlbumsWith(entalbum.IDEQ(entityID))).IDs(ctx)
	case "track":
		return q.Where(tag.HasTracksWith(enttrack.IDEQ(entityID))).IDs(ctx)
	default:
		return nil, fmt.Errorf("unknown entity type: %s", entityType)
	}
}

// entityTagInsertSQL returns the dialect-specific conflict-ignoring INSERT
// for entity_tags. MySQL has no ON CONFLICT clause and uses ? placeholders,
// so it gets INSERT IGNORE; PostgreSQL keeps $N placeholders with
// ON CONFLICT ... DO NOTHING; SQLite supports the same ON CONFLICT form.
// Governing: SPEC-0016 REQ "Denormalized Entity Tags Table", ADR-0023
func entityTagInsertSQL(driver string) string {
	switch driver {
	case "postgres":
		return `INSERT INTO entity_tags (user_id, tag_id, tag_type, tag_name, entity_type, entity_id)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 ON CONFLICT (tag_id, entity_type, entity_id) DO NOTHING`
	case "mysql":
		return `INSERT IGNORE INTO entity_tags (user_id, tag_id, tag_type, tag_name, entity_type, entity_id)
		 VALUES (?, ?, ?, ?, ?, ?)`
	default: // sqlite3
		return `INSERT INTO entity_tags (user_id, tag_id, tag_type, tag_name, entity_type, entity_id)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT (tag_id, entity_type, entity_id) DO NOTHING`
	}
}

// entityTagDeleteSQL returns the dialect-specific DELETE that removes the
// denormalized row for one (tag, entity) association.
// Governing: SPEC-0014 REQ "Denormalized Entity Tags Table" (dissociation path)
func entityTagDeleteSQL(driver string) string {
	if driver == "postgres" {
		return `DELETE FROM entity_tags WHERE tag_id = $1 AND entity_type = $2 AND entity_id = $3`
	}
	return `DELETE FROM entity_tags WHERE tag_id = ? AND entity_type = ? AND entity_id = ?`
}
