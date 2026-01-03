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
	ID            string
	Name          string
	Description   string
	ImageURL      string // URL to playlist cover art
	ExternalURL   string // Deep link to playlist on provider's website
	TrackCount    int    // Number of tracks in the playlist
	UniqueArtists int    // Number of unique artists in the playlist
	UniqueAlbums  int    // Number of unique albums in the playlist
	Tracks        []Track
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

// AuthConfig contains the configuration needed to start an OAuth flow.
type AuthConfig struct {
	AuthURL string // URL to redirect the user to for authentication
	State   string // State parameter for CSRF protection
}

// AuthResult contains the result of a successful authentication.
type AuthResult struct {
	AccessToken  string
	RefreshToken string
	Expiry       time.Time
	DisplayName  string // User's display name from the provider
	UserID       string // User's ID from the provider
}

// Authenticator is implemented by providers that support OAuth or similar authentication flows.
// This interface should NOT be used for Navidrome as it's the primary app authentication mechanism.
type Authenticator interface {
	Provider
	// SupportsAuth returns true if this provider supports user authentication from preferences.
	// Navidrome should return false as it's used for app login, not as a connected service.
	SupportsAuth() bool
	// GetAuthURL returns the URL to redirect the user to for authentication.
	// The state parameter should be stored in the session for verification on callback.
	GetAuthURL(state string) string
	// ExchangeCode exchanges the authorization code for access and refresh tokens.
	ExchangeCode(ctx context.Context, code string) (*AuthResult, error)
	// RefreshToken refreshes an expired access token.
	RefreshToken(ctx context.Context, refreshToken string) (*AuthResult, error)
	// Disconnect performs any cleanup needed when a user disconnects from the provider.
	// This might include revoking tokens if the provider supports it.
	Disconnect(ctx context.Context) error
}

// Factory defines the function signature for creating a provider instance for a specific user.
// Implementations should return nil, nil if the user is not configured for the provider.
type Factory func(ctx context.Context, user *ent.User) (Provider, error)

// AuthenticatorFactory defines the function signature for creating an authenticator instance.
// Unlike Factory, this doesn't require a user since it's used to initiate the auth flow.
type AuthenticatorFactory func() Authenticator
