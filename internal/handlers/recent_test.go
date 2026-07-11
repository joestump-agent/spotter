package handlers_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
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

// recentTestUserCounter ensures unique usernames across tests
var recentTestUserCounter int64

func uniqueRecentTestUsername() string {
	return fmt.Sprintf("recenttestuser_%d", atomic.AddInt64(&recentTestUserCounter, 1))
}

func setupRecentHandler(t *testing.T) (*ent.Client, *handlers.Handler) {
	client := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{}
	bus := events.NewBus()
	syncer := services.NewSyncer(client, cfg, logger, bus, nil)
	encryptor, _ := crypto.NewEncryptor(make([]byte, 32))
	jwtManager := auth.NewJWTManager(testJWTSecret)
	h := handlers.New(client, cfg, logger, encryptor, jwtManager, syncer, nil, nil, nil, nil, nil, bus, nil)
	return client, h
}

func createRecentTestUser(t *testing.T, client *ent.Client, paginationSize int) *ent.User {
	u, err := client.User.Create().
		SetUsername(uniqueRecentTestUsername()).
		SetPaginationSize(paginationSize).
		Save(context.Background())
	require.NoError(t, err)
	return u
}

func createRecentTestListen(t *testing.T, client *ent.Client, u *ent.User, trackName, artistName, albumName, source string, playedAt time.Time) {
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

func TestRecentListens_Unauthorized(t *testing.T) {
	_, h := setupRecentHandler(t)

	req := httptest.NewRequest("GET", "/recent", nil)
	w := httptest.NewRecorder()

	h.RecentListens(w, req)

	resp := w.Result()
	assert.Equal(t, http.StatusSeeOther, resp.StatusCode)
	assert.Equal(t, "/auth/login", resp.Header.Get("Location"))
}

func TestRecentListens_EmptyState(t *testing.T) {
	client, h := setupRecentHandler(t)
	u := createRecentTestUser(t, client, 25)

	req := httptest.NewRequest("GET", "/recent", nil)
	req = req.WithContext(context.WithValue(req.Context(), handlers.UserContextKey, u))
	w := httptest.NewRecorder()

	h.RecentListens(w, req)

	resp := w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(body), "No listens")
}

func TestRecentListens_WithData(t *testing.T) {
	client, h := setupRecentHandler(t)
	u := createRecentTestUser(t, client, 25)

	now := time.Now()
	createRecentTestListen(t, client, u, "Alpha Track", "Alpha Artist", "Alpha Album", "navidrome", now.Add(-1*time.Hour))
	createRecentTestListen(t, client, u, "Beta Track", "Beta Artist", "Beta Album", "spotify", now.Add(-2*time.Hour))

	req := httptest.NewRequest("GET", "/recent", nil)
	req = req.WithContext(context.WithValue(req.Context(), handlers.UserContextKey, u))
	w := httptest.NewRecorder()

	h.RecentListens(w, req)

	resp := w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)
	assert.Contains(t, bodyStr, "Alpha Track")
	assert.Contains(t, bodyStr, "Beta Track")
}

func TestRecentListens_ArtistFilter(t *testing.T) {
	client, h := setupRecentHandler(t)
	u := createRecentTestUser(t, client, 25)

	now := time.Now()
	createRecentTestListen(t, client, u, "Kept Track", "Filter Artist", "Album A", "navidrome", now.Add(-1*time.Hour))
	createRecentTestListen(t, client, u, "Dropped Track", "Other Artist", "Album B", "navidrome", now.Add(-2*time.Hour))

	req := httptest.NewRequest("GET", "/recent?artist=Filter+Artist", nil)
	req = req.WithContext(context.WithValue(req.Context(), handlers.UserContextKey, u))
	w := httptest.NewRecorder()

	h.RecentListens(w, req)

	resp := w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)
	assert.Contains(t, bodyStr, "Kept Track")
	assert.NotContains(t, bodyStr, "Dropped Track")
}

