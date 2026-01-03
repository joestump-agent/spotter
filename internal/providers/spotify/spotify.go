package spotify

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"spotter/ent"
	"spotter/internal/config"
	"spotter/internal/providers"
)

type Provider struct {
	logger *slog.Logger
	config *config.Config
	user   *ent.User
	auth   *ent.SpotifyAuth
}

// Ensure Provider implements interfaces
var _ providers.HistoryFetcher = (*Provider)(nil)
var _ providers.PlaylistManager = (*Provider)(nil)

// New returns a factory that creates Spotify providers for a given user.
func New(logger *slog.Logger, cfg *config.Config) providers.Factory {
	return func(ctx context.Context, user *ent.User) (providers.Provider, error) {
		// Check if the user has Spotify authentication data.
		// We expect the caller to have loaded the edges (e.g. WithSpotifyAuth()).
		if user.Edges.SpotifyAuth == nil {
			return nil, nil
		}

		return &Provider{
			logger: logger,
			config: cfg,
			user:   user,
			auth:   user.Edges.SpotifyAuth,
		}, nil
	}
}

func (p *Provider) Type() providers.Type {
	return providers.TypeSpotify
}

func (p *Provider) GetRecentListens(ctx context.Context, since time.Time) ([]providers.Track, error) {
	p.logger.Info("fetching recent listens from spotify", "username", p.user.Username, "since", since)

	// TODO: Implement actual Spotify API call
	// 1. Refresh Access Token if expired
	// 2. Client := spotify.New(auth.Client(ctx, token))
	// 3. items, err := Client.PlayerRecentlyPlayed()

	// Returning stub data for now
	return []providers.Track{
		{
			ID:         "spotify:track:4cOdK2wGLETKBW3PvgPWqT",
			Name:       "Never Gonna Give You Up",
			Artist:     "Rick Astley",
			Album:      "Whenever You Need Somebody",
			DurationMs: 213573,
			PlayedAt:   time.Now().Add(-5 * time.Minute),
			URL:        "https://open.spotify.com/track/4cOdK2wGLETKBW3PvgPWqT",
		},
	}, nil
}

func (p *Provider) GetPlaylists(ctx context.Context) ([]providers.Playlist, error) {
	p.logger.Info("fetching playlists from spotify", "username", p.user.Username)
	return []providers.Playlist{}, nil
}

func (p *Provider) CreatePlaylist(ctx context.Context, name, description string, tracks []providers.Track) error {
	p.logger.Info("creating playlist on spotify", "username", p.user.Username, "name", name, "track_count", len(tracks))

	if len(tracks) == 0 {
		return fmt.Errorf("cannot create empty playlist")
	}

	// TODO: Implement actual Spotify API call
	// 1. Create playlist
	// 2. Add tracks to playlist

	return nil
}
