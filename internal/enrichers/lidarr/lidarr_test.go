package lidarr

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"spotter/ent"
	"spotter/internal/config"
	"spotter/internal/enrichers"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

func TestEnrichTrack_Found(t *testing.T) {
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

		// 2. Find Album
		if r.URL.Path == "/api/v1/album" {
			albums := []lidarrAlbum{
				{
					ID:             20,
					ArtistID:       10,
					Title:          "Test Album",
					ForeignAlbumID: "mbid-album",
				},
			}
			json.NewEncoder(w).Encode(albums)
			return
		}

		// 3. Find Track
		if r.URL.Path == "/api/v1/track" {
			assert.Equal(t, "10", r.URL.Query().Get("artistId"))
			assert.Equal(t, "20", r.URL.Query().Get("albumId"))

			tracks := []lidarrTrack{
				{
					ID:          30,
					Title:       "Test Track",
					TrackNumber: 1,
					Duration:    180000,
					HasFile:     true,
				},
			}
			json.NewEncoder(w).Encode(tracks)
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
	tn := 1
	track := &ent.Track{
		Name:        "Test Track",
		TrackNumber: &tn,
		Edges: ent.TrackEdges{
			Album: &ent.Album{
				Name:          "Test Album",
				MusicbrainzID: albumMbid,
				Edges: ent.AlbumEdges{
					Artist: &ent.Artist{
						Name:          "Test Artist",
						MusicbrainzID: artistMbid,
					},
				},
			},
		},
	}

	trackEnricher := enricher.(enrichers.TrackEnricher)
	data, err := trackEnricher.EnrichTrack(context.Background(), track)
	require.NoError(t, err)
	require.NotNil(t, data)

	assert.Equal(t, "30", data.LidarrID)
	assert.Equal(t, "available", data.LidarrStatus)
}
