---
status: accepted
date: 2026-02-21
decision-makers: joestump
---

# ADR-0016: Factory-Slice Provider Pattern with Per-User Instantiation over Hardcoded Provider List

## Context and Problem Statement

Spotter integrates with multiple external music services — Navidrome, Spotify, and Last.fm — for listening history sync, playlist management, playlist syncing, and OAuth authentication. Each provider is optional and user-specific: Spotify requires per-user OAuth tokens, Last.fm requires per-user session keys, and Navidrome uses per-user Subsonic credentials. Two different service layers consume providers: the `Syncer` (for history and playlist sync from providers) and the `PlaylistSyncService` (for syncing playlists to Navidrome). How should the application organize provider instantiation so that providers are conditionally active based on per-user credentials, and both service layers can share the same registration mechanism?

<!-- Governing: SPEC music-provider-integration -->

## Decision Drivers

* Three providers today (Navidrome, Spotify, Last.fm), with the possibility of more (e.g., Tidal, Apple Music, Deezer)
* Provider availability is per-user — one user may have Spotify connected while another does not
* Two service layers (`Syncer` and `PlaylistSyncService`) both need access to providers but for different purposes
* Providers implement different capability interfaces (`HistoryFetcher`, `PlaylistManager`, `PlaylistSyncer`, `Authenticator`) — not all providers support all operations
* Adding a new provider should not require modifying service layer code

## Considered Options

* **Factory slice with per-user instantiation** — `Register()` appends `Factory` functions to a slice; `getActiveProviders()` calls each factory with the current user to get active providers
* **Hardcoded provider list** — services directly construct providers in a fixed order with conditional checks
* **Single super-provider** — one provider implementation that delegates to sub-providers internally
* **External plugin registry** — load providers dynamically from configuration or shared libraries

## Decision Outcome

Chosen option: **Factory slice with per-user instantiation**, because it cleanly separates provider registration (at startup) from provider instantiation (at runtime per-user), supports conditional activation based on user credentials via the `Factory` function signature `func(ctx, user) (Provider, error)`, and allows both `Syncer` and `PlaylistSyncService` to use the same `Register()` pattern with independent factory lists. Adding a new provider requires implementing the `Provider` interface (plus capability interfaces), writing a `New()` factory function, and adding `Register()` calls in `main.go`.

### Consequences

* Good, because provider registration is decoupled from provider consumption — `Syncer` and `PlaylistSyncService` only depend on the `providers` package interfaces
* Good, because the `Factory` signature `func(ctx, user) (Provider, error)` returns `nil, nil` when the user lacks credentials, enabling graceful per-user degradation
* Good, because the same factory function (e.g., `navidrome.New(logger, cfg)`) can be registered with both `Syncer` and `PlaylistSyncService` — DRY provider construction
* Good, because capability-based interfaces (`HistoryFetcher`, `PlaylistManager`, `PlaylistSyncer`, `Authenticator`) allow type assertions at runtime to check what operations a provider supports
* Bad, because the factory slice is unordered — provider execution order depends on registration order in `main.go`, not an explicit priority
* Bad, because factories are instantiated on every sync cycle via `getActiveProviders()` — no caching of provider instances across calls

### Confirmation

Compliance is confirmed by checking that `internal/providers/providers.go` defines the `Provider` interface, capability interfaces (`HistoryFetcher`, `PlaylistManager`, `PlaylistSyncer`, `Authenticator`), and the `Factory` type. Both `Syncer` and `PlaylistSyncService` in `internal/services/` should hold `[]providers.Factory` slices populated via `Register()` calls. Provider registration should occur in `cmd/server/main.go` — no direct provider construction should exist in the service layer.

## Pros and Cons of the Options

### Factory Slice with Per-User Instantiation

The `Factory` type (`internal/providers/providers.go:124`) is defined as `func(ctx context.Context, user *ent.User) (Provider, error)`. The `Syncer` struct (`internal/services/sync.go:21-27`) holds `Factories []providers.Factory`, populated via `Register()` at line 40. The `PlaylistSyncService` struct (`internal/services/playlist_sync.go:21-28`) independently holds its own `Factories []providers.Factory`, also populated via `Register()` at line 49. At runtime, `Syncer.getActiveProviders()` at `sync.go:122` refreshes the user entity with all auth edges (`WithSpotifyAuth()`, `WithNavidromeAuth()`, `WithLastfmAuth()`), then iterates all registered factories, calling each with the user context. Factories return `nil, nil` when the user lacks credentials for that provider, and the method collects non-nil providers into the active list. Registration in `cmd/server/main.go:79-81` registers three providers with the Syncer and one with the PlaylistSyncService: `syncer.Register(navidrome.New(logger, cfg))`, `syncer.Register(spotify.New(logger, cfg))`, `syncer.Register(lastfm.New(logger, cfg))`, and `playlistSyncSvc.Register(navidrome.New(logger, cfg))`.

