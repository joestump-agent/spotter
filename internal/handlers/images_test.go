package handlers_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"

	"spotter/ent"
	"spotter/ent/albumimage"
	"spotter/ent/artistimage"
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

// imageTestUserCounter ensures unique usernames across tests
var imageTestUserCounter int64

func uniqueImageTestUsername() string {
	return fmt.Sprintf("imagetestuser_%d", atomic.AddInt64(&imageTestUserCounter, 1))
}

func setupImageHandler(t *testing.T) (*ent.Client, *handlers.Handler) {
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

func createImageTestUser(t *testing.T, client *ent.Client) *ent.User {
	u, err := client.User.Create().
		SetUsername(uniqueImageTestUsername()).
		SetPaginationSize(25).
		Save(context.Background())
	require.NoError(t, err)
	return u
}

// writeLocalImage writes a fake PNG under ./data (relative to the package
// working directory) because serveImage only serves files inside ./data.
func writeLocalImage(t *testing.T, name string) string {
	dir := filepath.Join("data", "images")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, []byte("fake-png-bytes"), 0o644))
	t.Cleanup(func() {
		_ = os.Remove(path)
		// Remove the directories too if no other test still uses them
		// (os.Remove fails harmlessly on non-empty directories).
		_ = os.Remove(dir)
		_ = os.Remove("data")
	})
	return path
}

func imageRequest(target string, u *ent.User, id string) *http.Request {
	req := httptest.NewRequest("GET", target, nil)
	if u != nil {
		req = req.WithContext(context.WithValue(req.Context(), handlers.UserContextKey, u))
	}
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", id)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

func TestArtistImage_Unauthorized(t *testing.T) {
	_, h := setupImageHandler(t)

	req := imageRequest("/images/artist/1.png", nil, "1.png")
	w := httptest.NewRecorder()

	h.ArtistImage(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Result().StatusCode)
}

func TestArtistImage_InvalidID(t *testing.T) {
	client, h := setupImageHandler(t)
	u := createImageTestUser(t, client)

	req := imageRequest("/images/artist/notanid.png", u, "notanid.png")
	w := httptest.NewRecorder()

	h.ArtistImage(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Result().StatusCode)
}

func TestArtistImage_NotFound(t *testing.T) {
	client, h := setupImageHandler(t)
	u := createImageTestUser(t, client)

	req := imageRequest("/images/artist/99999.png", u, "99999.png")
	w := httptest.NewRecorder()

	h.ArtistImage(w, req)

	assert.Equal(t, http.StatusNotFound, w.Result().StatusCode)
}

func TestArtistImage_NoImages(t *testing.T) {
	client, h := setupImageHandler(t)
	u := createImageTestUser(t, client)

	a, err := client.Artist.Create().
		SetName("No Image Artist").
		SetUser(u).
		Save(context.Background())
	require.NoError(t, err)

	req := imageRequest("/images/artist/"+strconv.Itoa(a.ID)+".png", u, strconv.Itoa(a.ID)+".png")
	w := httptest.NewRecorder()

	h.ArtistImage(w, req)

	assert.Equal(t, http.StatusNotFound, w.Result().StatusCode)
}

func TestArtistImage_PrimaryThumbnailServed(t *testing.T) {
	client, h := setupImageHandler(t)
	u := createImageTestUser(t, client)
	ctx := context.Background()

	localPath := writeLocalImage(t, fmt.Sprintf("artist_primary_%d.png", u.ID))

	a, err := client.Artist.Create().
		SetName("Primary Image Artist").
		SetUser(u).
		Save(ctx)
	require.NoError(t, err)

	_, err = client.ArtistImage.Create().
		SetArtist(a).
		SetSource("fanart").
		SetImageType(artistimage.ImageTypeThumbnail).
		SetURL("https://example.com/orig.png").
		SetLocalPath(localPath).
		SetIsPrimary(true).
		Save(ctx)
	require.NoError(t, err)

	req := imageRequest("/images/artist/"+strconv.Itoa(a.ID)+".png", u, strconv.Itoa(a.ID)+".png")
	w := httptest.NewRecorder()

	h.ArtistImage(w, req)

	resp := w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "image/png", resp.Header.Get("Content-Type"))
	assert.Equal(t, "public, max-age=86400", resp.Header.Get("Cache-Control"))
	body, _ := io.ReadAll(resp.Body)
	assert.Equal(t, "fake-png-bytes", string(body))
}

