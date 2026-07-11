package middleware

import (
	"context"
	"log/slog"
	"net/http"

	"spotter/ent"
	"spotter/internal/auth"
	"spotter/internal/handlers"
)

// AuthMiddleware validates the JWT session cookie, loads the user, and stores
// it in the request context under handlers.UserContextKey. It was extracted
// from cmd/server/main.go so tests exercise the exact production middleware
// instead of a hand-copied mirror.
// Governing: ADR-0005 (Navidrome primary identity), ADR-0002 (Chi router), SPEC user-authentication REQ "MIDDLEWARE-001", REQ "MIDDLEWARE-002", REQ "MIDDLEWARE-003", REQ "MIDDLEWARE-004"
func AuthMiddleware(client *ent.Client, jwtManager *auth.JWTManager, logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Helper to redirect to login, handling HTMX requests
			redirectToLogin := func() {
				// Clear any invalid cookie
				http.SetCookie(w, &http.Cookie{
					Name:     handlers.CookieName,
					Value:    "",
					Path:     "/",
					HttpOnly: true,
					MaxAge:   -1,
				})

				if r.Header.Get("HX-Request") == "true" {
					w.Header().Set("HX-Redirect", "/auth/login")
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				// EventSource requests (SSE) must not be redirected — browsers
				// follow redirects transparently and then reconnect on error,
				// creating a tight polling loop against /auth/login. Return 401
				// instead so the browser's EventSource closes without retrying.
				if r.Header.Get("Accept") == "text/event-stream" {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
			}

			cookie, err := r.Cookie(handlers.CookieName)
			if err != nil {
				redirectToLogin()
				return
			}

			claims, err := jwtManager.ValidateToken(cookie.Value)
			if err != nil {
				logger.Info("auth: invalid JWT token", "path", r.URL.Path, "method", r.Method, "error", err)
				redirectToLogin()
				return
			}

			u, err := client.User.Get(r.Context(), claims.UserID)
			if err != nil {
				logger.Warn("auth: user not found for JWT claims", "user_id", claims.UserID, "path", r.URL.Path, "error", err)
				redirectToLogin()
				return
			}

			ctx := context.WithValue(r.Context(), handlers.UserContextKey, u)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
