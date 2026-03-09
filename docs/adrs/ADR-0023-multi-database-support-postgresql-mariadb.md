---
status: accepted
date: 2026-02-22
decision-makers: joestump
supersedes: ADR-0003
---

# ADR-0023: Add PostgreSQL and MariaDB Support Alongside SQLite

## Context and Problem Statement

ADR-0003 chose SQLite as the embedded database for Spotter's initial personal-use deployment model. While SQLite remains appropriate for single-user homelab setups, Spotter is gaining multi-user deployments where SQLite's serialized write model creates contention: concurrent background tickers (sync, metadata, playlist sync) compete for the write lock, and users with large libraries experience noticeable slowdowns. Additionally, operators who already run PostgreSQL or MariaDB in their homelab want to integrate Spotter into their existing database infrastructure rather than manage a separate SQLite volume.

## Decision Drivers

* Multi-user deployments experience write contention under SQLite's serialized write model
* Homelab operators with existing PostgreSQL or MariaDB infrastructure prefer to reuse it
* The existing `SPOTTER_DATABASE_DRIVER` / `SPOTTER_DATABASE_SOURCE` config keys already express the intent for multi-driver support
* Ent ORM already handles all three dialects (`dialect.SQLite`, `dialect.Postgres`, `dialect.MySQL`) — only the driver package imports are missing
* SQLite must remain fully supported as the zero-infrastructure default for single-user deployments

## Considered Options

* **Add PostgreSQL (`lib/pq`) + MariaDB (`go-sql-driver/mysql`) alongside SQLite** — all three drivers compiled in, operator selects via `SPOTTER_DATABASE_DRIVER`
* **Replace SQLite with PostgreSQL as the only driver** — breaking change, loses single-container simplicity
* **Add PostgreSQL only** — leaves out MariaDB operators

## Decision Outcome

Chosen option: **Add all three drivers**, because it preserves the zero-infrastructure SQLite default while enabling PostgreSQL and MariaDB for operators who want them. Ent ORM already abstracts the dialect differences; the code change is minimal (`internal/database/db.go` imports + startup validation).

### Drivers Selected

| Database | Go Driver | Reason |
|----------|-----------|--------|
| SQLite | `github.com/mattn/go-sqlite3` (existing) | Embedded, zero-ops, personal use default |
| PostgreSQL | `github.com/lib/pq` | Pure Go-compatible, stable, well-tested with Ent |
| MariaDB / MySQL | `github.com/go-sql-driver/mysql` | Standard MySQL protocol driver, works with MariaDB 10.x+ |

### Consequences

* Good, because SQLite remains the default — single-container deployments require no changes
* Good, because PostgreSQL and MariaDB operators can point Spotter at their existing infrastructure with two env var changes
* Good, because Ent's `Schema.Create()` handles DDL migration for all three dialects
* Good, because `lib/pq` and `go-sql-driver/mysql` are pure Go — no CGO required for PostgreSQL/MariaDB deployments
* Bad, because CGO (`go-sqlite3`) is still required when using the SQLite driver — multi-stage Docker build remains necessary
* Bad, because `SPOTTER_DATABASE_SOURCE` format differs per driver — operators must supply the correct DSN format
* Neutral, because all three drivers are compiled into the binary regardless of which is configured at runtime (minimal binary size impact)

### Confirmation

Compliance is confirmed by `go.mod` containing `github.com/lib/pq` and `github.com/go-sql-driver/mysql`, blank imports of both in `internal/database/db.go`, and `internal/config/config.go` validating the driver value against `["sqlite3", "postgres", "mysql"]`.

## Default Connection Strings by Driver

| Driver | `SPOTTER_DATABASE_DRIVER` | Default `SPOTTER_DATABASE_SOURCE` |
|--------|--------------------------|----------------------------------|
| SQLite | `sqlite3` | `file:spotter.db?cache=shared&_fk=1` |
| PostgreSQL | `postgres` | `host=localhost port=5432 dbname=spotter sslmode=disable` |
| MariaDB/MySQL | `mysql` | `spotter:spotter@tcp(localhost:3306)/spotter?parseTime=true&charset=utf8mb4` |

## More Information

* Ent dialect constants: `entgo.io/ent/dialect` — `dialect.SQLite`, `dialect.Postgres`, `dialect.MySQL`
* Spec: `docs/openspec/specs/multi-database-support/spec.md`
* Previous decision: [ADR-0003](./ADR-0003-sqlite-embedded-database.md) (superseded by this ADR)
* ORM choice: [ADR-0004](./ADR-0004-ent-orm-code-generation.md) (Ent ORM — unchanged)
