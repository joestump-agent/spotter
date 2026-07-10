// Governing: ADR-0020 (error handling and resilience), SPEC error-handling REQ-ERR-002 (429 retriable),
// AGENTS.md "External API Etiquette" (User-Agent, 429 handling)
// Tests for outbound HTTP etiquette in the Spotify provider: the shared
// User-Agent header and Retry-After-driven 429 retries.
package spotify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"spotter/internal/httputil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOutboundRequests_SetUserAgent(t *testing.T) {
	var mu sync.Mutex
	userAgents := map[string]string{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		userAgents[r.URL.Path] = r.Header.Get("User-Agent")
		mu.Unlock()
		writeJSON(t, w, map[string]any{"id": "user-123", "display_name": "Test User"})
	}))
	defer server.Close()

	p := newTestProvider(t, server.URL, validAuth(), nil)

	t.Run("send via doAPIRequest", func(t *testing.T) {
		resp, err := p.doAPIRequest(context.Background(), http.MethodGet, server.URL+"/v1/me/api", nil)
		require.NoError(t, err)
		p.closeBody(resp)

		mu.Lock()
		defer mu.Unlock()
		assert.Equal(t, httputil.UserAgent, userAgents["/v1/me/api"])
	})

	t.Run("fetchUserProfile", func(t *testing.T) {
		_, err := p.fetchUserProfile(context.Background(), "some-access-token")
		require.NoError(t, err)

		mu.Lock()
		defer mu.Unlock()
		assert.Equal(t, httputil.UserAgent, userAgents["/v1/me"])
	})
}

func TestDoAPIRequest_429RetryAfter(t *testing.T) {
	tests := []struct {
		name         string
		failures     int // number of leading 429 responses
		wantStatus   int
		wantAttempts int
	}{
		{
			name:         "recovers after one 429",
			failures:     1,
			wantStatus:   http.StatusOK,
			wantAttempts: 2,
		},
		{
			name:         "returns 429 response after exhausting retries",
			failures:     10,
			wantStatus:   http.StatusTooManyRequests,
			wantAttempts: httputil.MaxRateLimitRetries + 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var mu sync.Mutex
			attempts := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				attempts++
				n := attempts
				mu.Unlock()
				if n <= tt.failures {
					w.Header().Set("Retry-After", "1")
					w.WriteHeader(http.StatusTooManyRequests)
					return
				}
				writeJSON(t, w, map[string]any{"items": []any{}})
			}))
			defer server.Close()

			p := newTestProvider(t, server.URL, validAuth(), nil)
			resp, err := p.doAPIRequest(context.Background(), http.MethodGet, server.URL+"/v1/me/player/recently-played", nil)
			require.NoError(t, err)
			p.closeBody(resp)

			assert.Equal(t, tt.wantStatus, resp.StatusCode)
			assert.Equal(t, tt.wantAttempts, attempts)
		})
	}
}

func TestDoAPIRequest_429ContextCancelled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	p := newTestProvider(t, server.URL, validAuth(), nil)
	_, err := p.doAPIRequest(ctx, http.MethodGet, server.URL+"/v1/me", nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}
