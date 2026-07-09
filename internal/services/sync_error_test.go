package services_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"spotter/ent/enttest"
	"spotter/internal/config"
	"spotter/internal/events"
	"spotter/internal/providers"
	"spotter/internal/services"

	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSyncer_GetActiveProviders_Error_ReturnsWithoutTouchingDB(t *testing.T) {
	// When getActiveProviders returns an error (user not found), Sync should return the error
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { client.Close() })

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := events.NewBus()
	syncer := services.NewSyncer(client, &config.Config{}, logger, bus, nil)

	// Create a user, then delete them so getActiveProviders fails on refresh
	user, err := client.User.Create().SetUsername("testuser").Save(context.Background())
	require.NoError(t, err)

	// Register a mock provider that should never be called
	mockProv := &mockProvider{providerType: providers.TypeSpotify}
	syncer.Register(mockFactory(mockProv))

	// Delete the user so the refresh query fails
	require.NoError(t, client.User.DeleteOne(user).Exec(context.Background()))

	err = syncer.Sync(context.Background(), user)
	assert.Error(t, err, "Sync should return error when getActiveProviders fails")
	assert.Contains(t, err.Error(), "failed to refresh user data")

	// Verify no listens or playlists were created
	listens, _ := client.Listen.Query().All(context.Background())
	assert.Empty(t, listens, "no listens should be created on getActiveProviders failure")
}

func TestSyncer_ProviderError_TriggersBackoffRetriable(t *testing.T) {
	// When a provider returns a retriable error, backoff state is recorded
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { client.Close() })

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := events.NewBus()
	syncer := services.NewSyncer(client, &config.Config{}, logger, bus, nil)

	user, err := client.User.Create().SetUsername("testuser").Save(context.Background())
	require.NoError(t, err)
	_, err = client.SpotifyAuth.Create().
		SetUser(user).
		SetAccessToken("test").
		SetRefreshToken("test").
		SetExpiry(time.Now().Add(time.Hour)).
		Save(context.Background())
	require.NoError(t, err)

	// Register mock provider that returns a retriable error (timeout)
	mockProv := &mockProvider{
		providerType: providers.TypeSpotify,
		err:          fmt.Errorf("connection timeout"),
	}
	syncer.Register(mockFactory(mockProv))

	// First sync — should not return error (Sync swallows per-provider errors)
	err = syncer.Sync(context.Background(), user)
	assert.NoError(t, err, "Sync should not surface per-provider errors")

	// Second sync — the provider should be skipped due to backoff
	err = syncer.Sync(context.Background(), user)
	assert.NoError(t, err)
}

func TestSyncer_ProviderError_FatalNotRetried(t *testing.T) {
	// When a provider returns a fatal error, it should be blocked from retry
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { client.Close() })

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := events.NewBus()
	syncer := services.NewSyncer(client, &config.Config{}, logger, bus, nil)

	user, err := client.User.Create().SetUsername("testuser").Save(context.Background())
	require.NoError(t, err)
	_, err = client.SpotifyAuth.Create().
		SetUser(user).
		SetAccessToken("test").
		SetRefreshToken("test").
		SetExpiry(time.Now().Add(time.Hour)).
		Save(context.Background())
	require.NoError(t, err)

	// Register mock provider that returns a fatal error (unauthorized)
	fatalErr := services.NewHTTPStatusError(401, fmt.Errorf("token revoked"))
	mockProv := &mockProvider{
		providerType: providers.TypeSpotify,
		err:          fatalErr,
	}
	syncer.Register(mockFactory(mockProv))

	// First sync — triggers fatal error
	err = syncer.Sync(context.Background(), user)
	assert.NoError(t, err, "Sync should not surface per-provider errors")

	// Second sync — provider should be skipped due to fatal backoff
	err = syncer.Sync(context.Background(), user)
	assert.NoError(t, err)
}

