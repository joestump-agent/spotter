# Structured Metrics via slog JSON Events

**Status:** accepted
**Version:** 0.1.0
**Last Updated:** 2026-02-21
**Governing ADRs:** ADR-0019 (structured metrics observability), ADR-0010 (slog structured logging), ADR-0013 (background job scheduling), ADR-0008 (OpenAI API / LiteLLM backend)

## Overview

The observability subsystem provides lightweight, structured metric events emitted through the existing `slog` logger infrastructure. Instead of adding external metrics libraries (Prometheus, StatsD), Spotter emits well-defined `metric.*` log events with standardized attribute keys. These events are human-readable in text mode (default) and machine-parseable in JSON mode (opt-in via `SPOTTER_LOG_FORMAT=json`). Operators can use `jq`, `loki`, or simple scripts to aggregate, filter, and visualize metrics from log output.

## Scope

This spec covers:
- Log format configuration (`SPOTTER_LOG_FORMAT` environment variable)
- Metric event naming convention and attribute key schema
- Background job instrumentation (sync, enrichment, playlist sync)
- HTTP request instrumentation (enhancement of existing middleware)
- LLM/AI operation instrumentation (token usage, latency, model)
- Track matching instrumentation (strategy, confidence, success rate)

Out of scope: Log aggregation infrastructure (Loki, Grafana), alerting rules, log rotation and retention, dashboard templates.

---

## Requirements

### Log Format Configuration

**REQ-FMT-001** — The application MUST support a `SPOTTER_LOG_FORMAT` environment variable (or equivalent Viper config key `log.format`) with two valid values:
- `text` (default) — uses `slog.NewTextHandler(os.Stdout, opts)`
- `json` — uses `slog.NewJSONHandler(os.Stdout, opts)`

**REQ-FMT-002** — If `SPOTTER_LOG_FORMAT` is unset or set to any value other than `json`, the application MUST default to `text` format. Invalid values MUST NOT cause a startup failure.

**REQ-FMT-003** — The log format selection MUST occur in `cmd/server/main.go` during logger initialization, before any log statements are emitted.

**REQ-FMT-004** — The log level configuration MUST remain independent of the format selection. Both text and JSON modes MUST respect the configured log level (default: `Debug`).

**REQ-FMT-005** — The application MUST log the selected format on startup: `logger.Info("logger initialized", "format", format, "level", level)`.

### Metric Event Keys

**REQ-KEY-001** — All metric events MUST use the message prefix `metric.` followed by a category name. The following categories are defined:

| Message | Category | Description |
|---------|----------|-------------|
| `metric.sync` | Sync | Listen and playlist sync operations |
| `metric.enricher` | Enricher | Metadata enrichment runs per enricher |
| `metric.background_tick` | Background | Per-tick summary for each background loop |
| `metric.track_match` | Matching | Individual track matching attempts |
| `metric.llm` | LLM | AI/LLM API calls |
| `metric.request` | Request | HTTP request completion (existing middleware) |

**REQ-KEY-002** — Each metric event MUST include the attributes defined in its schema below. Optional attributes are marked with `(optional)`.

**REQ-KEY-003** — Duration attributes MUST use the key suffix `_ms` and represent milliseconds as `int64`. Durations MUST be measured using `time.Since(start).Milliseconds()`.

**REQ-KEY-004** — Boolean success/failure attributes MUST use the key `success` with a `bool` value. Error details (when `success=false`) MUST use the key `error` with a string value (empty string on success).

### Background Job Instrumentation

**REQ-BG-001** — Each background loop tick MUST emit a `metric.background_tick` event after processing all users for that tick:

| Attribute | Type | Description |
|-----------|------|-------------|
| `loop` | `string` | Loop identifier: `"sync"`, `"metadata"`, or `"playlist_sync"` |
| `users_processed` | `int` | Number of users processed in this tick |
| `duration_ms` | `int64` | Total wall-clock time for the tick |
| `errors` | `int` | Number of users that failed (optional, default 0) |

**REQ-BG-002** — The `metric.background_tick` event MUST be emitted at `Info` level regardless of whether errors occurred during the tick. Individual user errors MUST continue to be logged separately at `Error` level.

**REQ-BG-003** — The `metric.sync` event MUST be emitted after each per-user sync operation completes (success or failure):