func TestArtistImage_FallbackToNonPrimary(t *testing.T) {
	// Only a non-primary image with a valid path exists; the handler must
	// fall through the primary preferences and still serve it.
	// Governing: issue #127 — records with empty local_path are skipped.
	client, h := setupImageHandler(t)
	u := createImageTestUser(t, client)
	ctx := context.Background()

	localPath := writeLocalImage(t, fmt.Sprintf("artist_fallback_%d.png", u.ID))

	a, err := client.Artist.Create().
		SetName("Fallback Image Artist").
		SetUser(u).
		Save(ctx)
	require.NoError(t, err)

	// Primary thumbnail with empty local path must be skipped.
	_, err = client.ArtistImage.Create().
		SetArtist(a).
		SetSource("fanart").
		SetImageType(artistimage.ImageTypeThumbnail).
		SetURL("https://example.com/missing.png").
		SetLocalPath("").
		SetIsPrimary(true).
		Save(ctx)
	require.NoError(t, err)

	_, err = client.ArtistImage.Create().
		SetArtist(a).
		SetSource("fanart").
		SetImageType(artistimage.ImageTypeBackground).
		SetURL("https://example.com/orig.png").
		SetLocalPath(localPath).
		SetIsPrimary(false).
		Save(ctx)
	require.NoError(t, err)

	req := imageRequest("/images/artist/"+strconv.Itoa(a.ID)+".png", u, strconv.Itoa(a.ID)+".png")
	w := httptest.NewRecorder()

	h.ArtistImage(w, req)

	resp := w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	assert.Equal(t, "fake-png-bytes", string(body))
}

func TestArtistImage_PathTraversalBlocked(t *testing.T) {
	client, h := setupImageHandler(t)
	u := createImageTestUser(t, client)
	ctx := context.Background()

	a, err := client.Artist.Create().
		SetName("Traversal Artist").
		SetUser(u).
		Save(ctx)
	require.NoError(t, err)

	_, err = client.ArtistImage.Create().
		SetArtist(a).
		SetSource("fanart").
		SetImageType(artistimage.ImageTypeThumbnail).
		SetURL("https://example.com/orig.png").
		// The payload must reference a file that actually exists outside
		// ./data — a nonexistent target (e.g. /etc/passwd relative to the
		// package cwd) returns 404 even without the guard, making the test
		// vacuous. images.go is guaranteed to exist next to the test cwd.
		SetLocalPath("data/../images.go").
		SetIsPrimary(true).
		Save(ctx)
	require.NoError(t, err)

	req := imageRequest("/images/artist/"+strconv.Itoa(a.ID)+".png", u, strconv.Itoa(a.ID)+".png")
	w := httptest.NewRecorder()

	h.ArtistImage(w, req)

	assert.Equal(t, http.StatusNotFound, w.Result().StatusCode)
}

func TestAlbumImage_Unauthorized(t *testing.T) {
	_, h := setupImageHandler(t)

	req := imageRequest("/images/album/1.png", nil, "1.png")
	w := httptest.NewRecorder()

	h.AlbumImage(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Result().StatusCode)
}

func TestAlbumImage_InvalidID(t *testing.T) {
	client, h := setupImageHandler(t)
	u := createImageTestUser(t, client)

	req := imageRequest("/images/album/notanid.jpg", u, "notanid.jpg")
	w := httptest.NewRecorder()

	h.AlbumImage(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Result().StatusCode)
}

