package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"spotter/ent"
	"spotter/internal/auth"
	"spotter/internal/config"
	"spotter/internal/crypto"
	"spotter/internal/database"
	"spotter/internal/enrichers"
	enricherFanart "spotter/internal/enrichers/fanart"
	enricherLastfm "spotter/internal/enrichers/lastfm"
	enricherLidarr "spotter/internal/enrichers/lidarr"
	enricherListenbrainz "spotter/internal/enrichers/listenbrainz"
	enricherMusicbrainz "spotter/internal/enrichers/musicbrainz"
	enricherNavidrome "spotter/internal/enrichers/navidrome"
	enricherOpenai "spotter/internal/enrichers/openai"
	enricherSpotify "spotter/internal/enrichers/spotify"
	"spotter/internal/events"
	"spotter/internal/handlers"
	"spotter/internal/mailer"
	internalMiddleware "spotter/internal/middleware"
	"spotter/internal/migrations"
	"spotter/internal/notifications"
	"spotter/internal/providers/lastfm"
	"spotter/internal/providers/listenbrainz"
	"spotter/internal/providers/navidrome"
	"spotter/internal/providers/spotify"
	"spotter/internal/services"
	"spotter/internal/vibes"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"golang.org/x/time/rate"
)

func main() {
	// Governing: ADR-0019 (structured metrics), ADR-0010 (slog), SPEC observability REQ "FMT-001", REQ "FMT-002", REQ "FMT-003", REQ "FMT-004", REQ "FMT-005"
	// Bootstrap logger with text handler; will be replaced after config load
	bootstrapOpts := &slog.HandlerOptions{Level: slog.LevelDebug}
	logger := slog.New(slog.NewTextHandler(os.Stdout, bootstrapOpts))

	// Load Config
	cfg, err := config.Load()
	if err != nil {
		logger.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	// Re-initialize logger with configured format
	opts := &slog.HandlerOptions{Level: slog.LevelDebug}
	format := strings.ToLower(cfg.Log.Format)
	if format != "json" {
		format = "text" // REQ "FMT-002": invalid values default to text
	}
	if format == "json" {
		logger = slog.New(slog.NewJSONHandler(os.Stdout, opts))
	} else {
		logger = slog.New(slog.NewTextHandler(os.Stdout, opts))
	}
	logger.Info("logger initialized", "format", format, "level", "debug")

	// Initialize encryption for sensitive data
	encryptionKey, err := cfg.GetEncryptionKeyBytes()
	if err != nil {
		logger.Error("failed to get encryption key", "error", err)
		os.Exit(1)
	}
	encryptor, err := crypto.NewEncryptor(encryptionKey)
	if err != nil {
		logger.Error("failed to initialize encryptor", "error", err)
		os.Exit(1)
	}
	logger.Info("encryption initialized for sensitive data")

	// Initialize JWT Manager
	jwtManager := auth.NewJWTManager(cfg.Security.JWTSecret)
	logger.Info("JWT manager initialized")

	// Connect to Database
	client, err := database.NewClient(context.Background(), cfg.Database.Driver, cfg.Database.Source, encryptor, logger)
	if err != nil {
		logger.Error("failed to connect to database", "error", err)
		os.Exit(1)
	}
	defer func() {
		if err := client.Close(); err != nil {
			logger.Error("failed to close database client", "error", err)
			return
		}
		logger.Info("database connection closed")
	}()

	// Open a persistent raw *sql.DB for operations outside the Ent client (e.g. entity_tags upserts).
	// Governing: SPEC-0014 REQ "Denormalized Entity Tags Table"
	rawDB, err := database.OpenRawDB(cfg.Database.Driver, cfg.Database.Source)
	if err != nil {
		logger.Error("failed to open raw database connection", "error", err)
		os.Exit(1)
	}
	defer func() { _ = rawDB.Close() }()

	// One-time backfill of legacy JSON tag fields into the Tag entity and
	// entity_tags table. Runs post-migration (NewClient already ran
	// Schema.Create) and is guarded per-user so boots after the first are
	// cheap. Lives here rather than in database.NewClient because
	// internal/migrations depends on internal/tags -> internal/database.
	// Governing: SPEC-0014 REQ "Data Migration"
	if _, err := migrations.BackfillTagsIfNeeded(context.Background(), client, rawDB, logger); err != nil {
		logger.Error("tag backfill failed", "error", err)
	}

	// Initialize Event Bus
	bus := events.NewBus()

	// Governing: SPEC-0015 REQ "SMTP Configuration", ADR-0026
	// Initialize Mailer (NoopMailer if SMTP not configured)
	mailClient := mailer.New(mailer.Config{
		Host:     cfg.SMTP.Host,
		Port:     cfg.SMTP.Port,
		Username: cfg.SMTP.Username,
		Password: cfg.SMTP.Password,
		From:     cfg.SMTP.From,
		TLS:      cfg.SMTP.TLS,
	}, logger)
	if mailClient.IsConfigured() {
		logger.Info("smtp configured", "host", cfg.SMTP.Host, "port", cfg.SMTP.Port)
	} else {
		logger.Info("smtp disabled, using noop mailer")
	}

	// Governing: SPEC-0015 REQ "Notification Trigger", REQ "Cooldown Persistence", ADR-0026
	// Initialize Notifier (DBNotifier if SMTP configured, NoopNotifier otherwise)
	var notifier services.SyncNotifier
	if mailClient.IsConfigured() {
		// Governing: SPEC-0015 REQ "Email Content" — email links point at this Spotter instance
		notifier = notifications.NewDBNotifier(client, mailClient, cfg.Notifications.FailureCooldownDays, cfg.SpotterBaseURL(), logger)
		logger.Info("notification service initialized", "cooldown_days", cfg.Notifications.FailureCooldownDays)
	} else {
		notifier = notifications.NewNoopNotifier(logger)
		logger.Info("notification service disabled (smtp not configured)")
	}

	// Governing: ADR-0016 (pluggable provider factory), SPEC listen-playlist-sync REQ-SYNC-001 (factory registration at startup)
	// Initialize Sync Service (for playlists and listens)
	syncer := services.NewSyncer(client, cfg, logger, bus, notifier)
	syncer.Register(navidrome.New(logger, cfg))
	syncer.Register(spotify.New(logger, cfg, client))
	syncer.Register(lastfm.New(logger, cfg))
	// Governing: ADR-0016, SPEC music-provider-integration REQ "ListenBrainz Provider" (REQ-PROV-045)
	syncer.Register(listenbrainz.New(logger, cfg))

	// Initialize Playlist Sync Service (for syncing playlists to Navidrome)
	playlistSyncSvc := services.NewPlaylistSyncService(client, cfg, logger, bus)
	playlistSyncSvc.Register(navidrome.New(logger, cfg))

	// Initialize Metadata Service (for catalog enrichment)
	// Governing: SPEC metadata-enrichment-pipeline REQ-ENRICH-050 (duplicate registration fails at startup)
	metadataSvc := services.NewMetadataService(client, rawDB, cfg, logger, bus)
	registerEnricher := func(t enrichers.Type, factory enrichers.Factory) {
		if err := metadataSvc.Register(t, factory); err != nil {
			logger.Error("failed to register enricher", "type", t, "error", err)
			os.Exit(1)
		}
	}
	registerEnricher(enrichers.TypeLidarr, enricherLidarr.New(logger, cfg, client))
	registerEnricher(enrichers.TypeMusicBrainz, enricherMusicbrainz.New(logger, cfg))
	registerEnricher(enrichers.TypeNavidrome, enricherNavidrome.New(logger, cfg))
	registerEnricher(enrichers.TypeSpotify, enricherSpotify.New(logger, cfg, client))
	registerEnricher(enrichers.TypeLastFM, enricherLastfm.New(logger, cfg))
	// Governing: SPEC metadata-enrichment-pipeline REQ "ListenBrainz Enricher" (REQ-ENRICH-060)
	registerEnricher(enrichers.TypeListenBrainz, enricherListenbrainz.New(logger, cfg))
	registerEnricher(enrichers.TypeFanart, enricherFanart.New(logger, cfg))
	registerEnricher(enrichers.TypeOpenAI, enricherOpenai.New(logger, cfg))

	// Initialize Mixtape Generator Service (for AI-powered mixtape generation)
	mixtapeGenerator := vibes.NewMixtapeGenerator(client, cfg, logger, bus)
	logger.Info("vibes mixtape generator initialized",
		"default_max_tracks", cfg.Vibes.DefaultMaxTracks,
		"model", cfg.GetVibesModel(),
		"temperature", cfg.Vibes.Temperature)

	// Initialize Playlist Enhancer Service (for AI-powered playlist enhancement)
	playlistEnhancer := vibes.NewPlaylistEnhancer(client, cfg, logger, bus)
	logger.Info("playlist enhancer initialized")

	// Initialize Similar Artists Service (for AI-powered artist similarity detection)
	similarArtistsSvc := services.NewSimilarArtistsService(client, cfg, logger, bus)
	logger.Info("similar artists service initialized")

	// Initialize Handlers
	h := handlers.New(client, cfg, logger, encryptor, jwtManager, syncer, metadataSvc, playlistSyncSvc, mixtapeGenerator, playlistEnhancer, similarArtistsSvc, bus, notifier)

	// Governing: ADR-0007 (graceful shutdown), ADR-0018 (graceful shutdown), SPEC graceful-shutdown REQ "SHUTDOWN-001"
	// Governing: SPEC graceful-shutdown REQ-SIG-001 (signal.NotifyContext for SIGTERM/SIGINT)
	// Governing: SPEC graceful-shutdown REQ-SIG-002 (root context is parent for all background loops)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	// Governing: SPEC graceful-shutdown REQ-SIG-003 (defer stop to release signal resources)
	defer stop()

	// Governing: SPEC graceful-shutdown REQ-TMO-001 (30s default shutdown budget)
	// Governing: SPEC graceful-shutdown REQ-TMO-005 (configurable shutdown timeout)
	// Governing: ADR-0009 (Viper configuration: server.shutdown_timeout)
	shutdownTimeout, err := time.ParseDuration(cfg.Server.ShutdownTimeout)
	if err != nil {
		logger.Error("invalid server.shutdown_timeout, using default 30s", "error", err, "value", cfg.Server.ShutdownTimeout)
		shutdownTimeout = 30 * time.Second
	}

	// Governing: SPEC graceful-shutdown REQ-SEM-001 through REQ-SEM-004 (bounded concurrency semaphore)
	// Governing: SPEC graceful-shutdown REQ-SEM-002 (configurable semaphore capacity, default 10)
	// Governing: ADR-0009 (Viper configuration: server.max_concurrent_jobs)
	maxJobs := cfg.Server.MaxConcurrentJobs
	if maxJobs <= 0 {
		logger.Error("invalid server.max_concurrent_jobs, using default 10", "value", cfg.Server.MaxConcurrentJobs)
		maxJobs = 10
	}
	sem := make(chan struct{}, maxJobs)

	// Governing: SPEC graceful-shutdown REQ-WG-001 (single shared WaitGroup for all per-user goroutines)
	var wg sync.WaitGroup

	// Governing: SPEC metadata-enrichment-pipeline REQ-ENRICH-041, ADR-0013
	// Per-user in-flight guard shared by all background loops so a tick skips
	// users whose previous run for the same loop is still active.
	inflight := services.NewInFlightGuard()

	// Governing: SPEC observability REQ "BG-001", REQ "BG-002" (metric.background_tick emitted
	// after all per-user work for the tick completes, with real duration and error counts)
	// Governing: SPEC metadata-enrichment-pipeline REQ-ENRICH-041 (overlapping per-user runs skipped)
	// runPerUserTick fans out one background job goroutine per user for a single
	// tick of the named loop, then emits metric.background_tick once every
	// spawned goroutine has finished. Users whose previous run for this loop is
	// still in flight are skipped.
	runPerUserTick := func(loop string, run func(*ent.User) error) {
		tickStart := time.Now()
		// Governing: SPEC graceful-shutdown REQ-CTX-003 (per-user goroutines use root ctx)
		users, err := client.User.Query().All(ctx)
		if err != nil {
			logger.Error("failed to fetch users for background tick", "loop", loop, "error", err)
			return
		}
		var tickWg sync.WaitGroup
		var errCount atomic.Int64
		processed, skipped := 0, 0
		for _, u := range users {
			// Governing: SPEC metadata-enrichment-pipeline REQ-ENRICH-041 (skip overlapping runs)
			if !inflight.TryAcquire(loop, u.ID) {
				logger.Info("skipping user, previous background run still in flight",
					"loop", loop, "username", u.Username)
				skipped++
				continue
			}
			processed++
			// Governing: SPEC graceful-shutdown REQ-WG-002 (wg.Add before spawn, defer wg.Done first)
			wg.Add(1)
			tickWg.Add(1)
			go func(user *ent.User) {
				defer wg.Done()
				defer tickWg.Done()
				defer inflight.Release(loop, user.ID)
				// Governing: SPEC graceful-shutdown REQ-SEM-003, REQ-SEM-004 (semaphore acquire with ctx)
				select {
				case sem <- struct{}{}:
					defer func() { <-sem }()
				case <-ctx.Done():
					return
				}
				// Governing: SPEC graceful-shutdown REQ-CTX-002 (cancelled ctx passed to service methods)
				if err := run(user); err != nil {
					// Governing: SPEC observability REQ "BG-002" (per-user errors logged at Error level)
					logger.Error("background job failed", "loop", loop, "username", user.Username, "error", err)
					errCount.Add(1)
				}
			}(u)
		}
		// Governing: SPEC observability REQ "BG-001" (emit only after per-user work completes)
		wg.Add(1)
		go func() {
			defer wg.Done()
			tickWg.Wait()
			logger.Info("metric.background_tick",
				"loop", loop,
				"users_processed", processed,
				"users_skipped", skipped,
				"duration_ms", time.Since(tickStart).Milliseconds(),
				"errors", errCount.Load())
		}()
	}

	// Governing: ADR-0013 (goroutine ticker scheduling), SPEC listen-playlist-sync REQ-SYNC-040 (configurable ticker interval)
	// Governing: SPEC listen-playlist-sync REQ-SYNC-041 (per-user goroutines for parallel sync)
	// Background Sync Loop for listens/playlists
	syncInterval, err := time.ParseDuration(cfg.Sync.Interval)
	if err != nil {
		logger.Error("invalid sync interval, using default 5m", "error", err, "value", cfg.Sync.Interval)
		syncInterval = 5 * time.Minute
	}
	logger.Info("background sync configured", "interval", syncInterval)

	// Governing: ADR-0019 (structured metrics), SPEC observability REQ "BG-001", REQ "BG-002"
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(syncInterval)
		defer ticker.Stop()
		// Governing: SPEC graceful-shutdown REQ-CTX-001 (select on ctx.Done vs ticker.C, log shutdown)
		for {
			select {
			case <-ctx.Done():
				logger.Info("loop shutting down", "loop", "sync")
				return
			case <-ticker.C:
				// Governing: SPEC metadata-enrichment-pipeline REQ-ENRICH-041,
				// SPEC observability REQ "BG-001"
				runPerUserTick("sync", func(user *ent.User) error {
					return syncer.Sync(ctx, user)
				})
			}
		}
	}()

	// Background Metadata Enrichment Loop
	// Governing: SPEC metadata-enrichment-pipeline REQ-ENRICH-040 (background scheduler on configurable interval),
	// SPEC metadata-enrichment-pipeline REQ-ENRICH-042 (per-user isolation in enrichment runs),
	// SPEC metadata-enrichment-pipeline REQ-ENRICH-043 (MetadataService coordinates all enrichers)
	if cfg.Metadata.Enabled {
		metadataInterval, err := time.ParseDuration(cfg.Metadata.Interval)
		if err != nil {
			logger.Error("invalid metadata interval, using default 1h", "error", err, "value", cfg.Metadata.Interval)
			metadataInterval = 1 * time.Hour
		}
		metadataInitialDelay := cfg.MetadataInitialDelay()
		logger.Info("metadata enrichment configured",
			"interval", metadataInterval,
			"initial_delay", metadataInitialDelay,
			"order", cfg.MetadataEnricherOrder())

		wg.Add(1)
		go func() {
			defer wg.Done()

			// Governing: ADR-0019 (structured metrics), SPEC observability REQ "BG-001", REQ "BG-002"
			// Governing: SPEC metadata-enrichment-pipeline REQ-ENRICH-041 (overlapping runs skipped)
			syncMetadataForUsers := func() {
				runPerUserTick("metadata", func(user *ent.User) error {
					return metadataSvc.SyncAll(ctx, user)
				})
			}

			// Initial delay to let the app start up (metadata.initial_delay;
			// zero skips the delay entirely)
			if metadataInitialDelay > 0 {
				select {
				case <-ctx.Done():
					// Governing: SPEC graceful-shutdown REQ-CTX-001 (log loop shutdown)
					logger.Info("loop shutting down", "loop", "metadata")
					return
				case <-time.After(metadataInitialDelay):
				}
			}

			// Run immediately on startup
			syncMetadataForUsers()

			ticker := time.NewTicker(metadataInterval)
			defer ticker.Stop()
			// Governing: SPEC graceful-shutdown REQ-CTX-001 (select on ctx.Done vs ticker.C, log shutdown)
			// Governing: SPEC metadata-enrichment-pipeline REQ-ENRICH-041 (syncMetadataForUsers
			// fans out per-user work without blocking this loop; the shared InFlightGuard
			// skips users whose previous enrichment run is still active)
			for {
				select {
				case <-ctx.Done():
					logger.Info("loop shutting down", "loop", "metadata")
					return
				case <-ticker.C:
					syncMetadataForUsers()
				}
			}
		}()
	} else {
		logger.Info("metadata enrichment disabled")
	}

	// Governing: ADR-0019 (structured metrics), SPEC observability REQ "BG-001", REQ "BG-002"
	// Background Playlist Sync Loop (for syncing playlists to Navidrome)
	playlistSyncInterval, err := time.ParseDuration(cfg.PlaylistSync.SyncInterval)
	if err != nil {
		logger.Error("invalid playlist sync interval, using default 1h", "error", err, "value", cfg.PlaylistSync.SyncInterval)
		playlistSyncInterval = 1 * time.Hour
	}
	playlistSyncInitialDelay := cfg.PlaylistSyncInitialDelay()
	logger.Info("playlist sync configured",
		"interval", playlistSyncInterval,
		"initial_delay", playlistSyncInitialDelay)

	wg.Add(1)
	go func() {
		defer wg.Done()
		// Initial delay to let the app start up (playlist_sync.initial_delay;
		// zero skips the delay entirely)
		if playlistSyncInitialDelay > 0 {
			select {
			case <-ctx.Done():
				// Governing: SPEC graceful-shutdown REQ-CTX-001 (log loop shutdown)
				logger.Info("loop shutting down", "loop", "playlist_sync")
				return
			case <-time.After(playlistSyncInitialDelay):
			}
		}

		ticker := time.NewTicker(playlistSyncInterval)
		defer ticker.Stop()
		// Governing: SPEC graceful-shutdown REQ-CTX-001 (select on ctx.Done vs ticker.C, log shutdown)
		for {
			select {
			case <-ctx.Done():
				logger.Info("loop shutting down", "loop", "playlist_sync")
				return
			case <-ticker.C:
				// Governing: SPEC metadata-enrichment-pipeline REQ-ENRICH-041,
				// SPEC observability REQ "BG-001"
				runPerUserTick("playlist_sync", func(user *ent.User) error {
					return playlistSyncSvc.SyncAllEnabledPlaylists(ctx, user.ID)
				})
			}
		}
	}()

	// Governing: SPEC-0017 REQ "Background Submitter Goroutine", ADR-0029, ADR-0013
	// Background Lidarr Queue Submitter (only if Lidarr is configured)
	if cfg.Lidarr.BaseURL != "" && cfg.Lidarr.APIKey != "" {
		lidarrSubmitter := services.NewLidarrSubmitter(client, cfg, logger)
		wg.Add(1)
		go func() {
			defer wg.Done()
			lidarrSubmitter.Run(ctx)
		}()
		logger.Info("lidarr queue submitter enabled",
			"submit_interval", cfg.Lidarr.SubmitInterval,
			"queue_max", cfg.Lidarr.QueueMax)
	} else {
		logger.Info("lidarr queue submitter disabled (lidarr not configured)")
	}

	// Router setup
	r := chi.NewRouter()

	// Middleware
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	// Governing: SPEC user-authentication REQ "Security Headers and Server Timeouts"
	r.Use(internalMiddleware.SecurityHeaders)
	r.Use(internalMiddleware.Logger(logger))
	r.Use(middleware.Recoverer)
	// Governing: SPEC event-bus-sse REQ-SSE-004 — the request timeout is applied
	// per-group below instead of globally so the long-lived SSE stream at
	// /events is not killed every 60s by middleware.Timeout.
	requestTimeout := middleware.Timeout(60 * time.Second)

	// Static Files
	fileServer := http.FileServer(http.Dir("./static"))
	r.With(requestTimeout).Handle("/static/*", http.StripPrefix("/static", fileServer))

	// Governing: issue #155 — rate limiting on login endpoint
	// Configure per-IP rate limiter for auth endpoints
	authRateLimit := cfg.Security.AuthRateLimit
	if authRateLimit <= 0 {
		authRateLimit = 10
	}
	loginLimiter := internalMiddleware.NewIPRateLimiter(rate.Every(time.Minute/time.Duration(authRateLimit)), authRateLimit)

	// Public Routes
	r.Group(func(r chi.Router) {
		r.Use(requestTimeout)
		r.Get("/auth/login", h.Login)
		// Apply rate limiting only to POST /login
		r.With(internalMiddleware.RateLimit(loginLimiter, logger)).Post("/login", h.PostLogin)
		// Governing: ADR-0028 — logout is state-changing, so it is POST-only;
		// SameSite=Lax does not protect top-level GET navigations from CSRF.
		r.Post("/logout", h.Logout)
		r.Post("/auth/logout", h.Logout) // Alias for consistency

		// OAuth callbacks must be public (no session required)
		// They will validate session internally
		r.Get("/auth/spotify/callback", h.SpotifyCallback)
		r.Get("/auth/lastfm/callback", h.LastFMCallback)
	})

	// SSE stream route. Governing: SPEC event-bus-sse REQ-SSE-004 — mounted in
	// its own auth-protected group WITHOUT the request timeout middleware so the
	// stream is not force-closed every 60s (which also dropped any events
	// published during the reconnect gap). All other middleware still applies.
	r.Group(func(r chi.Router) {
		r.Use(internalMiddleware.AuthMiddleware(client, jwtManager, logger))
		r.Get("/events", h.Events)
	})

	// Protected Routes
	r.Group(func(r chi.Router) {
		r.Use(requestTimeout)
		r.Use(internalMiddleware.AuthMiddleware(client, jwtManager, logger))

		// Governing: issue #155 — /data/* moved behind auth to prevent unauthenticated access
		dataFileServer := http.FileServer(http.Dir("./data"))
		r.Handle("/data/*", http.StripPrefix("/data", dataFileServer))

		r.Get("/", h.Home)

		r.Get("/preferences", h.PreferencesRedirect)
		r.Get("/preferences/account", h.PreferencesAccount)
		r.Post("/preferences/account/email", h.PostPreferencesEmail)
		r.Post("/preferences/notifications/test", h.PostTestNotification)
		r.Get("/preferences/appearance", h.PreferencesAppearance)
		r.Post("/preferences/appearance", h.PostPreferencesAppearance)
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
		// Governing: SPEC music-provider-integration REQ "ListenBrainz Provider" (REQ-PROV-046)
		r.Post("/preferences/listenbrainz/sync", h.SyncListenBrainz)
		r.Post("/preferences/listenbrainz/rebuild", h.RebuildListenBrainz)
		r.Post("/preferences/listenbrainz/disconnect", h.DisconnectListenBrainz)
		// Governing: SPEC music-provider-integration REQ "ListenBrainz Listen Submission" (REQ-PROV-054)
		r.Post("/preferences/listenbrainz/submit-listens", h.ToggleListenBrainzSubmitListens)

		// Task routes
		r.Post("/preferences/tasks/sync-listens", h.TaskSyncListens)
		r.Post("/preferences/tasks/sync-playlists", h.TaskSyncPlaylists)
		r.Post("/preferences/tasks/enrich-metadata", h.TaskEnrichMetadata)
		r.Post("/preferences/tasks/sync-artist-images", h.TaskSyncArtistImages)
		r.Post("/preferences/tasks/sync-album-images", h.TaskSyncAlbumImages)
		r.Post("/preferences/tasks/reset", h.TaskResetData)
		r.Post("/preferences/tasks/cleanup", h.TaskCleanup)

		// OAuth login initiators (require existing session)
		r.Get("/auth/spotify/login", h.SpotifyLogin)
		r.Get("/auth/lastfm/login", h.LastFMLogin)

		// ListenBrainz has no OAuth: users paste their token from
		// listenbrainz.org/settings while logged in, so both routes stay
		// behind auth (no public callback needed).
		// Governing: SPEC music-provider-integration REQ "ListenBrainz Provider" (REQ-PROV-046)
		r.Get("/auth/listenbrainz/connect", h.ListenBrainzConnectForm)
		r.Post("/auth/listenbrainz/connect", h.ListenBrainzConnect)

		r.Get("/recent", h.RecentListens)
		r.Get("/playlists", h.Playlists)
		// Governing: ADR-0030, SPEC music-provider-integration REQ-PROV-053 (LB Radio
		// lives under the playlists section; the static segment wins over /playlists/{id})
		r.Get("/playlists/lb-radio", h.ListenBrainzRadioForm)
		r.Post("/playlists/lb-radio", h.ListenBrainzRadioGenerate)
		r.Get("/playlists/{id}", h.PlaylistShow)
		r.Get("/playlists/{id}.png", h.PlaylistImage)
		r.Post("/playlists/{id}/toggle-sync", h.TogglePlaylistSync)
		r.Post("/playlists/{id}/sync", h.SyncPlaylist)
		r.Post("/playlists/{id}/rebuild-sync", h.RebuildPlaylistSync)
		r.Get("/playlists/{id}/sync-status", h.GetPlaylistSyncStatus)
		r.Get("/playlists/{id}/sync-progress", h.GetPlaylistSyncProgress)
		r.Post("/playlists/{id}/debug-sync", h.DebugPlaylistSync)
		r.Post("/playlists/{id}/resolve-navidrome-conflict", h.ResolveNavidromeConflict)
		// Governing: SPEC playlist-sync-navidrome REQ-PLSYNC-071, REQ-PLSYNC-072 (arbitrary-target linking)
		r.Get("/playlists/{id}/link", h.LinkNavidromePicker)
		r.Post("/playlists/{id}/link/{targetId}", h.LinkWithNavidrome)
		r.Get("/playlists/{id}/sync-dropdown", h.GetPlaylistSyncDropdown)
		r.Post("/playlists/{id}/ai/generate-metadata", h.PlaylistGenerateMetadata)
		r.Post("/playlists/{id}/ai/generate-artwork", h.PlaylistGenerateArtwork)
		r.Get("/playlists/{id}/enhance-vibes-modal", h.EnhanceVibesModal)
		r.Post("/playlists/{id}/enhance-vibes", h.EnhanceVibes)

		// Vibes routes (DJs and Mixtapes)
		r.Get("/vibes", h.VibesRedirect)
		r.Route("/vibes/djs", func(r chi.Router) {
			r.Get("/", h.DJsIndex)
			r.Post("/", h.CreateDJ)
			r.Get("/{id}", h.DJShow)
			r.Put("/{id}", h.UpdateDJ)
			r.Delete("/{id}", h.DeleteDJ)
			r.Get("/suggestions/genres", h.GenreSuggestions)
			r.Get("/suggestions/artists", h.ArtistSuggestions)
		})
		r.Route("/vibes/mixtapes", func(r chi.Router) {
			r.Get("/", h.MixtapesIndex)
			r.Get("/{id}", h.MixtapeShow)
			r.Post("/", h.CreateMixtape)
			r.Put("/{id}", h.UpdateMixtape)
			r.Delete("/{id}", h.DeleteMixtape)
			r.Post("/{id}/toggle-sync", h.ToggleMixtapeSync)
			r.Post("/{id}/generate", h.GenerateMixtape)
		})

		// Library routes (artists, albums, tracks)
		r.Route("/library", func(r chi.Router) {
			// Artist routes
			r.Get("/artists", h.ArtistIndex)
			r.Get("/artist/{id}", h.ArtistShow)
			r.Get("/artist/{id}.png", h.ArtistImage)
			r.Get("/artist/{id}/chart", h.ArtistChart)
			r.Post("/artist/{id}/regenerate-ai", h.ArtistRegenerateAI)
			r.Post("/artist/{id}/find-similar", h.ArtistFindSimilar)
			r.Post("/artist/{id}/create-mixtape", h.ArtistCreateMixtape)
			r.Get("/artist/{id}/mixtape-modal", h.ArtistMixtapeModal)

			// Album routes
			r.Get("/albums", h.AlbumIndex)
			r.Get("/album/{id}", h.AlbumShow)
			r.Get("/album/{id}.png", h.AlbumImage)
			r.Get("/album/{id}/chart", h.AlbumChart)
			r.Post("/album/{id}/regenerate-ai", h.AlbumRegenerateAI)
			r.Post("/album/{id}/create-mixtape", h.AlbumCreateMixtape)
			r.Get("/album/{id}/mixtape-modal", h.AlbumMixtapeModal)

			// Track routes
			r.Get("/tracks", h.TrackIndex)
			r.Get("/track/{id}", h.TrackShow)
			r.Get("/track/{id}/chart", h.TrackChart)
			r.Post("/track/{id}/regenerate-ai", h.TrackRegenerateAI)
		})
	})

	addr := fmt.Sprintf("%s:%s", cfg.Server.Host, cfg.Server.Port)

	// Governing: SPEC user-authentication REQ "Security Headers and Server Timeouts"
	// Parse server timeouts from config with sensible defaults to protect against slowloris DoS
	readHeaderTimeout, err := time.ParseDuration(cfg.Server.ReadHeaderTimeout)
	if err != nil {
		logger.Error("invalid server.read_header_timeout, using default 10s", "error", err, "value", cfg.Server.ReadHeaderTimeout)
		readHeaderTimeout = 10 * time.Second
	}
	readTimeout, err := time.ParseDuration(cfg.Server.ReadTimeout)
	if err != nil {
		logger.Error("invalid server.read_timeout, using default 30s", "error", err, "value", cfg.Server.ReadTimeout)
		readTimeout = 30 * time.Second
	}
	writeTimeout, err := time.ParseDuration(cfg.Server.WriteTimeout)
	if err != nil {
		logger.Error("invalid server.write_timeout, using default 60s", "error", err, "value", cfg.Server.WriteTimeout)
		writeTimeout = 60 * time.Second
	}
	idleTimeout, err := time.ParseDuration(cfg.Server.IdleTimeout)
	if err != nil {
		logger.Error("invalid server.idle_timeout, using default 120s", "error", err, "value", cfg.Server.IdleTimeout)
		idleTimeout = 120 * time.Second
	}

	srv := &http.Server{
		Addr:              addr,
		Handler:           r,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}

	// Start the HTTP server in a goroutine
	go func() {
		logger.Info("starting server", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("server failed", "error", err)
			os.Exit(1)
		}
	}()

	// Wait for shutdown signal
	<-ctx.Done()
	logger.Info("shutdown initiated, waiting for background jobs to finish")

	// Governing: SPEC graceful-shutdown REQ-SIG-004 (second signal -> hard exit)
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		if _, ok := <-sigCh; ok {
			logger.Error("second signal received, forcing exit")
			os.Exit(1)
		}
	}()

	// Governing: SPEC graceful-shutdown REQ-TMO-002 (hard exit timer)
	// Governing: SPEC graceful-shutdown REQ-TMO-004 (non-zero exit code on timeout)
	timer := time.AfterFunc(shutdownTimeout, func() {
		logger.Error("shutdown timeout exceeded, forcing exit")
		os.Exit(1)
	})
	defer timer.Stop()

	// Governing: SPEC graceful-shutdown REQ-CTX-004 (HTTP server uses Shutdown(ctx) for graceful drain)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("server shutdown error", "error", err)
	}

	// Governing: SPEC graceful-shutdown REQ-WG-003 (wait for all per-user goroutines to drain)
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	// Governing: SPEC graceful-shutdown REQ-WG-004 (wg.Wait combined with shutdown timeout to avoid indefinite blocking)
	select {
	case <-done:
		// Governing: SPEC graceful-shutdown REQ-TMO-005 (clean shutdown exits with code 0)
		logger.Info("all background jobs finished cleanly")
	case <-shutdownCtx.Done():
		logger.Warn("shutdown timeout exceeded")
	}
}
