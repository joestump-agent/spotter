---
status: accepted
date: 2026-02-21
decision-makers: joestump
---

# ADR-0013: Native Goroutines with time.Ticker over External Job Schedulers for Background Task Execution

## Context and Problem Statement

Spotter runs three periodic background operations: syncing listen history and playlists from music providers, enriching catalog metadata from multiple external APIs, and writing playlists back to Navidrome. These jobs must run on configurable intervals, iterate over all users, and spawn concurrent per-user work. How should Spotter schedule and execute these recurring background tasks?

## Decision Drivers

* Three distinct background loops with different default intervals: listen/playlist sync (5 minutes), metadata enrichment (1 hour), Navidrome playlist write-back (1 hour)
* Each loop must iterate over all users and spawn concurrent per-user goroutines for parallelism
* Intervals are user-configurable via environment variables (`SPOTTER_SYNC_INTERVAL`, `SPOTTER_METADATA_INTERVAL`, `SPOTTER_PLAYLIST_SYNC_SYNC_INTERVAL`)
* Spotter is deployed as a single instance alongside a personal Navidrome server — no multi-instance coordination needed
* Background jobs are fire-and-forget — if one iteration fails, it logs the error and retries on the next tick
* Adding an external job queue or scheduler would require operating additional infrastructure (Redis, PostgreSQL, etc.)

## Considered Options

* **Native goroutines + time.NewTicker** — Go standard library ticker-based loops spawning per-user goroutines
* **Redis-based job queue (Asynq, Machinery)** — external queue with persistent retry, scheduling, and distributed workers
* **Cron library (robfig/cron)** — in-process cron scheduler with crontab-style expressions
* **Database polling with scheduled tasks** — store pending tasks in SQLite, poll on an interval, mark completed

## Decision Outcome

Chosen option: **Native goroutines + time.NewTicker**, because it requires zero external dependencies, uses Go's built-in concurrency primitives, and provides exactly the scheduling granularity Spotter needs. Each background loop is a goroutine launched in `cmd/server/main.go` with a `time.NewTicker` configured from the parsed `time.Duration` of the environment variable. Per-user work is spawned as sub-goroutines via `go func(user *ent.User) { ... }(u)`. Failed iterations log the error and are naturally retried on the next tick — no retry backoff or dead-letter queue is needed for personal-use sync operations.

### Consequences

* Good, because zero additional dependencies — only `time`, `context`, and `log/slog` from the standard library
* Good, because intervals are parsed from Go duration strings (e.g., `"5m"`, `"1h"`, `"30m"`) via `time.ParseDuration` with sensible defaults on parse failure
* Good, because per-user goroutines provide natural parallelism — users' sync operations do not block each other
* Good, because the metadata enrichment loop includes a 30-second startup delay and runs immediately on first tick, ensuring fresh data shortly after boot without overwhelming the system on startup
* Good, because the playlist sync loop includes a 1-minute startup delay, staggered from the metadata loop to avoid thundering herd on startup
* Good, because each loop follows the same pattern: parse duration → log config → start goroutine → `defer ticker.Stop()` → `for range ticker.C` → query users → spawn per-user goroutines
* Bad, because no persistence — if the process restarts mid-sync, any incomplete work is lost and must be redone on the next tick
* Bad, because no distributed coordination — running multiple Spotter instances would result in duplicate sync operations (consistent with single-instance deployment model per ADR-0003)
* Bad, because per-user goroutines are unbounded — a deployment with many users could spawn excessive concurrent goroutines, though this is unlikely for a personal music server

### Confirmation

Compliance is confirmed by `cmd/server/main.go` containing three `go func() { ... }()` blocks, each using `time.NewTicker`. No imports of `robfig/cron`, `asynq`, `machinery`, or similar scheduler libraries should appear in `go.mod`. The three intervals must be sourced from `cfg.Sync.Interval`, `cfg.Metadata.Interval`, and `cfg.PlaylistSync.SyncInterval` respectively, each parsed with `time.ParseDuration` and falling back to a hardcoded default on error.

## Pros and Cons of the Options

### Native Goroutines + time.NewTicker

Three goroutines launched in `main()`. Each creates a `time.NewTicker` with a configurable duration. On each tick, the loop queries all users from the database and spawns a per-user goroutine for the actual work. Errors are logged via `slog` and the loop continues to the next tick.

**Loop 1 — Listen/Playlist Sync** (`cmd/server/main.go:123-141`):
Interval from `cfg.Sync.Interval` (default `"5m"`). Calls `syncer.Sync(ctx, user)` per user, which synchronizes listen history and playlists from all registered providers (Navidrome, Spotify, Last.fm).

**Loop 2 — Metadata Enrichment** (`cmd/server/main.go:144-167`):
Interval from `cfg.Metadata.Interval` (default `"1h"`). Gated by `cfg.Metadata.Enabled`. Includes 30-second startup delay, then runs immediately before entering the ticker loop. Calls `runMetadataSync()` helper which spawns per-user goroutines calling `metadataSvc.SyncAll(ctx, user)`.

