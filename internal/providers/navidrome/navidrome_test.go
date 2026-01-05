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
	var tracks []providers.Track
	err = fetcher.GetRecentListens(context.Background(), since, func(listens []providers.Track) error {
		tracks = append(tracks, listens...)
		return nil
	})
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

func TestGetPlaylists(t *testing.T) {
	// Mock Navidrome Server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify standard parameters
		q := r.URL.Query()
		if q.Get("u") != "testuser" {
			http.Error(w, "invalid user", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")

		// Response for getPlaylists
		if r.URL.Path == "/rest/getPlaylists.view" {
			resp := `{
				"subsonic-response": {
					"status": "ok",
					"playlists": {
						"playlist": [
							{
								"id": "pl-1",
								"name": "My Playlist",
								"comment": "A great playlist",
								"coverArt": "pl-1",
								"songCount": 3,
								"duration": 600,
								"public": true,
								"owner": "testuser",
								"created": "2024-01-01T00:00:00Z"
							},
							{
								"id": "pl-2",
								"name": "Another Playlist",
								"comment": "",
								"coverArt": "",
								"songCount": 0,
								"duration": 0,
								"public": false,
								"owner": "testuser",
								"created": "2024-01-02T00:00:00Z"
							}
						]
					}
				}
			}`
			io.WriteString(w, resp)
			return
		}

		// Response for getPlaylist (single playlist details)
		if r.URL.Path == "/rest/getPlaylist.view" {
			playlistID := q.Get("id")
			if playlistID == "pl-1" {
				resp := `{
					"subsonic-response": {
						"status": "ok",
						"playlist": {
							"id": "pl-1",
							"name": "My Playlist",
							"entry": [
								{"id": "t1", "artist": "Artist A", "album": "Album X"},
								{"id": "t2", "artist": "Artist B", "album": "Album X"},
								{"id": "t3", "artist": "Artist A", "album": "Album Y"}
							]
						}
					}
				}`
				io.WriteString(w, resp)
				return
			}
			if playlistID == "pl-2" {
				resp := `{
					"subsonic-response": {
						"status": "ok",
						"playlist": {
							"id": "pl-2",
							"name": "Another Playlist",
							"entry": []
						}
					}
				}`
				io.WriteString(w, resp)
				return
			}
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

	// Test GetPlaylists
	manager, ok := provider.(providers.PlaylistManager)
	assert.True(t, ok, "Provider should implement PlaylistManager")

	playlists, err := manager.GetPlaylists(context.Background())
	assert.NoError(t, err)
	assert.Len(t, playlists, 2)

	// Check first playlist
	assert.Equal(t, "pl-1", playlists[0].ID)
	assert.Equal(t, "My Playlist", playlists[0].Name)
	assert.Equal(t, "A great playlist", playlists[0].Description)
	assert.Equal(t, 3, playlists[0].TrackCount)
	assert.Equal(t, 2, playlists[0].UniqueArtists) // Artist A, Artist B
	assert.Equal(t, 2, playlists[0].UniqueAlbums)  // Album X, Album Y
	assert.Contains(t, playlists[0].ImageURL, "getCoverArt.view")
	assert.Contains(t, playlists[0].ExternalURL, "/app/#/playlist/pl-1")

	// Check second playlist (empty)
	assert.Equal(t, "pl-2", playlists[1].ID)
	assert.Equal(t, "Another Playlist", playlists[1].Name)
	assert.Equal(t, 0, playlists[1].TrackCount)
	assert.Equal(t, 0, playlists[1].UniqueArtists)
	assert.Equal(t, 0, playlists[1].UniqueAlbums)
}

func TestProviderImplementsInterfaces(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{}
	cfg.Navidrome.BaseURL = "http://localhost:4533"

	user := &ent.User{
		Username: "testuser",
		Edges: ent.UserEdges{
			NavidromeAuth: &ent.NavidromeAuth{
				Password: "password123",
			},
		},
	}

	factory := navidrome.New(logger, cfg)
	provider, err := factory(context.Background(), user)
	assert.NoError(t, err)
	assert.NotNil(t, provider)

	// Check that provider implements required interfaces
	_, ok := provider.(providers.HistoryFetcher)
	assert.True(t, ok, "Provider should implement HistoryFetcher")

	_, ok = provider.(providers.PlaylistManager)
	assert.True(t, ok, "Provider should implement PlaylistManager")

	_, ok = provider.(providers.Authenticator)
	assert.True(t, ok, "Provider should implement Authenticator")
}

func TestPlaylistStatsDeduplication(t *testing.T) {
	// This tests the expected deduplication logic for artists and albums
	artists := make(map[string]struct{})
	albums := make(map[string]struct{})

	// Simulate adding tracks with duplicate artists and albums
	entries := []struct {
		artist string
		album  string
	}{
		{"Artist A", "Album 1"},
		{"Artist B", "Album 1"},
		{"Artist A", "Album 2"}, // Duplicate artist
		{"Artist C", "Album 1"}, // Duplicate album
		{"Artist A", "Album 1"}, // Both duplicates
	}

	for _, entry := range entries {
		if entry.artist != "" {
			artists[entry.artist] = struct{}{}
		}
		if entry.album != "" {
			albums[entry.album] = struct{}{}
		}
	}

	// Should have 3 unique artists (A, B, C) and 2 unique albums (1, 2)
	assert.Equal(t, 3, len(artists), "Should have 3 unique artists")
	assert.Equal(t, 2, len(albums), "Should have 2 unique albums")
}

func TestProviderImplementsPlaylistSyncer(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{}
	cfg.Navidrome.BaseURL = "http://localhost:4533"

	user := &ent.User{
		Username: "testuser",
		Edges: ent.UserEdges{
			NavidromeAuth: &ent.NavidromeAuth{
				Password: "password123",
			},
		},
	}

	factory := navidrome.New(logger, cfg)
	provider, err := factory(context.Background(), user)
	assert.NoError(t, err)
	assert.NotNil(t, provider)

	// Check that provider implements PlaylistSyncer
	_, ok := provider.(providers.PlaylistSyncer)
	assert.True(t, ok, "Provider should implement PlaylistSyncer")
}

func TestSyncPlaylist_Create(t *testing.T) {
	// Mock Navidrome Server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify standard parameters
		q := r.URL.Query()
		if q.Get("u") != "testuser" {
			http.Error(w, "invalid user", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")

		// Response for createPlaylist
		if r.URL.Path == "/rest/createPlaylist.view" {
			// Verify expected parameters
			name := q.Get("name")
			assert.NotEmpty(t, name, "Playlist name should be provided")

			// Check song IDs are present
			songIDs := q["songId"]
			assert.Len(t, songIDs, 2, "Should have 2 song IDs")

			resp := `{
				"subsonic-response": {
					"status": "ok",
					"playlist": {
						"id": "new-playlist-123"
					}
				}
			}`
			io.WriteString(w, resp)
			return
		}

		// Response for updatePlaylist (for setting description)
		if r.URL.Path == "/rest/updatePlaylist.view" {
			resp := `{
				"subsonic-response": {
					"status": "ok"
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

	// Test SyncPlaylist
	syncer, ok := provider.(providers.PlaylistSyncer)
	assert.True(t, ok, "Provider should implement PlaylistSyncer")

	playlistID, err := syncer.SyncPlaylist(context.Background(), providers.SyncPlaylistRequest{
		Name:        "Test Sync Playlist",
		Description: "A synced playlist",
		Tracks: []providers.Track{
			{ID: "track-1", Name: "Song One", Artist: "Artist A"},
			{ID: "track-2", Name: "Song Two", Artist: "Artist B"},
		},
	})
	assert.NoError(t, err)
	assert.Equal(t, "new-playlist-123", playlistID)
}

func TestDeletePlaylist_Success(t *testing.T) {
	deleteCalled := false

	// Mock Navidrome Server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("u") != "testuser" {
			http.Error(w, "invalid user", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")

		// Response for deletePlaylist
		if r.URL.Path == "/rest/deletePlaylist.view" {
			deleteCalled = true
			playlistID := q.Get("id")
			assert.Equal(t, "playlist-to-delete", playlistID)

			resp := `{
				"subsonic-response": {
					"status": "ok"
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

	// Test DeletePlaylist
	syncer, ok := provider.(providers.PlaylistSyncer)
	assert.True(t, ok)

	err = syncer.DeletePlaylist(context.Background(), "playlist-to-delete")
	assert.NoError(t, err)
	assert.True(t, deleteCalled, "Delete API should have been called")
}

func TestDeletePlaylist_NotFound(t *testing.T) {
	// Mock Navidrome Server that returns error
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if r.URL.Path == "/rest/deletePlaylist.view" {
			resp := `{
				"subsonic-response": {
					"status": "failed",
					"error": {
						"code": 70,
						"message": "Playlist not found"
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

	// Test DeletePlaylist with non-existent playlist
	syncer, ok := provider.(providers.PlaylistSyncer)
	assert.True(t, ok)

	err = syncer.DeletePlaylist(context.Background(), "non-existent-playlist")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "Playlist not found")
}

func TestUpdatePlaylistTracks_Success(t *testing.T) {
	updateCalled := false
	var removedIndices []string
	var addedSongIDs []string

	// Mock Navidrome Server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("u") != "testuser" {
			http.Error(w, "invalid user", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")

		// Response for getPlaylist (to get current tracks)
		if r.URL.Path == "/rest/getPlaylist.view" {
			resp := `{
				"subsonic-response": {
					"status": "ok",
					"playlist": {
						"id": "existing-playlist",
						"name": "Existing Playlist",
						"entry": [
							{"id": "old-track-1", "title": "Old Song 1", "artist": "Old Artist"},
							{"id": "old-track-2", "title": "Old Song 2", "artist": "Old Artist"}
						]
					}
				}
			}`
			io.WriteString(w, resp)
			return
		}

		// Response for updatePlaylist
		if r.URL.Path == "/rest/updatePlaylist.view" {
			updateCalled = true
			removedIndices = q["songIndexToRemove"]
			addedSongIDs = q["songIdToAdd"]

			resp := `{
				"subsonic-response": {
					"status": "ok"
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

	// Test UpdatePlaylistTracks
	syncer, ok := provider.(providers.PlaylistSyncer)
	assert.True(t, ok)

	newTracks := []providers.Track{
		{ID: "new-track-1", Name: "New Song 1", Artist: "New Artist"},
		{ID: "new-track-2", Name: "New Song 2", Artist: "New Artist"},
		{ID: "new-track-3", Name: "New Song 3", Artist: "New Artist"},
	}

	err = syncer.UpdatePlaylistTracks(context.Background(), "existing-playlist", newTracks)
	assert.NoError(t, err)
	assert.True(t, updateCalled, "Update API should have been called")

	// Should remove old tracks (indices 1 and 0, in reverse order)
	assert.Len(t, removedIndices, 2, "Should remove 2 old tracks")

	// Should add new tracks
	assert.Len(t, addedSongIDs, 3, "Should add 3 new tracks")
	assert.Contains(t, addedSongIDs, "new-track-1")
	assert.Contains(t, addedSongIDs, "new-track-2")
	assert.Contains(t, addedSongIDs, "new-track-3")
}

func TestSyncPlaylist_ApiError(t *testing.T) {
	// Mock Navidrome Server that returns error
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if r.URL.Path == "/rest/createPlaylist.view" {
			resp := `{
				"subsonic-response": {
					"status": "failed",
					"error": {
						"code": 50,
						"message": "Permission denied"
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

	// Test SyncPlaylist with API error
	syncer, ok := provider.(providers.PlaylistSyncer)
	assert.True(t, ok)

	_, err = syncer.SyncPlaylist(context.Background(), providers.SyncPlaylistRequest{
		Name:   "Test Playlist",
		Tracks: []providers.Track{{ID: "track-1"}},
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "Permission denied")
}

func TestSyncPlaylist_EmptyTracks(t *testing.T) {
	// Mock Navidrome Server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if r.URL.Path == "/rest/createPlaylist.view" {
			resp := `{
				"subsonic-response": {
					"status": "ok",
					"playlist": {
						"id": "empty-playlist-123"
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

	// Test SyncPlaylist with empty tracks
	syncer, ok := provider.(providers.PlaylistSyncer)
	assert.True(t, ok)

	playlistID, err := syncer.SyncPlaylist(context.Background(), providers.SyncPlaylistRequest{
		Name:   "Empty Playlist",
		Tracks: []providers.Track{},
	})
	assert.NoError(t, err)
	assert.Equal(t, "empty-playlist-123", playlistID)
}
