# Spotter Development Guide

## Project Overview

**Spotter** is an AI-powered playlist generator and metadata enrichment system for Navidrome. It aggregates listening history from multiple sources (Navidrome, Spotify, Last.fm), enriches music metadata using AI and external services, and generates intelligent playlists through customizable DJ personas.

### Key Features
- Unified listening history across multiple music services
- AI-powered mixtape generation with customizable DJ personas
- Automatic metadata enrichment from MusicBrainz, Spotify, Last.fm, Fanart.tv, and OpenAI
- Playlist synchronization between services with intelligent track matching
- Real-time updates via Server-Sent Events (SSE)
- Retro-themed UI (1970s music cabinet light theme, 1980s cyberpunk dark theme)
- Pluggable provider and enricher architecture

### User Stories

**Authentication & Onboarding**
- As a user, I want to log in using my existing Navidrome credentials so I don't need another account
- As a user, I want to connect my Spotify account to import my listening history and playlists
- As a user, I want to connect my Last.fm account to sync my scrobble history
- As a user, I want to disconnect external services when I no longer want to share data

**Listening History**
- As a user, I want to see my recent listening history from all connected services in one unified view
- As a user, I want to paginate through my listening history to explore older listens
- As a user, I want to see real-time updates when new tracks are played without refreshing the page
- As a user, I want to see which service each listen came from (Navidrome, Spotify, Last.fm)

**Playlist Management**
- As a user, I want to view all my playlists from Navidrome, Spotify, and Last.fm in one place
- As a user, I want to sync playlists from Spotify or Last.fm to my Navidrome library
- As a user, I want to see sync status (pending, success, warning, error) for each playlist
- As a user, I want to see how many tracks were successfully matched during sync
- As a user, I want to manually trigger playlist sync when I make changes
- As a user, I want to rebuild a playlist sync from scratch if something goes wrong
- As a user, I want to disable sync for a playlist and optionally remove it from Navidrome
- As a user, I want to see detailed match statistics (total tracks, matched tracks, percentage)
- As a user, I want automatic periodic sync so my playlists stay up-to-date

**AI-Powered Mixtapes (Vibes Engine)**
- As a user, I want to create DJ personas with unique personalities and music preferences
- As a user, I want to generate mixtapes based on a DJ's personality and my listening history
- As a user, I want to seed mixtapes with specific artists, albums, or tracks
- As a user, I want to schedule mixtapes to regenerate daily, weekly, or monthly
- As a user, I want to see why the AI selected each track for my mixtape
- As a user, I want to sync generated mixtapes to Navidrome as playable playlists
- As a user, I want to enhance existing playlists with AI-suggested tracks that complement the vibe
- As a user, I want to reorder playlist tracks using AI for better flow and energy progression
- As a user, I want to create mixtapes inspired by specific artists from their detail pages

**Metadata Enrichment**
- As a user, I want artist biographies and tags automatically added to my library
- As a user, I want album summaries and metadata enriched from multiple sources
- As a user, I want high-quality artist and album images downloaded to my library
- As a user, I want AI-generated summaries and tags clearly marked in the UI
- As a user, I want to manually trigger enrichment for specific artists or albums
- As a user, I want periodic background enrichment so my library stays current

**Artist Discovery**
- As a user, I want to find similar artists within my own library using AI analysis
- As a user, I want to see confidence scores for artist similarities
- As a user, I want to see explanations for why artists are considered similar
- As a user, I want to refresh similar artist recommendations to discover new connections
- As a user, I want to create mixtapes inspired by artists I discover

**Preferences & Customization**
- As a user, I want to choose between light, dark, or system-based themes
- As a user, I want to customize the AI personality for playlist generation
- As a user, I want to configure pagination (items per page) for listings
- As a user, I want to see when my connected services last synced
- As a user, I want to manually trigger background tasks (sync, enrichment, cleanup)

**User Experience**
- As a user, I want toast notifications for important events (sync started, completed, failed)
- As a user, I want progress indicators during long-running operations
- As a user, I want timeago timestamps that automatically update (e.g., "5 minutes ago")
- As a user, I want keyboard navigation and accessibility features
- As a user, I want responsive design that works on mobile and desktop

