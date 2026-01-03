package providers

import (
	"context"
	"time"

	"spotter/ent"
)

// Type identifies the source of the data (e.g., "spotify", "navidrome").
type Type string

const (
	TypeSpotify   Type = "spotify"
	TypeNavidrome Type = "navidrome"
	TypeLastFM    Type = "lastfm"
)

// Track represents a normalized audio track across services.
type Track struct {
	ID         string // Provider specific ID
	Name       string
	Artist     string
	Album      string
	DurationMs int
	PlayedAt   time.Time // When the track was listened to (UTC)
	URL        string    // Deep link to the track
}

// Playlist represents a collection of tracks.
type Playlist struct {
	ID          string
	Name        string
	Description string
	Tracks      []Track
}

// Provider is the base interface that all external music services must implement.
type Provider interface {
	// Type returns the identifier for this provider.
	Type() Type
}

// HistoryFetcher is implemented by providers that can retrieve listening history.
type HistoryFetcher interface {
	Provider
	// GetRecentListens retrieves tracks played after the given timestamp.
	GetRecentListens(ctx context.Context, since time.Time) ([]Track, error)
}

// PlaylistManager is implemented by providers that can read/write playlists.
type PlaylistManager interface {
	Provider
	// GetPlaylists retrieves the user's playlists.
	GetPlaylists(ctx context.Context) ([]Playlist, error)
	// CreatePlaylist creates a new playlist with the given tracks.
	CreatePlaylist(ctx context.Context, name, description string, tracks []Track) error
}

// Factory defines the function signature for creating a provider instance for a specific user.
// Implementations should return nil, nil if the user is not configured for the provider.
type Factory func(ctx context.Context, user *ent.User) (Provider, error)
