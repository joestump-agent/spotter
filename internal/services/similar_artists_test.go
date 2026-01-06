package services_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"spotter/ent"
	"spotter/ent/similarartist"
	"spotter/internal/config"
	"spotter/internal/events"
	"spotter/internal/services"

	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupSimilarArtistsTestDB(t *testing.T) *ent.Client {
	client, err := ent.Open("sqlite3", "file:ent?mode=memory&cache=shared&_fk=1")
	require.NoError(t, err)
	require.NoError(t, client.Schema.Create(context.Background()))
	t.Cleanup(func() {
		client.Close()
	})
	return client
}

func similarArtistsCreateTestUser(t *testing.T, client *ent.Client, username string) *ent.User {
	u, err := client.User.Create().
		SetUsername(username).
		Save(context.Background())
	require.NoError(t, err)
	return u
}

func similarArtistsCreateTestArtist(t *testing.T, client *ent.Client, user *ent.User, name string, genres []string) *ent.Artist {
	a, err := client.Artist.Create().
		SetName(name).
		SetGenres(genres).
		SetUser(user).
		Save(context.Background())
	require.NoError(t, err)
	return a
}

func TestNewSimilarArtistsService(t *testing.T) {
	client := setupSimilarArtistsTestDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{}
	bus := events.NewBus()

	svc := services.NewSimilarArtistsService(client, cfg, logger, bus)
	assert.NotNil(t, svc)
}

func TestNewSimilarArtistsService_NilLogger(t *testing.T) {
	client := setupSimilarArtistsTestDB(t)
	cfg := &config.Config{}
	bus := events.NewBus()

	// Should not panic with nil logger
	svc := services.NewSimilarArtistsService(client, cfg, nil, bus)
	assert.NotNil(t, svc)
}

func TestGetSimilarArtists_Empty(t *testing.T) {
	client := setupSimilarArtistsTestDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{}
	bus := events.NewBus()
	svc := services.NewSimilarArtistsService(client, cfg, logger, bus)

	user := similarArtistsCreateTestUser(t, client, "testuser")
	artist := similarArtistsCreateTestArtist(t, client, user, "Test Artist", []string{"Rock"})

	similar, err := svc.GetSimilarArtists(context.Background(), user.ID, artist.ID)
	require.NoError(t, err)
	assert.Empty(t, similar)
}

func TestGetSimilarArtists_WithData(t *testing.T) {
	client := setupSimilarArtistsTestDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{}
	bus := events.NewBus()
	svc := services.NewSimilarArtistsService(client, cfg, logger, bus)

	user := similarArtistsCreateTestUser(t, client, "testuser")
	sourceArtist := similarArtistsCreateTestArtist(t, client, user, "Source Artist", []string{"Rock"})
	similarArtist1 := similarArtistsCreateTestArtist(t, client, user, "Similar Artist 1", []string{"Rock", "Alternative"})
	similarArtist2 := similarArtistsCreateTestArtist(t, client, user, "Similar Artist 2", []string{"Rock", "Grunge"})

	// Create similar artist relationships
	_, err := client.SimilarArtist.Create().
		SetSourceArtist(sourceArtist).
		SetSimilarArtist(similarArtist1).
		SetUser(user).
		SetProvider(services.ProviderOpenAI).
		SetConfidence(0.95).
		SetRank(1).
		SetReason("Both are rock artists").
		Save(context.Background())
	require.NoError(t, err)

	_, err = client.SimilarArtist.Create().
		SetSourceArtist(sourceArtist).
		SetSimilarArtist(similarArtist2).
		SetUser(user).
		SetProvider(services.ProviderOpenAI).
		SetConfidence(0.85).
		SetRank(2).
		SetReason("Similar guitar-driven sound").
		Save(context.Background())
	require.NoError(t, err)

	similar, err := svc.GetSimilarArtists(context.Background(), user.ID, sourceArtist.ID)
	require.NoError(t, err)
	require.Len(t, similar, 2)

	// Should be ordered by rank
	assert.Equal(t, 1, similar[0].Rank)
	assert.Equal(t, 2, similar[1].Rank)
	assert.Equal(t, "Similar Artist 1", similar[0].Edges.SimilarArtist.Name)
	assert.Equal(t, "Similar Artist 2", similar[1].Edges.SimilarArtist.Name)
	assert.Equal(t, 0.95, similar[0].Confidence)
	assert.Equal(t, 0.85, similar[1].Confidence)
}

