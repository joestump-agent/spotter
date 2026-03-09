---
sidebar_position: 3
---

# Installation

The recommended way to run Spotter is with **Docker**. See the [Docker Deployment](/docs/getting-started/docker) guide for a production-ready setup with PostgreSQL.

For a quick start:

```bash
# Download the compose file
curl -o docker-compose.yml \
  https://raw.githubusercontent.com/joestump/spotter/main/docker-compose.postgres.yml

# Edit with your Navidrome URL, API keys, etc.
$EDITOR docker-compose.yml

# Start Spotter + PostgreSQL
docker compose up -d
```

Open `http://localhost:8080` and log in with your Navidrome credentials.

## Development Setup

If you want to build from source or contribute to Spotter:

### Prerequisites

- **Go 1.24+** - [Download Go](https://go.dev/dl/)
- **Node.js & npm** - For Tailwind CSS generation ([Download Node.js](https://nodejs.org/))
- **Make** - Build automation tool (usually pre-installed on macOS/Linux)

### Build from Source

```bash
git clone https://github.com/joestump/spotter.git
cd spotter

# Install dependencies
make deps

# Configure your environment
cp .env.example .env
# Edit .env with your Navidrome URL and API keys

# Run the server
make run
```

### Development Mode

For development with hot-reload:

```bash
make dev
```

This starts:
- **Air**: Go hot-reload on `http://localhost:8080`
- **Templ**: Template watching with proxy on `http://localhost:7331`
- **Tailwind**: CSS watching and rebuilding

## Next Steps

- [Docker Deployment](/docs/getting-started/docker) — production setup with PostgreSQL and persistent volumes
- [Configuration](/docs/getting-started/configuration) — all environment variables
- [Connect Spotify](/docs/providers/spotify) — for playlist sync and listening history