func TestAlbumImage_NotFound(t *testing.T) {
	client, h := setupImageHandler(t)
	u := createImageTestUser(t, client)

	req := imageRequest("/images/album/99999.png", u, "99999.png")
	w := httptest.NewRecorder()

	h.AlbumImage(w, req)

	assert.Equal(t, http.StatusNotFound, w.Result().StatusCode)
}

func TestAlbumImage_NoImages(t *testing.T) {
	client, h := setupImageHandler(t)
	u := createImageTestUser(t, client)
	ctx := context.Background()

	a, err := client.Artist.Create().
		SetName("Album Artist").
		SetUser(u).
		Save(ctx)
	require.NoError(t, err)
	al, err := client.Album.Create().
		SetName("No Image Album").
		SetUser(u).
		SetArtist(a).
		Save(ctx)
	require.NoError(t, err)

	req := imageRequest("/images/album/"+strconv.Itoa(al.ID)+".png", u, strconv.Itoa(al.ID)+".png")
	w := httptest.NewRecorder()

	h.AlbumImage(w, req)

	assert.Equal(t, http.StatusNotFound, w.Result().StatusCode)
}

func TestAlbumImage_PrimaryServed(t *testing.T) {
	client, h := setupImageHandler(t)
	u := createImageTestUser(t, client)
	ctx := context.Background()

	localPath := writeLocalImage(t, fmt.Sprintf("album_primary_%d.png", u.ID))

	a, err := client.Artist.Create().
		SetName("Album Artist").
		SetUser(u).
		Save(ctx)
	require.NoError(t, err)
	al, err := client.Album.Create().
		SetName("Primary Image Album").
		SetUser(u).
		SetArtist(a).
		Save(ctx)
	require.NoError(t, err)

	_, err = client.AlbumImage.Create().
		SetAlbum(al).
		SetSource("fanart").
		SetImageType(albumimage.ImageTypeCoverFront).
		SetURL("https://example.com/cover.png").
		SetLocalPath(localPath).
		SetIsPrimary(true).
		Save(ctx)
	require.NoError(t, err)

	req := imageRequest("/images/album/"+strconv.Itoa(al.ID)+".png", u, strconv.Itoa(al.ID)+".png")
	w := httptest.NewRecorder()

	h.AlbumImage(w, req)

	resp := w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "image/png", resp.Header.Get("Content-Type"))
	body, _ := io.ReadAll(resp.Body)
	assert.Equal(t, "fake-png-bytes", string(body))
}

func TestAlbumImage_FallbackToNonPrimary(t *testing.T) {
	client, h := setupImageHandler(t)
	u := createImageTestUser(t, client)
	ctx := context.Background()

	localPath := writeLocalImage(t, fmt.Sprintf("album_fallback_%d.png", u.ID))

	a, err := client.Artist.Create().
		SetName("Album Artist").
		SetUser(u).
		Save(ctx)
	require.NoError(t, err)
	al, err := client.Album.Create().
		SetName("Fallback Image Album").
		SetUser(u).
		SetArtist(a).
		Save(ctx)
	require.NoError(t, err)

	_, err = client.AlbumImage.Create().
		SetAlbum(al).
		SetSource("fanart").
		SetImageType(albumimage.ImageTypeCoverFront).
		SetURL("https://example.com/cover.png").
		SetLocalPath(localPath).
		SetIsPrimary(false).
		Save(ctx)
	require.NoError(t, err)

	req := imageRequest("/images/album/"+strconv.Itoa(al.ID)+".png", u, strconv.Itoa(al.ID)+".png")
	w := httptest.NewRecorder()

	h.AlbumImage(w, req)

	resp := w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	assert.Equal(t, "fake-png-bytes", string(body))
}

func TestPlaylistImage_Unauthorized(t *testing.T) {
	_, h := setupImageHandler(t)

	req := imageRequest("/images/playlist/1.png", nil, "1.png")
	w := httptest.NewRecorder()

	h.PlaylistImage(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Result().StatusCode)
}

