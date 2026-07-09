// Tests for mixtape seed scoping and DJ deletion conflict handling (issue #13).
// Governing: SPEC vibes-ai-mixtape-engine REQ-VIBES-003, REQ-VIBES-022
package handlers_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"spotter/ent"
	"spotter/ent/enttest"
	"spotter/internal/auth"
	"spotter/internal/config"
	"spotter/internal/crypto"
	"spotter/internal/events"
	"spotter/internal/handlers"
	"spotter/internal/vibes"

	"github.com/go-chi/chi/v5"
	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupVibesScopeHandler builds a handler with a real (never-invoked)
// MixtapeGenerator so GenerateMixtape gets past its nil-service guard and
// exercises the seed ownership checks.
func setupVibesScopeHandler(t *testing.T) (*ent.Client, *handlers.Handler) {
	dbName := strings.NewReplacer("/", "_", " ", "_", "=", "_").Replace(t.Name())
	client := enttest.Open(t, "sqlite3", "file:"+dbName+"?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { client.Close() })

	cfg := &config.Config{
		Vibes: config.VibesConfig{
			DefaultMaxTracks: 25,
		},
	}
	bus := events.NewBus()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	generator := vibes.NewMixtapeGenerator(client, cfg, logger, bus)

	encryptor, _ := crypto.NewEncryptor(make([]byte, 32))
	jwtManager := auth.NewJWTManager(testJWTSecret)
	h := handlers.New(client, cfg, logger, encryptor, jwtManager, nil, nil, nil, generator, nil, nil, bus, nil)
	return client, h
}

func createScopeTestUser(t *testing.T, client *ent.Client, username string) *ent.User {
	u, err := client.User.Create().
		SetUsername(username).
		SetPaginationSize(25).
		Save(context.Background())
	require.NoError(t, err)
	return u
}

func createScopeTestMixtape(t *testing.T, client *ent.Client, u *ent.User) *ent.Mixtape {
	dj, err := client.DJ.Create().
		SetName("Scope DJ " + u.Username).
		SetUser(u).
		Save(context.Background())
	require.NoError(t, err)

	m, err := client.Mixtape.Create().
		SetName("Scope Mixtape " + u.Username).
		SetMaxTracks(25).
		SetDj(dj).
		SetUser(u).
		Save(context.Background())
	require.NoError(t, err)
	return m
}

func generateMixtapeRequest(t *testing.T, h *handlers.Handler, u *ent.User, mixtapeID int, form url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/vibes/mixtapes/"+strconv.Itoa(mixtapeID)+"/generate",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", strconv.Itoa(mixtapeID))
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	req = req.WithContext(context.WithValue(req.Context(), handlers.UserContextKey, u))

	h.GenerateMixtape(w, req)
	return w
}

func TestGenerateMixtape_ForeignArtistSeedRejected(t *testing.T) {
	client, h := setupVibesScopeHandler(t)
	owner := createScopeTestUser(t, client, "seed_owner_artist")
	other := createScopeTestUser(t, client, "seed_other_artist")
	m := createScopeTestMixtape(t, client, owner)

	foreignArtist, err := client.Artist.Create().
		SetName("Foreign Artist").
		SetUser(other).
		Save(context.Background())
	require.NoError(t, err)

	form := url.Values{}
	form.Set("seed_type", "artist")
	form.Set("seed_id", strconv.Itoa(foreignArtist.ID))

	w := generateMixtapeRequest(t, h, owner, m.ID, form)
	assert.Equal(t, http.StatusNotFound, w.Code,
		"seeding with another user's artist must be rejected")
}

