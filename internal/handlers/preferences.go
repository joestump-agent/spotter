package handlers

import (
	"net/http"

	"spotter/ent/user"
	"spotter/internal/views/preferences"
)

func (h *Handler) Preferences(w http.ResponseWriter, r *http.Request) {
	u := h.GetUser(r.Context())
	if u == nil {
		http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
		return
	}

	// Refresh user to get edges
	u, err := h.Client.User.Query().
		Where(user.ID(u.ID)).
		WithSpotifyAuth().
		WithLastfmAuth().
		Only(r.Context())
	if err != nil {
		h.Logger.Error("failed to query user preferences", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	spotifyConnected := u.Edges.SpotifyAuth != nil
	lastfmConnected := u.Edges.LastfmAuth != nil

	h.Render(w, r, preferences.Index(spotifyConnected, lastfmConnected))
}

func (h *Handler) DisconnectSpotify(w http.ResponseWriter, r *http.Request) {
	u := h.GetUser(r.Context())
	if u == nil {
		http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
		return
	}

	u, err := h.Client.User.Query().
		Where(user.ID(u.ID)).
		WithSpotifyAuth().
		Only(r.Context())
	if err != nil {
		h.Logger.Error("failed to query user", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	if u.Edges.SpotifyAuth != nil {
		if err := h.Client.SpotifyAuth.DeleteOne(u.Edges.SpotifyAuth).Exec(r.Context()); err != nil {
			h.Logger.Error("failed to delete spotify auth", "error", err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
	}

	w.Header().Set("HX-Redirect", "/preferences")
}

func (h *Handler) DisconnectLastFM(w http.ResponseWriter, r *http.Request) {
	u := h.GetUser(r.Context())
	if u == nil {
		http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
		return
	}

	u, err := h.Client.User.Query().
		Where(user.ID(u.ID)).
		WithLastfmAuth().
		Only(r.Context())
	if err != nil {
		h.Logger.Error("failed to query user", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	if u.Edges.LastfmAuth != nil {
		if err := h.Client.LastFMAuth.DeleteOne(u.Edges.LastfmAuth).Exec(r.Context()); err != nil {
			h.Logger.Error("failed to delete lastfm auth", "error", err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
	}

	w.Header().Set("HX-Redirect", "/preferences")
}
