# Spotter

Spotter is an AI-powered playlist generator for Navidrome. It aggregates your listening history from various sources (Navidrome, Spotify, Last.fm) and uses that data to generate personalized playlists.

## Features

*   **Unified Listening History**: Syncs recent listens from Navidrome, Spotify, and Last.fm into a single view with pagination.
*   **Playlist Management**: View and sync playlists from all connected services.
*   **Navidrome Integration**: Log in using your existing Navidrome credentials.
*   **External Service Support**: Connect your Spotify and Last.fm accounts to import history and improve recommendations.
*   **Real-time Updates**: Server-Sent Events (SSE) push new listens and sync notifications to the UI automatically.
*   **Retro-Themed UI**: Custom-designed themes featuring a warm 1970s music cabinet aesthetic (light mode) and an 1980s cyberpunk vibe (dark mode).
*   **AI-Powered**: Customizable AI system prompts for personalized playlist generation.
*   **Pluggable Architecture**: Easily extensible sync framework for adding more music providers.
*   **Background Sync**: Configurable automatic synchronization of listening history and playlists.
</text>

<old_text line=72>
### Sync Configuration

| Variable | Description | Default |
| :--- | :--- | :--- |
| `SPOTTER_SYNC_INTERVAL` | How often to sync data from providers (Go duration format). | `5m` |

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

Spotter is configured using environment variables. You can set these in your shell or use a `.env` file.

### Server Configuration

| Variable | Description | Default |
| :--- | :--- | :--- |
| `SPOTTER_SERVER_PORT` | The HTTP port the server listens on. | `8080` |
| `SPOTTER_SERVER_HOST` | The host address to bind to. | `0.0.0.0` |

### Database Configuration

| Variable | Description | Default |
| :--- | :--- | :--- |
| `SPOTTER_DATABASE_DRIVER` | Database driver (`sqlite3`, `postgres`, `mysql`). | `sqlite3` |
| `SPOTTER_DATABASE_SOURCE` | Connection string for the database. | `file:spotter.db?cache=shared&_fk=1` |

### Sync Configuration

| Variable | Description | Default |
| :--- | :--- | :--- |
| `SPOTTER_SYNC_INTERVAL` | How often to sync data from providers (Go duration format). | `5m` |

### Provider Configuration

| Variable | Description | Default |
| :--- | :--- | :--- |
| `SPOTTER_NAVIDROME_BASE_URL` | **Required.** The URL of your Navidrome instance. | *None* |
| `SPOTTER_SPOTIFY_CLIENT_ID` | Spotify Client ID for API access. | *None* |
| `SPOTTER_SPOTIFY_CLIENT_SECRET` | Spotify Client Secret. | *None* |
| `SPOTTER_SPOTIFY_REDIRECT_URL` | OAuth callback URL for Spotify. | `http://localhost:8080/auth/spotify/callback` |
| `SPOTTER_LASTFM_API_KEY` | Last.fm API Key. | *None* |
| `SPOTTER_LASTFM_SHARED_SECRET`| Last.fm Shared Secret. | *None* |
| `SPOTTER_LASTFM_REDIRECT_URL` | Callback URL for Last.fm auth. | *None* |

### Example `.env`

```bash
# Required
SPOTTER_NAVIDROME_BASE_URL=https://music.example.com

# Optional - Sync Configuration
SPOTTER_SYNC_INTERVAL=10m

# Optional - Spotify Integration
SPOTTER_SPOTIFY_CLIENT_ID=your_spotify_id
SPOTTER_SPOTIFY_CLIENT_SECRET=your_spotify_secret
SPOTTER_SPOTIFY_REDIRECT_URL=http://localhost:8080/auth/spotify/callback

# Optional - Last.fm Integration
SPOTTER_LASTFM_API_KEY=your_lastfm_key
SPOTTER_LASTFM_SHARED_SECRET=your_lastfm_secret
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

*   **Backend**: Go with `chi` router and Server-Sent Events (SSE) for real-time updates.
*   **Database**: SQLite (via `ent` ORM) with automatic migrations.
*   **Frontend**: Server-side rendering with `templ` + `HTMX` for interactivity and real-time updates.
*   **Styling**: `DaisyUI` + `Tailwind CSS` with custom retro themes and `@iconify/tailwind` for icons.
*   **Background Jobs**: Configurable periodic sync for all connected providers.
*   **Real-time**: Event Bus + SSE for push notifications and live updates.

## User Features

### Themes

Spotter features two custom-designed retro themes that create an immersive nostalgic experience:

#### 🎵 Light Theme - 1970s Music Cabinet

The light theme evokes the warm, cozy feeling of a vintage 1970s hi-fi system:

*   **Color Palette**: Rich amber (#d97706), golden yellow (#f59e0b), and deep brown (#92400e) tones
*   **Background**: Cream and beige tones (#fef3c7) reminiscent of wood veneer
*   **Visual Effects**: 
    - Subtle wood-grain texture overlays
    - Raised beveled borders on cards and buttons
    - Soft inset shadows suggesting depth
    - Warm text shadows for a soft, analog feel
*   **Typography**: Bold, slightly spaced lettering with gentle shadows
*   **Aesthetic**: Think vintage record players, warm living rooms, and analog warmth

#### ⚡ Dark Theme - 1980s Cyberpunk

The dark theme channels the neon-soaked, digital future imagined in 1980s cyberpunk:

*   **Color Palette**: Neon cyan (#00d9ff), magenta (#ff00ff), and electric green (#00ff41)
*   **Background**: Deep dark blue-black (#0f0f23) with navy accents
*   **Visual Effects**:
    - Scan line overlay across the entire interface
    - Glowing neon borders on all interactive elements
    - Multiple layered box shadows creating depth and glow
    - Text shadows with neon glow effects
    - Icon glow filters for that electric feel
*   **Typography**: Sharp, uppercase lettering with wider tracking and neon glow
*   **Aesthetic**: Blade Runner meets Tron - dark, electric, and futuristic

**Theme Controls:**
*   **Sidebar Toggle**: Temporarily switch themes (persisted in browser localStorage, does not affect database)
*   **Preferences > General Tab**: Permanently set your theme preference (Light, Dark, or System)

### Preferences

*   **Theme Selection**: Choose between Light, Dark, or System (auto) themes. Your preference is saved to the database.
*   **AI System Prompt**: Customize the AI personality for playlist generation.
*   **Pagination**: Configure how many items to display per page (10-100).

### Connected Services

*   **Navidrome**: Automatically connected via your login credentials. View last sync time.
*   **Spotify**: Optional OAuth integration. Connect/disconnect from Preferences. View last sync time.
*   **Last.fm**: Optional OAuth integration. Connect/disconnect from Preferences. View last sync time and username.

### Real-time Features

*   Live updates for new listens via SSE
*   Toast notifications for sync events
*   Automatic background synchronization every 5 minutes (configurable)
*   No manual refresh buttons needed

## License

MIT