// Governing: SPEC music-provider-integration REQ-PROV-013/022/030/033, SPEC error-handling REQ-ERR-002
// Internal tests exercising the Spotify provider against a fake Spotify API
// server (token refresh persistence, ISRC mapping, cursor pagination,
// playlist creation, and 401 refresh-retry).
package spotify

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"spotter/ent"
	"spotter/ent/enttest"
	"spotter/internal/config"
	"spotter/internal/providers"

	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
)

// newTestProvider builds a Provider pointed at a fake Spotify server for both
// the Web API (baseURL) and the OAuth token endpoint (serverURL + "/token").
func newTestProvider(t *testing.T, serverURL string, auth *ent.SpotifyAuth, db *ent.Client) *Provider {
	t.Helper()

	cfg := &config.Config{}
	cfg.Spotify.ClientID = "test-client-id"
	cfg.Spotify.ClientSecret = "test-client-secret"

	oauthCfg := newOAuthConfig(cfg)
	oauthCfg.Endpoint = oauth2.Endpoint{
		AuthURL:  serverURL + "/authorize",
		TokenURL: serverURL + "/token",
	}

	return &Provider{
		logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		config:     cfg,
		user:       &ent.User{Username: "testuser"},
		auth:       auth,
		oauth:      oauthCfg,
		db:         db,
		baseURL:    serverURL,
		httpClient: &http.Client{Timeout: 5 * time.Second},
	}
}

func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	require.NoError(t, json.NewEncoder(w).Encode(v))
}

// Governing: SPEC music-provider-integration REQ-PROV-013 (refreshed tokens persisted)
func TestGetValidToken_RefreshIsPersisted(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:spotify_prov_refresh?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { client.Close() })
	ctx := context.Background()

	user, err := client.User.Create().SetUsername("testuser").Save(ctx)
	require.NoError(t, err)

	auth, err := client.SpotifyAuth.Create().
		SetAccessToken("old-access").
		SetRefreshToken("old-refresh").
		SetExpiry(time.Now().Add(-time.Hour)). // expired
		SetUser(user).
		Save(ctx)
	require.NoError(t, err)

	var mu sync.Mutex
	tokenCalls := 0
	rotateRefresh := true

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			mu.Lock()
			tokenCalls++
			mu.Unlock()
			resp := map[string]any{
				"access_token": "new-access",
				"expires_in":   3600,
				"token_type":   "Bearer",
			}
			if rotateRefresh {
				resp["refresh_token"] = "new-refresh"
			}
			writeJSON(t, w, resp)
		case "/v1/me":
			writeJSON(t, w, map[string]any{"id": "user-123", "display_name": "Test User"})
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer server.Close()

	t.Run("RotatedRefreshTokenPersisted", func(t *testing.T) {
		p := newTestProvider(t, server.URL, auth, client)

		token, err := p.getValidToken(ctx)
		require.NoError(t, err)
		assert.Equal(t, "new-access", token)
		assert.Equal(t, 1, tokenCalls)

		stored, err := client.SpotifyAuth.Get(ctx, auth.ID)
		require.NoError(t, err)
		assert.Equal(t, "new-access", stored.AccessToken)
		assert.Equal(t, "new-refresh", stored.RefreshToken, "rotated refresh token must be persisted")
		assert.True(t, stored.Expiry.After(time.Now()), "new expiry must be persisted")
	})

	t.Run("RefreshTokenKeptWhenNotRotated", func(t *testing.T) {
		rotateRefresh = false

		// Reset the row to an expired state with a known refresh token.
		require.NoError(t, client.SpotifyAuth.UpdateOneID(auth.ID).
			SetAccessToken("stale-access").
			SetRefreshToken("keep-me").
			SetExpiry(time.Now().Add(-time.Hour)).
			Exec(ctx))
		freshAuth, err := client.SpotifyAuth.Get(ctx, auth.ID)
		require.NoError(t, err)

		p := newTestProvider(t, server.URL, freshAuth, client)

		token, err := p.getValidToken(ctx)
		require.NoError(t, err)
		assert.Equal(t, "new-access", token)

		stored, err := client.SpotifyAuth.Get(ctx, auth.ID)
		require.NoError(t, err)
		assert.Equal(t, "new-access", stored.AccessToken)
		assert.Equal(t, "keep-me", stored.RefreshToken, "old refresh token must be kept when provider does not rotate")
	})
}

