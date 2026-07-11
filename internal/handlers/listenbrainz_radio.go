// Governing: ADR-0030 (LB Radio through the standard playlist pipeline),
// SPEC music-provider-integration REQ "ListenBrainz Provider" (REQ-PROV-053)
//
// LB Radio lives under the playlists section: a small prompt page whose POST
// calls the ListenBrainz lb-radio endpoint and persists the result as a
// regular "listenbrainz"-source playlist. Everything downstream — catalog
// linking, the listenbrainz badge, the sync-to-Navidrome toggle, and track
// matching — is the existing playlist machinery, unchanged.
package handlers

import (
	"fmt"
	"net/http"
	"strings"

	"spotter/ent/user"
	"spotter/internal/providers"
	"spotter/internal/providers/listenbrainz"
	"spotter/internal/views/playlists"
)

// radioMaxPromptLength caps the LB Radio prompt so the derived remote ID
// (lb-radio:<prompt>) fits the 255-character playlist remote_id column.
// Governing: ADR-0030, SPEC music-provider-integration REQ-PROV-053
const radioMaxPromptLength = 200

// ListenBrainzRadioForm renders the LB Radio prompt page.
// GET /playlists/lb-radio
func (h *Handler) ListenBrainzRadioForm(w http.ResponseWriter, r *http.Request) {
	u := h.RequireUserRedirect(w, r)
	if u == nil {
		return
	}

	// Refresh the user with the ListenBrainz edge so the page can show a
	// connect hint instead of a form that can only fail.
	refreshed, err := h.Client.User.Query().
		Where(user.ID(u.ID)).
		WithListenbrainzAuth().
		Only(r.Context())
	if err != nil {
		h.Logger.Error("failed to query user for lb-radio form", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	h.Render(w, r, playlists.Radio(refreshed.Edges.ListenbrainzAuth != nil, h.Config))
}

// ListenBrainzRadioGenerate generates an LB Radio playlist from the submitted
// prompt and persists it as a regular playlist. On success the client is
// redirected to the playlist show page; generation failures render an inline
// alert into the form's result target. Both outcomes raise a toast.
// POST /playlists/lb-radio
// Governing: ADR-0030, SPEC music-provider-integration REQ-PROV-053
func (h *Handler) ListenBrainzRadioGenerate(w http.ResponseWriter, r *http.Request) {
	u := h.RequireUser(w, r)
	if u == nil {
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	prompt := strings.TrimSpace(r.FormValue("prompt"))
	if prompt == "" {
		http.Error(w, "prompt is required", http.StatusBadRequest)
		return
	}
	if verr := ValidateMaxLength("prompt", prompt, radioMaxPromptLength); verr != nil {
		h.BadRequest(w, verr)
		return
	}

	mode := r.FormValue("mode")
	if mode == "" {
		mode = listenbrainz.RadioModeEasy
	}
	if !listenbrainz.ValidRadioMode(mode) {
		http.Error(w, "mode must be easy, medium, or hard", http.StatusBadRequest)
		return
	}

	// Build the provider exactly as the syncer does (factory pattern,
	// ADR-0016): credentials come from the user's ListenbrainzAuth edge.
	refreshed, err := h.Client.User.Query().
		Where(user.ID(u.ID)).
		WithListenbrainzAuth().
		Only(r.Context())
	if err != nil {
		h.Logger.Error("failed to query user for lb-radio generate", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	prov, err := listenbrainz.New(h.Logger, h.Config)(r.Context(), refreshed)
	if err != nil {
		h.Logger.Error("failed to build listenbrainz provider for lb-radio", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	if prov == nil {
		http.Error(w, "Connect your ListenBrainz account before using LB Radio", http.StatusBadRequest)
		return
	}
	lb, ok := prov.(*listenbrainz.Provider)
	if !ok {
		h.Logger.Error("listenbrainz factory returned unexpected provider type")
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	pl, err := lb.RadioPlaylist(r.Context(), prompt, mode)
	if err != nil {
		// Prompt syntax errors come back from ListenBrainz as 400s with a
		// human-readable message; surface it inline instead of a bare 4xx so
		// the user can fix the prompt.
		h.Logger.Warn("lb-radio generation failed", "prompt", prompt, "mode", mode, "error", err)
		if h.Bus != nil {
			h.Bus.PublishNotification(u.ID, "LB Radio Failed", "Could not generate a playlist for this prompt", "error")
		}
		h.Render(w, r, playlists.RadioError(fmt.Sprintf("ListenBrainz could not generate a playlist: %v", err)))
		return
	}
	if len(pl.Tracks) == 0 {
		h.Logger.Info("lb-radio generation returned no tracks", "prompt", prompt, "mode", mode)
		if h.Bus != nil {
			h.Bus.PublishNotification(u.ID, "LB Radio", "No tracks were generated for this prompt", "warning")
		}
		h.Render(w, r, playlists.RadioError("ListenBrainz returned no tracks for this prompt. Try a broader prompt (e.g. tag:(rock)) or an easier mode."))
		return
	}

	// Persist through the same upsert + track-persist path the playlist
	// syncer uses. Same prompt => same remote ID => regeneration updates the
	// existing playlist in place (ADR-0030).
	saved, err := h.Syncer.UpsertGeneratedPlaylist(r.Context(), refreshed, providers.TypeListenBrainz, pl)
	if err != nil {
		h.Logger.Error("failed to persist lb-radio playlist", "prompt", prompt, "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	h.Logger.Info("lb-radio playlist persisted",
		"playlist_id", saved.ID,
		"prompt", prompt,
		"mode", mode,
		"tracks", len(pl.Tracks),
		"user", u.Username)

	if h.Bus != nil {
		h.Bus.PublishNotification(u.ID, "LB Radio Playlist Ready",
			fmt.Sprintf("Generated %d tracks for %q", len(pl.Tracks), prompt), "success")
	}

	target := fmt.Sprintf("/playlists/%d", saved.ID)
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", target)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}
