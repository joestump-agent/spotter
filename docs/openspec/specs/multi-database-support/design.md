# Design: Multi-Database Support

## Context

Spotter originally used SQLite as its sole database backend (ADR-0003), chosen for its
zero-infrastructure simplicity in single-user homelab deployments. As Spotter gained
multi-user deployments, SQLite's serialized write model created contention between three
concurrent background tickers (sync, metadata enrichment, playlist sync) competing for
the write lock. Operators running existing PostgreSQL or MariaDB infrastructure wanted
to integrate Spotter into their database stack rather than manage a separate SQLite volume.

This design adds PostgreSQL and MariaDB/MySQL as selectable database backends while
preserving SQLite as the zero-infrastructure default. The implementation leverages Ent ORM's
multi-dialect support — Ent already handles DDL generation for all three databases, so the
code change is minimal: driver imports, config validation, and default DSN logic.

Governing ADRs:
[ADR-0023](../../adrs/ADR-0023-multi-database-support-postgresql-mariadb.md) (multi-database support — supersedes ADR-0003),
[ADR-0004](../../adrs/ADR-0004-ent-orm-code-generation.md) (Ent ORM code generation),
[ADR-0009](../../adrs/ADR-0009-viper-environment-variable-configuration.md) (Viper configuration).

## Goals / Non-Goals

### Goals

- Three database backends selectable at runtime: SQLite, PostgreSQL, MariaDB/MySQL
- Two environment variables control selection: `SPOTTER_DATABASE_DRIVER` and `SPOTTER_DATABASE_SOURCE`
- No code changes or recompilation required to switch backends
- All three drivers compiled into the single binary (blank imports)
- Driver validation at config load time with clear error messages
- Sensible default DSN per driver when `SPOTTER_DATABASE_SOURCE` is not set
- Schema migration via `client.Schema.Create(ctx)` for all dialects (Ent handles DDL)
- Docker Compose example files for PostgreSQL and MariaDB deployments
- SQLite remains the default for backward compatibility

### Non-Goals

- Connection pooling configuration (max connections, idle timeout) — deferred
- Production migration tooling (Atlas, Goose) — `Schema.Create()` is sufficient for now
- Read replicas, multi-master setups, or sharding
- CockroachDB, TiDB, or other PostgreSQL-compatible databases
- Data migration between database backends (users start fresh when switching)

## Decisions

### All Drivers Compiled In vs. Build Tags

**Choice**: All three database drivers are unconditionally imported via blank imports in
`internal/database/db.go`. The operator selects the active driver at runtime.

**Rationale**: A single binary simplifies distribution — the same Docker image works for
all three backends. The binary size impact of unused drivers is negligible (~2-3 MB).
Build tags would require separate binaries per database, complicating CI/CD and container
images.

**Alternatives considered**:
- Build tags (`//go:build postgres`): separate binaries per driver, complicates distribution
- Dynamic driver loading at runtime: Go does not support this cleanly without CGO plugins
- Single driver (PostgreSQL only): loses SQLite simplicity for single-user deployments

### Driver Selection via Config Validation

**Choice**: `config.Load()` validates `database.driver` against a whitelist (`sqlite3`,
`postgres`, `mysql`) and returns an error for unrecognized values before any database
connection is attempted.

**Rationale**: Fail-fast validation prevents confusing database connection errors from
reaching the user. The error message explicitly lists valid options.

**Alternatives considered**:
- Let `ent.Open()` fail with the driver's error: less user-friendly, harder to diagnose
- Accept any driver string: would allow typos that produce cryptic errors

### Driver-Specific Default Source Strings

**Choice**: When `SPOTTER_DATABASE_SOURCE` is not set (or equals the SQLite default),
the config applies a driver-appropriate default DSN.

**Rationale**: Operators switching from SQLite to PostgreSQL should only need to set
`SPOTTER_DATABASE_DRIVER=postgres` for a localhost development setup. Production
deployments will always set `SPOTTER_DATABASE_SOURCE` explicitly.

**Alternatives considered**:
- Require explicit `SPOTTER_DATABASE_SOURCE` for non-SQLite drivers: more typing for
  the common localhost case
- No defaults for any driver: breaks backward compatibility for SQLite users

## Architecture

### Driver Registration and Selection

```mermaid
flowchart TD
    ENV["Environment Variables<br/>SPOTTER_DATABASE_DRIVER<br/>SPOTTER_DATABASE_SOURCE"]
    CFG["config.Load()"]
    VAL{"Driver valid?<br/>sqlite3 / postgres / mysql"}
    DSN["Apply default DSN<br/>(if source not set)"]
    ERR["Return error:<br/>unsupported driver"]
    DB["database.NewClient(driver, source, encryptor)"]
    ENT["ent.Open(driver, source)"]
    SCHEMA["client.Schema.Create(ctx)<br/>DDL migration"]
    HOOK["RegisterEncryptionHooks()"]
    TAGS["CreateEntityTagsTable()"]

    ENV --> CFG
    CFG --> VAL
    VAL -->|No| ERR
    VAL -->|Yes| DSN
    DSN --> DB
    DB --> ENT
    ENT --> SCHEMA
    SCHEMA --> HOOK
    HOOK --> TAGS
```

### Driver-to-DSN Mapping

