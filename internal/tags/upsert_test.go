// Governing: SPEC-0016 REQ "Denormalized Entity Tags Table", ADR-0023
package tags

import (
	"context"
	"database/sql"
	"testing"

	"spotter/ent"
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

// setupUpsertTest creates an in-memory SQLite ent client + raw sql.DB with the
// entity_tags table, plus a user and artist fixture.
func setupUpsertTest(t *testing.T) (*ent.Client, *sql.DB, *ent.User, *ent.Artist) {
	t.Helper()
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
	return client, db, u, art
}

// entityTagCount counts entity_tags rows for the given entity.
func entityTagCount(t *testing.T, db *sql.DB, entityType string, entityID int) int {
	t.Helper()
	var count int
	require.NoError(t, db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM entity_tags WHERE entity_type = ? AND entity_id = ?`,
		entityType, entityID).Scan(&count))
	return count
}

// TestUpsertTagsForEntity_TrimsDisplayName verifies that the stored display
// name is the trimmed original casing while the normalized key is lowercase.
// Governing: SPEC-0014 REQ "Tag Normalization"
func TestUpsertTagsForEntity_TrimsDisplayName(t *testing.T) {
	client, db, u, art := setupUpsertTest(t)
	ctx := context.Background()

	typed := []TypedTag{{Name: "  Shoegaze  ", Type: "genre"}}
	require.NoError(t, UpsertTagsForEntity(ctx, client, db, u.ID, "artist", art.ID, typed))

	stored, err := client.Tag.Query().Only(ctx)
	require.NoError(t, err)
	assert.Equal(t, "Shoegaze", stored.Name, "display name must be trimmed with original casing")
	assert.Equal(t, "shoegaze", stored.NormalizedName)
}

// TestUpsertTagsForEntity_UnknownEntityTypeRollsBack verifies that a failure
// mid-batch (unknown entity type, hit after the first Tag row is created)
// rolls back the whole transaction: no Tag rows and no entity_tags rows leak.
// Governing: SPEC-0014 REQ "Denormalized Entity Tags Table"
func TestUpsertTagsForEntity_UnknownEntityTypeRollsBack(t *testing.T) {
	client, db, u, art := setupUpsertTest(t)
	ctx := context.Background()

	typed := []TypedTag{{Name: "Shoegaze", Type: "genre"}}
	err := UpsertTagsForEntity(ctx, client, db, u.ID, "bogus", art.ID, typed)
	require.Error(t, err)

	tagCount, err := client.Tag.Query().Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, tagCount, "failed upsert must roll back Tag creation")
	assert.Equal(t, 0, entityTagCount(t, db, "bogus", art.ID), "failed upsert must not leave entity_tags rows")
}

// TestReplaceTagsForEntity_Dissociates verifies the dissociation path: tags
// absent from the replacement set lose both their Ent edge and their
// entity_tags rows, while the Tag entities themselves survive.
// Governing: SPEC-0014 REQ "Denormalized Entity Tags Table" (dissociation path)
func TestReplaceTagsForEntity_Dissociates(t *testing.T) {
	client, db, u, art := setupUpsertTest(t)
	ctx := context.Background()

	initial := []TypedTag{
		{Name: "Shoegaze", Type: "genre"},
		{Name: "4AD", Type: "label"},
	}
	require.NoError(t, UpsertTagsForEntity(ctx, client, db, u.ID, "artist", art.ID, initial))
	require.Equal(t, 2, entityTagCount(t, db, "artist", art.ID))

	// Replace with only one of the two tags.
	replacement := []TypedTag{{Name: "Shoegaze", Type: "genre"}}
	require.NoError(t, ReplaceTagsForEntity(ctx, client, db, u.ID, "artist", art.ID, replacement))

	assert.Equal(t, 1, entityTagCount(t, db, "artist", art.ID), "stale entity_tags rows must be deleted")

	linked, err := art.QueryTagEntities().All(ctx)
	require.NoError(t, err)
	require.Len(t, linked, 1, "stale tag edges must be removed")
	assert.Equal(t, "shoegaze", linked[0].NormalizedName)

	tagCount, err := client.Tag.Query().Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, tagCount, "Tag entities themselves must never be deleted on dissociation")
}

// TestRemoveAllTagsForEntity clears every association atomically.
// Governing: SPEC-0014 REQ "Denormalized Entity Tags Table" (dissociation path)
func TestRemoveAllTagsForEntity(t *testing.T) {
	client, db, u, art := setupUpsertTest(t)
	ctx := context.Background()

	typed := []TypedTag{
		{Name: "Shoegaze", Type: "genre"},
		{Name: "Ethereal", Type: "ai"},
	}
	require.NoError(t, UpsertTagsForEntity(ctx, client, db, u.ID, "artist", art.ID, typed))

	require.NoError(t, RemoveAllTagsForEntity(ctx, client, db, u.ID, "artist", art.ID))

	assert.Equal(t, 0, entityTagCount(t, db, "artist", art.ID))
	linked, err := art.QueryTagEntities().Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, linked)
}
