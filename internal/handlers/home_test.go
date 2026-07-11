package handlers_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"spotter/ent"
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

// homeTestUserCounter ensures unique usernames across tests
var homeTestUserCounter int64

func uniqueHomeTestUsername() string {
	return fmt.Sprintf("hometestuser_%d", atomic.AddInt64(&homeTestUserCounter, 1))
}

func setupHomeHandler(t *testing.T, cfg *config.Config) (*ent.Client, *handlers.Handler) {
	client := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if cfg == nil {
		cfg = &config.Config{}
	}
	bus := events.NewBus()
	syncer := services.NewSyncer(client, cfg, logger, bus, nil)
	encryptor, _ := crypto.NewEncryptor(make([]byte, 32))
	jwtManager := auth.NewJWTManager(testJWTSecret)
	h := handlers.New(client, cfg, logger, encryptor, jwtManager, syncer, nil, nil, nil, nil, nil, bus, nil)
	return client, h
}

func createHomeTestUser(t *testing.T, client *ent.Client) *ent.User {
	u, err := client.User.Create().
		SetUsername(uniqueHomeTestUsername()).
		SetPaginationSize(25).
		Save(context.Background())
	require.NoError(t, err)
	return u
}

func createHomeTestListen(t *testing.T, client *ent.Client, u *ent.User, trackName, artistName, albumName, source string, playedAt time.Time) {
	_, err := client.Listen.Create().
		SetUser(u).
		SetTrackName(trackName).
		SetArtistName(artistName).
		SetAlbumName(albumName).
		SetSource(source).
		SetPlayedAt(playedAt).
		Save(context.Background())
	require.NoError(t, err)
}

func TestHome_Unauthorized(t *testing.T) {
	_, h := setupHomeHandler(t, nil)

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()

	h.Home(w, req)

	resp := w.Result()
	assert.Equal(t, http.StatusSeeOther, resp.StatusCode)
	assert.Equal(t, "/auth/login", resp.Header.Get("Location"))
}

func TestHome_EmptyStats(t *testing.T) {
	client, h := setupHomeHandler(t, nil)
	u := createHomeTestUser(t, client)

	req := httptest.NewRequest("GET", "/", nil)
	req = req.WithContext(context.WithValue(req.Context(), handlers.UserContextKey, u))
	w := httptest.NewRecorder()

	h.Home(w, req)

	resp := w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)
	assert.Contains(t, bodyStr, "Total Listens")
	assert.Contains(t, bodyStr, u.Username)
}

