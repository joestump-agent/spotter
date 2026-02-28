// Governing: SPEC-0014 REQ "Denormalized Entity Tags Table", ADR-0023 (PostgreSQL), ADR-0004 (Ent ORM)
package database

import (
	"context"
	"database/sql"
	"fmt"
)

// CreateEntityTagsTable creates the denormalized entity_tags query table
// if it does not already exist. This table lives outside the Ent schema
// and is maintained via raw SQL for cross-entity filtered tag lookups.
func CreateEntityTagsTable(ctx context.Context, driver string, db *sql.DB) error {
	var ddl string
	if driver == "postgres" {
		ddl = entityTagsPostgres
	} else {
		ddl = entityTagsSQLite
	}

	if _, err := db.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("failed to create entity_tags table: %w", err)
	}
	return nil
}

const entityTagsPostgres = `
CREATE TABLE IF NOT EXISTS entity_tags (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    tag_id BIGINT NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
    tag_type VARCHAR(20) NOT NULL,
    tag_name VARCHAR(255) NOT NULL,
    entity_type VARCHAR(20) NOT NULL,
    entity_id BIGINT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT entity_tags_unique UNIQUE (tag_id, entity_type, entity_id)
);
CREATE INDEX IF NOT EXISTS idx_entity_tags_lookup ON entity_tags (user_id, tag_type, tag_name, entity_type);
CREATE INDEX IF NOT EXISTS idx_entity_tags_entity ON entity_tags (entity_type, entity_id);
`

const entityTagsSQLite = `
CREATE TABLE IF NOT EXISTS entity_tags (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    tag_id INTEGER NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
    tag_type VARCHAR(20) NOT NULL,
    tag_name VARCHAR(255) NOT NULL,
    entity_type VARCHAR(20) NOT NULL,
    entity_id INTEGER NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (tag_id, entity_type, entity_id)
);
CREATE INDEX IF NOT EXISTS idx_entity_tags_lookup ON entity_tags (user_id, tag_type, tag_name, entity_type);
CREATE INDEX IF NOT EXISTS idx_entity_tags_entity ON entity_tags (entity_type, entity_id);
`
