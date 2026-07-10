// Governing: ADR-0020 (error handling and resilience), SPEC error-handling REQ-ERR-002 (429/5xx retriable),
// AGENTS.md "External API Etiquette" (User-Agent, 429 handling)
// Tests for outbound HTTP etiquette in the Last.fm provider: the shared
// User-Agent header, Retry-After-driven 429 retries, a dedicated HTTP client,
// and regression coverage for the retry-loop body-handling bugs.
package lastfm

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"spotter/ent"
	"spotter/internal/config"
	"spotter/internal/httputil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newEtiquetteTestProvider(t *testing.T, serverURL string) *Provider {
	t.Helper()

	cfg := &config.Config{}
	cfg.LastFM.APIKey = "test-api-key"
	cfg.LastFM.SharedSecret = "test-secret"

	user := &ent.User{
		Username: "testuser",
		Edges: ent.UserEdges{
			LastfmAuth: &ent.LastFMAuth{
				Username:   "lastfm-user",
				SessionKey: "session-key-123",
			},
		},
	}

	return createTestProvider(t, cfg, user, serverURL)
}

func TestNew_ConfiguredHTTPClient(t *testing.T) {
	p := newEtiquetteTestProvider(t, "http://example.invalid")

	require.NotNil(t, p.httpClient)
	assert.NotSame(t, http.DefaultClient, p.httpClient,
		"provider must not use http.DefaultClient (it has no timeout)")
	assert.NotZero(t, p.httpClient.Timeout, "provider HTTP client must have a timeout")
}

func TestDoRequest_SetsUserAgent(t *testing.T) {
	tests := []struct {
		name   string
		method string
	}{
		{name: "GET", method: "GET"},
		{name: "POST", method: "POST"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotUserAgent string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotUserAgent = r.Header.Get("User-Agent")
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{}`))
			}))
			defer server.Close()

			p := newEtiquetteTestProvider(t, server.URL)
			err := p.doRequest(context.Background(), tt.method, map[string]string{"method": "user.getinfo"}, nil)
			require.NoError(t, err)
			assert.Equal(t, httputil.UserAgent, gotUserAgent)
		})
	}
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
			name:         "recovers after one 429",
			failures:     1,
			wantErr:      false,
			wantAttempts: 2,
		},
		{
			name:          "gives up after exhausting retries",
			failures:      10,
			wantErr:       true,
			wantErrSubstr: "429",
			wantAttempts:  3,
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
				_, _ = w.Write([]byte(`{}`))
			}))
			defer server.Close()

			p := newEtiquetteTestProvider(t, server.URL)
			err := p.doRequest(context.Background(), "GET", map[string]string{"method": "user.getinfo"}, nil)

			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErrSubstr)
			} else {
				require.NoError(t, err)
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

	p := newEtiquetteTestProvider(t, server.URL)
	err := p.doRequest(ctx, "GET", map[string]string{"method": "user.getinfo"}, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}

// TestDoRequest_Regression_PostBodyResentOnRetry covers the latent bug where
// doRequest built the *http.Request once, outside the retry loop, and reused
// it across attempts. A POST body is consumed by the first attempt, so the
// retry fails at the transport with "http: ContentLength=N with Body length 0"
// (on a reused keep-alive connection Go's transport can silently rescue the
// request by rewinding via GetBody, so this test disables keep-alives to model
// the fresh-connection case where no rescue is possible) — the retry loop then
// exhausted itself without ever resending the payload.
//
// Original issue: spotter HTTP client hygiene backlog bundle
// (spotter-8s8/9r5/z0f/zvb/3pa/ahc/f99/nq9), latent bug item.
// This test fails without the fix (request rebuilt per attempt) and passes with it.
func TestDoRequest_Regression_PostBodyResentOnRetry(t *testing.T) {
	var mu sync.Mutex
	var bodies []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)

		mu.Lock()
		bodies = append(bodies, string(body))
		attempt := len(bodies)
		mu.Unlock()

		if attempt == 1 {
			// Transient server error: the client must retry with a full body.
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	p := newEtiquetteTestProvider(t, server.URL)
	p.WithHTTPClient(&http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{DisableKeepAlives: true},
	})
	err := p.doRequest(context.Background(), "POST", map[string]string{
		"method": "auth.getSession",
		"token":  "test-token",
	}, nil)
	require.NoError(t, err, "retry after 500 must succeed instead of failing on a consumed body")

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, bodies, 2, "expected exactly one retry")
	assert.NotEmpty(t, bodies[0])
	assert.Equal(t, bodies[0], bodies[1], "retried POST must resend the identical, complete body")
	assert.Contains(t, bodies[1], "method=auth.getSession")
}

// trackedBody wraps a response body and records when it is closed.
type trackedBody struct {
	io.ReadCloser
	closed atomic.Bool
}

func (b *trackedBody) Close() error {
	b.closed.Store(true)
	return b.ReadCloser.Close()
}

// closeTrackingTransport wraps the default transport and, before each request
// after the first, records whether all previously returned response bodies
// have already been closed.
type closeTrackingTransport struct {
	mu                sync.Mutex
	bodies            []*trackedBody
	priorBodiesClosed []bool
}

func (tr *closeTrackingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	tr.mu.Lock()
	if len(tr.bodies) > 0 {
		allClosed := true
		for _, b := range tr.bodies {
			if !b.closed.Load() {
				allClosed = false
				break
			}
		}
		tr.priorBodiesClosed = append(tr.priorBodiesClosed, allClosed)
	}
	tr.mu.Unlock()

	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		return nil, err
	}

	tb := &trackedBody{ReadCloser: resp.Body}
	resp.Body = tb

	tr.mu.Lock()
	tr.bodies = append(tr.bodies, tb)
	tr.mu.Unlock()

	return resp, nil
}

// TestDoRequest_Regression_BodyClosedBeforeRetry covers the latent bug where
// doRequest stacked `defer resp.Body.Close()` inside the retry loop, so
// response bodies from failed attempts stayed open (holding connections)
// until the function returned instead of being closed before the next
// attempt.
//
// Original issue: spotter HTTP client hygiene backlog bundle
// (spotter-8s8/9r5/z0f/zvb/3pa/ahc/f99/nq9), latent bug item.
// This test fails without the fix (bodies closed promptly in the loop) and passes with it.
func TestDoRequest_Regression_BodyClosedBeforeRetry(t *testing.T) {
	var mu sync.Mutex
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		attempts++
		n := attempts
		mu.Unlock()
		if n <= 2 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	transport := &closeTrackingTransport{}
	p := newEtiquetteTestProvider(t, server.URL)
	p.WithHTTPClient(&http.Client{Timeout: 5 * time.Second, Transport: transport})

	err := p.doRequest(context.Background(), "GET", map[string]string{"method": "user.getinfo"}, nil)
	require.NoError(t, err)

	transport.mu.Lock()
	defer transport.mu.Unlock()
	require.Len(t, transport.bodies, 3, "expected two failed attempts and one success")
	require.Len(t, transport.priorBodiesClosed, 2)
	for i, closed := range transport.priorBodiesClosed {
		assert.True(t, closed,
			"response bodies from earlier attempts must be closed before retry %d, not deferred until return", i+2)
	}
	for i, b := range transport.bodies {
		assert.True(t, b.closed.Load(), "response body %d must be closed by the time doRequest returns", i+1)
	}
}
