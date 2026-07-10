// Governing: ADR-0020 (error handling and resilience), SPEC error-handling REQ-ERR-002 (429 retriable),
// AGENTS.md "External API Etiquette" (User-Agent, 429 handling)
// Tests for outbound HTTP etiquette in the Fanart.tv enricher: the shared
// User-Agent header and Retry-After-driven 429 retries.
package fanart

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"spotter/internal/config"
	"spotter/internal/httputil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newEtiquetteTestEnricher builds an Enricher pointed at a test server.
func newEtiquetteTestEnricher(serverURL string) *Enricher {
	return &Enricher{
		logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		config:     &config.Config{},
		httpClient: &http.Client{Timeout: 5 * time.Second},
		apiKey:     "test-api-key",
		baseURL:    serverURL,
	}
}

func TestDoRequest_SetsUserAgent(t *testing.T) {
	var gotUserAgent string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUserAgent = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name": "Test Artist"}`))
	}))
	defer server.Close()

	e := newEtiquetteTestEnricher(server.URL)
	_, err := e.doRequest(context.Background(), "music/mbid-123")
	require.NoError(t, err)
	assert.Equal(t, httputil.UserAgent, gotUserAgent)
}

func TestDoRequest_429RetryAfter(t *testing.T) {
	tests := []struct {
		name          string
		failures      int // number of leading 429 responses
		wantErr       bool
		wantErrSubstr string
		wantAttempts  int
	}{
		{
			name:         "recovers after two 429s",
			failures:     2,
			wantErr:      false,
			wantAttempts: 3,
		},
		{
			name:          "gives up after exhausting retries",
			failures:      10,
			wantErr:       true,
			wantErrSubstr: "rate limited",
			wantAttempts:  httputil.MaxRateLimitRetries + 1,
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
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"name": "Test Artist"}`))
			}))
			defer server.Close()

			e := newEtiquetteTestEnricher(server.URL)
			data, err := e.doRequest(context.Background(), "music/mbid-123")

			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErrSubstr)
			} else {
				require.NoError(t, err)
				assert.NotEmpty(t, data)
			}
			assert.Equal(t, tt.wantAttempts, attempts)
		})
	}
}

func TestDoRequest_429ContextCancelled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	e := newEtiquetteTestEnricher(server.URL)
	_, err := e.doRequest(ctx, "music/mbid-123")
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}

// The pre-existing 404 and non-200 behavior must survive the retry rework.
func TestDoRequest_NotFoundStillReturnsNil(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	e := newEtiquetteTestEnricher(server.URL)
	data, err := e.doRequest(context.Background(), "music/unknown")
	require.NoError(t, err)
	assert.Nil(t, data)
}
