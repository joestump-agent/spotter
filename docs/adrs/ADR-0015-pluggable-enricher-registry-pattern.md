---
status: accepted
date: 2026-02-21
decision-makers: joestump
---

# ADR-0015: Type-Keyed Enricher Registry with Factory Pattern over Hardcoded Enrichment Chain

## Context and Problem Statement

Spotter enriches music catalog entities (artists, albums, tracks) with metadata from seven external sources: MusicBrainz, Lidarr, Navidrome, Spotify, Last.fm, Fanart.tv, and OpenAI. Each enricher is optional — it depends on whether the user has configured the relevant API key or service credentials. The enrichment order matters (MusicBrainz first for ID matching, OpenAI last for AI-powered summaries that use data from earlier enrichers). How should the application organize, register, and execute enrichers in a way that supports optional availability, configurable ordering, and easy addition of new sources?

<!-- Governing: SPEC metadata-enrichment-pipeline -->

## Decision Drivers

* Seven enrichers today, with the expectation that more may be added (e.g., Discogs, AllMusic)
* Each enricher is conditionally available based on per-user credentials — Spotify requires OAuth tokens, OpenAI requires an API key, Fanart.tv requires an API key
* Enricher execution order is significant and must be configurable via the `SPOTTER_METADATA_ORDER` environment variable
* Enrichers implement different capability interfaces (`ArtistEnricher`, `AlbumEnricher`, `TrackEnricher`, `IDMatcher`) — not all enrichers support all entity types
* The `MetadataService` should not need modification when a new enricher is added — only `cmd/server/main.go` registration changes

## Considered Options

* **Type-keyed Registry with Factory pattern** — `Registry` struct mapping `Type` to `Factory` functions; factories instantiate enrichers per-user at runtime; execution order from `DefaultOrder()` or config
* **Hardcoded ordered slice in MetadataService** — service directly creates enricher instances in a fixed order
* **Single monolithic enricher** — one enricher implementation that internally calls all sources
* **Plugin system with shared libraries** — load enrichers dynamically from `.so` files at runtime

## Decision Outcome

Chosen option: **Type-keyed Registry with Factory pattern**, because it cleanly separates enricher registration (at startup in `cmd/server/main.go`) from enricher execution (at runtime in `MetadataService`), supports per-user conditional instantiation via the `Factory` function signature `func(ctx, user) (Enricher, error)`, and allows execution order to be configured externally via `SPOTTER_METADATA_ORDER`. Adding a new enricher requires only implementing the `Enricher` interface (plus capability interfaces), writing a `New()` factory function, and adding one `Register()` call in `main.go`.

### Consequences

* Good, because the `MetadataService` is decoupled from individual enricher implementations — it only depends on the `enrichers` package interfaces
* Good, because each enricher is independently testable — the `Factory` function can return mock enrichers in tests
* Good, because the `Factory` signature `func(ctx, user) (Enricher, error)` returns `nil, nil` when the enricher is not configured, allowing graceful degradation
* Good, because execution order is configurable via `SPOTTER_METADATA_ORDER` (parsed by `Config.MetadataEnricherOrder()`) with a sensible `DefaultOrder()` fallback
* Good, because capability-based interfaces (`ArtistEnricher`, `AlbumEnricher`, `TrackEnricher`, `IDMatcher`) allow enrichers to declare exactly which entity types they support via Go type assertions
* Bad, because the `Registry` uses `map[Type]Factory` — only one factory per type is allowed, so two enrichers of the same type cannot coexist
* Bad, because the `getActiveEnrichers()` method instantiates all enrichers on every sync cycle, even when only one entity type needs enrichment

### Confirmation

Compliance is confirmed by checking that `internal/enrichers/enrichers.go` contains the `Registry` struct with `map[Type]Factory`, the `Factory` type alias, and `DefaultOrder()`. The `MetadataService` in `internal/services/metadata.go` should use `s.Registry.Get(t)` to look up factories and `getActiveEnrichers()` to instantiate them per-user. Enricher registration should occur exclusively in `cmd/server/main.go` via `metadataSvc.Register()` calls.

## Pros and Cons of the Options

### Type-Keyed Registry with Factory Pattern

