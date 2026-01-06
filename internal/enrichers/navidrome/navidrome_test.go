package navidrome

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
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

func TestNew_NoNavidromeAuth(t *testing.T) {
	cfg := &config.Config{}
	cfg.Navidrome.BaseURL = "http://localhost:4533"

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	factory := New(logger, cfg)

	// User has no NavidromeAuth edge
	user := &ent.User{
		ID:       1,
		Username: "testuser",
		Edges: ent.UserEdges{
			NavidromeAuth: nil,
		},
	}

	enricher, err := factory(context.Background(), user)
	assert.NoError(t, err)
	assert.Nil(t, enricher)
}

func TestNew_WithAuth(t *testing.T) {
	cfg := &config.Config{}
	cfg.Navidrome.BaseURL = "http://localhost:4533"

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	factory := New(logger, cfg)

	user := &ent.User{
		ID:       1,
		Username: "testuser",
		Edges: ent.UserEdges{
			NavidromeAuth: &ent.NavidromeAuth{
				ID:       1,
				Password: "testpass",
			},
		},
	}

	enricher, err := factory(context.Background(), user)
	require.NoError(t, err)
	require.NotNil(t, enricher)

	assert.Equal(t, enrichers.TypeNavidrome, enricher.Type())
	assert.Equal(t, "Navidrome", enricher.Name())
	assert.True(t, enricher.IsAvailable())
}

func TestGenerateToken_Correct(t *testing.T) {
	password := "mypassword"
	salt := "abcd1234"

	token := generateToken(password, salt)

	// Verify it's MD5(password + salt)
	expected := md5.Sum([]byte(password + salt))
	expectedHex := hex.EncodeToString(expected[:])

	assert.Equal(t, expectedHex, token)
	assert.Len(t, token, 32) // MD5 produces 32-char hex string
}

func TestAuthParams_IncludesSaltAndToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify authentication parameters
		assert.NotEmpty(t, r.URL.Query().Get("u"), "username should be present")
		assert.NotEmpty(t, r.URL.Query().Get("t"), "token should be present")
		assert.NotEmpty(t, r.URL.Query().Get("s"), "salt should be present")
		assert.Equal(t, "spotter", r.URL.Query().Get("c"))
		assert.Equal(t, "1.16.1", r.URL.Query().Get("v"))
		assert.Equal(t, "json", r.URL.Query().Get("f"))

		response := subsonicSearchResponse{
			SubsonicResponse: struct {
				Status        string `json:"status"`
				SearchResult3 struct {
					Artist []struct {
						ID            string `json:"id"`
						Name          string `json:"name"`
						AlbumCount    int    `json:"albumCount"`
						MusicBrainzID string `json:"musicBrainzId"`
					} `json:"artist"`
					Album []subsonicAlbum `json:"album"`
					Song  []subsonicSong  `json:"song"`
				} `json:"searchResult3"`
			}{
				Status: "ok",
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	enricher := createTestEnricher(t, server.URL)
	e := enricher.(*Enricher)

	_, _ = e.searchArtist(context.Background(), "Test")
}

func TestAuthParams_Username(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "testuser", r.URL.Query().Get("u"))

		response := subsonicSearchResponse{
			SubsonicResponse: struct {
				Status        string `json:"status"`
				SearchResult3 struct {
					Artist []struct {
						ID            string `json:"id"`
						Name          string `json:"name"`
						AlbumCount    int    `json:"albumCount"`
						MusicBrainzID string `json:"musicBrainzId"`
					} `json:"artist"`
					Album []subsonicAlbum `json:"album"`
					Song  []subsonicSong  `json:"song"`
				} `json:"searchResult3"`
			}{
				Status: "ok",
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	enricher := createTestEnricher(t, server.URL)
	e := enricher.(*Enricher)

	_, _ = e.searchArtist(context.Background(), "Test")
}

func TestEnrichArtist_ByID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/rest/getArtist" {
			assert.Equal(t, "artist-123", r.URL.Query().Get("id"))

			response := subsonicArtistResponse{
				SubsonicResponse: struct {
					Status string `json:"status"`
					Artist struct {
						ID             string          `json:"id"`
						Name           string          `json:"name"`
						AlbumCount     int             `json:"albumCount"`
						CoverArt       string          `json:"coverArt"`
						ArtistImageURL string          `json:"artistImageUrl"`
						MusicBrainzID  string          `json:"musicBrainzId"`
						SortName       string          `json:"sortName"`
						Album          []subsonicAlbum `json:"album"`
					} `json:"artist"`
				}{
					Status: "ok",
					Artist: struct {
						ID             string          `json:"id"`
						Name           string          `json:"name"`
						AlbumCount     int             `json:"albumCount"`
						CoverArt       string          `json:"coverArt"`
						ArtistImageURL string          `json:"artistImageUrl"`
						MusicBrainzID  string          `json:"musicBrainzId"`
						SortName       string          `json:"sortName"`
						Album          []subsonicAlbum `json:"album"`
					}{
						ID:            "artist-123",
						Name:          "Radiohead",
						MusicBrainzID: "mbid-radiohead",
						AlbumCount:    10,
					},
				},
			}

			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(response)
		} else if r.URL.Path == "/rest/getArtistInfo2" {
			response := subsonicArtistInfo{
				SubsonicResponse: struct {
					Status     string `json:"status"`
					ArtistInfo struct {
						Biography      string `json:"biography"`
						MusicBrainzID  string `json:"musicBrainzId"`
						LastFMURL      string `json:"lastFmUrl"`
						SmallImageURL  string `json:"smallImageUrl"`
						MediumImageURL string `json:"mediumImageUrl"`
						LargeImageURL  string `json:"largeImageUrl"`
					} `json:"artistInfo2"`
				}{
					Status: "ok",
					ArtistInfo: struct {
						Biography      string `json:"biography"`
						MusicBrainzID  string `json:"musicBrainzId"`
						LastFMURL      string `json:"lastFmUrl"`
						SmallImageURL  string `json:"smallImageUrl"`
						MediumImageURL string `json:"mediumImageUrl"`
						LargeImageURL  string `json:"largeImageUrl"`
					}{
						Biography:     "English rock band",
						MusicBrainzID: "mbid-radiohead",
					},
				},
			}

			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(response)
		}
	}))
	defer server.Close()

	enricher := createTestEnricher(t, server.URL)
	artistEnricher := enricher.(enrichers.ArtistEnricher)

	artist := &ent.Artist{
		ID:          1,
		Name:        "Radiohead",
		NavidromeID: "artist-123",
	}

	data, err := artistEnricher.EnrichArtist(context.Background(), artist)
	require.NoError(t, err)
	require.NotNil(t, data)

	assert.Equal(t, "artist-123", data.NavidromeID)
	assert.Equal(t, "mbid-radiohead", data.MusicBrainzID)
	assert.Contains(t, data.Bio, "English rock band")
}

