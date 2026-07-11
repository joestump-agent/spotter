// Governing: ADR-0030, SPEC music-provider-integration REQ "ListenBrainz Provider" (REQ-PROV-053)
package handlers_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"spotter/ent"
	"spotter/ent/playlist"
	"spotter/ent/playlisttrack"
	"spotter/internal/auth"
	"spotter/internal/config"
	"spotter/internal/crypto"
	"spotter/internal/events"
	"spotter/internal/handlers"
	"spotter/internal/providers"
	"spotter/internal/services"

	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	radioRecordingMBID1 = "2cfad207-3f55-4aec-8120-86cf66e34d59"
	radioRecordingMBID2 = "97e69767-5d34-4c97-b36a-f3b2b1ef9dae"
)

// setupRadioHandler builds a handler whose ListenBrainz provider points at
// lbServerURL (the httptest lb-radio server).
func setupRadioHandler(t *testing.T, lbServerURL string) (*ent.Client, *handlers.Handler) {
	t.Helper()
	client := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{}
	cfg.ListenBrainz.APIURL = lbServerURL
	bus := events.NewBus()
	syncer := services.NewSyncer(client, cfg, logger, bus, nil)
	encryptor, _ := crypto.NewEncryptor(make([]byte, 32))
	jwtManager := auth.NewJWTManager(testJWTSecret)
	h := handlers.New(client, cfg, logger, encryptor, jwtManager, syncer, nil, nil, nil, nil, nil, bus, nil)
	return client, h
}

// radioTestUser creates a user, optionally with a ListenBrainz auth edge.
func radioTestUser(t *testing.T, client *ent.Client, connected bool) *ent.User {
	t.Helper()
	u, err := client.User.Create().
		SetUsername(uniquePlaylistTestUsername()).
		SetPaginationSize(25).
		Save(context.Background())
	require.NoError(t, err)
	if connected {
		_, err = client.ListenBrainzAuth.Create().
			SetUser(u).
			SetUsername("lb_user").
			SetToken("lb-token").
			Save(context.Background())
		require.NoError(t, err)
	}
	return u
}

// postRadio performs an HTMX form POST to the generate handler.
func postRadio(h *handlers.Handler, u *ent.User, form url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/playlists/lb-radio", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	if u != nil {
		req = req.WithContext(context.WithValue(req.Context(), handlers.UserContextKey, u))
	}
	w := httptest.NewRecorder()
	h.ListenBrainzRadioGenerate(w, req)
	return w
}

// radioJSPFBody builds an lb-radio response with the given (title, creator,
// mbid) track triples; an empty mbid omits the identifier.
func radioJSPFBody(tracks ...[3]string) string {
	items := make([]string, 0, len(tracks))
	for _, tr := range tracks {
		id := ""
		if tr[2] != "" {
			id = fmt.Sprintf(`, "identifier": ["https://musicbrainz.org/recording/%s"]`, tr[2])
		}
		items = append(items, fmt.Sprintf(`{"title": %q, "creator": %q%s}`, tr[0], tr[1], id))
	}
	return fmt.Sprintf(`{"payload": {"jspf": {"playlist": {"title": "server title", "track": [%s]}}, "feedback": []}}`,
		strings.Join(items, ","))
}

func TestListenBrainzRadioGenerate_Unauthorized(t *testing.T) {
	_, h := setupRadioHandler(t, "http://unused.invalid")

	w := postRadio(h, nil, url.Values{"prompt": {"tag:(jazz)"}})
	assert.Equal(t, http.StatusUnauthorized, w.Result().StatusCode)
}

func TestListenBrainzRadioGenerate_EmptyPrompt400(t *testing.T) {
	client, h := setupRadioHandler(t, "http://unused.invalid")
	u := radioTestUser(t, client, true)

	for _, prompt := range []string{"", "   "} {
		w := postRadio(h, u, url.Values{"prompt": {prompt}})
		assert.Equal(t, http.StatusBadRequest, w.Result().StatusCode)
	}
}

func TestListenBrainzRadioGenerate_PromptTooLong400(t *testing.T) {
	client, h := setupRadioHandler(t, "http://unused.invalid")
	u := radioTestUser(t, client, true)

	// > 200 chars would not fit the lb-radio:<prompt> remote_id convention.
	w := postRadio(h, u, url.Values{"prompt": {strings.Repeat("x", 201)}})
	assert.Equal(t, http.StatusBadRequest, w.Result().StatusCode)
}

func TestListenBrainzRadioGenerate_InvalidMode400(t *testing.T) {
	client, h := setupRadioHandler(t, "http://unused.invalid")
	u := radioTestUser(t, client, true)

	w := postRadio(h, u, url.Values{"prompt": {"tag:(jazz)"}, "mode": {"extreme"}})
	assert.Equal(t, http.StatusBadRequest, w.Result().StatusCode)
}

