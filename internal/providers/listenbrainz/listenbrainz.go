// Governing: SPEC music-provider-integration REQ "ListenBrainz Provider" (REQ-PROV-045, REQ-PROV-046, REQ-PROV-047, REQ-PROV-048),
// ADR-0016 (pluggable provider factory pattern)
package listenbrainz

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"spotter/ent"
	"spotter/internal/config"
	"spotter/internal/httputil"
	"spotter/internal/providers"
)

const (
	defaultAPIBaseURL = "https://api.listenbrainz.org"
	maxRetries        = 3
	// maxRateLimitWait caps how long a single 429 Retry-After pause may last
	// so a misbehaving server cannot stall a sync worker indefinitely.
	maxRateLimitWait = 30 * time.Second
	// listensPageSize is the per-request listen count. The ListenBrainz API
	// caps count at MAX_ITEMS_PER_GET (100).
	// Governing: SPEC music-provider-integration REQ-PROV-048
	listensPageSize = 100
)

// MaxListensPerRequest is the maximum number of listens a single
// POST /1/submit-listens request may carry. The ListenBrainz API documents
// this limit as MAX_LISTENS_PER_REQUEST = 1000; larger payloads are rejected.
// SubmitListens splits its input into batches of at most this size, and the
// syncer uses the same constant so each persisted submission chunk maps to
// exactly one API request.
// Governing: SPEC music-provider-integration REQ-PROV-049 (batch limit)
const MaxListensPerRequest = 1000

// Provider implements the ListenBrainz provider (auth, listen-history sync,
// and listen submission). Playlist support arrives in later PRs.
type Provider struct {
	logger     *slog.Logger
	auth       *ent.ListenBrainzAuth
	baseURL    string
	httpClient *http.Client
}

// Governing: SPEC music-provider-integration REQ-PROV-001 (base Provider interface),
// REQ-PROV-048 (ListenBrainz implements HistoryFetcher),
// REQ-PROV-049 (ListenBrainz implements ListenSubmitter)
var _ providers.Provider = (*Provider)(nil)
var _ providers.HistoryFetcher = (*Provider)(nil)
var _ providers.ListenSubmitter = (*Provider)(nil)

// Governing: ADR-0016 (pluggable provider factory), SPEC music-provider-integration REQ-PROV-011 (nil,nil if unconfigured),
// REQ-PROV-012 (credentials from user.Edges.ListenbrainzAuth), REQ-PROV-045
// New returns a factory that creates ListenBrainz providers for a given user.
func New(logger *slog.Logger, cfg *config.Config) providers.Factory {
	return func(ctx context.Context, user *ent.User) (providers.Provider, error) {
		// Check if the user has ListenBrainz authentication data.
		if user.Edges.ListenbrainzAuth == nil {
			return nil, nil
		}

		p := newProvider(logger, cfg)
		p.auth = user.Edges.ListenbrainzAuth
		return p, nil
	}
}

// NewTokenValidator returns a Provider without user context, used by the
// connect handler to validate a pasted token before persisting it.
// Governing: SPEC music-provider-integration REQ-PROV-046 (validate-token on connect)
func NewTokenValidator(logger *slog.Logger, cfg *config.Config) *Provider {
	return newProvider(logger, cfg)
}

