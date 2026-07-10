// Governing: SPEC-0016 REQ "Denormalized Entity Tags Table", ADR-0023
package database

import (
	"context"
	"database/sql"
	"os"
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

// TestEntityTagsDDLDialects is the CI-friendly per-dialect unit test: it
// asserts token-level invariants of each dialect's DDL without needing a
// live server. Ported from upstream joestump/spotter#359.
func TestEntityTagsDDLDialects(t *testing.T) {
	// Assemble the full DDL text per dialect from the fork's constants.
	// MySQL indexes live in separate statements (information_schema guard),
	// so join them in for token checks.
	mysqlDDL := entityTagsMySQL
	for _, idx := range entityTagsMySQLIndexes {
		mysqlDDL += ";\n" + idx.ddl
	}

	tests := []struct {
		driver       string
		ddl          string
		wantTokens   []string
		rejectTokens []string
	}{
		{
			driver: "mysql",
			ddl:    mysqlDDL,
			wantTokens: []string{
				"AUTO_INCREMENT",
				"ENGINE=InnoDB",
				"DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP",
				"idx_entity_tags_lookup",
				"idx_entity_tags_entity",
				"entity_tags_unique",
			},
			// SQLite spelling must not leak into MySQL DDL, and TIMESTAMP
			// (2038 limit, implicit auto-update behavior) must not be used.
			rejectTokens: []string{"AUTOINCREMENT", "created_at TIMESTAMP", "BIGSERIAL"},
		},
		{
			driver: driverPostgres,
			ddl:    entityTagsPostgres,
			wantTokens: []string{
				"BIGSERIAL PRIMARY KEY",
				"TIMESTAMPTZ",
				"idx_entity_tags_lookup",
				"idx_entity_tags_entity",
				"entity_tags_unique",
			},
			rejectTokens: []string{"AUTO_INCREMENT", "ENGINE=InnoDB"},
		},
		{
			driver: "sqlite3",
			ddl:    entityTagsSQLite,
			wantTokens: []string{
				"INTEGER PRIMARY KEY AUTOINCREMENT",
				"idx_entity_tags_lookup",
				"idx_entity_tags_entity",
			},
			rejectTokens: []string{"ENGINE=InnoDB", "BIGSERIAL", "AUTO_INCREMENT"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.driver, func(t *testing.T) {
			for _, tok := range tt.wantTokens {
				assert.Contains(t, tt.ddl, tok, "%s DDL missing %q", tt.driver, tok)
			}
			for _, tok := range tt.rejectTokens {
				assert.NotContains(t, tt.ddl, tok, "%s DDL must not contain %q", tt.driver, tok)
			}
		})
	}
}

// TestCreateEntityTagsTable_UnknownDriverFallsBackToSQLite verifies that an
// unrecognized driver name takes the SQLite path, matching driverToStdlib.
func TestCreateEntityTagsTable_UnknownDriverFallsBackToSQLite(t *testing.T) {
	db, err := sql.Open("sqlite3", "file:"+t.Name()+"?mode=memory&cache=shared")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()
	require.NoError(t, CreateEntityTagsTable(ctx, "bogus", db))

	var count int
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'entity_tags'`).Scan(&count))
	assert.Equal(t, 1, count)
}

// TestCreateEntityTagsTable_Matrix runs the real DDL against each driver.
// Ported from upstream joestump/spotter#359.
//
// SQLite always runs (in-memory). Postgres and MySQL/MariaDB run only when
// a live server is available, signalled via environment variables:
//
//	SPOTTER_TEST_POSTGRES_DSN — e.g. "host=localhost port=5432 user=spotter password=spotter dbname=spotter sslmode=disable"
//	SPOTTER_TEST_MYSQL_DSN   — e.g. "spotter:spotter@tcp(localhost:3306)/spotter?parseTime=true&charset=utf8mb4"
//
// If the variable is unset, the sub-test skips gracefully so CI without
// databases stays green. To run locally with throwaway containers:
//
//	docker run --rm -d --name spotter-pg -e POSTGRES_USER=spotter -e POSTGRES_PASSWORD=spotter -e POSTGRES_DB=spotter -p 5432:5432 postgres:16
//	docker run --rm -d --name spotter-maria -e MARIADB_USER=spotter -e MARIADB_PASSWORD=spotter -e MARIADB_DATABASE=spotter -e MARIADB_ROOT_PASSWORD=root -p 3306:3306 mariadb:11
//	SPOTTER_TEST_POSTGRES_DSN="host=localhost port=5432 user=spotter password=spotter dbname=spotter sslmode=disable" \
//	SPOTTER_TEST_MYSQL_DSN="spotter:spotter@tcp(localhost:3306)/spotter?parseTime=true&charset=utf8mb4" \
//	go test ./internal/database/ -run TestCreateEntityTagsTable_Matrix -v
//
// WARNING: the live sub-tests DROP the users, tags, and entity_tags tables
// in the target database. Point the DSNs at throwaway databases only.
func TestCreateEntityTagsTable_Matrix(t *testing.T) {
	t.Run("sqlite3", func(t *testing.T) {
		db, err := sql.Open("sqlite3", "file:"+t.Name()+"?mode=memory&cache=shared&_fk=1")
		require.NoError(t, err)
		t.Cleanup(func() { _ = db.Close() })
		runEntityTagsMatrix(t, "sqlite3", db,
			`CREATE TABLE users (id INTEGER PRIMARY KEY AUTOINCREMENT)`,
			`CREATE TABLE tags (id INTEGER PRIMARY KEY AUTOINCREMENT)`)
		assertSQLiteColumns(t, db)
	})

	t.Run("postgres", func(t *testing.T) {
		dsn := os.Getenv("SPOTTER_TEST_POSTGRES_DSN")
		if dsn == "" {
			t.Skip("SPOTTER_TEST_POSTGRES_DSN not set; skipping live PostgreSQL test")
		}
		db := openLiveDB(t, driverPostgres, dsn)
		dropEntityTagsTables(t, db)
		runEntityTagsMatrix(t, driverPostgres, db,
			`CREATE TABLE users (id BIGSERIAL PRIMARY KEY)`,
			`CREATE TABLE tags (id BIGSERIAL PRIMARY KEY)`)
		assertInformationSchemaColumns(t, db, driverPostgres, map[string]string{
			"id":         "bigint",
			"user_id":    "bigint",
			"entity_id":  "bigint",
			"tag_type":   "character varying",
			"created_at": "timestamp with time zone",
		})
	})

	t.Run("mysql", func(t *testing.T) {
		dsn := os.Getenv("SPOTTER_TEST_MYSQL_DSN")
		if dsn == "" {
			t.Skip("SPOTTER_TEST_MYSQL_DSN not set; skipping live MariaDB/MySQL test")
		}
		db := openLiveDB(t, "mysql", dsn)
		dropEntityTagsTables(t, db)
		runEntityTagsMatrix(t, "mysql", db,
			`CREATE TABLE users (id BIGINT AUTO_INCREMENT PRIMARY KEY) ENGINE=InnoDB`,
			`CREATE TABLE tags (id BIGINT AUTO_INCREMENT PRIMARY KEY) ENGINE=InnoDB`)
		assertInformationSchemaColumns(t, db, "mysql", map[string]string{
			"id":         "bigint",
			"user_id":    "bigint",
			"entity_id":  "bigint",
			"tag_type":   "varchar",
			"created_at": "datetime",
		})
		// The fork guards MySQL index creation via information_schema
		// (no CREATE INDEX IF NOT EXISTS on MySQL 8); verify both indexes
		// landed and the guard sees them.
		for _, idx := range entityTagsMySQLIndexes {
			exists, err := mysqlIndexExists(context.Background(), db, "entity_tags", idx.name)
			require.NoError(t, err)
			assert.True(t, exists, "index %s should exist after migration", idx.name)
		}
	})
}

// openLiveDB connects and pings; if the server is unreachable the test is
// skipped (not failed) so a stale env var doesn't break local runs.
func openLiveDB(t *testing.T, driver, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open(driver, dsn)
	require.NoError(t, err, "failed to open %s", driver)
	t.Cleanup(func() { _ = db.Close() })
	if err := db.PingContext(context.Background()); err != nil {
		t.Skipf("%s server not reachable (%v); skipping", driver, err)
	}
	return db
}

// dropEntityTagsTables resets state on live servers so re-runs are clean.
// entity_tags is dropped first because it carries FKs to users and tags.
func dropEntityTagsTables(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	for _, stmt := range []string{
		`DROP TABLE IF EXISTS entity_tags`,
		`DROP TABLE IF EXISTS tags`,
		`DROP TABLE IF EXISTS users`,
	} {
		_, err := db.ExecContext(ctx, stmt)
		require.NoError(t, err, "cleanup %q failed", stmt)
	}
}

// runEntityTagsMatrix creates minimal parent tables (entity_tags carries
// FKs to users and tags, which Ent normally creates before this migration
// runs — see NewClient in db.go), runs CreateEntityTagsTable twice to
// prove idempotency, and round-trips an insert/select including the
// unique-constraint check required by the governing spec.
func runEntityTagsMatrix(t *testing.T, driver string, db *sql.DB, usersDDL, tagsDDL string) {
	t.Helper()
	ctx := context.Background()

	for _, stmt := range []string{usersDDL, tagsDDL} {
		_, err := db.ExecContext(ctx, stmt)
		require.NoError(t, err, "failed to create parent table")
	}

	require.NoError(t, CreateEntityTagsTable(ctx, driver, db),
		"CreateEntityTagsTable(%s) failed", driver)
	// Idempotency: the migration runs on every startup.
	require.NoError(t, CreateEntityTagsTable(ctx, driver, db),
		"CreateEntityTagsTable(%s) second run failed", driver)

	// Sanity insert/select round-trip.
	for _, stmt := range []string{
		`INSERT INTO users (id) VALUES (1)`,
		`INSERT INTO tags (id) VALUES (1)`,
		`INSERT INTO entity_tags (user_id, tag_id, tag_type, tag_name, entity_type, entity_id) VALUES (1, 1, 'genre', 'jazz', 'artist', 42)`,
	} {
		_, err := db.ExecContext(ctx, stmt)
		require.NoError(t, err, "seed %q failed", stmt)
	}

	var tagName string
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT tag_name FROM entity_tags WHERE entity_type = 'artist' AND entity_id = 42`).Scan(&tagName))
	assert.Equal(t, "jazz", tagName)

	// The (tag_id, entity_type, entity_id) unique constraint must hold.
	_, err := db.ExecContext(ctx,
		`INSERT INTO entity_tags (user_id, tag_id, tag_type, tag_name, entity_type, entity_id) VALUES (1, 1, 'genre', 'jazz', 'artist', 42)`)
	assert.Error(t, err, "duplicate (tag_id, entity_type, entity_id) insert succeeded; unique constraint missing")
}

