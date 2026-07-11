package handlers_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"spotter/ent"
	"spotter/ent/album"
	"spotter/ent/artist"
	"spotter/ent/listen"
	"spotter/ent/playlist"
	"spotter/ent/syncevent"
	"spotter/ent/track"
	"spotter/ent/user"
	"spotter/internal/auth"
	"spotter/internal/config"
	"spotter/internal/crypto"
	"spotter/internal/events"
	"spotter/internal/handlers"
	"spotter/internal/services"

	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// prefsTestUserCounter ensures unique usernames across tests
var prefsTestUserCounter int64

func uniquePrefsTestUsername() string {
	return fmt.Sprintf("prefstestuser_%d", atomic.AddInt64(&prefsTestUserCounter, 1))
}

// stubNotifier is a test double for services.SyncNotifier.
type stubNotifier struct {
	sendTestErr   error
	sendTestCalls atomic.Int64
}

func (s *stubNotifier) NotifyIfNeeded(_ context.Context, _ *ent.User, _ string, _ error) error {
	return nil
}

func (s *stubNotifier) ClearCooldown(_ context.Context, _ int, _ string) error {
	return nil
}

func (s *stubNotifier) SendTest(_ context.Context, _ *ent.User) error {
	s.sendTestCalls.Add(1)
	return s.sendTestErr
}

func setupPrefsHandler(t *testing.T) (*ent.Client, *handlers.Handler) {
	client := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{}
	cfg.Theme.Available = "dark,light"
	cfg.Theme.Default = "dark"
	bus := events.NewBus()
	syncer := services.NewSyncer(client, cfg, logger, bus, nil)
	encryptor, _ := crypto.NewEncryptor(make([]byte, 32))
	jwtManager := auth.NewJWTManager(testJWTSecret)
	h := handlers.New(client, cfg, logger, encryptor, jwtManager, syncer, nil, nil, nil, nil, nil, bus, nil)
	return client, h
}

func createPrefsTestUser(t *testing.T, client *ent.Client) *ent.User {
	u, err := client.User.Create().
		SetUsername(uniquePrefsTestUsername()).
		SetPaginationSize(25).
		Save(context.Background())
	require.NoError(t, err)
	return u
}

func prefsGet(target string, u *ent.User) *http.Request {
	req := httptest.NewRequest("GET", target, nil)
	if u != nil {
		req = req.WithContext(context.WithValue(req.Context(), handlers.UserContextKey, u))
	}
	return req
}

func prefsPostForm(target string, u *ent.User, form url.Values) *http.Request {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req := httptest.NewRequest("POST", target, body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if u != nil {
		req = req.WithContext(context.WithValue(req.Context(), handlers.UserContextKey, u))
	}
	return req
}

func TestPreferencesRedirect(t *testing.T) {
	_, h := setupPrefsHandler(t)

	req := httptest.NewRequest("GET", "/preferences", nil)
	w := httptest.NewRecorder()

	h.PreferencesRedirect(w, req)

	resp := w.Result()
	assert.Equal(t, http.StatusSeeOther, resp.StatusCode)
	assert.Equal(t, "/preferences/appearance", resp.Header.Get("Location"))
}

func TestPreferencesAppearance_Unauthorized(t *testing.T) {
	_, h := setupPrefsHandler(t)

	w := httptest.NewRecorder()
	h.PreferencesAppearance(w, prefsGet("/preferences/appearance", nil))

	resp := w.Result()
	assert.Equal(t, http.StatusSeeOther, resp.StatusCode)
	assert.Equal(t, "/auth/login", resp.Header.Get("Location"))
}

func TestPreferencesAppearance_Success(t *testing.T) {
	client, h := setupPrefsHandler(t)
	u := createPrefsTestUser(t, client)

	w := httptest.NewRecorder()
	h.PreferencesAppearance(w, prefsGet("/preferences/appearance", u))

	resp := w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(body), "Theme")
}

func TestPostPreferencesAppearance_Unauthorized(t *testing.T) {
	_, h := setupPrefsHandler(t)

	w := httptest.NewRecorder()
	h.PostPreferencesAppearance(w, prefsPostForm("/preferences/appearance", nil, nil))

	assert.Equal(t, http.StatusSeeOther, w.Result().StatusCode)
}

