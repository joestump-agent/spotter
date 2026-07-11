package services_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"spotter/ent"
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

	// First sync — the provider failure must surface as an error (issue #326)
	err = syncer.Sync(context.Background(), user)
	assert.Error(t, err, "Sync must surface aggregated per-provider errors")

	// Second sync — the provider is skipped due to backoff; a deliberate
	// backoff skip is throttling, not a new failure, so no error surfaces
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

	// First sync — triggers fatal error, which must surface (issue #326)
	err = syncer.Sync(context.Background(), user)
	assert.Error(t, err, "Sync must surface aggregated per-provider errors")

	// Second sync — provider is skipped due to fatal backoff (throttling, not a new failure)
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

	// First sync records the fatal error, blocks the provider, and surfaces
	// the error (issue #326).
	require.Error(t, syncer.Sync(context.Background(), user))

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
	require.Error(t, err, "syncHistory must surface aggregated per-provider errors (issue #326)")

	// Verify no listens were persisted
	listens, err := client.Listen.Query().All(context.Background())
	require.NoError(t, err)
	assert.Empty(t, listens, "no listens should be created when provider fails")
}

// metricSyncEvent mirrors the metric.sync attribute schema for log-capture assertions.
// Governing: ADR-0019 (structured metrics), SPEC observability REQ "BG-003"
type metricSyncEvent struct {
	Msg             string `json:"msg"`
	Provider        string `json:"provider"`
	ListensSynced   int    `json:"listens_synced"`
	PlaylistsSynced int    `json:"playlists_synced"`
	DurationMs      int64  `json:"duration_ms"`
	Success         bool   `json:"success"`
	Error           string `json:"error"`
}

// captureMetricSyncEvents parses JSON log output and returns metric.sync events keyed by provider.
func captureMetricSyncEvents(t *testing.T, logs *bytes.Buffer) map[string]metricSyncEvent {
	t.Helper()
	events := make(map[string]metricSyncEvent)
	scanner := bufio.NewScanner(logs)
	for scanner.Scan() {
		line := scanner.Bytes()
		var ev metricSyncEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		if ev.Msg == "metric.sync" {
			events[ev.Provider] = ev
		}
	}
	require.NoError(t, scanner.Err())
	return events
}

// TestSyncer_AllProvidersFail_SyncReturnsErrorAndMetricSyncFails is the issue
// joestump/spotter#326 acceptance test: when every provider fails, Sync()
// returns an aggregated error and each provider's metric.sync event logs
// success=false.
// Governing: ADR-0019, ADR-0020, SPEC observability REQ "BG-003"
func TestSyncer_AllProvidersFail_SyncReturnsErrorAndMetricSyncFails(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { client.Close() })

	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	bus := events.NewBus()
	syncer := services.NewSyncer(client, &config.Config{}, logger, bus, nil)

	user, err := client.User.Create().SetUsername("testuser").Save(context.Background())
	require.NoError(t, err)

	failingSpotify := &mockProvider{
		providerType: providers.TypeSpotify,
		err:          fmt.Errorf("connection timeout"),
	}
	failingNavidrome := &mockProvider{
		providerType: providers.TypeNavidrome,
		err:          fmt.Errorf("connection refused"),
	}
	syncer.Register(mockFactory(failingSpotify))
	syncer.Register(mockFactory(failingNavidrome))

	err = syncer.Sync(context.Background(), user)
	require.Error(t, err, "Sync must return an error when every provider fails")
	assert.Contains(t, err.Error(), string(providers.TypeSpotify))
	assert.Contains(t, err.Error(), string(providers.TypeNavidrome))

	metrics := captureMetricSyncEvents(t, &logs)
	require.Len(t, metrics, 2, "one metric.sync event per active provider")
	for provider, ev := range metrics {
		assert.False(t, ev.Success, "metric.sync for %s must log success=false", provider)
		assert.NotEmpty(t, ev.Error, "metric.sync for %s must carry the error", provider)
		assert.Zero(t, ev.ListensSynced, "no listens synced for failing provider %s", provider)
	}
}

