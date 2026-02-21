---
status: proposed
date: 2026-02-21
decision-makers: joestump
---

# ADR-0017: Generator Interface Abstraction over Direct Concrete Type Dependencies for AI Mixtape Engine

## Context and Problem Statement

Spotter's vibes system has two AI-powered operations: generating mixtapes from scratch (`MixtapeGenerator`) and enhancing existing playlists (`PlaylistEnhancer`). Both are currently backed by OpenAI API calls. HTTP handlers and background services need to invoke these operations without being coupled to the specific AI provider implementation. How should the vibes package expose its generation capabilities to consumers?

## Decision Drivers

* HTTP handlers in `internal/handlers/` call `GenerateMixtape()` and `EnhancePlaylist()` — they should not need to know which LLM provider is behind the call
* The OpenAI implementation requires `*ent.Client`, `*config.Config`, `*slog.Logger`, `*events.Bus`, and an `*http.Client` — these dependencies should not leak into handler code
* Future LLM providers (Anthropic, Ollama, local models) or rule-based generators should be swappable without modifying handler code
* Testing handlers requires mocking the generation behavior — an interface enables test doubles without build tags or complex test fixtures
* The vibes package already defines rich request/result types (`GenerationRequest`, `GenerationResult`, `EnhancementRequest`, `EnhancementResult`) that serve as the contract between callers and implementations

## Considered Options

* **Generator interface with single concrete implementation** — define a `Generator` interface in `internal/vibes/types.go` that `MixtapeGenerator` satisfies; consumers depend on the interface
* **Direct concrete type dependency** — handlers import and depend on `*vibes.MixtapeGenerator` and `*vibes.PlaylistEnhancer` directly
* **Function callback (closure injection)** — pass `func(ctx, *GenerationRequest) (*GenerationResult, error)` closures to handlers instead of an interface or struct
* **Separate generator microservice** — extract generation into a standalone HTTP service with its own API

## Decision Outcome

Chosen option: **Generator interface with single concrete implementation**, because it provides a clean dependency inversion point between HTTP handlers and the AI-powered generation logic. The `Generator` interface is defined in `internal/vibes/types.go:132-135` with a single method `GenerateMixtape(ctx context.Context, req *GenerationRequest) (*GenerationResult, error)`. The `MixtapeGenerator` struct in `internal/vibes/generator.go:36-43` is the sole concrete implementation, backed by OpenAI. All request and result types are defined alongside the interface in `types.go`, forming a self-contained contract that any future implementation must satisfy.

**Current state**: The `Handler` struct in `internal/handlers/handlers.go:40-41` currently holds `*vibes.MixtapeGenerator` and `*vibes.PlaylistEnhancer` as concrete pointer types rather than the `Generator` interface. This means the interface exists but is not yet fully leveraged for decoupling at the handler layer. The interface is positioned as the target abstraction for future refactoring — when a second implementation is introduced, handlers can switch to the interface type with no behavioral change.

### Consequences

* Good, because the `Generator` interface has a single method — minimal surface area that is easy to implement, mock, and reason about
* Good, because all input/output types (`GenerationRequest`, `GenerationResult`, `Seed`, `SeedType`, `GeneratedTrack`) are defined in the same `types.go` file as the interface — implementations only need to import one package
* Good, because the interface enables test doubles — handler tests can verify generation flow without making real OpenAI API calls (currently `vibes_test.go:739` tests nil-generator behavior)
* Good, because `MixtapeGenerator` encapsulates all OpenAI-specific concerns: prompt template loading, HTTP client construction, `ChatRequest`/`ChatResponse` serialization, and token tracking — none of this leaks through the interface
* Good, because the `GenerationResult` struct captures rich metadata (prompt used, model used, tokens consumed, match statistics) without the interface method needing multiple return values
* Bad, because the interface currently has only one implementation — the abstraction is speculative until a second generator is introduced
* Bad, because `PlaylistEnhancer` does not implement `Generator` — it has a different method signature (`EnhancePlaylist`) and different request/result types, so there is no unified interface across both operations
* Bad, because `Handler` still holds concrete types (`*vibes.MixtapeGenerator`, `*vibes.PlaylistEnhancer`) rather than the interface — the decoupling benefit is not yet realized at the wiring level in `handlers.go:40-41`

### Confirmation

Compliance is confirmed by `internal/vibes/types.go` containing a `Generator` interface with a `GenerateMixtape` method. The `MixtapeGenerator` struct in `internal/vibes/generator.go` must have a `GenerateMixtape` method with a matching signature. No handler code should directly call OpenAI — all AI interaction must flow through the `MixtapeGenerator` or `PlaylistEnhancer` types. The interface's request and result types should be the sole contract between callers and implementations.

## Pros and Cons of the Options

### Generator Interface with Single Concrete Implementation

A `Generator` interface in `internal/vibes/types.go:132-135` defining `GenerateMixtape(ctx context.Context, req *GenerationRequest) (*GenerationResult, error)`. The `MixtapeGenerator` struct satisfies this interface implicitly (Go structural typing). Consumers depend on the interface type; the concrete implementation is injected at startup in `cmd/server/main.go:98`.