func TestHome_WithListensAndProviders(t *testing.T) {
	// Mock Navidrome server so checkNavidromeOnline gets a 200 ping.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/rest/ping.view", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"subsonic-response":{"status":"ok"}}`))
	}))
	defer ts.Close()

	cfg := &config.Config{}
	cfg.Navidrome.BaseURL = ts.URL
	client, h := setupHomeHandler(t, cfg)
	u := createHomeTestUser(t, client)
	ctx := context.Background()

	// Connect all three providers.
	_, err := client.NavidromeAuth.Create().
		SetUser(u).
		SetPassword("secret").
		SetLastSyncedAt(time.Now()).
		Save(ctx)
	require.NoError(t, err)

	_, err = client.SpotifyAuth.Create().
		SetUser(u).
		SetAccessToken("token").
		SetRefreshToken("refresh").
		SetExpiry(time.Now().Add(time.Hour)).
		SetDisplayName("Spotify User").
		Save(ctx)
	require.NoError(t, err)

	_, err = client.LastFMAuth.Create().
		SetUser(u).
		SetSessionKey("key").
		SetUsername("lastfmuser").
		Save(ctx)
	require.NoError(t, err)

	// Listens across all sources; "Top Artist" has the most plays.
	now := time.Now()
	createHomeTestListen(t, client, u, "Track One", "Top Artist", "Album A", "navidrome", now.Add(-1*time.Hour))
	createHomeTestListen(t, client, u, "Track One", "Top Artist", "Album A", "navidrome", now.Add(-2*time.Hour))
	createHomeTestListen(t, client, u, "Track Two", "Top Artist", "Album A", "spotify", now.Add(-3*time.Hour))
	createHomeTestListen(t, client, u, "Track Three", "Other Artist", "Album B", "lastfm", now.Add(-4*time.Hour))

	// Enriched catalog entry matching the top artist so TopArtists is populated.
	_, err = client.Artist.Create().
		SetName("Top Artist").
		SetUser(u).
		Save(ctx)
	require.NoError(t, err)

	// Playlist tracks supplement the unique artist/album/track stats.
	pl, err := client.Playlist.Create().
		SetUser(u).
		SetRemoteID("home-pl-1").
		SetName("Home Playlist").
		SetSource("navidrome").
		Save(ctx)
	require.NoError(t, err)
	_, err = client.PlaylistTrack.Create().
		SetPlaylist(pl).
		SetTrackName("Playlist Track").
		SetArtistName("Playlist Artist").
		SetAlbumName("Playlist Album").
		Save(ctx)
	require.NoError(t, err)

	req := httptest.NewRequest("GET", "/", nil)
	req = req.WithContext(context.WithValue(req.Context(), handlers.UserContextKey, u))
	w := httptest.NewRecorder()

	h.Home(w, req)

	resp := w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)

	assert.Contains(t, bodyStr, "Total Listens")
	assert.Contains(t, bodyStr, "Top Artist", "enriched top artist should be rendered")
	assert.Contains(t, bodyStr, "Spotify User", "spotify display name should be rendered")
	assert.Contains(t, bodyStr, "lastfmuser", "last.fm username should be rendered")
}

func TestHome_NavidromeOfflineWhenNoBaseURL(t *testing.T) {
	// No Navidrome BaseURL configured: checkNavidromeOnline must short-circuit
	// to offline without making any network calls.
	client, h := setupHomeHandler(t, nil)
	u := createHomeTestUser(t, client)

	_, err := client.NavidromeAuth.Create().
		SetUser(u).
		SetPassword("secret").
		Save(context.Background())
	require.NoError(t, err)

	req := httptest.NewRequest("GET", "/", nil)
	req = req.WithContext(context.WithValue(req.Context(), handlers.UserContextKey, u))
	w := httptest.NewRecorder()

	h.Home(w, req)

	assert.Equal(t, http.StatusOK, w.Result().StatusCode)
}

func TestHome_NavidromePingFailure(t *testing.T) {
	// Server returns 500: checkNavidromeOnline must report offline but the
	// page still renders.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	cfg := &config.Config{}
	cfg.Navidrome.BaseURL = ts.URL
	client, h := setupHomeHandler(t, cfg)
	u := createHomeTestUser(t, client)

	_, err := client.NavidromeAuth.Create().
		SetUser(u).
		SetPassword("secret").
		Save(context.Background())
	require.NoError(t, err)

	req := httptest.NewRequest("GET", "/", nil)
	req = req.WithContext(context.WithValue(req.Context(), handlers.UserContextKey, u))
	w := httptest.NewRecorder()

	h.Home(w, req)

	assert.Equal(t, http.StatusOK, w.Result().StatusCode)
}

func TestHome_StatsErrorFallsBackToEmptyStats(t *testing.T) {
	// If getHomeStats fails (user row gone), Home must render fallback stats
	// instead of an error page.
	client, h := setupHomeHandler(t, nil)
	u := createHomeTestUser(t, client)

	// Delete the user row while keeping the stale pointer in context.
	require.NoError(t, client.User.DeleteOneID(u.ID).Exec(context.Background()))

	req := httptest.NewRequest("GET", "/", nil)
	req = req.WithContext(context.WithValue(req.Context(), handlers.UserContextKey, u))
	w := httptest.NewRecorder()

	h.Home(w, req)

	resp := w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(body), u.Username)
}