// TestSyncer_PartialFailure_SurfacesErrorButPreservesPartialSuccess asserts that
// one provider failing does not prevent other providers from syncing, that the
// aggregated error still surfaces, and that metric.sync attributes each
// provider's own counts and success flag (not aggregate totals).
// Governing: ADR-0019, ADR-0020, SPEC observability REQ "BG-003",
// SPEC listen-playlist-sync REQ-SYNC-011
func TestSyncer_PartialFailure_SurfacesErrorButPreservesPartialSuccess(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { client.Close() })

	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	bus := events.NewBus()
	syncer := services.NewSyncer(client, &config.Config{}, logger, bus, nil)

	user, err := client.User.Create().SetUsername("testuser").Save(context.Background())
	require.NoError(t, err)

	failingSpotify := &mockProvider{
		providerType: providers.TypeSpotify,
		err:          fmt.Errorf("connection timeout"),
	}
	workingNavidrome := &mockProvider{
		providerType: providers.TypeNavidrome,
		tracks: []providers.Track{
			{ID: "t1", Name: "Track One", Artist: "Artist One", Album: "Album One", PlayedAt: time.Now()},
			{ID: "t2", Name: "Track Two", Artist: "Artist Two", Album: "Album Two", PlayedAt: time.Now().Add(-10 * time.Minute)},
		},
	}
	syncer.Register(mockFactory(failingSpotify))
	syncer.Register(mockFactory(workingNavidrome))

	err = syncer.Sync(context.Background(), user)
	require.Error(t, err, "partial failure must still surface an error")
	assert.Contains(t, err.Error(), string(providers.TypeSpotify))
	assert.NotContains(t, err.Error(), string(providers.TypeNavidrome),
		"the healthy provider must not appear in the aggregated error")

	// Partial success preserved: the healthy provider's listens persisted
	listens, err := client.Listen.Query().All(context.Background())
	require.NoError(t, err)
	assert.Len(t, listens, 2, "healthy provider's listens must persist despite the other failing")

	// Per-provider metric attribution (REQ "BG-003")
	metrics := captureMetricSyncEvents(t, &logs)
	require.Len(t, metrics, 2, "one metric.sync event per active provider")

	spotifyEv := metrics[string(providers.TypeSpotify)]
	assert.False(t, spotifyEv.Success, "failing provider must log success=false")
	assert.NotEmpty(t, spotifyEv.Error)
	assert.Zero(t, spotifyEv.ListensSynced)

	navidromeEv := metrics[string(providers.TypeNavidrome)]
	assert.True(t, navidromeEv.Success, "healthy provider must log success=true")
	assert.Empty(t, navidromeEv.Error)
	assert.Equal(t, 2, navidromeEv.ListensSynced, "listens_synced must be the provider's own count")
}

// TestSyncer_SyncProvider_SurfacesProviderError asserts SyncProvider() returns
// the aggregated error for the targeted provider so the manual "Sync Failed"
// toast paths in the preferences handlers are reachable (issue #326).
// Governing: ADR-0020 (error handling and resilience)
func TestSyncer_SyncProvider_SurfacesProviderError(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { client.Close() })

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := events.NewBus()
	syncer := services.NewSyncer(client, &config.Config{}, logger, bus, nil)

	user, err := client.User.Create().SetUsername("testuser").Save(context.Background())
	require.NoError(t, err)

	failing := &mockProvider{
		providerType: providers.TypeSpotify,
		err:          fmt.Errorf("connection timeout"),
	}
	syncer.Register(mockFactory(failing))

	err = syncer.SyncProvider(context.Background(), user, providers.TypeSpotify)
	require.Error(t, err, "SyncProvider must surface the provider's failure")
	assert.Contains(t, err.Error(), string(providers.TypeSpotify))

	// A provider that is not active is a no-op, not an error.
	require.NoError(t, syncer.SyncProvider(context.Background(), user, providers.TypeNavidrome))
}

// failListenCreateHook makes every Listen INSERT fail, simulating a DB write
// outage during history persistence. Existence/dedup checks are queries, not
// mutations, so they still run — the failure surfaces at builder.Save(ctx)
// inside persistListens (the Create().Save() path).
func failListenCreateHook(client *ent.Client) {
	client.Listen.Use(func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
			if m.Op().Is(ent.OpCreate) {
				return nil, fmt.Errorf("simulated listen insert failure")
			}
			return next.Mutate(ctx, m)
		})
	})
}

