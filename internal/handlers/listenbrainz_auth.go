// Governing: ADR-0005 (Navidrome primary identity), ADR-0006 (AES-256-GCM encryption),
// SPEC music-provider-integration REQ "ListenBrainz Provider" (REQ-PROV-046)
//
// ListenBrainz has no OAuth flow: users paste their static user token from
// listenbrainz.org/settings. The connect handler validates the token against
// GET /1/validate-token before persisting it (encrypted at rest by the
// database hooks).
package handlers

import (
	"fmt"
	"net/http"
	"strings"

	"spotter/ent/user"
	"spotter/internal/events"
	"spotter/internal/providers"
	"spotter/internal/providers/listenbrainz"
	"spotter/internal/views/preferences"
)

// ListenBrainzConnectForm renders the paste-token form.
func (h *Handler) ListenBrainzConnectForm(w http.ResponseWriter, r *http.Request) {
	u := h.RequireUserRedirect(w, r)
	if u == nil {
		return
	}

	h.Render(w, r, preferences.ListenBrainzConnect(u, h.Config, r.URL.Query().Get("error")))
}

// ListenBrainzConnect validates the submitted token and stores it.
// Governing: SPEC music-provider-integration REQ-PROV-046 (validate-token on
// connect, token encrypted at rest per ADR-0006)
func (h *Handler) ListenBrainzConnect(w http.ResponseWriter, r *http.Request) {
	u := h.RequireUserRedirect(w, r)
	if u == nil {
		return
	}

	token := strings.TrimSpace(r.FormValue("token"))
	if token == "" {
		http.Redirect(w, r, "/auth/listenbrainz/connect?error=missing_token", http.StatusSeeOther)
		return
	}

	// Validate the token against the ListenBrainz API before persisting it.
	// The token itself is never logged.
	validator := listenbrainz.NewTokenValidator(h.Logger, h.Config)
	result, err := validator.ValidateToken(r.Context(), token)
	if err != nil {
		h.Logger.Error("failed to validate ListenBrainz token", "error", err, "username", u.Username)
		http.Redirect(w, r, "/auth/listenbrainz/connect?error=validation_failed", http.StatusSeeOther)
		return
	}
	if !result.Valid || result.UserName == "" {
		// A valid:true response without a user_name would store an auth row
		// with an empty label, so treat it as invalid too.
		h.Logger.Warn("ListenBrainz token rejected", "username", u.Username, "message", result.Message)
		http.Redirect(w, r, "/auth/listenbrainz/connect?error=invalid_token", http.StatusSeeOther)
		return
	}

	// Check if user already has ListenBrainz auth (update vs create)
	u, err = h.Client.User.Query().
		Where(user.ID(u.ID)).
		WithListenbrainzAuth().
		Only(r.Context())
	if err != nil {
		h.Logger.Error("failed to query user", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// Governing: SPEC music-provider-integration REQ "ListenBrainz Listen
	// Submission" (REQ-PROV-049) — submission is opt-in via the connect-form
	// checkbox and defaults OFF (an unchecked checkbox submits no value).
	submitListens := r.FormValue("submit_listens") != ""

	// Governing: ADR-0006 — the token is encrypted at rest by
	// encryptListenBrainzAuthHook registered in internal/database/hooks.go.
	if u.Edges.ListenbrainzAuth != nil {
		// Update existing auth
		_, err = h.Client.ListenBrainzAuth.UpdateOneID(u.Edges.ListenbrainzAuth.ID).
			SetToken(token).
			SetUsername(result.UserName).
			SetSubmitListens(submitListens).
			Save(r.Context())
	} else {
		// Create new auth
		_, err = h.Client.ListenBrainzAuth.Create().
			SetUser(u).
			SetToken(token).
			SetUsername(result.UserName).
			SetSubmitListens(submitListens).
			Save(r.Context())
	}

	if err != nil {
		h.Logger.Error("failed to save ListenBrainz auth", "error", err, "username", u.Username)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// Governing: SPEC-0015 REQ "Cooldown Reset on Recovery" — provider reconnected
	if h.Notifier != nil {
		if err := h.Notifier.ClearCooldown(r.Context(), u.ID, "listenbrainz"); err != nil {
			h.Logger.Error("failed to clear listenbrainz notification cooldown", "error", err)
		}
	}

	// Governing: SPEC error-handling REQ-STATE-004 — reconnecting is the user's
	// corrective action, so clear any fatal backoff state for ListenBrainz.
	if h.Syncer != nil {
		h.Syncer.ClearProviderBackoff(u.ID, providers.TypeListenBrainz)
	}

	h.Logger.Info("successfully connected ListenBrainz account",
		"username", u.Username,
		"listenbrainz_username", result.UserName)

	// Send notification
	h.Bus.Publish(u.ID, events.Event{
		Type: events.EventTypeNotification,
		Payload: events.NotificationPayload{
			Title:    "ListenBrainz Connected",
			Message:  fmt.Sprintf("Successfully connected as %s", result.UserName),
			IconType: "success",
		},
	})

	http.Redirect(w, r, "/preferences/providers", http.StatusSeeOther)
}
