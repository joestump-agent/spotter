// Governing: SPEC listen-playlist-sync REQ-SYNC-020, REQ-SYNC-021, REQ-SYNC-031, REQ-SYNC-032
package services_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"spotter/ent"
	"spotter/ent/enttest"
	"spotter/ent/listen"
	"spotter/ent/playlist"
	"spotter/ent/playlisttrack"
	"spotter/internal/config"
	"spotter/internal/events"
	"spotter/internal/providers"
	"spotter/internal/services"

	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sinceCapturingProvider records the since timestamp passed to GetRecentListens.
type sinceCapturingProvider struct {
	providerType  providers.Type
	receivedSince time.Time
}

func (m *sinceCapturingProvider) Type() providers.Type {
	return m.providerType
}

func (m *sinceCapturingProvider) GetRecentListens(ctx context.Context, since time.Time, callback func([]providers.Track) error) error {
	m.receivedSince = since
	return nil
}

// Governing: SPEC listen-playlist-sync REQ-SYNC-020 (bounded lookback when no history exists)
func TestSyncHistory_DefaultLookback_NoListens(t *testing.T) {
	client, syncer, _ := setupTestSyncer(t)
	ctx := context.Background()
	user := createTestUser(t, client)

	mock := &sinceCapturingProvider{providerType: providers.TypeSpotify}
	syncer.Register(mockFactory(mock))

	before := time.Now()
	require.NoError(t, syncer.SyncRecentListens(ctx, user))
	after := time.Now()

	// Default lookback is 720h (30 days), not the beginning of time.
	expectedEarliest := before.Add(-720 * time.Hour)
	expectedLatest := after.Add(-720 * time.Hour)
	assert.False(t, mock.receivedSince.Before(expectedEarliest), "since should not predate now-720h")
	assert.False(t, mock.receivedSince.After(expectedLatest), "since should not postdate now-720h")
}

// Governing: SPEC listen-playlist-sync REQ-SYNC-020 (sync.history_lookback is configurable)
func TestSyncHistory_ConfiguredLookback_NoListens(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { client.Close() })

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{}
	cfg.Sync.HistoryLookback = "24h"
	syncer := services.NewSyncer(client, cfg, logger, events.NewBus(), nil)

	ctx := context.Background()
	user := createTestUser(t, client)

	mock := &sinceCapturingProvider{providerType: providers.TypeSpotify}
	syncer.Register(mockFactory(mock))

	before := time.Now()
	require.NoError(t, syncer.SyncRecentListens(ctx, user))
	after := time.Now()

	assert.False(t, mock.receivedSince.Before(before.Add(-24*time.Hour)), "since should not predate now-24h")
	assert.False(t, mock.receivedSince.After(after.Add(-24*time.Hour)), "since should not postdate now-24h")
}

// Governing: SPEC listen-playlist-sync REQ-SYNC-021 (dedup by provider+provider_track_id+played_at)
func TestPersistListens_ProviderTrackIDDedup(t *testing.T) {
	client, syncer, _ := setupTestSyncer(t)
	ctx := context.Background()
	user := createTestUser(t, client)

	playedAt := time.Now().Add(-time.Hour).Truncate(time.Second)

	// First sync stores the listen with the provider track ID.
	first := &mockProvider{
		providerType: providers.TypeSpotify,
		tracks: []providers.Track{
			{ID: "sp-track-1", Name: "Original Title", Artist: "Some Artist", Album: "Some Album", PlayedAt: playedAt},
		},
	}
	syncer.Register(mockFactory(first))
	require.NoError(t, syncer.SyncRecentListens(ctx, user))

	listens, err := client.Listen.Query().All(ctx)
	require.NoError(t, err)
	require.Len(t, listens, 1)
	assert.Equal(t, "sp-track-1", listens[0].ProviderTrackID)

	// Second sync reports the same play with a different display title (e.g. the
	// provider renamed the track). The provider ID + played_at dedup key must
	// still recognize it as the same listen.
	second, secondSyncer := newTestSyncerForClient(t, client)
	second.tracks = []providers.Track{
		{ID: "sp-track-1", Name: "Original Title (2011 Remaster)", Artist: "Some Artist", Album: "Some Album", PlayedAt: playedAt},
	}
	require.NoError(t, secondSyncer.SyncRecentListens(ctx, user))

	count, err := client.Listen.Query().Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, count, "same provider track ID + played_at must not create a second listen")
}

// Governing: SPEC listen-playlist-sync REQ-SYNC-021 (fallback dedup key when no provider ID)
func TestPersistListens_FallbackDedupWithoutProviderID(t *testing.T) {
	client, syncer, _ := setupTestSyncer(t)
	ctx := context.Background()
	user := createTestUser(t, client)

	playedAt := time.Now().Add(-time.Hour).Truncate(time.Second)
	mock := &mockProvider{
		providerType: providers.TypeLastFM,
		tracks: []providers.Track{
			{Name: "No ID Track", Artist: "Some Artist", Album: "Some Album", PlayedAt: playedAt},
		},
	}
	syncer.Register(mockFactory(mock))

	require.NoError(t, syncer.SyncRecentListens(ctx, user))
	require.NoError(t, syncer.SyncRecentListens(ctx, user))

	count, err := client.Listen.Query().
		Where(listen.TrackName("No ID Track")).
		Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, count, "name/artist/played_at fallback key must dedup listens without provider IDs")
}