// failPlaylistCreateHook makes every Playlist INSERT fail, simulating a DB
// write outage during playlist persistence. The upsert's existence check is a
// query (not intercepted) and the row is new, so the failure surfaces at
// Create().Save() inside upsertPlaylistRow.
func failPlaylistCreateHook(client *ent.Client) {
	client.Playlist.Use(func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
			if m.Op().Is(ent.OpCreate) {
				return nil, fmt.Errorf("simulated playlist insert failure")
			}
			return next.Mutate(ctx, m)
		})
	})
}

// TestSyncer_PersistListens_DBFailure_SurfacesErrorAndMetricSyncFails is the
// issue #35 acceptance test for history persistence: when a Listen INSERT fails
// mid-sync, persistListens collects the genuine DB error and returns it (rather
// than swallowing it and returning nil), so Sync() surfaces an aggregated error
// and the provider's metric.sync event logs success=false.
// Governing: ADR-0020, SPEC listen-playlist-sync REQ-SYNC-022, SPEC observability REQ "BG-003"
func TestSyncer_PersistListens_DBFailure_SurfacesErrorAndMetricSyncFails(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { client.Close() })

	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	bus := events.NewBus()
	syncer := services.NewSyncer(client, &config.Config{}, logger, bus, nil)

	user, err := client.User.Create().SetUsername("testuser").Save(context.Background())
	require.NoError(t, err)

	prov := &mockProvider{
		providerType: providers.TypeSpotify,
		tracks: []providers.Track{
			{ID: "t1", Name: "Track One", Artist: "Artist One", Album: "Album One", PlayedAt: time.Now()},
			{ID: "t2", Name: "Track Two", Artist: "Artist Two", Album: "Album Two", PlayedAt: time.Now().Add(-10 * time.Minute)},
		},
	}
	syncer.Register(mockFactory(prov))

	// Install the failing INSERT hook only after setup, so the write outage
	// begins exactly when persistListens tries to save.
	failListenCreateHook(client)

	err = syncer.Sync(context.Background(), user)
	require.Error(t, err, "Sync must return an error when a listen INSERT fails mid-sync")
	assert.Contains(t, err.Error(), string(providers.TypeSpotify),
		"the aggregated error must name the failing provider")

	metrics := captureMetricSyncEvents(t, &logs)
	ev, ok := metrics[string(providers.TypeSpotify)]
	require.True(t, ok, "a metric.sync event must be emitted for the provider")
	assert.False(t, ev.Success, "metric.sync must report success=false when persistence fails")
	assert.NotEmpty(t, ev.Error, "metric.sync must carry the persistence error")
	assert.Zero(t, ev.ListensSynced, "no listens synced when every INSERT fails")

	listens, err := client.Listen.Query().All(context.Background())
	require.NoError(t, err)
	assert.Empty(t, listens, "no listens should persist when every INSERT fails")
}

// TestSyncer_PersistListens_DBFailure_SyncProviderSurfacesError asserts the
// same mid-sync DB write failure is surfaced by the single-provider entry point
// SyncProvider() (issue #35).
// Governing: ADR-0020, SPEC listen-playlist-sync REQ-SYNC-022
func TestSyncer_PersistListens_DBFailure_SyncProviderSurfacesError(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { client.Close() })

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := events.NewBus()
	syncer := services.NewSyncer(client, &config.Config{}, logger, bus, nil)

	user, err := client.User.Create().SetUsername("testuser").Save(context.Background())
	require.NoError(t, err)

	prov := &mockProvider{
		providerType: providers.TypeSpotify,
		tracks: []providers.Track{
			{ID: "t1", Name: "Track One", Artist: "Artist One", PlayedAt: time.Now()},
		},
	}
	syncer.Register(mockFactory(prov))
	failListenCreateHook(client)

	err = syncer.SyncProvider(context.Background(), user, providers.TypeSpotify)
	require.Error(t, err, "SyncProvider must surface a mid-sync listen INSERT failure")
	assert.Contains(t, err.Error(), string(providers.TypeSpotify))

	listens, err := client.Listen.Query().All(context.Background())
	require.NoError(t, err)
	assert.Empty(t, listens, "no listens should persist when the INSERT fails")
}

