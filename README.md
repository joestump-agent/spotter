# Spotter

Spotter is an AI-powered playlist generator for Navidrome. It aggregates your listening history from various sources (Navidrome, Spotify, Last.fm) and uses that data to generate personalized playlists.

## Features

*   **Unified Listening History**: Syncs recent listens from Navidrome and Spotify into a single view.
*   **Navidrome Integration**: Log in using your existing Navidrome credentials.
*   **External Service Support**: Connect your Spotify and Last.fm accounts to import history and improve recommendations.
*   **Modern Stack**: Built with Go, Ent (ORM), HTMX, Templ, and Tailwind CSS.
*   **Responsive UI**: Mobile-friendly interface with automatic Light/Dark mode support.
*   **Pluggable Architecture**: Easily extensible sync framework for adding more music providers.

## Getting Started

### Prerequisites

*   Go 1.23+
*   Node.js & npm (for Tailwind CSS generation)
*   Make

### Installation

1.  Clone the repository:
    ```bash
    git clone https://github.com/yourusername/spotter.git
    cd spotter
    ```

2.  Install dependencies:
    ```bash
    make deps
    ```

3.  Build and run locally:
    ```bash
    make run
    ```
    The server will start at `http://localhost:8080`.

### Docker

You can also run Spotter using Docker:

```bash
make docker-build
docker run -p 8080:8080 --env-file .env spotter
```

## Configuration

Spotter is configured using environment variables. You can set these in your shell or use a `.env` file (if you add support for it, otherwise pass them to the process).

| Variable | Description | Default |
| :--- | :--- | :--- |
| `SPOTTER_SERVER_PORT` | The HTTP port the server listens on. | `8080` |
| `SPOTTER_SERVER_HOST` | The host address to bind to. | `0.0.0.0` |
| `SPOTTER_DATABASE_DRIVER` | Database driver (`sqlite3`, `postgres`, `mysql`). | `sqlite3` |
| `SPOTTER_DATABASE_SOURCE` | Connection string for the database. | `file:spotter.db?cache=shared&_fk=1` |
| `SPOTTER_NAVIDROME_BASE_URL` | **Required.** The URL of your Navidrome instance. | *None* |
| `SPOTTER_SPOTIFY_CLIENT_ID` | Spotify Client ID for API access. | *None* |
| `SPOTTER_SPOTIFY_CLIENT_SECRET` | Spotify Client Secret. | *None* |
| `SPOTTER_SPOTIFY_REDIRECT_URL` | OAuth callback URL for Spotify. | *None* |
| `SPOTTER_LASTFM_API_KEY` | Last.fm API Key. | *None* |
| `SPOTTER_LASTFM_SHARED_SECRET`| Last.fm Shared Secret. | *None* |
| `SPOTTER_LASTFM_REDIRECT_URL` | Callback URL for Last.fm auth. | *None* |

### Example `.env`

```bash
SPOTTER_NAVIDROME_BASE_URL=https://music.example.com
SPOTTER_SPOTIFY_CLIENT_ID=your_spotify_id
SPOTTER_SPOTIFY_CLIENT_SECRET=your_spotify_secret
```

## Development

### Running Tests

To run the unit tests:

```bash
make test
```

### Building CSS

If you make changes to the Tailwind classes in `.templ` files, regenerate the CSS:

```bash
make css
```
# Or watch for changes
npm run watch:css
```

### Architecture

*   **Backend**: Go with `chi` router.
*   **Database**: SQLite (via `ent` ORM).
*   **Frontend**: Server-side rendering with `templ` + `HTMX` for interactivity.
*   **Styling**: `Tailwind CSS` with `@iconify/tailwind` for icons.

## License

MIT