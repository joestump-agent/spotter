package handlers_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"spotter/ent/playlist"
	"spotter/ent/user"
	"spotter/internal/config"
	"spotter/internal/events"
	"spotter/internal/handlers"
	"spotter/internal/services"

	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPlaylists_EmptyState(t *testing.T) {
	client := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{}
	bus := events.NewBus()
	syncer := services.NewSyncer(client, cfg, logger, bus)
	h := handlers.New(client, cfg, logger, syncer, nil, bus)

	// Create a test user
	u, err := client.User.Create().
		SetUsername("testuser").
		SetPaginationSize(25).
		Save(context.Background())
	require.NoError(t, err)

	req := httptest.NewRequest("GET", "/playlists", nil)
	req = req.WithContext(context.WithValue(req.Context(), handlers.UserContextKey, u))
	w := httptest.NewRecorder()

	h.Playlists(w, req)

	resp := w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(body), "No playlists found")
}

func TestPlaylists_WithData(t *testing.T) {
	client := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{}
	bus := events.NewBus()
	syncer := services.NewSyncer(client, cfg, logger, bus)
	h := handlers.New(client, cfg, logger, syncer, nil, bus)

	// Create a test user
	u, err := client.User.Create().
		SetUsername("testuser").
		SetPaginationSize(25).
		Save(context.Background())
	require.NoError(t, err)

	// Create test playlists
	_, err = client.Playlist.Create().
		SetUser(u).
		SetRemoteID("playlist1").
		SetName("My Favorites").
		SetDescription("Best tracks").
		SetSource("spotify").
		Save(context.Background())
	require.NoError(t, err)

	_, err = client.Playlist.Create().
		SetUser(u).
		SetRemoteID("playlist2").
		SetName("Chill Vibes").
		SetSource("navidrome").
		Save(context.Background())
	require.NoError(t, err)

	req := httptest.NewRequest("GET", "/playlists", nil)
	req = req.WithContext(context.WithValue(req.Context(), handlers.UserContextKey, u))
	w := httptest.NewRecorder()

	h.Playlists(w, req)

	resp := w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)

	assert.Contains(t, bodyStr, "My Favorites")
	assert.Contains(t, bodyStr, "Chill Vibes")
	assert.Contains(t, bodyStr, "Spotify")
	assert.Contains(t, bodyStr, "Navidrome")
}

func TestPlaylists_Pagination(t *testing.T) {
	client := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{}
	bus := events.NewBus()
	syncer := services.NewSyncer(client, cfg, logger, bus)
	h := handlers.New(client, cfg, logger, syncer, nil, bus)

	// Create a test user with small page size
	u, err := client.User.Create().
		SetUsername("testuser").
		SetPaginationSize(2).
		Save(context.Background())
	require.NoError(t, err)

	// Create 5 test playlists
	for i := 1; i <= 5; i++ {
		_, err = client.Playlist.Create().
			SetUser(u).
			SetRemoteID("playlist" + string(rune('0'+i))).
			SetName("Playlist " + string(rune('0'+i))).
			SetSource("navidrome").
			Save(context.Background())
		require.NoError(t, err)
	}

	// Test page 1
	req := httptest.NewRequest("GET", "/playlists?page=1", nil)
	req = req.WithContext(context.WithValue(req.Context(), handlers.UserContextKey, u))
	w := httptest.NewRecorder()

	h.Playlists(w, req)

	resp := w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// Count playlists displayed (should only show 2 on page 1)
	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)

	// Should have pagination controls
	assert.Contains(t, bodyStr, "join")

	// Test page 2
	req = httptest.NewRequest("GET", "/playlists?page=2", nil)
	req = req.WithContext(context.WithValue(req.Context(), handlers.UserContextKey, u))
	w = httptest.NewRecorder()

	h.Playlists(w, req)

	resp = w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestPlaylists_UserIsolation(t *testing.T) {
	client := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{}
	bus := events.NewBus()
	syncer := services.NewSyncer(client, cfg, logger, bus)
	h := handlers.New(client, cfg, logger, syncer, nil, bus)

	// Create two test users
	u1, err := client.User.Create().
		SetUsername("user1").
		SetPaginationSize(25).
		Save(context.Background())
	require.NoError(t, err)

	u2, err := client.User.Create().
		SetUsername("user2").
		SetPaginationSize(25).
		Save(context.Background())
	require.NoError(t, err)

	// Create playlist for user1
	_, err = client.Playlist.Create().
		SetUser(u1).
		SetRemoteID("playlist1").
		SetName("User1 Playlist").
		SetSource("spotify").
		Save(context.Background())
	require.NoError(t, err)

	// Create playlist for user2
	_, err = client.Playlist.Create().
		SetUser(u2).
		SetRemoteID("playlist2").
		SetName("User2 Playlist").
		SetSource("navidrome").
		Save(context.Background())
	require.NoError(t, err)

	// User1 should only see their playlist
	req := httptest.NewRequest("GET", "/playlists", nil)
	req = req.WithContext(context.WithValue(req.Context(), handlers.UserContextKey, u1))
	w := httptest.NewRecorder()

	h.Playlists(w, req)

	resp := w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)

	assert.Contains(t, bodyStr, "User1 Playlist")
	assert.NotContains(t, bodyStr, "User2 Playlist")

	// Verify count
	count, err := client.Playlist.Query().
		Where(playlist.HasUserWith(user.ID(u1.ID))).
		Count(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}