// TestSyncer_PersistPlaylists_DBFailure_SurfacesErrorAndMetricSyncFails is the
// issue #35 acceptance test for playlist persistence: when a Playlist INSERT
// fails mid-sync, persistPlaylists collects the genuine DB error and returns it,
// so Sync() surfaces an aggregated error and metric.sync logs success=false.
// Governing: ADR-0020, SPEC listen-playlist-sync REQ-SYNC-031, SPEC observability REQ "BG-003"
func TestSyncer_PersistPlaylists_DBFailure_SurfacesErrorAndMetricSyncFails(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { client.Close() })

	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	bus := events.NewBus()
	syncer := services.NewSyncer(client, &config.Config{}, logger, bus, nil)

	user, err := client.User.Create().SetUsername("testuser").Save(context.Background())
	require.NoError(t, err)

	// Spotify source so persistPlaylists skips the Navidrome managed-playlist
	// pre-query and the failure is isolated to the per-playlist create path.
	prov := &mockProvider{
		providerType: providers.TypeSpotify,
		playlists: []providers.Playlist{
			{ID: "pl1", Name: "Playlist One", TrackCount: 3},
			{ID: "pl2", Name: "Playlist Two", TrackCount: 5},
		},
	}
	syncer.Register(mockFactory(prov))
	failPlaylistCreateHook(client)

	err = syncer.Sync(context.Background(), user)
	require.Error(t, err, "Sync must return an error when a playlist INSERT fails mid-sync")
	assert.Contains(t, err.Error(), string(providers.TypeSpotify))

	metrics := captureMetricSyncEvents(t, &logs)
	ev, ok := metrics[string(providers.TypeSpotify)]
	require.True(t, ok, "a metric.sync event must be emitted for the provider")
	assert.False(t, ev.Success, "metric.sync must report success=false when playlist persistence fails")
	assert.NotEmpty(t, ev.Error, "metric.sync must carry the persistence error")
	assert.Zero(t, ev.PlaylistsSynced, "no playlists synced when every INSERT fails")

	playlists, err := client.Playlist.Query().All(context.Background())
	require.NoError(t, err)
	assert.Empty(t, playlists, "no playlists should persist when every INSERT fails")
}

// TestSyncer_PersistListens_ExpectedSkips_DoNotFailSync guards the other half of
// issue #35: EXPECTED skips (validation failures) must NOT be reported as DB
// errors. A batch with one valid and one invalid track still succeeds — the
// valid track persists, the invalid one is skipped, Sync() returns nil, and
// metric.sync reports success=true.
// Governing: SPEC listen-playlist-sync REQ-SYNC-021, REQ-SYNC-022
func TestSyncer_PersistListens_ExpectedSkips_DoNotFailSync(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { client.Close() })

	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	bus := events.NewBus()
	syncer := services.NewSyncer(client, &config.Config{}, logger, bus, nil)

	user, err := client.User.Create().SetUsername("testuser").Save(context.Background())
	require.NoError(t, err)

	prov := &mockProvider{
		providerType: providers.TypeSpotify,
		tracks: []providers.Track{
			{ID: "t1", Name: "Valid Track", Artist: "Artist One", PlayedAt: time.Now()},
			{ID: "t2", Name: "Missing Artist", Artist: "", PlayedAt: time.Now().Add(-time.Minute)},
		},
	}
	syncer.Register(mockFactory(prov))

	// No failing hook: DB writes succeed. Only the missing-artist skip occurs.
	err = syncer.Sync(context.Background(), user)
	require.NoError(t, err, "an expected validation skip must not fail the sync")

	metrics := captureMetricSyncEvents(t, &logs)
	ev := metrics[string(providers.TypeSpotify)]
	assert.True(t, ev.Success, "metric.sync must report success=true when only expected skips occur")
	assert.Empty(t, ev.Error)
	assert.Equal(t, 1, ev.ListensSynced, "only the valid track counts as synced")

	listens, err := client.Listen.Query().All(context.Background())
	require.NoError(t, err)
	assert.Len(t, listens, 1, "only the valid track should persist")
}