The `Registry` struct (`internal/enrichers/enrichers.go:178-180`) holds a `map[Type]Factory` where `Type` is a string constant (e.g., `TypeMusicBrainz`, `TypeSpotify`, `TypeOpenAI`) and `Factory` is `func(ctx context.Context, user *ent.User) (Enricher, error)`. Seven enricher types are defined as constants at lines 12-19. Registration happens in `cmd/server/main.go:89-95` where each enricher package's `New()` function is called and passed to `metadataSvc.Register()`. At enrichment time, `MetadataService.getActiveEnrichers()` at line 478 iterates the configured order (from `Config.MetadataEnricherOrder()`), looks up each factory via `s.Registry.Get(t)`, calls it with the current user context, checks `IsAvailable()`, and collects active enrichers. The base `Enricher` interface requires `Type()`, `Name()`, and `IsAvailable()`. Capability interfaces extend this: `ArtistEnricher` adds `EnrichArtist()` and `GetArtistImages()`, `AlbumEnricher` adds `EnrichAlbum()` and `GetAlbumImages()`, `TrackEnricher` adds `EnrichTrack()`, and `IDMatcher` adds `MatchArtist()`, `MatchAlbum()`, `MatchTrack()`. The enrichment loop uses Go type assertions (`e.(enrichers.ArtistEnricher)`) to check capabilities before calling entity-specific methods.

* Good, because `DefaultOrder()` at line 221 defines a sensible execution order: MusicBrainz first (ID matching), then Lidarr, Navidrome, Spotify, Last.fm, Fanart (metadata), OpenAI last (AI enrichment that benefits from earlier data)
* Good, because `ParseType()` at line 210 validates enricher type strings, preventing typos in the `SPOTTER_METADATA_ORDER` config
* Good, because the `Factory` returning `nil, nil` is an explicit "not configured" signal — no need for separate availability checks before instantiation
* Good, because capability interfaces allow a single enricher (e.g., OpenAI) to implement `ArtistEnricher`, `AlbumEnricher`, and `TrackEnricher` simultaneously (as seen in `internal/enrichers/openai/openai.go:52-55`)
* Neutral, because the `Registry` does not enforce that registered types match `DefaultOrder()` — an unregistered type in the order is silently skipped
* Bad, because no concurrency controls on the registry — registrations must happen before any concurrent access (acceptable since registration only occurs at startup)

### Hardcoded Ordered Slice in MetadataService

The `MetadataService` directly constructs enrichers in a fixed order, with `if` guards for availability.

* Good, because simple to understand — the enrichment pipeline is visible in one method
* Good, because no abstraction overhead — direct construction and invocation
* Bad, because adding a new enricher requires modifying the `MetadataService` code — violates open/closed principle
* Bad, because the execution order is fixed at compile time — no runtime configuration
* Bad, because conditional availability checks are interleaved with construction, making the method long and brittle
* Bad, because testing requires mocking external service clients rather than injecting test factories

### Single Monolithic Enricher

One large enricher implementation that internally calls all sources in sequence.

* Good, because single entry point — `Enrich(artist)` handles everything
* Bad, because violates single responsibility — one package depends on MusicBrainz, Spotify, Last.fm, OpenAI, Fanart.tv, etc.
* Bad, because a failure in one source (e.g., Spotify rate limit) could block all enrichment
* Bad, because per-user credential checking is mixed with enrichment logic
* Bad, because the implementation would be thousands of lines in a single file

### Plugin System with Shared Libraries

Use Go's `plugin` package to load enrichers from `.so` shared libraries at runtime.

* Good, because truly decoupled — enrichers can be developed and deployed independently
* Good, because supports third-party enricher development without modifying the core application
* Bad, because Go's `plugin` package only works on Linux — incompatible with macOS development
* Bad, because shared libraries must be compiled with the exact same Go version and dependencies as the host
* Bad, because significant operational complexity for a personal, single-instance application
* Bad, because debugging across plugin boundaries is difficult

## More Information

* Enricher interfaces and registry: `internal/enrichers/enrichers.go` — `Type` constants, `Enricher`, `ArtistEnricher`, `AlbumEnricher`, `TrackEnricher`, `IDMatcher` interfaces, `Factory` type, `Registry` struct, `DefaultOrder()`
* Enricher registration: `cmd/server/main.go:88-95` — seven `metadataSvc.Register()` calls, one per enricher
* Active enricher resolution: `internal/services/metadata.go:478-511` — `getActiveEnrichers()` iterates configured order, instantiates via factories, checks availability
* MetadataService struct: `internal/services/metadata.go:33-40` — holds `*enrichers.Registry`
* Configurable order: `internal/config/config.go:136-149` — `MetadataEnricherOrder()` parses `SPOTTER_METADATA_ORDER`
* Default order config: `internal/config/config.go:255` — `"lidarr,musicbrainz,navidrome,spotify,lastfm,fanart,openai"`
* OpenAI enricher implementing multiple capability interfaces: `internal/enrichers/openai/openai.go:52-55`
* Governing specification: `metadata-enrichment-pipeline`
