package services_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"fmt"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"

	"spotter/ent"
	"spotter/ent/enttest"
	"spotter/ent/playlist"
	"spotter/ent/syncevent"
	user_ent "spotter/ent/user"
	"spotter/internal/config"
	"spotter/internal/events"
	"spotter/internal/providers"
	"spotter/internal/services"

	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockPlaylistSyncer implements providers.PlaylistSyncer for testing
type mockPlaylistSyncer struct {
	providerType   providers.Type
	syncedPlaylist *providers.SyncPlaylistRequest
	syncedID       string
	syncErr        error
	deletedID      string
	deleteErr      error
	updatedID      string
	updatedTracks  []providers.Track
	updateErr      error

	// Optional synchronization hooks for concurrency tests (issue #48). When
	// syncPlaylistEntered is non-nil it is closed the first time SyncPlaylist
	// (the create path) is entered; when syncPlaylistBlock is non-nil,
	// SyncPlaylist blocks on a receive from it before returning. This lets a
	// test hold a "create" in flight while it drives a concurrent pair.
	syncPlaylistEntered chan struct{}
	syncPlaylistBlock   chan struct{}
	syncPlaylistOnce    sync.Once
}

func (m *mockPlaylistSyncer) Type() providers.Type {
	return m.providerType
}

func (m *mockPlaylistSyncer) SyncPlaylist(ctx context.Context, playlist providers.SyncPlaylistRequest) (string, error) {
	if m.syncPlaylistEntered != nil {
		m.syncPlaylistOnce.Do(func() { close(m.syncPlaylistEntered) })
	}
	if m.syncPlaylistBlock != nil {
		<-m.syncPlaylistBlock
	}
	if m.syncErr != nil {
		return "", m.syncErr
	}
	m.syncedPlaylist = &playlist
	return m.syncedID, nil
}

func (m *mockPlaylistSyncer) DeletePlaylist(ctx context.Context, remotePlaylistID string) error {
	if m.deleteErr != nil {
		return m.deleteErr
	}
	m.deletedID = remotePlaylistID
	return nil
}

func (m *mockPlaylistSyncer) UpdatePlaylistTracks(ctx context.Context, remotePlaylistID string, tracks []providers.Track) error {
	if m.updateErr != nil {
		return m.updateErr
	}
	m.updatedID = remotePlaylistID
	m.updatedTracks = tracks
	return nil
}

// mockPlaylistSyncerFactory creates a factory that returns the mock syncer
func mockPlaylistSyncerFactory(p providers.Provider) providers.Factory {
	return func(ctx context.Context, user *ent.User) (providers.Provider, error) {
		return p, nil
	}
}

func setupPlaylistSyncService(t *testing.T) (*ent.Client, *services.PlaylistSyncService, *events.Bus, *mockPlaylistSyncer) {
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { client.Close() })

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{}
	cfg.PlaylistSync.MinMatchConfidence = 0.8
	cfg.PlaylistSync.DeleteOnUnsync = true

	bus := events.NewBus()

	svc := services.NewPlaylistSyncService(client, cfg, logger, bus)

	// Create mock syncer
	mockSyncer := &mockPlaylistSyncer{
		providerType: providers.TypeNavidrome,
		syncedID:     "nav-playlist-123",
	}

	// Register the mock factory
	svc.Register(mockPlaylistSyncerFactory(mockSyncer))

	return client, svc, bus, mockSyncer
}

func createTestUserWithNavidromeAuth(t *testing.T, client *ent.Client) *ent.User {
	ctx := context.Background()

	// Create user first
	user, err := client.User.Create().
		SetUsername("testuser").
		Save(ctx)
	require.NoError(t, err)

	// Create Navidrome auth with required user edge
	_, err = client.NavidromeAuth.Create().
		SetPassword("testpassword").
		SetUser(user).
		Save(ctx)
	require.NoError(t, err)

	// Reload user with auth edge
	user, err = client.User.Query().
		Where(user_ent.ID(user.ID)).
		WithNavidromeAuth().
		Only(ctx)
	require.NoError(t, err)

	return user
}

var playlistCounter int

func createTestPlaylistForSync(t *testing.T, client *ent.Client, user *ent.User, source string, syncEnabled bool) *ent.Playlist {
	ctx := context.Background()
	playlistCounter++

	pl, err := client.Playlist.Create().
		SetUser(user).
		SetRemoteID(fmt.Sprintf("remote-%d", playlistCounter)).
		SetName("Test Playlist").
		SetDescription("A test playlist").
		SetSource(source).
		SetTrackCount(5).
		SetSyncToNavidrome(syncEnabled).
		Save(ctx)
	require.NoError(t, err)

	return pl
}

func createTestPlaylistTracksForSync(t *testing.T, client *ent.Client, pl *ent.Playlist) {
	ctx := context.Background()

	tracks := []struct {
		name   string
		artist string
		album  string
	}{
		{"Song One", "Artist A", "Album 1"},
		{"Song Two", "Artist B", "Album 2"},
		{"Song Three", "Artist C", "Album 3"},
	}

	for i, track := range tracks {
		_, err := client.PlaylistTrack.Create().
			SetPlaylist(pl).
			SetTrackName(track.name).
			SetArtistName(track.artist).
			SetAlbumName(track.album).
			SetPosition(i + 1).
			Save(ctx)
		require.NoError(t, err)
	}
}

