package handlers_test

// Tests for the Navidrome pairing surface:
//   - ResolveNavidromeConflict (POST /playlists/{id}/resolve-navidrome-conflict)
//   - TogglePlaylistSync navidrome_action override (POST /playlists/{id}/toggle-sync)
//   - LinkNavidromePicker (GET /playlists/{id}/link)
//   - LinkWithNavidrome (POST /playlists/{id}/link/{targetId})
//
// Governing: SPEC playlist-sync-navidrome REQ-PLSYNC-033, REQ-PLSYNC-070,
// REQ-PLSYNC-071, REQ-PLSYNC-072

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"spotter/ent"
	"spotter/ent/playlist"
	"spotter/internal/auth"
	"spotter/internal/config"
	"spotter/internal/crypto"
	"spotter/internal/events"
	"spotter/internal/handlers"
	"spotter/internal/providers"
	"spotter/internal/services"

	"github.com/go-chi/chi/v5"
	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockNavidromeProvider implements providers.PlaylistSyncer and
// providers.PlaylistManager for handler tests. It is safe for concurrent use
// because handlers dispatch sync/pair operations in background goroutines.
type mockNavidromeProvider struct {
	mu sync.Mutex

	playlists       []providers.Playlist
	getPlaylistsErr error

	syncedID      string
	deletedID     string
	updatedID     string
	updatedTracks []providers.Track
}

func (m *mockNavidromeProvider) Type() providers.Type { return providers.TypeNavidrome }

func (m *mockNavidromeProvider) GetPlaylists(ctx context.Context) ([]providers.Playlist, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.getPlaylistsErr != nil {
		return nil, m.getPlaylistsErr
	}
	return m.playlists, nil
}

func (m *mockNavidromeProvider) CreatePlaylist(ctx context.Context, name, description string, tracks []providers.Track) error {
	return nil
}

func (m *mockNavidromeProvider) SyncPlaylist(ctx context.Context, req providers.SyncPlaylistRequest) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.syncedID = "nav-created-by-mock"
	return m.syncedID, nil
}

func (m *mockNavidromeProvider) DeletePlaylist(ctx context.Context, remotePlaylistID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deletedID = remotePlaylistID
	return nil
}

func (m *mockNavidromeProvider) UpdatePlaylistTracks(ctx context.Context, remotePlaylistID string, tracks []providers.Track) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.updatedID = remotePlaylistID
	m.updatedTracks = tracks
	return nil
}

func (m *mockNavidromeProvider) DeletedID() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.deletedID
}

func (m *mockNavidromeProvider) UpdatedID() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.updatedID
}

// setupPlaylistLinkHandler builds a handler whose PlaylistSyncService has a
// mock Navidrome provider registered, so pairing/removal flows can complete.
func setupPlaylistLinkHandler(t *testing.T, deleteOnUnsync bool) (*ent.Client, *handlers.Handler, *mockNavidromeProvider) {
	client := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{}
	cfg.PlaylistSync.MinMatchConfidence = 0.8
	cfg.PlaylistSync.DeleteOnUnsync = deleteOnUnsync
	bus := events.NewBus()
	syncer := services.NewSyncer(client, cfg, logger, bus, nil)
	playlistSyncSvc := services.NewPlaylistSyncService(client, cfg, logger, bus)

	mock := &mockNavidromeProvider{}
	playlistSyncSvc.Register(func(ctx context.Context, u *ent.User) (providers.Provider, error) {
		return mock, nil
	})

	encryptor, _ := crypto.NewEncryptor(make([]byte, 32))
	jwtManager := auth.NewJWTManager(testJWTSecret)
	h := handlers.New(client, cfg, logger, encryptor, jwtManager, syncer, nil, playlistSyncSvc, nil, nil, nil, bus, nil)
	return client, h, mock
}

// createLinkTestUser creates a user with a NavidromeAuth edge so the
// PlaylistSyncService can resolve the (mock) Navidrome provider.
func createLinkTestUser(t *testing.T, client *ent.Client) *ent.User {
	u, err := client.User.Create().
		SetUsername(uniquePlaylistTestUsername()).
		SetPaginationSize(25).
		Save(context.Background())
	require.NoError(t, err)

	_, err = client.NavidromeAuth.Create().
		SetPassword("testpassword").
		SetUser(u).
		Save(context.Background())
	require.NoError(t, err)

	return u
}