func TestEnrichArtist_BySearch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/rest/search3" {
			response := subsonicSearchResponse{
				SubsonicResponse: struct {
					Status        string `json:"status"`
					SearchResult3 struct {
						Artist []struct {
							ID            string `json:"id"`
							Name          string `json:"name"`
							AlbumCount    int    `json:"albumCount"`
							MusicBrainzID string `json:"musicBrainzId"`
						} `json:"artist"`
						Album []subsonicAlbum `json:"album"`
						Song  []subsonicSong  `json:"song"`
					} `json:"searchResult3"`
				}{
					Status: "ok",
					SearchResult3: struct {
						Artist []struct {
							ID            string `json:"id"`
							Name          string `json:"name"`
							AlbumCount    int    `json:"albumCount"`
							MusicBrainzID string `json:"musicBrainzId"`
						} `json:"artist"`
						Album []subsonicAlbum `json:"album"`
						Song  []subsonicSong  `json:"song"`
					}{
						Artist: []struct {
							ID            string `json:"id"`
							Name          string `json:"name"`
							AlbumCount    int    `json:"albumCount"`
							MusicBrainzID string `json:"musicBrainzId"`
						}{
							{
								ID:            "found-artist-id",
								Name:          "The Beatles",
								MusicBrainzID: "mbid-beatles",
							},
						},
					},
				},
			}

			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(response)
		} else if r.URL.Path == "/rest/getArtist" {
			response := subsonicArtistResponse{}
			response.SubsonicResponse.Status = "ok"
			response.SubsonicResponse.Artist.ID = "found-artist-id"
			response.SubsonicResponse.Artist.Name = "The Beatles"
			response.SubsonicResponse.Artist.MusicBrainzID = "mbid-beatles"

			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(response)
		} else if r.URL.Path == "/rest/getArtistInfo2" {
			response := subsonicArtistInfo{}
			response.SubsonicResponse.Status = "ok"

			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(response)
		}
	}))
	defer server.Close()

	enricher := createTestEnricher(t, server.URL)
	artistEnricher := enricher.(enrichers.ArtistEnricher)

	artist := &ent.Artist{
		ID:          2,
		Name:        "The Beatles",
		NavidromeID: "", // No Navidrome ID
	}

	data, err := artistEnricher.EnrichArtist(context.Background(), artist)
	require.NoError(t, err)
	require.NotNil(t, data)

	assert.Equal(t, "found-artist-id", data.NavidromeID)
	assert.Equal(t, "mbid-beatles", data.MusicBrainzID)
}

