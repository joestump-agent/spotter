// Governing: SPEC-0016 REQ "Denormalized Entity Tags Table", ADR-0023 (PostgreSQL), ADR-0004 (Ent ORM)
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
	// Governing: SPEC-0016 REQ "Denormalized Entity Tags Table", ADR-0023
	// MySQL/MariaDB needs its own path: go-sql-driver rejects multiple
	// statements per Exec by default, AUTOINCREMENT is SQLite-only syntax,
	// and CREATE INDEX has no IF NOT EXISTS guard on MySQL 8.
	if driver == "mysql" {
		return createEntityTagsTableMySQL(ctx, db)
	}

	var ddl string
	if driver == driverPostgres {
		ddl = entityTagsPostgres
	} else {
		ddl = entityTagsSQLite
	}

	if _, err := db.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("failed to create entity_tags table: %w", err)
	}
	return nil
}

// createEntityTagsTableMySQL creates entity_tags on MySQL/MariaDB. Each
// statement is executed individually, and index creation is guarded by an
// information_schema lookup because MySQL 8 lacks CREATE INDEX IF NOT EXISTS.
// Governing: SPEC-0016 REQ "Denormalized Entity Tags Table", ADR-0023
func createEntityTagsTableMySQL(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, entityTagsMySQL); err != nil {
		return fmt.Errorf("failed to create entity_tags table: %w", err)
	}

	for _, idx := range entityTagsMySQLIndexes {
		exists, err := mysqlIndexExists(ctx, db, "entity_tags", idx.name)
		if err != nil {
			return fmt.Errorf("failed to check index %s on entity_tags: %w", idx.name, err)
		}
		if exists {
			continue
		}
		if _, err := db.ExecContext(ctx, idx.ddl); err != nil {
			return fmt.Errorf("failed to create index %s on entity_tags: %w", idx.name, err)
		}
	}
	return nil
}

// mysqlIndexExists reports whether the named index already exists on the
// given table in the current schema.
func mysqlIndexExists(ctx context.Context, db *sql.DB, table, index string) (bool, error) {
	var count int
	err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM information_schema.statistics
		 WHERE table_schema = DATABASE() AND table_name = ? AND index_name = ?`,
		table, index).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
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

// entityTagsMySQL uses AUTO_INCREMENT (MySQL syntax; AUTOINCREMENT is
// SQLite-only) and table-level FOREIGN KEY constraints — MySQL silently
// ignores inline column REFERENCES clauses. Ent creates users.id and tags.id
// as signed BIGINT on MySQL, so the FK columns match that type.
const entityTagsMySQL = `
CREATE TABLE IF NOT EXISTS entity_tags (
    id BIGINT NOT NULL AUTO_INCREMENT,
    user_id BIGINT NOT NULL,
    tag_id BIGINT NOT NULL,
    tag_type VARCHAR(20) NOT NULL,
    tag_name VARCHAR(255) NOT NULL,
    entity_type VARCHAR(20) NOT NULL,
    entity_id BIGINT NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (id),
    CONSTRAINT entity_tags_unique UNIQUE (tag_id, entity_type, entity_id),
    CONSTRAINT entity_tags_user_id_fk FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
    CONSTRAINT entity_tags_tag_id_fk FOREIGN KEY (tag_id) REFERENCES tags(id) ON DELETE CASCADE
)`

// entityTagsMySQLIndexes are created one statement at a time; creation is
// skipped when information_schema reports the index already exists.
var entityTagsMySQLIndexes = []struct {
	name string
	ddl  string
}{
	{
		name: "idx_entity_tags_lookup",
		ddl:  `CREATE INDEX idx_entity_tags_lookup ON entity_tags (user_id, tag_type, tag_name, entity_type)`,
	},
	{
		name: "idx_entity_tags_entity",
		ddl:  `CREATE INDEX idx_entity_tags_entity ON entity_tags (entity_type, entity_id)`,
	},
}
