// Governing: SPEC music-provider-integration REQ-PROV-013, SPEC error-handling REQ-ERR-002
// Tests for token refresh persistence and 401 refresh-retry in the Spotify enricher.
package spotify

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"spotter/ent"
	"spotter/ent/enttest"
	"spotter/internal/config"
	"spotter/internal/enrichers"

	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
	spotifyOAuth "golang.org/x/oauth2/spotify"
)

// newConformanceEnricher builds an Enricher whose HTTP client rewrites every
// request (Web API and OAuth token endpoint alike) to the fake server.
func newConformanceEnricher(serverURL string, auth *ent.SpotifyAuth, db *ent.Client) *Enricher {
	cfg := &config.Config{}
	cfg.Spotify.ClientID = "test-client-id"
	cfg.Spotify.ClientSecret = "test-client-secret"

	return &Enricher{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		config: cfg,
		user:   &ent.User{ID: 1},
		auth:   auth,
		db:     db,
		oauth: &oauth2.Config{
			ClientID:     cfg.Spotify.ClientID,
			ClientSecret: cfg.Spotify.ClientSecret,
			Endpoint:     spotifyOAuth.Endpoint,
		},
		httpClient: &http.Client{
			Transport: &testTransport{baseURL: serverURL},
		},
	}
}

// Governing: SPEC music-provider-integration REQ-PROV-013 (refreshed tokens persisted)
func TestEnricherTokenRefresh_Persisted(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:spotify_enr_refresh?mode=memory&cache=shared&_fk=1")
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

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// spotifyOAuth.Endpoint token URL path
		if r.URL.Path == "/api/token" {
			mu.Lock()
			tokenCalls++
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "new-access",
				"refresh_token": "new-refresh",
				"expires_in":    3600,
				"token_type":    "Bearer",
			}))
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer server.Close()

	e := newConformanceEnricher(server.URL, auth, client)

	token, err := e.getValidToken(ctx)
	require.NoError(t, err)
	assert.Equal(t, "new-access", token)
	assert.Equal(t, 1, tokenCalls)

	stored, err := client.SpotifyAuth.Get(ctx, auth.ID)
	require.NoError(t, err)
	assert.Equal(t, "new-access", stored.AccessToken)
	assert.Equal(t, "new-refresh", stored.RefreshToken, "rotated refresh token must be persisted")
	assert.True(t, stored.Expiry.After(time.Now()))
}

// Governing: SPEC error-handling REQ-ERR-002 Scenario 4 (single refresh+retry on mid-operation 401)
func TestEnricherDoRequest_401RefreshRetry(t *testing.T) {
	t.Run("RetrySucceedsAfterRefresh", func(t *testing.T) {
		var mu sync.Mutex
		tokenCalls := 0
		searchCalls := 0

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/api/token":
				mu.Lock()
				tokenCalls++
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
					"access_token": "fresh-token",
					"expires_in":   3600,
					"token_type":   "Bearer",
				}))
			case "/v1/search":
				mu.Lock()
				searchCalls++
				mu.Unlock()
				if r.Header.Get("Authorization") != "Bearer fresh-token" {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				require.NoError(t, json.NewEncoder(w).Encode(searchResponse{}))
			default:
				http.Error(w, "not found", http.StatusNotFound)
			}
		}))
		defer server.Close()

		// Token looks valid locally but has been revoked server-side.
		auth := &ent.SpotifyAuth{
			AccessToken:  "revoked-token",
			RefreshToken: "refresh-token",
			Expiry:       time.Now().Add(time.Hour),
		}

		e := newConformanceEnricher(server.URL, auth, nil)
		var matcher enrichers.IDMatcher = e

		id, confidence, err := matcher.MatchArtist(context.Background(), "Radiohead")
		require.NoError(t, err)
		assert.Equal(t, "", id) // empty results, but no error
		assert.Equal(t, 0.0, confidence)

		assert.Equal(t, 1, tokenCalls, "401 must trigger exactly one token refresh")
		assert.Equal(t, 2, searchCalls, "request must be retried exactly once after refresh")
		assert.Equal(t, "fresh-token", auth.AccessToken, "in-memory auth must carry the new token")
	})

	t.Run("PersistentUnauthorizedFailsAfterOneRetry", func(t *testing.T) {
		var mu sync.Mutex
		tokenCalls := 0
		searchCalls := 0

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/api/token":
				mu.Lock()
				tokenCalls++
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
					"access_token": "fresh-token",
					"expires_in":   3600,
					"token_type":   "Bearer",
				}))
			case "/v1/search":
				mu.Lock()
				searchCalls++
				mu.Unlock()
				w.WriteHeader(http.StatusUnauthorized)
			default:
				http.Error(w, "not found", http.StatusNotFound)
			}
		}))
		defer server.Close()

		auth := &ent.SpotifyAuth{
			AccessToken:  "revoked-token",
			RefreshToken: "refresh-token",
			Expiry:       time.Now().Add(time.Hour),
		}

		e := newConformanceEnricher(server.URL, auth, nil)
		var matcher enrichers.IDMatcher = e

		_, _, err := matcher.MatchArtist(context.Background(), "Radiohead")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unauthorized")
		assert.Equal(t, 1, tokenCalls, "only one refresh attempt is allowed")
		assert.Equal(t, 2, searchCalls, "only one retry is allowed")
	})
}
