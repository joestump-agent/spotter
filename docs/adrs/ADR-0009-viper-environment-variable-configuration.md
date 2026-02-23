---
status: accepted
date: 2026-02-21
decision-makers: joestump
---

# ADR-0009: Viper with SPOTTER_* Environment Variables over Raw os.Getenv for Configuration Management

## Context and Problem Statement

Spotter requires extensive configuration: database connection, server binding, Navidrome and Lidarr URLs, OAuth credentials for Spotify and Last.fm, OpenAI API settings, metadata enrichment options, playlist sync behavior, vibes generation tuning, security keys, and theme preferences. This configuration must work seamlessly in Docker containers (where environment variables are the standard), in local development (where `.env` files are convenient), and in production (where secrets management systems inject environment variables). How should the application load, validate, and provide typed access to this diverse configuration?

## Decision Drivers

* Configuration includes 40+ settings spanning security, database, server, external services, AI, and UI
* The application is designed for containerized deployment — environment variables are the 12-factor app standard
* Configuration values span multiple types: strings (URLs, API keys), integers (ports, track limits), floats (temperatures, confidence thresholds), booleans (feature flags), and durations
* Nested configuration groups (e.g., `metadata.images.max_width`) map naturally to structured environment variables
* Sensible defaults are critical — users should only need to set a handful of required values to get started
* Configuration must fail fast with clear error messages when required values are missing

## Considered Options

* **Viper with `SPOTTER_*` environment variables** — Viper library with `SetEnvPrefix("SPOTTER")`, automatic env binding, typed defaults, and struct unmarshaling
* **Raw `os.Getenv()` calls** — standard library only, manual parsing and validation per variable
* **YAML config file only** — load all configuration from a `config.yaml` file
* **kelseyhightower/envconfig** — lightweight struct-tag-based environment variable binding

## Decision Outcome

Chosen option: **Viper with `SPOTTER_*` environment variables**, because Viper provides automatic environment variable binding with the `SPOTTER_` prefix, type-safe defaults via `SetDefault()`, nested configuration support through dot-separated keys that map to underscore-separated environment variables (e.g., `openai.base_url` maps to `SPOTTER_OPENAI_BASE_URL`), and unmarshaling into a strongly-typed `Config` struct. This gives operators a consistent, discoverable configuration interface while keeping the code DRY — one `Config` struct definition in `internal/config/config.go` serves as both the schema and the documentation.

### Consequences

* Good, because all configuration is centralized in a single `Config` struct with `mapstructure` tags — the struct is the schema
* Good, because `SetEnvPrefix("SPOTTER")` namespaces all variables, preventing collisions with other applications in the same environment
* Good, because `SetEnvKeyReplacer(strings.NewReplacer(".", "_"))` maps nested struct fields to flat environment variables automatically
* Good, because `SetDefault()` calls define sensible defaults for every optional setting — users only need to set required values
* Good, because `v.Unmarshal(&cfg)` produces a typed struct — no string-to-type conversion scattered across the codebase
* Good, because the `Load()` function validates required fields and returns clear error messages (e.g., `"navidrome.base_url is required"`)
* Bad, because Viper treats empty string environment variables as "set", overriding defaults — requires post-unmarshal fixup logic for `OpenAI.BaseURL` and `OpenAI.Model`
* Bad, because Viper is a relatively large dependency with transitive dependencies (fsnotify, mapstructure, pflag, afero, etc.)

### Confirmation

Compliance is confirmed by checking that `internal/config/config.go` uses `viper.New()` with `SetEnvPrefix("SPOTTER")` and `AutomaticEnv()`. All configuration access throughout the codebase should go through the `*config.Config` struct — no direct `os.Getenv()` calls for application configuration should exist outside of `config.Load()`.

## Pros and Cons of the Options

### Viper with SPOTTER_* Environment Variables