func TestListenBrainzRadioGenerate_NotConnected400(t *testing.T) {
	client, h := setupRadioHandler(t, "http://unused.invalid")
	u := radioTestUser(t, client, false)

	w := postRadio(h, u, url.Values{"prompt": {"tag:(jazz)"}})
	resp := w.Result()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(body), "Connect your ListenBrainz account")
}

// Governing: SPEC music-provider-integration REQ-PROV-053 — the happy path
// persists a regular "listenbrainz"-source playlist (lb-radio:<prompt> remote
// ID) whose track rows carry recording MBIDs in remote_id, via the SAME
// persist path the playlist syncer uses (no duplicated matching logic:
// catalog linking stays with the metadata service's name/artist pass).
func TestListenBrainzRadioGenerate_HappyPathPersistsPlaylist(t *testing.T) {
	const prompt = "artist:(nina simone)"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/1/explore/lb-radio", r.URL.Path)
		assert.Equal(t, prompt, r.URL.Query().Get("prompt"))
		assert.Equal(t, "medium", r.URL.Query().Get("mode"))
		assert.Equal(t, "Token lb-token", r.Header.Get("Authorization"))
		fmt.Fprint(w, radioJSPFBody(
			[3]string{"Feeling Good", "Nina Simone", radioRecordingMBID1},
			[3]string{"Sinnerman", "Nina Simone", radioRecordingMBID2},
		))
	}))
	defer server.Close()

	client, h := setupRadioHandler(t, server.URL)
	u := radioTestUser(t, client, true)

	w := postRadio(h, u, url.Values{"prompt": {prompt}, "mode": {"medium"}})
	resp := w.Result()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	// Persisted playlist row: regular playlist, listenbrainz source, radio remote ID.
	pl, err := client.Playlist.Query().Only(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "LB Radio: "+prompt, pl.Name)
	assert.Equal(t, string(providers.TypeListenBrainz), pl.Source)
	assert.Equal(t, providers.ListenBrainzRadioRemoteIDPrefix+prompt, pl.RemoteID)
	assert.Contains(t, pl.Description, "mode: medium")
	assert.Equal(t, 2, pl.TrackCount)
	assert.True(t, pl.IsActive)
	assert.False(t, pl.SyncToNavidrome, "sync stays opt-in via the standard toggle")

	// HTMX redirect to the persisted playlist's show page.
	assert.Equal(t, fmt.Sprintf("/playlists/%d", pl.ID), resp.Header.Get("HX-Redirect"))

	// Track rows carry recording MBIDs in remote_id (the ADR-0014 provider
	// track ID slot) so downstream matching works unchanged.
	tracks, err := client.PlaylistTrack.Query().
		Where(playlisttrack.HasPlaylistWith(playlist.ID(pl.ID))).
		Order(ent.Asc(playlisttrack.FieldPosition)).
		All(context.Background())
	require.NoError(t, err)
	require.Len(t, tracks, 2)
	assert.Equal(t, "Feeling Good", tracks[0].TrackName)
	assert.Equal(t, "Nina Simone", tracks[0].ArtistName)
	assert.Equal(t, radioRecordingMBID1, tracks[0].RemoteID)
	assert.Equal(t, "Sinnerman", tracks[1].TrackName)
	assert.Equal(t, radioRecordingMBID2, tracks[1].RemoteID)
}