// createNavidromeTracksForMatching creates matching Navidrome tracks in the library
// so the TrackMatcher can find matches for the playlist tracks
func createNavidromeTracksForMatching(t *testing.T, client *ent.Client, user *ent.User) {
	ctx := context.Background()

	tracks := []struct {
		name        string
		artist      string
		navidromeID string
	}{
		{"Song One", "Artist A", "nav-track-1"},
		{"Song Two", "Artist B", "nav-track-2"},
		{"Song Three", "Artist C", "nav-track-3"},
	}

	for _, track := range tracks {
		// Create artist with user
		artist, err := client.Artist.Create().
			SetName(track.artist).
			SetUser(user).
			Save(ctx)
		require.NoError(t, err)

		// Create track with navidrome ID
		_, err = client.Track.Create().
			SetName(track.name).
			SetArtist(artist).
			SetNillableNavidromeID(&track.navidromeID).
			Save(ctx)
		require.NoError(t, err)
	}
}

func TestPlaylistSyncService_SyncPlaylistToNavidrome_NewPlaylist(t *testing.T) {
	client, svc, bus, mockSyncer := setupPlaylistSyncService(t)
	ctx := context.Background()

	// Subscribe to notifications
	user := createTestUserWithNavidromeAuth(t, client)
	notifCh, cleanup := bus.Subscribe(user.ID)
	defer cleanup()

	// Create Navidrome tracks in the library for matching
	createNavidromeTracksForMatching(t, client, user)

	// Create a Spotify playlist with sync enabled
	pl := createTestPlaylistForSync(t, client, user, "spotify", true)
	createTestPlaylistTracksForSync(t, client, pl)

	// Sync the playlist
	err := svc.SyncPlaylistToNavidrome(ctx, pl.ID)
	require.NoError(t, err)

	// Verify the playlist was synced
	assert.NotNil(t, mockSyncer.syncedPlaylist)
	assert.Contains(t, mockSyncer.syncedPlaylist.Name, "Test Playlist")
	assert.Equal(t, "A test playlist", mockSyncer.syncedPlaylist.Description)

	// Verify database was updated
	updatedPl, err := client.Playlist.Get(ctx, pl.ID)
	require.NoError(t, err)
	assert.Equal(t, "nav-playlist-123", updatedPl.NavidromePlaylistID)
	assert.NotNil(t, updatedPl.LastSyncedAt)
	assert.Empty(t, updatedPl.SyncError)

	// Verify notifications were sent (we send "Syncing Playlist" first, then "Playlist Synced")
	receivedSyncedNotification := false
	for i := 0; i < 3; i++ { // Drain up to 3 notifications
		select {
		case event := <-notifCh:
			assert.Equal(t, events.EventTypeNotification, event.Type)
			payload := event.Payload.(events.NotificationPayload)
			if payload.Title == "Playlist Synced" {
				receivedSyncedNotification = true
			}
		case <-time.After(100 * time.Millisecond):
		}
	}
	assert.True(t, receivedSyncedNotification, "Expected 'Playlist Synced' notification")
}

func TestPlaylistSyncService_SyncPlaylistToNavidrome_ExistingPlaylist(t *testing.T) {
	client, svc, _, mockSyncer := setupPlaylistSyncService(t)
	ctx := context.Background()

	user := createTestUserWithNavidromeAuth(t, client)

	// Create a playlist that was already synced
	pl, err := client.Playlist.Create().
		SetUser(user).
		SetRemoteID("remote-456").
		SetName("Existing Playlist").
		SetSource("spotify").
		SetSyncToNavidrome(true).
		SetNavidromePlaylistID("existing-nav-id").
		SetTrackCount(3).
		Save(ctx)
	require.NoError(t, err)

	createTestPlaylistTracksForSync(t, client, pl)

	// Sync the playlist (should update, not create)
	err = svc.SyncPlaylistToNavidrome(ctx, pl.ID)
	require.NoError(t, err)

	// Verify update was called, not create
	assert.Equal(t, "existing-nav-id", mockSyncer.updatedID)
	assert.Nil(t, mockSyncer.syncedPlaylist) // SyncPlaylist should not be called

	// Verify database still has the original ID
	updatedPl, err := client.Playlist.Get(ctx, pl.ID)
	require.NoError(t, err)
	assert.Equal(t, "existing-nav-id", updatedPl.NavidromePlaylistID)
}

func TestPlaylistSyncService_SyncPlaylistToNavidrome_SyncDisabled(t *testing.T) {
	client, svc, _, mockSyncer := setupPlaylistSyncService(t)
	ctx := context.Background()

	user := createTestUserWithNavidromeAuth(t, client)

	// Create a playlist with sync disabled
	pl := createTestPlaylistForSync(t, client, user, "spotify", false)

	// Try to sync
	err := svc.SyncPlaylistToNavidrome(ctx, pl.ID)
	require.NoError(t, err)

	// Verify no sync was attempted
	assert.Nil(t, mockSyncer.syncedPlaylist)
	assert.Empty(t, mockSyncer.updatedID)
}

func TestPlaylistSyncService_SyncPlaylistToNavidrome_NavidromePlaylistError(t *testing.T) {
	client, svc, _, _ := setupPlaylistSyncService(t)
	ctx := context.Background()

	user := createTestUserWithNavidromeAuth(t, client)

	// Create a Navidrome playlist (should not be syncable to Navidrome)
	pl := createTestPlaylistForSync(t, client, user, "navidrome", true)

	// Try to sync - should fail
	err := svc.SyncPlaylistToNavidrome(ctx, pl.ID)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "cannot sync Navidrome playlist")
}

