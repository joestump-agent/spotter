// Governing: SPEC-0016 REQ "Denormalized Entity Tags Table", ADR-0023
package database

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCreateEntityTagsTable_SQLite verifies the SQLite path creates the
// table and indexes and stays idempotent across repeated calls.
func TestCreateEntityTagsTable_SQLite(t *testing.T) {
	db, err := sql.Open("sqlite3", "file:"+t.Name()+"?mode=memory&cache=shared")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()
	require.NoError(t, CreateEntityTagsTable(ctx, "sqlite3", db))
	// Second call must be a no-op, not an error.
	require.NoError(t, CreateEntityTagsTable(ctx, "sqlite3", db))

	var count int
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'entity_tags'`).Scan(&count))
	assert.Equal(t, 1, count)

	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name IN ('idx_entity_tags_lookup', 'idx_entity_tags_entity')`).Scan(&count))
	assert.Equal(t, 2, count)
}

// TestEntityTagsMySQLDDL validates the MySQL DDL by construction: a real
// MySQL server is not available in CI, so assert the statement invariants
// that made the previous (SQLite-flavored) DDL fail on MySQL 8/MariaDB 10.6.
func TestEntityTagsMySQLDDL(t *testing.T) {
	// go-sql-driver rejects multiple statements per Exec by default, so the
	// CREATE TABLE must be a single statement with no separators.
	assert.NotContains(t, entityTagsMySQL, ";",
		"MySQL CREATE TABLE must be a single statement (go-sql-driver disallows multi-statement Exec by default)")

	// AUTOINCREMENT is SQLite-only; MySQL requires AUTO_INCREMENT.
	assert.Contains(t, entityTagsMySQL, "AUTO_INCREMENT")
	assert.NotContains(t, entityTagsMySQL, "AUTOINCREMENT",
		"AUTOINCREMENT (no underscore) is SQLite syntax and invalid on MySQL")

	// MySQL 8 has no CREATE INDEX IF NOT EXISTS; existence is checked via
	// information_schema before each CREATE INDEX instead.
	for _, idx := range entityTagsMySQLIndexes {
		assert.NotContains(t, idx.ddl, "IF NOT EXISTS",
			"CREATE INDEX IF NOT EXISTS is not MySQL 8 syntax; index %s must rely on the information_schema guard", idx.name)
		assert.NotContains(t, idx.ddl, ";")
		assert.Contains(t, idx.ddl, idx.name, "index DDL must create the name checked by the guard")
	}

	// MySQL silently ignores inline column REFERENCES clauses, so the FKs
	// must be declared as table-level constraints.
	assert.Contains(t, entityTagsMySQL, "FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE")
	assert.Contains(t, entityTagsMySQL, "FOREIGN KEY (tag_id) REFERENCES tags(id) ON DELETE CASCADE")
	for _, line := range strings.Split(entityTagsMySQL, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "user_id") || strings.HasPrefix(trimmed, "tag_id") {
			assert.NotContains(t, trimmed, "REFERENCES",
				"inline REFERENCES is silently ignored by MySQL; use table-level constraints")
		}
	}
}

// TestDriverName verifies dialect detection from a *sql.DB handle.
// sql.Open does not connect, so postgres/mysql handles can be constructed
// without a running server.
func TestDriverName(t *testing.T) {
	cases := []struct {
		driver string
		dsn    string
		want   string
	}{
		{"sqlite3", "file:drivername?mode=memory", "sqlite3"},
		{"postgres", "host=localhost dbname=unused", "postgres"},
		{"mysql", "user:pass@tcp(localhost:3306)/unused", "mysql"},
	}
	for _, tc := range cases {
		t.Run(tc.driver, func(t *testing.T) {
			db, err := sql.Open(tc.driver, tc.dsn)
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			assert.Equal(t, tc.want, DriverName(db))
		})
	}
}
