---
status: accepted
date: 2026-02-21
decision-makers: joestump
---

# ADR-0019: slog-Based Structured Event Logging as Lightweight Metrics over Dedicated Metrics Infrastructure

## Context and Problem Statement

Spotter's background operations — listen sync, metadata enrichment, playlist write-back, AI mixtape generation, and similar artist discovery — produce no structured metrics about their execution. The only observability is unstructured `logger.Error(...)` calls on failure and occasional `logger.Info(...)` on startup. There is no way to answer questions like: "How long does a metadata enrichment run take?", "How many listens were synced in the last hour?", "What is the success rate of track matching?", or "How many OpenAI tokens are consumed per mixtape generation?" How should Spotter expose operational metrics for a single-instance, personal-use deployment without requiring external metrics infrastructure?

## Decision Drivers

* Spotter is a personal application running on a single instance — metrics infrastructure (Prometheus, Grafana, Datadog) is excessive
* The existing `*slog.Logger` is already injected into every service via dependency injection (ADR-0010)
* `slog` supports swappable handlers — `TextHandler` for human-readable output, `JSONHandler` for machine-parseable output
* JSON-formatted log events are directly parseable by `jq`, `loki`, or simple shell scripts without any client library
* Background loops currently log errors but not success metrics — there is no record of normal operation
* Track matching uses a three-tier strategy (ISRC, exact match, fuzzy match) but there is no data on which strategy succeeds most often (ADR-0014)
* AI operations (OpenAI API calls) have cost implications — token usage should be observable

## Considered Options

* **slog-based structured event logging** — emit well-defined metric events via `slog.Info()` with standardized keys, optionally formatted as JSON via `SPOTTER_LOG_FORMAT=json`
* **Prometheus client library** — expose a `/metrics` endpoint with counters, histograms, and gauges scraped by a Prometheus server
* **Datadog agent/StatsD** — emit metrics to a local Datadog agent or StatsD collector via UDP
* **No metrics** — continue with error-only logging and rely on manual investigation when issues arise

## Decision Outcome

Chosen option: **slog-based structured event logging**, because it builds on the existing slog infrastructure (ADR-0010), requires zero additional dependencies, produces metric events that are human-readable in text mode and machine-parseable in JSON mode, and is appropriate for a personal-use application where the operator is also the developer. Metric events are emitted as `slog.Info("metric.{category}", ...)` calls with well-defined attribute keys (`enricher`, `provider`, `duration_ms`, `success`, `strategy`, `model`, `tokens_used`, etc.). The log format is controlled by a new `SPOTTER_LOG_FORMAT` environment variable: `text` (default, current behavior) or `json` (switches the handler to `slog.JSONHandler`).

### Consequences

* Good, because zero additional dependencies — uses only the existing `log/slog` package already in every service
* Good, because JSON output is directly parseable by `jq`, `loki`, or log aggregation tools: `cat spotter.log | jq 'select(.msg == "metric.sync")'`
* Good, because `SPOTTER_LOG_FORMAT` is opt-in — existing `TextHandler` output is unchanged by default
* Good, because metric events use the same `*slog.Logger` injection path — no new plumbing required in service constructors
* Good, because well-defined attribute keys provide a stable schema for dashboards and alerts without formal schema versioning
* Good, because applicable to all observable operations: sync, enrichment, track matching, AI generation, HTTP requests
* Bad, because log-based metrics lack aggregation — there are no pre-computed counters, histograms, or percentiles; these must be derived by parsing logs
* Bad, because no alerting integration — an external tool must tail logs and trigger alerts on error patterns
* Bad, because high-frequency metric events (e.g., per-track matching) may produce verbose log output — log volume should be monitored
* Bad, because no built-in metric visualization — requires `jq` scripts, `loki` + Grafana, or custom tooling to create dashboards

### Confirmation

Compliance is confirmed by verifying that `cmd/server/main.go` reads `SPOTTER_LOG_FORMAT` (or equivalent Viper config) and switches between `slog.NewTextHandler` and `slog.NewJSONHandler`. Key metric events should be present as `slog.Info("metric.*", ...)` calls in sync services, enricher pipelines, track matching, and AI generation code. No Prometheus, Datadog, or StatsD imports should appear in `go.mod`.

## Pros and Cons of the Options

### slog-Based Structured Event Logging