// newPlaylistRequest builds a request with user context and chi URL params.
func newPlaylistRequest(method, target string, body string, u *ent.User, params map[string]string) *http.Request {
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if u != nil {
		req = req.WithContext(context.WithValue(req.Context(), handlers.UserContextKey, u))
	}
	rctx := chi.NewRouteContext()
	for k, v := range params {
		rctx.URLParams.Add(k, v)
	}
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

// awaitNotification waits for the bus notification with the given title.
//
// The PlaylistSyncService publishes its terminal notification ("Playlist
// Synced", "Playlist Removed", "Playlist Sync Failed") AFTER its final
// database write, so receiving it is a deterministic signal that the
// handler's background goroutine has finished mutating state — no polling of
// DB or mock state that races with the goroutine. Subscribe BEFORE invoking
// the handler so the event cannot be missed. An "error" notification arriving
// first fails the test immediately with the async error message.
func awaitNotification(t *testing.T, ch <-chan events.Event, title string) {
	t.Helper()
	deadline := time.After(30 * time.Second)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatalf("event bus subscription closed while waiting for notification %q", title)
			}
			if ev.Type != events.EventTypeNotification {
				continue
			}
			payload, isNotification := ev.Payload.(events.NotificationPayload)
			if !isNotification {
				continue
			}
			if payload.Title == title {
				return
			}
			if payload.IconType == "error" {
				t.Fatalf("async operation failed while waiting for %q: %s: %s",
					title, payload.Title, payload.Message)
			}
		case <-deadline:
			t.Fatalf("timed out waiting for notification %q", title)
		}
	}
}

// ---------------------------------------------------------------------------
// ResolveNavidromeConflict
// ---------------------------------------------------------------------------

func TestResolveNavidromeConflict_Unauthorized(t *testing.T) {
	_, h, _ := setupPlaylistLinkHandler(t, false)

	req := newPlaylistRequest("POST", "/playlists/1/resolve-navidrome-conflict",
		"action=pair&existing_id=nav-1", nil, map[string]string{"id": "1"})
	w := httptest.NewRecorder()

	h.ResolveNavidromeConflict(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Result().StatusCode)
}

func TestResolveNavidromeConflict_NotFound(t *testing.T) {
	client, h, _ := setupPlaylistLinkHandler(t, false)
	u := createLinkTestUser(t, client)

	req := newPlaylistRequest("POST", "/playlists/99999/resolve-navidrome-conflict",
		"action=pair&existing_id=nav-1", u, map[string]string{"id": "99999"})
	w := httptest.NewRecorder()

	h.ResolveNavidromeConflict(w, req)

	assert.Equal(t, http.StatusNotFound, w.Result().StatusCode)
}

func TestResolveNavidromeConflict_WrongSource(t *testing.T) {
	client, h, _ := setupPlaylistLinkHandler(t, false)
	u := createLinkTestUser(t, client)

	// A Navidrome-source playlist can never be in a Navidrome conflict
	pl, err := client.Playlist.Create().
		SetUser(u).
		SetRemoteID("nav-native").
		SetName("Native Playlist").
		SetSource("navidrome").
		Save(context.Background())
	require.NoError(t, err)

	req := newPlaylistRequest("POST", "/playlists/"+strconv.Itoa(pl.ID)+"/resolve-navidrome-conflict",
		"action=pair&existing_id=nav-1", u, map[string]string{"id": strconv.Itoa(pl.ID)})
	w := httptest.NewRecorder()

	h.ResolveNavidromeConflict(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Result().StatusCode)
}

