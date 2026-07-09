// Governing: ADR-0005 (Navidrome auth), ADR-0007 (event bus), SPEC listen-playlist-sync
package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"spotter/ent"
	"spotter/ent/listen"
	"spotter/ent/playlist"
	"spotter/ent/playlisttrack"
	"spotter/ent/syncevent"
	"spotter/ent/user"
	"spotter/internal/config"
	"spotter/internal/events"
	"spotter/internal/providers"
)

// SyncNotifier handles email notifications for sync failures with cooldown.
// Governing: SPEC-0015 REQ "Notification Trigger", REQ "Cooldown Reset on Recovery", ADR-0026
type SyncNotifier interface {
	NotifyIfNeeded(ctx context.Context, u *ent.User, provider string, syncErr error) error
	ClearCooldown(ctx context.Context, userID int, provider string) error
	// Governing: SPEC-0015 REQ "Preferences UI — Email Address and Notification Status"
	SendTest(ctx context.Context, u *ent.User) error
}

// Governing: ADR-0020 (exponential backoff and circuit breaker), ADR-0007 (event bus for notifications), SPEC error-handling
type Syncer struct {
	client    *ent.Client
	config    *config.Config
	logger    *slog.Logger
	bus       *events.Bus
	factories []providers.Factory
	backoff   *BackoffManager
	notifier  SyncNotifier
}

func NewSyncer(client *ent.Client, cfg *config.Config, logger *slog.Logger, bus *events.Bus, notifier SyncNotifier) *Syncer {
	return &Syncer{
		client:    client,
		config:    cfg,
		logger:    logger,
		bus:       bus,
		factories: []providers.Factory{},
		backoff:   NewBackoffManager(),
		notifier:  notifier,
	}
}

// Governing: ADR-0016 (pluggable provider factory), SPEC listen-playlist-sync REQ-SYNC-001
// Register adds a new provider factory to the syncer.
func (s *Syncer) Register(factory providers.Factory) {
	s.factories = append(s.factories, factory)
}

// ClearProviderBackoff resets the backoff state (including the fatal flag) for
// a user's provider so it is retried on the next sync tick. Handlers call this
// when the user takes corrective action (reconnects or disconnects a provider).
// Governing: SPEC error-handling REQ-STATE-004 (fatal flag cleared only on user action)
func (s *Syncer) ClearProviderBackoff(userID int, providerType providers.Type) {
	s.backoff.ClearFatal(BackoffKey{UserID: userID, ProviderType: providerType})
}

// providerSyncStats accumulates per-provider values across the history and
// playlist phases of a single Sync run for metric.sync emission.
// Governing: ADR-0019 (structured metrics), SPEC observability REQ "BG-003"
type providerSyncStats struct {
	listensSynced   int
	playlistsSynced int
	duration        time.Duration
	failed          bool
	errs            []string
}

// syncStatsFor returns the stats entry for a provider, creating it on first
// use. It returns nil when stats collection is disabled (nil map).
func syncStatsFor(stats map[providers.Type]*providerSyncStats, t providers.Type) *providerSyncStats {
	if stats == nil {
		return nil
	}
	st, ok := stats[t]
	if !ok {
		st = &providerSyncStats{}
		stats[t] = st
	}
	return st
}

// Governing: ADR-0019 (structured metrics), SPEC observability REQ "BG-003"
// Governing: SPEC graceful-shutdown REQ-REC-004 (ctx propagated to DB ops; cancellation leaves DB consistent)
// Governing: SPEC listen-playlist-sync REQ-SYNC-010 (full sync: providers -> history -> playlists)
// Governing: SPEC listen-playlist-sync REQ-SYNC-011 (history failure does not abort playlist sync)
// Sync performs a full synchronization (history and playlists) for the user.
// It attempts both phases regardless of individual failures and returns the
// joined errors so callers can observe partial failure.
// Governing: SPEC observability REQ-BG-001 (callers count real per-user errors)
func (s *Syncer) Sync(ctx context.Context, u *ent.User) error {
	s.logger.Info("starting full sync", "username", u.Username)

	refreshedUser, activeProviders, err := s.getActiveProviders(ctx, u)
	if err != nil {
		return err
	}

	// Governing: ADR-0019 (structured metrics), SPEC observability REQ "BG-003"
	// (per-provider listens, playlists, duration, and success in metric.sync)
	stats := make(map[providers.Type]*providerSyncStats)

	// 1. History
	_, histErr := s.syncHistory(ctx, refreshedUser, activeProviders, stats)
	if histErr != nil {
		s.logger.Error("failed to sync history", "username", refreshedUser.Username, "error", histErr)
	}

	// 2. Playlists
	_, plErr := s.syncPlaylists(ctx, refreshedUser, activeProviders, stats)
	if plErr != nil {
		s.logger.Error("failed to sync playlists", "username", refreshedUser.Username, "error", plErr)
	}

	// Emit metric.sync with the real per-provider values collected above.
	// Providers skipped entirely (e.g. in a backoff window) have no stats
	// entry and emit no event.
	for _, p := range activeProviders {
		st, ok := stats[p.Type()]
		if !ok {
			continue
		}
		s.logger.Info("metric.sync",
			"provider", string(p.Type()),
			"listens_synced", st.listensSynced,
			"playlists_synced", st.playlistsSynced,
			"duration_ms", st.duration.Milliseconds(),
			"success", !st.failed,
			"error", strings.Join(st.errs, "; "))
	}

	s.logger.Info("full sync completed", "username", refreshedUser.Username)
	// Governing: SPEC listen-playlist-sync REQ-SYNC-011 (history failure does not
	// abort playlist sync) — both phases ran above; surface any failures now.
	return errors.Join(histErr, plErr)
}

