package listenbrainz

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"spotter/ent"
	"spotter/internal/config"
	"spotter/internal/httputil"
	"spotter/internal/providers"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// createTestProvider creates a Provider with a custom base URL for testing
func createTestProvider(t *testing.T, cfg *config.Config, user *ent.User, serverURL string) *Provider {
	t.Helper()
	factory := New(nil, cfg)
	provider, err := factory(context.Background(), user)
	require.NoError(t, err)
	require.NotNil(t, provider)

	p := provider.(*Provider)
	p.WithBaseURL(serverURL)
	return p
}

func testUser() *ent.User {
	return &ent.User{
		Username: "testuser",
		Edges: ent.UserEdges{
			ListenbrainzAuth: &ent.ListenBrainzAuth{
				Username: "lb-user",
				Token:    "user-token-123",
			},
		},
	}
}

// Governing: SPEC music-provider-integration REQ-PROV-011 (nil,nil if unconfigured), REQ-PROV-045
func TestNew_NoAuth(t *testing.T) {
	factory := New(nil, &config.Config{})

	user := &ent.User{
		Username: "testuser",
		// No ListenbrainzAuth edge
	}

	provider, err := factory(context.Background(), user)
	assert.NoError(t, err)
	assert.Nil(t, provider)
}

func TestNew_WithAuth(t *testing.T) {
	factory := New(nil, &config.Config{})

	provider, err := factory(context.Background(), testUser())
	require.NoError(t, err)
	require.NotNil(t, provider)

	assert.Equal(t, providers.TypeListenBrainz, provider.Type())
	assert.Equal(t, "lb-user", provider.(*Provider).Username())
}

func TestNew_BaseURLFromConfig(t *testing.T) {
	cfg := &config.Config{}
	cfg.ListenBrainz.APIURL = "https://listenbrainz.example.com"

	factory := New(nil, cfg)
	provider, err := factory(context.Background(), testUser())
	require.NoError(t, err)
	assert.Equal(t, "https://listenbrainz.example.com", provider.(*Provider).baseURL)

	// Default when unconfigured
	provider, err = New(nil, &config.Config{})(context.Background(), testUser())
	require.NoError(t, err)
	assert.Equal(t, defaultAPIBaseURL, provider.(*Provider).baseURL)
}

func TestNewTokenValidator(t *testing.T) {
	p := NewTokenValidator(nil, &config.Config{})
	require.NotNil(t, p)
	assert.Equal(t, providers.TypeListenBrainz, p.Type())
	assert.Equal(t, "", p.Username())
}

// Governing: SPEC music-provider-integration REQ-PROV-046 (validate-token on connect)
func TestValidateToken(t *testing.T) {
	tests := []struct {
		name         string
		token        string
		handler      http.HandlerFunc
		closedServer bool
		wantErr      bool
		wantValid    bool
		wantUserName string
	}{
		{
			name:  "valid token",
			token: "good-token",
			handler: func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, http.MethodGet, r.Method)
				assert.Equal(t, "/1/validate-token", r.URL.Path)
				assert.Equal(t, "Token good-token", r.Header.Get("Authorization"))
				assert.Equal(t, httputil.UserAgent, r.Header.Get("User-Agent"))

				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"code":      200,
					"message":   "Token valid.",
					"valid":     true,
					"user_name": "lb-user",
				})
			},
			wantValid:    true,
			wantUserName: "lb-user",
		},
		{
			name:  "invalid token",
			token: "bad-token",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"code":    200,
					"message": "Token invalid.",
					"valid":   false,
				})
			},
			wantValid: false,
		},
		{
			name:         "network error",
			token:        "any-token",
			closedServer: true,
			wantErr:      true,
		},
		{
			name:  "malformed response",
			token: "any-token",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte("not json"))
			},
			wantErr: true,
		},
		{
			name:    "empty token rejected without request",
			token:   "",
			wantErr: true,
			handler: func(w http.ResponseWriter, r *http.Request) {
				t.Error("no request should be made for an empty token")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := tt.handler
			if handler == nil {
				handler = func(w http.ResponseWriter, r *http.Request) {}
			}
			server := httptest.NewServer(handler)
			if tt.closedServer {
				server.Close()
			} else {
				defer server.Close()
			}

			p := NewTokenValidator(nil, &config.Config{}).WithBaseURL(server.URL)

			// Bound the test so network-error retries do not sleep through the
			// full exponential backoff schedule.
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()

			result, err := p.ValidateToken(ctx, tt.token)
			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, result)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, result)
			assert.Equal(t, tt.wantValid, result.Valid)
			assert.Equal(t, tt.wantUserName, result.UserName)
		})
	}
}

func TestValidateToken_MalformedResponseIsFatal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("{{{"))
	}))
	defer server.Close()

	p := NewTokenValidator(nil, &config.Config{}).WithBaseURL(server.URL)

	_, err := p.ValidateToken(context.Background(), "token")
	require.Error(t, err)
	// Governing: SPEC error-handling REQ-ERR-003 (unparseable response body is fatal)
	assert.ErrorIs(t, err, providers.ErrMalformedResponse)
}

