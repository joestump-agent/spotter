package lidarr

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"spotter/ent"
	"spotter/internal/config"
	"spotter/internal/enrichers"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// discardLogger returns a no-op logger suitable for tests that exercise error
// paths which call e.logger.Error/Warn (passing nil panics in those paths).
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestNew(t *testing.T) {
	cfg := &config.Config{}
	cfg.Lidarr.BaseURL = "http://localhost:8686"
	cfg.Lidarr.APIKey = "test-api-key"

	factory := New(nil, cfg, nil)
	enricher, err := factory(context.Background(), nil)
	require.NoError(t, err)
	require.NotNil(t, enricher)

	assert.Equal(t, enrichers.TypeLidarr, enricher.Type())
	assert.Equal(t, "Lidarr", enricher.Name())
	assert.True(t, enricher.IsAvailable())
}

func TestEnrichArtist_Found(t *testing.T) {
	// Mock Lidarr API
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "test-api-key", r.Header.Get("X-Api-Key"))

		// Mock artist search by MBID
		if r.URL.Path == "/api/v1/artist" {
			artists := []lidarrArtist{
				{
					ID:              123,
					ArtistName:      "Test Artist",
					ForeignArtistID: "mbid-123",
					Overview:        "Test Bio",
					Genres:          []string{"Rock"},
				},
			}
			json.NewEncoder(w).Encode(artists)
			return
		}

		http.NotFound(w, r)
	}))
	defer server.Close()

	cfg := &config.Config{}
	cfg.Lidarr.BaseURL = server.URL
	cfg.Lidarr.APIKey = "test-api-key"

	factory := New(nil, cfg, nil)
	enricher, err := factory(context.Background(), nil)
	require.NoError(t, err)

	mbid := "mbid-123"
	artist := &ent.Artist{
		Name:          "Test Artist",
		MusicbrainzID: mbid,
	}

	artistEnricher := enricher.(enrichers.ArtistEnricher)
	data, err := artistEnricher.EnrichArtist(context.Background(), artist)
	require.NoError(t, err)
	require.NotNil(t, data)

	assert.Equal(t, "123", data.LidarrID)
	assert.Equal(t, "mbid-123", data.MusicBrainzID)
	assert.Equal(t, "Test Bio", data.Bio)
	assert.Contains(t, data.Genres, "Rock")
}

func TestEnrichArtist_NotFound_Add(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 1. Initial Search by MBID (returns empty)
		if r.URL.Path == "/api/v1/artist" && r.Method == "GET" {
			json.NewEncoder(w).Encode([]lidarrArtist{})
			return
		}

		// 2. Search by Name (returns empty)
		if r.URL.Path == "/api/v1/artist/lookup" && r.Method == "GET" && r.URL.Query().Get("term") == "Test Artist" {
			json.NewEncoder(w).Encode([]lidarrArtist{})
			return
		}

		// 2b. Search by MBID lookup (for adding)
		if r.URL.Path == "/api/v1/artist/lookup" && r.Method == "GET" && r.URL.Query().Get("term") == "lidarr:mbid-123" {
			artists := []lidarrArtist{
				{
					ArtistName:      "Test Artist",
					ForeignArtistID: "mbid-123",
				},
			}
			json.NewEncoder(w).Encode(artists)
			return
		}

		// 3. Get Root Folders
		if r.URL.Path == "/api/v1/rootfolder" {
			folders := []struct {
				Path string `json:"path"`
			}{{Path: "/music"}}
			json.NewEncoder(w).Encode(folders)
			return
		}

		// 4. Get Quality Profiles (optional, but code does it)
		if r.URL.Path == "/api/v1/qualityprofile" {
			json.NewEncoder(w).Encode([]interface{}{}) // Return empty or mock
			return
		}
		// 5. Get Metadata Profiles (optional)
		if r.URL.Path == "/api/v1/metadataprofile" {
			json.NewEncoder(w).Encode([]interface{}{})
			return
		}

		// 6. Add Artist (POST)
		if r.URL.Path == "/api/v1/artist" && r.Method == "POST" {
			var payload map[string]interface{}
			json.NewDecoder(r.Body).Decode(&payload)
			assert.Equal(t, "Test Artist", payload["artistName"])

			resp := lidarrArtist{
				ID:              456,
				ArtistName:      "Test Artist",
				ForeignArtistID: "mbid-123",
			}
			json.NewEncoder(w).Encode(resp)
			return
		}

		http.Error(w, fmt.Sprintf("Unexpected request: %s %s", r.Method, r.URL.String()), http.StatusNotFound)
	}))
	defer server.Close()

	cfg := &config.Config{}
	cfg.Lidarr.BaseURL = server.URL
	cfg.Lidarr.APIKey = "test-api-key"

	factory := New(nil, cfg, nil)
	enricher, err := factory(context.Background(), nil)
	require.NoError(t, err)

	mbid := "mbid-123"
	artist := &ent.Artist{
		Name:          "Test Artist",
		MusicbrainzID: mbid,
	}

	artistEnricher := enricher.(enrichers.ArtistEnricher)
	data, err := artistEnricher.EnrichArtist(context.Background(), artist)
	require.NoError(t, err)
	require.NotNil(t, data)

	assert.Equal(t, "456", data.LidarrID)
}