// SyncProvider performs a full synchronization for a specific provider only.
func (s *Syncer) SyncProvider(ctx context.Context, u *ent.User, providerType providers.Type) error {
	s.logger.Info("starting provider sync", "username", u.Username, "provider", providerType)

	refreshedUser, activeProviders, err := s.getActiveProviders(ctx, u)
	if err != nil {
		return err
	}

	// Filter to only the requested provider
	var targetProviders []providers.Provider
	for _, p := range activeProviders {
		if p.Type() == providerType {
			targetProviders = append(targetProviders, p)
		}
	}

	if len(targetProviders) == 0 {
		s.logger.Warn("provider not found or not active", "provider", providerType)
		return nil
	}

	// 1. History
	if _, err := s.syncHistory(ctx, refreshedUser, targetProviders, nil); err != nil {
		s.logger.Error("failed to sync history", "username", refreshedUser.Username, "provider", providerType, "error", err)
	}

	// 2. Playlists
	if _, err := s.syncPlaylists(ctx, refreshedUser, targetProviders, nil); err != nil {
		s.logger.Error("failed to sync playlists", "username", refreshedUser.Username, "provider", providerType, "error", err)
	}

	s.logger.Info("provider sync completed", "username", refreshedUser.Username, "provider", providerType)
	return nil
}

// SyncRecentListens pulls recent listening history from all registered providers.
func (s *Syncer) SyncRecentListens(ctx context.Context, u *ent.User) error {
	refreshedUser, activeProviders, err := s.getActiveProviders(ctx, u)
	if err != nil {
		return err
	}
	_, err = s.syncHistory(ctx, refreshedUser, activeProviders, nil)
	return err
}

// SyncPlaylists pulls playlists from all registered providers.
func (s *Syncer) SyncPlaylists(ctx context.Context, u *ent.User) error {
	refreshedUser, activeProviders, err := s.getActiveProviders(ctx, u)
	if err != nil {
		return err
	}
	_, err = s.syncPlaylists(ctx, refreshedUser, activeProviders, nil)
	return err
}

// Governing: SPEC listen-playlist-sync REQ-SYNC-002 (nil factories silently skipped), REQ-SYNC-003 (factory errors logged and skipped)
// getActiveProviders returns the refreshed user with all auth edges loaded and a list of active providers.
func (s *Syncer) getActiveProviders(ctx context.Context, u *ent.User) (*ent.User, []providers.Provider, error) {
	// Refresh user to ensure we have all auth edges loaded so factories can check configuration.
	// We need these so the factories can decide if they can create a provider.
	refreshedUser, err := s.client.User.Query().
		Where(user.ID(u.ID)).
		WithSpotifyAuth().
		WithNavidromeAuth().
		WithLastfmAuth().
		Only(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to refresh user data: %w", err)
	}

	var active []providers.Provider
	for _, factory := range s.factories {
		provider, err := factory(ctx, refreshedUser)
		if err != nil {
			s.logger.Error("failed to create provider", "error", err, "username", refreshedUser.Username)
			continue
		}
		if provider != nil {
			active = append(active, provider)
		}
	}
	return refreshedUser, active, nil
}

// logEvent persists a sync event to the database.
func (s *Syncer) logEvent(ctx context.Context, u *ent.User, eventType syncevent.EventType, provider string, message string, metadata map[string]interface{}) {
	builder := s.client.SyncEvent.Create().
		SetUser(u).
		SetEventType(eventType).
		SetProvider(provider).
		SetMessage(message)

	if metadata != nil {
		if metadataJSON, err := json.Marshal(metadata); err == nil {
			builder.SetMetadata(string(metadataJSON))
		}
	}

	if _, err := builder.Save(ctx); err != nil {
		s.logger.Warn("failed to log sync event", "event_type", eventType, "provider", provider, "error", err)
	}
}