func TestGetSimilarArtists_UserIsolation(t *testing.T) {
	client := setupSimilarArtistsTestDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{}
	bus := events.NewBus()
	svc := services.NewSimilarArtistsService(client, cfg, logger, bus)

	user1 := similarArtistsCreateTestUser(t, client, "user1")
	user2 := similarArtistsCreateTestUser(t, client, "user2")

	// Create artists for both users
	sourceArtist1 := similarArtistsCreateTestArtist(t, client, user1, "Source Artist", []string{"Rock"})
	similarArtist1 := similarArtistsCreateTestArtist(t, client, user1, "Similar Artist", []string{"Rock"})

	sourceArtist2 := similarArtistsCreateTestArtist(t, client, user2, "Source Artist", []string{"Rock"})
	similarArtist2 := similarArtistsCreateTestArtist(t, client, user2, "Similar Artist", []string{"Rock"})

	// Create similar artist relationships for both users
	_, err := client.SimilarArtist.Create().
		SetSourceArtist(sourceArtist1).
		SetSimilarArtist(similarArtist1).
		SetUser(user1).
		SetProvider(services.ProviderOpenAI).
		SetConfidence(0.90).
		SetRank(1).
		Save(context.Background())
	require.NoError(t, err)

	_, err = client.SimilarArtist.Create().
		SetSourceArtist(sourceArtist2).
		SetSimilarArtist(similarArtist2).
		SetUser(user2).
		SetProvider(services.ProviderOpenAI).
		SetConfidence(0.80).
		SetRank(1).
		Save(context.Background())
	require.NoError(t, err)

	// User 1 should only see their own similar artists
	similar1, err := svc.GetSimilarArtists(context.Background(), user1.ID, sourceArtist1.ID)
	require.NoError(t, err)
	require.Len(t, similar1, 1)
	assert.Equal(t, 0.90, similar1[0].Confidence)

	// User 2 should only see their own similar artists
	similar2, err := svc.GetSimilarArtists(context.Background(), user2.ID, sourceArtist2.ID)
	require.NoError(t, err)
	require.Len(t, similar2, 1)
	assert.Equal(t, 0.80, similar2[0].Confidence)
}

func TestClearSimilarArtists(t *testing.T) {
	client := setupSimilarArtistsTestDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{}
	bus := events.NewBus()
	svc := services.NewSimilarArtistsService(client, cfg, logger, bus)

	user := similarArtistsCreateTestUser(t, client, "testuser")
	sourceArtist := similarArtistsCreateTestArtist(t, client, user, "Source Artist", []string{"Rock"})
	similarArtist := similarArtistsCreateTestArtist(t, client, user, "Similar Artist", []string{"Rock"})

	// Create similar artist relationship
	_, err := client.SimilarArtist.Create().
		SetSourceArtist(sourceArtist).
		SetSimilarArtist(similarArtist).
		SetUser(user).
		SetProvider(services.ProviderOpenAI).
		SetConfidence(0.90).
		SetRank(1).
		Save(context.Background())
	require.NoError(t, err)

	// Verify relationship exists
	similar, err := svc.GetSimilarArtists(context.Background(), user.ID, sourceArtist.ID)
	require.NoError(t, err)
	assert.Len(t, similar, 1)

	// Clear similar artists
	err = svc.ClearSimilarArtists(context.Background(), user.ID, sourceArtist.ID)
	require.NoError(t, err)

	// Verify relationship is removed
	similar, err = svc.GetSimilarArtists(context.Background(), user.ID, sourceArtist.ID)
	require.NoError(t, err)
	assert.Empty(t, similar)
}