func TestEnrichAlbum_Found(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 1. Find Artist
		if r.URL.Path == "/api/v1/artist" {
			artists := []lidarrArtist{
				{
					ID:              10,
					ArtistName:      "Test Artist",
					ForeignArtistID: "mbid-artist",
				},
			}
			json.NewEncoder(w).Encode(artists)
			return
		}

		// 2. Find Album (by Artist ID)
		if r.URL.Path == "/api/v1/album" {
			assert.Equal(t, "10", r.URL.Query().Get("artistId"))
			albums := []lidarrAlbum{
				{
					ID:             20,
					Title:          "Test Album",
					ForeignAlbumID: "mbid-album",
					ReleaseDate:    "2023-01-01",
					Genres:         []string{"Pop"},
					AlbumType:      "Album",
				},
			}
			json.NewEncoder(w).Encode(albums)
			return
		}

		http.NotFound(w, r)
	}))
	defer server.Close()

	cfg := &config.Config{}
	cfg.Lidarr.BaseURL = server.URL
	cfg.Lidarr.APIKey = "test-api-key"

	factory := New(nil, cfg, nil)
	enricher, err := factory(context.Background(), nil)
	require.NoError(t, err)

	artistMbid := "mbid-artist"
	albumMbid := "mbid-album"
	album := &ent.Album{
		Name:          "Test Album",
		MusicbrainzID: albumMbid,
		Edges: ent.AlbumEdges{
			Artist: &ent.Artist{
				Name:          "Test Artist",
				MusicbrainzID: artistMbid,
			},
		},
	}

	albumEnricher := enricher.(enrichers.AlbumEnricher)
	data, err := albumEnricher.EnrichAlbum(context.Background(), album)
	require.NoError(t, err)
	require.NotNil(t, data)

	assert.Equal(t, "20", data.LidarrID)
	assert.Equal(t, "mbid-album", data.MusicBrainzID)
	assert.Equal(t, 2023, data.Year)
}

// TestEnrichTrack_AlbumFullyDownloaded verifies that when the album is in
// Lidarr and all tracks are downloaded (trackFileCount >= totalTrackCount),
// EnrichTrack reports "available".
func TestEnrichTrack_AlbumFullyDownloaded(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/artist" {
			json.NewEncoder(w).Encode([]lidarrArtist{
				{ID: 10, ArtistName: "Test Artist", ForeignArtistID: "mbid-artist"},
			})
			return
		}
		if r.URL.Path == "/api/v1/album" {
			json.NewEncoder(w).Encode([]lidarrAlbum{
				{
					ID:             20,
					ArtistID:       10,
					Title:          "Test Album",
					ForeignAlbumID: "mbid-album",
					Statistics: lidarrAlbumStatistics{
						TrackFileCount:  12,
						TotalTrackCount: 12,
						PercentOfTracks: 100.0,
					},
				},
			})
			return
		}
		// /api/v1/track should NOT be called — status is derived from album.
		if r.URL.Path == "/api/v1/track" {
			t.Error("EnrichTrack must not call /api/v1/track — Lidarr tracks at album level")
			http.Error(w, "should not be called", http.StatusInternalServerError)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	cfg := &config.Config{}
	cfg.Lidarr.BaseURL = server.URL
	cfg.Lidarr.APIKey = "test-api-key"

	factory := New(nil, cfg, nil)
	enricher, err := factory(context.Background(), nil)
	require.NoError(t, err)

	tn := 1
	track := &ent.Track{
		Name:        "Test Track",
		TrackNumber: &tn,
		Edges: ent.TrackEdges{
			Album: &ent.Album{
				Name:          "Test Album",
				MusicbrainzID: "mbid-album",
				Edges: ent.AlbumEdges{
					Artist: &ent.Artist{Name: "Test Artist", MusicbrainzID: "mbid-artist"},
				},
			},
		},
	}

	trackEnricher := enricher.(enrichers.TrackEnricher)
	data, err := trackEnricher.EnrichTrack(context.Background(), track)
	require.NoError(t, err)
	require.NotNil(t, data)

	assert.Equal(t, "available", data.LidarrStatus)
}