func TestEnrichArtist_WithMBID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/rest/search3" {
			// Verify MBID is used in search
			query := r.URL.Query().Get("query")
			assert.Contains(t, query, "mbid:mbid-test")

			response := subsonicSearchResponse{}
			response.SubsonicResponse.Status = "ok"
			response.SubsonicResponse.SearchResult3.Artist = []struct {
				ID            string `json:"id"`
				Name          string `json:"name"`
				AlbumCount    int    `json:"albumCount"`
				MusicBrainzID string `json:"musicBrainzId"`
			}{
				{
					ID:            "artist-by-mbid",
					Name:          "Test Artist",
					MusicBrainzID: "mbid-test",
				},
			}

			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(response)
		} else if r.URL.Path == "/rest/getArtist" {
			response := subsonicArtistResponse{}
			response.SubsonicResponse.Status = "ok"
			response.SubsonicResponse.Artist.ID = "artist-by-mbid"
			response.SubsonicResponse.Artist.Name = "Test Artist"
			response.SubsonicResponse.Artist.MusicBrainzID = "mbid-test"

			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(response)
		} else if r.URL.Path == "/rest/getArtistInfo2" {
			response := subsonicArtistInfo{}
			response.SubsonicResponse.Status = "ok"

			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(response)
		}
	}))
	defer server.Close()

	enricher := createTestEnricher(t, server.URL)
	artistEnricher := enricher.(enrichers.ArtistEnricher)

	artist := &ent.Artist{
		Name:          "Test Artist",
		MusicbrainzID: "mbid-test",
	}

	data, err := artistEnricher.EnrichArtist(context.Background(), artist)
	require.NoError(t, err)
	require.NotNil(t, data)
}

func TestEnrichAlbum_ByID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/rest/getAlbum", r.URL.Path)
		assert.Equal(t, "album-456", r.URL.Query().Get("id"))

		response := subsonicAlbumResponse{}
		response.SubsonicResponse.Status = "ok"
		response.SubsonicResponse.Album.ID = "album-456"
		response.SubsonicResponse.Album.Name = "OK Computer"
		response.SubsonicResponse.Album.Artist = "Radiohead"
		response.SubsonicResponse.Album.Year = 1997
		response.SubsonicResponse.Album.Genre = "Alternative Rock"
		response.SubsonicResponse.Album.MusicBrainzID = "album-mbid-456"

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	enricher := createTestEnricher(t, server.URL)
	albumEnricher := enricher.(enrichers.AlbumEnricher)

	album := &ent.Album{
		ID:          1,
		Name:        "OK Computer",
		NavidromeID: "album-456",
		Edges: ent.AlbumEdges{
			Artist: &ent.Artist{Name: "Radiohead"},
		},
	}

	data, err := albumEnricher.EnrichAlbum(context.Background(), album)
	require.NoError(t, err)
	require.NotNil(t, data)

	assert.Equal(t, "album-456", data.NavidromeID)
	assert.Equal(t, "album-mbid-456", data.MusicBrainzID)
	assert.Equal(t, 1997, data.Year)
	assert.Equal(t, "Alternative Rock", data.Genre)
}

