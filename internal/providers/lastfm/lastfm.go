package lastfm

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
	auth   *ent.LastFMAuth
}

// Ensure Provider implements interfaces
var _ providers.HistoryFetcher = (*Provider)(nil)
var _ providers.Authenticator = (*Provider)(nil)

// New returns a factory that creates Last.fm providers for a given user.
func New(logger *slog.Logger, cfg *config.Config) providers.Factory {
	return func(ctx context.Context, user *ent.User) (providers.Provider, error) {
		// Check if the user has Last.fm authentication data.
		if user.Edges.LastfmAuth == nil {
			return nil, nil
		}

		return &Provider{
			logger: logger,
			config: cfg,
			user:   user,
			auth:   user.Edges.LastfmAuth,
		}, nil
	}
}

// NewAuthenticator returns an authenticator factory for Last.fm.
// This is used for the OAuth flow before a user is connected.
func NewAuthenticator(logger *slog.Logger, cfg *config.Config) providers.AuthenticatorFactory {
	return func() providers.Authenticator {
		return &Provider{
			logger: logger,
			config: cfg,
		}
	}
}

func (p *Provider) Type() providers.Type {
	return providers.TypeLastFM
}

// SupportsAuth returns true since Last.fm supports authentication from preferences.
func (p *Provider) SupportsAuth() bool {
	return true
}

// GetAuthURL returns the Last.fm authentication URL.
// TODO: Implement actual Last.fm auth flow
func (p *Provider) GetAuthURL(state string) string {
	// Last.fm uses a different auth flow - web auth
	// http://www.last.fm/api/auth/?api_key=xxx&cb=callback
	if p.config.LastFM.APIKey == "" {
		p.logger.Warn("Last.fm API key not configured")
		return ""
	}

	// TODO: Implement actual Last.fm web auth URL
	p.logger.Info("Last.fm auth URL requested (stubbed)")
	return fmt.Sprintf("http://www.last.fm/api/auth/?api_key=%s&cb=%s",
		p.config.LastFM.APIKey,
		p.config.LastFM.RedirectURL)
}

// ExchangeCode exchanges the authorization token for a session key.
// TODO: Implement actual Last.fm token exchange
func (p *Provider) ExchangeCode(ctx context.Context, code string) (*providers.AuthResult, error) {
	p.logger.Info("Last.fm code exchange requested (stubbed)", "code", code)

	// TODO: Implement actual Last.fm session key retrieval
	// Last.fm returns a token in the callback, which needs to be exchanged for a session key
	// using auth.getSession API method

	return nil, fmt.Errorf("Last.fm authentication not yet implemented")
}

// RefreshToken is not applicable for Last.fm as session keys don't expire.
func (p *Provider) RefreshToken(ctx context.Context, refreshToken string) (*providers.AuthResult, error) {
	// Last.fm session keys don't expire, so no refresh needed
	p.logger.Debug("Last.fm refresh token called (no-op, session keys don't expire)")
	return nil, fmt.Errorf("Last.fm session keys do not expire")
}

// Disconnect performs cleanup when disconnecting from Last.fm.
func (p *Provider) Disconnect(ctx context.Context) error {
	p.logger.Info("disconnecting from Last.fm", "user", p.user.Username)
	// Last.fm doesn't have a token revocation endpoint
	// The session key will be deleted from the database by the handler
	return nil
}

// GetRecentListens retrieves tracks played after the given timestamp.
// TODO: Implement actual Last.fm API call
func (p *Provider) GetRecentListens(ctx context.Context, since time.Time) ([]providers.Track, error) {
	p.logger.Info("fetching recent listens from Last.fm (stubbed)", "username", p.user.Username, "since", since)

	// TODO: Implement actual Last.fm API call
	// Use user.getRecentTracks API method
	// https://www.last.fm/api/show/user.getRecentTracks

	return []providers.Track{}, nil
}