func TestPlaylistSyncService_SyncPlaylistToNavidrome_SyncError(t *testing.T) {
	client, svc, bus, mockSyncer := setupPlaylistSyncService(t)
	ctx := context.Background()

	// Set up mock to return error
	mockSyncer.syncErr = assert.AnError

	user := createTestUserWithNavidromeAuth(t, client)
	notifCh, cleanup := bus.Subscribe(user.ID)
	defer cleanup()

	pl := createTestPlaylistForSync(t, client, user, "spotify", true)
	createTestPlaylistTracksForSync(t, client, pl)

	// Try to sync
	err := svc.SyncPlaylistToNavidrome(ctx, pl.ID)
	assert.Error(t, err)

	// Verify sync_error was saved
	updatedPl, err := client.Playlist.Get(ctx, pl.ID)
	require.NoError(t, err)
	assert.NotEmpty(t, updatedPl.SyncError)

	// Verify error notification was sent (we send "Syncing Playlist" first, then "Playlist Sync Failed")
	receivedFailedNotification := false
	for i := 0; i < 3; i++ { // Drain up to 3 notifications
		select {
		case event := <-notifCh:
			assert.Equal(t, events.EventTypeNotification, event.Type)
			payload := event.Payload.(events.NotificationPayload)
			if payload.Title == "Playlist Sync Failed" {
				receivedFailedNotification = true
			}
		case <-time.After(100 * time.Millisecond):
		}
	}
	assert.True(t, receivedFailedNotification, "Expected 'Playlist Sync Failed' notification")
}

func TestPlaylistSyncService_RemovePlaylistFromNavidrome_Success(t *testing.T) {
	client, svc, bus, mockSyncer := setupPlaylistSyncService(t)
	ctx := context.Background()

	user := createTestUserWithNavidromeAuth(t, client)
	notifCh, cleanup := bus.Subscribe(user.ID)
	defer cleanup()

	// Create a playlist that was synced
	pl, err := client.Playlist.Create().
		SetUser(user).
		SetRemoteID("remote-789").
		SetName("Synced Playlist").
		SetSource("spotify").
		SetSyncToNavidrome(false). // Sync was just disabled
		SetNavidromePlaylistID("nav-to-delete").
		SetMatchedTrackCount(5).
		Save(ctx)
	require.NoError(t, err)

	// Remove from Navidrome
	err = svc.RemovePlaylistFromNavidrome(ctx, pl.ID)
	require.NoError(t, err)

	// Verify delete was called
	assert.Equal(t, "nav-to-delete", mockSyncer.deletedID)

	// Verify database was cleared
	updatedPl, err := client.Playlist.Get(ctx, pl.ID)
	require.NoError(t, err)
	assert.Empty(t, updatedPl.NavidromePlaylistID)
	assert.Nil(t, updatedPl.LastSyncedAt)
	assert.Equal(t, 0, updatedPl.MatchedTrackCount)

	// Verify notification was sent
	select {
	case event := <-notifCh:
		assert.Equal(t, events.EventTypeNotification, event.Type)
		payload := event.Payload.(events.NotificationPayload)
		assert.Contains(t, payload.Title, "Playlist Removed")
	case <-time.After(100 * time.Millisecond):
		t.Log("No notification received (may be expected if async)")
	}
}

func TestPlaylistSyncService_RemovePlaylistFromNavidrome_NoNavidromeID(t *testing.T) {
	client, svc, _, mockSyncer := setupPlaylistSyncService(t)
	ctx := context.Background()

	user := createTestUserWithNavidromeAuth(t, client)

	// Create a playlist without Navidrome ID
	pl := createTestPlaylistForSync(t, client, user, "spotify", false)

	// Try to remove - should succeed without calling delete
	err := svc.RemovePlaylistFromNavidrome(ctx, pl.ID)
	require.NoError(t, err)

	// Verify delete was NOT called
	assert.Empty(t, mockSyncer.deletedID)
}

func TestPlaylistSyncService_RemovePlaylistFromNavidrome_DeleteDisabled(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { client.Close() })

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{}
	cfg.PlaylistSync.MinMatchConfidence = 0.8
	cfg.PlaylistSync.DeleteOnUnsync = false // Delete disabled

	bus := events.NewBus()
	svc := services.NewPlaylistSyncService(client, cfg, logger, bus)

	mockSyncer := &mockPlaylistSyncer{
		providerType: providers.TypeNavidrome,
	}
	svc.Register(mockPlaylistSyncerFactory(mockSyncer))

	ctx := context.Background()
	user := createTestUserWithNavidromeAuth(t, client)

	// Create a playlist that was synced
	pl, err := client.Playlist.Create().
		SetUser(user).
		SetRemoteID("remote-abc").
		SetName("Keep Me").
		SetSource("spotify").
		SetSyncToNavidrome(false).
		SetNavidromePlaylistID("keep-this-id").
		Save(ctx)
	require.NoError(t, err)

	// Try to remove - should NOT call delete because DeleteOnUnsync is false
	err = svc.RemovePlaylistFromNavidrome(ctx, pl.ID)
	require.NoError(t, err)

	// Verify delete was NOT called
	assert.Empty(t, mockSyncer.deletedID)
}