func TestResolveNavidromeConflict_InvalidAction(t *testing.T) {
	client, h, _ := setupPlaylistLinkHandler(t, false)
	u := createLinkTestUser(t, client)

	pl, err := client.Playlist.Create().
		SetUser(u).
		SetRemoteID("spotify-conflict-1").
		SetName("Conflicted").
		SetSource("spotify").
		Save(context.Background())
	require.NoError(t, err)

	req := newPlaylistRequest("POST", "/playlists/"+strconv.Itoa(pl.ID)+"/resolve-navidrome-conflict",
		"action=bogus", u, map[string]string{"id": strconv.Itoa(pl.ID)})
	w := httptest.NewRecorder()

	h.ResolveNavidromeConflict(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Result().StatusCode)
}

func TestResolveNavidromeConflict_PairMissingExistingID(t *testing.T) {
	client, h, _ := setupPlaylistLinkHandler(t, false)
	u := createLinkTestUser(t, client)

	pl, err := client.Playlist.Create().
		SetUser(u).
		SetRemoteID("spotify-conflict-2").
		SetName("Conflicted").
		SetSource("spotify").
		Save(context.Background())
	require.NoError(t, err)

	req := newPlaylistRequest("POST", "/playlists/"+strconv.Itoa(pl.ID)+"/resolve-navidrome-conflict",
		"action=pair", u, map[string]string{"id": strconv.Itoa(pl.ID)})
	w := httptest.NewRecorder()

	h.ResolveNavidromeConflict(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Result().StatusCode)
}

func TestResolveNavidromeConflict_NewNameMissingName(t *testing.T) {
	client, h, _ := setupPlaylistLinkHandler(t, false)
	u := createLinkTestUser(t, client)

	pl, err := client.Playlist.Create().
		SetUser(u).
		SetRemoteID("spotify-conflict-3").
		SetName("Conflicted").
		SetSource("spotify").
		Save(context.Background())
	require.NoError(t, err)

	req := newPlaylistRequest("POST", "/playlists/"+strconv.Itoa(pl.ID)+"/resolve-navidrome-conflict",
		"action=new-name", u, map[string]string{"id": strconv.Itoa(pl.ID)})
	w := httptest.NewRecorder()

	h.ResolveNavidromeConflict(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Result().StatusCode)
}

func TestResolveNavidromeConflict_Pair_HappyPath(t *testing.T) {
	client, h, mock := setupPlaylistLinkHandler(t, false)
	u := createLinkTestUser(t, client)

	// Spotify playlist, sync disabled, same name as an existing Navidrome playlist
	pl, err := client.Playlist.Create().
		SetUser(u).
		SetRemoteID("spotify-pair-happy").
		SetName("Shared Name").
		SetSource("spotify").
		SetSyncToNavidrome(false).
		Save(context.Background())
	require.NoError(t, err)

	// The cached Navidrome-source duplicate that triggered the conflict
	duplicate, err := client.Playlist.Create().
		SetUser(u).
		SetRemoteID("nav-shared-77").
		SetName("Shared Name").
		SetSource("navidrome").
		Save(context.Background())
	require.NoError(t, err)

	// Subscribe before invoking the handler so the async completion event
	// cannot be missed.
	notifications, unsubscribe := h.Bus.Subscribe(u.ID)
	defer unsubscribe()

	req := newPlaylistRequest("POST", "/playlists/"+strconv.Itoa(pl.ID)+"/resolve-navidrome-conflict",
		"action=pair&existing_id=nav-shared-77", u, map[string]string{"id": strconv.Itoa(pl.ID)})
	w := httptest.NewRecorder()

	h.ResolveNavidromeConflict(w, req)

	resp := w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// HTMX response: the refreshed sync dropdown replaces the conflict UI
	body, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(body), `id="playlist-sync-dropdown"`)

	// Pairing runs async: the "Playlist Synced" notification is published after
	// the final DB write, so state below is settled once it arrives.
	awaitNotification(t, notifications, "Playlist Synced")

	updatedPl, err := client.Playlist.Get(context.Background(), pl.ID)
	require.NoError(t, err)
	assert.True(t, updatedPl.SyncToNavidrome)
	assert.Equal(t, "nav-shared-77", updatedPl.NavidromePlaylistID,
		"pairing must set navidrome_playlist_id")
	assert.NotNil(t, updatedPl.LastSyncedAt, "pairing must trigger an immediate sync")
	assert.Equal(t, "nav-shared-77", mock.UpdatedID(), "immediate sync must UPDATE the paired playlist")

	// The Navidrome-source duplicate was deleted
	exists, err := client.Playlist.Query().
		Where(playlist.ID(duplicate.ID)).
		Exist(context.Background())
	require.NoError(t, err)
	assert.False(t, exists)
}

