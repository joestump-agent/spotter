---
sidebar_position: 4
---

# Metadata Enrichment

Spotter automatically enriches your music library with metadata from multiple sources.

## Overview

The metadata enrichment system aggregates data from:

- **MusicBrainz**: Open music database (no API key required)
- **Spotify**: Audio features and high-resolution artwork
- **Last.fm**: Community-driven tags and biographies
- **Fanart.tv**: High-quality artist and album images
- **Lidarr**: Music collection manager integration
- **OpenAI**: AI-generated summaries, biographies, and tags

## How It Works

1. **Automatic Scheduling**: Enrichment runs periodically based on configuration
2. **Priority Order**: Enrichers run in a configurable order, allowing later sources to override earlier ones
3. **Incremental Updates**: Only entities lacking metadata are processed
4. **AI Enhancement**: OpenAI runs last to generate intelligent summaries from all collected data

## Configuration

```bash
# Enable/disable enrichment
SPOTTER_METADATA_ENABLED=true

# Run enrichment every hour
SPOTTER_METADATA_INTERVAL=1h

# Enricher priority (later sources can override earlier ones)
SPOTTER_METADATA_ORDER=lidarr,musicbrainz,navidrome,spotify,lastfm,fanart,openai
```

## Enriched Data

### Artists

- Name and alternate names
- Biography
- Genres and tags
- Images (profile, background, logo)
- MusicBrainz ID
- External IDs (Spotify, Last.fm, etc.)
- AI-generated summary and analysis

### Albums

- Title
- Release date
- Genres and tags
- Cover artwork (multiple sizes)
- Track listing
- MusicBrainz ID
- AI-generated summary

### Tracks

- Title
- Duration
- Track number
- Audio features (BPM, key, energy, danceability)
- Genres and tags
- ISRC code
- AI-generated description

## Image Handling

Downloaded images are stored locally and optimized:

```bash
# Enable local image storage
SPOTTER_METADATA_IMAGES_DOWNLOAD=true

# Storage directory
SPOTTER_METADATA_IMAGES_DIRECTORY=./data/images

# Maximum dimensions (images are resized)
SPOTTER_METADATA_IMAGES_MAX_WIDTH=1000
SPOTTER_METADATA_IMAGES_MAX_HEIGHT=1000
```

## Manual Enrichment

See the [Enrichers Overview](/docs/enrichers/overview) for details on manual enrichment and individual enricher configuration.

## Rate Limiting

Each enricher respects API rate limits:

| Service | Rate Limit |
| :--- | :--- |
| MusicBrainz | 1 request/second |
| Spotify | Varies by endpoint |
| Last.fm | Generous limits |
| Fanart.tv | Based on API tier |

## Troubleshooting

### Missing Metadata

If metadata isn't appearing:

1. Check that the enricher is enabled in `SPOTTER_METADATA_ORDER`
2. Verify API keys are configured correctly
3. Check logs for rate limiting or API errors
4. Try triggering manual enrichment from Tasks

### Wrong Data

If metadata is incorrect:

1. The MusicBrainz ID may be mismatched
2. Try refreshing from the entity's detail page
3. OpenAI summaries may need regeneration