| Attribute | Type | Description |
|-----------|------|-------------|
| `provider` | `string` | Provider name: `"navidrome"`, `"spotify"`, `"lastfm"` |
| `listens_synced` | `int` | Number of new listens imported |
| `playlists_synced` | `int` | Number of playlists synced |
| `duration_ms` | `int64` | Time to complete this provider sync |
| `success` | `bool` | Whether the sync completed without error |
| `error` | `string` | Error message if `success=false`, empty otherwise |

**REQ-BG-004** — The `metric.enricher` event MUST be emitted after each enricher completes processing an entity batch:

| Attribute | Type | Description |
|-----------|------|-------------|
| `enricher` | `string` | Enricher name: `"lidarr"`, `"musicbrainz"`, `"navidrome"`, `"spotify"`, `"lastfm"`, `"fanart"`, `"openai"` |
| `entity_type` | `string` | Entity type processed: `"artist"`, `"album"`, `"track"` |
| `entities_processed` | `int` | Number of entities processed (optional) |
| `duration_ms` | `int64` | Time to complete enrichment |
| `success` | `bool` | Whether enrichment completed without error |
| `error` | `string` | Error message if `success=false`, empty otherwise |

### Request Instrumentation

**REQ-REQ-001** — The existing request logging middleware (`internal/middleware/logging.go`) MUST be updated to use the `metric.request` message prefix for consistency with the metric event convention. The existing attributes (method, path, status, remote_ip, latency, request_id) MUST be preserved.

**REQ-REQ-002** — The request logging middleware MUST add a `duration_ms` attribute (in addition to the existing `latency` duration) for consistency with other metric events:

| Attribute | Type | Description |
|-----------|------|-------------|
| `method` | `string` | HTTP method |
| `path` | `string` | Request path |
| `status` | `int` | Response status code |
| `remote_ip` | `string` | Client IP address |
| `latency` | `duration` | Request duration (existing, for human readability) |
| `duration_ms` | `int64` | Request duration in milliseconds (for machine parsing) |
| `request_id` | `string` | Request ID from middleware |

### LLM Instrumentation

**REQ-LLM-001** — Every LLM API call (OpenAI or LiteLLM-compatible backend) MUST emit a `metric.llm` event upon completion:

| Attribute | Type | Description |
|-----------|------|-------------|
| `model` | `string` | Model identifier (e.g., `"gpt-4o"`, `"gpt-4o-mini"`) |
| `operation` | `string` | Operation type: `"mixtape_generate"`, `"playlist_enhance"`, `"similar_artists"`, `"artist_bio"`, `"album_review"`, `"track_review"`, `"playlist_metadata"` |
| `tokens_used` | `int` | Total tokens consumed (prompt + completion) |
| `prompt_tokens` | `int` | Prompt tokens (optional, if available from API response) |
| `completion_tokens` | `int` | Completion tokens (optional, if available from API response) |
| `duration_ms` | `int64` | Wall-clock time for the API call |
| `success` | `bool` | Whether the API call succeeded |
| `error` | `string` | Error message if `success=false`, empty otherwise |

**REQ-LLM-002** — Token counts MUST be extracted from the OpenAI API response `usage` field when available. If the API response does not include usage data (e.g., streaming responses), `tokens_used` MUST be set to `0` and the event MUST still be emitted.

**REQ-LLM-003** — The `operation` attribute MUST distinguish between different AI use cases so that token consumption can be analyzed per feature.

### Track Matching Instrumentation

**REQ-MATCH-001** — Each track matching attempt MUST emit a `metric.track_match` event:

| Attribute | Type | Description |
|-----------|------|-------------|
| `strategy` | `string` | Matching strategy used: `"isrc"`, `"exact"`, `"fuzzy"` |
| `matched` | `bool` | Whether a match was found |
| `confidence` | `float64` | Match confidence score (0.0-1.0, relevant for fuzzy matching) |
| `duration_ms` | `int64` | Time to complete the matching attempt |
| `source_provider` | `string` | Provider the track originated from (optional) |
| `target_provider` | `string` | Provider being matched against (optional) |

**REQ-MATCH-002** — If a track is matched via the first strategy (ISRC), the event for that strategy MUST have `matched=true`. Subsequent strategies (exact, fuzzy) MUST NOT emit events for that track — only the successful (or final failing) strategy emits.

**REQ-MATCH-003** — For tracks that fail all three strategies, a single `metric.track_match` event MUST be emitted with `strategy="fuzzy"` (the last attempted), `matched=false`, and `confidence=0.0`.

---

## Metric Event Examples