### RFC 2119 Requirements

#### Configuration (CFG)
- **CFG-001**: The system MUST validate that NavidromeBaseURL is provided in configuration
- **CFG-002**: The system MUST validate that OpenAI API key is provided when AI features are enabled
- **CFG-003**: The system MUST validate that Lidarr base URL is provided when Lidarr integration is enabled
- **CFG-004**: The system MUST validate that Lidarr API key is provided when Lidarr integration is enabled
- **CFG-005**: The system MUST validate that required enrichers are enabled
- **CFG-006**: The system SHOULD set default theme to "dark" when not specified
- **CFG-007**: The system MUST support environment variable overrides for all configuration values
- **CFG-008**: The system MUST load configuration from YAML files when present

#### Authentication & Authorization (AUTH)
- **AUTH-001**: The system MUST authenticate users via Navidrome Subsonic API
- **AUTH-002**: The system MUST reject login attempts with invalid credentials
- **AUTH-003**: The system MUST create or retrieve user records after successful authentication
- **AUTH-004**: The system MUST create NavidromeAuth edges linking users to their Navidrome credentials
- **AUTH-005**: The system MUST set secure HTTP-only session cookies upon successful authentication
- **AUTH-006**: The system MUST redirect authenticated users to the home page
- **AUTH-007**: The system MUST validate that username and password are provided in login requests
- **AUTH-008**: The system MUST NOT expose Navidrome credentials in responses
- **AUTH-009**: The system MUST use Subsonic authentication protocol for Navidrome API calls
- **AUTH-010**: The system MUST generate salted MD5 tokens for Subsonic API authentication
- **AUTH-011**: The system MUST include client and version parameters in Subsonic API requests

#### Provider Integration (PROV)
- **PROV-SP-001**: The system MUST refresh expired OAuth2 tokens automatically
- **PROV-SP-002**: The system MUST check token expiry before making API requests
- **PROV-SP-003**: The system MUST calculate playlist statistics including unique artists and albums
- **PROV-SP-004**: The system MUST handle token refresh failures gracefully
- **PROV-SP-005**: The system SHOULD cache token refresh results to avoid redundant API calls
- **PROV-ND-001**: The system MUST support fetching playlists from Navidrome
- **PROV-ND-002**: The system MUST support creating new playlists in Navidrome
- **PROV-ND-003**: The system MUST support updating existing playlists in Navidrome
- **PROV-ND-004**: The system MUST support deleting playlists from Navidrome
- **PROV-ND-005**: The system MUST fetch recent listens with configurable time ranges
- **PROV-ND-006**: The system MUST filter recent listens by timestamp
- **PROV-ND-007**: The system SHOULD handle pagination for large result sets
- **PROV-LF-001**: The system MUST authenticate with Last.fm using API keys
- **PROV-LF-002**: The system MUST fetch artist information including biography and tags
- **PROV-LF-003**: The system MUST fetch album information and release metadata
- **PROV-LF-004**: The system MUST strip HTML tags from biography text
- **PROV-LF-005**: The system MUST parse MusicBrainz IDs from Last.fm responses
- **PROV-LF-006**: The system SHOULD handle missing or incomplete metadata gracefully

