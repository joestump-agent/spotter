// Governing: SPEC-0016 REQ "Schema Migration", ADR-0004 (Ent ORM), ADR-0023
package database

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// legacyAuthDDL mirrors the auth tables as Ent created them BEFORE the
// spmixin.Timestamps{} mixin was added to SpotifyAuth, NavidromeAuth, and
// LastFMAuth (i.e. without created_at/updated_at). The users table is created
// in its current shape (it predates the mixin change and is unaffected by it)
// so the auth tables' foreign keys resolve exactly as on a real old database.
const legacyAuthDDL = `
CREATE TABLE users (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    username VARCHAR(255) NOT NULL UNIQUE,
    email VARCHAR(320) NULL,
    theme VARCHAR(50) NOT NULL DEFAULT 'dark',
    system_prompt VARCHAR(10000) NULL,
    pagination_size INTEGER NOT NULL DEFAULT 25,
    last_login_at DATETIME NOT NULL
);
CREATE TABLE spotify_auths (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    display_name TEXT NULL,
    last_synced_at DATETIME NULL,
    access_token TEXT NOT NULL,
    refresh_token TEXT NOT NULL,
    expiry DATETIME NOT NULL,
    user_spotify_auth INTEGER NOT NULL UNIQUE
        CONSTRAINT spotify_auths_users_spotify_auth REFERENCES users (id)
);
CREATE TABLE navidrome_auths (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    password TEXT NOT NULL,
    last_synced_at DATETIME NULL,
    user_navidrome_auth INTEGER NOT NULL UNIQUE
        CONSTRAINT navidrome_auths_users_navidrome_auth REFERENCES users (id)
);
CREATE TABLE last_fm_auths (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    last_synced_at DATETIME NULL,
    session_key TEXT NOT NULL,
    username TEXT NOT NULL,
    user_lastfm_auth INTEGER NOT NULL UNIQUE
        CONSTRAINT last_fm_auths_users_lastfm_auth REFERENCES users (id)
);`

// legacyAuthSeed populates one row per auth table, matching a real deployment
// where a user has connected all three services.
const legacyAuthSeed = `
INSERT INTO users (id, username, last_login_at) VALUES (1, 'alice', '2025-01-01 00:00:00');
INSERT INTO spotify_auths (id, access_token, refresh_token, expiry, user_spotify_auth)
    VALUES (1, 'tok', 'refresh', '2025-06-01 00:00:00', 1);
INSERT INTO navidrome_auths (id, password, user_navidrome_auth) VALUES (1, 'secret', 1);
INSERT INTO last_fm_auths (id, session_key, username, user_lastfm_auth) VALUES (1, 'sk', 'alice', 1);`

// setupLegacyAuthDatabase creates an on-disk SQLite database shaped like a
// release that predates the auth-table Timestamps mixin, with one seeded row
// per auth table. It returns the DSN to hand to NewClient.
func setupLegacyAuthDatabase(t *testing.T) string {
	t.Helper()
	source := "file:" + filepath.Join(t.TempDir(), "legacy.db") + "?_fk=1"

	db, err := sql.Open("sqlite3", source)
	if err != nil {
		t.Fatalf("failed to open sqlite db: %v", err)
	}
	defer func() { _ = db.Close() }()

	if _, err := db.Exec(legacyAuthDDL); err != nil {
		t.Fatalf("failed to create legacy schema: %v", err)
	}
	if _, err := db.Exec(legacyAuthSeed); err != nil {
		t.Fatalf("failed to seed legacy data: %v", err)
	}
	return source
}