func TestEnrichAlbum_BySearch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/rest/search3" {
			response := subsonicSearchResponse{}
			response.SubsonicResponse.Status = "ok"
			response.SubsonicResponse.SearchResult3.Album = []subsonicAlbum{
				{
					ID:            "album-found",
					Name:          "Abbey Road",
					Artist:        "The Beatles",
					Year:          1969,
					MusicBrainzID: "album-mbid-abbey",
				},
			}

			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(response)
		} else if r.URL.Path == "/rest/getAlbum" {
			response := subsonicAlbumResponse{}
			response.SubsonicResponse.Status = "ok"
			response.SubsonicResponse.Album.ID = "album-found"
			response.SubsonicResponse.Album.Name = "Abbey Road"
			response.SubsonicResponse.Album.Year = 1969

			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(response)
		}
	}))
	defer server.Close()

	enricher := createTestEnricher(t, server.URL)
	albumEnricher := enricher.(enrichers.AlbumEnricher)

	album := &ent.Album{
		ID:          2,
		Name:        "Abbey Road",
		NavidromeID: "", // No Navidrome ID
		Edges: ent.AlbumEdges{
			Artist: &ent.Artist{Name: "The Beatles", NavidromeID: "artist-beatles"},
		},
	}

	data, err := albumEnricher.EnrichAlbum(context.Background(), album)
	require.NoError(t, err)
	require.NotNil(t, data)

	assert.Equal(t, "album-found", data.NavidromeID)
	assert.Equal(t, 1969, data.Year)
}

func TestEnrichAlbum_ParsesYear(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := subsonicAlbumResponse{}
		response.SubsonicResponse.Status = "ok"
		response.SubsonicResponse.Album.ID = "album-test"
		response.SubsonicResponse.Album.Name = "Test Album"
		response.SubsonicResponse.Album.Year = 2023

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	enricher := createTestEnricher(t, server.URL)
	albumEnricher := enricher.(enrichers.AlbumEnricher)

	album := &ent.Album{
		ID:          1,
		Name:        "Test Album",
		NavidromeID: "album-test",
		Edges: ent.AlbumEdges{
			Artist: &ent.Artist{Name: "Test Artist"},
		},
	}

	data, err := albumEnricher.EnrichAlbum(context.Background(), album)
	require.NoError(t, err)
	require.NotNil(t, data)

	assert.Equal(t, 2023, data.Year)
}

func TestGetCoverArtURL_WithID(t *testing.T) {
	cfg := &config.Config{}
	cfg.Navidrome.BaseURL = "http://localhost:4533"

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	user := &ent.User{
		ID:       1,
		Username: "testuser",
		Edges: ent.UserEdges{
			NavidromeAuth: &ent.NavidromeAuth{
				ID:       1,
				Password: "testpass",
			},
		},
	}

	enricher := &Enricher{
		logger:     logger,
		config:     cfg,
		user:       user,
		auth:       user.Edges.NavidromeAuth,
		httpClient: &http.Client{},
	}

	coverURL := enricher.getCoverArtURL("cover-art-123")

	assert.Contains(t, coverURL, "/rest/getCoverArt")
	assert.Contains(t, coverURL, "id=cover-art-123")
	assert.Contains(t, coverURL, "u=testuser")
	assert.Contains(t, coverURL, "s=") // Salt should be present
	assert.Contains(t, coverURL, "t=") // Token should be present
}

func TestGetCoverArtURL_NoID(t *testing.T) {
	cfg := &config.Config{}
	cfg.Navidrome.BaseURL = "http://localhost:4533"

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	user := &ent.User{
		ID:       1,
		Username: "testuser",
		Edges: ent.UserEdges{
			NavidromeAuth: &ent.NavidromeAuth{
				ID:       1,
				Password: "testpass",
			},
		},
	}

	enricher := &Enricher{
		logger:     logger,
		config:     cfg,
		user:       user,
		auth:       user.Edges.NavidromeAuth,
		httpClient: &http.Client{},
	}

	coverURL := enricher.getCoverArtURL("")
	assert.Equal(t, "", coverURL)
}

func TestSearch_MultipleResults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := subsonicSearchResponse{}
		response.SubsonicResponse.Status = "ok"
		response.SubsonicResponse.SearchResult3.Artist = []struct {
			ID            string `json:"id"`
			Name          string `json:"name"`
			AlbumCount    int    `json:"albumCount"`
			MusicBrainzID string `json:"musicBrainzId"`
		}{
			{ID: "artist-1", Name: "First Artist"},
			{ID: "artist-2", Name: "Second Artist"},
			{ID: "artist-3", Name: "Third Artist"},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	enricher := createTestEnricher(t, server.URL)
	e := enricher.(*Enricher)

	artistID, err := e.searchArtist(context.Background(), "Test")
	require.NoError(t, err)
	assert.Equal(t, "artist-1", artistID) // Should return first result
}