// Governing: SPEC error-handling REQ-STATE-004 (fatal flag cleared only when the
// user takes corrective action, after which the provider syncs again)
func TestSyncer_ClearProviderBackoff_AllowsRetryAfterFatal(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { client.Close() })

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := events.NewBus()
	syncer := services.NewSyncer(client, &config.Config{}, logger, bus, nil)

	user, err := client.User.Create().SetUsername("testuser").Save(context.Background())
	require.NoError(t, err)
	_, err = client.SpotifyAuth.Create().
		SetUser(user).
		SetAccessToken("test").
		SetRefreshToken("test").
		SetExpiry(time.Now().Add(time.Hour)).
		Save(context.Background())
	require.NoError(t, err)

	fatalErr := services.NewHTTPStatusError(401, fmt.Errorf("token revoked"))
	mockProv := &mockProvider{
		providerType: providers.TypeSpotify,
		err:          fatalErr,
	}
	syncer.Register(mockFactory(mockProv))

	// First sync records the fatal error and blocks the provider.
	require.NoError(t, syncer.Sync(context.Background(), user))

	// The provider recovers, but the fatal flag still blocks syncing.
	mockProv.err = nil
	mockProv.tracks = []providers.Track{{
		ID:       "t1",
		Name:     "Song",
		Artist:   "Artist",
		PlayedAt: time.Now().Add(-time.Minute),
	}}
	require.NoError(t, syncer.Sync(context.Background(), user))
	listens, err := client.Listen.Query().All(context.Background())
	require.NoError(t, err)
	assert.Empty(t, listens, "fatal-blocked provider must not sync until the user acts")

	// Simulate the user reconnecting the provider (handler calls ClearProviderBackoff).
	syncer.ClearProviderBackoff(user.ID, providers.TypeSpotify)

	require.NoError(t, syncer.Sync(context.Background(), user))
	listens, err = client.Listen.Query().All(context.Background())
	require.NoError(t, err)
	assert.Len(t, listens, 1, "provider should sync again after fatal state is cleared")
}

func TestSyncer_SyncRecentListens_GetActiveProviders_Error(t *testing.T) {
	// SyncRecentListens should propagate getActiveProviders errors
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { client.Close() })

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := events.NewBus()
	syncer := services.NewSyncer(client, &config.Config{}, logger, bus, nil)

	user, err := client.User.Create().SetUsername("testuser").Save(context.Background())
	require.NoError(t, err)

	// Delete user to force getActiveProviders to fail
	require.NoError(t, client.User.DeleteOne(user).Exec(context.Background()))

	err = syncer.SyncRecentListens(context.Background(), user)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to refresh user data")
}

func TestSyncer_SyncPlaylists_GetActiveProviders_Error(t *testing.T) {
	// SyncPlaylists should propagate getActiveProviders errors
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { client.Close() })

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := events.NewBus()
	syncer := services.NewSyncer(client, &config.Config{}, logger, bus, nil)

	user, err := client.User.Create().SetUsername("testuser").Save(context.Background())
	require.NoError(t, err)

	// Delete user to force getActiveProviders to fail
	require.NoError(t, client.User.DeleteOne(user).Exec(context.Background()))

	err = syncer.SyncPlaylists(context.Background(), user)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to refresh user data")
}

func TestSyncer_HistoryCallbackError_DoesNotPersistPartialData(t *testing.T) {
	// When GetRecentListens returns an error, the provider is skipped
	// and no partial data leaks
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { client.Close() })

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := events.NewBus()
	syncer := services.NewSyncer(client, &config.Config{}, logger, bus, nil)

	user, err := client.User.Create().SetUsername("testuser").Save(context.Background())
	require.NoError(t, err)
	_, err = client.NavidromeAuth.Create().
		SetUser(user).
		SetPassword("testpassword").
		Save(context.Background())
	require.NoError(t, err)

	// Register a provider that returns an error from GetRecentListens
	failProv := &mockProvider{
		providerType: providers.TypeNavidrome,
		err:          fmt.Errorf("connection timeout"),
	}
	syncer.Register(mockFactory(failProv))

	err = syncer.SyncRecentListens(context.Background(), user)
	require.NoError(t, err) // syncHistory swallows per-provider errors

	// Verify no listens were persisted
	listens, err := client.Listen.Query().All(context.Background())
	require.NoError(t, err)
	assert.Empty(t, listens, "no listens should be created when provider fails")
}
