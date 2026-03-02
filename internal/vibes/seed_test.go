package vibes_test

import (
	"context"
	"testing"

	"spotter/ent"
	"spotter/ent/enttest"
	"spotter/internal/vibes"

	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupSeedTestDB(t *testing.T) *ent.Client {
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { client.Close() })
	return client
}

func createSeedTestUser(t *testing.T, client *ent.Client, username string) *ent.User {
	u, err := client.User.Create().
		SetUsername(username).
		Save(context.Background())
	require.NoError(t, err)
	return u
}

func TestSeedDefaultDJs_CreatesExactly4DJs(t *testing.T) {
	client := setupSeedTestDB(t)
	user := createSeedTestUser(t, client, "testuser")

	err := vibes.SeedDefaultDJs(context.Background(), client, user)
	require.NoError(t, err)

	djs, err := client.DJ.Query().All(context.Background())
	require.NoError(t, err)
	assert.Len(t, djs, 4, "SeedDefaultDJs should create exactly 4 DJs")
}

func TestSeedDefaultDJs_AllFieldsPopulated(t *testing.T) {
	client := setupSeedTestDB(t)
	user := createSeedTestUser(t, client, "testuser")

	err := vibes.SeedDefaultDJs(context.Background(), client, user)
	require.NoError(t, err)

	djs, err := client.DJ.Query().All(context.Background())
	require.NoError(t, err)
	require.Len(t, djs, 4)

	for _, dj := range djs {
		assert.NotEmpty(t, dj.GenresInclude, "DJ %q should have non-empty GenresInclude", dj.Name)
		assert.NotEmpty(t, dj.Vibes, "DJ %q should have non-empty Vibes", dj.Name)
		assert.NotEmpty(t, dj.SystemPrompt, "DJ %q should have non-empty SystemPrompt", dj.Name)
	}
}

func TestSeedDefaultDJs_CalledTwice_NoPanic(t *testing.T) {
	client := setupSeedTestDB(t)
	user := createSeedTestUser(t, client, "testuser")

	err := vibes.SeedDefaultDJs(context.Background(), client, user)
	require.NoError(t, err)

	// Second call should return an error (unique constraint on name+user) but not panic
	err = vibes.SeedDefaultDJs(context.Background(), client, user)
	assert.Error(t, err, "second call should fail due to unique constraint")

	// Original 4 DJs should still be intact
	djs, err := client.DJ.Query().All(context.Background())
	require.NoError(t, err)
	assert.Len(t, djs, 4, "original 4 DJs should still exist")
}

func TestSeedDefaultDJs_DBUnavailable_ReturnsWrappedError(t *testing.T) {
	client := setupSeedTestDB(t)
	user := createSeedTestUser(t, client, "testuser")

	// Close the DB to simulate unavailability
	client.Close()

	err := vibes.SeedDefaultDJs(context.Background(), client, user)
	assert.Error(t, err, "should return error when DB is unavailable")
	assert.Contains(t, err.Error(), "seeding DJ", "error should be wrapped with DJ context")
}
