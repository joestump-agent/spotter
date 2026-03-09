---
sidebar_position: 6
---

# Lidarr Enricher

Lidarr is a music collection manager that can automatically find, download, and organize music. Spotter integrates with Lidarr to both pull metadata from its library and automatically submit albums you've listened to for download.

## How It Works

Spotter's Lidarr integration has two sides: **enrichment** (reading from Lidarr) and **submission** (writing to Lidarr). Together they create a loop where the music you listen to on Spotify, Last.fm, and Navidrome is automatically requested in Lidarr for download.

### Enrichment

When Spotter enriches an artist or album, the Lidarr enricher checks whether it already exists in your Lidarr library. If found, it pulls metadata (genres, album type, release date, download status) back into Spotter. If not found, the item is queued for submission.

### Submission Pipeline

Rather than submitting directly to Lidarr's API during enrichment (which would flood Lidarr when processing a large library), Spotter uses a **decoupled queue with backpressure**:

```text
Enrichment discovers album not in Lidarr
    ↓
Album + artist inserted into submission queue (DB table)
    ↓
Background submitter wakes every 3 minutes
    ↓
Checks Lidarr's download queue depth
    ↓
If queue depth < max (default 50), submits next item
    ↓
If queue depth >= max, waits until next cycle (backpressure)
```

**Key behaviors:**

- **Artists are submitted before albums** — Lidarr requires an artist to exist before you can add one of their albums. The submitter processes artists first (ordered by creation time), then albums.
- **Artists are added without auto-search** — When Spotter adds an artist to Lidarr, it sets `monitor: none` and `searchForMissingAlbums: false`. This registers the artist in Lidarr without triggering a search for all of their albums. Only the specific albums Spotter submits will be searched.
- **Albums trigger a search** — When an album is submitted, Lidarr searches your indexers for it. This is the intended behavior — these are albums you've actually listened to.
- **Only albums you've encountered are submitted** — Spotter only queues albums that appear in your listening history or playlists. If you've listened to 2 of an artist's 10 albums, only those 2 are submitted to Lidarr.

### Backpressure

The submitter checks Lidarr's actual download queue depth before each submission. If the queue is at or above the configured maximum (`SPOTTER_LIDARR_QUEUE_MAX`, default 50), it pauses and waits until the next cycle. This prevents Spotter from piling up hundreds of downloads faster than Lidarr (and your indexers/download client) can process them.

The check interval (`SPOTTER_LIDARR_SUBMIT_INTERVAL`, default 3 minutes) is intentionally slow — downloads take time, and there's no benefit to checking every few seconds.

### Failure Handling

When a submission fails, the submitter classifies the error:

**Permanent failures** (never retried):
- Artist lookup returned an empty name (bad MusicBrainz entry)
- Artist path already configured for another artist (name collision in Lidarr)
- Album/artist not found in Lidarr's metadata lookup (MBID doesn't exist)

**Transient failures** (retried with exponential backoff):
- Network errors, timeouts, Lidarr API 500s
- Backoff: 1 minute base, doubling each attempt, max 1 hour, up to 10 attempts

When a permanently-failed queue entry is cleaned up (after 30 days), Spotter clears the `lidarr_status` on the corresponding artist or album so the UI no longer shows a stale "Queued" badge.

### Queue Cleanup

The submitter runs cleanup on every cycle:
- **Submitted entries** are deleted after 7 days (they've done their job)
- **Permanently failed entries** (10+ attempts) are deleted after 30 days, and their entity's Lidarr status is cleared

## UI Status Indicators

Spotter shows a Lidarr status icon next to tracks in the library. The status is derived from the album level (Lidarr operates on albums, not individual tracks):

| Icon | Status | Meaning |
| :--- | :--- | :--- |
| Queue list (yellow) | `queued` | In Spotter's submission queue, not yet sent to Lidarr |
| Eye (blue) | `monitored` | In Lidarr, tracked but not fully downloaded |
| Download arrow (blue, pulsing) | `grabbed` | Actively downloading in Lidarr |
| Check badge (green) | `available` | Fully downloaded and available in Lidarr |
| Clock (yellow) | `pending` | Requested in Lidarr but not yet being processed |
| Exclamation (red) | `missing` | Lidarr knows about it but can't find it |

When a Lidarr base URL is configured and the artist has a Lidarr ID, the status icon links directly to the artist page in Lidarr.

## Setup

### Get API Key

1. Open Lidarr
2. Go to **Settings** > **General**
3. Copy the **API Key** from the Security section

### Configure

```bash
SPOTTER_LIDARR_BASE_URL=http://localhost:8686
SPOTTER_LIDARR_API_KEY=your_lidarr_api_key
```

## Configuration

| Variable | Description | Default |
| :--- | :--- | :--- |
| `SPOTTER_LIDARR_BASE_URL` | Lidarr instance URL | *Required* |
| `SPOTTER_LIDARR_API_KEY` | Lidarr API key | *Required* |
| `SPOTTER_LIDARR_QUEUE_MAX` | Max Lidarr download queue depth before pausing submissions | `50` |
| `SPOTTER_LIDARR_SUBMIT_INTERVAL` | How often the background submitter wakes to check and drain the queue | `3m` |

### Tuning Tips

- **Large library, fast connection**: You can increase `QUEUE_MAX` to 100+ if your indexers and download client can handle it.
- **Slow indexers or metered connection**: Lower `QUEUE_MAX` to 10-20 and increase `SUBMIT_INTERVAL` to 5-10 minutes.
- **The submitter is adaptive** — it won't submit faster than Lidarr can process. The interval just controls how often it *checks*.

## Network Configuration

### Docker

If Lidarr is on the same Docker network:

```bash
SPOTTER_LIDARR_BASE_URL=http://lidarr:8686
```

If on the host:

```bash
# Docker Desktop
SPOTTER_LIDARR_BASE_URL=http://host.docker.internal:8686

# Linux
SPOTTER_LIDARR_BASE_URL=http://172.17.0.1:8686
```

## Troubleshooting

### "Connection refused"

1. Verify Lidarr is running
2. Check the URL is correct
3. Ensure network connectivity between containers

### "Unauthorized"

1. Verify the API key
2. Check API access is enabled in Lidarr
3. Try regenerating the API key

### Stale "Queued" badges

If tracks show a yellow "Queued for Lidarr" badge that never changes:
1. Check the Spotter logs for `metric.lidarr.failed` entries
2. The submission may have permanently failed (bad MusicBrainz ID, artist name collision)
3. Permanently failed entries are cleaned up automatically after 30 days, which clears the badge
4. To clear immediately, you can delete the failed rows from the `lidarr_queues` table — the next enrichment cycle will clear the entity's status

### Queue not draining

If `metric.lidarr.backpressure` appears in logs:
1. Lidarr's download queue is full — wait for downloads to complete
2. Check Lidarr's Activity page for stuck downloads
3. Consider increasing `SPOTTER_LIDARR_QUEUE_MAX` if Lidarr is handling the load fine

### "Artist must not be empty"

Some MusicBrainz entries return empty artist names from Lidarr's lookup API. These are automatically detected as permanent failures and won't be retried. The corresponding album can still be enriched by other sources (Spotify, Last.fm, MusicBrainz directly).
