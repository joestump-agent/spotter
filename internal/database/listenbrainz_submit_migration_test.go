// Governing: SPEC music-provider-integration REQ "ListenBrainz Listen Submission" (REQ-PROV-054),
// SPEC-0016 REQ "Schema Migration", ADR-0004 (Ent ORM)
//
// Upgrade-safety tests for the listen-submission schema additions:
//   - listens.submitted_to_listenbrainz_at (nullable time)
//   - listen_brainz_auths.submit_listens (bool NOT NULL DEFAULT false)
//
// The PR #39 lesson (see backfill_timestamps_test.go): a NOT NULL column whose
// default exists only on the Go side bricks migration of any table that
// already has rows. These tests boot the new binary against a database shaped
// like the previous release, WITH seeded rows, and prove auto-migration
// succeeds because the new columns are nullable or carry a constant SQL
// default.
package database

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	entlisten "spotter/ent/listen"

	_ "github.com/mattn/go-sqlite3"
)

// legacyListenBrainzDDL mirrors the users, listen_brainz_auths, and listens
// tables as Ent created them BEFORE the listen-submission fields were added
// (no submit_listens, no submitted_to_listenbrainz_at). Constraint and index
// names match Ent's generated symbols so the tables look exactly like a real
// old database. The albums/artists/tracks tables referenced by the listens
// foreign keys are created by Ent during migration; SQLite permits declaring
// foreign keys against tables that do not exist yet.
const legacyListenBrainzDDL = `
CREATE TABLE users (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    username VARCHAR(255) NOT NULL UNIQUE,
    email VARCHAR(320) NULL,
    theme VARCHAR(50) NOT NULL DEFAULT 'dark',
    system_prompt VARCHAR(10000) NULL,
    pagination_size INTEGER NOT NULL DEFAULT 25,
    last_login_at DATETIME NOT NULL
);
CREATE TABLE listen_brainz_auths (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    last_synced_at DATETIME NULL,
    token TEXT NOT NULL,
    username TEXT NOT NULL,
    user_listenbrainz_auth INTEGER NOT NULL UNIQUE
        CONSTRAINT listen_brainz_auths_users_listenbrainz_auth REFERENCES users (id)
);
CREATE TABLE listens (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    track_name TEXT NOT NULL,
    artist_name TEXT NOT NULL,
    album_name TEXT NOT NULL,
    source TEXT NOT NULL,
    played_at DATETIME NOT NULL,
    url TEXT NULL,
    provider_track_id VARCHAR(2048) NULL DEFAULT '',
    album_listens INTEGER NULL
        CONSTRAINT listens_albums_listens REFERENCES albums (id) ON DELETE SET NULL,
    artist_listens INTEGER NULL
        CONSTRAINT listens_artists_listens REFERENCES artists (id) ON DELETE SET NULL,
    track_listens INTEGER NULL
        CONSTRAINT listens_tracks_listens REFERENCES tracks (id) ON DELETE SET NULL,
    user_listens INTEGER NOT NULL
        CONSTRAINT listens_users_listens REFERENCES users (id)
);
CREATE UNIQUE INDEX listen_played_at_source_track_name_artist_name_user_listens
    ON listens (played_at, source, track_name, artist_name, user_listens);`

// legacyListenBrainzSeed matches a real deployment: one user with a connected
// ListenBrainz account and existing listens from two different sources.
const legacyListenBrainzSeed = `
INSERT INTO users (id, username, last_login_at) VALUES (1, 'alice', '2025-01-01 00:00:00');
INSERT INTO listen_brainz_auths (id, created_at, updated_at, token, username, user_listenbrainz_auth)
    VALUES (1, '2025-01-01 00:00:00', '2025-01-01 00:00:00', 'tok', 'alice-lb', 1);
INSERT INTO listens (id, track_name, artist_name, album_name, source, played_at, user_listens)
    VALUES (1, 'Old Song', 'Old Artist', 'Old Album', 'navidrome', '2025-01-02 00:00:00', 1);
INSERT INTO listens (id, track_name, artist_name, album_name, source, played_at, user_listens)
    VALUES (2, 'LB Song', 'LB Artist', 'LB Album', 'listenbrainz', '2025-01-03 00:00:00', 1);`