```mermaid
erDiagram
    SQLITE {
        string driver "sqlite3"
        string default_source "file:spotter.db?cache=shared&_fk=1"
        string go_driver "github.com/mattn/go-sqlite3"
        bool requires_cgo "true"
    }
    POSTGRES {
        string driver "postgres"
        string default_source "host=localhost port=5432 dbname=spotter sslmode=disable"
        string go_driver "github.com/lib/pq"
        bool requires_cgo "false"
    }
    MYSQL {
        string driver "mysql"
        string default_source "spotter:spotter@tcp(localhost:3306)/spotter?parseTime=true&charset=utf8mb4"
        string go_driver "github.com/go-sql-driver/mysql"
        bool requires_cgo "false"
    }
```

## Key Implementation Details

**Driver registration**: `internal/database/db.go` (75 lines)
- Three blank imports register all drivers at init time:
  ```go
  _ "github.com/go-sql-driver/mysql"
  _ "github.com/lib/pq"
  _ "github.com/mattn/go-sqlite3"
  ```
- `NewClient(driver, source, encryptor)` calls `ent.Open(driver, source)`, runs
  `client.Schema.Create(ctx)` for DDL migration, registers encryption hooks, and
  creates the `entity_tags` denormalized table.
- `driverToStdlib()` maps Ent dialect names to `database/sql` driver names (they
  happen to be identical for all three supported drivers).
- `OpenRawDB(driver, source)` opens a persistent `*sql.DB` for raw SQL operations
  outside the Ent client (used by the tag taxonomy system).

**Config validation**: `internal/config/config.go:349-361`
- Whitelist validation: `validDrivers := map[string]bool{"sqlite3": true, "postgres": true, "mysql": true}`
- Driver-specific default source: checks if the source is empty or equals the SQLite
  default (`file:spotter.db?...`), then applies the appropriate default DSN.
- Default driver: `v.SetDefault("database.driver", "sqlite3")` — SQLite remains default.

**Test coverage**: `internal/config/config_test.go`
- Tests verify valid drivers pass validation, invalid drivers return errors,
  and driver-specific defaults are applied when source is empty.

**Docker Compose examples**:
- `docker-compose.postgres.yml` — Spotter + PostgreSQL with health check and named volume
- `docker-compose.mariadb.yml` — Spotter + MariaDB with health check and named volume

**go.mod dependencies**:
- `github.com/lib/pq` — pure Go PostgreSQL driver
- `github.com/go-sql-driver/mysql` — pure Go MySQL/MariaDB driver
- `github.com/mattn/go-sqlite3` — CGO-based SQLite driver (existing)

## Risks / Trade-offs

- **CGO still required for SQLite** — The SQLite driver (`go-sqlite3`) requires CGO, which
  means the Docker image must use a multi-stage build with CGO-enabled compilation. PostgreSQL
  and MariaDB drivers are pure Go. Operators who only need PostgreSQL/MariaDB could benefit
  from a CGO-free build, but this is not implemented.
- **DSN format differs per driver** — Operators must know the correct DSN format for their
  chosen database. Incorrect DSN formats produce driver-specific errors that may not be
  immediately clear. Mitigation: documentation with example DSN formats for each driver.
- **No data migration between backends** — Switching from SQLite to PostgreSQL requires
  starting with an empty database. Operators with existing SQLite data must manually export
  and import. This is acceptable for a personal application but limits flexibility.
- **Schema.Create() is not production-grade migration** — Ent's `Schema.Create()` performs
  non-destructive auto-migration (add columns/tables, never drop). For complex schema
  changes requiring column renames or data transforms, a proper migration tool (Atlas)
  would be needed. Deferred to a future spec.
- **All drivers in binary regardless of use** — The binary includes all three driver
  packages even though only one is used at runtime. This is a ~2-3 MB binary size cost,
  which is negligible for a containerized application.

## Migration Plan

### Implementation Steps (completed)

1. **go.mod**: Added `github.com/lib/pq` and `github.com/go-sql-driver/mysql` as direct
   dependencies
2. **database/db.go**: Added blank imports for `lib/pq` and `go-sql-driver/mysql`
3. **config.go**: Added `database.driver` validation against `["sqlite3", "postgres", "mysql"]`
4. **config.go**: Added driver-specific default DSN logic for PostgreSQL and MariaDB
5. **config.go**: Set `v.SetDefault("database.driver", "sqlite3")` for backward compatibility
6. **config_test.go**: Added tests for valid/invalid drivers and default DSN application
7. **Docker Compose**: Created `docker-compose.postgres.yml` and `docker-compose.mariadb.yml`
   with health checks, `depends_on` conditions, and named volumes

### Operator Migration Path

To switch from SQLite to PostgreSQL:
1. Set `SPOTTER_DATABASE_DRIVER=postgres`
2. Set `SPOTTER_DATABASE_SOURCE=postgres://user:pass@host:5432/spotter?sslmode=disable`
3. Restart Spotter — `Schema.Create()` creates all tables in PostgreSQL
4. Data from SQLite is not migrated; the library rebuilds via sync and enrichment

## Open Questions

- Should Spotter include a built-in data migration tool for moving between database
  backends? This would be significant engineering effort for a personal application.
- Should connection pooling be configurable (max open connections, max idle connections,
  connection max lifetime)? Currently uses `database/sql` defaults.
- Should the SQLite driver be swappable to a pure-Go alternative (e.g., `modernc.org/sqlite`)
  to eliminate the CGO requirement entirely?
- Should Ent's Atlas migration be adopted for production-grade schema versioning? Currently
  `Schema.Create()` handles all DDL, but it cannot drop columns or rename fields.