func TestPlaylistSyncService_SyncAllEnabledPlaylists(t *testing.T) {
	client, svc, _, mockSyncer := setupPlaylistSyncService(t)
	ctx := context.Background()

	user := createTestUserWithNavidromeAuth(t, client)

	// Create multiple playlists
	pl1 := createTestPlaylistForSync(t, client, user, "spotify", true)
	createTestPlaylistTracksForSync(t, client, pl1)

	pl2, err := client.Playlist.Create().
		SetUser(user).
		SetRemoteID("remote-pl2").
		SetName("Playlist 2").
		SetSource("lastfm").
		SetSyncToNavidrome(true).
		SetTrackCount(2).
		Save(ctx)
	require.NoError(t, err)
	createTestPlaylistTracksForSync(t, client, pl2)

	// Create a playlist with sync disabled (should be skipped)
	_ = createTestPlaylistForSync(t, client, user, "spotify", false)

	// Create a Navidrome playlist (should be skipped)
	_, err = client.Playlist.Create().
		SetUser(user).
		SetRemoteID("nav-remote").
		SetName("Native Playlist").
		SetSource("navidrome").
		SetSyncToNavidrome(true). // Even though enabled, should be skipped
		Save(ctx)
	require.NoError(t, err)

	// Reset sync counter
	mockSyncer.syncedPlaylist = nil

	// Sync all enabled playlists
	err = svc.SyncAllEnabledPlaylists(ctx, user.ID)
	require.NoError(t, err)

	// Verify playlists were synced (check database for LastSyncedAt)
	syncedPlaylists, err := client.Playlist.Query().
		Where(
			playlist.SyncToNavidrome(true),
			playlist.SourceNEQ("navidrome"),
			playlist.LastSyncedAtNotNil(),
		).
		Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, syncedPlaylists)
}

func TestPlaylistSyncService_GetPlaylistSyncStatus(t *testing.T) {
	client, svc, _, _ := setupPlaylistSyncService(t)
	ctx := context.Background()

	user := createTestUserWithNavidromeAuth(t, client)

	syncTime := time.Now().Add(-time.Hour)

	// Create a synced playlist
	pl, err := client.Playlist.Create().
		SetUser(user).
		SetRemoteID("remote-status").
		SetName("Status Test").
		SetSource("spotify").
		SetSyncToNavidrome(true).
		SetNavidromePlaylistID("nav-status-123").
		SetLastSyncedAt(syncTime).
		SetMatchedTrackCount(8).
		SetTrackCount(10).
		Save(ctx)
	require.NoError(t, err)

	// Get status
	status, err := svc.GetPlaylistSyncStatus(ctx, pl.ID)
	require.NoError(t, err)

	assert.True(t, status.SyncEnabled)
	assert.Equal(t, "nav-status-123", status.NavidromeID)
	assert.NotNil(t, status.LastSyncedAt)
	assert.Equal(t, 8, status.MatchedTracks)
	assert.Equal(t, 10, status.TotalTracks)
	assert.InDelta(t, 80.0, status.MatchPercentage, 0.1)
	assert.Empty(t, status.SyncError)
}

func TestPlaylistSyncService_GetPlaylistSyncStatus_WithError(t *testing.T) {
	client, svc, _, _ := setupPlaylistSyncService(t)
	ctx := context.Background()

	user := createTestUserWithNavidromeAuth(t, client)

	// Create a playlist with sync error
	pl, err := client.Playlist.Create().
		SetUser(user).
		SetRemoteID("remote-error").
		SetName("Error Test").
		SetSource("spotify").
		SetSyncToNavidrome(true).
		SetSyncError("Connection refused").
		SetTrackCount(5).
		Save(ctx)
	require.NoError(t, err)

	// Get status
	status, err := svc.GetPlaylistSyncStatus(ctx, pl.ID)
	require.NoError(t, err)

	assert.True(t, status.SyncEnabled)
	assert.Empty(t, status.NavidromeID)
	assert.Equal(t, "Connection refused", status.SyncError)
}

func TestPlaylistSyncService_SyncPlaylistToNavidrome_PlaylistNotFound(t *testing.T) {
	_, svc, _, _ := setupPlaylistSyncService(t)
	ctx := context.Background()

	// Try to sync a non-existent playlist
	err := svc.SyncPlaylistToNavidrome(ctx, 99999)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to load playlist")
}

func TestPlaylistSyncService_RebuildPlaylistSync_Success(t *testing.T) {
	client, svc, bus, mockSyncer := setupPlaylistSyncService(t)
	ctx := context.Background()

	user := createTestUserWithNavidromeAuth(t, client)
	notifCh, cleanup := bus.Subscribe(user.ID)
	defer cleanup()

	// Create Navidrome tracks in the library for matching
	createNavidromeTracksForMatching(t, client, user)

	// Create a playlist that was already synced
	pl, err := client.Playlist.Create().
		SetUser(user).
		SetRemoteID("remote-rebuild").
		SetName("Rebuild Test Playlist").
		SetSource("spotify").
		SetSyncToNavidrome(true).
		SetNavidromePlaylistID("old-nav-id").
		SetMatchedTrackCount(5).
		SetTrackCount(10).
		Save(ctx)
	require.NoError(t, err)

	createTestPlaylistTracksForSync(t, client, pl)

	// Rebuild the playlist sync
	err = svc.RebuildPlaylistSync(ctx, pl.ID)
	require.NoError(t, err)

	// Verify old playlist was deleted
	assert.Equal(t, "old-nav-id", mockSyncer.deletedID)

	// Verify new playlist was created (syncedPlaylist should be set)
	assert.NotNil(t, mockSyncer.syncedPlaylist)
	assert.Contains(t, mockSyncer.syncedPlaylist.Name, "Rebuild Test Playlist")

	// Verify database was updated with new ID
	updatedPl, err := client.Playlist.Get(ctx, pl.ID)
	require.NoError(t, err)
	assert.Equal(t, "nav-playlist-123", updatedPl.NavidromePlaylistID)
	assert.NotNil(t, updatedPl.LastSyncedAt)
	assert.Empty(t, updatedPl.SyncError)

	// Verify notifications were sent (rebuild warning + sync success)
	receivedRebuildNotification := false
	for i := 0; i < 5; i++ {
		select {
		case event := <-notifCh:
			assert.Equal(t, events.EventTypeNotification, event.Type)
			payload := event.Payload.(events.NotificationPayload)
			if payload.Title == "Rebuilding Playlist" {
				receivedRebuildNotification = true
			}
		case <-time.After(100 * time.Millisecond):
		}
	}
	assert.True(t, receivedRebuildNotification, "Expected 'Rebuilding Playlist' notification")
}