// Governing: SPEC listen-playlist-sync REQ-SYNC-020 (since timestamp from last listen)
// Governing: SPEC listen-playlist-sync REQ-SYNC-022 (per-track errors logged, sync continues)
// Governing: SPEC listen-playlist-sync REQ-SYNC-023 (notification published on new listens)
func (s *Syncer) syncHistory(ctx context.Context, u *ent.User, activeProviders []providers.Provider, stats map[providers.Type]*providerSyncStats) (int, error) {
	allAdded := 0
	for _, provider := range activeProviders {
		// Check if provider supports history fetching
		fetcher, ok := provider.(providers.HistoryFetcher)
		if !ok {
			continue
		}

		providerName := string(provider.Type())

		// Check backoff state before calling provider
		// Governing: SPEC error-handling REQ-BACK-004, REQ-STATE-004
		backoffKey := BackoffKey{UserID: u.ID, ProviderType: provider.Type()}
		if skip, reason := s.backoff.ShouldSkip(backoffKey); skip {
			s.logger.Info("skipping provider due to backoff", "provider", providerName, "reason", reason)
			continue
		}

		// Governing: ADR-0019 (structured metrics), SPEC observability REQ "BG-003"
		providerStart := time.Now()

		// Send sync starting notification
		// Governing: SPEC event-bus-sse REQ-BUS-012 (convenience Publish* methods)
		s.bus.PublishNotification(u.ID, "Syncing Listens",
			fmt.Sprintf("Fetching recent listens from %s...", providerName), "info")

		// Log sync started event
		s.logEvent(ctx, u, syncevent.EventTypeSyncStarted, providerName, fmt.Sprintf("Started syncing listens from %s", providerName), nil)

		// Governing: SPEC graceful-shutdown REQ-REC-001, REQ-REC-002 (idempotent listen sync via timestamp watermark)
		// Determine the last sync time for this provider/source to optimize fetching.
		// We query the latest listen for this specific user and source.
		lastListen, err := s.client.Listen.Query().
			Where(
				listen.HasUserWith(user.ID(u.ID)),
				listen.Source(string(provider.Type())),
			).
			Order(ent.Desc(listen.FieldPlayedAt)).
			First(ctx)
		if err != nil && !ent.IsNotFound(err) {
			s.logger.Warn("failed to query last listen", "provider", provider.Type(), "error", err)
		}

		var since time.Time
		if lastListen != nil {
			since = lastListen.PlayedAt
			s.logger.Debug("found last listen", "provider", provider.Type(), "played_at", since)
		} else {
			// Governing: SPEC listen-playlist-sync REQ-SYNC-020 (bounded lookback when no history exists)
			lookback := s.config.HistoryLookbackDuration()
			since = time.Now().Add(-lookback)
			s.logger.Debug("no previous history found, using configured history lookback",
				"provider", provider.Type(), "lookback", lookback, "since", since)
		}

		s.logger.Debug("fetching history", "provider", provider.Type(), "since", since)

		var totalAdded, totalSkipped, totalFound int

		err = fetcher.GetRecentListens(ctx, since, func(tracks []providers.Track) error {
			if len(tracks) == 0 {
				return nil
			}
			totalFound += len(tracks)
			s.logger.Info("found new tracks batch", "count", len(tracks), "provider", provider.Type())

			count, skipped, err := s.persistListens(ctx, u, provider.Type(), tracks)
			if err != nil {
				s.logger.Error("failed to persist listens batch", "error", err)
				return err
			}
			totalAdded += count
			totalSkipped += skipped
			return nil
		})

		if err != nil {
			// Governing: SPEC observability REQ "BG-003" (per-provider failure recorded)
			if st := syncStatsFor(stats, provider.Type()); st != nil {
				st.duration += time.Since(providerStart)
				st.failed = true
				st.errs = append(st.errs, err.Error())
			}
			// Classify error and record backoff state
			// Governing: SPEC error-handling REQ-ERR-001 through REQ-ERR-004
			errClass := ClassifyError(err)
			s.backoff.RecordFailure(backoffKey, err, errClass)
			s.logger.Error("failed to fetch/persist recent listens",
				"provider", provider.Type(),
				"username", u.Username,
				"error", err,
				"error_class", errClass.String(),
			)
			// Publish fatal error notification (retriable errors do not trigger notifications)
			// Governing: SPEC error-handling REQ-NOTIFY-001, REQ-NOTIFY-002, REQ-NOTIFY-003
			if errClass == ErrorClassFatal {
				s.publishFatalNotification(u.ID, backoffKey, providerName, err)
				// Governing: SPEC-0015 REQ "Notification Trigger"
				if s.notifier != nil {
					if notifyErr := s.notifier.NotifyIfNeeded(ctx, u, providerName, err); notifyErr != nil {
						s.logger.Error("failed to send email notification", "error", notifyErr, "provider", providerName)
					}
				}
			}
			// Log sync failed event
			s.logEvent(ctx, u, syncevent.EventTypeSyncFailed, providerName, fmt.Sprintf("Failed to fetch listens from %s: %v", providerName, err), nil)
			continue
		}

		// Record success to reset backoff state
		// Governing: SPEC error-handling REQ-RECOVER-001, REQ-RECOVER-002
		s.backoff.RecordSuccess(backoffKey)
		// Governing: SPEC-0015 REQ "Cooldown Reset on Recovery"
		if s.notifier != nil {
			if notifyErr := s.notifier.ClearCooldown(ctx, u.ID, providerName); notifyErr != nil {
				s.logger.Error("failed to clear notification cooldown", "error", notifyErr, "provider", providerName)
			}
		}

		// Governing: SPEC observability REQ "BG-003" (per-provider listens and duration)
		if st := syncStatsFor(stats, provider.Type()); st != nil {
			st.listensSynced += totalAdded
			st.duration += time.Since(providerStart)
		}

		allAdded += totalAdded
		if totalAdded > 0 {
			// Governing: SPEC event-bus-sse REQ-BUS-012 (convenience Publish* methods)
			s.bus.PublishNotification(u.ID, "New Listens Synced",
				fmt.Sprintf("Imported %d tracks from %s", totalAdded, provider.Type()), "success")
		}

		if totalFound > 0 {
			// Log sync completed event
			s.logEvent(ctx, u, syncevent.EventTypeSyncCompleted, providerName,
				fmt.Sprintf("Completed syncing listens from %s: %d added, %d skipped", providerName, totalAdded, totalSkipped),
				map[string]interface{}{"added": totalAdded, "skipped": totalSkipped, "total": totalFound})
		} else {
			s.logger.Debug("no new tracks found", "provider", provider.Type())
			// Log sync completed with no new tracks
			s.logEvent(ctx, u, syncevent.EventTypeSyncCompleted, providerName,
				fmt.Sprintf("Completed syncing listens from %s: no new tracks", providerName), nil)
		}

		// Update last_synced_at after sync attempt
		if err := s.updateLastSyncedAt(ctx, u, provider.Type()); err != nil {
			s.logger.Warn("failed to update last_synced_at", "provider", provider.Type(), "error", err)
		}
	}
	return allAdded, nil
}