The base `Provider` interface (`providers.go:44-48`) requires only `Type() Type`. Capability interfaces extend it: `HistoryFetcher` adds `GetRecentListens()` for listening history retrieval, `PlaylistManager` adds `GetPlaylists()` and `CreatePlaylist()` for reading/writing playlists from providers, `PlaylistSyncer` adds `SyncPlaylist()`, `DeletePlaylist()`, and `UpdatePlaylistTracks()` for pushing playlists to a provider, and `Authenticator` adds `SupportsAuth()`, `GetAuthURL()`, `ExchangeCode()`, `RefreshToken()`, and `Disconnect()` for OAuth flow management. A separate `AuthenticatorFactory` type (`providers.go:128`) exists for creating authenticators without a user context, since the auth flow starts before the user is identified.

* Good, because `getActiveProviders()` refreshes user auth edges before factory calls — ensuring factories see the latest OAuth token state
* Good, because the same `navidrome.New(logger, cfg)` factory is registered with both `Syncer` and `PlaylistSyncService`, demonstrating factory reuse
* Good, because `Syncer.Sync()` at line 44 uses `getActiveProviders()` once and passes the resulting slice to both `syncHistory()` and `syncPlaylists()` — single instantiation per sync cycle
* Good, because `SyncProvider()` at line 68 filters the active providers list to run only a specific provider type — supports targeted sync from the UI
* Good, because the `Authenticator` interface is explicitly documented to exclude Navidrome (`providers.go:104`) — preventing confusion between app login and provider connection
* Neutral, because three providers may not warrant a factory abstraction, but the pattern costs little and enables future providers
* Bad, because no type-keyed registry (unlike the enricher pattern in ADR-0015) — providers are identified only by position in the slice, requiring iteration to find a specific type

### Hardcoded Provider List

Services directly construct provider instances, checking user credentials inline.

* Good, because explicit — the full provider setup is visible in one place
* Good, because no factory abstraction overhead for only three providers
* Bad, because adding a provider requires modifying both `Syncer` and `PlaylistSyncService` code
* Bad, because per-user credential checks are interleaved with provider construction logic
* Bad, because testing requires mocking external HTTP clients rather than injecting test factories
* Bad, because the Syncer and PlaylistSyncService would duplicate provider construction code

### Single Super-Provider

One provider implementation that internally delegates to Navidrome, Spotify, and Last.fm.

* Good, because single interface for all provider operations
* Bad, because violates single responsibility — one package depends on all external service clients
* Bad, because a rate limit or error in one provider could block operations for others
* Bad, because per-user credential checking for three different OAuth flows becomes complex in one implementation
* Bad, because the `Authenticator` flow differs fundamentally between Spotify (OAuth 2.0) and Last.fm (web auth) and Navidrome (Subsonic API) — forcing them into one implementation is awkward

### External Plugin Registry

Load provider implementations dynamically from configuration or shared libraries.

* Good, because truly decoupled — providers can be developed independently
* Bad, because Go's `plugin` package only works on Linux
* Bad, because massive operational complexity for three providers
* Bad, because providers need access to the `ent.User` entity and auth edges — sharing Go types across plugin boundaries is fragile
* Bad, because debugging across plugin boundaries is difficult

## More Information

* Provider interfaces: `internal/providers/providers.go` — `Provider`, `HistoryFetcher`, `PlaylistManager`, `PlaylistSyncer`, `Authenticator`, `Factory`, `AuthenticatorFactory`
* Provider type constants: `internal/providers/providers.go:13-17` — `TypeSpotify`, `TypeNavidrome`, `TypeLastFM`
* Syncer registration: `cmd/server/main.go:79-81` — three `syncer.Register()` calls
* PlaylistSyncService registration: `cmd/server/main.go:85` — one `playlistSyncSvc.Register()` call
* Syncer struct: `internal/services/sync.go:21-27` — `Factories []providers.Factory`
* Per-user instantiation: `internal/services/sync.go:122-147` — `getActiveProviders()` refreshes user, iterates factories, collects active providers
* PlaylistSyncService struct: `internal/services/playlist_sync.go:21-28` — independent `Factories []providers.Factory`
* Navidrome provider factory: `internal/providers/navidrome/` — implements `HistoryFetcher`, `PlaylistManager`, `PlaylistSyncer`
* Spotify provider factory: `internal/providers/spotify/` — implements `HistoryFetcher`, `PlaylistManager`, `Authenticator`
* Last.fm provider factory: `internal/providers/lastfm/` — implements `HistoryFetcher`, `Authenticator`
* Related: ADR-0015 (enricher registry uses a type-keyed map variant of the same factory pattern)
* Governing specification: `music-provider-integration`
