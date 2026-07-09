// Governing: SPEC-0016 REQ "Denormalized Entity Tags Table", ADR-0023
package tags

import (
	"context"
	"database/sql"
	"testing"

	"spotter/ent/enttest"
	"spotter/internal/database"

	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEntityTagInsertSQL verifies the driver-dispatch for the entity_tags
// conflict-ignoring insert: MySQL gets INSERT IGNORE with ? placeholders
// (it has no ON CONFLICT clause and no $N placeholders), PostgreSQL keeps
// $N with ON CONFLICT DO NOTHING, and SQLite uses ? with ON CONFLICT.
func TestEntityTagInsertSQL(t *testing.T) {
	t.Run("mysql", func(t *testing.T) {
		q := entityTagInsertSQL("mysql")
		assert.Contains(t, q, "INSERT IGNORE INTO entity_tags")
		assert.Contains(t, q, "VALUES (?, ?, ?, ?, ?, ?)")
		assert.NotContains(t, q, "ON CONFLICT")
		assert.NotContains(t, q, "$1")
	})

	t.Run("postgres", func(t *testing.T) {
		q := entityTagInsertSQL("postgres")
		assert.Contains(t, q, "VALUES ($1, $2, $3, $4, $5, $6)")
		assert.Contains(t, q, "ON CONFLICT (tag_id, entity_type, entity_id) DO NOTHING")
		assert.NotContains(t, q, "INSERT IGNORE")
	})

	t.Run("sqlite3", func(t *testing.T) {
		q := entityTagInsertSQL("sqlite3")
		assert.Contains(t, q, "VALUES (?, ?, ?, ?, ?, ?)")
		assert.Contains(t, q, "ON CONFLICT (tag_id, entity_type, entity_id) DO NOTHING")
		assert.NotContains(t, q, "INSERT IGNORE")
	})

	t.Run("unknown driver falls back to sqlite form", func(t *testing.T) {
		assert.Equal(t, entityTagInsertSQL("sqlite3"), entityTagInsertSQL("bogus"))
	})

	// Every variant must insert the same column list in the same order,
	// since upsertEntityTag binds arguments positionally.
	for _, driver := range []string{"mysql", "postgres", "sqlite3"} {
		q := entityTagInsertSQL(driver)
		assert.Contains(t, q, "(user_id, tag_id, tag_type, tag_name, entity_type, entity_id)", "driver %s", driver)
	}
}

// TestUpsertTagsForEntity_Idempotent exercises the full upsert path on
// SQLite: calling twice with the same tags must not duplicate entity_tags
// rows or Tag entities.
func TestUpsertTagsForEntity_Idempotent(t *testing.T) {
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared&_fk=1"
	client := enttest.Open(t, "sqlite3", dsn)
	t.Cleanup(func() { _ = client.Close() })

	db, err := sql.Open("sqlite3", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()
	require.NoError(t, database.CreateEntityTagsTable(ctx, "sqlite3", db))

	u, err := client.User.Create().SetUsername("testuser").Save(ctx)
	require.NoError(t, err)
	art, err := client.Artist.Create().SetName("Artist").SetUser(u).Save(ctx)
	require.NoError(t, err)

	typed := []TypedTag{
		{Name: "Shoegaze", Type: "genre"},
		{Name: "4AD", Type: "label"},
	}

	require.NoError(t, UpsertTagsForEntity(ctx, client, db, u.ID, "artist", art.ID, typed))
	require.NoError(t, UpsertTagsForEntity(ctx, client, db, u.ID, "artist", art.ID, typed))

	var count int
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM entity_tags WHERE entity_type = 'artist' AND entity_id = ?`,
		art.ID).Scan(&count))
	assert.Equal(t, 2, count, "repeat upserts must not duplicate entity_tags rows")

	tagCount, err := client.Tag.Query().Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, tagCount, "repeat upserts must not duplicate Tag entities")
}