func newProvider(logger *slog.Logger, cfg *config.Config) *Provider {
	l := logger
	if l == nil {
		l = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	baseURL := defaultAPIBaseURL
	if cfg != nil && cfg.ListenBrainz.APIURL != "" {
		baseURL = cfg.ListenBrainz.APIURL
	}
	return &Provider{
		logger:     l,
		baseURL:    baseURL,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// WithBaseURL sets a custom base URL (used for testing).
func (p *Provider) WithBaseURL(url string) *Provider {
	p.baseURL = url
	return p
}

// WithHTTPClient sets a custom HTTP client (used for testing).
func (p *Provider) WithHTTPClient(client *http.Client) *Provider {
	p.httpClient = client
	return p
}

// Governing: SPEC music-provider-integration REQ-PROV-001, REQ-PROV-045
func (p *Provider) Type() providers.Type {
	return providers.TypeListenBrainz
}

// Username returns the ListenBrainz username of the connected user, or ""
// when the provider was built without user context (e.g. NewTokenValidator).
func (p *Provider) Username() string {
	if p.auth == nil {
		return ""
	}
	return p.auth.Username
}

// ValidateTokenResult is the response of the ListenBrainz validate-token endpoint.
type ValidateTokenResult struct {
	Code     int    `json:"code"`
	Message  string `json:"message"`
	Valid    bool   `json:"valid"`
	UserName string `json:"user_name"`
}

// Governing: SPEC music-provider-integration REQ-PROV-046 (GET /1/validate-token with Authorization: Token <token>)
// ValidateToken checks a user token against the ListenBrainz validate-token
// endpoint. A reachable API always yields a result (Valid reports whether the
// token is usable); an error indicates the check itself could not be performed.
func (p *Provider) ValidateToken(ctx context.Context, token string) (*ValidateTokenResult, error) {
	if token == "" {
		return nil, fmt.Errorf("listenbrainz token is required")
	}

	var result ValidateTokenResult
	if err := p.doRequest(ctx, http.MethodGet, "/1/validate-token", token, nil, &result); err != nil {
		return nil, fmt.Errorf("failed to validate listenbrainz token: %w", err)
	}
	return &result, nil
}

// listensResponse mirrors the GET /1/user/{username}/listens payload.
type listensResponse struct {
	Payload struct {
		Count   int `json:"count"`
		Listens []struct {
			ListenedAt    int64  `json:"listened_at"`
			RecordingMsid string `json:"recording_msid"`
			TrackMetadata struct {
				ArtistName     string `json:"artist_name"`
				TrackName      string `json:"track_name"`
				ReleaseName    string `json:"release_name"`
				AdditionalInfo struct {
					RecordingMbid string `json:"recording_mbid"`
					RecordingMsid string `json:"recording_msid"`
					DurationMs    int    `json:"duration_ms"`
					// Duration is the track length in seconds; some submitting
					// clients populate it instead of duration_ms.
					Duration  int    `json:"duration"`
					ISRC      string `json:"isrc"`
					OriginURL string `json:"origin_url"`
				} `json:"additional_info"`
			} `json:"track_metadata"`
		} `json:"listens"`
	} `json:"payload"`
}

// Governing: SPEC music-provider-integration REQ-PROV-048 (listens endpoint,
// backwards max_ts pagination, client-side since bound), REQ-PROV-002
// (batched callback contract)
// GetRecentListens retrieves listens played after the given timestamp.
//
// The ListenBrainz listens endpoint returns listens newest-first and rejects
// requests that combine min_ts and max_ts, so pagination walks backwards:
// each page's oldest listened_at plus one second becomes the next request's
// max_ts. max_ts is EXCLUSIVE (strictly-older listens only), so passing
// oldest itself would permanently drop any listens sharing the boundary
// timestamp that fell past the page cut; oldest+1 re-fetches the boundary
// second instead, and the idempotent persist layer absorbs the re-delivered
// duplicates. The since bound is enforced client-side: listens strictly
// before since are already synced and are not delivered, while listens AT
// since are delivered (the watermark second may hold not-yet-synced ties).
// The loop terminates when a page is empty or short, when a listen strictly
// before since appears, or when the cursor fails to strictly decrease
// (>= 100 listens sharing one timestamp, or a misbehaving server re-serving
// the same page).
func (p *Provider) GetRecentListens(ctx context.Context, since time.Time, callback func([]providers.Track) error) error {
	p.logger.Info("fetching recent listens from listenbrainz", "username", p.auth.Username, "since", since)

	basePath := "/1/user/" + url.PathEscape(p.auth.Username) + "/listens"
	var maxTS int64 // 0 = unset (first page starts at the newest listen)

	for {
		query := url.Values{}
		query.Set("count", strconv.Itoa(listensPageSize))
		if maxTS > 0 {
			query.Set("max_ts", strconv.FormatInt(maxTS, 10))
		}

		var result listensResponse
		if err := p.doRequest(ctx, http.MethodGet, basePath+"?"+query.Encode(), p.auth.Token, nil, &result); err != nil {
			return fmt.Errorf("failed to fetch listenbrainz listens: %w", err)
		}

		listens := result.Payload.Listens
		p.logger.Debug("fetched listens page", "count", len(listens), "max_ts", maxTS)
		if len(listens) == 0 {
			return nil
		}

		reachedSince := false
		oldest := int64(0)
		var tracks []providers.Track
		for _, l := range listens {
			if l.ListenedAt <= 0 {
				// Defensive: a listen without a timestamp cannot be ordered
				// or deduplicated, and would corrupt the pagination cursor.
				p.logger.Warn("skipping listen without listened_at", "track", l.TrackMetadata.TrackName)
				continue
			}
			if oldest == 0 || l.ListenedAt < oldest {
				oldest = l.ListenedAt
			}

			playedAt := time.Unix(l.ListenedAt, 0).UTC()
			// Governing: SPEC listen-playlist-sync REQ-SYNC-020 — listens
			// strictly before the since watermark are already synced. Listens
			// AT since are still delivered: the watermark second may hold ties
			// that were never synced, dropping them would lose them forever,
			// and re-delivering the already-synced one is safe because
			// persistListens de-duplicates idempotently (SPEC
			// listen-playlist-sync REQ-SYNC-021).
			if !since.IsZero() && playedAt.Before(since) {
				reachedSince = true
				continue
			}

			info := l.TrackMetadata.AdditionalInfo
			// Prefer the MusicBrainz recording MBID as the stable ID, then the
			// MessyBrainz MSID; fall back to the listen-level MSID.
			id := info.RecordingMbid
			if id == "" {
				id = info.RecordingMsid
			}
			if id == "" {
				id = l.RecordingMsid
			}

			// Prefer duration_ms; fall back to duration (seconds), which some
			// submitting clients send instead.
			durationMs := info.DurationMs
			if durationMs == 0 && info.Duration > 0 {
				durationMs = info.Duration * 1000
			}

			tracks = append(tracks, providers.Track{
				ID:         id,
				Name:       l.TrackMetadata.TrackName,
				Artist:     l.TrackMetadata.ArtistName,
				Album:      l.TrackMetadata.ReleaseName,
				DurationMs: durationMs,
				PlayedAt:   playedAt,
				URL:        info.OriginURL,
				ISRC:       info.ISRC,
			})
		}

		if len(tracks) > 0 {
			if err := callback(tracks); err != nil {
				return err
			}
		}

		if reachedSince || len(listens) < listensPageSize {
			return nil
		}
		// max_ts is EXCLUSIVE, so the tie-safe cursor is oldest+1: passing
		// oldest itself would silently drop any listens that share oldest's
		// timestamp but fell past the page boundary. Re-fetching the boundary
		// second re-delivers at most one second of listens, which the
		// idempotent persist layer de-duplicates.
		next := oldest + 1
		if oldest == 0 || (maxTS > 0 && next >= maxTS) {
			// No usable cursor, or the cursor failed to STRICTLY decrease.
			// On a well-behaved server every listen satisfies listened_at <
			// maxTS, so next <= maxTS always; next == maxTS means the entire
			// page shares the boundary timestamp (>= 100 listens in one
			// second) and re-requesting would re-serve it forever. The same
			// guard also stops a misbehaving server that ignores max_ts.
			p.logger.Warn("listenbrainz pagination cursor did not strictly decrease, stopping", "max_ts", maxTS, "oldest", oldest)
			return nil
		}
		maxTS = next
	}
}

// submitAdditionalInfo carries optional per-listen metadata for submission.
type submitAdditionalInfo struct {
	DurationMs       int    `json:"duration_ms,omitempty"`
	ISRC             string `json:"isrc,omitempty"`
	OriginURL        string `json:"origin_url,omitempty"`
	SubmissionClient string `json:"submission_client,omitempty"`
}

// submitTrackMetadata mirrors the track_metadata object of the
// POST /1/submit-listens payload.
type submitTrackMetadata struct {
	ArtistName     string                `json:"artist_name"`
	TrackName      string                `json:"track_name"`
	ReleaseName    string                `json:"release_name,omitempty"`
	AdditionalInfo *submitAdditionalInfo `json:"additional_info,omitempty"`
}

// submitListen is one element of the submit-listens payload array.
type submitListen struct {
	ListenedAt    int64               `json:"listened_at"`
	TrackMetadata submitTrackMetadata `json:"track_metadata"`
}

// submitListensRequest is the POST /1/submit-listens request body.
type submitListensRequest struct {
	ListenType string         `json:"listen_type"`
	Payload    []submitListen `json:"payload"`
}

// Governing: SPEC music-provider-integration REQ-PROV-049 (submit-listens with
// listen_type "import", batches of at most MaxListensPerRequest)
// SubmitListens pushes listens that originated from other sources to
// ListenBrainz via POST /1/submit-listens. The input is split into batches of
// at most MaxListensPerRequest listens; listen_type "import" is used because
// the listens are historical plays, not live "playing now" events. Listens
// without a track name, artist name, or played-at timestamp are skipped
// (ListenBrainz requires all three). An error is returned as soon as a batch
// fails; already-accepted batches stay accepted, and re-submitting them later
// is safe because ListenBrainz de-duplicates identical listens server-side.
func (p *Provider) SubmitListens(ctx context.Context, listens []providers.Track) error {
	// Filter out listens ListenBrainz cannot represent before batching so
	// batch boundaries line up with what is actually sent.
	payload := make([]submitListen, 0, len(listens))
	for _, l := range listens {
		if l.Name == "" || l.Artist == "" || l.PlayedAt.IsZero() {
			p.logger.Warn("skipping listen not submittable to listenbrainz",
				"track", l.Name, "artist", l.Artist, "played_at", l.PlayedAt)
			continue
		}
		payload = append(payload, submitListen{
			ListenedAt: l.PlayedAt.Unix(),
			TrackMetadata: submitTrackMetadata{
				ArtistName:  l.Artist,
				TrackName:   l.Name,
				ReleaseName: l.Album,
				AdditionalInfo: &submitAdditionalInfo{
					DurationMs:       l.DurationMs,
					ISRC:             l.ISRC,
					OriginURL:        l.URL,
					SubmissionClient: httputil.UserAgent,
				},
			},
		})
	}
	if len(payload) == 0 {
		return nil
	}

	p.logger.Info("submitting listens to listenbrainz", "username", p.auth.Username, "count", len(payload))

	for start := 0; start < len(payload); start += MaxListensPerRequest {
		end := start + MaxListensPerRequest
		if end > len(payload) {
			end = len(payload)
		}
		body, err := json.Marshal(submitListensRequest{
			ListenType: "import",
			Payload:    payload[start:end],
		})
		if err != nil {
			return fmt.Errorf("failed to encode listenbrainz submit payload: %w", err)
		}
		if err := p.doRequest(ctx, http.MethodPost, "/1/submit-listens", p.auth.Token, body, nil); err != nil {
			return fmt.Errorf("failed to submit listens to listenbrainz: %w", err)
		}
	}
	return nil
}

// doRequest performs an authenticated GET/POST against the ListenBrainz API
// with retry on transient failures. body is the JSON request body (nil for
// GET); it is kept as a byte slice and a fresh bytes.Reader is built on EVERY
// attempt, so retries after a 429 or 5xx resend the complete body instead of
// an already-drained reader.
// Governing: SPEC music-provider-integration REQ-PROV-047, AGENTS.md "External
// API Etiquette" — every request carries a descriptive User-Agent, and 429
// responses are honored by waiting the advertised Retry-After (or
// X-RateLimit-Reset-In) interval before retrying.
func (p *Provider) doRequest(ctx context.Context, method, path, token string, body []byte, result interface{}) error {
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
			p.logger.Info("retrying listenbrainz request", "attempt", attempt+1, "path", path)
		}
		wait, waitSet = 0, false

		// Rebuild the body reader per attempt: an io.Reader consumed by a
		// failed attempt cannot be resent, so each retry gets a fresh one.
		var bodyReader io.Reader
		if body != nil {
			bodyReader = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, p.baseURL+path, bodyReader)
		if err != nil {
			return err
		}
		// Governing: AGENTS.md "External API Etiquette" — shared descriptive User-Agent.
		req.Header.Set("User-Agent", httputil.UserAgent)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if token != "" {
			// Governing: SPEC music-provider-integration REQ-PROV-046 (Authorization: Token <token>)
			req.Header.Set("Authorization", "Token "+token)
		}

		resp, err := p.httpClient.Do(req)
		if err != nil {
			lastErr = err
			continue
		}

		if resp.StatusCode == http.StatusOK {
			var decodeErr error
			if result != nil {
				decodeErr = json.NewDecoder(resp.Body).Decode(result)
			}
			p.closeBody(resp)
			if decodeErr != nil {
				// Governing: SPEC error-handling REQ-ERR-003 (unparseable response body is fatal)
				return fmt.Errorf("failed to decode listenbrainz response: %w: %w", providers.ErrMalformedResponse, decodeErr)
			}
			return nil
		}

		// Try to read body for error details (bounded so a huge error page
		// cannot balloon memory).
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		p.closeBody(resp)
		lastErr = fmt.Errorf("listenbrainz api returned status %d: %s", resp.StatusCode, string(body))

		if resp.StatusCode == http.StatusTooManyRequests {
			// Governing: SPEC music-provider-integration REQ-PROV-047 — never
			// retry before the server-advertised pause. If the advertised
			// interval exceeds our cap, abort instead of retrying early.
			w, ok := rateLimitWait(resp.Header)
			if !ok {
				return fmt.Errorf("listenbrainz api rate limited and advertised retry interval exceeds the %s cap: %w", maxRateLimitWait, lastErr)
			}
			wait, waitSet = w, true
			p.logger.Warn("listenbrainz rate limit hit, backing off", "wait", wait, "path", path)
			continue
		}

		// Retry other 5xx errors; everything else is not retryable.
		if resp.StatusCode >= 500 {
			continue
		}
		return lastErr
	}

	return lastErr
}

func (p *Provider) closeBody(resp *http.Response) {
	if err := resp.Body.Close(); err != nil {
		p.logger.Warn("failed to close response body", "error", err)
	}
}

// rateLimitWait derives the pause ListenBrainz asks for from a 429 response.
// It prefers Retry-After (delay-seconds or HTTP-date form), falls back to the
// ListenBrainz-specific X-RateLimit-Reset-In header, and defaults to 1s when
// neither is present or parseable. ok is false when the advertised interval
// exceeds maxRateLimitWait: REQ-PROV-047 forbids retrying earlier than
// advertised, so the caller must abort rather than retry with a capped wait.
// Governing: SPEC music-provider-integration REQ-PROV-047
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
