// Governing: SPEC metadata-enrichment-pipeline REQ "ListenBrainz Enricher"
// (REQ-ENRICH-060 through REQ-ENRICH-064), ADR-0015 (type-keyed enricher
// registry with factory pattern)
package listenbrainz

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"time"

	"spotter/ent"
	"spotter/internal/config"
	"spotter/internal/enrichers"
	"spotter/internal/httputil"
	tagsutil "spotter/internal/tags"
)

const (
	defaultBaseURL = "https://api.listenbrainz.org"
	maxRetries     = 3
	// maxRateLimitWait caps how long a single 429 Retry-After pause may last
	// so a misbehaving server cannot stall the enrichment loop indefinitely.
	// Mirrors internal/providers/listenbrainz.
	maxRateLimitWait = 30 * time.Second
	// lookupConfidence is the confidence reported for /1/metadata/lookup
	// matches. The ListenBrainz MBID mapper does not return a match score,
	// but it only answers when its mapping is considered reliable, so a
	// fixed high-but-not-perfect confidence is reported.
	lookupConfidence = 0.9
	// popularityReferenceLog10 is the log10 listen count treated as "maximum
	// popularity" (10^7 listens) when scaling raw ListenBrainz listen counts
	// onto the 0-100 popularity scale shared with Spotify.
	popularityReferenceLog10 = 7.0
)

// errNotFound signals a 404 from the ListenBrainz API; callers treat it as
// "no data" rather than a pipeline error.
var errNotFound = errors.New("listenbrainz: not found")

// Enricher implements the ListenBrainz metadata enricher.
//
// ListenBrainz metadata endpoints (/1/metadata/*, /1/popularity/*) are
// public and require no user token, so — like the MusicBrainz enricher —
// this enricher is always available and is deliberately token-less. The
// ListenBrainz *provider* requires per-user auth because listen history is
// per-user data; enrichment metadata is global, and gating it on a pasted
// token would deny enrichment to users without ListenBrainz accounts for no
// benefit.
// Governing: SPEC metadata-enrichment-pipeline REQ-ENRICH-060
type Enricher struct {
	logger     *slog.Logger
	config     *config.Config
	httpClient *http.Client
	baseURL    string
}

// Ensure Enricher implements interfaces
var _ enrichers.Enricher = (*Enricher)(nil)
var _ enrichers.ArtistEnricher = (*Enricher)(nil)
var _ enrichers.TrackEnricher = (*Enricher)(nil)
var _ enrichers.IDMatcher = (*Enricher)(nil)

// New creates a new ListenBrainz enricher factory.
// Unlike credentialed enrichers (Last.fm, Fanart), the factory never returns
// nil: all endpoints used are anonymous public reads (rate limited but open),
// matching the MusicBrainz enricher convention.
// Governing: ADR-0015 (factory pattern for per-user instantiation),
// SPEC metadata-enrichment-pipeline REQ-ENRICH-060 (token-less availability)
func New(logger *slog.Logger, cfg *config.Config) enrichers.Factory {
	return func(ctx context.Context, user *ent.User) (enrichers.Enricher, error) {
		baseURL := defaultBaseURL
		if cfg != nil && cfg.ListenBrainz.APIURL != "" {
			baseURL = cfg.ListenBrainz.APIURL
		}

		return &Enricher{
			logger: logger,
			config: cfg,
			httpClient: &http.Client{
				Timeout: 30 * time.Second,
			},
			baseURL: baseURL,
		}, nil
	}
}

// WithBaseURL sets a custom base URL for the enricher (used for testing).
func (e *Enricher) WithBaseURL(url string) *Enricher {
	e.baseURL = url
	return e
}

// WithHTTPClient sets a custom HTTP client for the enricher (used for testing).
func (e *Enricher) WithHTTPClient(client *http.Client) *Enricher {
	e.httpClient = client
	return e
}

func (e *Enricher) Type() enrichers.Type {
	return enrichers.TypeListenBrainz
}

func (e *Enricher) Name() string {
	return "ListenBrainz"
}

func (e *Enricher) IsAvailable() bool {
	// Public metadata/popularity endpoints need no credentials.
	return true
}