func TestResolveNavidromeConflict_NewName_HappyPath(t *testing.T) {
	client, h, _ := setupPlaylistLinkHandler(t, false)
	u := createLinkTestUser(t, client)

	pl, err := client.Playlist.Create().
		SetUser(u).
		SetRemoteID("spotify-newname-happy").
		SetName("Shared Name").
		SetSource("spotify").
		SetSyncToNavidrome(false).
		Save(context.Background())
	require.NoError(t, err)

	// Subscribe before invoking the handler so the async completion event
	// cannot be missed.
	notifications, unsubscribe := h.Bus.Subscribe(u.ID)
	defer unsubscribe()

	req := newPlaylistRequest("POST", "/playlists/"+strconv.Itoa(pl.ID)+"/resolve-navidrome-conflict",
		"action=new-name&name=Custom+Navidrome+Name", u, map[string]string{"id": strconv.Itoa(pl.ID)})
	w := httptest.NewRecorder()

	h.ResolveNavidromeConflict(w, req)

	resp := w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	body, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(body), `id="playlist-sync-dropdown"`)

	// Wait for the async sync to finish so the goroutine doesn't outlive the
	// test DB; the notification is published after the final DB write.
	awaitNotification(t, notifications, "Playlist Synced")

	// Custom name saved, sync enabled, and the async sync completed
	updatedPl, err := client.Playlist.Get(context.Background(), pl.ID)
	require.NoError(t, err)
	assert.Equal(t, "Custom Navidrome Name", updatedPl.NavidromePlaylistName)
	assert.True(t, updatedPl.SyncToNavidrome)
	assert.NotNil(t, updatedPl.LastSyncedAt, "sync must run after name resolution")
}

// ---------------------------------------------------------------------------
// TogglePlaylistSync navidrome_action override
// ---------------------------------------------------------------------------

// Governing: SPEC playlist-sync-navidrome REQ-PLSYNC-033
func TestTogglePlaylistSync_InvalidNavidromeAction(t *testing.T) {
	client, h, _ := setupPlaylistLinkHandler(t, false)
	u := createLinkTestUser(t, client)

	pl, err := client.Playlist.Create().
		SetUser(u).
		SetRemoteID("spotify-invalid-action").
		SetName("Synced Playlist").
		SetSource("spotify").
		SetSyncToNavidrome(true).
		SetNavidromePlaylistID("nav-keep").
		Save(context.Background())
	require.NoError(t, err)

	req := newPlaylistRequest("POST", "/playlists/"+strconv.Itoa(pl.ID)+"/toggle-sync",
		"navidrome_action=bogus", u, map[string]string{"id": strconv.Itoa(pl.ID)})
	w := httptest.NewRecorder()

	h.TogglePlaylistSync(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Result().StatusCode)

	// Sync state must be unchanged
	updatedPl, err := client.Playlist.Get(context.Background(), pl.ID)
	require.NoError(t, err)
	assert.True(t, updatedPl.SyncToNavidrome)
}

