package handlers_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
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

	"github.com/go-chi/chi/v5"
	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// trackTestUserCounter ensures unique usernames across tests
var trackTestUserCounter int64

func uniqueTrackTestUsername() string {
	return fmt.Sprintf("tracktestuser_%d", atomic.AddInt64(&trackTestUserCounter, 1))
}

func setupTrackHandler(t *testing.T) (*ent.Client, *handlers.Handler) {
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

func createTrackTestUser(t *testing.T, client *ent.Client) *ent.User {
	u, err := client.User.Create().
		SetUsername(uniqueTrackTestUsername()).
		SetPaginationSize(25).
		Save(context.Background())
	require.NoError(t, err)
	return u
}

// createTrackFixture creates an artist, album, and track owned by the user.
func createTrackFixture(t *testing.T, client *ent.Client, u *ent.User, artistName, albumName, trackName string) (*ent.Artist, *ent.Album, *ent.Track) {
	ctx := context.Background()
	a, err := client.Artist.Create().
		SetName(artistName).
		SetUser(u).
		Save(ctx)
	require.NoError(t, err)

	al, err := client.Album.Create().
		SetName(albumName).
		SetUser(u).
		SetArtist(a).
		Save(ctx)
	require.NoError(t, err)

	tr, err := client.Track.Create().
		SetName(trackName).
		SetArtist(a).
		SetAlbum(al).
		Save(ctx)
	require.NoError(t, err)
	return a, al, tr
}

func trackRequest(method, target string, u *ent.User, id string) *http.Request {
	req := httptest.NewRequest(method, target, nil)
	if u != nil {
		req = req.WithContext(context.WithValue(req.Context(), handlers.UserContextKey, u))
	}
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", id)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

func TestTrackShow_Unauthorized(t *testing.T) {
	_, h := setupTrackHandler(t)

	req := trackRequest("GET", "/library/track/1", nil, "1")
	w := httptest.NewRecorder()

	h.TrackShow(w, req)

	resp := w.Result()
	assert.Equal(t, http.StatusSeeOther, resp.StatusCode)
	assert.Equal(t, "/auth/login", resp.Header.Get("Location"))
}

func TestTrackShow_InvalidID(t *testing.T) {
	client, h := setupTrackHandler(t)
	u := createTrackTestUser(t, client)

	req := trackRequest("GET", "/library/track/invalid", u, "invalid")
	w := httptest.NewRecorder()

	h.TrackShow(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Result().StatusCode)
}

func TestTrackShow_NotFound(t *testing.T) {
	client, h := setupTrackHandler(t)
	u := createTrackTestUser(t, client)

	req := trackRequest("GET", "/library/track/99999", u, "99999")
	w := httptest.NewRecorder()

	h.TrackShow(w, req)

	assert.Equal(t, http.StatusNotFound, w.Result().StatusCode)
}

func TestTrackShow_UserIsolation(t *testing.T) {
	client, h := setupTrackHandler(t)
	owner := createTrackTestUser(t, client)
	other := createTrackTestUser(t, client)

	_, _, tr := createTrackFixture(t, client, owner, "Owner Artist", "Owner Album", "Owner Track")

	// Another user must not be able to view the track.
	req := trackRequest("GET", "/library/track/"+strconv.Itoa(tr.ID), other, strconv.Itoa(tr.ID))
	w := httptest.NewRecorder()

	h.TrackShow(w, req)

	assert.Equal(t, http.StatusNotFound, w.Result().StatusCode)
}

func TestTrackShow_Success(t *testing.T) {
	client, h := setupTrackHandler(t)
	u := createTrackTestUser(t, client)
	ctx := context.Background()

	_, _, tr := createTrackFixture(t, client, u, "Show Artist", "Show Album", "Show Track")

	// Listens for the track feed the stats charts.
	for i := 0; i < 3; i++ {
		_, err := client.Listen.Create().
			SetUser(u).
			SetTrackName("Show Track").
			SetArtistName("Show Artist").
			SetAlbumName("Show Album").
			SetSource("navidrome").
			SetPlayedAt(time.Now().Add(-time.Duration(i+1) * time.Hour)).
			Save(ctx)
		require.NoError(t, err)
	}

	// A playlist containing the track exercises getPlaylistsWithTrack.
	pl, err := client.Playlist.Create().
		SetUser(u).
		SetRemoteID("track-show-pl").
		SetName("Track Show Playlist").
		SetSource("navidrome").
		Save(ctx)
	require.NoError(t, err)
	_, err = client.PlaylistTrack.Create().
		SetPlaylist(pl).
		SetTrack(tr).
		SetTrackName("Show Track").
		SetArtistName("Show Artist").
		SetAlbumName("Show Album").
		Save(ctx)
	require.NoError(t, err)

	req := trackRequest("GET", "/library/track/"+strconv.Itoa(tr.ID)+"?timeframe=1y", u, strconv.Itoa(tr.ID))
	w := httptest.NewRecorder()

	h.TrackShow(w, req)

	resp := w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)
	assert.Contains(t, bodyStr, "Show Track")
	assert.Contains(t, bodyStr, "Show Artist")
	assert.Contains(t, bodyStr, "Track Show Playlist")
}

func TestTrackChart_Unauthorized(t *testing.T) {
	_, h := setupTrackHandler(t)

	req := trackRequest("GET", "/library/track/1/chart", nil, "1")
	w := httptest.NewRecorder()

	h.TrackChart(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Result().StatusCode)
}

func TestTrackChart_InvalidID(t *testing.T) {
	client, h := setupTrackHandler(t)
	u := createTrackTestUser(t, client)

	req := trackRequest("GET", "/library/track/invalid/chart", u, "invalid")
	w := httptest.NewRecorder()

	h.TrackChart(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Result().StatusCode)
}

func TestTrackChart_NotFound(t *testing.T) {
	client, h := setupTrackHandler(t)
	u := createTrackTestUser(t, client)

	req := trackRequest("GET", "/library/track/99999/chart", u, "99999")
	w := httptest.NewRecorder()

	h.TrackChart(w, req)

	assert.Equal(t, http.StatusNotFound, w.Result().StatusCode)
}

func TestTrackChart_UserIsolation(t *testing.T) {
	client, h := setupTrackHandler(t)
	owner := createTrackTestUser(t, client)
	other := createTrackTestUser(t, client)

	_, _, tr := createTrackFixture(t, client, owner, "Chart Artist", "Chart Album", "Chart Track")

	req := trackRequest("GET", "/library/track/"+strconv.Itoa(tr.ID)+"/chart", other, strconv.Itoa(tr.ID))
	w := httptest.NewRecorder()

	h.TrackChart(w, req)

	assert.Equal(t, http.StatusNotFound, w.Result().StatusCode)
}

func TestTrackChart_Success(t *testing.T) {
	client, h := setupTrackHandler(t)
	u := createTrackTestUser(t, client)

	_, _, tr := createTrackFixture(t, client, u, "Chart Artist", "Chart Album", "Chart Track")

	req := trackRequest("GET", "/library/track/"+strconv.Itoa(tr.ID)+"/chart", u, strconv.Itoa(tr.ID))
	w := httptest.NewRecorder()

	h.TrackChart(w, req)

	assert.Equal(t, http.StatusOK, w.Result().StatusCode)
}

func TestTrackIndex_Unauthorized(t *testing.T) {
	_, h := setupTrackHandler(t)

	req := httptest.NewRequest("GET", "/library/tracks", nil)
	w := httptest.NewRecorder()

	h.TrackIndex(w, req)

	resp := w.Result()
	assert.Equal(t, http.StatusSeeOther, resp.StatusCode)
	assert.Equal(t, "/auth/login", resp.Header.Get("Location"))
}

func TestTrackIndex_EmptyState(t *testing.T) {
	client, h := setupTrackHandler(t)
	u := createTrackTestUser(t, client)

	req := httptest.NewRequest("GET", "/library/tracks", nil)
	req = req.WithContext(context.WithValue(req.Context(), handlers.UserContextKey, u))
	w := httptest.NewRecorder()

	h.TrackIndex(w, req)

	resp := w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(body), "No tracks")
}

