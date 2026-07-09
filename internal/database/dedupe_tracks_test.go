// Governing: SPEC metadata-enrichment-pipeline (catalog uniqueness)
package database

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// setupLegacyTrackSchema creates a minimal pre-unique-index schema containing
// only the tables and columns DedupeTracks touches, mirroring a database
// created by an older release (before the unique (name, artist_tracks) index).
func setupLegacyTrackSchema(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", "file:"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("failed to open sqlite db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ddl := `
CREATE TABLE tracks (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL,
    created_at DATETIME NOT NULL,
    artist_tracks INTEGER
);
CREATE TABLE listens (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    track_listens INTEGER
);
CREATE TABLE playlist_tracks (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    track_playlist_tracks INTEGER
);
CREATE TABLE tag_tracks (
    tag_id INTEGER NOT NULL,
    track_id INTEGER NOT NULL
);
CREATE TABLE entity_tags (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    entity_type TEXT NOT NULL,
    entity_id INTEGER NOT NULL
);`
	if _, err := db.Exec(ddl); err != nil {
		t.Fatalf("failed to create legacy schema: %v", err)
	}
	return db
}

func TestDedupeTracks_MergesDuplicates(t *testing.T) {
	db := setupLegacyTrackSchema(t)
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	seed := `
INSERT INTO tracks (id, name, created_at, artist_tracks) VALUES
    (1, 'Song A', '2020-01-01 00:00:00', 10),  -- keeper (oldest)
    (2, 'Song A', '2021-01-01 00:00:00', 10),  -- duplicate
    (3, 'Song A', '2022-01-01 00:00:00', 10),  -- duplicate
    (4, 'Song A', '2020-06-01 00:00:00', 20),  -- same name, different artist
    (5, 'Song B', '2020-01-01 00:00:00', 10),  -- different name
    (6, 'Orphan',  '2020-01-01 00:00:00', NULL); -- no artist edge
INSERT INTO listens (id, track_listens) VALUES (1, 2), (2, 3), (3, 1), (4, 4);
INSERT INTO playlist_tracks (id, track_playlist_tracks) VALUES (1, 3), (2, 5);
INSERT INTO tag_tracks (tag_id, track_id) VALUES (100, 2), (100, 1);
INSERT INTO entity_tags (entity_type, entity_id) VALUES ('track', 3), ('track', 1), ('album', 2);`
	if _, err := db.Exec(seed); err != nil {
		t.Fatalf("failed to seed data: %v", err)
	}

	removed, err := DedupeTracks(ctx, "sqlite3", db, logger)
	if err != nil {
		t.Fatalf("DedupeTracks returned error: %v", err)
	}
	if removed != 2 {
		t.Fatalf("expected 2 duplicates removed, got %d", removed)
	}

	// The keeper and non-duplicates survive; duplicates 2 and 3 are gone.
	assertIntQuery(t, db, `SELECT COUNT(*) FROM tracks`, 4)
	assertIntQuery(t, db, `SELECT COUNT(*) FROM tracks WHERE id IN (2, 3)`, 0)
	assertIntQuery(t, db, `SELECT COUNT(*) FROM tracks WHERE id = 1`, 1)

	// Listens re-pointed to the keeper; unrelated listen untouched.
	assertIntQuery(t, db, `SELECT COUNT(*) FROM listens WHERE track_listens = 1`, 3)
	assertIntQuery(t, db, `SELECT COUNT(*) FROM listens WHERE track_listens = 4`, 1)

	// Playlist tracks re-pointed to the keeper; unrelated row untouched.
	assertIntQuery(t, db, `SELECT COUNT(*) FROM playlist_tracks WHERE track_playlist_tracks = 1`, 1)
	assertIntQuery(t, db, `SELECT COUNT(*) FROM playlist_tracks WHERE track_playlist_tracks = 5`, 1)

	// Tag links of the duplicates deleted; keeper's own links preserved.
	assertIntQuery(t, db, `SELECT COUNT(*) FROM tag_tracks WHERE track_id = 2`, 0)
	assertIntQuery(t, db, `SELECT COUNT(*) FROM tag_tracks WHERE track_id = 1`, 1)
	assertIntQuery(t, db, `SELECT COUNT(*) FROM entity_tags WHERE entity_type = 'track' AND entity_id = 3`, 0)
	assertIntQuery(t, db, `SELECT COUNT(*) FROM entity_tags WHERE entity_type = 'track' AND entity_id = 1`, 1)
	assertIntQuery(t, db, `SELECT COUNT(*) FROM entity_tags WHERE entity_type = 'album'`, 1)
}

func TestDedupeTracks_NoDuplicates(t *testing.T) {
	db := setupLegacyTrackSchema(t)
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	if _, err := db.Exec(`INSERT INTO tracks (id, name, created_at, artist_tracks) VALUES
        (1, 'Song A', '2020-01-01 00:00:00', 10),
        (2, 'Song B', '2020-01-01 00:00:00', 10)`); err != nil {
		t.Fatalf("failed to seed data: %v", err)
	}

	removed, err := DedupeTracks(ctx, "sqlite3", db, logger)
	if err != nil {
		t.Fatalf("DedupeTracks returned error: %v", err)
	}
	if removed != 0 {
		t.Fatalf("expected 0 duplicates removed, got %d", removed)
	}
	assertIntQuery(t, db, `SELECT COUNT(*) FROM tracks`, 2)
}

func TestDedupeTracks_FreshDatabase(t *testing.T) {
	// A brand-new database has no tracks table yet; DedupeTracks must be a no-op.
	db, err := sql.Open("sqlite3", "file:"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("failed to open sqlite db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	removed, err := DedupeTracks(context.Background(), "sqlite3", db, logger)
	if err != nil {
		t.Fatalf("DedupeTracks on fresh database returned error: %v", err)
	}
	if removed != 0 {
		t.Fatalf("expected 0 removed on fresh database, got %d", removed)
	}
}

func assertIntQuery(t *testing.T, db *sql.DB, query string, want int) {
	t.Helper()
	var got int
	if err := db.QueryRow(query).Scan(&got); err != nil {
		t.Fatalf("query %q failed: %v", query, err)
	}
	if got != want {
		t.Fatalf("query %q = %d, want %d", query, got, want)
	}
}