**Loop 3 — Playlist Sync to Navidrome** (`cmd/server/main.go:172-200`):
Interval from `cfg.PlaylistSync.SyncInterval` (default `"1h"`). Includes 1-minute startup delay. Calls `playlistSyncSvc.SyncAllEnabledPlaylists(ctx, user.ID)` per user, writing Spotter-managed playlists back to Navidrome.

* Good, because the pattern is transparent — a developer reading `main.go` can see all three loops, their intervals, and what they do without consulting external configuration files
* Good, because `defer ticker.Stop()` ensures clean shutdown of each ticker when the goroutine exits
* Good, because `time.ParseDuration` supports flexible interval formats — seconds, minutes, hours (e.g., `"30s"`, `"5m"`, `"2h"`)
* Good, because the metadata loop's conditional gate (`if cfg.Metadata.Enabled`) allows metadata enrichment to be entirely disabled without affecting the other two loops
* Neutral, because staggered startup delays (30s for metadata, 1m for playlist sync) are hardcoded — not configurable, but sensible for preventing startup storms
* Bad, because no observability — there are no metrics for tick count, execution duration, or failure rate beyond structured log messages
* Bad, because unbounded per-user concurrency — `go func(user *ent.User)` spawns one goroutine per user with no worker pool or semaphore

### Redis-Based Job Queue (Asynq, Machinery)

External Redis instance as a persistent job queue. Background jobs are enqueued as tasks, dequeued by workers, with automatic retry, dead-letter queues, and scheduled execution.

* Good, because persistent retry — failed jobs are retried with configurable backoff without waiting for the next tick interval
* Good, because dead-letter queue — permanently failing jobs are captured for investigation
* Good, because distributed — multiple Spotter instances can share one Redis queue with automatic deduplication
* Good, because scheduling flexibility — cron expressions, delayed execution, unique job constraints
* Bad, because requires running a Redis server alongside Spotter — contradicts the single-binary, minimal-infrastructure deployment model
* Bad, because adds `asynq` or `machinery` dependency with its own configuration, serialization format, and error handling patterns
* Bad, because Redis is another service to monitor, backup, and maintain — excessive for a personal music server
* Bad, because job payloads must be serialized to JSON/protobuf — losing Go type safety on user structs

### Cron Library (robfig/cron)

In-process cron scheduler using `robfig/cron` or similar library. Jobs are registered with crontab expressions and executed by the library's internal scheduler.

* Good, because crontab expressions provide fine-grained scheduling (e.g., `"*/5 * * * *"` for every 5 minutes, `"0 */2 * * *"` for every 2 hours)
* Good, because the library handles timing, overlapping execution prevention, and logging
* Good, because in-process — no external infrastructure required
* Bad, because adds an external dependency for functionality achievable with `time.NewTicker` in ~10 lines of code
* Bad, because crontab expressions are less intuitive than Go duration strings (`"5m"`) for simple interval-based scheduling
* Bad, because the library's job registration API adds abstraction that obscures the direct relationship between config and execution
* Neutral, because `robfig/cron` is mature and widely used, but the added complexity is not justified for three simple interval loops

### Database Polling with Scheduled Tasks

Store pending background tasks in a `scheduled_tasks` SQLite table. A single polling goroutine queries for tasks due for execution and processes them.

* Good, because task state survives process restarts — incomplete tasks can be resumed
* Good, because uses existing SQLite infrastructure — no new external service
* Good, because task history provides built-in audit trail of what ran and when
* Bad, because polling interval introduces scheduling latency (1-5 seconds between polls)
* Bad, because frequent polling creates unnecessary SQLite read load — counter to the single-writer model (ADR-0003)
* Bad, because requires a new database schema (`scheduled_tasks` table), migration, and cleanup logic
* Bad, because marking tasks as completed, handling retries, and preventing duplicate execution adds significant complexity compared to a simple ticker loop

## More Information

* Background loop implementation: `cmd/server/main.go:116-200` — three goroutine blocks with `time.NewTicker`
* Metadata sync helper: `cmd/server/main.go:344-358` — `runMetadataSync()` function spawning per-user goroutines
* Sync interval config: `internal/config/config.go:69` — `Sync.Interval` (default `"5m"`, env `SPOTTER_SYNC_INTERVAL`)
* Metadata interval config: `internal/config/config.go:94` — `Metadata.Interval` (default `"1h"`, env `SPOTTER_METADATA_INTERVAL`)
* Playlist sync interval config: `internal/config/config.go:13` — `PlaylistSync.SyncInterval` (default `"1h"`, env `SPOTTER_PLAYLIST_SYNC_SYNC_INTERVAL`)
* Viper config defaults: `internal/config/config.go:211,234,254` — `v.SetDefault("sync.interval", "5m")`, `v.SetDefault("playlist_sync.sync_interval", "1h")`, `v.SetDefault("metadata.interval", "1h")`
* Syncer service: `internal/services/` — `NewSyncer`, `NewPlaylistSyncService`, `NewMetadataService`
* Single-instance deployment constraint: see ADR-0003 (SQLite)
* Event bus for background job notifications: see ADR-0007 (in-memory event bus)