// TestEnrichTrack_AlbumMonitored verifies that when the album is in Lidarr
// but not fully downloaded, EnrichTrack reports "monitored".
func TestEnrichTrack_AlbumMonitored(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/artist" {
			json.NewEncoder(w).Encode([]lidarrArtist{
				{ID: 10, ArtistName: "Test Artist", ForeignArtistID: "mbid-artist"},
			})
			return
		}
		if r.URL.Path == "/api/v1/album" {
			json.NewEncoder(w).Encode([]lidarrAlbum{
				{
					ID:             20,
					ArtistID:       10,
					Title:          "Test Album",
					ForeignAlbumID: "mbid-album",
					Statistics: lidarrAlbumStatistics{
						TrackFileCount:  5,
						TotalTrackCount: 12,
						PercentOfTracks: 41.7,
					},
				},
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	cfg := &config.Config{}
	cfg.Lidarr.BaseURL = server.URL
	cfg.Lidarr.APIKey = "test-api-key"

	factory := New(nil, cfg, nil)
	enricher, err := factory(context.Background(), nil)
	require.NoError(t, err)

	tn := 1
	track := &ent.Track{
		Name:        "Test Track",
		TrackNumber: &tn,
		Edges: ent.TrackEdges{
			Album: &ent.Album{
				Name:          "Test Album",
				MusicbrainzID: "mbid-album",
				Edges: ent.AlbumEdges{
					Artist: &ent.Artist{Name: "Test Artist", MusicbrainzID: "mbid-artist"},
				},
			},
		},
	}

	trackEnricher := enricher.(enrichers.TrackEnricher)
	data, err := trackEnricher.EnrichTrack(context.Background(), track)
	require.NoError(t, err)
	require.NotNil(t, data)

	assert.Equal(t, "monitored", data.LidarrStatus)
}

// TestEnrichArtist_NoMBID_FoundByName verifies that an artist without a
// MusicbrainzID can still be found in Lidarr via a name-based lookup.
func TestEnrichArtist_NoMBID_FoundByName(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// MBID step is skipped (no MBID), so only the lookup path fires.
		if r.URL.Path == "/api/v1/artist/lookup" {
			assert.Equal(t, "Test Artist", r.URL.Query().Get("term"))
			artists := []lidarrArtist{
				{
					ID:              77,
					ArtistName:      "Test Artist",
					ForeignArtistID: "mbid-from-lidarr",
					Overview:        "Name-matched bio",
					Genres:          []string{"Jazz"},
				},
			}
			json.NewEncoder(w).Encode(artists)
			return
		}

		http.Error(w, fmt.Sprintf("unexpected: %s %s", r.Method, r.URL.Path), http.StatusNotFound)
	}))
	defer server.Close()

	cfg := &config.Config{}
	cfg.Lidarr.BaseURL = server.URL
	cfg.Lidarr.APIKey = "test-api-key"

	factory := New(nil, cfg, nil)
	enricher, err := factory(context.Background(), nil)
	require.NoError(t, err)

	// Artist has no MusicbrainzID — must be matched by name.
	artist := &ent.Artist{Name: "Test Artist"}

	artistEnricher := enricher.(enrichers.ArtistEnricher)
	data, err := artistEnricher.EnrichArtist(context.Background(), artist)
	require.NoError(t, err)
	require.NotNil(t, data)

	assert.Equal(t, "77", data.LidarrID)
	assert.Equal(t, "mbid-from-lidarr", data.MusicBrainzID)
	assert.Equal(t, "Name-matched bio", data.Bio)
}