func TestPlaylistSyncService_RebuildPlaylistSync_SyncDisabled(t *testing.T) {
	client, svc, _, _ := setupPlaylistSyncService(t)
	ctx := context.Background()

	user := createTestUserWithNavidromeAuth(t, client)

	// Create a playlist with sync disabled
	pl := createTestPlaylistForSync(t, client, user, "spotify", false)

	// Try to rebuild - should fail
	err := svc.RebuildPlaylistSync(ctx, pl.ID)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "sync is not enabled")
}

func TestPlaylistSyncService_RebuildPlaylistSync_NavidromePlaylist(t *testing.T) {
	client, svc, _, _ := setupPlaylistSyncService(t)
	ctx := context.Background()

	user := createTestUserWithNavidromeAuth(t, client)

	// Create a Navidrome playlist
	pl := createTestPlaylistForSync(t, client, user, "navidrome", true)

	// Try to rebuild - should fail
	err := svc.RebuildPlaylistSync(ctx, pl.ID)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "cannot rebuild Navidrome playlist")
}

func TestPlaylistSyncService_RebuildPlaylistSync_PlaylistNotFound(t *testing.T) {
	_, svc, _, _ := setupPlaylistSyncService(t)
	ctx := context.Background()

	// Try to rebuild a non-existent playlist
	err := svc.RebuildPlaylistSync(ctx, 99999)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to load playlist")
}

func TestPlaylistSyncService_RebuildPlaylistSync_NoExistingNavidromeID(t *testing.T) {
	client, svc, _, mockSyncer := setupPlaylistSyncService(t)
	ctx := context.Background()

	user := createTestUserWithNavidromeAuth(t, client)

	// Create a playlist with sync enabled but no Navidrome ID yet
	pl, err := client.Playlist.Create().
		SetUser(user).
		SetRemoteID("remote-rebuild-new").
		SetName("New Rebuild Playlist").
		SetSource("spotify").
		SetSyncToNavidrome(true).
		SetTrackCount(3).
		Save(ctx)
	require.NoError(t, err)

	createTestPlaylistTracksForSync(t, client, pl)

	// Rebuild should work (just creates new playlist)
	err = svc.RebuildPlaylistSync(ctx, pl.ID)
	require.NoError(t, err)

	// Verify delete was NOT called (no existing ID)
	assert.Empty(t, mockSyncer.deletedID)

	// Verify new playlist was created
	assert.NotNil(t, mockSyncer.syncedPlaylist)

	// Verify database was updated
	updatedPl, err := client.Playlist.Get(ctx, pl.ID)
	require.NoError(t, err)
	assert.Equal(t, "nav-playlist-123", updatedPl.NavidromePlaylistID)
}

// mockPlaylistManagerSyncer extends mockPlaylistSyncer with the
// providers.PlaylistManager read interface so ListNavidromePlaylists can be
// exercised against a provider that supports both reading and write-back.
type mockPlaylistManagerSyncer struct {
	mockPlaylistSyncer
	playlists       []providers.Playlist
	getPlaylistsErr error
}

func (m *mockPlaylistManagerSyncer) GetPlaylists(ctx context.Context) ([]providers.Playlist, error) {
	if m.getPlaylistsErr != nil {
		return nil, m.getPlaylistsErr
	}
	return m.playlists, nil
}

func (m *mockPlaylistManagerSyncer) CreatePlaylist(ctx context.Context, name, description string, tracks []providers.Track) error {
	return nil
}

// Governing: SPEC playlist-sync-navidrome REQ-PLSYNC-070 (PairWithNavidrome)
func TestPlaylistSyncService_PairWithNavidrome_Success(t *testing.T) {
	client, svc, _, mockSyncer := setupPlaylistSyncService(t)
	ctx := context.Background()

	user := createTestUserWithNavidromeAuth(t, client)
	createNavidromeTracksForMatching(t, client, user)

	// Spotify playlist with sync enabled but not yet paired
	pl := createTestPlaylistForSync(t, client, user, "spotify", true)
	createTestPlaylistTracksForSync(t, client, pl)

	// A Navidrome-source duplicate cached in Spotter's DB for the same remote playlist
	duplicate, err := client.Playlist.Create().
		SetUser(user).
		SetRemoteID("nav-existing-42").
		SetName("Test Playlist").
		SetSource("navidrome").
		Save(ctx)
	require.NoError(t, err)

	// Pair with the existing Navidrome playlist
	err = svc.PairWithNavidrome(ctx, pl.ID, "nav-existing-42")
	require.NoError(t, err)

	// navidrome_playlist_id is set to the chosen remote ID
	updatedPl, err := client.Playlist.Get(ctx, pl.ID)
	require.NoError(t, err)
	assert.Equal(t, "nav-existing-42", updatedPl.NavidromePlaylistID)

	// An immediate sync was triggered and took the UPDATE path (existing playlist)
	assert.Equal(t, "nav-existing-42", mockSyncer.updatedID)
	assert.Nil(t, mockSyncer.syncedPlaylist, "pairing must not create a new Navidrome playlist")
	assert.NotNil(t, updatedPl.LastSyncedAt, "pairing must trigger an immediate sync")
	assert.Len(t, mockSyncer.updatedTracks, 3, "all matched tracks should be pushed to the paired playlist")

	// The Navidrome-source duplicate was removed from Spotter's DB
	exists, err := client.Playlist.Query().
		Where(playlist.ID(duplicate.ID)).
		Exist(ctx)
	require.NoError(t, err)
	assert.False(t, exists, "Navidrome-source duplicate must be deleted after pairing")
}

