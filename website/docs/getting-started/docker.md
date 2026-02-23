---
sidebar_position: 3
---

# Docker Deployment

Spotter ships as a Docker image on the GitHub Container Registry. The recommended setup uses PostgreSQL as the database.

:::warning Persist `/app/data` or lose your artwork
Spotter stores downloaded album and artist artwork in `/app/data/images`. If you don't mount this directory as a volume, **all images will be lost every time the container restarts or updates**. Always mount `/app/data`.
:::

## Quick Start with Docker Compose

The easiest way to run Spotter is with the included `docker-compose.postgres.yml`:

```bash
# Edit the compose file with your settings first
docker compose -f docker-compose.postgres.yml up -d
```

Or copy it as your starting point:

```bash
cp docker-compose.postgres.yml docker-compose.yml
# Edit docker-compose.yml with your Navidrome URL, API keys, etc.
docker compose up -d
```

## Docker Compose Reference

A production-ready Compose file with PostgreSQL:

```yaml
services:
  postgres:
    image: postgres:16-alpine
    environment:
      POSTGRES_DB: spotter
      POSTGRES_USER: spotter
      POSTGRES_PASSWORD: spotter
    volumes:
      - postgres_data:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U spotter"]
      interval: 5s
      timeout: 5s
      retries: 5

  spotter:
    image: ghcr.io/joestump/spotter:latest
    ports:
      - "8080:8080"
    depends_on:
      postgres:
        condition: service_healthy
    environment:
      SPOTTER_DATABASE_DRIVER: postgres
      SPOTTER_DATABASE_SOURCE: "host=postgres port=5432 dbname=spotter user=spotter password=spotter sslmode=disable"
      SPOTTER_NAVIDROME_BASE_URL: "https://your-navidrome-instance"
      SPOTTER_OPENAI_API_KEY: "sk-your-api-key"
      SPOTTER_SECURITY_ENCRYPTION_KEY: "replace-with-64-hex-chars"
      SPOTTER_SECURITY_JWT_SECRET: "replace-with-at-least-32-chars"
    volumes:
      - spotter_data:/app/data   # Required: persists images and prompt templates

volumes:
  postgres_data:
  spotter_data:
```

## Persistent Data

### `/app/data` — Required volume

The `/app/data` directory holds all mutable runtime data:

| Path | Contents |
| :--- | :--- |
| `/app/data/images/artists/` | Downloaded artist artwork |
| `/app/data/images/albums/` | Downloaded album artwork |
| `/app/data/prompts/` | AI prompt templates |

**Without this volume**, every container restart or image update wipes all downloaded artwork. Spotter will automatically re-detect missing files and queue them for re-download on the next sync, but it's much better to just persist the directory.

Use a named volume (recommended):
```yaml
volumes:
  - spotter_data:/app/data
```

Or a bind mount if you want to access the files directly on the host:
```yaml
volumes:
  - ./data:/app/data
```

### PostgreSQL data

PostgreSQL manages its own persistence via the `postgres_data` volume. No extra configuration needed beyond mounting `/var/lib/postgresql/data` for the Postgres container.

## Running with `docker run`

For quick testing (not recommended for production without volume mounts):

```bash
docker run -p 8080:8080 \
  -e SPOTTER_DATABASE_DRIVER=postgres \
  -e SPOTTER_DATABASE_SOURCE="host=your-postgres port=5432 dbname=spotter user=spotter password=spotter sslmode=disable" \
  -e SPOTTER_NAVIDROME_BASE_URL=https://music.example.com \
  -e SPOTTER_OPENAI_API_KEY=sk-your-api-key \
  -v spotter-data:/app/data \
  ghcr.io/joestump/spotter:latest
```

## Environment Variables

See the [Configuration](/docs/getting-started/configuration) guide for the complete list.

### Required Variables

| Variable | Description |
| :--- | :--- |
| `SPOTTER_NAVIDROME_BASE_URL` | URL of your Navidrome instance |
| `SPOTTER_OPENAI_API_KEY` | OpenAI API key for AI features |
| `SPOTTER_DATABASE_DRIVER` | Set to `postgres` |
| `SPOTTER_DATABASE_SOURCE` | PostgreSQL connection string |

### Network Configuration

If Spotter needs to connect to services on the host machine (like a local Navidrome instance), use the special Docker DNS name:

```bash
# On Docker Desktop (macOS/Windows)
SPOTTER_NAVIDROME_BASE_URL=http://host.docker.internal:4533

# On Linux (add to docker run)
docker run --add-host=host.docker.internal:host-gateway ...
```

## Health Checks

```bash
docker ps
curl http://localhost:8080/health
```

## Troubleshooting

### Container Won't Start

Check the logs:

```bash
docker logs <container_id>
```

### Images Not Showing After Update

If artwork disappears after a container update, the `/app/data` volume was probably not mounted. Spotter will self-heal on the next sync run: go to **Preferences → Tasks** and trigger a sync to re-download all missing images.

To avoid this in the future, ensure `/app/data` is mounted as a persistent volume.

### Permission Issues

If you encounter permission issues with mounted volumes:

```bash
# Fix ownership on Linux
sudo chown -R 1000:1000 ./data
```