func TestRecentListens_SortAscByTrack(t *testing.T) {
	client, h := setupRecentHandler(t)
	u := createRecentTestUser(t, client, 25)

	now := time.Now()
	createRecentTestListen(t, client, u, "Zed Track", "Artist", "Album", "navidrome", now.Add(-1*time.Hour))
	createRecentTestListen(t, client, u, "Alpha Track", "Artist", "Album", "navidrome", now.Add(-2*time.Hour))

	req := httptest.NewRequest("GET", "/recent?sort=track&dir=asc", nil)
	req = req.WithContext(context.WithValue(req.Context(), handlers.UserContextKey, u))
	w := httptest.NewRecorder()

	h.RecentListens(w, req)

	resp := w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)
	// Ascending sort puts Alpha before Zed in the rendered table.
	alphaIdx := strings.Index(bodyStr, "Alpha Track")
	zedIdx := strings.Index(bodyStr, "Zed Track")
	require.GreaterOrEqual(t, alphaIdx, 0)
	require.GreaterOrEqual(t, zedIdx, 0)
	assert.Less(t, alphaIdx, zedIdx)
}

func TestRecentListens_UnknownSortFallsBackToPlayedAt(t *testing.T) {
	client, h := setupRecentHandler(t)
	u := createRecentTestUser(t, client, 25)

	now := time.Now()
	createRecentTestListen(t, client, u, "Newest Track", "Artist", "Album", "navidrome", now.Add(-1*time.Hour))
	createRecentTestListen(t, client, u, "Oldest Track", "Artist", "Album", "navidrome", now.Add(-48*time.Hour))

	req := httptest.NewRequest("GET", "/recent?sort=bogus&dir=desc", nil)
	req = req.WithContext(context.WithValue(req.Context(), handlers.UserContextKey, u))
	w := httptest.NewRecorder()

	h.RecentListens(w, req)

	resp := w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)
	newestIdx := strings.Index(bodyStr, "Newest Track")
	oldestIdx := strings.Index(bodyStr, "Oldest Track")
	require.GreaterOrEqual(t, newestIdx, 0)
	require.GreaterOrEqual(t, oldestIdx, 0)
	assert.Less(t, newestIdx, oldestIdx, "default order is most recent first")
}

func TestRecentListens_Pagination(t *testing.T) {
	client, h := setupRecentHandler(t)
	u := createRecentTestUser(t, client, 2)

	now := time.Now()
	for i := 0; i < 5; i++ {
		createRecentTestListen(t, client, u,
			fmt.Sprintf("Paged Track %d", i), "Artist", "Album", "navidrome",
			now.Add(-time.Duration(i)*time.Hour))
	}

	req := httptest.NewRequest("GET", "/recent?page=2", nil)
	req = req.WithContext(context.WithValue(req.Context(), handlers.UserContextKey, u))
	w := httptest.NewRecorder()

	h.RecentListens(w, req)

	resp := w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)
	// Page 2 with page size 2, ordered newest first: tracks 2 and 3.
	assert.Contains(t, bodyStr, "Paged Track 2")
	assert.Contains(t, bodyStr, "Paged Track 3")
	assert.NotContains(t, bodyStr, "Paged Track 0")
}

func TestRefreshRecentListens_Unauthorized(t *testing.T) {
	_, h := setupRecentHandler(t)

	req := httptest.NewRequest("POST", "/recent/refresh", nil)
	w := httptest.NewRecorder()

	h.RefreshRecentListens(w, req)

	resp := w.Result()
	assert.Equal(t, http.StatusSeeOther, resp.StatusCode)
	assert.Equal(t, "/auth/login", resp.Header.Get("Location"))
}

func TestRefreshRecentListens_RedirectsToRecent(t *testing.T) {
	client, h := setupRecentHandler(t)
	u := createRecentTestUser(t, client, 25)

	req := httptest.NewRequest("POST", "/recent/refresh", nil)
	req = req.WithContext(context.WithValue(req.Context(), handlers.UserContextKey, u))
	w := httptest.NewRecorder()

	h.RefreshRecentListens(w, req)

	// Only the synchronous response is asserted; the background sync has no
	// deterministic completion signal.
	assert.Equal(t, "/recent", w.Header().Get("HX-Redirect"))
}