// Governing: SPEC playlist-sync-navidrome REQ-PLSYNC-070 (PairWithNavidrome)
func TestPlaylistSyncService_PairWithNavidrome_NoDuplicate(t *testing.T) {
	client, svc, _, mockSyncer := setupPlaylistSyncService(t)
	ctx := context.Background()

	user := createTestUserWithNavidromeAuth(t, client)
	createNavidromeTracksForMatching(t, client, user)

	pl := createTestPlaylistForSync(t, client, user, "spotify", true)
	createTestPlaylistTracksForSync(t, client, pl)

	// Pair with a remote ID that has no cached Navidrome-source duplicate
	err := svc.PairWithNavidrome(ctx, pl.ID, "nav-arbitrary-target")
	require.NoError(t, err)

	updatedPl, err := client.Playlist.Get(ctx, pl.ID)
	require.NoError(t, err)
	assert.Equal(t, "nav-arbitrary-target", updatedPl.NavidromePlaylistID)
	assert.Equal(t, "nav-arbitrary-target", mockSyncer.updatedID)
	assert.NotNil(t, updatedPl.LastSyncedAt)
}

// Governing: SPEC playlist-sync-navidrome REQ-PLSYNC-070 (PairWithNavidrome)
func TestPlaylistSyncService_PairWithNavidrome_SyncDisabled(t *testing.T) {
	client, svc, _, mockSyncer := setupPlaylistSyncService(t)
	ctx := context.Background()

	user := createTestUserWithNavidromeAuth(t, client)

	// Sync disabled: pairing still records the remote ID, but the follow-up
	// sync is a no-op until sync is enabled (handlers enable sync first).
	pl := createTestPlaylistForSync(t, client, user, "spotify", false)

	err := svc.PairWithNavidrome(ctx, pl.ID, "nav-disabled-target")
	require.NoError(t, err)

	updatedPl, err := client.Playlist.Get(ctx, pl.ID)
	require.NoError(t, err)
	assert.Equal(t, "nav-disabled-target", updatedPl.NavidromePlaylistID)
	assert.Empty(t, mockSyncer.updatedID, "sync must not run while sync_to_navidrome is false")
}

func TestPlaylistSyncService_PairWithNavidrome_PlaylistNotFound(t *testing.T) {
	_, svc, _, _ := setupPlaylistSyncService(t)
	ctx := context.Background()

	err := svc.PairWithNavidrome(ctx, 99999, "nav-whatever")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to load playlist")
}

// TestPlaylistSyncService_PairDuringInFlightSync_PairingNotLost is a regression
// test for issue #48: a Sync-Now / scheduled sync in flight for an
// enabled-but-unpaired playlist must not clobber a pairing the user commits
// concurrently.
//
// Before the per-playlist lock, the sync loaded the row with an empty
// navidrome_playlist_id, created a fresh remote playlist, and its Save
// overwrote (last-write-wins) the id PairWithNavidrome had just set — leaving
// the playlist linked to a duplicate the user never chose.
//
// The test forces the exact dangerous interleaving deterministically: the sync
// is blocked mid-create while holding the per-playlist lock, then the pair is
// launched and must serialize behind the sync. With the lock the pair's load
// always happens after the sync's Save, so the pairing the user chose wins
// regardless of goroutine scheduling.
// Governing: issue #48 (playlist sync concurrency guard),
// SPEC playlist-sync-navidrome REQ-PLSYNC-070 (PairWithNavidrome)
func TestPlaylistSyncService_PairDuringInFlightSync_PairingNotLost(t *testing.T) {
	client, svc, _, mockSyncer := setupPlaylistSyncService(t)
	ctx := context.Background()

	user := createTestUserWithNavidromeAuth(t, client)
	createNavidromeTracksForMatching(t, client, user)

	// Enabled but not yet paired -> a sync takes the CREATE path.
	pl := createTestPlaylistForSync(t, client, user, "spotify", true)
	createTestPlaylistTracksForSync(t, client, pl)

	// The user's chosen existing Navidrome playlist, cached as a navidrome-source
	// duplicate in Spotter's DB (as PairWithNavidrome expects).
	const pairedRemoteID = "nav-user-chosen-99"
	require.NotEqual(t, mockSyncer.syncedID, pairedRemoteID)
	duplicate, err := client.Playlist.Create().
		SetUser(user).
		SetRemoteID(pairedRemoteID).
		SetName("Test Playlist").
		SetSource("navidrome").
		Save(ctx)
	require.NoError(t, err)

	// Make the create path block so the sync is provably "in flight" (holding
	// the per-playlist lock) while we launch the pair.
	mockSyncer.syncPlaylistEntered = make(chan struct{})
	mockSyncer.syncPlaylistBlock = make(chan struct{})

	var wg sync.WaitGroup
	var syncErr, pairErr error

	// 1. Start the sync; it grabs the per-playlist lock, loads the unpaired row,
	//    enters the CREATE path, and blocks inside SyncPlaylist.
	wg.Add(1)
	go func() {
		defer wg.Done()
		syncErr = svc.SyncPlaylistToNavidrome(ctx, pl.ID)
	}()
	<-mockSyncer.syncPlaylistEntered // sync now holds the lock, blocked mid-create

	// 2. Launch the pair. It must block on the per-playlist lock until the sync
	//    finishes; without the lock it would race the sync's Save.
	wg.Add(1)
	go func() {
		defer wg.Done()
		pairErr = svc.PairWithNavidrome(ctx, pl.ID, pairedRemoteID)
	}()

	// 3. Release the blocked create so the sync completes and drops the lock,
	//    letting the pair proceed.
	close(mockSyncer.syncPlaylistBlock)
	wg.Wait()

	require.NoError(t, syncErr)
	require.NoError(t, pairErr)

	// The pairing the user chose survives: the playlist is linked to the paired
	// remote id, NOT the freshly-created duplicate the in-flight sync produced.
	updatedPl, err := client.Playlist.Get(ctx, pl.ID)
	require.NoError(t, err)
	assert.Equal(t, pairedRemoteID, updatedPl.NavidromePlaylistID,
		"pairing must win over the in-flight create sync")
	assert.NotEqual(t, mockSyncer.syncedID, updatedPl.NavidromePlaylistID,
		"playlist must not be left linked to the sync-created duplicate")

	// The pair took the UPDATE path against the user's chosen playlist...
	assert.Equal(t, pairedRemoteID, mockSyncer.updatedID,
		"pair must UPDATE the chosen playlist, not create a new one")

	// ...and the navidrome-source duplicate cache row was removed by the pair.
	exists, err := client.Playlist.Query().
		Where(playlist.ID(duplicate.ID)).
		Exist(ctx)
	require.NoError(t, err)
	assert.False(t, exists, "navidrome-source duplicate must be deleted after pairing")
}