// TestEnrichAlbum_ArtistWithoutMBID verifies that album matching works even
// when the artist has no MusicbrainzID (name-based artist lookup).
// This is the common real-world path: Lidarr → Navidrome → Spotter, where
// the artist/album may not have MBIDs populated yet.
func TestEnrichAlbum_ArtistWithoutMBID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Artist lookup by name (no MBID)
		if r.URL.Path == "/api/v1/artist/lookup" {
			artists := []lidarrArtist{
				{ID: 10, ArtistName: "No MBID Artist", ForeignArtistID: "mbid-discovered"},
			}
			json.NewEncoder(w).Encode(artists)
			return
		}

		// Album lookup by artist ID
		if r.URL.Path == "/api/v1/album" {
			assert.Equal(t, "10", r.URL.Query().Get("artistId"))
			albums := []lidarrAlbum{
				{
					ID:             25,
					Title:          "Name-Only Album",
					ForeignAlbumID: "mbid-album-x",
					ReleaseDate:    "2020-06-15",
					AlbumType:      "Album",
				},
			}
			json.NewEncoder(w).Encode(albums)
			return
		}

		http.Error(w, fmt.Sprintf("unexpected: %s %s", r.Method, r.URL.Path), http.StatusNotFound)
	}))
	defer server.Close()

	cfg := &config.Config{}
	cfg.Lidarr.BaseURL = server.URL
	cfg.Lidarr.APIKey = "test-api-key"

	factory := New(nil, cfg, nil)
	enricher, err := factory(context.Background(), nil)
	require.NoError(t, err)

	album := &ent.Album{
		Name: "Name-Only Album",
		Edges: ent.AlbumEdges{
			// Artist has NO MusicbrainzID — previously this caused findAlbum to return nil
			Artist: &ent.Artist{Name: "No MBID Artist"},
		},
	}

	albumEnricher := enricher.(enrichers.AlbumEnricher)
	data, err := albumEnricher.EnrichAlbum(context.Background(), album)
	require.NoError(t, err)
	require.NotNil(t, data, "album should be found even when artist has no MusicbrainzID")

	assert.Equal(t, "25", data.LidarrID)
	assert.Equal(t, "mbid-album-x", data.MusicBrainzID)
	assert.Equal(t, 2020, data.Year)
}

// TestEnrichTrack_NeverCallsTrackEndpoint verifies that EnrichTrack derives
// status from the album, never from /api/v1/track. Lidarr only allows
// requesting at album/artist level so per-track lookups are both unreliable
// (disc-format track numbers) and unnecessary.
//
// Regression: previously "Amazing" from Big Ones showed "available" while
// "Blind Man" showed "pending" because disc-format track numbers ("1-04")
// didn't match our stored integer "4", making findTrack return nil.
func TestEnrichTrack_NeverCallsTrackEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/artist" {
			json.NewEncoder(w).Encode([]lidarrArtist{
				{ID: 10, ArtistName: "Aerosmith", ForeignArtistID: "mbid-artist"},
			})
			return
		}
		if r.URL.Path == "/api/v1/album" {
			json.NewEncoder(w).Encode([]lidarrAlbum{
				{
					ID: 20, ArtistID: 10, Title: "Big Ones", ForeignAlbumID: "mbid-album",
					Statistics: lidarrAlbumStatistics{TrackFileCount: 20, TotalTrackCount: 20},
				},
			})
			return
		}
		if r.URL.Path == "/api/v1/track" {
			t.Error("EnrichTrack must not call /api/v1/track — status is album-level")
			http.Error(w, "should not be called", http.StatusInternalServerError)
			return
		}
		http.Error(w, fmt.Sprintf("unexpected: %s %s", r.Method, r.URL.Path), http.StatusNotFound)
	}))
	defer server.Close()

	cfg := &config.Config{}
	cfg.Lidarr.BaseURL = server.URL
	cfg.Lidarr.APIKey = "test-api-key"

	factory := New(nil, cfg, nil)
	enricher, err := factory(context.Background(), nil)
	require.NoError(t, err)

	// All tracks from Big Ones must show the same album-level status.
	for _, trackName := range []string{"Amazing", "Blind Man", "Crazy"} {
		tn := 1
		track := &ent.Track{
			Name:        trackName,
			TrackNumber: &tn,
			Edges: ent.TrackEdges{
				Album: &ent.Album{
					Name:          "Big Ones",
					MusicbrainzID: "mbid-album",
					Edges: ent.AlbumEdges{
						Artist: &ent.Artist{Name: "Aerosmith", MusicbrainzID: "mbid-artist"},
					},
				},
			},
		}
		trackEnricher := enricher.(enrichers.TrackEnricher)
		data, err := trackEnricher.EnrichTrack(context.Background(), track)
		require.NoError(t, err)
		require.NotNil(t, data)
		assert.Equal(t, "available", data.LidarrStatus,
			"all tracks from the same album must show the same Lidarr status (%s)", trackName)
	}
}