// doRequest performs a request against the ListenBrainz API with retry on
// transient failures. body (may be nil) is sent as JSON; result (may be nil)
// receives the decoded JSON response.
// Governing: SPEC metadata-enrichment-pipeline REQ-ENRICH-063, AGENTS.md
// "External API Etiquette" — every request carries the shared descriptive
// User-Agent, and 429 responses are honored by waiting the advertised
// Retry-After (or X-RateLimit-Reset-In) interval before retrying, aborting
// outright when the advertised wait exceeds the cap.
func (e *Enricher) doRequest(ctx context.Context, method, path string, query url.Values, body, result interface{}) error {
	reqURL := e.baseURL + path
	if len(query) > 0 {
		reqURL += "?" + query.Encode()
	}

	var payload []byte
	if body != nil {
		var err error
		payload, err = json.Marshal(body)
		if err != nil {
			return fmt.Errorf("failed to encode listenbrainz request body: %w", err)
		}
	}

	var lastErr error
	var wait time.Duration
	var waitSet bool

	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			if !waitSet {
				// Exponential backoff for 5xx/network errors: 1s, 2s
				wait = time.Duration(1<<uint(attempt-1)) * time.Second
			}
			if wait > 0 {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(wait):
				}
			}
			e.logger.Info("retrying listenbrainz request", "attempt", attempt+1, "path", path)
		}
		wait, waitSet = 0, false

		var reqBody io.Reader
		if payload != nil {
			reqBody = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, method, reqURL, reqBody)
		if err != nil {
			return err
		}
		// Governing: AGENTS.md "External API Etiquette" — shared descriptive User-Agent.
		req.Header.Set("User-Agent", httputil.UserAgent)
		req.Header.Set("Accept", "application/json")
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}

		resp, err := e.httpClient.Do(req)
		if err != nil {
			lastErr = err
			continue
		}

		if resp.StatusCode == http.StatusOK {
			var decodeErr error
			if result != nil {
				decodeErr = json.NewDecoder(resp.Body).Decode(result)
			}
			e.closeBody(resp)
			if decodeErr != nil {
				return fmt.Errorf("failed to decode listenbrainz response: %w", decodeErr)
			}
			return nil
		}

		// Bounded read so a huge error page cannot balloon memory.
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		e.closeBody(resp)

		if resp.StatusCode == http.StatusNotFound {
			return errNotFound
		}

		lastErr = fmt.Errorf("listenbrainz api returned status %d: %s", resp.StatusCode, string(respBody))

		if resp.StatusCode == http.StatusTooManyRequests {
			// Governing: SPEC metadata-enrichment-pipeline REQ-ENRICH-063 —
			// never retry before the server-advertised pause. If the
			// advertised interval exceeds our cap, abort instead of
			// retrying early.
			w, ok := rateLimitWait(resp.Header)
			if !ok {
				return fmt.Errorf("listenbrainz api rate limited and advertised retry interval exceeds the %s cap: %w", maxRateLimitWait, lastErr)
			}
			wait, waitSet = w, true
			e.logger.Warn("listenbrainz rate limit hit, backing off", "wait", wait, "path", path)
			continue
		}

		// Retry 5xx errors; everything else is not retryable.
		if resp.StatusCode >= 500 {
			continue
		}
		return lastErr
	}

	return lastErr
}

func (e *Enricher) closeBody(resp *http.Response) {
	if err := resp.Body.Close(); err != nil {
		e.logger.Warn("failed to close response body", "error", err)
	}
}

// rateLimitWait derives the pause ListenBrainz asks for from a 429 response.
// It prefers Retry-After (delay-seconds or HTTP-date form), falls back to the
// ListenBrainz-specific X-RateLimit-Reset-In header, and defaults to 1s when
// neither is present or parseable. ok is false when the advertised interval
// exceeds maxRateLimitWait: the caller must abort rather than retry earlier
// than advertised. Mirrors internal/providers/listenbrainz.
// Governing: SPEC metadata-enrichment-pipeline REQ-ENRICH-063
func rateLimitWait(h http.Header) (wait time.Duration, ok bool) {
	advertised := time.Duration(-1)
	if v := h.Get("Retry-After"); v != "" {
		if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
			advertised = time.Duration(secs) * time.Second
		} else if t, err := http.ParseTime(v); err == nil {
			if d := time.Until(t); d > 0 {
				advertised = d
			} else {
				advertised = 0
			}
		}
	}
	if advertised < 0 {
		if v := h.Get("X-RateLimit-Reset-In"); v != "" {
			if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
				advertised = time.Duration(secs) * time.Second
			}
		}
	}
	if advertised < 0 {
		return time.Second, true
	}
	if advertised > maxRateLimitWait {
		return 0, false
	}
	return advertised, true
}