func validAuth() *ent.SpotifyAuth {
	return &ent.SpotifyAuth{
		AccessToken:  "valid-token",
		RefreshToken: "refresh-token",
		Expiry:       time.Now().Add(time.Hour),
	}
}

// Governing: SPEC music-provider-integration REQ-PROV-022 (ISRC), REQ-PROV-033 (cursor pagination toward since)
func TestGetRecentListens_CursorPaginationAndISRC(t *testing.T) {
	since := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)

	item := func(id, name, isrc string, playedAt time.Time) map[string]any {
		return map[string]any{
			"track": map[string]any{
				"id":            id,
				"name":          name,
				"duration_ms":   200000,
				"album":         map[string]any{"name": "Album"},
				"artists":       []map[string]any{{"name": "Artist"}},
				"external_urls": map[string]any{"spotify": "https://open.spotify.com/track/" + id},
				"external_ids":  map[string]any{"isrc": isrc},
			},
			"played_at": playedAt.Format(time.RFC3339),
		}
	}

	var mu sync.Mutex
	var queries []string

	var serverURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/me/player/recently-played", r.URL.Path)
		mu.Lock()
		queries = append(queries, r.URL.RawQuery)
		mu.Unlock()

		before := r.URL.Query().Get("before")
		switch before {
		case "":
			// Page 1: two plays newer than since, more pages available.
			writeJSON(t, w, map[string]any{
				"items": []map[string]any{
					item("track-a", "Song A", "USABC2600001", since.Add(48*time.Hour)),
					item("track-b", "Song B", "USABC2600002", since.Add(24*time.Hour)),
				},
				"cursors": map[string]any{"before": "cursor-1", "after": "x"},
				"next":    serverURL + "/v1/me/player/recently-played?limit=50&before=cursor-1",
			})
		case "cursor-1":
			// Page 2: one play newer than since, one older -> pagination must stop
			// even though another page is advertised.
			writeJSON(t, w, map[string]any{
				"items": []map[string]any{
					item("track-c", "Song C", "USABC2600003", since.Add(12*time.Hour)),
					item("track-d", "Song D", "USABC2600004", since.Add(-time.Hour)),
				},
				"cursors": map[string]any{"before": "cursor-2", "after": "x"},
				"next":    serverURL + "/v1/me/player/recently-played?limit=50&before=cursor-2",
			})
		default:
			t.Errorf("unexpected before cursor: %q", before)
			http.Error(w, "unexpected page", http.StatusBadRequest)
		}
	}))
	defer server.Close()
	serverURL = server.URL

	p := newTestProvider(t, server.URL, validAuth(), nil)

	var batches [][]providers.Track
	err := p.GetRecentListens(context.Background(), since, func(tracks []providers.Track) error {
		batches = append(batches, tracks)
		return nil
	})
	require.NoError(t, err)

	// Stopped after page 2 because an item at/older than since was seen.
	require.Len(t, queries, 2)
	assert.Contains(t, queries[1], "before=cursor-1", "second page must follow the before cursor")

	require.Len(t, batches, 2)
	require.Len(t, batches[0], 2)
	require.Len(t, batches[1], 1, "plays older than since must be filtered out")

	assert.Equal(t, "track-a", batches[0][0].ID)
	assert.Equal(t, "USABC2600001", batches[0][0].ISRC, "ISRC must be mapped from external_ids")
	assert.Equal(t, "USABC2600003", batches[1][0].ISRC)
	assert.Equal(t, "Song A", batches[0][0].Name)
	assert.Equal(t, "Artist", batches[0][0].Artist)
	assert.Equal(t, "Album", batches[0][0].Album)
}