// Governing: SPEC listen-playlist-sync REQ-SYNC-030 (fetch playlists from each PlaylistManager provider)
func (s *Syncer) syncPlaylists(ctx context.Context, u *ent.User, activeProviders []providers.Provider, stats map[providers.Type]*providerSyncStats) (int, error) {
	allAdded := 0
	for _, provider := range activeProviders {
		manager, ok := provider.(providers.PlaylistManager)
		if !ok {
			continue
		}

		providerName := string(provider.Type())

		// Check backoff state before calling provider
		// Governing: SPEC error-handling REQ-BACK-004, REQ-STATE-004
		backoffKey := BackoffKey{UserID: u.ID, ProviderType: provider.Type()}
		if skip, reason := s.backoff.ShouldSkip(backoffKey); skip {
			s.logger.Info("skipping provider due to backoff", "provider", providerName, "reason", reason)
			continue
		}

		// Governing: ADR-0019 (structured metrics), SPEC observability REQ "BG-003"
		providerStart := time.Now()

		// Send sync starting notification
		// Governing: SPEC event-bus-sse REQ-BUS-012 (convenience Publish* methods)
		s.bus.PublishNotification(u.ID, "Syncing Playlists",
			fmt.Sprintf("Fetching playlists from %s...", providerName), "info")

		// Log sync started event
		s.logEvent(ctx, u, syncevent.EventTypeSyncStarted, providerName, fmt.Sprintf("Started syncing playlists from %s", providerName), nil)

		s.logger.Info("syncing playlists", "provider", provider.Type(), "username", u.Username)
		playlists, err := manager.GetPlaylists(ctx)
		if err != nil {
			// Governing: SPEC observability REQ "BG-003" (per-provider failure recorded)
			if st := syncStatsFor(stats, provider.Type()); st != nil {
				st.duration += time.Since(providerStart)
				st.failed = true
				st.errs = append(st.errs, err.Error())
			}
			// Classify error and record backoff state
			// Governing: SPEC error-handling REQ-ERR-001 through REQ-ERR-004
			errClass := ClassifyError(err)
			s.backoff.RecordFailure(backoffKey, err, errClass)
			s.logger.Error("failed to get playlists",
				"provider", provider.Type(),
				"username", u.Username,
				"error", err,
				"error_class", errClass.String(),
			)
			// Publish fatal error notification (retriable errors do not trigger notifications)
			// Governing: SPEC error-handling REQ-NOTIFY-001, REQ-NOTIFY-002, REQ-NOTIFY-003
			if errClass == ErrorClassFatal {
				s.publishFatalNotification(u.ID, backoffKey, providerName, err)
				// Governing: SPEC-0015 REQ "Notification Trigger"
				if s.notifier != nil {
					if notifyErr := s.notifier.NotifyIfNeeded(ctx, u, providerName, err); notifyErr != nil {
						s.logger.Error("failed to send email notification", "error", notifyErr, "provider", providerName)
					}
				}
			}
			// Log sync failed event
			s.logEvent(ctx, u, syncevent.EventTypeSyncFailed, providerName, fmt.Sprintf("Failed to fetch playlists from %s: %v", providerName, err), nil)
			continue
		}

		// Record success to reset backoff state
		// Governing: SPEC error-handling REQ-RECOVER-001, REQ-RECOVER-002
		s.backoff.RecordSuccess(backoffKey)
		// Governing: SPEC-0015 REQ "Cooldown Reset on Recovery"
		if s.notifier != nil {
			if notifyErr := s.notifier.ClearCooldown(ctx, u.ID, providerName); notifyErr != nil {
				s.logger.Error("failed to clear notification cooldown", "error", notifyErr, "provider", providerName)
			}
		}
		s.logger.Info("fetched playlists", "provider", provider.Type(), "count", len(playlists))

		playlistsAdded := 0
		if len(playlists) > 0 {
			added, skipped, err := s.persistPlaylists(ctx, u, provider.Type(), playlists)
			if err != nil {
				s.logger.Error("failed to persist playlists", "error", err)
			}
			allAdded += added
			playlistsAdded = added

			if added > 0 {
				// Governing: SPEC event-bus-sse REQ-BUS-012 (convenience Publish* methods)
				s.bus.PublishNotification(u.ID, "Playlists Synced",
					fmt.Sprintf("Imported %d playlists from %s", added, provider.Type()), "success")
			}

			// Log sync completed event
			s.logEvent(ctx, u, syncevent.EventTypeSyncCompleted, providerName,
				fmt.Sprintf("Completed syncing playlists from %s: %d added, %d updated", providerName, added, skipped),
				map[string]interface{}{"added": added, "updated": skipped, "total": len(playlists)})
		} else {
			// Log sync completed with no playlists
			s.logEvent(ctx, u, syncevent.EventTypeSyncCompleted, providerName,
				fmt.Sprintf("Completed syncing playlists from %s: no playlists found", providerName), nil)
		}

		// Governing: SPEC listen-playlist-sync REQ-SYNC-032 (deactivate playlists no longer returned by the provider)
		s.reconcileInactivePlaylists(ctx, u, provider.Type(), playlists)

		// Update last_synced_at after sync attempt
		if err := s.updateLastSyncedAt(ctx, u, provider.Type()); err != nil {
			s.logger.Warn("failed to update last_synced_at", "provider", provider.Type(), "error", err)
		}

		// Governing: SPEC observability REQ "BG-003" (per-provider playlists and duration)
		if st := syncStatsFor(stats, provider.Type()); st != nil {
			st.playlistsSynced += playlistsAdded
			st.duration += time.Since(providerStart)
		}
	}
	return allAdded, nil
}