// ListenBrainz API response types.

// lbTag is one entry of a tag list in /1/metadata responses:
// {"count": 4, "tag": "trip hop", "genre_mbid": "..."}.
type lbTag struct {
	Count     int    `json:"count"`
	Tag       string `json:"tag"`
	GenreMBID string `json:"genre_mbid"`
}

// lbTagBlock groups tags by entity in /1/metadata responses:
// {"artist": [...], "recording": [...], "release_group": [...]}.
type lbTagBlock struct {
	Artist       []lbTag `json:"artist"`
	Recording    []lbTag `json:"recording"`
	ReleaseGroup []lbTag `json:"release_group"`
}

// lbLookupResponse is the response of GET /1/metadata/lookup, the MBID
// mapper. An unmatched lookup returns 200 with an empty JSON object, so all
// fields must be treated as optional:
//
//	{
//	  "artist_credit_name": "Rick Astley",
//	  "artist_mbids": ["db92a151-1ac2-438b-bc43-b82e149ddd50"],
//	  "recording_mbid": "b1a9c0e9-d987-4042-ae91-78d6a3267d69",
//	  "recording_name": "Never Gonna Give You Up",
//	  "release_mbid": "...", "release_name": "..."
//	}
type lbLookupResponse struct {
	ArtistCreditName string   `json:"artist_credit_name"`
	ArtistMBIDs      []string `json:"artist_mbids"`
	RecordingMBID    string   `json:"recording_mbid"`
	RecordingName    string   `json:"recording_name"`
	ReleaseMBID      string   `json:"release_mbid"`
	ReleaseName      string   `json:"release_name"`
}

// lbArtistMetadata is one element of the GET /1/metadata/artist response,
// which is a JSON array (one entry per requested MBID, unknown MBIDs
// omitted):
//
//	[{"artist_mbid": "...", "name": "Portishead", "type": "Group",
//	  "area": "United Kingdom", "begin_year": 1991, "rels": {...},
//	  "tag": {"artist": [{"count": 10, "tag": "trip hop"}]}}]
type lbArtistMetadata struct {
	ArtistMBID string     `json:"artist_mbid"`
	Name       string     `json:"name"`
	Type       string     `json:"type"`
	Area       string     `json:"area"`
	BeginYear  int        `json:"begin_year"`
	Tag        lbTagBlock `json:"tag"`
}

// lbRecordingMetadata is one value of the GET /1/metadata/recording
// response, which is a JSON object keyed by recording MBID (unknown MBIDs
// omitted):
//
//	{"<mbid>": {
//	   "artist": {"name": "...", "artists": [{"artist_mbid": "...", ...}]},
//	   "recording": {"name": "...", "length": 253000, "rels": [...]},
//	   "release": {"mbid": "...", "name": "...", "year": 1994},
//	   "tag": {"artist": [...], "recording": [{"count": 4, "tag": "trip hop"}]}}}
type lbRecordingMetadata struct {
	Artist struct {
		Name    string `json:"name"`
		Artists []struct {
			ArtistMBID string `json:"artist_mbid"`
			Name       string `json:"name"`
		} `json:"artists"`
	} `json:"artist"`
	Recording struct {
		Name   string `json:"name"`
		Length int    `json:"length"` // milliseconds
	} `json:"recording"`
	Release struct {
		MBID string `json:"mbid"`
		Name string `json:"name"`
		Year int    `json:"year"`
	} `json:"release"`
	Tag lbTagBlock `json:"tag"`
}

// lbArtistPopularity is one element of the POST /1/popularity/artist
// response. Request body: {"artist_mbids": ["...", ...]}; response is an
// array aligned with the request, with null counts for unknown artists:
//
//	[{"artist_mbid": "...", "total_listen_count": 1234, "total_user_count": 56}]
type lbArtistPopularity struct {
	ArtistMBID       string `json:"artist_mbid"`
	TotalListenCount *int64 `json:"total_listen_count"`
	TotalUserCount   *int64 `json:"total_user_count"`
}