// Governing: SPEC music-provider-integration REQ-PROV-033 (page cap bounds the pagination loop)
func TestGetRecentListens_StopsAtPageCap(t *testing.T) {
	var mu sync.Mutex
	requests := 0

	var serverURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests++
		page := requests
		mu.Unlock()

		writeJSON(t, w, map[string]any{
			"items": []map[string]any{
				{
					"track": map[string]any{
						"id":      fmt.Sprintf("track-%d", page),
						"name":    "Song",
						"artists": []map[string]any{{"name": "Artist"}},
						"album":   map[string]any{"name": "Album"},
					},
					"played_at": time.Now().UTC().Format(time.RFC3339),
				},
			},
			"cursors": map[string]any{"before": fmt.Sprintf("cursor-%d", page)},
			"next":    serverURL + "/v1/me/player/recently-played?limit=50",
		})
	}))
	defer server.Close()
	serverURL = server.URL

	p := newTestProvider(t, server.URL, validAuth(), nil)

	count := 0
	err := p.GetRecentListens(context.Background(), time.Now().Add(-365*24*time.Hour), func(tracks []providers.Track) error {
		count += len(tracks)
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, maxRecentlyPlayedPages, requests, "pagination must stop at the page cap")
	assert.Equal(t, maxRecentlyPlayedPages, count)
}

// Governing: SPEC music-provider-integration REQ-PROV-003, REQ-PROV-030 (playlist creation with 100-URI batches)
func TestCreatePlaylist_PostsAndBatches(t *testing.T) {
	var mu sync.Mutex
	var createBody map[string]any
	var batchSizes []int
	var firstURI string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/me" && r.Method == http.MethodGet:
			writeJSON(t, w, map[string]any{"id": "user-123", "display_name": "Test User"})

		case r.URL.Path == "/v1/users/user-123/playlists" && r.Method == http.MethodPost:
			mu.Lock()
			require.NoError(t, json.NewDecoder(r.Body).Decode(&createBody))
			mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			writeJSON(t, w, map[string]any{"id": "new-playlist-id", "name": "Test Mix"})

		case r.URL.Path == "/v1/playlists/new-playlist-id/tracks" && r.Method == http.MethodPost:
			var body struct {
				URIs []string `json:"uris"`
			}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			mu.Lock()
			if len(batchSizes) == 0 && len(body.URIs) > 0 {
				firstURI = body.URIs[0]
			}
			batchSizes = append(batchSizes, len(body.URIs))
			mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			writeJSON(t, w, map[string]any{"snapshot_id": "snap-1"})

		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer server.Close()

	p := newTestProvider(t, server.URL, validAuth(), nil)

	// 205 addable tracks plus one local file (empty ID) that must be skipped.
	tracks := make([]providers.Track, 0, 206)
	for i := 0; i < 205; i++ {
		tracks = append(tracks, providers.Track{ID: fmt.Sprintf("t%03d", i), Name: fmt.Sprintf("Song %d", i)})
	}
	tracks = append(tracks, providers.Track{ID: "", Name: "Local File"})

	playlistID, err := p.createPlaylist(context.Background(), "Test Mix", "A description", tracks)
	require.NoError(t, err)
	assert.Equal(t, "new-playlist-id", playlistID)

	assert.Equal(t, "Test Mix", createBody["name"])
	assert.Equal(t, "A description", createBody["description"])
	assert.Equal(t, false, createBody["public"], "playlists must be created private")

	assert.Equal(t, []int{100, 100, 5}, batchSizes, "URIs must be added in batches of at most 100")
	assert.Equal(t, "spotify:track:t000", firstURI)

	// The exported interface method must work too.
	err = p.CreatePlaylist(context.Background(), "Test Mix", "A description", tracks[:1])
	assert.NoError(t, err)
}