func TestClearSimilarArtists_OnlyAffectsTargetArtist(t *testing.T) {
	client := setupSimilarArtistsTestDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{}
	bus := events.NewBus()
	svc := services.NewSimilarArtistsService(client, cfg, logger, bus)

	user := similarArtistsCreateTestUser(t, client, "testuser")
	sourceArtist1 := similarArtistsCreateTestArtist(t, client, user, "Source Artist 1", []string{"Rock"})
	sourceArtist2 := similarArtistsCreateTestArtist(t, client, user, "Source Artist 2", []string{"Pop"})
	similarArtist := similarArtistsCreateTestArtist(t, client, user, "Similar Artist", []string{"Rock", "Pop"})

	// Create similar artist relationships for both source artists
	_, err := client.SimilarArtist.Create().
		SetSourceArtist(sourceArtist1).
		SetSimilarArtist(similarArtist).
		SetUser(user).
		SetProvider(services.ProviderOpenAI).
		SetConfidence(0.90).
		SetRank(1).
		Save(context.Background())
	require.NoError(t, err)

	_, err = client.SimilarArtist.Create().
		SetSourceArtist(sourceArtist2).
		SetSimilarArtist(similarArtist).
		SetUser(user).
		SetProvider(services.ProviderOpenAI).
		SetConfidence(0.85).
		SetRank(1).
		Save(context.Background())
	require.NoError(t, err)

	// Clear similar artists for source artist 1 only
	err = svc.ClearSimilarArtists(context.Background(), user.ID, sourceArtist1.ID)
	require.NoError(t, err)

	// Verify source artist 1 relationships are removed
	similar1, err := svc.GetSimilarArtists(context.Background(), user.ID, sourceArtist1.ID)
	require.NoError(t, err)
	assert.Empty(t, similar1)

	// Verify source artist 2 relationships are still there
	similar2, err := svc.GetSimilarArtists(context.Background(), user.ID, sourceArtist2.ID)
	require.NoError(t, err)
	assert.Len(t, similar2, 1)
}

func TestSimilarArtist_MultipleProviders(t *testing.T) {
	client := setupSimilarArtistsTestDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{}
	bus := events.NewBus()
	svc := services.NewSimilarArtistsService(client, cfg, logger, bus)

	user := similarArtistsCreateTestUser(t, client, "testuser")
	sourceArtist := similarArtistsCreateTestArtist(t, client, user, "Source Artist", []string{"Rock"})
	similarArtist := similarArtistsCreateTestArtist(t, client, user, "Similar Artist", []string{"Rock"})

	// Create similar artist relationships with different providers
	_, err := client.SimilarArtist.Create().
		SetSourceArtist(sourceArtist).
		SetSimilarArtist(similarArtist).
		SetUser(user).
		SetProvider(services.ProviderOpenAI).
		SetConfidence(0.95).
		SetRank(1).
		Save(context.Background())
	require.NoError(t, err)

	_, err = client.SimilarArtist.Create().
		SetSourceArtist(sourceArtist).
		SetSimilarArtist(similarArtist).
		SetUser(user).
		SetProvider(services.ProviderLastFM).
		SetConfidence(0.80).
		SetRank(2).
		Save(context.Background())
	require.NoError(t, err)

	// Both relationships should be returned
	similar, err := svc.GetSimilarArtists(context.Background(), user.ID, sourceArtist.ID)
	require.NoError(t, err)
	assert.Len(t, similar, 2)

	// Verify different providers
	providers := make(map[string]bool)
	for _, s := range similar {
		providers[s.Provider] = true
	}
	assert.True(t, providers[services.ProviderOpenAI])
	assert.True(t, providers[services.ProviderLastFM])
}

func TestFindSimilarArtists_NoAPIKey(t *testing.T) {
	client := setupSimilarArtistsTestDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{}
	// No API key configured
	bus := events.NewBus()
	svc := services.NewSimilarArtistsService(client, cfg, logger, bus)

	user := similarArtistsCreateTestUser(t, client, "testuser")
	// Need at least 2 artists for the function to try calling the API
	artist := similarArtistsCreateTestArtist(t, client, user, "Test Artist", []string{"Rock"})
	_ = similarArtistsCreateTestArtist(t, client, user, "Another Artist", []string{"Rock"})

	err := svc.FindSimilarArtists(context.Background(), user.ID, artist.ID)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "OpenAI API key not configured")
}

func TestFindSimilarArtists_ArtistNotFound(t *testing.T) {
	client := setupSimilarArtistsTestDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{}
	// No API key - we should fail before reaching the API call
	bus := events.NewBus()
	svc := services.NewSimilarArtistsService(client, cfg, logger, bus)

	user := similarArtistsCreateTestUser(t, client, "testuser")

	err := svc.FindSimilarArtists(context.Background(), user.ID, 99999)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to get target artist")
}