// Governing: SPEC music-provider-integration REQ-PROV-047, AGENTS.md "External API Etiquette"
func TestDoRequest_UserAgent(t *testing.T) {
	var gotUA, gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	p := createTestProvider(t, &config.Config{}, testUser(), server.URL)

	err := p.doRequest(context.Background(), http.MethodGet, "/1/validate-token", "token-abc", nil)
	require.NoError(t, err)
	assert.Equal(t, "Spotter/1.0.0", gotUA)
	assert.Equal(t, "Token token-abc", gotAuth)
}

// Governing: SPEC music-provider-integration REQ-PROV-047 (honor 429 + Retry-After)
func TestDoRequest_RateLimit429(t *testing.T) {
	tests := []struct {
		name   string
		header string
	}{
		{name: "Retry-After header", header: "Retry-After"},
		{name: "X-RateLimit-Reset-In fallback", header: "X-RateLimit-Reset-In"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			attempts := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempts++
				if attempts == 1 {
					w.Header().Set(tt.header, "0")
					w.WriteHeader(http.StatusTooManyRequests)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{}`))
			}))
			defer server.Close()

			p := createTestProvider(t, &config.Config{}, testUser(), server.URL)

			err := p.doRequest(context.Background(), http.MethodGet, "/1/test", "token", nil)
			require.NoError(t, err)
			assert.Equal(t, 2, attempts)
		})
	}
}

func TestDoRequest_Retry500(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("server error"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	p := createTestProvider(t, &config.Config{}, testUser(), server.URL)

	err := p.doRequest(context.Background(), http.MethodGet, "/1/test", "token", nil)
	require.NoError(t, err)
	assert.Equal(t, 2, attempts)
}

func TestDoRequest_No400Retry(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("unauthorized"))
	}))
	defer server.Close()

	p := createTestProvider(t, &config.Config{}, testUser(), server.URL)

	err := p.doRequest(context.Background(), http.MethodGet, "/1/test", "token", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "401")
	assert.Equal(t, 1, attempts) // 4xx (other than 429) is not retryable
}

func TestRateLimitWait(t *testing.T) {
	tests := []struct {
		name    string
		headers map[string]string
		want    time.Duration
		wantOK  bool
	}{
		{name: "no headers defaults to 1s", headers: nil, want: time.Second, wantOK: true},
		{name: "Retry-After seconds", headers: map[string]string{"Retry-After": "2"}, want: 2 * time.Second, wantOK: true},
		{name: "X-RateLimit-Reset-In seconds", headers: map[string]string{"X-RateLimit-Reset-In": "3"}, want: 3 * time.Second, wantOK: true},
		{name: "Retry-After preferred over reset-in", headers: map[string]string{"Retry-After": "2", "X-RateLimit-Reset-In": "9"}, want: 2 * time.Second, wantOK: true},
		{name: "zero allowed", headers: map[string]string{"Retry-After": "0"}, want: 0, wantOK: true},
		{name: "over cap aborts instead of retrying early", headers: map[string]string{"Retry-After": "3600"}, want: 0, wantOK: false},
		{name: "over-cap reset-in aborts", headers: map[string]string{"X-RateLimit-Reset-In": "3600"}, want: 0, wantOK: false},
		{name: "garbage falls back to 1s", headers: map[string]string{"Retry-After": "soon"}, want: time.Second, wantOK: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := http.Header{}
			for k, v := range tt.headers {
				h.Set(k, v)
			}
			got, ok := rateLimitWait(h)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.wantOK, ok)
		})
	}

	t.Run("future http-date honored", func(t *testing.T) {
		h := http.Header{}
		h.Set("Retry-After", time.Now().Add(10*time.Second).UTC().Format(http.TimeFormat))
		got, ok := rateLimitWait(h)
		assert.True(t, ok)
		assert.InDelta(t, float64(10*time.Second), float64(got), float64(2*time.Second))
	})

	t.Run("far-future http-date aborts", func(t *testing.T) {
		h := http.Header{}
		h.Set("Retry-After", time.Now().Add(time.Hour).UTC().Format(http.TimeFormat))
		_, ok := rateLimitWait(h)
		assert.False(t, ok)
	})
}

func TestInterfaceImplementation(t *testing.T) {
	// The foundation provider only implements the base Provider interface;
	// HistoryFetcher lands in a later PR. The negative assertion guards the
	// syncer's type-assert skip: an accidental GetRecentListens stub would
	// flip syncHistory from skip to live-call.
	var _ providers.Provider = (*Provider)(nil)
	_, isFetcher := interface{}(&Provider{}).(providers.HistoryFetcher)
	assert.False(t, isFetcher, "foundation provider must not satisfy HistoryFetcher yet")
}