#### Enrichers (ENR)
- **ENR-FA-001**: The system MUST require API key for Fanart.tv integration
- **ENR-FA-002**: The system MUST fetch artist images by MusicBrainz ID
- **ENR-FA-003**: The system MUST fetch album images by MusicBrainz ID
- **ENR-FA-004**: The system MUST prioritize image types (thumbnail, cover, banner)
- **ENR-FA-005**: The system SHOULD cache Fanart.tv API responses to minimize requests
- **ENR-LF-001**: The system MUST enrich artists with Last.fm biography data
- **ENR-LF-002**: The system MUST enrich albums with Last.fm metadata
- **ENR-LF-003**: The system MUST enrich tracks with Last.fm metadata
- **ENR-LF-004**: The system MUST strip HTML from biography content before storage
- **ENR-LF-005**: The system MUST parse and store tags from Last.fm
- **ENR-LF-006**: The system MUST extract MusicBrainz IDs when available
- **ENR-LF-007**: The system SHOULD deduplicate tags across multiple sources
- **ENR-MB-001**: The system MUST respect MusicBrainz API rate limits (1 request per second)
- **ENR-MB-002**: The system MUST search for artist MusicBrainz IDs by name
- **ENR-MB-003**: The system MUST search for album MusicBrainz IDs by title and artist
- **ENR-MB-004**: The system MUST search for track MusicBrainz IDs by title, artist, and album
- **ENR-MB-005**: The system MUST parse release year from MusicBrainz date fields
- **ENR-MB-006**: The system MUST handle fuzzy matching for artist/album/track names
- **ENR-MB-007**: The system SHOULD cache MusicBrainz ID lookups to reduce API calls
- **ENR-AI-001**: The system MUST use OpenAI API for AI-powered metadata enrichment
- **ENR-AI-002**: The system MUST parse JSON responses from OpenAI completions
- **ENR-AI-003**: The system MUST deduplicate tags from AI enrichment
- **ENR-AI-004**: The system MUST skip AI enrichment for recently enriched entities (within 7 days)
- **ENR-AI-005**: The system MUST sanitize AI-generated JSON to handle trailing commas
- **ENR-AI-006**: The system SHOULD handle AI API failures gracefully without blocking other operations
- **ENR-AI-007**: The system MUST provide entity context (artist/album/track data) to AI prompts

#### Handlers (HAND)
- **HAND-AL-001**: The system MUST display album details including tracks
- **HAND-AL-002**: The system MUST enforce user isolation (users can only view their own albums)
- **HAND-AL-003**: The system MUST trigger manual enrichment when requested
- **HAND-AL-004**: The system SHOULD display enrichment status and timestamps
- **HAND-PL-001**: The system MUST allow users to toggle playlist sync on/off
- **HAND-PL-002**: The system MUST enforce user isolation (users can only access their own playlists)
- **HAND-PL-003**: The system MUST display sync status with appropriate badges (Success, Warning, Error, Pending, Neutral)
- **HAND-PL-004**: The system MUST show WARNING status when zero tracks are matched (not SUCCESS)
- **HAND-PL-005**: The system MUST calculate and display match statistics (total tracks, matched tracks, match percentage)
- **HAND-PL-006**: The system SHOULD refresh playlist data after sync operations
- **HAND-VB-001**: The system MUST generate AI-powered mixtapes based on user prompts
- **HAND-VB-002**: The system MUST validate that prompts are provided for mixtape generation
- **HAND-VB-003**: The system MUST match AI-generated tracks to user's Navidrome library
- **HAND-VB-004**: The system MUST use fuzzy matching with confidence thresholds for track matching
- **HAND-VB-005**: The system SHOULD display unmatched tracks to the user
- **HAND-VB-006**: The system MUST allow users to create playlists from mixtapes