// Governing: SPEC playlist-sync-navidrome REQ-PLSYNC-033 — explicit "delete"
// overrides delete_on_unsync=false.
func TestTogglePlaylistSync_DisableSync_DeleteOverride(t *testing.T) {
	client, h, mock := setupPlaylistLinkHandler(t, false) // config says keep
	u := createLinkTestUser(t, client)

	pl, err := client.Playlist.Create().
		SetUser(u).
		SetRemoteID("spotify-delete-override").
		SetName("Delete Override").
		SetSource("spotify").
		SetSyncToNavidrome(true).
		SetNavidromePlaylistID("nav-delete-override").
		Save(context.Background())
	require.NoError(t, err)

	// Subscribe before invoking the handler so the async completion event
	// cannot be missed.
	notifications, unsubscribe := h.Bus.Subscribe(u.ID)
	defer unsubscribe()

	req := newPlaylistRequest("POST", "/playlists/"+strconv.Itoa(pl.ID)+"/toggle-sync",
		"navidrome_action=delete", u, map[string]string{"id": strconv.Itoa(pl.ID)})
	w := httptest.NewRecorder()

	h.TogglePlaylistSync(w, req)

	assert.Equal(t, http.StatusOK, w.Result().StatusCode)

	// Removal runs async: the "Playlist Removed" notification is published
	// after the pairing info is cleared in the DB.
	awaitNotification(t, notifications, "Playlist Removed")

	updatedPl, err := client.Playlist.Get(context.Background(), pl.ID)
	require.NoError(t, err)
	assert.False(t, updatedPl.SyncToNavidrome)
	assert.Empty(t, updatedPl.NavidromePlaylistID,
		"explicit delete must remove the Navidrome playlist even when delete_on_unsync=false")
	assert.Equal(t, "nav-delete-override", mock.DeletedID())
}

// Governing: SPEC playlist-sync-navidrome REQ-PLSYNC-033 — explicit "keep"
// overrides delete_on_unsync=true.
func TestTogglePlaylistSync_DisableSync_KeepOverride(t *testing.T) {
	client, h, mock := setupPlaylistLinkHandler(t, true) // config says delete
	u := createLinkTestUser(t, client)

	pl, err := client.Playlist.Create().
		SetUser(u).
		SetRemoteID("spotify-keep-override").
		SetName("Keep Override").
		SetSource("spotify").
		SetSyncToNavidrome(true).
		SetNavidromePlaylistID("nav-keep-override").
		Save(context.Background())
	require.NoError(t, err)

	req := newPlaylistRequest("POST", "/playlists/"+strconv.Itoa(pl.ID)+"/toggle-sync",
		"navidrome_action=keep", u, map[string]string{"id": strconv.Itoa(pl.ID)})
	w := httptest.NewRecorder()

	h.TogglePlaylistSync(w, req)

	assert.Equal(t, http.StatusOK, w.Result().StatusCode)

	// Sync disabled synchronously
	updatedPl, err := client.Playlist.Get(context.Background(), pl.ID)
	require.NoError(t, err)
	assert.False(t, updatedPl.SyncToNavidrome)

	// Give the async removal path time to (incorrectly) run, then verify the
	// Navidrome playlist was kept despite delete_on_unsync=true.
	time.Sleep(300 * time.Millisecond)
	updatedPl, err = client.Playlist.Get(context.Background(), pl.ID)
	require.NoError(t, err)
	assert.Equal(t, "nav-keep-override", updatedPl.NavidromePlaylistID,
		"explicit keep must retain pairing info even when delete_on_unsync=true")
	assert.Empty(t, mock.DeletedID(), "explicit keep must not delete the Navidrome playlist")
}

// ---------------------------------------------------------------------------
// LinkNavidromePicker (GET /playlists/{id}/link)
// ---------------------------------------------------------------------------

func TestLinkNavidromePicker_Unauthorized(t *testing.T) {
	_, h, _ := setupPlaylistLinkHandler(t, false)

	req := newPlaylistRequest("GET", "/playlists/1/link", "", nil, map[string]string{"id": "1"})
	w := httptest.NewRecorder()

	h.LinkNavidromePicker(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Result().StatusCode)
}

func TestLinkNavidromePicker_NotFound(t *testing.T) {
	client, h, _ := setupPlaylistLinkHandler(t, false)
	u := createLinkTestUser(t, client)

	req := newPlaylistRequest("GET", "/playlists/99999/link", "", u, map[string]string{"id": "99999"})
	w := httptest.NewRecorder()

	h.LinkNavidromePicker(w, req)

	assert.Equal(t, http.StatusNotFound, w.Result().StatusCode)
}