// TestNewClient_Regression_AuthTimestampsUpgradeWithExistingRows is the
// regression test for the PR #39 adversarial-review finding: adding
// spmixin.Timestamps{} to the auth schemas made Ent auto-migration emit
// `ALTER TABLE ... ADD COLUMN created_at ... NOT NULL` with no DEFAULT, which
// SQLite rejects on any table that already has rows:
//
//	MIGRATION FAILED: sql/schema: add column "created_at" to table:
//	"last_fm_auths": Cannot add a NOT NULL column with default value NULL
//
// field.Time("created_at").Default(time.Now) is a Go-level default only, so
// every existing deployment with connected services would fail to start.
// The fix backfills the columns via raw SQL before Schema.Create runs
// (BackfillAuthTimestamps in backfill_timestamps.go).
func TestNewClient_Regression_AuthTimestampsUpgradeWithExistingRows(t *testing.T) {
	source := setupLegacyAuthDatabase(t)

	// Phase 2 of the upgrade: boot the new binary against the old database.
	client, err := NewClient("sqlite3", source, nil)
	if err != nil {
		t.Fatalf("NewClient failed migrating a pre-timestamps database with existing auth rows: %v", err)
	}
	defer func() { _ = client.Close() }()

	// The seeded rows must survive with non-null backfilled timestamps.
	db, err := sql.Open("sqlite3", source)
	if err != nil {
		t.Fatalf("failed to reopen sqlite db: %v", err)
	}
	defer func() { _ = db.Close() }()

	for _, table := range []string{"spotify_auths", "navidrome_auths", "last_fm_auths"} {
		assertIntQuery(t, db, `SELECT COUNT(*) FROM `+table, 1)
		assertIntQuery(t, db,
			`SELECT COUNT(*) FROM `+table+` WHERE created_at IS NOT NULL AND updated_at IS NOT NULL`, 1)
	}

	// The migrated schema must be usable by the generated client, proving the
	// backfilled columns are compatible with what Ent expects.
	ctx := context.Background()
	auth, err := client.LastFMAuth.Get(ctx, 1)
	if err != nil {
		t.Fatalf("failed to read migrated last_fm_auths row via ent: %v", err)
	}
	if auth.CreatedAt.IsZero() || auth.UpdatedAt.IsZero() {
		t.Fatalf("expected non-zero backfilled timestamps, got created_at=%v updated_at=%v",
			auth.CreatedAt, auth.UpdatedAt)
	}
	if auth.SessionKey != "sk" || auth.Username != "alice" {
		t.Fatalf("existing auth data corrupted by migration: %+v", auth)
	}
}

// TestBackfillAuthTimestamps_FreshDatabase verifies the pre-migration step is
// a no-op when the auth tables do not exist yet (fresh install).
func TestBackfillAuthTimestamps_FreshDatabase(t *testing.T) {
	db, err := sql.Open("sqlite3", "file:"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("failed to open sqlite db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	if err := BackfillAuthTimestamps(context.Background(), "sqlite3", db, logger); err != nil {
		t.Fatalf("BackfillAuthTimestamps on empty database returned error: %v", err)
	}
}

// TestBackfillAuthTimestamps_Idempotent verifies re-running the backfill
// leaves already-present columns and their values untouched, so restarting
// the server (or re-running against an already-migrated database) is safe.
func TestBackfillAuthTimestamps_Idempotent(t *testing.T) {
	source := setupLegacyAuthDatabase(t)
	db, err := sql.Open("sqlite3", source)
	if err != nil {
		t.Fatalf("failed to open sqlite db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	if err := BackfillAuthTimestamps(ctx, "sqlite3", db, logger); err != nil {
		t.Fatalf("first BackfillAuthTimestamps run returned error: %v", err)
	}

	// Capture the backfilled values, then pin one to a sentinel to prove the
	// second run does not overwrite existing data.
	if _, err := db.Exec(`UPDATE last_fm_auths SET created_at = '1999-12-31 23:59:59'`); err != nil {
		t.Fatalf("failed to pin sentinel timestamp: %v", err)
	}

	if err := BackfillAuthTimestamps(ctx, "sqlite3", db, logger); err != nil {
		t.Fatalf("second BackfillAuthTimestamps run returned error: %v", err)
	}

	for _, table := range []string{"spotify_auths", "navidrome_auths", "last_fm_auths"} {
		assertIntQuery(t, db,
			`SELECT COUNT(*) FROM `+table+` WHERE created_at IS NOT NULL AND updated_at IS NOT NULL`, 1)
	}
	assertIntQuery(t, db,
		`SELECT COUNT(*) FROM last_fm_auths WHERE created_at = '1999-12-31 23:59:59'`, 1)
}
