// Governing: SPEC music-provider-integration REQ "ListenBrainz Provider" (REQ-PROV-045, REQ-PROV-046, REQ-PROV-047),
// ADR-0016 (pluggable provider factory pattern)
package listenbrainz

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
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
)

// Provider implements the ListenBrainz provider skeleton (auth + registration).
// Listen-history sync, scrobbling, and playlist support arrive in later PRs.
type Provider struct {
	logger     *slog.Logger
	auth       *ent.ListenBrainzAuth
	baseURL    string
	httpClient *http.Client
}

// Governing: SPEC music-provider-integration REQ-PROV-001 (base Provider interface)
var _ providers.Provider = (*Provider)(nil)

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
	if err := p.doRequest(ctx, http.MethodGet, "/1/validate-token", token, &result); err != nil {
		return nil, fmt.Errorf("failed to validate listenbrainz token: %w", err)
	}
	return &result, nil
}

// doRequest performs an authenticated GET/POST against the ListenBrainz API
// with retry on transient failures.
// Governing: SPEC music-provider-integration REQ-PROV-047, AGENTS.md "External
// API Etiquette" — every request carries a descriptive User-Agent, and 429
// responses are honored by waiting the advertised Retry-After (or
// X-RateLimit-Reset-In) interval before retrying.
func (p *Provider) doRequest(ctx context.Context, method, path, token string, result interface{}) error {
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

		req, err := http.NewRequestWithContext(ctx, method, p.baseURL+path, nil)
		if err != nil {
			return err
		}
		// Governing: AGENTS.md "External API Etiquette" — shared descriptive User-Agent.
		req.Header.Set("User-Agent", httputil.UserAgent)
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
