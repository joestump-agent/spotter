// Package httputil holds shared conventions for outbound HTTP requests to
// external APIs: a common User-Agent string and 429 (Too Many Requests)
// Retry-After handling.
//
// Governing: ADR-0020 (error handling and resilience),
// SPEC error-handling REQ-ERR-002 (HTTP 429 is retriable),
// AGENTS.md "External API Etiquette" (descriptive User-Agent, graceful 429 handling)
package httputil

import (
	"context"
	"net/http"
	"strconv"
	"time"
)

// ClientName and ClientVersion identify Spotter to external APIs that take a
// split client name + version (e.g. the ListenBrainz submission payload's
// submission_client / submission_client_version fields).
const (
	ClientName    = "Spotter"
	ClientVersion = "1.0.0"
)

// UserAgent is the User-Agent header value sent on every outbound request to
// an external API.
const UserAgent = ClientName + "/" + ClientVersion

const (
	// MaxRateLimitRetries is the number of retry attempts after a 429 response.
	MaxRateLimitRetries = 3
	// DefaultRetryAfter is the wait applied when a 429 response carries no
	// usable Retry-After header.
	DefaultRetryAfter = 5 * time.Second
	// MaxRetryAfter caps the wait requested by a Retry-After header so a
	// misbehaving API cannot stall a sync indefinitely.
	MaxRetryAfter = 60 * time.Second
)

// RetryAfter returns how long to wait before retrying a rate-limited request,
// based on the response's Retry-After header. Both RFC 9110 forms are
// supported: delay-seconds and HTTP-date. Missing, unparseable, zero, or
// negative values fall back to DefaultRetryAfter; values above MaxRetryAfter
// are capped.
func RetryAfter(resp *http.Response) time.Duration {
	retryAfter := DefaultRetryAfter
	if raHeader := resp.Header.Get("Retry-After"); raHeader != "" {
		if seconds, err := strconv.Atoi(raHeader); err == nil {
			retryAfter = time.Duration(seconds) * time.Second
		} else if t, err := http.ParseTime(raHeader); err == nil {
			retryAfter = time.Until(t)
		}
		if retryAfter > MaxRetryAfter {
			retryAfter = MaxRetryAfter
		}
	}
	// Guard against zero or negative Retry-After values
	if retryAfter <= 0 {
		retryAfter = DefaultRetryAfter
	}
	return retryAfter
}

// Sleep waits for the given duration or until the context is cancelled,
// returning the context's error in the latter case.
func Sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