// lbRecordingPopularity is one element of the POST /1/popularity/recording
// response. Request body: {"recording_mbids": ["...", ...]}; response is an
// array aligned with the request, with null counts for unknown recordings:
//
//	[{"recording_mbid": "...", "total_listen_count": 1234, "total_user_count": 56}]
type lbRecordingPopularity struct {
	RecordingMBID    string `json:"recording_mbid"`
	TotalListenCount *int64 `json:"total_listen_count"`
	TotalUserCount   *int64 `json:"total_user_count"`
}

// popularityScore maps a raw ListenBrainz total listen count onto the 0-100
// popularity scale used by ArtistData/TrackData (historically Spotify-fed).
// ListenBrainz reports absolute listen counts, not a bounded score, so the
// count is log10-scaled: 0 listens -> 0, 10^7 or more listens -> 100. This
// keeps the field comparable in magnitude to Spotify's popularity while
// preserving ordering between entities.
// Governing: SPEC metadata-enrichment-pipeline REQ-ENRICH-062
func popularityScore(listenCount int64) int {
	if listenCount <= 0 {
		return 0
	}
	score := int(math.Round(100 * math.Log10(float64(listenCount)+1) / popularityReferenceLog10))
	if score > 100 {
		score = 100
	}
	if score < 1 {
		score = 1 // any listens at all rank above "no data"
	}
	return score
}

// MatchArtist is not supported: ListenBrainz has no artist-by-name search
// endpoint (/1/metadata/lookup requires a recording name). The MusicBrainz
// enricher, which runs earlier in the default order, owns artist ID
// matching. Returns no match rather than an error so the pipeline falls
// through to other IDMatchers.
// Governing: SPEC metadata-enrichment-pipeline REQ-ENRICH-061
func (e *Enricher) MatchArtist(ctx context.Context, name string) (string, float64, error) {
	return "", 0, nil
}

// MatchAlbum is not supported: ListenBrainz cannot resolve a release from an
// album/artist pair without a recording. Returns no match (see MatchArtist).
// Governing: SPEC metadata-enrichment-pipeline REQ-ENRICH-061
func (e *Enricher) MatchAlbum(ctx context.Context, albumName, artistName string) (string, float64, error) {
	return "", 0, nil
}

// MatchTrack resolves a recording MBID via the ListenBrainz MBID mapper:
// GET /1/metadata/lookup?artist_name=...&recording_name=...[&release_name=...].
// Governing: SPEC metadata-enrichment-pipeline REQ-ENRICH-005 (IDMatcher),
// REQ-ENRICH-061 (lookup-based track matching)
func (e *Enricher) MatchTrack(ctx context.Context, trackName, artistName, albumName string) (string, float64, error) {
	if trackName == "" || artistName == "" {
		return "", 0, nil
	}

	result, err := e.lookup(ctx, artistName, trackName, albumName)
	if err != nil {
		return "", 0, err
	}
	if result == nil || result.RecordingMBID == "" {
		return "", 0, nil
	}

	e.logger.Debug("found ListenBrainz recording match",
		"track", trackName,
		"artist", artistName,
		"mbid", result.RecordingMBID,
		"confidence", lookupConfidence)

	return result.RecordingMBID, lookupConfidence, nil
}

// lookup calls GET /1/metadata/lookup. A nil result means no match.
func (e *Enricher) lookup(ctx context.Context, artistName, recordingName, releaseName string) (*lbLookupResponse, error) {
	params := url.Values{}
	params.Set("artist_name", artistName)
	params.Set("recording_name", recordingName)
	if releaseName != "" {
		params.Set("release_name", releaseName)
	}

	var result lbLookupResponse
	if err := e.doRequest(ctx, http.MethodGet, "/1/metadata/lookup", params, nil, &result); err != nil {
		if errors.Is(err, errNotFound) {
			return nil, nil
		}
		return nil, err
	}
	if result.RecordingMBID == "" {
		// The mapper returns 200 with an empty object on no match.
		return nil, nil
	}
	return &result, nil
}

