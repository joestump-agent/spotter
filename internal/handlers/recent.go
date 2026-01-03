package handlers

import (
	"context"
	"net/http"

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

	listens, err := h.Client.Listen.Query().
		Where(listen.HasUserWith(user.ID(u.ID))).
		Order(ent.Desc(listen.FieldPlayedAt)).
		Limit(50).
		All(r.Context())
	if err != nil {
		h.Logger.Error("failed to fetch recent listens", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	h.Render(w, r, recent.Index(listens))
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