func TestSearch_NoResults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := subsonicSearchResponse{}
		response.SubsonicResponse.Status = "ok"
		response.SubsonicResponse.SearchResult3.Artist = []struct {
			ID            string `json:"id"`
			Name          string `json:"name"`
			AlbumCount    int    `json:"albumCount"`
			MusicBrainzID string `json:"musicBrainzId"`
		}{}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	enricher := createTestEnricher(t, server.URL)
	e := enricher.(*Enricher)

	artistID, err := e.searchArtist(context.Background(), "NonexistentArtist")
	assert.NoError(t, err)
	assert.Equal(t, "", artistID)
}

func TestSubsonicError_Code10(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := map[string]interface{}{
			"subsonic-response": map[string]interface{}{
				"status": "failed",
				"error": map[string]interface{}{
					"code":    10,
					"message": "Required parameter is missing",
				},
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	enricher := createTestEnricher(t, server.URL)
	artistEnricher := enricher.(enrichers.ArtistEnricher)

	artist := &ent.Artist{Name: "Test"}

	_, err := artistEnricher.EnrichArtist(context.Background(), artist)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "error 10")
}

func TestSubsonicError_Code40(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := map[string]interface{}{
			"subsonic-response": map[string]interface{}{
				"status": "failed",
				"error": map[string]interface{}{
					"code":    40,
					"message": "Wrong username or password",
				},
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	enricher := createTestEnricher(t, server.URL)
	artistEnricher := enricher.(enrichers.ArtistEnricher)

	artist := &ent.Artist{Name: "Test"}

	_, err := artistEnricher.EnrichArtist(context.Background(), artist)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "error 40")
}

func TestSubsonicError_Code70(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := map[string]interface{}{
			"subsonic-response": map[string]interface{}{
				"status": "failed",
				"error": map[string]interface{}{
					"code":    70,
					"message": "The requested data was not found",
				},
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	enricher := createTestEnricher(t, server.URL)
	artistEnricher := enricher.(enrichers.ArtistEnricher)

	artist := &ent.Artist{Name: "NotFound"}

	_, err := artistEnricher.EnrichArtist(context.Background(), artist)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "error 70")
}

func TestEnrichTrack_BySearch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/rest/search3" {
			response := subsonicSearchResponse{}
			response.SubsonicResponse.Status = "ok"
			response.SubsonicResponse.SearchResult3.Song = []subsonicSong{
				{
					ID:       "track-123",
					Title:    "Paranoid Android",
					Artist:   "Radiohead",
					Album:    "OK Computer",
					Duration: 383,
					BPM:      144,
					Track:    2,
				},
			}

			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(response)
		}
	}))
	defer server.Close()

	enricher := createTestEnricher(t, server.URL)
	trackEnricher := enricher.(enrichers.TrackEnricher)

	trackNum := 2
	track := &ent.Track{
		ID:          1,
		Name:        "Paranoid Android",
		TrackNumber: &trackNum,
		Edges: ent.TrackEdges{
			Artist: &ent.Artist{Name: "Radiohead", NavidromeID: "artist-radiohead"},
			Album:  &ent.Album{Name: "OK Computer", NavidromeID: "album-ok-computer"},
		},
	}

	data, err := trackEnricher.EnrichTrack(context.Background(), track)
	require.NoError(t, err)
	require.NotNil(t, data)

	assert.Equal(t, "track-123", data.NavidromeID)
	assert.Equal(t, 383000, data.DurationMs) // Converted to milliseconds
}

func TestInterfaceImplementation(t *testing.T) {
	// Verify Enricher implements all required interfaces
	var _ enrichers.Enricher = (*Enricher)(nil)
	var _ enrichers.ArtistEnricher = (*Enricher)(nil)
	var _ enrichers.AlbumEnricher = (*Enricher)(nil)
	var _ enrichers.TrackEnricher = (*Enricher)(nil)
}

// Helper function to create a test enricher with custom base URL
func createTestEnricher(t *testing.T, serverURL string) enrichers.Enricher {
	cfg := &config.Config{}
	cfg.Navidrome.BaseURL = serverURL

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	user := &ent.User{
		ID:       1,
		Username: "testuser",
		Edges: ent.UserEdges{
			NavidromeAuth: &ent.NavidromeAuth{
				ID:       1,
				Password: "testpass",
			},
		},
	}

	enricher := &Enricher{
		logger:     logger,
		config:     cfg,
		user:       user,
		auth:       user.Edges.NavidromeAuth,
		httpClient: &http.Client{},
	}

	return enricher
}