// assertSQLiteColumns verifies column types via PRAGMA table_info.
func assertSQLiteColumns(t *testing.T, db *sql.DB) {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), `PRAGMA table_info(entity_tags)`)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()

	got := map[string]string{}
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		require.NoError(t, rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk))
		got[name] = strings.ToUpper(ctype)
	}
	require.NoError(t, rows.Err())

	for col, want := range map[string]string{
		"id":         "INTEGER",
		"user_id":    "INTEGER",
		"entity_id":  "INTEGER",
		"tag_name":   "VARCHAR(255)",
		"created_at": "DATETIME",
	} {
		assert.Equal(t, want, got[col], "sqlite column %s type", col)
	}
}

// assertInformationSchemaColumns verifies column types on live servers.
func assertInformationSchemaColumns(t *testing.T, db *sql.DB, driver string, want map[string]string) {
	t.Helper()
	var query string
	if driver == driverPostgres {
		query = `SELECT column_name, data_type FROM information_schema.columns WHERE table_name = 'entity_tags' AND table_schema = current_schema()`
	} else {
		query = `SELECT column_name, data_type FROM information_schema.columns WHERE table_name = 'entity_tags' AND table_schema = DATABASE()`
	}
	rows, err := db.QueryContext(context.Background(), query)
	require.NoError(t, err, "information_schema query failed")
	defer func() { _ = rows.Close() }()

	got := map[string]string{}
	for rows.Next() {
		var name, dtype string
		require.NoError(t, rows.Scan(&name, &dtype))
		got[strings.ToLower(name)] = strings.ToLower(dtype)
	}
	require.NoError(t, rows.Err())
	require.NotEmpty(t, got, "entity_tags not found in information_schema.columns")

	for col, wantType := range want {
		assert.Equal(t, wantType, got[col], "%s column %s type", driver, col)
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
