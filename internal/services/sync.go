package services

import (
	"context"
	"log/slog"
	"time"

	"spotter/ent"
	"spotter/internal/config"
)

type Syncer struct {
	Client *ent.Client
	Config *config.Config
	Logger *slog.Logger
}

func NewSyncer(client *ent.Client, cfg *config.Config, logger *slog.Logger) *Syncer {
	return &Syncer{
		Client: client,
		Config: cfg,
		Logger: logger,
	}
}

// SyncRecentListens pulls recent listening history from connected services (Spotify, Last.fm)
// and persists them into the database.
func (s *Syncer) SyncRecentListens(ctx context.Context, u *ent.User) error {
	s.Logger.Info("Starting sync for user", "username", u.Username)

	// Simulate sync process
	time.Sleep(1 * time.Second)

	// TODO: Implement actual Spotify sync logic here
	// 1. Check if user has SpotifyAuth
	// 2. Refresh token if expired
	// 3. Fetch recently played tracks from Spotify API
	// 4. Save to Listen entity

	// TODO: Implement actual Last.fm sync logic here
	// 1. Check if user has LastFMAuth
	// 2. Fetch recent tracks from Last.fm API
	// 3. Save to Listen entity

	s.Logger.Info("Sync completed for user", "username", u.Username)
	return nil
}