Metric events emitted as `slog.Info("metric.sync", slog.String("provider", "navidrome"), slog.Int("listens_synced", 42), slog.Int64("duration_ms", 1234), slog.String("error", ""))`. The handler is selected at startup based on `SPOTTER_LOG_FORMAT`:

```go
var handler slog.Handler
if cfg.Log.Format == "json" {
    handler = slog.NewJSONHandler(os.Stdout, opts)
} else {
    handler = slog.NewTextHandler(os.Stdout, opts)
}
logger := slog.New(handler)
```

**Defined metric event categories:**

| Event | Key attributes |
|-------|---------------|
| `metric.sync` | `provider`, `listens_synced`, `playlists_synced`, `duration_ms`, `error` |
| `metric.enricher` | `enricher`, `entity_type`, `duration_ms`, `success` |
| `metric.background_tick` | `loop`, `users_processed`, `duration_ms` |
| `metric.track_match` | `strategy` (isrc/exact/fuzzy), `matched` (bool), `confidence`, `duration_ms` |
| `metric.llm` | `model`, `tokens_used`, `duration_ms`, `success` |
| `metric.request` | (existing) `method`, `path`, `status`, `latency`, `request_id` |

* Good, because the existing request logging middleware (`internal/middleware/logging.go`) is already a metric event — it just needs standardized keys
* Good, because each metric event is a single `slog.Info()` call — minimal code overhead at each instrumentation point
* Good, because `slog.With()` can create sub-loggers with pre-set context attributes (e.g., `logger.With("loop", "sync")`) to reduce repetition
* Neutral, because the `metric.*` naming convention is a project convention, not enforced by slog — discipline required to maintain it
* Bad, because no built-in rate limiting on metric events — a sync that processes 10,000 listens would emit 10,000 track match events if instrumented per-track

### Prometheus Client Library

`github.com/prometheus/client_golang` — register counters, histograms, and gauges; expose a `/metrics` endpoint for Prometheus scraping.

* Good, because Prometheus is the industry standard for Go application metrics — well-established patterns and tooling
* Good, because histograms provide automatic percentile calculation (p50, p95, p99) for latency metrics
* Good, because counters and gauges provide real-time aggregated values without log parsing
* Good, because Grafana dashboards can be built directly against Prometheus data
* Bad, because requires running a Prometheus server to scrape the `/metrics` endpoint — external infrastructure
* Bad, because adds `github.com/prometheus/client_golang` and its transitive dependencies to `go.mod`
* Bad, because metric registration (counter/histogram/gauge definitions) adds boilerplate code across all services
* Bad, because overkill for a single-instance personal application — Prometheus is designed for fleet-wide observability

### Datadog Agent / StatsD

Emit metrics via UDP to a local Datadog agent or StatsD collector using a client library like `github.com/DataDog/datadog-go`.

* Good, because StatsD protocol is simple — counter and timing metrics via UDP with minimal overhead
* Good, because Datadog provides visualization, alerting, and anomaly detection out of the box
* Bad, because requires running a Datadog agent (or StatsD-compatible collector) alongside the application
* Bad, because Datadog is a paid SaaS service — cost is unjustifiable for a personal music application
* Bad, because adds an external dependency and UDP networking for metrics collection
* Bad, because StatsD metrics lack the rich context of structured log attributes (key-value pairs with string values)

### No Metrics

Continue with error-only logging. Rely on the absence of error logs to infer healthy operation.

* Good, because zero implementation effort
* Good, because no additional log volume
* Bad, because "no errors" does not mean "working correctly" — a sync loop that silently processes zero listens would not be detected
* Bad, because no data on AI token consumption — cost implications are invisible
* Bad, because no performance baseline — gradual degradation (e.g., enrichment taking longer over time) would not be noticed
* Bad, because debugging production issues requires reproducing them locally — there is no historical record of normal vs. abnormal behavior

## More Information

* Current logger initialization: `cmd/server/main.go:38-41` — `slog.New(slog.NewTextHandler(os.Stdout, opts))`
* Current request logging: `internal/middleware/logging.go:11-31` — structured HTTP request logging with method, path, status, latency, request ID
* slog handler choice: see ADR-0010 (stdlib slog structured logging)
* Background loops to instrument: see ADR-0013 (goroutine ticker background scheduling)
* Track matching strategies to observe: see ADR-0014 (three-tier track matching algorithm)
* AI API backend: see ADR-0008 (OpenAI API / LiteLLM-compatible LLM backend)
* Configuration system: see ADR-0009 (Viper environment variable configuration)
