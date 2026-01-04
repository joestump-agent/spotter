package handlers

import (
	"context"
	"net/http"
	"strconv"

	"spotter/ent"
	"spotter/ent/listen"
	"spotter/ent/user"
	"spotter/internal/views/recent"
)

func (h *Handler) RecentListens(w http.ResponseWriter, r *http.Request) {
	u := h.GetUser(r.Context())
	if u == nil {
		http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
		return
	}

	// Refresh user to get pagination settings
	u, err := h.Client.User.Query().
		Where(user.ID(u.ID)).
		Only(r.Context())
	if err != nil {
		h.Logger.Error("failed to query user", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// Get page number from query
	page := 1
	if pageStr := r.URL.Query().Get("page"); pageStr != "" {
		if p, err := strconv.Atoi(pageStr); err == nil && p > 0 {
			page = p
		}
	}

	pageSize := u.PaginationSize
	offset := (page - 1) * pageSize

	listens, err := h.Client.Listen.Query().
		Where(listen.HasUserWith(user.ID(u.ID))).
		WithArtist().
		WithAlbum().
		WithTrack().
		Order(ent.Desc(listen.FieldPlayedAt)).
		Limit(pageSize).
		Offset(offset).
		All(r.Context())
	if err != nil {
		h.Logger.Error("failed to fetch recent listens", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// Get total count for pagination
	total, err := h.Client.Listen.Query().
		Where(listen.HasUserWith(user.ID(u.ID))).
		Count(r.Context())
	if err != nil {
		h.Logger.Error("failed to count listens", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	totalPages := (total + pageSize - 1) / pageSize

	h.Render(w, r, recent.Index(listens, page, totalPages, h.Config))
}

func (h *Handler) RefreshRecentListens(w http.ResponseWriter, r *http.Request) {
	u := h.GetUser(r.Context())
	if u == nil {
		http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
		return
	}

	// Trigger background sync
	go func() {
		if err := h.Syncer.SyncRecentListens(context.Background(), u); err != nil {
			h.Logger.Error("Background sync failed", "error", err, "username", u.Username)
		}
	}()

	// Reload the page
	w.Header().Set("HX-Redirect", "/recent")
}