// publishFatalNotification publishes a user-visible notification for fatal provider errors.
// It only publishes once per fatal error occurrence. Only called for fatal errors (not retriable).
// Governing: SPEC error-handling REQ-NOTIFY-001, REQ-NOTIFY-002, REQ-NOTIFY-003
func (s *Syncer) publishFatalNotification(userID int, key BackoffKey, providerName string, err error) {
	state, ok := s.backoff.GetState(key)
	if !ok || state.NotifiedFatal {
		return
	}

	title := fmt.Sprintf("%s Connection Failed", providerName)
	message := fmt.Sprintf("Error from %s: %v. Please check your connection in Preferences.", providerName, err)

	// Governing: SPEC event-bus-sse REQ-BUS-012 (convenience Publish* methods)
	s.bus.PublishNotification(userID, title, message, "error")

	s.backoff.MarkNotified(key)
}

// Governing: SPEC listen-playlist-sync REQ-SYNC-021 (upsert Listen with dedup by provider+track+played_at)
func (s *Syncer) persistListens(ctx context.Context, u *ent.User, source providers.Type, tracks []providers.Track) (int, int, error) {
	savedCount := 0
	skippedCount := 0
	providerName := string(source)

	for _, track := range tracks {
		// Basic validation
		if track.Name == "" || track.Artist == "" {
			skippedCount++
			s.logEvent(ctx, u, syncevent.EventTypeTrackSkipped, providerName,
				"Skipped track with missing name or artist",
				map[string]interface{}{"reason": "missing_name_or_artist"})
			continue
		}

		// Cross-provider de-duplication: check if a similar listen exists within a time window
		// This prevents duplicate entries when the same song is reported by multiple providers
		if s.isDuplicateListen(ctx, u, track) {
			s.logger.Debug("skipping duplicate listen (cross-provider)", "track", track.Name, "artist", track.Artist, "played_at", track.PlayedAt)
			skippedCount++
			s.logEvent(ctx, u, syncevent.EventTypeTrackSkipped, providerName,
				fmt.Sprintf("Skipped duplicate: %s by %s", track.Name, track.Artist),
				map[string]interface{}{"track": track.Name, "artist": track.Artist, "reason": "cross_provider_duplicate"})
			continue
		}

		// Governing: SPEC graceful-shutdown REQ-REC-001 (idempotent sync), REQ-REC-003 (existence check before insert)
		// Governing: SPEC listen-playlist-sync REQ-SYNC-021 (dedup by provider+provider_track_id+played_at
		// when the provider supplies a track ID; fall back to provider+track_name+artist_name+played_at)
		// Check if it exists to avoid unique constraint violations.
		dedupQuery := s.client.Listen.Query().
			Where(
				listen.HasUserWith(user.ID(u.ID)),
				listen.Source(string(source)),
				listen.PlayedAt(track.PlayedAt),
			)
		if track.ID != "" {
			dedupQuery = dedupQuery.Where(listen.ProviderTrackID(track.ID))
		} else {
			dedupQuery = dedupQuery.Where(
				listen.TrackName(track.Name),
				listen.ArtistName(track.Artist),
			)
		}
		exists, err := dedupQuery.Exist(ctx)

		if err != nil {
			s.logger.Warn("failed to check existence of listen", "error", err)
			continue
		}

		if exists {
			s.logger.Debug("skipping duplicate listen", "track", track.Name, "artist", track.Artist, "played_at", track.PlayedAt)
			skippedCount++
			s.logEvent(ctx, u, syncevent.EventTypeTrackSkipped, providerName,
				fmt.Sprintf("Skipped existing: %s by %s", track.Name, track.Artist),
				map[string]interface{}{"track": track.Name, "artist": track.Artist, "reason": "already_exists"})
			continue
		}

		builder := s.client.Listen.Create().
			SetUser(u).
			SetTrackName(track.Name).
			SetArtistName(track.Artist).
			SetAlbumName(track.Album).
			SetSource(string(source)).
			SetPlayedAt(track.PlayedAt)

		if track.URL != "" {
			builder.SetURL(track.URL)
		}
		// Governing: SPEC listen-playlist-sync REQ-SYNC-021 (store provider track ID for de-duplication)
		if track.ID != "" {
			builder.SetProviderTrackID(track.ID)
		}

		l, err := builder.Save(ctx)
		if err != nil {
			s.logger.Warn("failed to save listen",
				"track", track.Name,
				"provider", source,
				"error", err,
			)
		} else {
			savedCount++
			s.logger.Debug("saved listen", "track", track.Name, "artist", track.Artist, "provider", source)

			// Log track added event
			s.logEvent(ctx, u, syncevent.EventTypeTrackAdded, providerName,
				fmt.Sprintf("Added: %s by %s", track.Name, track.Artist),
				map[string]interface{}{"track": track.Name, "artist": track.Artist, "album": track.Album})

			// Governing: SPEC event-bus-sse REQ-BUS-011 (recent-listen carries RecentListenPayload),
			// REQ-BUS-012 (convenience Publish* methods)
			s.bus.PublishRecentListen(u.ID, events.RecentListenPayload{
				ListenID:   l.ID,
				TrackName:  l.TrackName,
				ArtistName: l.ArtistName,
				AlbumName:  l.AlbumName,
				Source:     l.Source,
				PlayedAt:   l.PlayedAt,
				URL:        l.URL,
			})
		}
	}
	return savedCount, skippedCount, nil
}