#### Services (SRV)
- **SRV-AI-001**: The system MUST enrich artists that have never been AI-enriched (last_ai_enriched_at IS NULL)
- **SRV-AI-002**: The system MUST re-enrich artists whose AI enrichment is older than 7 days
- **SRV-AI-003**: The system MUST track separate timestamps for AI enrichment vs general enrichment
- **SRV-AI-004**: The system MUST query for artists needing AI enrichment independently of general enrichment status
- **SRV-AI-005**: The system MUST update last_ai_enriched_at timestamp after successful AI enrichment
- **SRV-AI-006**: The system SHOULD prioritize artists with no AI enrichment over stale enrichment
- **SRV-PS-001**: The system MUST sync playlists from external providers to Navidrome
- **SRV-PS-002**: The system MUST match external tracks to Navidrome library tracks
- **SRV-PS-003**: The system MUST use fuzzy matching with configurable confidence thresholds
- **SRV-PS-004**: The system MUST track sync status (success, warning, error, pending)
- **SRV-PS-005**: The system MUST record match statistics (total tracks, matched tracks)
- **SRV-PS-006**: The system MUST respect delete configuration (delete orphaned tracks or preserve them)
- **SRV-PS-007**: The system MUST update playlist metadata in database after sync
- **SRV-PS-008**: The system SHOULD handle partial sync failures gracefully
- **SRV-PS-009**: The system MUST NOT mark syncs as successful when zero tracks are matched
- **SRV-TM-001**: The system MUST perform fuzzy matching between external and library tracks
- **SRV-TM-002**: The system MUST normalize track titles by removing "(Remastered)", "(Live)", and punctuation
- **SRV-TM-003**: The system MUST normalize artist names for comparison
- **SRV-TM-004**: The system MUST normalize album titles for comparison
- **SRV-TM-005**: The system MUST calculate match confidence scores (0-100)
- **SRV-TM-006**: The system MUST enforce minimum confidence threshold (default 80)
- **SRV-TM-007**: The system MUST match on artist + title as primary criteria
- **SRV-TM-008**: The system SHOULD boost confidence when album names also match
- **SRV-TM-009**: The system SHOULD use Levenshtein distance for fuzzy string matching
- **SRV-TM-010**: The system MUST return the highest confidence match when multiple candidates exist
- **SRV-SA-001**: The system MUST fetch similar artists from Last.fm
- **SRV-SA-002**: The system MUST store similar artist relationships in database
- **SRV-SA-003**: The system SHOULD deduplicate similar artist relationships
- **SRV-SA-004**: The system MUST handle cases where no similar artists are found
- **SRV-SY-001**: The system MUST sync artists from Navidrome to local database
- **SRV-SY-002**: The system MUST sync albums from Navidrome to local database
- **SRV-SY-003**: The system MUST sync tracks from Navidrome to local database
- **SRV-SY-004**: The system MUST create relationships between artists, albums, and tracks
- **SRV-SY-005**: The system MUST handle incremental updates (new artists/albums/tracks)
- **SRV-SY-006**: The system SHOULD batch database operations for performance
- **SRV-SY-007**: The system MUST preserve existing enrichment data during sync

#### Vibes (AI Mixtapes) (VIB)
- **VIB-GEN-001**: The system MUST generate mixtapes using OpenAI based on user prompts
- **VIB-GEN-002**: The system MUST parse AI response to extract track list with artist, title, and album
- **VIB-GEN-003**: The system MUST match AI-generated tracks to user's Navidrome library
- **VIB-GEN-004**: The system MUST use fuzzy matching with Levenshtein distance for track matching
- **VIB-GEN-005**: The system MUST calculate similarity scores for track matching
- **VIB-GEN-006**: The system MUST track which tracks were successfully matched vs unmatched
- **VIB-GEN-007**: The system SHOULD provide reasonable defaults when AI doesn't specify album names
- **VIB-ENH-001**: The system MUST enhance existing playlists with AI suggestions
- **VIB-ENH-002**: The system MUST analyze playlist characteristics (genres, moods, artists)
- **VIB-ENH-003**: The system MUST generate contextual AI prompts based on playlist content
- **VIB-ENH-004**: The system MUST match AI suggestions to user's library
- **VIB-ENH-005**: The system SHOULD preserve original playlist tracks while adding enhancements
- **VIB-PAR-001**: The system MUST parse JSON responses from AI mixtape generation
- **VIB-PAR-002**: The system MUST sanitize AI-generated JSON to fix trailing commas
- **VIB-PAR-003**: The system MUST handle malformed JSON from AI responses gracefully
- **VIB-PAR-004**: The system MUST extract track information (artist, title, album, reason) from AI JSON
- **VIB-PAR-005**: The system MUST validate that required fields (artist, title) are present
- **VIB-PAR-006**: The system SHOULD parse optional fields (album, reason) when available

