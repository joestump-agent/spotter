package handlers

import (
	"context"
	"fmt"
	"net/http"

	"spotter/ent/user"
	"spotter/internal/events"
	"spotter/internal/providers/lastfm"
)

// LastFMLogin initiates the Last.fm authentication flow.
func (h *Handler) LastFMLogin(w http.ResponseWriter, r *http.Request) {
	u := h.GetUser(r.Context())
	if u == nil {
		http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
		return
	}

	// Check if Last.fm is configured
	if h.Config.LastFM.APIKey == "" {
		h.Logger.Error("Last.fm API key not configured")
		http.Error(w, "Last.fm integration is not configured", http.StatusServiceUnavailable)
		return
	}

	// Create authenticator and get auth URL
	// Last.fm doesn't support state parameter natively, so we pass empty string
	authFactory := lastfm.NewAuthenticator(h.Logger, h.Config)
	authenticator := authFactory()
	authURL := authenticator.GetAuthURL("")

	h.Logger.Info("redirecting user to Last.fm Auth", "username", u.Username)
	http.Redirect(w, r, authURL, http.StatusSeeOther)
}

// LastFMCallback handles the callback from Last.fm.
func (h *Handler) LastFMCallback(w http.ResponseWriter, r *http.Request) {
	u := h.GetUser(r.Context())
	if u == nil {
		http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
		return
	}

	// Get authorization token
	token := r.URL.Query().Get("token")
	if token == "" {
		h.Logger.Warn("missing authorization token in Last.fm callback")
		http.Redirect(w, r, "/preferences/providers?error=missing_token", http.StatusSeeOther)
		return
	}

	// Exchange token for session
	authFactory := lastfm.NewAuthenticator(h.Logger, h.Config)
	authenticator := authFactory()
	authResult, err := authenticator.ExchangeCode(r.Context(), token)
	if err != nil {
		h.Logger.Error("failed to exchange Last.fm token", "error", err, "username", u.Username)
		http.Redirect(w, r, "/preferences/providers?error=exchange_failed", http.StatusSeeOther)
		return
	}

	// Check if user already has Last.fm auth (update vs create)
	u, err = h.Client.User.Query().
		Where(user.ID(u.ID)).
		WithLastfmAuth().
		Only(r.Context())
	if err != nil {
		h.Logger.Error("failed to query user", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	if u.Edges.LastfmAuth != nil {
		// Update existing auth
		_, err = h.Client.LastFMAuth.UpdateOneID(u.Edges.LastfmAuth.ID).
			SetSessionKey(authResult.AccessToken). // AuthResult stores session key in AccessToken field
			SetUsername(authResult.DisplayName).
			Save(r.Context())
	} else {
		// Create new auth
		_, err = h.Client.LastFMAuth.Create().
			SetUser(u).
			SetSessionKey(authResult.AccessToken).
			SetUsername(authResult.DisplayName).
			Save(r.Context())
	}

	if err != nil {
		h.Logger.Error("failed to save Last.fm auth", "error", err, "username", u.Username)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	h.Logger.Info("successfully connected Last.fm account",
		"username", u.Username,
		"lastfm_username", authResult.DisplayName)

	// Send notification
	h.Bus.Publish(u.ID, events.Event{
		Type: events.EventTypeNotification,
		Payload: events.NotificationPayload{
			Title:    "Last.fm Connected",
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

		h.Logger.Info("starting Last.fm sync after auth", "username", username)

		// Send notification that sync is starting
		h.Bus.Publish(userID, events.Event{
			Type: events.EventTypeNotification,
			Payload: events.NotificationPayload{
				Title:    "Syncing Last.fm",
				Message:  "Fetching your recent listens...",
				IconType: "info",
			},
		})

		if err := h.Syncer.Sync(ctx, syncUser); err != nil {
			h.Logger.Error("Last.fm sync failed", "error", err, "username", username)
			h.Bus.Publish(userID, events.Event{
				Type: events.EventTypeNotification,
				Payload: events.NotificationPayload{
					Title:    "Sync Failed",
					Message:  "Failed to sync Last.fm data. Please try again later.",
					IconType: "error",
				},
			})
		}
	}(u.ID, u.Username)

	http.Redirect(w, r, "/preferences/providers", http.StatusSeeOther)
}