// EnrichArtist fetches artist tags and popularity from ListenBrainz.
// It requires a MusicBrainz ID: ListenBrainz metadata endpoints are keyed by
// MBID, and MusicBrainz runs earlier in the default order to supply it.
// Skipped data (deliberately): bios (not served by ListenBrainz), similar
// artists (only available from the separate ListenBrainz Labs service, which
// has no stability guarantees), and images (not served at all).
// Governing: SPEC metadata-enrichment-pipeline REQ-ENRICH-002 (ArtistEnricher),
// REQ-ENRICH-062 (popularity), REQ-ENRICH-064 (MBID-keyed metadata)
func (e *Enricher) EnrichArtist(ctx context.Context, artist *ent.Artist) (*enrichers.ArtistData, error) {
	// Artist has string fields (not *string) - check for empty string
	if artist.MusicbrainzID == "" {
		e.logger.Debug("skipping ListenBrainz artist - no MusicBrainz ID", "artist", artist.Name)
		return nil, nil
	}

	e.logger.Debug("enriching artist from ListenBrainz", "name", artist.Name, "mbid", artist.MusicbrainzID)

	params := url.Values{}
	params.Set("artist_mbids", artist.MusicbrainzID)
	params.Set("inc", "tag")

	var metadata []lbArtistMetadata
	if err := e.doRequest(ctx, http.MethodGet, "/1/metadata/artist/", params, nil, &metadata); err != nil {
		if errors.Is(err, errNotFound) {
			return nil, nil
		}
		return nil, err
	}
	if len(metadata) == 0 {
		e.logger.Debug("no ListenBrainz data for artist", "artist", artist.Name)
		return nil, nil
	}

	meta := metadata[0]
	tags := tagNames(meta.Tag.Artist)

	// Governing: SPEC-0014 REQ "Enricher Integration", ADR-0015 (Pluggable Enricher Registry)
	// ListenBrainz tags originate from the MusicBrainz genre/tag taxonomy,
	// so they are typed "genre" like the MusicBrainz enricher's tags.
	var typedTags []tagsutil.TypedTag
	for _, t := range tags {
		typedTags = append(typedTags, tagsutil.TypedTag{Name: t, Type: "genre"})
	}

	result := &enrichers.ArtistData{
		Tags:      tags,
		TypedTags: typedTags,
	}

	// Popularity is best-effort: a failure must not discard the tag data.
	// Governing: SPEC metadata-enrichment-pipeline REQ-ENRICH-062
	if pop, err := e.artistPopularity(ctx, artist.MusicbrainzID); err != nil {
		e.logger.Warn("failed to fetch ListenBrainz artist popularity", "artist", artist.Name, "error", err)
	} else if pop != nil {
		result.Popularity = pop
	}

	e.logger.Debug("enriched artist from ListenBrainz",
		"artist", artist.Name,
		"tags", len(tags),
		"has_popularity", result.Popularity != nil)

	return result, nil
}

// GetArtistImages returns nil: ListenBrainz does not serve artist imagery.
func (e *Enricher) GetArtistImages(ctx context.Context, artist *ent.Artist) ([]enrichers.ImageData, error) {
	return nil, nil
}

// artistPopularity fetches the total listen count for an artist MBID via
// POST /1/popularity/artist and converts it to a 0-100 score. A nil result
// means ListenBrainz has no popularity data for the artist.
// Governing: SPEC metadata-enrichment-pipeline REQ-ENRICH-062
func (e *Enricher) artistPopularity(ctx context.Context, mbid string) (*int, error) {
	body := map[string][]string{"artist_mbids": {mbid}}

	var result []lbArtistPopularity
	if err := e.doRequest(ctx, http.MethodPost, "/1/popularity/artist", nil, body, &result); err != nil {
		if errors.Is(err, errNotFound) {
			return nil, nil
		}
		return nil, err
	}

	for _, entry := range result {
		if entry.ArtistMBID == mbid && entry.TotalListenCount != nil {
			score := popularityScore(*entry.TotalListenCount)
			return &score, nil
		}
	}
	return nil, nil
}

