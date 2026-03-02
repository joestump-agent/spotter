---
status: accepted
date: 2026-03-02
decision-makers: Joe Stump
---

# ADR-0027: Image Storage: Local Filesystem

## Context and Problem Statement

Spotter enriches artist and album metadata with cover art downloaded from external providers (Spotify, Fanart.tv, Last.fm, Navidrome). These images are served to the browser via authenticated HTTP routes. Where and how should downloaded images be stored?

## Decision Drivers

* Spotter is a single-instance, self-hosted application — no multi-node concerns
* Images are fetched asynchronously by enricher goroutines and served via HTTP handlers
* The `local_path` DB column already tracks each image's on-disk location
* Minimising external dependencies is a core project principle (no SaaS, no cloud services)
* Docker deployments may lose ephemeral filesystem data on container recreation

## Considered Options

* **Option A**: Local filesystem at `data/images/` relative to process CWD
* **Option B**: Object storage (S3, Wasabi, MinIO)
* **Option C**: Database BLOBs (store image bytes in PostgreSQL)

## Decision Outcome

Chosen option: **Option A** (local filesystem), because it requires zero external dependencies, is the simplest implementation for a single-instance application, and leverages standard filesystem operations for image serving.

Images are stored at `data/images/{artists,albums}/` relative to the process working directory. Each image record in the database stores a relative `local_path` (e.g. `data/images/artists/1-thumbnail.png`). Images are served to authenticated users via auth-gated HTTP routes at `/library/{artist,album}/{id}.png`.

### Consequences

* Good, because no external service dependency — works on any host with a filesystem
* Good, because standard `os.Create` / `os.Stat` operations are fast and well-understood
* Good, because `local_path` in the DB provides a clear mapping from metadata to file
* Good, because images can be inspected, backed up, or cleared using standard filesystem tools
* Bad, because images are ephemeral in Docker deployments without a volume mount for `/app/data`
* Bad, because a stale `local_path` in the DB after container recreation causes broken image references until self-healing runs
* Neutral, because `repairStaleImagePaths()` in `MetadataService.DownloadImages()` provides self-healing — it clears stale paths so re-download is triggered on the next enrichment cycle

### Confirmation

Compliance is confirmed when:
- All image downloads write to `data/images/{artists,albums}/` via `enrichers.DownloadAndSaveImage()`
- The `local_path` DB column stores relative paths (not absolute)
- Images are served through auth-gated `/library/artist/{id}.png` and `/library/album/{id}.png` routes
- Production deployments document the requirement for a volume mount at `/app/data/images`

## Pros and Cons of the Options

### Option A — Local filesystem at `data/images/`

Images are downloaded by enrichers, resized to max 1024px, converted to PNG, and saved to `data/images/{artists,albums}/`. The DB `local_path` column stores the relative path.

* Good, because zero external dependencies — filesystem is always available
* Good, because simplest implementation: `os.MkdirAll` + `os.Create` + `png.Encode`
* Good, because `repairStaleImagePaths()` self-heals after container restarts
* Neutral, because requires volume mount in Docker for persistence across container updates
* Bad, because no built-in redundancy or replication

### Option B — Object storage (S3, Wasabi, MinIO)

Upload images to an S3-compatible object store. Serve via pre-signed URLs or a proxy handler.

* Good, because images survive container recreation without volume mounts
* Good, because object storage provides built-in durability and replication
* Bad, because introduces an external service dependency (S3 client, credentials, endpoint config)
* Bad, because adds network latency to every image save and serve operation
* Bad, because violates the project's minimal-dependency principle for a single-user app

### Option C — Database BLOBs

Store image bytes directly in PostgreSQL as `bytea` columns.

* Good, because images are inherently persisted with the database backup
* Good, because no filesystem path management needed
* Bad, because PostgreSQL is not optimised for serving binary blobs — increases DB size and backup times significantly
* Bad, because requires base64 encoding/decoding overhead or streaming from DB on every HTTP request
* Bad, because image serving cannot leverage OS-level file caching or sendfile optimisations

## More Information

* **Image download**: `internal/enrichers/images.go` — `DownloadAndSaveImage()` handles fetch, resize (max 1024px via `nfnt/resize`), PNG encode, and save
* **Self-healing**: `internal/services/metadata.go` — `repairStaleImagePaths()` clears `local_path` for records where the file no longer exists on disk, called at the start of `DownloadImages()`
* **Image serving**: `internal/handlers/` — auth-gated routes at `/library/artist/{id}.png` and `/library/album/{id}.png`
* **DB schema**: `ent/schema/artist_image.go`, `ent/schema/album_image.go` — `local_path` field (string)
* **Production note**: The container at `ie01.stump.wtf` does NOT have a volume mount for `/app/data`, so images are re-downloaded after every container update. WUD triggers automatic container updates on new tags, which clears all images.
* **Related**: ADR-0015 (pluggable enricher registry — enrichers call `DownloadAndSaveImage`), SPEC metadata-enrichment-pipeline REQ-ENRICH-031..033