func TestPlaylistImage_InvalidID(t *testing.T) {
	client, h := setupImageHandler(t)
	u := createImageTestUser(t, client)

	req := imageRequest("/images/playlist/notanid.png", u, "notanid.png")
	w := httptest.NewRecorder()

	h.PlaylistImage(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Result().StatusCode)
}

func TestPlaylistImage_NotFound(t *testing.T) {
	client, h := setupImageHandler(t)
	u := createImageTestUser(t, client)

	req := imageRequest("/images/playlist/99999.png", u, "99999.png")
	w := httptest.NewRecorder()

	h.PlaylistImage(w, req)

	assert.Equal(t, http.StatusNotFound, w.Result().StatusCode)
}

func TestPlaylistImage_NoImageURL(t *testing.T) {
	client, h := setupImageHandler(t)
	u := createImageTestUser(t, client)

	pl, err := client.Playlist.Create().
		SetUser(u).
		SetRemoteID("img-pl-1").
		SetName("No Image Playlist").
		SetSource("spotify").
		Save(context.Background())
	require.NoError(t, err)

	req := imageRequest("/images/playlist/"+strconv.Itoa(pl.ID)+".png", u, strconv.Itoa(pl.ID)+".png")
	w := httptest.NewRecorder()

	h.PlaylistImage(w, req)

	assert.Equal(t, http.StatusNotFound, w.Result().StatusCode)
}

func TestPlaylistImage_RedirectsToExternalURL(t *testing.T) {
	client, h := setupImageHandler(t)
	u := createImageTestUser(t, client)

	pl, err := client.Playlist.Create().
		SetUser(u).
		SetRemoteID("img-pl-2").
		SetName("External Image Playlist").
		SetSource("spotify").
		SetImageURL("https://images.example.com/playlist.png").
		Save(context.Background())
	require.NoError(t, err)

	req := imageRequest("/images/playlist/"+strconv.Itoa(pl.ID)+".png", u, strconv.Itoa(pl.ID)+".png")
	w := httptest.NewRecorder()

	h.PlaylistImage(w, req)

	resp := w.Result()
	assert.Equal(t, http.StatusTemporaryRedirect, resp.StatusCode)
	assert.Equal(t, "https://images.example.com/playlist.png", resp.Header.Get("Location"))
}

// TestPlaylistImage_InvalidSchemeBlocked verifies the open-redirect guard:
// non-http(s) image URLs must not be redirected to.
// Governing: SPEC user-authentication REQ "Input Validation"
func TestPlaylistImage_InvalidSchemeBlocked(t *testing.T) {
	client, h := setupImageHandler(t)
	u := createImageTestUser(t, client)

	pl, err := client.Playlist.Create().
		SetUser(u).
		SetRemoteID("img-pl-3").
		SetName("Evil Image Playlist").
		SetSource("spotify").
		SetImageURL("javascript:alert(1)").
		Save(context.Background())
	require.NoError(t, err)

	req := imageRequest("/images/playlist/"+strconv.Itoa(pl.ID)+".png", u, strconv.Itoa(pl.ID)+".png")
	w := httptest.NewRecorder()

	h.PlaylistImage(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Result().StatusCode)
}

func TestPlaylistImage_UserIsolation(t *testing.T) {
	client, h := setupImageHandler(t)
	owner := createImageTestUser(t, client)
	other := createImageTestUser(t, client)

	pl, err := client.Playlist.Create().
		SetUser(owner).
		SetRemoteID("img-pl-4").
		SetName("Owner Playlist").
		SetSource("spotify").
		SetImageURL("https://images.example.com/owner.png").
		Save(context.Background())
	require.NoError(t, err)

	req := imageRequest("/images/playlist/"+strconv.Itoa(pl.ID)+".png", other, strconv.Itoa(pl.ID)+".png")
	w := httptest.NewRecorder()

	h.PlaylistImage(w, req)

	assert.Equal(t, http.StatusNotFound, w.Result().StatusCode)
}