func TestFindSimilarArtists_NoOtherArtistsInLibrary(t *testing.T) {
	client := setupSimilarArtistsTestDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{}
	// No API key needed - should return early when no other artists exist
	bus := events.NewBus()
	svc := services.NewSimilarArtistsService(client, cfg, logger, bus)

	user := similarArtistsCreateTestUser(t, client, "testuser")
	artist := similarArtistsCreateTestArtist(t, client, user, "Only Artist", []string{"Rock"})

	// Should succeed but find no similar artists (returns early)
	err := svc.FindSimilarArtists(context.Background(), user.ID, artist.ID)
	assert.NoError(t, err)

	similar, err := svc.GetSimilarArtists(context.Background(), user.ID, artist.ID)
	require.NoError(t, err)
	assert.Empty(t, similar)
}

func TestSimilarArtist_TimestampFields(t *testing.T) {
	client := setupSimilarArtistsTestDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{}
	bus := events.NewBus()
	_ = services.NewSimilarArtistsService(client, cfg, logger, bus)

	user := similarArtistsCreateTestUser(t, client, "testuser")
	sourceArtist := similarArtistsCreateTestArtist(t, client, user, "Source Artist", []string{"Rock"})
	similarArtist := similarArtistsCreateTestArtist(t, client, user, "Similar Artist", []string{"Rock"})

	beforeCreate := time.Now().Add(-time.Second)

	sa, err := client.SimilarArtist.Create().
		SetSourceArtist(sourceArtist).
		SetSimilarArtist(similarArtist).
		SetUser(user).
		SetProvider(services.ProviderOpenAI).
		SetConfidence(0.90).
		SetRank(1).
		Save(context.Background())
	require.NoError(t, err)

	afterCreate := time.Now().Add(time.Second)

	assert.True(t, sa.CreatedAt.After(beforeCreate))
	assert.True(t, sa.CreatedAt.Before(afterCreate))
	assert.True(t, sa.UpdatedAt.After(beforeCreate))
	assert.True(t, sa.UpdatedAt.Before(afterCreate))
}

func TestSimilarArtist_UniqueConstraint(t *testing.T) {
	client := setupSimilarArtistsTestDB(t)
	user := similarArtistsCreateTestUser(t, client, "testuser")
	sourceArtist := similarArtistsCreateTestArtist(t, client, user, "Source Artist", []string{"Rock"})
	similarArtist := similarArtistsCreateTestArtist(t, client, user, "Similar Artist", []string{"Rock"})

	// Create first relationship
	_, err := client.SimilarArtist.Create().
		SetSourceArtist(sourceArtist).
		SetSimilarArtist(similarArtist).
		SetUser(user).
		SetProvider(services.ProviderOpenAI).
		SetConfidence(0.90).
		SetRank(1).
		Save(context.Background())
	require.NoError(t, err)

	// Try to create duplicate relationship with same provider - should fail
	_, err = client.SimilarArtist.Create().
		SetSourceArtist(sourceArtist).
		SetSimilarArtist(similarArtist).
		SetUser(user).
		SetProvider(services.ProviderOpenAI).
		SetConfidence(0.85).
		SetRank(2).
		Save(context.Background())
	assert.Error(t, err) // Should fail due to unique constraint
}

func TestSimilarArtist_ConfidenceBounds(t *testing.T) {
	client := setupSimilarArtistsTestDB(t)
	user := similarArtistsCreateTestUser(t, client, "testuser")
	sourceArtist := similarArtistsCreateTestArtist(t, client, user, "Source Artist", []string{"Rock"})
	similarArtist := similarArtistsCreateTestArtist(t, client, user, "Similar Artist", []string{"Rock"})

	// Test valid confidence values
	validConfidences := []float64{0.0, 0.5, 1.0}
	for _, conf := range validConfidences {
		// Delete any existing entry first
		client.SimilarArtist.Delete().
			Where(
				similarartist.SourceArtistID(sourceArtist.ID),
				similarartist.SimilarArtistID(similarArtist.ID),
			).
			Exec(context.Background())

		sa, err := client.SimilarArtist.Create().
			SetSourceArtist(sourceArtist).
			SetSimilarArtist(similarArtist).
			SetUser(user).
			SetProvider(services.ProviderOpenAI).
			SetConfidence(conf).
			SetRank(1).
			Save(context.Background())
		require.NoError(t, err, "Should accept confidence %f", conf)
		assert.Equal(t, conf, sa.Confidence)
	}
}

func TestProviderConstants(t *testing.T) {
	assert.Equal(t, "OpenAI", services.ProviderOpenAI)
	assert.Equal(t, "LastFM", services.ProviderLastFM)
}