func TestLinkNavidromePicker_UserIsolation(t *testing.T) {
	client, h, _ := setupPlaylistLinkHandler(t, false)
	owner := createLinkTestUser(t, client)
	other := createLinkTestUser(t, client)

	// Playlist belongs to owner; other must not be able to open the picker for it
	pl, err := client.Playlist.Create().
		SetUser(owner).
		SetRemoteID("spotify-isolated").
		SetName("Owner Playlist").
		SetSource("spotify").
		Save(context.Background())
	require.NoError(t, err)

	req := newPlaylistRequest("GET", "/playlists/"+strconv.Itoa(pl.ID)+"/link", "", other,
		map[string]string{"id": strconv.Itoa(pl.ID)})
	w := httptest.NewRecorder()

	h.LinkNavidromePicker(w, req)

	assert.Equal(t, http.StatusNotFound, w.Result().StatusCode)
}

func TestLinkNavidromePicker_WrongSource(t *testing.T) {
	client, h, _ := setupPlaylistLinkHandler(t, false)
	u := createLinkTestUser(t, client)

	pl, err := client.Playlist.Create().
		SetUser(u).
		SetRemoteID("nav-native-link").
		SetName("Native Playlist").
		SetSource("navidrome").
		Save(context.Background())
	require.NoError(t, err)

	req := newPlaylistRequest("GET", "/playlists/"+strconv.Itoa(pl.ID)+"/link", "", u,
		map[string]string{"id": strconv.Itoa(pl.ID)})
	w := httptest.NewRecorder()

	h.LinkNavidromePicker(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Result().StatusCode)
}

func TestLinkNavidromePicker_HappyPath(t *testing.T) {
	client, h, mock := setupPlaylistLinkHandler(t, false)
	u := createLinkTestUser(t, client)

	mock.playlists = []providers.Playlist{
		{ID: "nav-a", Name: "Morning Coffee", TrackCount: 12},
		{ID: "nav-b", Name: "Late Night", TrackCount: 30},
	}

	pl, err := client.Playlist.Create().
		SetUser(u).
		SetRemoteID("spotify-picker-happy").
		SetName("Pick For Me").
		SetSource("spotify").
		Save(context.Background())
	require.NoError(t, err)

	req := newPlaylistRequest("GET", "/playlists/"+strconv.Itoa(pl.ID)+"/link", "", u,
		map[string]string{"id": strconv.Itoa(pl.ID)})
	w := httptest.NewRecorder()

	h.LinkNavidromePicker(w, req)

	resp := w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)
	assert.Contains(t, bodyStr, `id="playlist-link-picker"`)
	assert.Contains(t, bodyStr, "Morning Coffee")
	assert.Contains(t, bodyStr, "Late Night")
	// Candidate buttons post to the link endpoint
	assert.Contains(t, bodyStr, "/playlists/"+strconv.Itoa(pl.ID)+"/link/nav-a")
	assert.Contains(t, bodyStr, "/playlists/"+strconv.Itoa(pl.ID)+"/link/nav-b")
}

func TestLinkNavidromePicker_ProviderError(t *testing.T) {
	client, h, mock := setupPlaylistLinkHandler(t, false)
	u := createLinkTestUser(t, client)

	mock.getPlaylistsErr = assert.AnError

	pl, err := client.Playlist.Create().
		SetUser(u).
		SetRemoteID("spotify-picker-err").
		SetName("Pick For Me").
		SetSource("spotify").
		Save(context.Background())
	require.NoError(t, err)

	req := newPlaylistRequest("GET", "/playlists/"+strconv.Itoa(pl.ID)+"/link", "", u,
		map[string]string{"id": strconv.Itoa(pl.ID)})
	w := httptest.NewRecorder()

	h.LinkNavidromePicker(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Result().StatusCode)
}

// ---------------------------------------------------------------------------
// LinkWithNavidrome (POST /playlists/{id}/link/{targetId})
// ---------------------------------------------------------------------------

func TestLinkWithNavidrome_Unauthorized(t *testing.T) {
	_, h, _ := setupPlaylistLinkHandler(t, false)

	req := newPlaylistRequest("POST", "/playlists/1/link/nav-1", "", nil,
		map[string]string{"id": "1", "targetId": "nav-1"})
	w := httptest.NewRecorder()

	h.LinkWithNavidrome(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Result().StatusCode)
}