func TestPostPreferencesAppearance_SavesThemeAndPagination(t *testing.T) {
	client, h := setupPrefsHandler(t)
	u := createPrefsTestUser(t, client)

	form := url.Values{}
	form.Set("theme", "light")
	form.Set("pagination_size", "50")

	w := httptest.NewRecorder()
	h.PostPreferencesAppearance(w, prefsPostForm("/preferences/appearance", u, form))

	resp := w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "preferences-saved", resp.Header.Get("HX-Trigger"))

	updated, err := client.User.Get(context.Background(), u.ID)
	require.NoError(t, err)
	assert.Equal(t, "light", updated.Theme)
	assert.Equal(t, 50, updated.PaginationSize)
}

func TestPostPreferencesAppearance_InvalidThemeFallsBackToDefault(t *testing.T) {
	client, h := setupPrefsHandler(t)
	u := createPrefsTestUser(t, client)

	form := url.Values{}
	form.Set("theme", "not-a-theme")

	w := httptest.NewRecorder()
	h.PostPreferencesAppearance(w, prefsPostForm("/preferences/appearance", u, form))

	assert.Equal(t, http.StatusOK, w.Result().StatusCode)

	updated, err := client.User.Get(context.Background(), u.ID)
	require.NoError(t, err)
	assert.Equal(t, "dark", updated.Theme, "invalid theme must fall back to configured default")
}

func TestPostPreferencesAppearance_InvalidPaginationSizeIgnored(t *testing.T) {
	client, h := setupPrefsHandler(t)
	u := createPrefsTestUser(t, client)

	for _, badSize := range []string{"5", "1000", "abc"} {
		form := url.Values{}
		form.Set("theme", "dark")
		form.Set("pagination_size", badSize)

		w := httptest.NewRecorder()
		h.PostPreferencesAppearance(w, prefsPostForm("/preferences/appearance", u, form))
		assert.Equal(t, http.StatusOK, w.Result().StatusCode)

		updated, err := client.User.Get(context.Background(), u.ID)
		require.NoError(t, err)
		assert.Equal(t, 25, updated.PaginationSize, "pagination size %q must be rejected", badSize)
	}
}

func TestPreferencesAccount_Success(t *testing.T) {
	client, h := setupPrefsHandler(t)
	u := createPrefsTestUser(t, client)

	w := httptest.NewRecorder()
	h.PreferencesAccount(w, prefsGet("/preferences/account", u))

	resp := w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(body), "Email")
}

func TestPostPreferencesEmail_InvalidEmail(t *testing.T) {
	client, h := setupPrefsHandler(t)
	u := createPrefsTestUser(t, client)

	form := url.Values{}
	form.Set("email", "not-an-email")

	w := httptest.NewRecorder()
	h.PostPreferencesEmail(w, prefsPostForm("/preferences/account/email", u, form))

	resp := w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(body), "valid email address")

	updated, err := client.User.Get(context.Background(), u.ID)
	require.NoError(t, err)
	assert.Empty(t, updated.Email, "invalid email must not be persisted")
}

func TestPostPreferencesEmail_SavesValidEmail(t *testing.T) {
	client, h := setupPrefsHandler(t)
	u := createPrefsTestUser(t, client)

	form := url.Values{}
	form.Set("email", "user@example.com")

	w := httptest.NewRecorder()
	h.PostPreferencesEmail(w, prefsPostForm("/preferences/account/email", u, form))

	resp := w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(body), "Email address updated")

	updated, err := client.User.Get(context.Background(), u.ID)
	require.NoError(t, err)
	assert.Equal(t, "user@example.com", updated.Email)
}

func TestPostPreferencesEmail_ClearsEmail(t *testing.T) {
	client, h := setupPrefsHandler(t)
	u := createPrefsTestUser(t, client)
	require.NoError(t, client.User.UpdateOneID(u.ID).SetEmail("old@example.com").Exec(context.Background()))

	form := url.Values{}
	form.Set("email", "")

	w := httptest.NewRecorder()
	h.PostPreferencesEmail(w, prefsPostForm("/preferences/account/email", u, form))

	assert.Equal(t, http.StatusOK, w.Result().StatusCode)

	updated, err := client.User.Get(context.Background(), u.ID)
	require.NoError(t, err)
	assert.Empty(t, updated.Email)
}

func TestPostTestNotification_NoNotifierConfigured(t *testing.T) {
	client, h := setupPrefsHandler(t)
	u := createPrefsTestUser(t, client)

	w := httptest.NewRecorder()
	h.PostTestNotification(w, prefsPostForm("/preferences/account/test-notification", u, nil))

	resp := w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(body), "Notification service is not available")
}

