package handlers

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/http"
	"time"

	"spotter/ent/user"
	"spotter/internal/events"
	"spotter/internal/providers/spotify"
)

const (
	spotifyStateCookie = "spotify_oauth_state"
	spotifyStateTTL    = 10 * time.Minute
)

// generateState creates a random state string for OAuth CSRF protection.
func generateState() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(b), nil
}

// SpotifyLogin initiates the Spotify OAuth flow.
func (h *Handler) SpotifyLogin(w http.ResponseWriter, r *http.Request) {
	u := h.GetUser(r.Context())
	if u == nil {
		http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
		return
	}

	// Check if Spotify is configured
	if h.Config.Spotify.ClientID == "" || h.Config.Spotify.ClientSecret == "" {
		h.Logger.Error("Spotify OAuth not configured")
		http.Error(w, "Spotify integration is not configured", http.StatusServiceUnavailable)
		return
	}

	// Generate state for CSRF protection
	state, err := generateState()
	if err != nil {
		h.Logger.Error("failed to generate OAuth state", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// Store state in cookie
	http.SetCookie(w, &http.Cookie{
		Name:     spotifyStateCookie,
		Value:    state,
		Path:     "/",
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(spotifyStateTTL),
	})

	// Create authenticator and get auth URL
	authFactory := spotify.NewAuthenticator(h.Logger, h.Config)
	authenticator := authFactory()
	authURL := authenticator.GetAuthURL(state)

	h.Logger.Info("redirecting user to Spotify OAuth", "username", u.Username)
	http.Redirect(w, r, authURL, http.StatusSeeOther)
}

// SpotifyCallback handles the OAuth callback from Spotify.
func (h *Handler) SpotifyCallback(w http.ResponseWriter, r *http.Request) {
	u := h.GetUser(r.Context())
	if u == nil {
		http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
		return
	}

	// Check for error from Spotify
	if errParam := r.URL.Query().Get("error"); errParam != "" {
		h.Logger.Warn("Spotify OAuth error", "error", errParam, "username", u.Username)
		http.Redirect(w, r, "/preferences/providers?error=spotify_denied", http.StatusSeeOther)
		return
	}

	// Verify state
	stateCookie, err := r.Cookie(spotifyStateCookie)
	if err != nil {
		h.Logger.Warn("missing OAuth state cookie", "error", err)
		http.Redirect(w, r, "/preferences/providers?error=invalid_state", http.StatusSeeOther)
		return
	}

	stateParam := r.URL.Query().Get("state")
	if stateParam == "" || stateParam != stateCookie.Value {
		h.Logger.Warn("OAuth state mismatch", "expected", stateCookie.Value, "got", stateParam)
		http.Redirect(w, r, "/preferences/providers?error=invalid_state", http.StatusSeeOther)
		return
	}

	// Clear the state cookie
	http.SetCookie(w, &http.Cookie{
		Name:     spotifyStateCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Expires:  time.Now().Add(-time.Hour),
	})

	// Get authorization code
	code := r.URL.Query().Get("code")
	if code == "" {
		h.Logger.Warn("missing authorization code in callback")
		http.Redirect(w, r, "/preferences/providers?error=missing_code", http.StatusSeeOther)
		return
	}

	// Exchange code for tokens
	authFactory := spotify.NewAuthenticator(h.Logger, h.Config)
	authenticator := authFactory()
	authResult, err := authenticator.ExchangeCode(r.Context(), code)
	if err != nil {
		h.Logger.Error("failed to exchange Spotify code", "error", err, "username", u.Username)
		http.Redirect(w, r, "/preferences/providers?error=exchange_failed", http.StatusSeeOther)
		return
	}

	// Check if user already has Spotify auth (update vs create)
	u, err = h.Client.User.Query().
		Where(user.ID(u.ID)).
		WithSpotifyAuth().
		Only(r.Context())
	if err != nil {
		h.Logger.Error("failed to query user", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	if u.Edges.SpotifyAuth != nil {
		// Update existing auth
		_, err = h.Client.SpotifyAuth.UpdateOneID(u.Edges.SpotifyAuth.ID).
			SetAccessToken(authResult.AccessToken).
			SetRefreshToken(authResult.RefreshToken).
			SetExpiry(authResult.Expiry).
			SetDisplayName(authResult.DisplayName).
			Save(r.Context())
	} else {
		// Create new auth
		_, err = h.Client.SpotifyAuth.Create().
			SetUser(u).
			SetAccessToken(authResult.AccessToken).
			SetRefreshToken(authResult.RefreshToken).
			SetExpiry(authResult.Expiry).
			SetDisplayName(authResult.DisplayName).
			Save(r.Context())
	}

	if err != nil {
		h.Logger.Error("failed to save Spotify auth", "error", err, "username", u.Username)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	h.Logger.Info("successfully connected Spotify account",
		"username", u.Username,
		"spotify_display_name", authResult.DisplayName)

	// Send notification
	h.Bus.Publish(u.ID, events.Event{
		Type: events.EventTypeNotification,
		Payload: events.NotificationPayload{
			Title:    "Spotify Connected",
			Message:  fmt.Sprintf("Successfully connected as %s", authResult.DisplayName),
			IconType: "success",
		},
	})

	// Trigger sync in background
	go func(userID int, username string) {
		ctx := context.Background()

		// Re-fetch user with all auth edges for sync
		syncUser, err := h.Client.User.Query().
			Where(user.ID(userID)).
			WithSpotifyAuth().
			WithNavidromeAuth().
			WithLastfmAuth().
			Only(ctx)
		if err != nil {
			h.Logger.Error("failed to fetch user for sync", "error", err, "username", username)
			return
		}

		h.Logger.Info("starting Spotify sync after OAuth", "username", username)

		// Send notification that sync is starting
		h.Bus.Publish(userID, events.Event{
			Type: events.EventTypeNotification,
			Payload: events.NotificationPayload{
				Title:    "Syncing Spotify",
				Message:  "Fetching your recent listens and playlists...",
				IconType: "info",
			},
		})

		if err := h.Syncer.Sync(ctx, syncUser); err != nil {
			h.Logger.Error("Spotify sync failed", "error", err, "username", username)
			h.Bus.Publish(userID, events.Event{
				Type: events.EventTypeNotification,
				Payload: events.NotificationPayload{
					Title:    "Sync Failed",
					Message:  "Failed to sync Spotify data. Please try again later.",
					IconType: "error",
				},
			})
		}
	}(u.ID, u.Username)

	http.Redirect(w, r, "/preferences/providers", http.StatusSeeOther)
}
