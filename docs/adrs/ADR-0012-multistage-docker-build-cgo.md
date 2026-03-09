---
status: accepted
date: 2026-02-21
decision-makers: joestump
---

# ADR-0012: Multi-Stage Docker Build with Golang and Node.js Builder over Single-Stage or External CI Pipelines

## Context and Problem Statement

Spotter's build has two non-trivial requirements: the Go binary requires CGO for the `go-sqlite3` driver ([ADR-0003](./ADR-0003-sqlite-embedded-database.md)), and the UI requires Node.js to compile Tailwind CSS with DaisyUI ([ADR-0011](./ADR-0011-tailwind-daisyui-ui-styling.md)). Additionally, Templ templates must be code-generated before Go compilation. How should Docker packaging handle these heterogeneous build dependencies while keeping the final runtime image small and self-contained?

## Decision Drivers

* CGO must be enabled (`CGO_ENABLED=1`) for `go-sqlite3` — the Go binary cannot be statically linked with `CGO_ENABLED=0`
* Node.js 20 is required to run `npx tailwindcss` for CSS compilation and to install DaisyUI, Iconify, and other npm packages
* Templ code generation (`templ generate`) must run before Go compilation
* Ent ORM code generation (`go generate ./ent`) must also run before Go compilation
* The final Docker image should be as small as possible — no Go compiler, no Node.js, no npm packages at runtime
* The build must be self-contained — `docker build` alone should produce a runnable image with no external CI steps required

## Considered Options

* **Multi-stage build with golang + Node.js builder, slim runtime** — single Dockerfile with builder stage installing both toolchains, runtime stage using debian:bookworm-slim
* **Single-stage golang image** — one stage that includes Go, Node.js, and all build tools in the final image
* **Pure Go binary with alternative SQLite driver** — replace `go-sqlite3` (CGO) with a pure-Go SQLite implementation to eliminate CGO requirement
* **Separate CSS build pipeline outside Docker** — pre-compile CSS in CI, copy artifacts into a simpler Docker build

## Decision Outcome

Chosen option: **Multi-stage build with golang + Node.js builder, slim runtime**, because it produces a minimal runtime image (~150MB debian:bookworm-slim + binary + static assets) while accommodating both CGO compilation and Node.js CSS compilation in a single self-contained `docker build` command. The builder stage starts from `golang:1.24`, installs Node.js 20 via NodeSource, then executes the full build pipeline: `make docker-deps` (Go modules + templ tool + npm packages), `make generate` (Ent + Templ code gen), `make css` (Tailwind compilation), and `make build-binary` (CGO-enabled Go build). The runtime stage copies only the compiled binary and `static/` directory to `debian:bookworm-slim` with CA certificates.

### Consequences

* Good, because the final runtime image contains only the binary, static assets, and CA certificates — no Go compiler, no Node.js, no npm packages, no source code
* Good, because `debian:bookworm-slim` runtime provides the C library (`libc`) required by the CGO-linked binary — alpine would require musl compatibility workarounds
* Good, because Docker layer caching on `COPY go.mod go.sum package.json package-lock.json* Makefile` means dependency installation is cached when only source code changes
* Good, because the entire build is self-contained — `docker build -t spotter .` produces a runnable image with no prerequisites beyond Docker itself
* Good, because Makefile targets (`docker-deps`, `generate`, `css`, `build-binary`) are shared between Docker builds and local development — no duplicated build logic
* Bad, because the builder stage is large (~2GB with golang:1.24 + Node.js + npm packages) — build times are longer and CI disk usage is higher
* Bad, because installing Node.js via `curl | bash` in the Dockerfile fetches from NodeSource on every uncached build — adds network dependency and build time
* Bad, because debian:bookworm-slim is larger than alpine or scratch — but required for CGO compatibility

### Confirmation

Compliance is confirmed by `Dockerfile` containing exactly two `FROM` directives: `FROM golang:1.24 AS builder` and `FROM debian:bookworm-slim`. The builder stage must install Node.js, run code generation (`make generate`), compile CSS (`make css`), and build the binary with `CGO_ENABLED=1` (`make build-binary`). The runtime stage must contain only `COPY --from=builder` directives for the binary and static assets, plus `ca-certificates` installation. No Go, Node.js, or npm tools should be present in the runtime stage.

## Pros and Cons of the Options

### Multi-Stage Build with Golang + Node.js Builder, Slim Runtime

