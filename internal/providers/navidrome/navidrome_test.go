package navidrome_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"spotter/ent"
	"spotter/internal/config"
	"spotter/internal/providers"
	"spotter/internal/providers/navidrome"

	"github.com/stretchr/testify/assert"
)

func TestGetRecentListens(t *testing.T) {
	// Mock Navidrome Server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify standard parameters
		q := r.URL.Query()
		if q.Get("u") != "testuser" {
			http.Error(w, "invalid user", http.StatusBadRequest)
			return
		}
		if q.Get("c") != "spotter" {
			http.Error(w, "invalid client", http.StatusBadRequest)
			return
		}

		// Response for getNowPlaying
		if r.URL.Path == "/rest/getNowPlaying.view" {
			w.Header().Set("Content-Type", "application/json")
			// Return a mocked response
			// One track played 5 minutes ago, one played 60 minutes ago
			resp := `{
				"subsonic-response": {
					"status": "ok",
					"nowPlaying": {
						"entry": [
							{
								"id": "1",
								"title": "Recent Track",
								"artist": "Test Artist",
								"album": "Test Album",
								"duration": 300,
								"minutesAgo": 5,
								"username": "testuser"
							},
							{
								"id": "2",
								"title": "Old Track",
								"artist": "Old Artist",
								"album": "Old Album",
								"duration": 300,
								"minutesAgo": 60,
								"username": "testuser"
							},
                            {
								"id": "3",
								"title": "Other User Track",
								"artist": "Other Artist",
								"album": "Other Album",
								"duration": 300,
								"minutesAgo": 2,
								"username": "otheruser"
							}
						]
					}
				}
			}`
			io.WriteString(w, resp)
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer ts.Close()

	// Setup Config
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{}
	cfg.Navidrome.BaseURL = ts.URL

	// Setup User & Auth
	user := &ent.User{
		Username: "testuser",
		Edges: ent.UserEdges{
			NavidromeAuth: &ent.NavidromeAuth{
				Password: "password123",
			},
		},
	}

	// Create Provider
	factory := navidrome.New(logger, cfg)
	provider, err := factory(context.Background(), user)
	assert.NoError(t, err)
	assert.NotNil(t, provider)

	// Assert Type
	assert.Equal(t, providers.TypeNavidrome, provider.Type())

	// Test GetRecentListens
	fetcher, ok := provider.(providers.HistoryFetcher)
	assert.True(t, ok)

	// Fetch tracks since 30 minutes ago
	since := time.Now().Add(-30 * time.Minute)
	tracks, err := fetcher.GetRecentListens(context.Background(), since)
	assert.NoError(t, err)

	// Should only find "Recent Track" (5 mins ago).
	// "Old Track" is 60 mins ago (older than 30).
	// "Other User Track" is username mismatch.
	assert.Len(t, tracks, 1)
	assert.Equal(t, "Recent Track", tracks[0].Name)
	assert.Equal(t, "Test Artist", tracks[0].Artist)
	assert.Equal(t, "1", tracks[0].ID)
}

func TestNewFactory_NoAuth(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{}

	user := &ent.User{
		Username: "testuser",
		// No NavidromeAuth edge
	}

	factory := navidrome.New(logger, cfg)
	provider, err := factory(context.Background(), user)
	assert.NoError(t, err)
	assert.Nil(t, provider)
}