`config.Load()` in `internal/config/config.go:199` creates a new Viper instance, sets the `SPOTTER` prefix, configures the key replacer for nested fields, registers 40+ defaults via `SetDefault()`, and unmarshals into the `Config` struct. The struct uses `mapstructure` tags for field mapping. Post-unmarshal validation checks required fields (`navidrome.base_url`, `lidarr.base_url`, `lidarr.api_key`, `openai.api_key`, `security.encryption_key`) and returns descriptive errors. Helper methods on `Config` provide computed values: `AvailableThemes()`, `MetadataEnricherOrder()`, `IsOpenAIEnabled()`, `GetVibesModel()`, `GetVibesPromptsDirectory()`, `GetEncryptionKeyBytes()`.

* Good, because `AutomaticEnv()` means any `SPOTTER_*` variable is automatically available without explicit binding per key
* Good, because defaults are co-located with the loading logic — `v.SetDefault("vibes.temperature", 0.8)` makes the default discoverable
* Good, because nested structs map cleanly: `Config.OpenAI.BaseURL` reads from `SPOTTER_OPENAI_BASE_URL`
* Good, because configuration is loaded once in `cmd/server/main.go:44` and the `*Config` pointer is passed to all services via dependency injection
* Good, because `.env` file support via Viper's `gotenv` integration works out of the box for local development
* Neutral, because the `Config` struct at 116 lines is large but reflects genuine configuration complexity
* Bad, because Viper's empty-string override behavior requires manual fixup at lines 271-276 for `OpenAI.BaseURL` and `OpenAI.Model`
* Bad, because Viper adds 10+ transitive dependencies (visible in `go.mod` lines 23-45)

### Raw os.Getenv

Use Go's standard library `os.Getenv()` and `os.LookupEnv()` for each configuration value, with manual type conversion via `strconv`.

* Good, because zero external dependencies — pure standard library
* Good, because explicit and easy to understand — each variable is loaded individually
* Bad, because 40+ `os.Getenv()` calls with manual `strconv.Atoi()`, `strconv.ParseFloat()`, and `strconv.ParseBool()` conversions
* Bad, because default values must be implemented with repetitive `if value == "" { value = "default" }` patterns
* Bad, because no automatic mapping between nested struct fields and environment variables
* Bad, because no structured validation — each field requires its own error handling

### YAML Config File Only

Load all configuration from a `config.yaml` or `config.toml` file using Viper's file-reading capabilities or a YAML parser.

* Good, because YAML supports nested configuration naturally with clear visual hierarchy
* Good, because a single file documents all available settings
* Bad, because not container-friendly — requires mounting a config file into the container
* Bad, because secrets (API keys, encryption keys) would be stored in a plaintext file
* Bad, because does not follow 12-factor app principles — environment variables are the standard for containerized applications
* Bad, because complicates deployment in orchestrators (Kubernetes, Docker Compose) where env vars are the native configuration mechanism

### kelseyhightower/envconfig

Use `kelseyhightower/envconfig` for struct-tag-based environment variable binding.

* Good, because lighter than Viper — single dependency with minimal overhead
* Good, because struct tags (`envconfig:"BASE_URL"`) are concise and declarative
* Good, because built-in support for required fields, default values, and custom decoders
* Bad, because does not support `.env` files without an additional library
* Bad, because flat namespace — no nested struct support without manual prefixing
* Bad, because less community adoption in Go ecosystem compared to Viper — fewer examples and integrations

## More Information

* Configuration loading: `internal/config/config.go:199-309` — `Load()` function with Viper setup, defaults, unmarshaling, and validation
* Config struct definition: `internal/config/config.go:48-116` — all configuration fields with `mapstructure` tags and doc comments
* Helper methods: `internal/config/config.go:118-178` — `AvailableThemes()`, `MetadataEnricherOrder()`, `IsOpenAIEnabled()`, `GetVibesModel()`, `GetVibesPromptsDirectory()`
* Config initialization: `cmd/server/main.go:44` — `config.Load()` called once, result passed to all services
* Environment variable examples: `.env.example` — documents required and optional `SPOTTER_*` variables
* Viper dependency: `go.mod:11` — `github.com/spf13/viper v1.21.0`
* Related: ADR-0008 (OpenAI configuration that leverages these env vars)