func TestPostTestNotification_Success(t *testing.T) {
	client, h := setupPrefsHandler(t)
	u := createPrefsTestUser(t, client)
	require.NoError(t, client.User.UpdateOneID(u.ID).SetEmail("notify@example.com").Exec(context.Background()))
	u.Email = "notify@example.com"

	notifier := &stubNotifier{}
	h.Notifier = notifier

	w := httptest.NewRecorder()
	h.PostTestNotification(w, prefsPostForm("/preferences/account/test-notification", u, nil))

	resp := w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(body), "Test notification sent to notify@example.com")
	assert.Equal(t, int64(1), notifier.sendTestCalls.Load())
}

func TestPostTestNotification_SendFailure(t *testing.T) {
	client, h := setupPrefsHandler(t)
	u := createPrefsTestUser(t, client)

	h.Notifier = &stubNotifier{sendTestErr: errors.New("smtp is down")}

	w := httptest.NewRecorder()
	h.PostTestNotification(w, prefsPostForm("/preferences/account/test-notification", u, nil))

	resp := w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(body), "Failed to send test email")
}

func TestPreferencesProviders_Unauthorized(t *testing.T) {
	_, h := setupPrefsHandler(t)

	w := httptest.NewRecorder()
	h.PreferencesProviders(w, prefsGet("/preferences/providers", nil))

	resp := w.Result()
	assert.Equal(t, http.StatusSeeOther, resp.StatusCode)
	assert.Equal(t, "/auth/login", resp.Header.Get("Location"))
}

func TestPreferencesProviders_Success(t *testing.T) {
	client, h := setupPrefsHandler(t)
	u := createPrefsTestUser(t, client)

	// Connect one provider so both connected and disconnected states render.
	_, err := client.SpotifyAuth.Create().
		SetUser(u).
		SetAccessToken("token").
		SetRefreshToken("refresh").
		SetExpiry(time.Now().Add(time.Hour)).
		Save(context.Background())
	require.NoError(t, err)

	w := httptest.NewRecorder()
	h.PreferencesProviders(w, prefsGet("/preferences/providers", u))

	resp := w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(body), "Providers")
}

func TestDisconnectSpotify_RemovesAuth(t *testing.T) {
	client, h := setupPrefsHandler(t)
	u := createPrefsTestUser(t, client)

	_, err := client.SpotifyAuth.Create().
		SetUser(u).
		SetAccessToken("token").
		SetRefreshToken("refresh").
		SetExpiry(time.Now().Add(time.Hour)).
		Save(context.Background())
	require.NoError(t, err)

	w := httptest.NewRecorder()
	h.DisconnectSpotify(w, prefsPostForm("/preferences/providers/spotify/disconnect", u, nil))

	assert.Equal(t, "/preferences/providers", w.Header().Get("HX-Redirect"))

	refreshed, err := client.User.Query().
		Where(user.ID(u.ID)).
		WithSpotifyAuth().
		Only(context.Background())
	require.NoError(t, err)
	assert.Nil(t, refreshed.Edges.SpotifyAuth, "spotify auth must be deleted")
}

func TestDisconnectSpotify_NoAuthIsNoop(t *testing.T) {
	client, h := setupPrefsHandler(t)
	u := createPrefsTestUser(t, client)

	w := httptest.NewRecorder()
	h.DisconnectSpotify(w, prefsPostForm("/preferences/providers/spotify/disconnect", u, nil))

	assert.Equal(t, "/preferences/providers", w.Header().Get("HX-Redirect"))
}

func TestDisconnectLastFM_RemovesAuth(t *testing.T) {
	client, h := setupPrefsHandler(t)
	u := createPrefsTestUser(t, client)

	_, err := client.LastFMAuth.Create().
		SetUser(u).
		SetSessionKey("key").
		SetUsername("lastfmuser").
		Save(context.Background())
	require.NoError(t, err)

	w := httptest.NewRecorder()
	h.DisconnectLastFM(w, prefsPostForm("/preferences/providers/lastfm/disconnect", u, nil))

	assert.Equal(t, "/preferences/providers", w.Header().Get("HX-Redirect"))

	refreshed, err := client.User.Query().
		Where(user.ID(u.ID)).
		WithLastfmAuth().
		Only(context.Background())
	require.NoError(t, err)
	assert.Nil(t, refreshed.Edges.LastfmAuth, "lastfm auth must be deleted")
}