// Governing: SPEC listen-playlist-sync REQ-SYNC-031 (same track at two positions occupies two rows)
func TestPersistPlaylistTracks_DuplicateTrackInPlaylist(t *testing.T) {
	client, syncer, _ := setupTestSyncer(t)
	ctx := context.Background()
	user := createTestUser(t, client)

	dupPlaylist := providers.Playlist{
		ID:         "pl-dup",
		Name:       "Dup Playlist",
		TrackCount: 3,
		Tracks: []providers.Track{
			{ID: "t1", Name: "Repeated Song", Artist: "Artist A"},
			{ID: "t2", Name: "Other Song", Artist: "Artist B"},
			{ID: "t1", Name: "Repeated Song", Artist: "Artist A"},
		},
	}
	mock := &mockProvider{
		providerType: providers.TypeSpotify,
		playlists:    []providers.Playlist{dupPlaylist},
	}
	syncer.Register(mockFactory(mock))

	require.NoError(t, syncer.SyncPlaylists(ctx, user))

	rows, err := client.PlaylistTrack.Query().
		Where(playlisttrack.HasPlaylistWith(playlist.RemoteID("pl-dup"))).
		Order(ent.Asc(playlisttrack.FieldPosition)).
		All(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 3, "the same track at two positions must occupy two rows")
	assert.Equal(t, []int{0, 1, 2}, []int{rows[0].Position, rows[1].Position, rows[2].Position})
	assert.Equal(t, "Repeated Song", rows[0].TrackName)
	assert.Equal(t, "Other Song", rows[1].TrackName)
	assert.Equal(t, "Repeated Song", rows[2].TrackName)

	// Re-sync the identical playlist: rows must be reused, not deleted or duplicated.
	require.NoError(t, syncer.SyncPlaylists(ctx, user))

	rows, err = client.PlaylistTrack.Query().
		Where(playlisttrack.HasPlaylistWith(playlist.RemoteID("pl-dup"))).
		Order(ent.Asc(playlisttrack.FieldPosition)).
		All(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 3, "re-syncing an unchanged playlist must keep both duplicate rows")
	assert.Equal(t, []int{0, 1, 2}, []int{rows[0].Position, rows[1].Position, rows[2].Position})
}

// Governing: SPEC listen-playlist-sync REQ-SYNC-032 (playlists no longer returned are deactivated and can reappear)
func TestSyncPlaylists_DeactivatesMissingAndReactivates(t *testing.T) {
	client, syncer, _ := setupTestSyncer(t)
	ctx := context.Background()
	user := createTestUser(t, client)

	plA := providers.Playlist{ID: "pl-a", Name: "Playlist A"}
	plB := providers.Playlist{ID: "pl-b", Name: "Playlist B"}

	mock := &mockProvider{
		providerType: providers.TypeSpotify,
		playlists:    []providers.Playlist{plA, plB},
	}
	syncer.Register(mockFactory(mock))

	// Both playlists present: both active.
	require.NoError(t, syncer.SyncPlaylists(ctx, user))
	active, err := client.Playlist.Query().Where(playlist.IsActive(true)).Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, active)

	// Provider stops returning B: B must be deactivated, A stays active.
	mock.playlists = []providers.Playlist{plA}
	require.NoError(t, syncer.SyncPlaylists(ctx, user))

	b, err := client.Playlist.Query().Where(playlist.RemoteID("pl-b")).Only(ctx)
	require.NoError(t, err)
	assert.False(t, b.IsActive, "playlist no longer returned by the provider must be deactivated")

	a, err := client.Playlist.Query().Where(playlist.RemoteID("pl-a")).Only(ctx)
	require.NoError(t, err)
	assert.True(t, a.IsActive, "playlist still returned by the provider must stay active")

	// B reappears: it must be reactivated.
	mock.playlists = []providers.Playlist{plA, plB}
	require.NoError(t, syncer.SyncPlaylists(ctx, user))

	b, err = client.Playlist.Query().Where(playlist.RemoteID("pl-b")).Only(ctx)
	require.NoError(t, err)
	assert.True(t, b.IsActive, "playlist that reappears at the provider must be reactivated")
}

// newTestSyncerForClient builds a fresh syncer (and mock provider) sharing an existing client.
func newTestSyncerForClient(t *testing.T, client *ent.Client) (*mockProvider, *services.Syncer) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{}
	syncer := services.NewSyncer(client, cfg, logger, events.NewBus(), nil)
	mock := &mockProvider{providerType: providers.TypeSpotify}
	syncer.Register(mockFactory(mock))
	return mock, syncer
}
