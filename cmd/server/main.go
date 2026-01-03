package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"spotter/ent"
	"spotter/ent/user"
	"spotter/internal/config"
	"spotter/internal/database"
	"spotter/internal/handlers"
	"spotter/internal/providers/navidrome"
	"spotter/internal/providers/spotify"
	"spotter/internal/services"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	// Load Config
	cfg, err := config.Load()
	if err != nil {
		logger.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	// Connect to Database
	client, err := database.NewClient(cfg.Database.Driver, cfg.Database.Source)
	if err != nil {
		logger.Error("failed to connect to database", "error", err)
		os.Exit(1)
	}
	defer client.Close()

	// Initialize Services
	syncer := services.NewSyncer(client, cfg, logger)
	syncer.Register(navidrome.New(logger, cfg))
	syncer.Register(spotify.New(logger, cfg))

	// Initialize Handlers
	h := handlers.New(client, cfg, logger, syncer)

	// Router setup
	r := chi.NewRouter()

	// Middleware
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(60 * time.Second))

	// Static Files
	fileServer := http.FileServer(http.Dir("./static"))
	r.Handle("/static/*", http.StripPrefix("/static", fileServer))

	// Public Routes
	r.Group(func(r chi.Router) {
		r.Get("/auth/login", h.Login)
		r.Post("/login", h.PostLogin)
		r.Get("/logout", h.Logout)
	})

	// Protected Routes
	r.Group(func(r chi.Router) {
		r.Use(AuthMiddleware(client))
		r.Get("/", h.Home)
		r.Post("/generate", h.GeneratePlaylist)

		r.Get("/preferences", h.Preferences)
		r.Post("/preferences/spotify/disconnect", h.DisconnectSpotify)
		r.Post("/preferences/lastfm/disconnect", h.DisconnectLastFM)

		r.Get("/recent", h.RecentListens)
		r.Post("/recent/refresh", h.RefreshRecentListens)
	})

	addr := fmt.Sprintf("%s:%s", cfg.Server.Host, cfg.Server.Port)
	logger.Info("starting server", "addr", addr)
	if err := http.ListenAndServe(addr, r); err != nil {
		logger.Error("server failed", "error", err)
		os.Exit(1)
	}
}

func AuthMiddleware(client *ent.Client) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cookie, err := r.Cookie("spotter_user")
			if err != nil {
				http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
				return
			}

			username := cookie.Value
			if username == "" {
				http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
				return
			}

			u, err := client.User.Query().
				Where(user.Username(username)).
				Only(r.Context())

			if err != nil {
				// User not found or db error, redirect to login
				http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
				return
			}

			ctx := context.WithValue(r.Context(), handlers.UserContextKey, u)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
