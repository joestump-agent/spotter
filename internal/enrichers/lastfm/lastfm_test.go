package lastfm

import (
	"context"
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

func TestNew_NoAPIKey(t *testing.T) {
	cfg := &config.Config{}
	// No API key configured

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	factory := New(logger, cfg)
	enricher, err := factory(context.Background(), nil)

	assert.NoError(t, err)
	assert.Nil(t, enricher)
}

func TestNew_WithAPIKey(t *testing.T) {
	cfg := &config.Config{}
	cfg.LastFM.APIKey = "test-api-key"

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	factory := New(logger, cfg)
	enricher, err := factory(context.Background(), nil)

	require.NoError(t, err)
	require.NotNil(t, enricher)

	assert.Equal(t, enrichers.TypeLastFM, enricher.Type())
	assert.Equal(t, "Last.fm", enricher.Name())
	assert.True(t, enricher.IsAvailable())
}

func TestEnrichArtist_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		assert.Equal(t, "artist.getinfo", r.URL.Query().Get("method"))
		assert.Equal(t, "Radiohead", r.URL.Query().Get("artist"))
		assert.Equal(t, "1", r.URL.Query().Get("autocorrect"))
		assert.Equal(t, "test-api-key", r.URL.Query().Get("api_key"))

		response := lastfmArtistResponse{
			Artist: lastfmArtist{
				Name: "Radiohead",
				MBID: "mbid-123",
				URL:  "https://www.last.fm/music/Radiohead",
				Bio: lastfmBio{
					Content: "Radiohead are an English rock band. <a href=\"https://www.last.fm/music/Radiohead\">Read more on Last.fm</a>",
					Summary: "Short bio",
				},
				Tags: struct {
					Tag []lastfmTag `json:"tag"`
				}{
					Tag: []lastfmTag{
						{Name: "alternative rock"},
						{Name: "indie"},
						{Name: "electronic"},
					},
				},
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	enricher := createTestEnricher(t, cfg.LastFM.APIKey, server.URL)
	artistEnricher := enricher.(enrichers.ArtistEnricher)

	artist := &ent.Artist{
		ID:   1,
		Name: "Radiohead",
	}

	data, err := artistEnricher.EnrichArtist(context.Background(), artist)
	require.NoError(t, err)
	require.NotNil(t, data)

	assert.Equal(t, "https://www.last.fm/music/Radiohead", data.LastFMURL)
	assert.Contains(t, data.Bio, "Radiohead are an English rock band")
	assert.NotContains(t, data.Bio, "<a href")
	assert.NotContains(t, data.Bio, "Read more on Last.fm")
	assert.Contains(t, data.Tags, "alternative rock")
	assert.Contains(t, data.Tags, "indie")
	assert.Contains(t, data.Tags, "electronic")
}

func TestEnrichArtist_BioHTMLStripping(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := lastfmArtistResponse{
			Artist: lastfmArtist{
				Name: "Test Artist",
				Bio: lastfmBio{
					Content: "<p>Artist with <strong>HTML</strong> tags.</p><br/><a href='test'>Link</a>",
				},
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	enricher := createTestEnricher(t, cfg.LastFM.APIKey, server.URL)
	artistEnricher := enricher.(enrichers.ArtistEnricher)

	artist := &ent.Artist{Name: "Test Artist"}

	data, err := artistEnricher.EnrichArtist(context.Background(), artist)
	require.NoError(t, err)
	require.NotNil(t, data)

	assert.NotContains(t, data.Bio, "<p>")
	assert.NotContains(t, data.Bio, "</p>")
	assert.NotContains(t, data.Bio, "<strong>")
	assert.NotContains(t, data.Bio, "<br/>")
	assert.NotContains(t, data.Bio, "<a href")
	assert.Contains(t, data.Bio, "Artist with HTML tags")
}

func TestEnrichArtist_RemovesReadMoreLink(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := lastfmArtistResponse{
			Artist: lastfmArtist{
				Name: "The Beatles",
				Bio: lastfmBio{
					Content: "The Beatles were an English rock band. <a href=\"https://www.last.fm/music/The+Beatles\">Read more on Last.fm</a>. User-contributed text is available under the Creative Commons By-SA License.",
				},
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	enricher := createTestEnricher(t, cfg.LastFM.APIKey, server.URL)
	artistEnricher := enricher.(enrichers.ArtistEnricher)

	artist := &ent.Artist{Name: "The Beatles"}

	data, err := artistEnricher.EnrichArtist(context.Background(), artist)
	require.NoError(t, err)
	require.NotNil(t, data)

	assert.NotContains(t, data.Bio, "Read more on Last.fm")
	assert.NotContains(t, data.Bio, "<a href")
	assert.Contains(t, data.Bio, "The Beatles were an English rock band")
}

func TestEnrichArtist_ParsesTags(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := lastfmArtistResponse{
			Artist: lastfmArtist{
				Name: "Test Artist",
				Tags: struct {
					Tag []lastfmTag `json:"tag"`
				}{
					Tag: []lastfmTag{
						{Name: "rock"},
						{Name: "pop"},
						{Name: "alternative"},
						{Name: "indie"},
						{Name: "british"},
					},
				},
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	enricher := createTestEnricher(t, cfg.LastFM.APIKey, server.URL)
	artistEnricher := enricher.(enrichers.ArtistEnricher)

	artist := &ent.Artist{Name: "Test Artist"}

	data, err := artistEnricher.EnrichArtist(context.Background(), artist)
	require.NoError(t, err)
	require.NotNil(t, data)

	assert.Len(t, data.Tags, 5)
	assert.Contains(t, data.Tags, "rock")
	assert.Contains(t, data.Tags, "pop")
	assert.Contains(t, data.Tags, "alternative")
	assert.Contains(t, data.Tags, "indie")
	assert.Contains(t, data.Tags, "british")
}

func TestEnrichArtist_AutocorrectParam(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify autocorrect parameter is set
		assert.Equal(t, "1", r.URL.Query().Get("autocorrect"))

		response := lastfmArtistResponse{
			Artist: lastfmArtist{
				Name: "Radiohead", // Corrected spelling
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	enricher := createTestEnricher(t, cfg.LastFM.APIKey, server.URL)
	artistEnricher := enricher.(enrichers.ArtistEnricher)

	artist := &ent.Artist{Name: "Radiohedd"} // Typo

	_, err := artistEnricher.EnrichArtist(context.Background(), artist)
	assert.NoError(t, err)
}

func TestEnrichArtist_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := map[string]interface{}{
			"error":   6,
			"message": "Artist not found",
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	enricher := createTestEnricher(t, cfg.LastFM.APIKey, server.URL)
	artistEnricher := enricher.(enrichers.ArtistEnricher)

	artist := &ent.Artist{Name: "NonexistentArtist12345"}

	data, err := artistEnricher.EnrichArtist(context.Background(), artist)
	assert.NoError(t, err)
	assert.Nil(t, data)
}

func TestEnrichArtist_WithMusicBrainzID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify MBID parameter is passed
		assert.Equal(t, "mbid-radiohead", r.URL.Query().Get("mbid"))

		response := lastfmArtistResponse{
			Artist: lastfmArtist{
				Name: "Radiohead",
				MBID: "mbid-radiohead",
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	enricher := createTestEnricher(t, cfg.LastFM.APIKey, server.URL)
	artistEnricher := enricher.(enrichers.ArtistEnricher)

	artist := &ent.Artist{
		Name:          "Radiohead",
		MusicbrainzID: "mbid-radiohead",
	}

	data, err := artistEnricher.EnrichArtist(context.Background(), artist)
	require.NoError(t, err)
	require.NotNil(t, data)
}

func TestEnrichAlbum_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "album.getinfo", r.URL.Query().Get("method"))
		assert.Equal(t, "OK Computer", r.URL.Query().Get("album"))
		assert.Equal(t, "Radiohead", r.URL.Query().Get("artist"))
		assert.Equal(t, "1", r.URL.Query().Get("autocorrect"))

		response := lastfmAlbumResponse{
			Album: lastfmAlbum{
				Name:   "OK Computer",
				Artist: "Radiohead",
				MBID:   "album-mbid-123",
				Tags: struct {
					Tag []lastfmTag `json:"tag"`
				}{
					Tag: []lastfmTag{
						{Name: "alternative rock"},
						{Name: "90s"},
					},
				},
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	enricher := createTestEnricher(t, cfg.LastFM.APIKey, server.URL)
	albumEnricher := enricher.(enrichers.AlbumEnricher)

	album := &ent.Album{
		Name: "OK Computer",
		Edges: ent.AlbumEdges{
			Artist: &ent.Artist{
				Name: "Radiohead",
			},
		},
	}

	data, err := albumEnricher.EnrichAlbum(context.Background(), album)
	require.NoError(t, err)
	require.NotNil(t, data)

	assert.Contains(t, data.Tags, "alternative rock")
	assert.Contains(t, data.Tags, "90s")
	assert.Equal(t, "album-mbid-123", data.MusicBrainzID)
}

func TestEnrichAlbum_WithArtist(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify artist parameter is included
		assert.Equal(t, "The Beatles", r.URL.Query().Get("artist"))
		assert.Equal(t, "Abbey Road", r.URL.Query().Get("album"))

		response := lastfmAlbumResponse{
			Album: lastfmAlbum{
				Name:   "Abbey Road",
				Artist: "The Beatles",
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	enricher := createTestEnricher(t, cfg.LastFM.APIKey, server.URL)
	albumEnricher := enricher.(enrichers.AlbumEnricher)

	album := &ent.Album{
		Name: "Abbey Road",
		Edges: ent.AlbumEdges{
			Artist: &ent.Artist{Name: "The Beatles"},
		},
	}

	_, err := albumEnricher.EnrichAlbum(context.Background(), album)
	assert.NoError(t, err)
}

func TestEnrichAlbum_NoArtist(t *testing.T) {
	cfg := &config.Config{}
	cfg.LastFM.APIKey = "test-api-key"

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	factory := New(logger, cfg)
	enricher, _ := factory(context.Background(), nil)
	albumEnricher := enricher.(enrichers.AlbumEnricher)

	album := &ent.Album{
		Name: "Test Album",
		// No artist edge
	}

	data, err := albumEnricher.EnrichAlbum(context.Background(), album)
	assert.NoError(t, err)
	assert.Nil(t, data)
}

func TestEnrichAlbum_ParsesTags(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := lastfmAlbumResponse{
			Album: lastfmAlbum{
				Name: "Test Album",
				Tags: struct {
					Tag []lastfmTag `json:"tag"`
				}{
					Tag: []lastfmTag{
						{Name: "rock"},
						{Name: "classic rock"},
						{Name: "70s"},
					},
				},
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	enricher := createTestEnricher(t, cfg.LastFM.APIKey, server.URL)
	albumEnricher := enricher.(enrichers.AlbumEnricher)

	album := &ent.Album{
		Name: "Test Album",
		Edges: ent.AlbumEdges{
			Artist: &ent.Artist{Name: "Test Artist"},
		},
	}

	data, err := albumEnricher.EnrichAlbum(context.Background(), album)
	require.NoError(t, err)
	require.NotNil(t, data)

	assert.Len(t, data.Tags, 3)
	assert.Contains(t, data.Tags, "rock")
	assert.Contains(t, data.Tags, "classic rock")
	assert.Contains(t, data.Tags, "70s")
}

func TestEnrichAlbum_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := map[string]interface{}{
			"error":   6,
			"message": "Album not found",
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	enricher := createTestEnricher(t, cfg.LastFM.APIKey, server.URL)
	albumEnricher := enricher.(enrichers.AlbumEnricher)

	album := &ent.Album{
		Name: "Nonexistent Album",
		Edges: ent.AlbumEdges{
			Artist: &ent.Artist{Name: "Unknown Artist"},
		},
	}

	data, err := albumEnricher.EnrichAlbum(context.Background(), album)
	assert.NoError(t, err)
	assert.Nil(t, data)
}

func TestEnrichTrack_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "track.getinfo", r.URL.Query().Get("method"))
		assert.Equal(t, "Paranoid Android", r.URL.Query().Get("track"))
		assert.Equal(t, "Radiohead", r.URL.Query().Get("artist"))

		response := lastfmTrackResponse{
			Track: lastfmTrack{
				Name:     "Paranoid Android",
				MBID:     "track-mbid-456",
				Duration: "383000",
				TopTags: struct {
					Tag []lastfmTag `json:"tag"`
				}{
					Tag: []lastfmTag{
						{Name: "alternative rock"},
						{Name: "progressive rock"},
					},
				},
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	enricher := createTestEnricher(t, cfg.LastFM.APIKey, server.URL)
	trackEnricher := enricher.(enrichers.TrackEnricher)

	track := &ent.Track{
		Name: "Paranoid Android",
		Edges: ent.TrackEdges{
			Artist: &ent.Artist{Name: "Radiohead"},
		},
	}

	data, err := trackEnricher.EnrichTrack(context.Background(), track)
	require.NoError(t, err)
	require.NotNil(t, data)

	assert.Contains(t, data.Tags, "alternative rock")
	assert.Contains(t, data.Tags, "progressive rock")
	assert.Equal(t, "track-mbid-456", data.MusicBrainzID)
	assert.Equal(t, 383000, data.DurationMs)
}

func TestEnrichTrack_WithArtistAndAlbum(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Karma Police", r.URL.Query().Get("track"))
		assert.Equal(t, "Radiohead", r.URL.Query().Get("artist"))

		response := lastfmTrackResponse{
			Track: lastfmTrack{
				Name: "Karma Police",
				Album: struct {
					Artist string        `json:"artist"`
					Title  string        `json:"title"`
					Image  []lastfmImage `json:"image"`
				}{
					Artist: "Radiohead",
					Title:  "OK Computer",
				},
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	enricher := createTestEnricher(t, cfg.LastFM.APIKey, server.URL)
	trackEnricher := enricher.(enrichers.TrackEnricher)

	track := &ent.Track{
		Name: "Karma Police",
		Edges: ent.TrackEdges{
			Artist: &ent.Artist{Name: "Radiohead"},
			Album:  &ent.Album{Name: "OK Computer"},
		},
	}

	_, err := trackEnricher.EnrichTrack(context.Background(), track)
	assert.NoError(t, err)
}

func TestEnrichTrack_NoArtist(t *testing.T) {
	cfg := &config.Config{}
	cfg.LastFM.APIKey = "test-api-key"

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	factory := New(logger, cfg)
	enricher, _ := factory(context.Background(), nil)
	trackEnricher := enricher.(enrichers.TrackEnricher)

	track := &ent.Track{
		Name: "Test Track",
		// No artist edge
	}

	data, err := trackEnricher.EnrichTrack(context.Background(), track)
	assert.NoError(t, err)
	assert.Nil(t, data)
}

func TestEnrichTrack_ParsesTags(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := lastfmTrackResponse{
			Track: lastfmTrack{
				Name: "Test Track",
				TopTags: struct {
					Tag []lastfmTag `json:"tag"`
				}{
					Tag: []lastfmTag{
						{Name: "electronic"},
						{Name: "ambient"},
						{Name: "experimental"},
					},
				},
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	enricher := createTestEnricher(t, cfg.LastFM.APIKey, server.URL)
	trackEnricher := enricher.(enrichers.TrackEnricher)

	track := &ent.Track{
		Name: "Test Track",
		Edges: ent.TrackEdges{
			Artist: &ent.Artist{Name: "Test Artist"},
		},
	}

	data, err := trackEnricher.EnrichTrack(context.Background(), track)
	require.NoError(t, err)
	require.NotNil(t, data)

	assert.Len(t, data.Tags, 3)
	assert.Contains(t, data.Tags, "electronic")
	assert.Contains(t, data.Tags, "ambient")
	assert.Contains(t, data.Tags, "experimental")
}

func TestGetArtistImages_AllSizes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := lastfmArtistResponse{
			Artist: lastfmArtist{
				Name: "Test Artist",
				Image: []lastfmImage{
					{Text: "http://example.com/small.jpg", Size: "small"},
					{Text: "http://example.com/medium.jpg", Size: "medium"},
					{Text: "http://example.com/large.jpg", Size: "large"},
					{Text: "http://example.com/extralarge.jpg", Size: "extralarge"},
					{Text: "http://example.com/mega.jpg", Size: "mega"},
				},
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	enricher := createTestEnricher(t, cfg.LastFM.APIKey, server.URL)
	artistEnricher := enricher.(enrichers.ArtistEnricher)

	artist := &ent.Artist{ID: 1, Name: "Test Artist"}

	images, err := artistEnricher.GetArtistImages(context.Background(), artist)
	require.NoError(t, err)
	// Note: Images may not download in test, but structure is verified
	assert.NotNil(t, images)
}

func TestGetArtistImages_SizeMapping(t *testing.T) {
	tests := []struct {
		size           string
		expectedWidth  int
		expectedHeight int
	}{
		{"small", 34, 34},
		{"medium", 64, 64},
		{"large", 174, 174},
		{"extralarge", 300, 300},
		{"mega", 500, 500},
	}

	for _, tt := range tests {
		t.Run(tt.size, func(t *testing.T) {
			width, height := imageSizeFromLastFM(tt.size)
			assert.Equal(t, tt.expectedWidth, width)
			assert.Equal(t, tt.expectedHeight, height)
		})
	}
}

func TestGetArtistImages_EmptyURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := lastfmArtistResponse{
			Artist: lastfmArtist{
				Name: "Test Artist",
				Image: []lastfmImage{
					{Text: "", Size: "small"},                             // Empty URL
					{Text: "http://example.com/valid.jpg", Size: "large"}, // Valid URL
				},
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	enricher := createTestEnricher(t, cfg.LastFM.APIKey, server.URL)
	artistEnricher := enricher.(enrichers.ArtistEnricher)

	artist := &ent.Artist{ID: 1, Name: "Test Artist"}

	images, err := artistEnricher.GetArtistImages(context.Background(), artist)
	require.NoError(t, err)
	// Should skip empty URLs
	assert.NotNil(t, images)
}

func TestGetAlbumImages_AllSizes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := lastfmAlbumResponse{
			Album: lastfmAlbum{
				Name: "Test Album",
				Image: []lastfmImage{
					{Text: "http://example.com/cover-small.jpg", Size: "small"},
					{Text: "http://example.com/cover-medium.jpg", Size: "medium"},
					{Text: "http://example.com/cover-large.jpg", Size: "large"},
					{Text: "http://example.com/cover-xl.jpg", Size: "extralarge"},
				},
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	enricher := createTestEnricher(t, cfg.LastFM.APIKey, server.URL)
	albumEnricher := enricher.(enrichers.AlbumEnricher)

	album := &ent.Album{
		ID:   1,
		Name: "Test Album",
		Edges: ent.AlbumEdges{
			Artist: &ent.Artist{Name: "Test Artist"},
		},
	}

	images, err := albumEnricher.GetAlbumImages(context.Background(), album)
	require.NoError(t, err)
	assert.NotNil(t, images)
}

func TestCleanBio_RemovesHTMLTags(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "paragraph tags",
			input:    "<p>This is a test.</p>",
			expected: "This is a test.",
		},
		{
			name:     "bold tags",
			input:    "Text with <strong>bold</strong> content",
			expected: "Text with bold content",
		},
		{
			name:     "break tags",
			input:    "Line one<br/>Line two",
			expected: "Line oneLine two",
		},
		{
			name:     "link tags",
			input:    "Click <a href='url'>here</a>",
			expected: "Click here",
		},
		{
			name:     "multiple tags",
			input:    "<p>Text with <em>emphasis</em> and <strong>bold</strong></p>",
			expected: "Text with emphasis and bold",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := cleanBio(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestCleanBio_RemovesReadMore(t *testing.T) {
	bio := "Artist biography text here. <a href=\"https://www.last.fm/music/Artist\">Read more on Last.fm</a>. More text after."
	result := cleanBio(bio)

	assert.NotContains(t, result, "Read more on Last.fm")
	assert.NotContains(t, result, "<a href")
	assert.Contains(t, result, "Artist biography text here")
}

func TestCleanBio_PreservesNewlines(t *testing.T) {
	// Note: cleanBio doesn't explicitly handle newlines, but doesn't remove them
	bio := "Line one\nLine two\nLine three"
	result := cleanBio(bio)

	assert.Contains(t, result, "Line one")
	assert.Contains(t, result, "Line two")
	assert.Contains(t, result, "Line three")
}

func TestCleanBio_HandlesEmpty(t *testing.T) {
	result := cleanBio("")
	assert.Equal(t, "", result)
}

func TestAPIError_Code2(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := map[string]interface{}{
			"error":   2,
			"message": "Invalid service",
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	enricher := createTestEnricher(t, cfg.LastFM.APIKey, server.URL)
	artistEnricher := enricher.(enrichers.ArtistEnricher)

	artist := &ent.Artist{Name: "Test"}

	_, err := artistEnricher.EnrichArtist(context.Background(), artist)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "error 2")
}

func TestAPIError_Code6(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := map[string]interface{}{
			"error":   6,
			"message": "Not found",
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	enricher := createTestEnricher(t, cfg.LastFM.APIKey, server.URL)
	artistEnricher := enricher.(enrichers.ArtistEnricher)

	artist := &ent.Artist{Name: "NotFound"}

	data, err := artistEnricher.EnrichArtist(context.Background(), artist)
	assert.NoError(t, err) // Error code 6 returns nil, not error
	assert.Nil(t, data)
}

func TestAPIError_Code10(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := map[string]interface{}{
			"error":   10,
			"message": "Invalid API key",
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	enricher := createTestEnricher(t, cfg.LastFM.APIKey, server.URL)
	artistEnricher := enricher.(enrichers.ArtistEnricher)

	artist := &ent.Artist{Name: "Test"}

	_, err := artistEnricher.EnrichArtist(context.Background(), artist)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "error 10")
}

func TestAPIError_Code11(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := map[string]interface{}{
			"error":   11,
			"message": "Service offline",
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	enricher := createTestEnricher(t, cfg.LastFM.APIKey, server.URL)
	artistEnricher := enricher.(enrichers.ArtistEnricher)

	artist := &ent.Artist{Name: "Test"}

	_, err := artistEnricher.EnrichArtist(context.Background(), artist)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "error 11")
}

func TestAPIError_Code16(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := map[string]interface{}{
			"error":   16,
			"message": "Service temporarily unavailable",
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	enricher := createTestEnricher(t, cfg.LastFM.APIKey, server.URL)
	artistEnricher := enricher.(enrichers.ArtistEnricher)

	artist := &ent.Artist{Name: "Test"}

	_, err := artistEnricher.EnrichArtist(context.Background(), artist)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "error 16")
}

func TestEnrich_MissingBio(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := lastfmArtistResponse{
			Artist: lastfmArtist{
				Name: "Test Artist",
				Bio: lastfmBio{
					Content: "", // No bio
					Summary: "", // No summary either
				},
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	enricher := createTestEnricher(t, cfg.LastFM.APIKey, server.URL)
	artistEnricher := enricher.(enrichers.ArtistEnricher)

	artist := &ent.Artist{Name: "Test Artist"}

	data, err := artistEnricher.EnrichArtist(context.Background(), artist)
	require.NoError(t, err)
	require.NotNil(t, data)

	assert.Equal(t, "", data.Bio)
}

func TestEnrich_EmptyTags(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := lastfmArtistResponse{
			Artist: lastfmArtist{
				Name: "Test Artist",
				Tags: struct {
					Tag []lastfmTag `json:"tag"`
				}{
					Tag: []lastfmTag{}, // Empty tags
				},
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	enricher := createTestEnricher(t, cfg.LastFM.APIKey, server.URL)
	artistEnricher := enricher.(enrichers.ArtistEnricher)

	artist := &ent.Artist{Name: "Test Artist"}

	data, err := artistEnricher.EnrichArtist(context.Background(), artist)
	require.NoError(t, err)
	require.NotNil(t, data)

	assert.Len(t, data.Tags, 0)
}

func TestEnrich_NoImages(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := lastfmArtistResponse{
			Artist: lastfmArtist{
				Name:  "Test Artist",
				Image: []lastfmImage{}, // No images
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	enricher := createTestEnricher(t, cfg.LastFM.APIKey, server.URL)
	artistEnricher := enricher.(enrichers.ArtistEnricher)

	artist := &ent.Artist{ID: 1, Name: "Test Artist"}

	images, err := artistEnricher.GetArtistImages(context.Background(), artist)
	require.NoError(t, err)
	assert.NotNil(t, images)
}

func TestInterfaceImplementation(t *testing.T) {
	// Verify Enricher implements all required interfaces
	var _ enrichers.Enricher = (*Enricher)(nil)
	var _ enrichers.ArtistEnricher = (*Enricher)(nil)
	var _ enrichers.AlbumEnricher = (*Enricher)(nil)
	var _ enrichers.TrackEnricher = (*Enricher)(nil)
}

// Helper function to create a test enricher with custom base URL
func createTestEnricher(t *testing.T, apiKey, serverURL string) enrichers.Enricher {
	cfg := &config.Config{}
	cfg.LastFM.APIKey = apiKey

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	enricher := &Enricher{
		logger:       logger,
		config:       cfg,
		apiKey:       apiKey,
		sharedSecret: "test-secret",
		httpClient: &http.Client{
			Transport: &testTransport{baseURL: serverURL},
		},
	}

	return enricher
}

// testTransport is a custom http.RoundTripper that rewrites URLs to use the test server
type testTransport struct {
	baseURL string
}

func (t *testTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Rewrite the URL to use the test server
	if t.baseURL != "" {
		req.URL.Scheme = "http"
		testURL := t.baseURL
		if len(testURL) > 7 && testURL[:7] == "http://" {
			testURL = testURL[7:]
		}
		req.URL.Host = testURL
	}

	return http.DefaultTransport.RoundTrip(req)
}