// Governing: SPEC playlist-sync-navidrome REQ-PLSYNC-033 (per-request keep/delete override)
func TestPlaylistSyncService_RemovePlaylistFromNavidromeWithChoice_KeepOverridesConfig(t *testing.T) {
	// Config says delete on unsync...
	client, svc, _, mockSyncer := setupPlaylistSyncService(t) // DeleteOnUnsync = true
	ctx := context.Background()

	user := createTestUserWithNavidromeAuth(t, client)

	pl, err := client.Playlist.Create().
		SetUser(user).
		SetRemoteID("remote-keep-choice").
		SetName("Keep Me Anyway").
		SetSource("spotify").
		SetSyncToNavidrome(false).
		SetNavidromePlaylistID("nav-keep-choice").
		Save(ctx)
	require.NoError(t, err)

	// ...but the explicit per-request choice is to keep.
	err = svc.RemovePlaylistFromNavidromeWithChoice(ctx, pl.ID, false)
	require.NoError(t, err)

	// Delete was NOT called and the pairing info is retained.
	assert.Empty(t, mockSyncer.deletedID)
	updatedPl, err := client.Playlist.Get(ctx, pl.ID)
	require.NoError(t, err)
	assert.Equal(t, "nav-keep-choice", updatedPl.NavidromePlaylistID)
}

// Governing: SPEC playlist-sync-navidrome REQ-PLSYNC-033 (per-request keep/delete override)
func TestPlaylistSyncService_RemovePlaylistFromNavidromeWithChoice_DeleteOverridesConfig(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { client.Close() })

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{}
	cfg.PlaylistSync.MinMatchConfidence = 0.8
	cfg.PlaylistSync.DeleteOnUnsync = false // Config says keep on unsync...

	bus := events.NewBus()
	svc := services.NewPlaylistSyncService(client, cfg, logger, bus)

	mockSyncer := &mockPlaylistSyncer{
		providerType: providers.TypeNavidrome,
	}
	svc.Register(mockPlaylistSyncerFactory(mockSyncer))

	ctx := context.Background()
	user := createTestUserWithNavidromeAuth(t, client)

	pl, err := client.Playlist.Create().
		SetUser(user).
		SetRemoteID("remote-delete-choice").
		SetName("Delete Me Anyway").
		SetSource("spotify").
		SetSyncToNavidrome(false).
		SetNavidromePlaylistID("nav-delete-choice").
		Save(ctx)
	require.NoError(t, err)

	// ...but the explicit per-request choice is to delete.
	err = svc.RemovePlaylistFromNavidromeWithChoice(ctx, pl.ID, true)
	require.NoError(t, err)

	// Delete WAS called and sync info was cleared.
	assert.Equal(t, "nav-delete-choice", mockSyncer.deletedID)
	updatedPl, err := client.Playlist.Get(ctx, pl.ID)
	require.NoError(t, err)
	assert.Empty(t, updatedPl.NavidromePlaylistID)
}

// Governing: SPEC playlist-sync-navidrome REQ-PLSYNC-071 (candidate listing via provider GetPlaylists)
func TestPlaylistSyncService_ListNavidromePlaylists_Success(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { client.Close() })

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{}
	cfg.PlaylistSync.MinMatchConfidence = 0.8

	bus := events.NewBus()
	svc := services.NewPlaylistSyncService(client, cfg, logger, bus)

	mockManager := &mockPlaylistManagerSyncer{
		mockPlaylistSyncer: mockPlaylistSyncer{providerType: providers.TypeNavidrome},
		playlists: []providers.Playlist{
			{ID: "nav-1", Name: "Morning Coffee", TrackCount: 12},
			{ID: "nav-2", Name: "Late Night", TrackCount: 30},
		},
	}
	svc.Register(mockPlaylistSyncerFactory(mockManager))

	ctx := context.Background()
	user := createTestUserWithNavidromeAuth(t, client)

	playlists, err := svc.ListNavidromePlaylists(ctx, user.ID)
	require.NoError(t, err)
	require.Len(t, playlists, 2)
	assert.Equal(t, "nav-1", playlists[0].ID)
	assert.Equal(t, "Morning Coffee", playlists[0].Name)
	assert.Equal(t, "nav-2", playlists[1].ID)
}