func TestTrackIndex_WithDataAndSort(t *testing.T) {
	client, h := setupTrackHandler(t)
	u := createTrackTestUser(t, client)

	createTrackFixture(t, client, u, "Index Artist A", "Index Album A", "Index Track A")
	createTrackFixture(t, client, u, "Index Artist B", "Index Album B", "Index Track B")

	req := httptest.NewRequest("GET", "/library/tracks?sort=duration&dir=desc", nil)
	req = req.WithContext(context.WithValue(req.Context(), handlers.UserContextKey, u))
	w := httptest.NewRecorder()

	h.TrackIndex(w, req)

	resp := w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)
	assert.Contains(t, bodyStr, "Index Track A")
	assert.Contains(t, bodyStr, "Index Track B")
}

func TestTrackIndex_ArtistFilter(t *testing.T) {
	client, h := setupTrackHandler(t)
	u := createTrackTestUser(t, client)

	createTrackFixture(t, client, u, "Wanted Artist", "Album A", "Wanted Track")
	createTrackFixture(t, client, u, "Unwanted Artist", "Album B", "Unwanted Track")

	req := httptest.NewRequest("GET", "/library/tracks?artist=Wanted+Artist", nil)
	req = req.WithContext(context.WithValue(req.Context(), handlers.UserContextKey, u))
	w := httptest.NewRecorder()

	h.TrackIndex(w, req)

	resp := w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)
	assert.Contains(t, bodyStr, "Wanted Track")
	assert.NotContains(t, bodyStr, "Unwanted Track")
}