func TestGenerateMixtape_ForeignAlbumSeedRejected(t *testing.T) {
	client, h := setupVibesScopeHandler(t)
	owner := createScopeTestUser(t, client, "seed_owner_album")
	other := createScopeTestUser(t, client, "seed_other_album")
	m := createScopeTestMixtape(t, client, owner)

	foreignArtist, err := client.Artist.Create().
		SetName("Foreign Album Artist").
		SetUser(other).
		Save(context.Background())
	require.NoError(t, err)
	foreignAlbum, err := client.Album.Create().
		SetName("Foreign Album").
		SetArtist(foreignArtist).
		SetUser(other).
		Save(context.Background())
	require.NoError(t, err)

	form := url.Values{}
	form.Set("seed_type", "album")
	form.Set("seed_id", strconv.Itoa(foreignAlbum.ID))

	w := generateMixtapeRequest(t, h, owner, m.ID, form)
	assert.Equal(t, http.StatusNotFound, w.Code,
		"seeding with another user's album must be rejected")
}

func TestGenerateMixtape_ForeignTrackSeedRejected(t *testing.T) {
	client, h := setupVibesScopeHandler(t)
	owner := createScopeTestUser(t, client, "seed_owner_tracks")
	other := createScopeTestUser(t, client, "seed_other_tracks")
	m := createScopeTestMixtape(t, client, owner)

	// One track owned by the requester, one by another user.
	ownArtist, err := client.Artist.Create().
		SetName("Own Artist").
		SetUser(owner).
		Save(context.Background())
	require.NoError(t, err)
	ownTrack, err := client.Track.Create().
		SetName("Own Track").
		SetArtist(ownArtist).
		Save(context.Background())
	require.NoError(t, err)

	foreignArtist, err := client.Artist.Create().
		SetName("Foreign Track Artist").
		SetUser(other).
		Save(context.Background())
	require.NoError(t, err)
	foreignTrack, err := client.Track.Create().
		SetName("Foreign Track").
		SetArtist(foreignArtist).
		Save(context.Background())
	require.NoError(t, err)

	form := url.Values{}
	form.Set("seed_type", "tracks")
	form.Set("track_ids", strconv.Itoa(ownTrack.ID)+","+strconv.Itoa(foreignTrack.ID))

	w := generateMixtapeRequest(t, h, owner, m.ID, form)
	assert.Equal(t, http.StatusNotFound, w.Code,
		"a tracks seed containing another user's track must be rejected")
}

// Governing: SPEC vibes-ai-mixtape-engine REQ-VIBES-003 (lead decision: DJ
// deletion is blocked while mixtapes reference it, with a 409 + reassign
// guidance instead of a foreign-key 500).
func TestDeleteDJ_WithMixtapes_ReturnsConflict(t *testing.T) {
	client, h := setupVibesScopeHandler(t)
	u := createScopeTestUser(t, client, "dj_delete_conflict")
	m := createScopeTestMixtape(t, client, u)

	d, err := m.QueryDj().Only(context.Background())
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodDelete, "/vibes/djs/"+strconv.Itoa(d.ID), nil)
	w := httptest.NewRecorder()

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", strconv.Itoa(d.ID))
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	req = req.WithContext(context.WithValue(req.Context(), handlers.UserContextKey, u))

	h.DeleteDJ(w, req)

	assert.Equal(t, http.StatusConflict, w.Code)
	assert.Contains(t, w.Body.String(), "reassign",
		"conflict response should carry the reassign guidance the UI shows")

	// The DJ must still exist.
	exists, err := client.DJ.Query().Exist(context.Background())
	require.NoError(t, err)
	assert.True(t, exists)
}

func TestDeleteDJ_WithoutMixtapes_Succeeds(t *testing.T) {
	client, h := setupVibesScopeHandler(t)
	u := createScopeTestUser(t, client, "dj_delete_ok")

	d, err := client.DJ.Create().
		SetName("Deletable DJ").
		SetUser(u).
		Save(context.Background())
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodDelete, "/vibes/djs/"+strconv.Itoa(d.ID), nil)
	w := httptest.NewRecorder()

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", strconv.Itoa(d.ID))
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	req = req.WithContext(context.WithValue(req.Context(), handlers.UserContextKey, u))

	h.DeleteDJ(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	count, err := client.DJ.Query().Count(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 0, count)
}