// Governing: SPEC playlist-sync-navidrome REQ-PLSYNC-071 (candidate listing via provider GetPlaylists)
func TestPlaylistSyncService_ListNavidromePlaylists_NotAPlaylistManager(t *testing.T) {
	client, svc, _, _ := setupPlaylistSyncService(t) // mockPlaylistSyncer is not a PlaylistManager
	ctx := context.Background()

	user := createTestUserWithNavidromeAuth(t, client)

	_, err := svc.ListNavidromePlaylists(ctx, user.ID)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "does not implement PlaylistManager")
}

func TestPlaylistSyncService_ListNavidromePlaylists_UserNotFound(t *testing.T) {
	_, svc, _, _ := setupPlaylistSyncService(t)
	ctx := context.Background()

	_, err := svc.ListNavidromePlaylists(ctx, 99999)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to load user")
}

// libraryQueryFailingDriver wraps an Ent driver and, when enabled, fails any
// read query against the "tracks" table (the library query issued by
// TrackMatcher.LoadLibraryIndex) while letting every other statement through,
// so playlist updates and SyncEvent writes still succeed.
type libraryQueryFailingDriver struct {
	dialect.Driver
	fail          atomic.Bool
	failedQueries atomic.Int64
}

func (d *libraryQueryFailingDriver) Query(ctx context.Context, query string, args, v any) error {
	if d.fail.Load() && (strings.Contains(query, "`tracks`") || strings.Contains(query, `"tracks"`)) {
		d.failedQueries.Add(1)
		return errors.New("injected library query failure")
	}
	return d.Driver.Query(ctx, query, args, v)
}

// Governing: SPEC playlist-sync-navidrome REQ-PLSYNC-060 (SyncEvent audit logging on failure)
// Issue #330 / PR review follow-up: when the tick-level LoadLibraryIndex in
// SyncAllEnabledPlaylists fails, the tick must NOT abort before any playlist
// gets error handling. It falls back to per-playlist loading so each due
// playlist still reaches handleSyncError: sync_status=error, a
// playlist_sync_failed SyncEvent, and a UI notification.
func TestPlaylistSyncService_SyncAllEnabledPlaylists_LibraryIndexLoadFailure(t *testing.T) {
	ctx := context.Background()

	drv, err := entsql.Open("sqlite3", "file:libfailtick?mode=memory&cache=shared&_fk=1")
	require.NoError(t, err)
	failing := &libraryQueryFailingDriver{Driver: drv}
	client := ent.NewClient(ent.Driver(failing))
	t.Cleanup(func() { client.Close() })
	require.NoError(t, client.Schema.Create(ctx))

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{}
	cfg.PlaylistSync.MinMatchConfidence = 0.7
	bus := events.NewBus()
	svc := services.NewPlaylistSyncService(client, cfg, logger, bus)
	svc.Register(mockPlaylistSyncerFactory(&mockPlaylistSyncer{
		providerType: providers.TypeNavidrome,
		syncedID:     "nav-playlist-tickfail",
	}))

	user := createTestUserWithNavidromeAuth(t, client)
	pl1 := createTestPlaylistForSync(t, client, user, "spotify", true)
	createTestPlaylistTracksForSync(t, client, pl1)
	pl2 := createTestPlaylistForSync(t, client, user, "spotify", true)
	createTestPlaylistTracksForSync(t, client, pl2)

	failing.fail.Store(true)
	err = svc.SyncAllEnabledPlaylists(ctx, user.ID)
	failing.fail.Store(false)

	// The tick itself reports the per-playlist failures, not the index error.
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to sync 2 playlists")

	// Proof the fallback ran: one failed tick-level load, then one failed
	// per-playlist load for each of the two playlists (1 + 2 = 3).
	assert.Equal(t, int64(3), failing.failedQueries.Load(),
		"expected tick-level load plus one per-playlist fallback load per playlist")

	// REQ-PLSYNC-060: every due playlist ends in sync_status=error with the
	// library-index error recorded.
	for _, plID := range []int{pl1.ID, pl2.ID} {
		updated, getErr := client.Playlist.Get(ctx, plID)
		require.NoError(t, getErr)
		assert.Equal(t, playlist.SyncStatusError, updated.SyncStatus,
			"playlist %d must end with sync_status=error", plID)
		assert.Contains(t, updated.SyncError, "failed to load library index",
			"playlist %d must record the library index error", plID)
	}

	// ...and every due playlist gets a playlist_sync_failed SyncEvent.
	failedEvents, err := client.SyncEvent.Query().
		Where(syncevent.EventTypeEQ(syncevent.EventTypePlaylistSyncFailed)).
		All(ctx)
	require.NoError(t, err)
	require.Len(t, failedEvents, 2)
	combinedMetadata := failedEvents[0].Metadata + failedEvents[1].Metadata
	assert.Contains(t, combinedMetadata, fmt.Sprintf(`"playlist_id":%d`, pl1.ID))
	assert.Contains(t, combinedMetadata, fmt.Sprintf(`"playlist_id":%d`, pl2.ID))
}