* Good, because Go's implicit interface satisfaction means `MixtapeGenerator` does not need to declare `implements Generator` — adding the interface is non-breaking
* Good, because the `GenerationRequest` struct provides a rich parameter object pattern — DJ persona, seed data (artist/album/tracks), max tracks, user ID — avoiding long parameter lists
* Good, because `GenerationResult` includes debugging metadata (`PromptUsed`, `ModelUsed`, `TokensUsed`) that implementations can populate differently
* Good, because the `Seed` type system (`SeedTypeArtist`, `SeedTypeAlbum`, `SeedTypeTracks`) with constructor functions (`NewArtistSeed`, `NewAlbumSeed`, `NewTracksSeed`) provides a clean API for callers to specify generation context
* Good, because a future Anthropic or Ollama implementation would only need to implement the single `GenerateMixtape` method while reusing all request/result types
* Neutral, because the interface has only one method — could grow as new generation modes are added (e.g., regenerate, partial regenerate)
* Bad, because `PlaylistEnhancer.EnhancePlaylist()` is a parallel concept but uses different types (`EnhancementRequest`, `EnhancementResult`) — no unified `Enhancer` interface exists yet

### Direct Concrete Type Dependency

Handlers import `*vibes.MixtapeGenerator` directly and call its methods. No interface indirection.

* Good, because simpler — no interface to maintain, no risk of interface drift
* Good, because IDE navigation goes directly to the implementation
* Good, because this is what the codebase currently does at the handler layer (`handlers.go:40` uses `*vibes.MixtapeGenerator`)
* Bad, because handlers are coupled to the OpenAI-specific implementation — switching to a different LLM requires changing handler code
* Bad, because testing requires either a real OpenAI connection or nil-checking the generator (current pattern in `vibes_test.go:745`)
* Bad, because the `MixtapeGenerator` constructor requires `*ent.Client`, `*config.Config`, `*slog.Logger`, `*events.Bus` — test setup becomes heavyweight

### Function Callback (Closure Injection)

Instead of an interface, pass `func(context.Context, *GenerationRequest) (*GenerationResult, error)` as a field on `Handler`.

* Good, because maximally lightweight — no interface type needed, just a function signature
* Good, because closures can capture any dependencies without a struct
* Good, because easy to create test doubles: `handler.Generate = func(ctx, req) { return mockResult, nil }`
* Bad, because function types lack discoverability — `Handler.Generate func(...)` is less self-documenting than `Handler.Generator vibes.Generator`
* Bad, because multiple methods per implementation (e.g., adding `RegenerateMixtape` alongside `GenerateMixtape`) would require multiple function fields, becoming unwieldy
* Bad, because function callbacks cannot carry shared state or be introspected — no way to log which implementation is active or check capabilities

### Separate Generator Microservice

Extract the mixtape generation into a standalone HTTP/gRPC service that Spotter calls remotely.

* Good, because true decoupling — the generator can be scaled, deployed, and versioned independently
* Good, because language-agnostic — the generator could be written in Python with better ML library support
* Bad, because dramatically increases infrastructure complexity — contradicts the single-binary, single-instance deployment model (ADR-0003)
* Bad, because network latency and serialization overhead for every generation request
* Bad, because the generator needs access to the user's library (available tracks, listening history) — either the service needs database access or the caller must serialize large datasets in each request
* Bad, because a personal music server does not benefit from microservice scaling — the user base is typically one person

## More Information

* Generator interface: `internal/vibes/types.go:132-135` — `Generator` interface with `GenerateMixtape` method
* Request/result types: `internal/vibes/types.go:62-129` — `GenerationRequest`, `GenerationResult`, `GeneratedTrack`, `GenerationStats`
* Seed types: `internal/vibes/types.go:9-59` — `SeedType`, `Seed`, constructor functions
* MixtapeGenerator implementation: `internal/vibes/generator.go:36-43` — concrete struct with OpenAI backing
* MixtapeGenerator constructor: `internal/vibes/generator.go:46-74` — `NewMixtapeGenerator()` with dependency injection
* PlaylistEnhancer implementation: `internal/vibes/enhancer.go:30-37` — parallel concrete struct (no shared interface)
* Enhancement types: `internal/vibes/types.go:223-346` — `EnhancementRequest`, `EnhancementResult`, `EnhancedTrack`, `EnhancementMode`
* Handler struct (concrete types): `internal/handlers/handlers.go:40-41` — `MixtapeGenerator *vibes.MixtapeGenerator`, `PlaylistEnhancer *vibes.PlaylistEnhancer`
* Handler construction: `internal/handlers/handlers.go:46-60` — `New()` function accepting concrete types
* Wiring in main: `cmd/server/main.go:98-106` — `vibes.NewMixtapeGenerator()` and `vibes.NewPlaylistEnhancer()` created and injected
* Handler usage (vibes): `internal/handlers/vibes.go:605-667` — `h.MixtapeGenerator.GenerateMixtape(ctx, req)`
* Handler usage (artists): `internal/handlers/artists.go:799-816` — artist-seeded mixtape generation
* Handler usage (albums): `internal/handlers/albums.go:575-592` — album-seeded mixtape generation
* Handler usage (playlists): `internal/handlers/playlists.go:898-923` — `h.PlaylistEnhancer.EnhancePlaylist(ctx, req)`
* Nil-generator test: `internal/handlers/vibes_test.go:739` — tests behavior when generator is not configured
* OpenAI configuration: see ADR-0008
* Single-instance constraint: see ADR-0003 (SQLite)