// isDuplicateListen checks if a similar listen already exists across all providers.
// It uses a time window to account for slight timing differences between providers.
func (s *Syncer) isDuplicateListen(ctx context.Context, u *ent.User, track providers.Track) bool {
	// Use a 2-minute window to catch duplicates from different providers
	// that might have slightly different timestamps for the same play
	timeWindow := 2 * time.Minute
	startTime := track.PlayedAt.Add(-timeWindow)
	endTime := track.PlayedAt.Add(timeWindow)

	exists, err := s.client.Listen.Query().
		Where(
			listen.HasUserWith(user.ID(u.ID)),
			listen.TrackName(track.Name),
			listen.ArtistName(track.Artist),
			listen.PlayedAtGTE(startTime),
			listen.PlayedAtLTE(endTime),
		).
		Exist(ctx)

	if err != nil {
		s.logger.Warn("failed to check for duplicate listen", "error", err)
		return false
	}

	return exists
}

// Governing: SPEC listen-playlist-sync REQ-SYNC-012 (sync cursor updated after each provider sync)
func (s *Syncer) updateLastSyncedAt(ctx context.Context, u *ent.User, providerType providers.Type) error {
	now := time.Now()
	switch providerType {
	case providers.TypeNavidrome:
		if u.Edges.NavidromeAuth != nil {
			return s.client.NavidromeAuth.UpdateOneID(u.Edges.NavidromeAuth.ID).SetLastSyncedAt(now).Exec(ctx)
		}
	case providers.TypeSpotify:
		if u.Edges.SpotifyAuth != nil {
			return s.client.SpotifyAuth.UpdateOneID(u.Edges.SpotifyAuth.ID).SetLastSyncedAt(now).Exec(ctx)
		}
	case providers.TypeLastFM:
		if u.Edges.LastfmAuth != nil {
			return s.client.LastFMAuth.UpdateOneID(u.Edges.LastfmAuth.ID).SetLastSyncedAt(now).Exec(ctx)
		}
	}
	return nil
}

// Governing: SPEC listen-playlist-sync REQ-SYNC-031 (upsert Playlist by source+remoteID)
func (s *Syncer) persistPlaylists(ctx context.Context, u *ent.User, source providers.Type, playlists []providers.Playlist) (int, int, error) {
	addedCount := 0
	updatedCount := 0
	skippedCount := 0
	providerName := string(source)

	// If importing from Navidrome, get all playlist IDs that are managed by Spotter
	// (i.e., playlists synced from other sources to Navidrome)
	spotterManagedNavidromeIDs := make(map[string]bool)
	if source == providers.TypeNavidrome {
		managedPlaylists, err := s.client.Playlist.Query().
			Where(
				playlist.HasUserWith(user.ID(u.ID)),
				playlist.NavidromePlaylistIDNEQ(""),
				playlist.SyncToNavidrome(true),
			).
			All(ctx)
		if err != nil {
			s.logger.Warn("failed to query Spotter-managed playlists", "error", err)
		} else {
			for _, mp := range managedPlaylists {
				spotterManagedNavidromeIDs[mp.NavidromePlaylistID] = true
			}
			if len(spotterManagedNavidromeIDs) > 0 {
				s.logger.Debug("found Spotter-managed Navidrome playlists to exclude",
					"count", len(spotterManagedNavidromeIDs))
			}
		}
	}

	for _, pl := range playlists {
		if pl.Name == "" {
			s.logEvent(ctx, u, syncevent.EventTypePlaylistSkipped, providerName,
				"Skipped playlist with empty name",
				map[string]interface{}{"playlist_id": pl.ID, "reason": "empty_name"})
			continue
		}

		// Skip Navidrome playlists that are managed by Spotter (synced from other sources)
		if source == providers.TypeNavidrome && spotterManagedNavidromeIDs[pl.ID] {
			s.logger.Debug("skipping Spotter-managed Navidrome playlist",
				"playlist_id", pl.ID, "name", pl.Name)
			s.logEvent(ctx, u, syncevent.EventTypePlaylistSkipped, providerName,
				fmt.Sprintf("Skipped Spotter-managed playlist: %s", pl.Name),
				map[string]interface{}{"playlist_id": pl.ID, "reason": "spotter_managed"})
			skippedCount++
			continue
		}

		// Governing: SPEC graceful-shutdown REQ-REC-001 (idempotent sync), REQ-REC-003 (existence check before insert)
		// Check if playlist exists
		existingPlaylist, err := s.client.Playlist.Query().
			Where(
				playlist.HasUserWith(user.ID(u.ID)),
				playlist.Source(string(source)),
				playlist.RemoteID(pl.ID),
			).
			Only(ctx)

		if err != nil && !ent.IsNotFound(err) {
			s.logger.Warn("failed to check playlist existence", "error", err)
			continue
		}

		var playlistID int
		if existingPlaylist != nil {
			// Update existing playlist
			// Governing: SPEC listen-playlist-sync REQ-SYNC-032 (reactivate playlists that reappear at the provider)
			_, err := s.client.Playlist.UpdateOne(existingPlaylist).
				SetName(pl.Name).
				SetDescription(pl.Description).
				SetImageURL(pl.ImageURL).
				SetExternalURL(pl.ExternalURL).
				SetTrackCount(pl.TrackCount).
				SetUniqueArtists(pl.UniqueArtists).
				SetUniqueAlbums(pl.UniqueAlbums).
				SetIsActive(true).
				Save(ctx)
			if err != nil {
				s.logger.Warn("failed to update playlist", "name", pl.Name, "error", err)
				continue
			}
			s.logger.Debug("updated playlist", "name", pl.Name, "source", source)
			playlistID = existingPlaylist.ID
			updatedCount++
		} else {
			// Create new playlist
			newPlaylist, err := s.client.Playlist.Create().
				SetUser(u).
				SetRemoteID(pl.ID).
				SetName(pl.Name).
				SetDescription(pl.Description).
				SetImageURL(pl.ImageURL).
				SetExternalURL(pl.ExternalURL).
				SetTrackCount(pl.TrackCount).
				SetUniqueArtists(pl.UniqueArtists).
				SetUniqueAlbums(pl.UniqueAlbums).
				SetSource(string(source)).
				Save(ctx)
			if err != nil {
				s.logger.Warn("failed to create playlist", "name", pl.Name, "error", err)
				continue
			}
			s.logger.Debug("created playlist", "name", pl.Name, "source", source)
			playlistID = newPlaylist.ID
			addedCount++

			// Log playlist added event
			s.logEvent(ctx, u, syncevent.EventTypePlaylistAdded, providerName,
				fmt.Sprintf("Added playlist: %s", pl.Name),
				map[string]interface{}{"playlist_name": pl.Name, "playlist_id": pl.ID})
		}

		// Persist playlist tracks
		if len(pl.Tracks) > 0 {
			if err := s.persistPlaylistTracks(ctx, playlistID, pl.Tracks); err != nil {
				s.logger.Warn("failed to persist playlist tracks", "playlist", pl.Name, "error", err)
			}
		}
	}
	if skippedCount > 0 {
		s.logger.Info("skipped Spotter-managed playlists during import",
			"provider", providerName, "skipped", skippedCount)
	}
	return addedCount, updatedCount, nil
}

