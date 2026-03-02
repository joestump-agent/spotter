package handlers

import (
	"net/http"

	"spotter/ent"
)

// RequireUser returns the authenticated user for API/HTMX handlers.
// If no user is authenticated, it writes a 401 Unauthorized response and returns nil.
// Callers MUST check the returned value: if nil, return immediately.
func (h *Handler) RequireUser(w http.ResponseWriter, r *http.Request) *ent.User {
	u := h.GetUser(r.Context())
	if u == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
	}
	return u
}

// RequireUserRedirect returns the authenticated user for page-level handlers.
// If no user is authenticated, it redirects to /auth/login and returns nil.
// Callers MUST check the returned value: if nil, return immediately.
func (h *Handler) RequireUserRedirect(w http.ResponseWriter, r *http.Request) *ent.User {
	u := h.GetUser(r.Context())
	if u == nil {
		http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
	}
	return u
}