### JSON format (`SPOTTER_LOG_FORMAT=json`)

```json
{"time":"2026-02-21T10:30:00Z","level":"INFO","msg":"metric.sync","provider":"navidrome","listens_synced":42,"playlists_synced":3,"duration_ms":1234,"success":true,"error":""}
```

```json
{"time":"2026-02-21T10:31:00Z","level":"INFO","msg":"metric.enricher","enricher":"musicbrainz","entity_type":"artist","entities_processed":15,"duration_ms":8500,"success":true,"error":""}
```

```json
{"time":"2026-02-21T10:32:00Z","level":"INFO","msg":"metric.llm","model":"gpt-4o","operation":"mixtape_generate","tokens_used":2150,"prompt_tokens":1800,"completion_tokens":350,"duration_ms":4200,"success":true,"error":""}
```

```json
{"time":"2026-02-21T10:33:00Z","level":"INFO","msg":"metric.track_match","strategy":"fuzzy","matched":true,"confidence":0.85,"duration_ms":12,"source_provider":"spotify","target_provider":"navidrome"}
```

### Text format (default)

```text
time=2026-02-21T10:30:00Z level=INFO msg=metric.sync provider=navidrome listens_synced=42 playlists_synced=3 duration_ms=1234 success=true error=""
```

### Querying with jq

```bash
# All sync metrics for navidrome
cat spotter.log | jq 'select(.msg == "metric.sync" and .provider == "navidrome")'

# Average LLM token usage per operation
cat spotter.log | jq 'select(.msg == "metric.llm") | {operation, tokens_used}'

# Track matching success rate by strategy
cat spotter.log | jq 'select(.msg == "metric.track_match") | {strategy, matched}'

# Failed enricher runs
cat spotter.log | jq 'select(.msg == "metric.enricher" and .success == false)'
```

---

## Scenarios

### Scenario 1: Operator analyzes sync performance

```gherkin
Given SPOTTER_LOG_FORMAT=json is set
And the sync loop has been running for 24 hours
When the operator runs: cat spotter.log | jq 'select(.msg == "metric.sync")'
Then they see one event per provider per user per tick
And each event includes listens_synced, duration_ms, and success
And they can calculate average sync duration and total listens synced per day
```

### Scenario 2: Operator investigates high token usage

```gherkin
Given SPOTTER_LOG_FORMAT=json is set
And multiple users have been generating mixtapes
When the operator runs: cat spotter.log | jq 'select(.msg == "metric.llm") | .tokens_used' | awk '{sum+=$1} END {print sum}'
Then they see total token consumption across all LLM operations
And they can break down usage by operation type to identify the most expensive feature
```

### Scenario 3: Text format backward compatibility

```gherkin
Given SPOTTER_LOG_FORMAT is not set (or set to "text")
When the application starts and runs background jobs
Then all metric events are emitted in slog TextHandler format
And existing log parsing scripts continue to work
And the only difference from pre-observability output is the addition of new metric.* log lines
```

### Scenario 4: Track matching strategy effectiveness

```gherkin
Given SPOTTER_LOG_FORMAT=json is set
And a playlist sync has matched 100 tracks
When the operator queries metric.track_match events
Then they can determine: 60 matched via ISRC, 25 via exact match, 10 via fuzzy match, 5 unmatched
And they can see the average confidence score for fuzzy matches
And they can identify which provider pairs have the lowest match rates
```

---

## Implementation Notes

- Logger initialization: `cmd/server/main.go:38-41` — add `SPOTTER_LOG_FORMAT` check and handler selection
- Config addition: `internal/config/config.go` — add `Log.Format` field with Viper binding to `SPOTTER_LOG_FORMAT`
- Request middleware: `internal/middleware/logging.go:18` — change message to `metric.request`, add `duration_ms`
- Sync instrumentation: `internal/services/syncer.go` — emit `metric.sync` after each provider sync
- Enricher instrumentation: `internal/services/metadata.go` — emit `metric.enricher` after each enricher run
- Background tick: `cmd/server/main.go` — emit `metric.background_tick` after each loop iteration
- LLM instrumentation: `internal/vibes/generator.go`, `internal/vibes/enhancer.go`, `internal/services/similar_artists.go` — emit `metric.llm` after each API call
- Track matching: `internal/services/` (track matching code) — emit `metric.track_match` per matching attempt
- Governing comment: `// Governing: ADR-0019 (structured metrics), ADR-0010 (slog), SPEC observability`