// Governing: SPEC listen-playlist-sync REQ-SYNC-032 (playlists no longer returned by a provider are deactivated)
// reconcileInactivePlaylists marks local playlists for this user/source that were not
// present in the provider's latest response as inactive. Playlists that reappear are
// reactivated by the upsert path in persistPlaylists. Spotter-managed Navidrome
// playlists (created via pairing) are still returned by the provider, so their remote
// IDs remain in the fetched set and they are never deactivated here.
func (s *Syncer) reconcileInactivePlaylists(ctx context.Context, u *ent.User, source providers.Type, fetched []providers.Playlist) {
	remoteIDs := make([]string, 0, len(fetched))
	for _, pl := range fetched {
		if pl.ID != "" {
			remoteIDs = append(remoteIDs, pl.ID)
		}
	}

	update := s.client.Playlist.Update().
		Where(
			playlist.HasUserWith(user.ID(u.ID)),
			playlist.Source(string(source)),
			playlist.IsActive(true),
		)
	if len(remoteIDs) > 0 {
		update = update.Where(playlist.RemoteIDNotIn(remoteIDs...))
	}

	deactivated, err := update.SetIsActive(false).Save(ctx)
	if err != nil {
		s.logger.Warn("failed to deactivate playlists missing from provider",
			"provider", source, "error", err)
		return
	}
	if deactivated > 0 {
		s.logger.Info("deactivated playlists no longer returned by provider",
			"provider", source, "count", deactivated)
		s.logEvent(ctx, u, syncevent.EventTypePlaylistSkipped, string(source),
			fmt.Sprintf("Deactivated %d playlists no longer present on %s", deactivated, source),
			map[string]interface{}{"deactivated": deactivated, "reason": "missing_from_provider"})
	}
}