func TestDisconnectListenBrainz_RemovesAuth(t *testing.T) {
	client, h := setupPrefsHandler(t)
	u := createPrefsTestUser(t, client)

	_, err := client.ListenBrainzAuth.Create().
		SetUser(u).
		SetToken("token").
		SetUsername("lbuser").
		Save(context.Background())
	require.NoError(t, err)

	w := httptest.NewRecorder()
	h.DisconnectListenBrainz(w, prefsPostForm("/preferences/providers/listenbrainz/disconnect", u, nil))

	assert.Equal(t, "/preferences/providers", w.Header().Get("HX-Redirect"))

	refreshed, err := client.User.Query().
		Where(user.ID(u.ID)).
		WithListenbrainzAuth().
		Only(context.Background())
	require.NoError(t, err)
	assert.Nil(t, refreshed.Edges.ListenbrainzAuth, "listenbrainz auth must be deleted")
}

// Sync handlers return immediately with a toast; only the synchronous
// response is asserted because the background goroutine has no deterministic
// completion signal.
// Governing: SPEC listen-playlist-sync REQ-SYNC-051
func TestSyncProviderHandlers_ReturnToastImmediately(t *testing.T) {
	client, h := setupPrefsHandler(t)

	tests := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"navidrome", h.SyncNavidrome},
		{"spotify", h.SyncSpotify},
		{"lastfm", h.SyncLastFM},
		{"listenbrainz", h.SyncListenBrainz},
	}

	// Create all users up front: each handler spawns a background sync
	// goroutine whose user-table reads would otherwise contend with the next
	// subtest's user insert on the shared-cache SQLite database.
	users := make([]*ent.User, len(tests))
	for i := range tests {
		users[i] = createPrefsTestUser(t, client)
	}

	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			u := users[i]

			w := httptest.NewRecorder()
			tc.handler(w, prefsPostForm("/preferences/providers/"+tc.name+"/sync", u, nil))

			resp := w.Result()
			assert.Equal(t, http.StatusOK, resp.StatusCode)
			body, _ := io.ReadAll(resp.Body)
			assert.Contains(t, string(body), "Sync Started")
		})
	}
}

func TestSyncNavidrome_Unauthorized(t *testing.T) {
	_, h := setupPrefsHandler(t)

	w := httptest.NewRecorder()
	h.SyncNavidrome(w, prefsPostForm("/preferences/providers/navidrome/sync", nil, nil))

	assert.Equal(t, http.StatusSeeOther, w.Result().StatusCode)
}

func TestRebuildNavidrome_DeletesNavidromeData(t *testing.T) {
	client, h := setupPrefsHandler(t)
	u := createPrefsTestUser(t, client)
	ctx := context.Background()

	// Navidrome data that must be deleted.
	_, err := client.Listen.Create().
		SetUser(u).
		SetTrackName("ND Track").
		SetArtistName("ND Artist").
		SetAlbumName("ND Album").
		SetSource("navidrome").
		SetPlayedAt(time.Now()).
		Save(ctx)
	require.NoError(t, err)
	_, err = client.Playlist.Create().
		SetUser(u).
		SetRemoteID("nd-pl").
		SetName("ND Playlist").
		SetSource("navidrome").
		Save(ctx)
	require.NoError(t, err)

	// Spotify data that must survive.
	_, err = client.Listen.Create().
		SetUser(u).
		SetTrackName("SP Track").
		SetArtistName("SP Artist").
		SetAlbumName("SP Album").
		SetSource("spotify").
		SetPlayedAt(time.Now()).
		Save(ctx)
	require.NoError(t, err)

	w := httptest.NewRecorder()
	h.RebuildNavidrome(w, prefsPostForm("/preferences/providers/navidrome/rebuild", u, nil))

	resp := w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(body), "Rebuild Started")

	ndListens, err := client.Listen.Query().
		Where(listen.HasUserWith(user.ID(u.ID)), listen.Source("navidrome")).
		Count(ctx)
	require.NoError(t, err)
	assert.Zero(t, ndListens, "navidrome listens must be deleted")

	spListens, err := client.Listen.Query().
		Where(listen.HasUserWith(user.ID(u.ID)), listen.Source("spotify")).
		Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, spListens, "spotify listens must be preserved")

	ndPlaylists, err := client.Playlist.Query().
		Where(playlist.HasUserWith(user.ID(u.ID)), playlist.Source("navidrome")).
		Count(ctx)
	require.NoError(t, err)
	assert.Zero(t, ndPlaylists, "navidrome playlists must be deleted")
}

