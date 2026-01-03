package spotify_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"spotter/ent"
	"spotter/internal/config"
	"spotter/internal/providers"
	"spotter/internal/providers/spotify"

	"github.com/stretchr/testify/assert"
)

func TestNewFactory(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{}

	t.Run("ReturnsNilWithoutAuth", func(t *testing.T) {
		user := &ent.User{
			Username: "testuser",
			// No SpotifyAuth edge
		}

		factory := spotify.New(logger, cfg)
		provider, err := factory(context.Background(), user)
		assert.NoError(t, err)
		assert.Nil(t, provider)
	})

	t.Run("ReturnsProviderWithAuth", func(t *testing.T) {
		user := &ent.User{
			Username: "testuser",
			Edges: ent.UserEdges{
				SpotifyAuth: &ent.SpotifyAuth{
					AccessToken: "token",
				},
			},
		}

		factory := spotify.New(logger, cfg)
		provider, err := factory(context.Background(), user)
		assert.NoError(t, err)
		assert.NotNil(t, provider)
		assert.Equal(t, providers.TypeSpotify, provider.Type())
	})
}

func TestGetRecentListens(t *testing.T) {
	// Setup
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{}
	user := &ent.User{
		Username: "testuser",
		Edges: ent.UserEdges{
			SpotifyAuth: &ent.SpotifyAuth{AccessToken: "token"},
		},
	}

	factory := spotify.New(logger, cfg)
	provider, _ := factory(context.Background(), user)
	fetcher := provider.(providers.HistoryFetcher)

	// Test (Stubbed implementation check)
	// Currently returns a hardcoded Rick Astley track
	tracks, err := fetcher.GetRecentListens(context.Background(), time.Now())
	assert.NoError(t, err)
	assert.NotEmpty(t, tracks)
	assert.Equal(t, "Rick Astley", tracks[0].Artist)
}

func TestCreatePlaylist(t *testing.T) {
	// Setup
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{}
	user := &ent.User{
		Username: "testuser",
		Edges: ent.UserEdges{
			SpotifyAuth: &ent.SpotifyAuth{AccessToken: "token"},
		},
	}

	factory := spotify.New(logger, cfg)
	provider, _ := factory(context.Background(), user)
	manager := provider.(providers.PlaylistManager)

	t.Run("EmptyTracks", func(t *testing.T) {
		err := manager.CreatePlaylist(context.Background(), "My Playlist", "Desc", []providers.Track{})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "cannot create empty playlist")
	})

	t.Run("SuccessStub", func(t *testing.T) {
		tracks := []providers.Track{
			{ID: "1", Name: "Track 1"},
		}
		err := manager.CreatePlaylist(context.Background(), "My Playlist", "Desc", tracks)
		assert.NoError(t, err)
	})
}