// TestNewClient_ListenSubmissionColumnsUpgradeWithExistingRows boots the new
// binary against a seeded pre-submission database and verifies auto-migration
// adds the new columns without data loss:
//   - submitted_to_listenbrainz_at is nullable, so Ent's ADD COLUMN succeeds
//     on a listens table that already has rows (NULL = never submitted, which
//     is exactly the correct semantic for pre-existing listens);
//   - submit_listens uses a constant Default(false), so Ent emits
//     ADD COLUMN ... NOT NULL DEFAULT false, which SQLite accepts on a
//     non-empty table — existing connections stay opted OUT.
func TestNewClient_ListenSubmissionColumnsUpgradeWithExistingRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-lb.db")
	source := "file:" + path + "?_fk=1"

	// Seed WITHOUT foreign-key enforcement: the legacy listens table declares
	// foreign keys against albums/artists/tracks, which on a real old database
	// existed but here are created later by Ent's migration. The seeded rows
	// hold NULL in those columns, so enforcement is irrelevant to the data.
	db, err := sql.Open("sqlite3", "file:"+path)
	if err != nil {
		t.Fatalf("failed to open sqlite db: %v", err)
	}
	if _, err := db.Exec(legacyListenBrainzDDL); err != nil {
		_ = db.Close()
		t.Fatalf("failed to create legacy schema: %v", err)
	}
	if _, err := db.Exec(legacyListenBrainzSeed); err != nil {
		_ = db.Close()
		t.Fatalf("failed to seed legacy data: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("failed to close seed connection: %v", err)
	}

	ctx := context.Background()
	client, err := NewClient(ctx, "sqlite3", source, nil, nil)
	if err != nil {
		t.Fatalf("NewClient failed migrating a pre-submission database with existing rows: %v", err)
	}
	defer func() { _ = client.Close() }()

	// Existing rows survive with the correct upgrade defaults.
	db, err = sql.Open("sqlite3", source)
	if err != nil {
		t.Fatalf("failed to reopen sqlite db: %v", err)
	}
	defer func() { _ = db.Close() }()

	assertIntQuery(t, db, `SELECT COUNT(*) FROM listens`, 2)
	assertIntQuery(t, db, `SELECT COUNT(*) FROM listens WHERE submitted_to_listenbrainz_at IS NULL`, 2)
	assertIntQuery(t, db, `SELECT COUNT(*) FROM listen_brainz_auths`, 1)
	// Governing: REQ-PROV-054 — existing connections default to opted OUT.
	assertIntQuery(t, db, `SELECT COUNT(*) FROM listen_brainz_auths WHERE submit_listens = false`, 1)

	// The migrated schema must be usable through the generated client.
	unsubmitted, err := client.Listen.Query().
		Where(entlisten.SubmittedToListenbrainzAtIsNil()).
		Count(ctx)
	if err != nil {
		t.Fatalf("failed to query migrated listens via ent: %v", err)
	}
	if unsubmitted != 2 {
		t.Fatalf("expected 2 unsubmitted listens after upgrade, got %d", unsubmitted)
	}

	auth, err := client.ListenBrainzAuth.Get(ctx, 1)
	if err != nil {
		t.Fatalf("failed to read migrated listen_brainz_auths row via ent: %v", err)
	}
	if auth.SubmitListens {
		t.Fatal("submit_listens must default to false for pre-existing connections (opt-in)")
	}
	if auth.Username != "alice-lb" {
		t.Fatalf("existing auth data corrupted by migration: %+v", auth)
	}

	// Opting in via the generated client must work on the migrated schema.
	if err := client.ListenBrainzAuth.UpdateOneID(1).SetSubmitListens(true).Exec(ctx); err != nil {
		t.Fatalf("failed to opt in on migrated schema: %v", err)
	}
	assertIntQuery(t, db, `SELECT COUNT(*) FROM listen_brainz_auths WHERE submit_listens = true`, 1)
}
