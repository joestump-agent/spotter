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
	"spotter/internal/enrichers"
	enricherFanart "spotter/internal/enrichers/fanart"
	enricherLastfm "spotter/internal/enrichers/lastfm"
	enricherMusicbrainz "spotter/internal/enrichers/musicbrainz"
	enricherNavidrome "spotter/internal/enrichers/navidrome"
	enricherSpotify "spotter/internal/enrichers/spotify"
	"spotter/internal/events"
	"spotter/internal/handlers"
	internalMiddleware "spotter/internal/middleware"
	"spotter/internal/providers/lastfm"
	"spotter/internal/providers/navidrome"
	"spotter/internal/providers/spotify"
	"spotter/internal/services"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

func main() {
	opts := &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}
	logger := slog.New(slog.NewTextHandler(os.Stdout, opts))

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

	// Initialize Event Bus
	bus := events.NewBus()

	// Initialize Sync Service (for playlists and listens)
	syncer := services.NewSyncer(client, cfg, logger, bus)
	syncer.Register(navidrome.New(logger, cfg))
	syncer.Register(spotify.New(logger, cfg))
	syncer.Register(lastfm.New(logger, cfg))

	// Initialize Metadata Service (for catalog enrichment)
	metadataSvc := services.NewMetadataService(client, cfg, logger, bus)
	metadataSvc.Register(enrichers.TypeMusicBrainz, enricherMusicbrainz.New(logger, cfg))
	metadataSvc.Register(enrichers.TypeNavidrome, enricherNavidrome.New(logger, cfg))
	metadataSvc.Register(enrichers.TypeSpotify, enricherSpotify.New(logger, cfg))
	metadataSvc.Register(enrichers.TypeLastFM, enricherLastfm.New(logger, cfg))
	metadataSvc.Register(enrichers.TypeFanart, enricherFanart.New(logger, cfg))

	// Initialize Handlers
	h := handlers.New(client, cfg, logger, syncer, metadataSvc, bus)

	// Background Sync Loop for listens/playlists
	syncInterval, err := time.ParseDuration(cfg.Sync.Interval)
	if err != nil {
		logger.Error("invalid sync interval, using default 5m", "error", err, "value", cfg.Sync.Interval)
		syncInterval = 5 * time.Minute
	}
	logger.Info("background sync configured", "interval", syncInterval)

	go func() {
		ticker := time.NewTicker(syncInterval)
		defer ticker.Stop()
		for range ticker.C {
			ctx := context.Background()
			users, err := client.User.Query().All(ctx)
			if err != nil {
				logger.Error("failed to fetch users for background sync", "error", err)
				continue
			}
			for _, u := range users {
				go func(user *ent.User) {
					if err := syncer.Sync(ctx, user); err != nil {
						logger.Error("background sync failed", "username", user.Username, "error", err)
					}
				}(u)
			}
		}
	}()

	// Background Metadata Enrichment Loop
	if cfg.Metadata.Enabled {
		metadataInterval, err := time.ParseDuration(cfg.Metadata.Interval)
		if err != nil {
			logger.Error("invalid metadata interval, using default 1h", "error", err, "value", cfg.Metadata.Interval)
			metadataInterval = 1 * time.Hour
		}
		logger.Info("metadata enrichment configured",
			"interval", metadataInterval,
			"order", cfg.MetadataEnricherOrder())

		go func() {
			// Initial delay to let the app start up
			time.Sleep(30 * time.Second)

			// Run immediately on startup
			runMetadataSync(client, metadataSvc, logger)

			ticker := time.NewTicker(metadataInterval)
			defer ticker.Stop()
			for range ticker.C {
				runMetadataSync(client, metadataSvc, logger)
			}
		}()
	} else {
		logger.Info("metadata enrichment disabled")
	}

	// Router setup
	r := chi.NewRouter()

	// Middleware
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(internalMiddleware.Logger(logger))
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
		r.Get("/events", h.Events)
		r.Post("/generate", h.GeneratePlaylist)

		r.Get("/preferences", h.PreferencesRedirect)
		r.Get("/preferences/appearance", h.PreferencesAppearance)
		r.Post("/preferences/appearance", h.PostPreferencesAppearance)
		r.Get("/preferences/ai", h.PreferencesAI)
		r.Post("/preferences/ai", h.PostPreferencesAI)
		r.Get("/preferences/providers", h.PreferencesProviders)
		r.Get("/preferences/tasks", h.PreferencesTasks)

		// Provider-specific sync/rebuild routes
		r.Post("/preferences/navidrome/sync", h.SyncNavidrome)
		r.Post("/preferences/navidrome/rebuild", h.RebuildNavidrome)
		r.Post("/preferences/spotify/sync", h.SyncSpotify)
		r.Post("/preferences/spotify/rebuild", h.RebuildSpotify)
		r.Post("/preferences/spotify/disconnect", h.DisconnectSpotify)
		r.Post("/preferences/lastfm/sync", h.SyncLastFM)
		r.Post("/preferences/lastfm/rebuild", h.RebuildLastFM)
		r.Post("/preferences/lastfm/disconnect", h.DisconnectLastFM)

		// Task routes
		r.Post("/preferences/tasks/sync-listens", h.TaskSyncListens)
		r.Post("/preferences/tasks/sync-playlists", h.TaskSyncPlaylists)
		r.Post("/preferences/tasks/enrich-metadata", h.TaskEnrichMetadata)
		r.Post("/preferences/tasks/reset", h.TaskResetData)
		r.Post("/preferences/tasks/cleanup", h.TaskCleanup)

		// Spotify OAuth
		r.Get("/auth/spotify/login", h.SpotifyLogin)
		r.Get("/auth/spotify/callback", h.SpotifyCallback)

		r.Get("/recent", h.RecentListens)
		r.Get("/playlists", h.Playlists)
	})

	addr := fmt.Sprintf("%s:%s", cfg.Server.Host, cfg.Server.Port)
	logger.Info("starting server", "addr", addr)
	if err := http.ListenAndServe(addr, r); err != nil {
		logger.Error("server failed", "error", err)
		os.Exit(1)
	}
}

// runMetadataSync runs metadata enrichment for all users.
func runMetadataSync(client *ent.Client, metadataSvc *services.MetadataService, logger *slog.Logger) {
	ctx := context.Background()
	users, err := client.User.Query().All(ctx)
	if err != nil {
		logger.Error("failed to fetch users for metadata sync", "error", err)
		return
	}
	for _, u := range users {
		go func(user *ent.User) {
			if err := metadataSvc.SyncAll(ctx, user); err != nil {
				logger.Error("metadata sync failed", "username", user.Username, "error", err)
			}
		}(u)
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