#### View Components (VIEW)
- **VIEW-SS-001**: The system MUST display SUCCESS status when all tracks are matched (100%)
- **VIEW-SS-002**: The system MUST display WARNING status when some tracks are matched but not all
- **VIEW-SS-003**: The system MUST display WARNING status when zero tracks are matched (not SUCCESS)
- **VIEW-SS-004**: The system MUST display ERROR status when sync operations fail
- **VIEW-SS-005**: The system MUST display PENDING status when sync is in progress
- **VIEW-SS-006**: The system MUST display NEUTRAL status when no sync has been attempted
- **VIEW-SS-007**: The system MUST apply appropriate CSS classes for badge styling
- **VIEW-SS-008**: The system MUST calculate progress percentage for partial matches
- **VIEW-SS-009**: The system MUST display match statistics (X of Y tracks matched)
- **VIEW-TT-001**: The system MUST prioritize medium image URLs over small image URLs when available
- **VIEW-TT-002**: The system MUST fall back to small image URLs when medium images are not available
- **VIEW-TT-003**: The system MUST handle missing album images gracefully
- **VIEW-TT-004**: The system SHOULD display track duration in human-readable format

#### Data Validation (VAL)
- **VAL-001**: The system MUST validate that required fields are present before API calls
- **VAL-002**: The system MUST validate that API keys are configured before making external requests
- **VAL-003**: The system MUST validate user ownership before allowing data access
- **VAL-004**: The system MUST validate confidence thresholds are within valid ranges (0-100)
- **VAL-005**: The system MUST validate that timestamps are in correct format
- **VAL-006**: The system SHOULD validate string lengths to prevent buffer overflows
- **VAL-007**: The system MUST validate that MusicBrainz IDs are in correct UUID format
- **VAL-008**: The system MUST validate that playlist IDs exist before sync operations

#### Error Handling (ERR)
- **ERR-001**: The system MUST return appropriate HTTP status codes for errors (400, 401, 404, 500)
- **ERR-002**: The system MUST provide descriptive error messages for validation failures
- **ERR-003**: The system MUST handle network timeouts gracefully
- **ERR-004**: The system MUST handle external API failures without crashing
- **ERR-005**: The system MUST log errors with sufficient context for debugging
- **ERR-006**: The system MUST handle database connection failures gracefully
- **ERR-007**: The system SHOULD retry transient failures with exponential backoff
- **ERR-008**: The system MUST NOT expose sensitive information (API keys, passwords) in error messages
- **ERR-009**: The system MUST handle JSON parsing errors from external APIs
- **ERR-010**: The system MUST handle rate limit errors with appropriate delays

#### Integration (INT)
- **INT-001**: The system MUST support integration with Navidrome as the primary music server
- **INT-002**: The system MAY support integration with Spotify for playlist import
- **INT-003**: The system MAY support integration with Last.fm for metadata enrichment
- **INT-004**: The system MAY support integration with MusicBrainz for canonical IDs
- **INT-005**: The system MAY support integration with Fanart.tv for images
- **INT-006**: The system MAY support integration with Lidarr for music management
- **INT-007**: The system MUST support integration with OpenAI for AI features
- **INT-008**: The system MUST handle cases where optional integrations are disabled
- **INT-009**: The system MUST provide meaningful operation when enrichers fail
- **INT-010**: The system SHOULD allow per-user configuration of external integrations

## Issue Tracking

This project uses **bd** (beads) for issue tracking. Run `bd onboard` to get started.

**Workflow:**
```bash
bd ready              # Find available work
bd show <id>          # View issue details
bd update <id> --status in_progress  # Claim work
bd close <id>         # Complete work (only after ALL quality gates pass)
bd sync               # Sync with git
```