func TestTrackIndex_UserIsolation(t *testing.T) {
	client, h := setupTrackHandler(t)
	u1 := createTrackTestUser(t, client)
	u2 := createTrackTestUser(t, client)

	createTrackFixture(t, client, u1, "U1 Artist", "U1 Album", "U1 Track")
	createTrackFixture(t, client, u2, "U2 Artist", "U2 Album", "U2 Track")

	req := httptest.NewRequest("GET", "/library/tracks", nil)
	req = req.WithContext(context.WithValue(req.Context(), handlers.UserContextKey, u1))
	w := httptest.NewRecorder()

	h.TrackIndex(w, req)

	resp := w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)
	assert.Contains(t, bodyStr, "U1 Track")
	assert.NotContains(t, bodyStr, "U2 Track")
}

func TestTrackRegenerateAI_Unauthorized(t *testing.T) {
	_, h := setupTrackHandler(t)

	req := trackRequest("POST", "/library/track/1/regenerate-ai", nil, "1")
	w := httptest.NewRecorder()

	h.TrackRegenerateAI(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Result().StatusCode)
}

func TestTrackRegenerateAI_InvalidID(t *testing.T) {
	client, h := setupTrackHandler(t)
	u := createTrackTestUser(t, client)

	req := trackRequest("POST", "/library/track/invalid/regenerate-ai", u, "invalid")
	w := httptest.NewRecorder()

	h.TrackRegenerateAI(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Result().StatusCode)
}

func TestTrackRegenerateAI_NotFound(t *testing.T) {
	client, h := setupTrackHandler(t)
	u := createTrackTestUser(t, client)

	req := trackRequest("POST", "/library/track/99999/regenerate-ai", u, "99999")
	w := httptest.NewRecorder()

	h.TrackRegenerateAI(w, req)

	assert.Equal(t, http.StatusNotFound, w.Result().StatusCode)
}

func TestTrackRegenerateAI_EnricherUnavailable(t *testing.T) {
	// With no MetadataSvc configured, AI enrichment must fail with 503.
	client, h := setupTrackHandler(t)
	u := createTrackTestUser(t, client)

	_, _, tr := createTrackFixture(t, client, u, "AI Artist", "AI Album", "AI Track")

	req := trackRequest("POST", "/library/track/"+strconv.Itoa(tr.ID)+"/regenerate-ai", u, strconv.Itoa(tr.ID))
	w := httptest.NewRecorder()

	h.TrackRegenerateAI(w, req)

	assert.Equal(t, http.StatusServiceUnavailable, w.Result().StatusCode)
}