// TestEnrichTrack_AlbumNotInLidarr verifies that when the album is genuinely
// absent from Lidarr, the status remains "pending" (the pre-existing behavior).
func TestEnrichTrack_AlbumNotInLidarr(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/artist" {
			json.NewEncoder(w).Encode([]lidarrArtist{
				{ID: 10, ArtistName: "New Artist", ForeignArtistID: "mbid-artist"},
			})
			return
		}
		if r.URL.Path == "/api/v1/album" && r.Method == "GET" {
			// Album is NOT in Lidarr yet.
			json.NewEncoder(w).Encode([]lidarrAlbum{})
			return
		}
		// EnrichAlbum will try album/lookup before adding — return empty so it
		// errors gracefully (requires a non-nil logger, see discardLogger below).
		if r.URL.Path == "/api/v1/album/lookup" {
			json.NewEncoder(w).Encode([]lidarrAlbum{})
			return
		}
		http.Error(w, fmt.Sprintf("unexpected: %s %s", r.Method, r.URL.Path), http.StatusNotFound)
	}))
	defer server.Close()

	cfg := &config.Config{}
	cfg.Lidarr.BaseURL = server.URL
	cfg.Lidarr.APIKey = "test-api-key"

	// Must pass a real logger: EnrichAlbum logs errors when album lookup fails.
	factory := New(discardLogger(), cfg, nil)
	enricher, err := factory(context.Background(), nil)
	require.NoError(t, err)

	tn := 1
	track := &ent.Track{
		Name:        "New Track",
		TrackNumber: &tn,
		Edges: ent.TrackEdges{
			Album: &ent.Album{
				Name:          "Unknown Album",
				MusicbrainzID: "mbid-unknown",
				Edges: ent.AlbumEdges{
					Artist: &ent.Artist{Name: "New Artist", MusicbrainzID: "mbid-artist"},
				},
			},
		},
	}

	trackEnricher := enricher.(enrichers.TrackEnricher)
	data, err := trackEnricher.EnrichTrack(context.Background(), track)
	require.NoError(t, err)
	require.NotNil(t, data)

	// Album not in Lidarr — "pending" is correct.
	assert.Equal(t, "pending", data.LidarrStatus,
		"track should report 'pending' when album is genuinely absent from Lidarr")
}

// TestEnrichAlbum_NoArtistEdge verifies that findAlbum returns nil (not an
// error) when the album has no artist edge loaded.
func TestEnrichAlbum_NoArtistEdge(t *testing.T) {
	cfg := &config.Config{}
	cfg.Lidarr.BaseURL = "http://localhost"
	cfg.Lidarr.APIKey = "key"

	factory := New(nil, cfg, nil)
	enricher, err := factory(context.Background(), nil)
	require.NoError(t, err)

	album := &ent.Album{Name: "Orphaned Album"} // no Edges.Artist

	albumEnricher := enricher.(enrichers.AlbumEnricher)
	data, err := albumEnricher.EnrichAlbum(context.Background(), album)
	assert.NoError(t, err)
	assert.Nil(t, data, "should return nil without error when artist edge is missing")
}