// Governing: SPEC listen-playlist-sync REQ-SYNC-031 (upsert PlaylistTrack with position), REQ-SYNC-032 (removed tracks deleted)
// persistPlaylistTracks saves tracks for a playlist, upserting to preserve catalog links
func (s *Syncer) persistPlaylistTracks(ctx context.Context, playlistID int, tracks []providers.Track) error {
	// Get the playlist to access user and provider info
	pl, err := s.client.Playlist.Query().
		WithUser().
		Where(playlist.ID(playlistID)).
		Only(ctx)
	if err != nil {
		return fmt.Errorf("failed to get playlist: %w", err)
	}
	providerName := pl.Source
	userID := pl.Edges.User.ID

	// Get existing playlist tracks with their catalog links
	existingTracks, err := s.client.PlaylistTrack.Query().
		Where(playlisttrack.HasPlaylistWith(playlist.ID(playlistID))).
		WithTrack().
		WithArtist().
		WithAlbum().
		All(ctx)
	if err != nil {
		return fmt.Errorf("failed to get existing playlist tracks: %w", err)
	}

	// Build maps for quick lookup of existing tracks.
	// Use remote_id as primary key, fall back to track_name+artist_name.
	// Each identity maps to ALL rows with that identity (a playlist can legitimately
	// contain the same track at multiple positions), and each row is consumed at most
	// once so duplicate occurrences map to distinct rows instead of shadowing each other.
	existingByRemoteID := make(map[string][]*ent.PlaylistTrack)
	existingByNameArtist := make(map[string][]*ent.PlaylistTrack)
	existingIDs := make(map[int]bool)

	for _, pt := range existingTracks {
		existingIDs[pt.ID] = true
		if pt.RemoteID != "" {
			existingByRemoteID[pt.RemoteID] = append(existingByRemoteID[pt.RemoteID], pt)
		}
		key := pt.TrackName + "|" + pt.ArtistName
		existingByNameArtist[key] = append(existingByNameArtist[key], pt)
	}

	// First, move all existing tracks to negative positions to avoid unique constraint conflicts
	// when updating positions
	for i, pt := range existingTracks {
		if err := s.client.PlaylistTrack.UpdateOneID(pt.ID).SetPosition(-(i + 1)).Exec(ctx); err != nil {
			s.logger.Warn("failed to temporarily reposition playlist track", "error", err, "id", pt.ID)
		}
	}

	// Track which existing tracks we've seen (to delete removed ones).
	// A row in seenIDs has been consumed by an incoming occurrence and cannot be
	// matched again, so the same remote track at two positions occupies two rows.
	seenIDs := make(map[int]bool)
	addedCount := 0
	updatedCount := 0

	// firstUnconsumed returns the first row with the given identity that has not
	// already been claimed by an earlier occurrence of the same track.
	firstUnconsumed := func(rows []*ent.PlaylistTrack) *ent.PlaylistTrack {
		for _, pt := range rows {
			if !seenIDs[pt.ID] {
				return pt
			}
		}
		return nil
	}

	for i, track := range tracks {
		if track.Name == "" || track.Artist == "" {
			continue
		}

		// Try to find existing track by remote_id first, then by name+artist
		var existing *ent.PlaylistTrack
		if track.ID != "" {
			existing = firstUnconsumed(existingByRemoteID[track.ID])
		}
		if existing == nil {
			key := track.Name + "|" + track.Artist
			existing = firstUnconsumed(existingByNameArtist[key])
		}

		if existing != nil {
			// Update existing track, preserving catalog links
			seenIDs[existing.ID] = true
			update := s.client.PlaylistTrack.UpdateOneID(existing.ID).
				SetTrackName(track.Name).
				SetArtistName(track.Artist).
				SetPosition(i)

			if track.ID != "" {
				update.SetRemoteID(track.ID)
			}
			if track.Album != "" {
				update.SetAlbumName(track.Album)
			}
			if track.DurationMs > 0 {
				update.SetDurationMs(track.DurationMs)
			}
			if track.URL != "" {
				update.SetURL(track.URL)
			}
			// Governing: SPEC music-provider-integration REQ-PROV-022 (ISRC persisted for tier-1 matching, ADR-0014)
			if track.ISRC != "" {
				update.SetIsrc(track.ISRC)
			}

			if err := update.Exec(ctx); err != nil {
				s.logger.Warn("failed to update playlist track", "error", err, "track", track.Name)
				continue
			}
			updatedCount++
		} else {
			// Create new track
			builder := s.client.PlaylistTrack.Create().
				SetPlaylistID(playlistID).
				SetTrackName(track.Name).
				SetArtistName(track.Artist).
				SetPosition(i)

			if track.ID != "" {
				builder.SetRemoteID(track.ID)
			}
			if track.Album != "" {
				builder.SetAlbumName(track.Album)
			}
			if track.DurationMs > 0 {
				builder.SetDurationMs(track.DurationMs)
			}
			if track.URL != "" {
				builder.SetURL(track.URL)
			}
			// Governing: SPEC music-provider-integration REQ-PROV-022 (ISRC persisted for tier-1 matching, ADR-0014)
			if track.ISRC != "" {
				builder.SetIsrc(track.ISRC)
			}

			if _, err := builder.Save(ctx); err != nil {
				s.logger.Warn("failed to create playlist track", "error", err, "track", track.Name)
				continue
			}
			addedCount++
		}
	}

	// Delete tracks that are no longer in the playlist
	deletedCount := 0
	for id := range existingIDs {
		if !seenIDs[id] {
			if err := s.client.PlaylistTrack.DeleteOneID(id).Exec(ctx); err != nil {
				s.logger.Warn("failed to delete removed playlist track", "error", err, "id", id)
				continue
			}
			deletedCount++
		}
	}

	totalCount := addedCount + updatedCount
	if totalCount > 0 || deletedCount > 0 {
		s.logger.Debug("synced playlist tracks",
			"playlist_id", playlistID,
			"added", addedCount,
			"updated", updatedCount,
			"deleted", deletedCount)

		// Log event and send notification
		message := fmt.Sprintf("Synced %d tracks for playlist: %s", totalCount, pl.Name)
		if deletedCount > 0 {
			message = fmt.Sprintf("Synced %d tracks (%d removed) for playlist: %s", totalCount, deletedCount, pl.Name)
		}
		s.logEvent(ctx, pl.Edges.User, syncevent.EventTypeTrackAdded, providerName, message,
			map[string]interface{}{"playlist_name": pl.Name, "track_count": totalCount, "added": addedCount, "updated": updatedCount, "deleted": deletedCount})

		// Governing: SPEC event-bus-sse REQ-BUS-012 (convenience Publish* methods)
		s.bus.PublishNotification(userID, "Playlist Tracks Synced", message, "success")
	}

	return nil
}