func TestRebuildSpotify_DeletesSpotifyData(t *testing.T) {
	client, h := setupPrefsHandler(t)
	u := createPrefsTestUser(t, client)
	ctx := context.Background()

	_, err := client.Listen.Create().
		SetUser(u).
		SetTrackName("SP Track").
		SetArtistName("SP Artist").
		SetAlbumName("SP Album").
		SetSource("spotify").
		SetPlayedAt(time.Now()).
		Save(ctx)
	require.NoError(t, err)
	_, err = client.Playlist.Create().
		SetUser(u).
		SetRemoteID("sp-pl").
		SetName("SP Playlist").
		SetSource("spotify").
		Save(ctx)
	require.NoError(t, err)

	w := httptest.NewRecorder()
	h.RebuildSpotify(w, prefsPostForm("/preferences/providers/spotify/rebuild", u, nil))

	resp := w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(body), "Rebuild Started")
	assert.Contains(t, string(body), "Deleted 1 listens and 1 playlists")

	count, err := client.Listen.Query().
		Where(listen.HasUserWith(user.ID(u.ID)), listen.Source("spotify")).
		Count(ctx)
	require.NoError(t, err)
	assert.Zero(t, count)
}

func TestRebuildLastFM_DeletesLastFMListens(t *testing.T) {
	client, h := setupPrefsHandler(t)
	u := createPrefsTestUser(t, client)
	ctx := context.Background()

	_, err := client.Listen.Create().
		SetUser(u).
		SetTrackName("LF Track").
		SetArtistName("LF Artist").
		SetAlbumName("LF Album").
		SetSource("lastfm").
		SetPlayedAt(time.Now()).
		Save(ctx)
	require.NoError(t, err)

	w := httptest.NewRecorder()
	h.RebuildLastFM(w, prefsPostForm("/preferences/providers/lastfm/rebuild", u, nil))

	resp := w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(body), "Rebuild Started")

	count, err := client.Listen.Query().
		Where(listen.HasUserWith(user.ID(u.ID)), listen.Source("lastfm")).
		Count(ctx)
	require.NoError(t, err)
	assert.Zero(t, count)
}

func TestRebuildListenBrainz_DeletesListenBrainzListens(t *testing.T) {
	client, h := setupPrefsHandler(t)
	u := createPrefsTestUser(t, client)
	ctx := context.Background()

	_, err := client.Listen.Create().
		SetUser(u).
		SetTrackName("LB Track").
		SetArtistName("LB Artist").
		SetAlbumName("LB Album").
		SetSource("listenbrainz").
		SetPlayedAt(time.Now()).
		Save(ctx)
	require.NoError(t, err)

	w := httptest.NewRecorder()
	h.RebuildListenBrainz(w, prefsPostForm("/preferences/providers/listenbrainz/rebuild", u, nil))

	resp := w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(body), "Rebuild Started")

	count, err := client.Listen.Query().
		Where(listen.HasUserWith(user.ID(u.ID)), listen.Source("listenbrainz")).
		Count(ctx)
	require.NoError(t, err)
	assert.Zero(t, count)
}

func createPrefsSyncEvent(t *testing.T, client *ent.Client, u *ent.User, eventType syncevent.EventType, provider, message string) {
	_, err := client.SyncEvent.Create().
		SetUser(u).
		SetEventType(eventType).
		SetProvider(provider).
		SetMessage(message).
		Save(context.Background())
	require.NoError(t, err)
}

func TestPreferencesTasks_Unauthorized(t *testing.T) {
	_, h := setupPrefsHandler(t)

	w := httptest.NewRecorder()
	h.PreferencesTasks(w, prefsGet("/preferences/tasks", nil))

	resp := w.Result()
	assert.Equal(t, http.StatusSeeOther, resp.StatusCode)
	assert.Equal(t, "/auth/login", resp.Header.Get("Location"))
}

