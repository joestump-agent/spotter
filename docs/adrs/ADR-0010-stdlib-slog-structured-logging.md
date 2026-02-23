---
status: accepted
date: 2026-02-21
decision-makers: joestump
---

# ADR-0010: Go stdlib log/slog over Third-Party Structured Loggers for Application Logging

## Context and Problem Statement

Spotter requires structured logging throughout the application: request logging in HTTP middleware, service-level operation tracking in sync and enrichment pipelines, AI API call diagnostics, and error reporting. The logger must support key-value attributes (structured fields), multiple log levels, and be injectable into services via dependency injection. Should the project use a third-party structured logging library or Go's standard library `log/slog` package introduced in Go 1.21?

## Decision Drivers

* Spotter targets Go 1.24+ (as declared in `go.mod`) — `log/slog` is fully available and stable
* The logger is passed as a `*slog.Logger` parameter to every service constructor — the interface must be widely understood
* Minimizing external dependencies is a project goal (self-hosted, personal application)
* Log output goes to stdout for container-friendly log collection — no need for complex log routing
* Multiple services need contextual logging with varying attribute sets (request IDs, user IDs, artist names, API response codes)
* Some services need the ability to suppress logging entirely (e.g., test contexts, optional enrichers)

## Considered Options

* **Go stdlib `log/slog`** — standard library structured logger with `TextHandler` writing to stdout
* **Uber Zap** — high-performance structured logger with zero-allocation logging
* **logrus** — widely-used structured logger with field-based API
* **zerolog** — zero-allocation JSON-first structured logger

## Decision Outcome

Chosen option: **Go stdlib `log/slog`**, because it provides structured logging with key-value attributes, multiple log levels, handler-based output formatting, and the `slog.Logger` type as a standard interface — all without adding any external dependency. The logger is initialized once in `cmd/server/main.go` with a `TextHandler` writing to `os.Stdout` at `Debug` level, then passed via dependency injection to every service. This adds zero entries to `go.mod` for logging.

### Consequences

* Good, because zero external dependencies for logging — `log/slog` is part of the Go standard library since Go 1.21
* Good, because `*slog.Logger` is the standard interface — any Go developer recognizes it without learning a third-party API
* Good, because the `slog.Handler` interface allows swapping output format (text, JSON) without changing any call sites
* Good, because structured attributes (`slog.String()`, `slog.Int()`, `slog.Duration()`) provide type-safe key-value logging
* Good, because the no-op handler pattern (`nopHandler`) enables silent loggers for optional services and tests
* Bad, because `slog.TextHandler` output is less structured than JSON for log aggregation tools — switching to `slog.JSONHandler` would fix this but is not yet configured
* Bad, because no built-in log rotation or file output — relies on container runtime or external tools for log management

### Confirmation

Compliance is confirmed by verifying that `go.mod` contains no logging library imports (no `go.uber.org/zap`, `github.com/sirupsen/logrus`, or `github.com/rs/zerolog`). All services should accept `*slog.Logger` as a constructor parameter. The logger should be initialized in `cmd/server/main.go` using `slog.New()` with a `slog.TextHandler`.

## Pros and Cons of the Options

### Go stdlib log/slog

Logger initialized in `cmd/server/main.go:38-41` as `slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))`. The `*slog.Logger` is passed to every service constructor: `services.NewSyncer(client, cfg, logger, bus)`, `services.NewMetadataService(client, cfg, logger, bus)`, `vibes.NewMixtapeGenerator(client, cfg, logger, bus)`, etc. HTTP request logging uses slog in `internal/middleware/logging.go:11-31` with structured attributes for method, path, status, remote IP, latency, and request ID. Services that may not need logging (e.g., the OpenAI enricher when created without a logger) use a `nopHandler` — a custom `slog.Handler` implementation that discards all records, defined in both `internal/enrichers/openai/openai.go:31-36` and `internal/vibes/generator.go:28-33`.

* Good, because standard library — no version conflicts, no supply chain risk, no breaking API changes
* Good, because `slog.Handler` interface is extensible — can add JSON output, log sampling, or remote shipping by swapping the handler
* Good, because structured attributes are compile-time type-checked via `slog.String()`, `slog.Int()`, `slog.Duration()`, etc.
* Good, because `slog.With()` enables contextual loggers with pre-set attributes (though not currently used extensively)
* Good, because the `nopHandler` pattern provides a clean way to create silent loggers without nil checks throughout the codebase
* Neutral, because `TextHandler` output format is human-readable but less machine-parseable than JSON — adequate for personal-use deployment
* Bad, because no built-in performance optimizations like Zap's zero-allocation encoding — acceptable for Spotter's throughput

### Uber Zap

`go.uber.org/zap` — high-performance structured logger designed for low-latency, high-throughput applications.

* Good, because zero-allocation logging in the hot path via `zap.Logger` (not `SugaredLogger`)
* Good, because built-in JSON encoder with nanosecond timestamp precision
* Good, because extensive ecosystem of integrations (gRPC, HTTP middleware, etc.)
* Bad, because complex API — `zap.Logger` vs `zap.SugaredLogger`, `zap.Field` constructors vs variadic key-value pairs
* Bad, because external dependency — adds `go.uber.org/zap`, `go.uber.org/atomic`, `go.uber.org/multierr` to `go.mod`
* Bad, because performance benefits are irrelevant for Spotter's scale (single-user, request rates in the single digits per second)

### logrus

`github.com/sirupsen/logrus` — one of the first popular structured loggers for Go, with a field-based API.

* Good, because familiar API used by many Go projects — low learning curve
* Good, because built-in formatters for text and JSON output
* Good, because hook system for routing logs to external services
* Bad, because the maintainer has declared the project in maintenance mode — no new features, only critical fixes
* Bad, because external dependency with its own set of transitive dependencies
* Bad, because the `logrus.Entry` field-based API is now superseded by `slog`'s standard approach

### zerolog

`github.com/rs/zerolog` — zero-allocation JSON logger designed for minimal overhead.

* Good, because true zero-allocation logging — even lower overhead than Zap in benchmarks
* Good, because fluent API: `log.Info().Str("key", "value").Msg("message")` is concise
* Good, because JSON output by default — ideal for log aggregation pipelines
* Bad, because JSON-first design is less readable for human debugging in a personal application context
* Bad, because external dependency — adds `github.com/rs/zerolog` to `go.mod`
* Bad, because fluent API style differs from slog's variadic approach — would be non-standard for Go 1.21+ projects

## More Information

* Logger initialization: `cmd/server/main.go:38-41` — `slog.New(slog.NewTextHandler(os.Stdout, opts))` with `LevelDebug`
* Request logging middleware: `internal/middleware/logging.go:11-31` — structured HTTP request logging with method, path, status, latency, request ID
* No-op handler (enricher): `internal/enrichers/openai/openai.go:31-36` — `nopHandler` struct implementing `slog.Handler` for silent logging
* No-op handler (vibes): `internal/vibes/generator.go:28-33` — identical `nopHandler` for the mixtape generator
* Logger injection pattern: `cmd/server/main.go:78-110` — single `logger` instance passed to all service constructors
* Go version: `go.mod:3` — `go 1.24.0` (slog available since Go 1.21)
* Related: ADR-0009 (Viper configuration that the logger reports on during startup)