// Governing: ADR-0030 (regenerate-in-place semantics), SPEC
// music-provider-integration REQ-PROV-053 — the same trimmed prompt (any
// mode) updates the existing playlist row: tracks are replaced, while the
// playlist ID and Navidrome sync state are preserved.
func TestListenBrainzRadioGenerate_RegenerationUpdatesInPlace(t *testing.T) {
	const prompt = "tag:(soul)"

	generation := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if generation == 0 {
			fmt.Fprint(w, radioJSPFBody(
				[3]string{"First Track", "Artist A", radioRecordingMBID1},
			))
			return
		}
		fmt.Fprint(w, radioJSPFBody(
			[3]string{"Second Track", "Artist B", radioRecordingMBID2},
		))
	}))
	defer server.Close()

	client, h := setupRadioHandler(t, server.URL)
	u := radioTestUser(t, client, true)
	ctx := context.Background()

	w := postRadio(h, u, url.Values{"prompt": {prompt}, "mode": {"easy"}})
	require.Equal(t, http.StatusOK, w.Result().StatusCode)

	first, err := client.Playlist.Query().Only(ctx)
	require.NoError(t, err)

	// Simulate the user enabling Navidrome sync and an established pairing:
	// regeneration must preserve both.
	_, err = client.Playlist.UpdateOne(first).
		SetSyncToNavidrome(true).
		SetNavidromePlaylistID("nd-123").
		Save(ctx)
	require.NoError(t, err)

	// Regenerate with the same prompt but a DIFFERENT mode: mode is a
	// generation knob, not part of the upsert key.
	generation = 1
	w = postRadio(h, u, url.Values{"prompt": {prompt}, "mode": {"hard"}})
	require.Equal(t, http.StatusOK, w.Result().StatusCode)

	// Still exactly one playlist row, same ID, sync state preserved.
	rows, err := client.Playlist.Query().All(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 1, "regeneration must update, not duplicate")
	assert.Equal(t, first.ID, rows[0].ID)
	assert.True(t, rows[0].SyncToNavidrome, "sync toggle survives regeneration")
	assert.Equal(t, "nd-123", rows[0].NavidromePlaylistID, "Navidrome pairing survives regeneration")
	assert.Contains(t, rows[0].Description, "mode: hard", "description reflects the latest generation")
	assert.Equal(t, 1, rows[0].TrackCount)
	assert.Equal(t, fmt.Sprintf("/playlists/%d", first.ID), w.Result().Header.Get("HX-Redirect"))

	// Tracks replaced: the removed track is gone, the new one present.
	tracks, err := client.PlaylistTrack.Query().
		Where(playlisttrack.HasPlaylistWith(playlist.ID(first.ID))).
		All(ctx)
	require.NoError(t, err)
	require.Len(t, tracks, 1)
	assert.Equal(t, "Second Track", tracks[0].TrackName)
	assert.Equal(t, radioRecordingMBID2, tracks[0].RemoteID)
}

// Zero generated tracks is surfaced as an inline error (not a persisted
// empty playlist).
func TestListenBrainzRadioGenerate_EmptyResultShowsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, radioJSPFBody())
	}))
	defer server.Close()

	client, h := setupRadioHandler(t, server.URL)
	u := radioTestUser(t, client, true)

	w := postRadio(h, u, url.Values{"prompt": {"tag:(obscure)"}})
	resp := w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Empty(t, resp.Header.Get("HX-Redirect"))
	body, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(body), "no tracks")

	count, err := client.Playlist.Query().Count(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 0, count, "empty generations must not persist a playlist")
}

// ListenBrainz prompt-syntax errors (API 400s) render inline so the user can
// fix the prompt.
func TestListenBrainzRadioGenerate_APIErrorShowsInline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"code": 400, "error": "Unknown prompt element artist_bogus"}`)
	}))
	defer server.Close()

	client, h := setupRadioHandler(t, server.URL)
	u := radioTestUser(t, client, true)

	w := postRadio(h, u, url.Values{"prompt": {"artist_bogus:(x)"}})
	resp := w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(body), "could not generate a playlist")

	count, err := client.Playlist.Query().Count(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 0, count)
}

func TestListenBrainzRadioForm(t *testing.T) {
	t.Run("connected shows the prompt form", func(t *testing.T) {
		client, h := setupRadioHandler(t, "http://unused.invalid")
		u := radioTestUser(t, client, true)

		req := httptest.NewRequest("GET", "/playlists/lb-radio", nil)
		req = req.WithContext(context.WithValue(req.Context(), handlers.UserContextKey, u))
		w := httptest.NewRecorder()
		h.ListenBrainzRadioForm(w, req)

		resp := w.Result()
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		body, _ := io.ReadAll(resp.Body)
		assert.Contains(t, string(body), "LB Radio")
		assert.Contains(t, string(body), `name="prompt"`)
		assert.Contains(t, string(body), `name="mode"`)
	})

	t.Run("not connected shows connect hint", func(t *testing.T) {
		client, h := setupRadioHandler(t, "http://unused.invalid")
		u := radioTestUser(t, client, false)

		req := httptest.NewRequest("GET", "/playlists/lb-radio", nil)
		req = req.WithContext(context.WithValue(req.Context(), handlers.UserContextKey, u))
		w := httptest.NewRecorder()
		h.ListenBrainzRadioForm(w, req)

		resp := w.Result()
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		body, _ := io.ReadAll(resp.Body)
		assert.Contains(t, string(body), "ListenBrainz is not connected")
	})

	t.Run("unauthenticated redirects to login", func(t *testing.T) {
		_, h := setupRadioHandler(t, "http://unused.invalid")

		req := httptest.NewRequest("GET", "/playlists/lb-radio", nil)
		w := httptest.NewRecorder()
		h.ListenBrainzRadioForm(w, req)

		resp := w.Result()
		assert.Equal(t, http.StatusSeeOther, resp.StatusCode)
		assert.Equal(t, "/auth/login", resp.Header.Get("Location"))
	})
}
