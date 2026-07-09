// Governing: SPEC-0014 REQ "Enricher Integration", SPEC-0014 REQ "Denormalized Entity Tags Table",
// SPEC-0014 REQ "Data Migration"
package tags

import (
	"context"
	"database/sql"
	"fmt"

	"spotter/ent"
	"spotter/ent/tag"
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
// denormalized entity_tags table. Idempotent: safe to call multiple times.
// Governing: SPEC-0014 REQ "Enricher Integration", SPEC-0014 REQ "Denormalized Entity Tags Table"
func UpsertTagsForEntity(ctx context.Context, client *ent.Client, db *sql.DB, userID int, entityType string, entityID int, typed []TypedTag) error {
	// Governing: SPEC-0016 REQ "Denormalized Entity Tags Table", ADR-0023
	// Resolve the dialect once so upsertEntityTag can pick driver-specific
	// placeholder and conflict-handling syntax (mirrors how the database
	// package threads the driver name into its raw-SQL helpers).
	driver := database.DriverName(db)
	for _, tt := range typed {
		if tt.Name == "" {
			continue
		}

		normalized := Normalize(tt.Name)
		if normalized == "" {
			continue
		}

		tagType := tag.TagType(tt.Type)

		// Look up existing tag by (normalized_name, tag_type, user_id)
		t, err := client.Tag.Query().
			Where(
				tag.NormalizedNameEQ(normalized),
				tag.TagTypeEQ(tagType),
				tag.HasUserWith(user.IDEQ(userID)),
			).
			Only(ctx)

		if ent.IsNotFound(err) {
			// Create new tag
			t, err = client.Tag.Create().
				SetName(tt.Name).
				SetNormalizedName(normalized).
				SetTagType(tagType).
				SetUserID(userID).
				Save(ctx)
			if err != nil {
				return fmt.Errorf("create tag %q (type %s): %w", tt.Name, tt.Type, err)
			}
		} else if err != nil {
			return fmt.Errorf("query tag %q (type %s): %w", normalized, tt.Type, err)
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
			return fmt.Errorf("unknown entity type: %s", entityType)
		}
		if err != nil {
			return fmt.Errorf("add %s edge for tag %q: %w", entityType, normalized, err)
		}

		// Upsert into denormalized entity_tags table (conflict-ignoring insert for idempotency)
		if err := upsertEntityTag(ctx, db, driver, userID, t.ID, tt.Type, normalized, entityType, entityID); err != nil {
			return fmt.Errorf("upsert entity_tag for tag %q: %w", normalized, err)
		}
	}
	return nil
}

// upsertEntityTag inserts a row into entity_tags, ignoring conflicts for idempotency.
// Governing: SPEC-0016 REQ "Denormalized Entity Tags Table", ADR-0023
func upsertEntityTag(ctx context.Context, db *sql.DB, driver string, userID, tagID int, tagType, tagName, entityType string, entityID int) error {
	_, err := db.ExecContext(ctx,
		entityTagInsertSQL(driver),
		userID, tagID, tagType, tagName, entityType, entityID,
	)
	return err
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