func TestPreferencesTasks_WithEventHistory(t *testing.T) {
	client, h := setupPrefsHandler(t)
	u := createPrefsTestUser(t, client)

	// Populate one event per last-run lookup in getTasksWithLastRun.
	createPrefsSyncEvent(t, client, u, syncevent.EventTypeSyncCompleted, "navidrome", "Synced 10 listens")
	createPrefsSyncEvent(t, client, u, syncevent.EventTypeSyncCompleted, "spotify", "Synced 3 playlists")
	createPrefsSyncEvent(t, client, u, syncevent.EventTypeMetadataCompleted, "metadata", "Metadata enrichment complete")
	createPrefsSyncEvent(t, client, u, syncevent.EventTypeImageDownloaded, "metadata", "Downloaded artist image")
	createPrefsSyncEvent(t, client, u, syncevent.EventTypeImageDownloaded, "metadata", "Downloaded album image")
	createPrefsSyncEvent(t, client, u, syncevent.EventTypeDataReset, "system", "Data reset complete")
	createPrefsSyncEvent(t, client, u, syncevent.EventTypeCleanupCompleted, "system", "Cleanup complete")

	w := httptest.NewRecorder()
	h.PreferencesTasks(w, prefsGet("/preferences/tasks", u))

	resp := w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)
	assert.Contains(t, bodyStr, "Event History")
	assert.Contains(t, bodyStr, "Synced 10 listens")
	assert.Contains(t, bodyStr, "Cleanup complete")
}

func TestPreferencesTasks_FilterByProviderAndEventType(t *testing.T) {
	client, h := setupPrefsHandler(t)
	u := createPrefsTestUser(t, client)

	createPrefsSyncEvent(t, client, u, syncevent.EventTypeSyncCompleted, "navidrome", "NavidromeOnlyMarker")
	createPrefsSyncEvent(t, client, u, syncevent.EventTypeSyncFailed, "spotify", "SpotifyOnlyMarker")

	w := httptest.NewRecorder()
	h.PreferencesTasks(w, prefsGet("/preferences/tasks?provider=navidrome&event_type=sync_completed", u))

	resp := w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)
	assert.Contains(t, bodyStr, "NavidromeOnlyMarker")
	assert.NotContains(t, bodyStr, "SpotifyOnlyMarker")
}

func TestTaskSyncListens_ReturnsToast(t *testing.T) {
	client, h := setupPrefsHandler(t)
	u := createPrefsTestUser(t, client)

	w := httptest.NewRecorder()
	h.TaskSyncListens(w, prefsPostForm("/preferences/tasks/sync-listens", u, nil))

	resp := w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(body), "Task Started")
}

func TestTaskSyncPlaylists_ReturnsToast(t *testing.T) {
	client, h := setupPrefsHandler(t)
	u := createPrefsTestUser(t, client)

	w := httptest.NewRecorder()
	h.TaskSyncPlaylists(w, prefsPostForm("/preferences/tasks/sync-playlists", u, nil))

	resp := w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(body), "Task Started")
}

func TestTaskEnrichMetadata_NoMetadataService(t *testing.T) {
	client, h := setupPrefsHandler(t)
	u := createPrefsTestUser(t, client)

	w := httptest.NewRecorder()
	h.TaskEnrichMetadata(w, prefsPostForm("/preferences/tasks/enrich-metadata", u, nil))

	resp := w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(body), "Metadata service is not configured")
}

func TestTaskSyncArtistImages_NoMetadataService(t *testing.T) {
	client, h := setupPrefsHandler(t)
	u := createPrefsTestUser(t, client)

	w := httptest.NewRecorder()
	h.TaskSyncArtistImages(w, prefsPostForm("/preferences/tasks/sync-artist-images", u, nil))

	resp := w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(body), "Metadata service is not configured")
}

func TestTaskSyncAlbumImages_NoMetadataService(t *testing.T) {
	client, h := setupPrefsHandler(t)
	u := createPrefsTestUser(t, client)

	w := httptest.NewRecorder()
	h.TaskSyncAlbumImages(w, prefsPostForm("/preferences/tasks/sync-album-images", u, nil))

	resp := w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(body), "Metadata service is not configured")
}

// withMetadataService attaches a real MetadataService (empty enricher
// registry) so the non-nil service branches are exercised.
func withMetadataService(client *ent.Client, h *handlers.Handler) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h.MetadataSvc = services.NewMetadataService(client, nil, h.Config, logger, h.Bus)
}

func TestTaskEnrichMetadata_StartsTask(t *testing.T) {
	client, h := setupPrefsHandler(t)
	withMetadataService(client, h)
	u := createPrefsTestUser(t, client)

	w := httptest.NewRecorder()
	h.TaskEnrichMetadata(w, prefsPostForm("/preferences/tasks/enrich-metadata", u, nil))

	resp := w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(body), "Task Started")
}