func TestLinkWithNavidrome_NotFound(t *testing.T) {
	client, h, _ := setupPlaylistLinkHandler(t, false)
	u := createLinkTestUser(t, client)

	req := newPlaylistRequest("POST", "/playlists/99999/link/nav-1", "", u,
		map[string]string{"id": "99999", "targetId": "nav-1"})
	w := httptest.NewRecorder()

	h.LinkWithNavidrome(w, req)

	assert.Equal(t, http.StatusNotFound, w.Result().StatusCode)
}

func TestLinkWithNavidrome_WrongSource(t *testing.T) {
	client, h, _ := setupPlaylistLinkHandler(t, false)
	u := createLinkTestUser(t, client)

	pl, err := client.Playlist.Create().
		SetUser(u).
		SetRemoteID("nav-native-target").
		SetName("Native Playlist").
		SetSource("navidrome").
		Save(context.Background())
	require.NoError(t, err)

	req := newPlaylistRequest("POST", "/playlists/"+strconv.Itoa(pl.ID)+"/link/nav-1", "", u,
		map[string]string{"id": strconv.Itoa(pl.ID), "targetId": "nav-1"})
	w := httptest.NewRecorder()

	h.LinkWithNavidrome(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Result().StatusCode)
}

func TestLinkWithNavidrome_MissingTargetID(t *testing.T) {
	client, h, _ := setupPlaylistLinkHandler(t, false)
	u := createLinkTestUser(t, client)

	pl, err := client.Playlist.Create().
		SetUser(u).
		SetRemoteID("spotify-no-target").
		SetName("No Target").
		SetSource("spotify").
		Save(context.Background())
	require.NoError(t, err)

	req := newPlaylistRequest("POST", "/playlists/"+strconv.Itoa(pl.ID)+"/link/", "", u,
		map[string]string{"id": strconv.Itoa(pl.ID)})
	w := httptest.NewRecorder()

	h.LinkWithNavidrome(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Result().StatusCode)
}

// Governing: SPEC playlist-sync-navidrome REQ-PLSYNC-072 (arbitrary-target linking)
func TestLinkWithNavidrome_HappyPath(t *testing.T) {
	client, h, mock := setupPlaylistLinkHandler(t, false)
	u := createLinkTestUser(t, client)

	// Sync disabled and target name completely unrelated: linking must work
	// for ANY Navidrome playlist, not just same-name conflicts.
	pl, err := client.Playlist.Create().
		SetUser(u).
		SetRemoteID("spotify-link-happy").
		SetName("My Spotify Mix").
		SetSource("spotify").
		SetSyncToNavidrome(false).
		Save(context.Background())
	require.NoError(t, err)

	// Subscribe before invoking the handler so the async completion event
	// cannot be missed.
	notifications, unsubscribe := h.Bus.Subscribe(u.ID)
	defer unsubscribe()

	req := newPlaylistRequest("POST", "/playlists/"+strconv.Itoa(pl.ID)+"/link/nav-arbitrary-99", "", u,
		map[string]string{"id": strconv.Itoa(pl.ID), "targetId": "nav-arbitrary-99"})
	w := httptest.NewRecorder()

	h.LinkWithNavidrome(w, req)

	resp := w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// HTMX response: refreshed sync dropdown
	body, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(body), `id="playlist-sync-dropdown"`)

	// Pairing runs async: the "Playlist Synced" notification is published
	// after the final DB write, so state below is settled once it arrives.
	awaitNotification(t, notifications, "Playlist Synced")

	updatedPl, err := client.Playlist.Get(context.Background(), pl.ID)
	require.NoError(t, err)
	assert.True(t, updatedPl.SyncToNavidrome)
	assert.Equal(t, "nav-arbitrary-99", updatedPl.NavidromePlaylistID,
		"linking must pair with the chosen Navidrome playlist")
	assert.NotNil(t, updatedPl.LastSyncedAt, "linking must trigger an immediate sync")
	assert.Equal(t, "nav-arbitrary-99", mock.UpdatedID(), "immediate sync must UPDATE the linked playlist")
}