Two-stage Dockerfile. Builder stage: `golang:1.24` base, installs Node.js 20 via NodeSource `setup_20.x`, copies dependency manifests first for layer caching, runs `make docker-deps` → `make generate` → `make css` → `make build-binary`. Runtime stage: `debian:bookworm-slim`, installs `ca-certificates`, copies binary (`spotter-server`) and `static/` directory from builder.

* Good, because clear separation of build-time and runtime concerns — build tools never ship to production
* Good, because `make docker-deps` target installs exactly the tools needed: Go modules, `templ` CLI, and npm packages — no development-only tools like `air`, `golangci-lint`, or `concurrently`
* Good, because the Makefile `build-binary` target explicitly sets `CGO_ENABLED=1` ensuring the CGO requirement is documented and enforced
* Good, because dependency files are copied before source code (`COPY go.mod go.sum package.json package-lock.json* Makefile ./`), enabling Docker layer caching for the expensive `make docker-deps` step
* Neutral, because the builder image size (~2GB) is discarded after build — only the slim runtime image is tagged and pushed
* Bad, because NodeSource `setup_20.x` script pins to Node.js 20.x — major Node.js upgrades require Dockerfile changes
* Bad, because the builder stage runs four sequential `make` targets — no parallelism within the Docker build

### Single-Stage Golang Image

One `FROM golang:1.24` with Node.js installed, building and running in the same image.

* Good, because simpler Dockerfile — single stage, no `COPY --from=builder` directives
* Good, because easier to debug — all build tools available in the running container
* Bad, because the final image includes the Go compiler (~800MB), Node.js (~100MB), npm packages, and all source code
* Bad, because image size would be ~2GB+ instead of ~150MB — dramatically increases pull time, storage costs, and attack surface
* Bad, because build tools in production create unnecessary security exposure

### Pure Go Binary with Alternative SQLite Driver

Replace `mattn/go-sqlite3` (CGO) with a pure-Go SQLite implementation like `modernc.org/sqlite` to eliminate the CGO build requirement entirely.

* Good, because `CGO_ENABLED=0` enables fully static binaries — could use `scratch` or `alpine` as the runtime base
* Good, because eliminates the need for a C compiler in the builder stage
* Good, because cross-compilation becomes trivial without CGO
* Bad, because would require replacing the SQLite driver across the entire codebase — contradicts [ADR-0003](./ADR-0003-sqlite-embedded-database.md) which chose `go-sqlite3`
* Bad, because `modernc.org/sqlite` has different performance characteristics and compatibility guarantees than the canonical C SQLite via `go-sqlite3`
* Bad, because the Ent ORM integration is tested with `go-sqlite3` — switching drivers introduces migration risk

### Separate CSS Build Pipeline Outside Docker

Pre-compile Tailwind CSS in a CI step before Docker build, then copy the pre-built `output.css` into a simpler Go-only Dockerfile.

* Good, because the Dockerfile becomes simpler — no Node.js installation needed
* Good, because CSS compilation can be parallelized with Go compilation in CI
* Bad, because `docker build` is no longer self-contained — requires external CI orchestration to produce the CSS artifact first
* Bad, because local development and Docker builds diverge — developers must remember to build CSS separately before running `docker build`
* Bad, because the pre-built CSS must be committed to the repository or passed as a build artifact — either pollutes version control or adds CI artifact management complexity
* Bad, because the Tailwind content scanning step (`tailwind.config.js` content paths) must be run in an environment with access to all `.templ` and `.go` files — moving it outside Docker means duplicating the source context

## More Information

* Dockerfile: `Dockerfile` — two-stage build definition
* Builder base: `golang:1.24` with Node.js 20 via NodeSource
* Runtime base: `debian:bookworm-slim` with `ca-certificates`
* Docker build target: `Makefile:161-164` — `docker build -t spotter .`
* Docker run target: `Makefile:166-168` — `docker run -p 8080:8080 --env-file .env spotter`
* Docker dependency target: `Makefile:42-49` — `make docker-deps` installs Go modules, templ CLI, and npm packages
* Build binary target: `Makefile:68-71` — `CGO_ENABLED=1 go build -o $(BINARY_NAME) $(MAIN_PATH)`
* CSS build target: `Makefile:58-61` — `npx tailwindcss -i ./static/css/input.css -o ./static/css/output.css --minify`
* Code generation target: `Makefile:51-56` — `go generate ./ent` and `templ generate`
* CGO requirement: see [ADR-0003](./ADR-0003-sqlite-embedded-database.md) (SQLite with go-sqlite3)
* Tailwind + DaisyUI: see [ADR-0011](./ADR-0011-tailwind-daisyui-ui-styling.md)