func TestTaskSyncArtistImages_StartsTask(t *testing.T) {
	client, h := setupPrefsHandler(t)
	withMetadataService(client, h)
	u := createPrefsTestUser(t, client)

	w := httptest.NewRecorder()
	h.TaskSyncArtistImages(w, prefsPostForm("/preferences/tasks/sync-artist-images", u, nil))

	resp := w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(body), "Task Started")
}

func TestTaskSyncAlbumImages_StartsTask(t *testing.T) {
	client, h := setupPrefsHandler(t)
	withMetadataService(client, h)
	u := createPrefsTestUser(t, client)

	w := httptest.NewRecorder()
	h.TaskSyncAlbumImages(w, prefsPostForm("/preferences/tasks/sync-album-images", u, nil))

	resp := w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(body), "Task Started")
}

func TestTaskResetData_DeletesAllUserData(t *testing.T) {
	client, h := setupPrefsHandler(t)
	u := createPrefsTestUser(t, client)
	ctx := context.Background()

	// Seed listens, playlists, and a full catalog entry.
	_, err := client.Listen.Create().
		SetUser(u).
		SetTrackName("Reset Track").
		SetArtistName("Reset Artist").
		SetAlbumName("Reset Album").
		SetSource("navidrome").
		SetPlayedAt(time.Now()).
		Save(ctx)
	require.NoError(t, err)
	_, err = client.Playlist.Create().
		SetUser(u).
		SetRemoteID("reset-pl").
		SetName("Reset Playlist").
		SetSource("navidrome").
		Save(ctx)
	require.NoError(t, err)
	a, err := client.Artist.Create().
		SetName("Reset Artist").
		SetUser(u).
		Save(ctx)
	require.NoError(t, err)
	al, err := client.Album.Create().
		SetName("Reset Album").
		SetUser(u).
		SetArtist(a).
		Save(ctx)
	require.NoError(t, err)
	_, err = client.Track.Create().
		SetName("Reset Track").
		SetArtist(a).
		SetAlbum(al).
		Save(ctx)
	require.NoError(t, err)

	w := httptest.NewRecorder()
	h.TaskResetData(w, prefsPostForm("/preferences/tasks/reset-data", u, nil))

	resp := w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(body), "Data Reset")

	// All synchronously deleted entities must be gone.
	listenCount, err := client.Listen.Query().Where(listen.HasUserWith(user.ID(u.ID))).Count(ctx)
	require.NoError(t, err)
	assert.Zero(t, listenCount)

	playlistCount, err := client.Playlist.Query().Where(playlist.HasUserWith(user.ID(u.ID))).Count(ctx)
	require.NoError(t, err)
	assert.Zero(t, playlistCount)

	artistCount, err := client.Artist.Query().Where(artist.HasUserWith(user.ID(u.ID))).Count(ctx)
	require.NoError(t, err)
	assert.Zero(t, artistCount)

	albumCount, err := client.Album.Query().Where(album.HasUserWith(user.ID(u.ID))).Count(ctx)
	require.NoError(t, err)
	assert.Zero(t, albumCount)

	trackCount, err := client.Track.Query().Where(track.HasArtistWith(artist.HasUserWith(user.ID(u.ID)))).Count(ctx)
	require.NoError(t, err)
	assert.Zero(t, trackCount)

	// The handler logs a start and a completion DataReset event synchronously.
	resetEvents, err := client.SyncEvent.Query().
		Where(
			syncevent.HasUserWith(user.ID(u.ID)),
			syncevent.EventTypeEQ(syncevent.EventTypeDataReset),
		).Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, resetEvents)
}

func TestTaskCleanup_LogsStartEvent(t *testing.T) {
	client, h := setupPrefsHandler(t)
	u := createPrefsTestUser(t, client)
	ctx := context.Background()

	w := httptest.NewRecorder()
	h.TaskCleanup(w, prefsPostForm("/preferences/tasks/cleanup", u, nil))

	resp := w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(body), "Cleanup Started")

	// The CleanupStarted event is written synchronously before the handler returns.
	started, err := client.SyncEvent.Query().
		Where(
			syncevent.HasUserWith(user.ID(u.ID)),
			syncevent.EventTypeEQ(syncevent.EventTypeCleanupStarted),
		).Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, started)
}