// EnrichTrack fetches recording tags, duration, and popularity from
// ListenBrainz. When the track lacks a MusicBrainz ID it is first resolved
// via the MBID mapper (MatchTrack). Skipped data (deliberately): audio
// features (BPM/energy/etc. are not served by the public API) and wiki-style
// summaries (not available).
// Governing: SPEC metadata-enrichment-pipeline REQ-ENRICH-004 (TrackEnricher),
// REQ-ENRICH-061 (lookup fallback), REQ-ENRICH-062 (popularity),
// REQ-ENRICH-064 (MBID-keyed metadata)
func (e *Enricher) EnrichTrack(ctx context.Context, track *ent.Track) (*enrichers.TrackData, error) {
	artistName := ""
	if track.Edges.Artist != nil {
		artistName = track.Edges.Artist.Name
	}

	// Track has *string fields (Nillable) - check for nil or empty
	mbid := ""
	if track.MusicbrainzID != nil {
		mbid = *track.MusicbrainzID
	}
	matched := false

	if mbid == "" {
		if artistName == "" {
			e.logger.Debug("skipping ListenBrainz track - no MBID and no artist name", "track", track.Name)
			return nil, nil
		}
		albumName := ""
		if track.Edges.Album != nil {
			albumName = track.Edges.Album.Name
		}
		var err error
		mbid, _, err = e.MatchTrack(ctx, track.Name, artistName, albumName)
		if err != nil {
			return nil, err
		}
		if mbid == "" {
			e.logger.Debug("no ListenBrainz match for track", "track", track.Name, "artist", artistName)
			return nil, nil
		}
		matched = true
	}

	e.logger.Debug("enriching track from ListenBrainz", "track", track.Name, "mbid", mbid)

	params := url.Values{}
	params.Set("recording_mbids", mbid)
	params.Set("inc", "artist tag release")

	var metadata map[string]lbRecordingMetadata
	if err := e.doRequest(ctx, http.MethodGet, "/1/metadata/recording/", params, nil, &metadata); err != nil {
		if errors.Is(err, errNotFound) {
			return nil, nil
		}
		return nil, err
	}

	meta, ok := metadata[mbid]
	if !ok {
		e.logger.Debug("no ListenBrainz data for track", "track", track.Name, "mbid", mbid)
		return nil, nil
	}

	tags := tagNames(meta.Tag.Recording)

	// Governing: SPEC-0014 REQ "Enricher Integration", ADR-0015 (Pluggable Enricher Registry)
	var typedTags []tagsutil.TypedTag
	for _, t := range tags {
		typedTags = append(typedTags, tagsutil.TypedTag{Name: t, Type: "genre"})
	}

	result := &enrichers.TrackData{
		Tags:      tags,
		TypedTags: typedTags,
	}

	if meta.Recording.Length > 0 {
		result.DurationMs = meta.Recording.Length
	}

	// Persist a freshly matched MBID so later enrichers can use it.
	if matched {
		result.MusicBrainzID = mbid
	}

	// Popularity is best-effort: a failure must not discard the tag data.
	// Governing: SPEC metadata-enrichment-pipeline REQ-ENRICH-062
	if pop, err := e.recordingPopularity(ctx, mbid); err != nil {
		e.logger.Warn("failed to fetch ListenBrainz recording popularity", "track", track.Name, "error", err)
	} else if pop != nil {
		result.Popularity = pop
	}

	e.logger.Debug("enriched track from ListenBrainz",
		"track", track.Name,
		"tags", len(tags),
		"has_popularity", result.Popularity != nil)

	return result, nil
}

// recordingPopularity fetches the total listen count for a recording MBID
// via POST /1/popularity/recording and converts it to a 0-100 score. A nil
// result means ListenBrainz has no popularity data for the recording.
// Governing: SPEC metadata-enrichment-pipeline REQ-ENRICH-062
func (e *Enricher) recordingPopularity(ctx context.Context, mbid string) (*int, error) {
	body := map[string][]string{"recording_mbids": {mbid}}

	var result []lbRecordingPopularity
	if err := e.doRequest(ctx, http.MethodPost, "/1/popularity/recording", nil, body, &result); err != nil {
		if errors.Is(err, errNotFound) {
			return nil, nil
		}
		return nil, err
	}

	for _, entry := range result {
		if entry.RecordingMBID == mbid && entry.TotalListenCount != nil {
			score := popularityScore(*entry.TotalListenCount)
			return &score, nil
		}
	}
	return nil, nil
}

// tagNames extracts non-empty tag names sorted by vote count (descending)
// so the most agreed-upon tags come first, matching how ListenBrainz ranks
// them in its own UI. Ties preserve API order via stable sort.
func tagNames(entries []lbTag) []string {
	sorted := make([]lbTag, len(entries))
	copy(sorted, entries)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].Count > sorted[j].Count
	})

	names := make([]string, 0, len(sorted))
	for _, t := range sorted {
		if t.Tag != "" {
			names = append(names, t.Tag)
		}
	}
	return names
}