func TestCreatePlaylist_EmptyTracks(t *testing.T) {
	p := newTestProvider(t, "http://127.0.0.1:0", validAuth(), nil)
	err := p.CreatePlaylist(context.Background(), "Empty", "Nope", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot create empty playlist")
}

// Governing: SPEC error-handling REQ-ERR-002 Scenario 4 (single refresh+retry on mid-operation 401)
func Test401_TriggersExactlyOneRefreshAndRetry(t *testing.T) {
	t.Run("RetrySucceedsAfterRefresh", func(t *testing.T) {
		client := enttest.Open(t, "sqlite3", "file:spotify_prov_401?mode=memory&cache=shared&_fk=1")
		t.Cleanup(func() { client.Close() })
		ctx := context.Background()

		user, err := client.User.Create().SetUsername("testuser").Save(ctx)
		require.NoError(t, err)
		auth, err := client.SpotifyAuth.Create().
			SetAccessToken("revoked-token").
			SetRefreshToken("refresh-token").
			SetExpiry(time.Now().Add(time.Hour)). // looks valid locally, revoked server-side
			SetUser(user).
			Save(ctx)
		require.NoError(t, err)

		var mu sync.Mutex
		tokenCalls := 0
		historyCalls := 0

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/token":
				mu.Lock()
				tokenCalls++
				mu.Unlock()
				writeJSON(t, w, map[string]any{
					"access_token": "fresh-token",
					"expires_in":   3600,
					"token_type":   "Bearer",
				})
			case "/v1/me":
				writeJSON(t, w, map[string]any{"id": "user-123"})
			case "/v1/me/player/recently-played":
				mu.Lock()
				historyCalls++
				mu.Unlock()
				if r.Header.Get("Authorization") != "Bearer fresh-token" {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				writeJSON(t, w, map[string]any{"items": []any{}})
			default:
				http.Error(w, "not found", http.StatusNotFound)
			}
		}))
		defer server.Close()

		p := newTestProvider(t, server.URL, auth, client)

		err = p.GetRecentListens(ctx, time.Now().Add(-24*time.Hour), func([]providers.Track) error {
			return nil
		})
		require.NoError(t, err)

		assert.Equal(t, 1, tokenCalls, "401 must trigger exactly one token refresh")
		assert.Equal(t, 2, historyCalls, "request must be retried exactly once after refresh")

		stored, err := client.SpotifyAuth.Get(ctx, auth.ID)
		require.NoError(t, err)
		assert.Equal(t, "fresh-token", stored.AccessToken, "token refreshed on 401 must be persisted")
	})

	t.Run("PersistentUnauthorizedFailsAfterOneRetry", func(t *testing.T) {
		var mu sync.Mutex
		tokenCalls := 0
		historyCalls := 0

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/token":
				mu.Lock()
				tokenCalls++
				mu.Unlock()
				writeJSON(t, w, map[string]any{
					"access_token": "fresh-token",
					"expires_in":   3600,
					"token_type":   "Bearer",
				})
			case "/v1/me":
				writeJSON(t, w, map[string]any{"id": "user-123"})
			case "/v1/me/player/recently-played":
				mu.Lock()
				historyCalls++
				mu.Unlock()
				w.WriteHeader(http.StatusUnauthorized)
			default:
				http.Error(w, "not found", http.StatusNotFound)
			}
		}))
		defer server.Close()

		p := newTestProvider(t, server.URL, validAuth(), nil)

		err := p.GetRecentListens(context.Background(), time.Now().Add(-24*time.Hour), func([]providers.Track) error {
			return nil
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "401")
		assert.Equal(t, 1, tokenCalls, "only one refresh attempt is allowed")
		assert.Equal(t, 2, historyCalls, "only one retry is allowed")
	})
}

// Governing: SPEC music-provider-integration REQ-PROV-022 (ISRC requested and mapped for playlist tracks)
func TestGetPlaylists_TracksIncludeISRC(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/me/playlists":
			writeJSON(t, w, map[string]any{
				"items": []map[string]any{
					{
						"id":            "playlist-1",
						"name":          "My Playlist",
						"description":   "desc",
						"external_urls": map[string]any{"spotify": "https://open.spotify.com/playlist/playlist-1"},
						"tracks":        map[string]any{"total": 1},
					},
				},
				"next": nil,
			})
		case "/v1/playlists/playlist-1/tracks":
			assert.True(t, strings.Contains(r.URL.Query().Get("fields"), "external_ids"),
				"fields selector must request external_ids for ISRC")
			writeJSON(t, w, map[string]any{
				"items": []map[string]any{
					{
						"track": map[string]any{
							"id":            "track-1",
							"name":          "Song 1",
							"duration_ms":   180000,
							"artists":       []map[string]any{{"name": "Artist A"}},
							"album":         map[string]any{"name": "Album X"},
							"external_urls": map[string]any{"spotify": "https://open.spotify.com/track/track-1"},
							"external_ids":  map[string]any{"isrc": "GBXYZ2600042"},
						},
					},
				},
				"next": nil,
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer server.Close()

	p := newTestProvider(t, server.URL, validAuth(), nil)

	playlists, err := p.GetPlaylists(context.Background())
	require.NoError(t, err)
	require.Len(t, playlists, 1)
	require.Len(t, playlists[0].Tracks, 1)
	assert.Equal(t, "GBXYZ2600042", playlists[0].Tracks[0].ISRC)
}