## Tech Stack
- **Language**: Go 1.24+
- **ORM**: [Ent](https://entgo.io/) (Code generation based)
- **Router**: [Chi](https://github.com/go-chi/chi) v5
- **Templating**: [Templ](https://github.com/a-h/templ)
- **Database**: SQLite (via `mattn/go-sqlite3`)
- **Logging**: `log/slog`
- **Config**: Viper
- **UI Icons**: [Hero Icons](http://heroicons.com)
- **UI Components**: DaisyUI (create shared, reusable components)

## Architecture

### Core Philosophy
Spotter separates "Providers" (sources of user data like history/playlists) from "Enrichers" (sources of metadata). Both use a Factory pattern for instantiation based on the current user context.

### Database (Ent)
- **Schema**: Defined in `ent/schema`
- **Generation**: Run `go generate ./ent` after schema changes
- **Usage**: Use the generated client for all DB operations. Avoid raw SQL.
- **Traversals**: Prefer Ent traversals over manual joins

### Providers (`internal/providers`)
Providers handle user-specific data interaction (Listen History, Playlists).

**Required Interfaces** (defined in `providers.go`):
- `HistoryFetcher`: Syncing listening history (handling pagination/cursors)
- `PlaylistManager`: Reading/Creating playlists
- `PlaylistSyncer`: Syncing playlists between services
- `Authenticator`: Handling OAuth flows

**Authentication:**
- Implement `Authenticator` for OAuth services (Spotify, Last.fm)
- **Token Management**: Auto-refresh tokens within provider methods. Callers should not handle expired tokens.
- **State**: Persist tokens via `ent` edges (e.g., `User.Edges.SpotifyAuth`)

**Factories**: `New(logger, config)` returns a `Factory` function `func(ctx, user) (Provider, error)`

### Enrichers (`internal/enrichers`)
Enrichers add metadata, images, and AI-generated content to local entities.

**Required Interfaces** (defined in `enrichers.go`):
- `IDMatcher`: **Critical**. Matches local names to external IDs (e.g., Name → Spotify ID)
- `ArtistEnricher`, `AlbumEnricher`, `TrackEnricher`: Fetches metadata using IDs

**Implementation:**
- Implement `IsAvailable() bool` to check if config/auth is present
- Return standardized structs (`ArtistData`, `AlbumData`)
- Download and resize images locally; do not store hotlinks

## Coding Standards

### Error Handling
- Use `fmt.Errorf("context: %w", err)` for wrapping
- Never panic
- Log errors with structured attributes using `slog`

### Context
- Pass `context.Context` as the first argument to all IO-bound functions
- Respect context cancellation

### Testing
- **Mocking**: Use `httptest.NewServer` to mock external APIs
- **No Network**: Tests must not make real network calls
- **Coverage**: Test happy paths, 401/403 (Auth), 429 (Rate Limits), and 404s

### External API Etiquette
- **Rate Limiting**: Handle 429 responses gracefully (exponential backoff or error)
- **Batching**: Use batch APIs where possible (e.g., Spotify Audio Features)
- **User Agent**: Set a descriptive User-Agent string

## Quality Gates (MANDATORY)

Before running `bd close <id>`, ALL of the following MUST pass:

1. **Tests**: All tests pass
2. **Linters**: Code passes all linter checks
3. **Build**: Project builds successfully
4. **Code Generation**: Run `go generate ./ent` if schema changed
5. **Standards**: Code follows all standards above (error handling, context, testing, etc.)

**DO NOT close issues until quality gates pass.**

## Completing Work (Landing the Plane)

When ending a work session, complete ALL steps below. **Work is NOT complete until `git push` succeeds.**

**MANDATORY WORKFLOW:**

1. **Verify quality gates** - Ensure all quality gates pass (tests, linters, builds)
2. **File issues for remaining work** - Create issues for anything that needs follow-up
3. **Close completed issues** - Run `bd close <id>` ONLY if quality gates passed
4. **PUSH TO REMOTE** - This is MANDATORY:
   ```bash
   git pull --rebase
   bd sync
   git push
   git status  # MUST show "up to date with origin"
   ```
5. **Clean up** - Clear stashes, prune remote branches
6. **Verify** - All changes committed AND pushed
7. **Hand off** - Provide context for next session

**CRITICAL RULES:**
- Work is NOT complete until `git push` succeeds
- NEVER stop before pushing - that leaves work stranded locally
- NEVER say "ready to push when you are" - YOU must push
- If push fails, resolve and retry until it succeeds
- DO NOT close issues unless ALL quality gates have passed
