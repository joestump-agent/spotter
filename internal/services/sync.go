package services

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"spotter/ent"
	"spotter/ent/listen"
	"spotter/ent/user"
	"spotter/internal/config"
	"spotter/internal/providers"
)

type Syncer struct {
	Client    *ent.Client
	Config    *config.Config
	Logger    *slog.Logger
	Factories []providers.Factory
}

func NewSyncer(client *ent.Client, cfg *config.Config, logger *slog.Logger) *Syncer {
	return &Syncer{
		Client:    client,
		Config:    cfg,
		Logger:    logger,
		Factories: []providers.Factory{},
	}
}

// Register adds a new provider factory to the syncer.
func (s *Syncer) Register(factory providers.Factory) {
	s.Factories = append(s.Factories, factory)
}

// Sync performs a full synchronization (history and playlists) for the user.
func (s *Syncer) Sync(ctx context.Context, u *ent.User) error {
	s.Logger.Info("Starting full sync", "username", u.Username)

	activeProviders, err := s.getActiveProviders(ctx, u)
	if err != nil {
		return err
	}

	// 1. History
	if err := s.syncHistory(ctx, u, activeProviders); err != nil {
		s.Logger.Error("failed to sync history", "username", u.Username, "error", err)
	}

	// 2. Playlists
	if err := s.syncPlaylists(ctx, u, activeProviders); err != nil {
		s.Logger.Error("failed to sync playlists", "username", u.Username, "error", err)
	}

	s.Logger.Info("Full sync completed", "username", u.Username)
	return nil
}

// SyncRecentListens pulls recent listening history from all registered providers.
func (s *Syncer) SyncRecentListens(ctx context.Context, u *ent.User) error {
	activeProviders, err := s.getActiveProviders(ctx, u)
	if err != nil {
		return err
	}
	return s.syncHistory(ctx, u, activeProviders)
}

// SyncPlaylists pulls playlists from all registered providers.
func (s *Syncer) SyncPlaylists(ctx context.Context, u *ent.User) error {
	activeProviders, err := s.getActiveProviders(ctx, u)
	if err != nil {
		return err
	}
	return s.syncPlaylists(ctx, u, activeProviders)
}

func (s *Syncer) getActiveProviders(ctx context.Context, u *ent.User) ([]providers.Provider, error) {
	// Refresh user to ensure we have all auth edges loaded so factories can check configuration.
	// We need these so the factories can decide if they can create a provider.
	u, err := s.Client.User.Query().
		Where(user.ID(u.ID)).
		WithSpotifyAuth().
		WithNavidromeAuth().
		WithLastfmAuth().
		Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to refresh user data: %w", err)
	}

	var active []providers.Provider
	for _, factory := range s.Factories {
		provider, err := factory(ctx, u)
		if err != nil {
			s.Logger.Error("failed to create provider", "error", err, "username", u.Username)
			continue
		}
		if provider != nil {
			active = append(active, provider)
		}
	}
	return active, nil
}

func (s *Syncer) syncHistory(ctx context.Context, u *ent.User, activeProviders []providers.Provider) error {
	for _, provider := range activeProviders {
		// Check if provider supports history fetching
		fetcher, ok := provider.(providers.HistoryFetcher)
		if !ok {
			continue
		}

		// Determine the last sync time for this provider/source to optimize fetching.
		// We query the latest listen for this specific user and source.
		lastListen, _ := s.Client.Listen.Query().
			Where(
				listen.HasUserWith(user.ID(u.ID)),
				listen.Source(string(provider.Type())),
			).
			Order(ent.Desc(listen.FieldPlayedAt)).
			First(ctx)

		var since time.Time
		if lastListen != nil {
			since = lastListen.PlayedAt
		} else {
			// Default to 24 hours ago if no history exists
			since = time.Now().Add(-24 * time.Hour)
		}

		tracks, err := fetcher.GetRecentListens(ctx, since)
		if err != nil {
			s.Logger.Error("failed to fetch recent listens",
				"provider", provider.Type(),
				"username", u.Username,
				"error", err,
			)
			continue
		}

		if len(tracks) > 0 {
			s.Logger.Info("found new tracks", "count", len(tracks), "provider", provider.Type())
			if err := s.persistListens(ctx, u, provider.Type(), tracks); err != nil {
				s.Logger.Error("failed to persist listens", "error", err)
			}
		}
	}
	return nil
}

func (s *Syncer) syncPlaylists(ctx context.Context, u *ent.User, activeProviders []providers.Provider) error {
	for _, provider := range activeProviders {
		manager, ok := provider.(providers.PlaylistManager)
		if !ok {
			continue
		}

		// Placeholder for playlist sync logic
		// We just call GetPlaylists to exercise the provider
		s.Logger.Info("syncing playlists", "provider", provider.Type(), "username", u.Username)
		if _, err := manager.GetPlaylists(ctx); err != nil {
			s.Logger.Error("failed to get playlists",
				"provider", provider.Type(),
				"username", u.Username,
				"error", err,
			)
		}
	}
	return nil
}

func (s *Syncer) persistListens(ctx context.Context, u *ent.User, source providers.Type, tracks []providers.Track) error {
	for _, track := range tracks {
		// Basic validation
		if track.Name == "" || track.Artist == "" {
			continue
		}

		// Check if it exists to avoid unique constraint violations.
		// We use the fields defined in the unique index: played_at, source, track_name, artist_name, user.
		exists, err := s.Client.Listen.Query().
			Where(
				listen.HasUserWith(user.ID(u.ID)),
				listen.Source(string(source)),
				listen.PlayedAt(track.PlayedAt),
				listen.TrackName(track.Name),
				listen.ArtistName(track.Artist),
			).
			Exist(ctx)

		if err != nil {
			s.Logger.Warn("failed to check existence of listen", "error", err)
			continue
		}

		if exists {
			continue
		}

		builder := s.Client.Listen.Create().
			SetUser(u).
			SetTrackName(track.Name).
			SetArtistName(track.Artist).
			SetAlbumName(track.Album).
			SetSource(string(source)).
			SetPlayedAt(track.PlayedAt)

		if track.URL != "" {
			builder.SetURL(track.URL)
		}

		if err := builder.Exec(ctx); err != nil {
			s.Logger.Warn("failed to save listen",
				"track", track.Name,
				"provider", source,
				"error", err,
			)
		}
	}
	return nil
}
