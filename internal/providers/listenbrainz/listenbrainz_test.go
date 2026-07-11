package listenbrainz

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
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

// lbListen builds a listen entry as returned by GET /1/user/{u}/listens.
func lbListen(ts int64, name, artist, album string, extra map[string]interface{}) map[string]interface{} {
	info := map[string]interface{}{}
	for k, v := range extra {
		info[k] = v
	}
	return map[string]interface{}{
		"listened_at": ts,
		"track_metadata": map[string]interface{}{
			"track_name":      name,
			"artist_name":     artist,
			"release_name":    album,
			"additional_info": info,
		},
	}
}

// lbListensPage generates count listens with descending listened_at starting
// at newest (newest-first, as the API returns them).
func lbListensPage(newest int64, count int) []interface{} {
	listens := make([]interface{}, 0, count)
	for i := 0; i < count; i++ {
		ts := newest - int64(i)
		listens = append(listens, lbListen(ts, fmt.Sprintf("Track %d", ts), "Artist", "Album", nil))
	}
	return listens
}

func writeListens(t *testing.T, w http.ResponseWriter, listens []interface{}) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	err := json.NewEncoder(w).Encode(map[string]interface{}{
		"payload": map[string]interface{}{
			"count":   len(listens),
			"listens": listens,
		},
	})
	require.NoError(t, err)
}

// Governing: SPEC music-provider-integration REQ-PROV-048
func TestGetRecentListens_SinglePage(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/1/user/lb-user/listens", r.URL.Path)
		assert.Equal(t, "100", r.URL.Query().Get("count"))
		assert.Empty(t, r.URL.Query().Get("max_ts"), "first page must not set max_ts")
		assert.Empty(t, r.URL.Query().Get("min_ts"), "min_ts must never be combined with max_ts paging")
		assert.Equal(t, "Token user-token-123", r.Header.Get("Authorization"))
		assert.Equal(t, httputil.UserAgent, r.Header.Get("User-Agent"))

		writeListens(t, w, []interface{}{
			lbListen(1700000200, "Song A", "Artist A", "Album A", map[string]interface{}{
				"recording_mbid": "mbid-a",
				"duration_ms":    215000,
				"isrc":           "USRC17607839",
				"origin_url":     "https://music.example/a",
			}),
			lbListen(1700000100, "Song B", "Artist B", "Album B", nil),
		})
	}))
	defer server.Close()

	p := createTestProvider(t, &config.Config{}, testUser(), server.URL)

	var collected []providers.Track
	err := p.GetRecentListens(context.Background(), time.Time{}, func(tracks []providers.Track) error {
		collected = append(collected, tracks...)
		return nil
	})

	require.NoError(t, err)
	assert.Equal(t, 1, requests, "a short page must terminate pagination")
	require.Len(t, collected, 2)

	assert.Equal(t, "mbid-a", collected[0].ID)
	assert.Equal(t, "Song A", collected[0].Name)
	assert.Equal(t, "Artist A", collected[0].Artist)
	assert.Equal(t, "Album A", collected[0].Album)
	assert.Equal(t, 215000, collected[0].DurationMs)
	assert.Equal(t, "USRC17607839", collected[0].ISRC)
	assert.Equal(t, "https://music.example/a", collected[0].URL)
	assert.Equal(t, time.Unix(1700000200, 0).UTC(), collected[0].PlayedAt)
	assert.Equal(t, time.UTC, collected[0].PlayedAt.Location())

	assert.Equal(t, "Song B", collected[1].Name)
	assert.Empty(t, collected[1].ID, "no MBID/MSID means no stable ID")
}

// Governing: SPEC music-provider-integration REQ-PROV-048 (backwards max_ts pagination)
func TestGetRecentListens_Pagination(t *testing.T) {
	const newest = int64(1700000999)
	var maxTSSeen []string
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		maxTSSeen = append(maxTSSeen, r.URL.Query().Get("max_ts"))
		switch requests {
		case 1:
			// Full page: newest .. newest-99
			writeListens(t, w, lbListensPage(newest, 100))
		case 2:
			// Short page below the previous oldest terminates the walk.
			writeListens(t, w, lbListensPage(newest-100, 3))
		default:
			t.Errorf("unexpected request %d", requests)
			writeListens(t, w, nil)
		}
	}))
	defer server.Close()

	p := createTestProvider(t, &config.Config{}, testUser(), server.URL)

	var collected []providers.Track
	err := p.GetRecentListens(context.Background(), time.Time{}, func(tracks []providers.Track) error {
		collected = append(collected, tracks...)
		return nil
	})

	require.NoError(t, err)
	assert.Equal(t, 2, requests)
	assert.Len(t, collected, 103)
	// max_ts advances backwards: unset on page 1, then the oldest
	// listened_at of page 1 (exclusive bound) on page 2.
	require.Equal(t, []string{"", strconv.FormatInt(newest-99, 10)}, maxTSSeen)
}

// Governing: SPEC music-provider-integration REQ-PROV-048 (client-side since bound)
func TestGetRecentListens_SinceFiltering(t *testing.T) {
	const newest = int64(1700000999)
	since := time.Unix(newest-50, 0) // splits a full page in half
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		writeListens(t, w, lbListensPage(newest, 100))
	}))
	defer server.Close()

	p := createTestProvider(t, &config.Config{}, testUser(), server.URL)

	var collected []providers.Track
	err := p.GetRecentListens(context.Background(), since, func(tracks []providers.Track) error {
		collected = append(collected, tracks...)
		return nil
	})

	require.NoError(t, err)
	assert.Equal(t, 1, requests, "reaching since must terminate pagination even on a full page")
	// Only listens strictly after since are delivered: newest-49 .. newest.
	require.Len(t, collected, 50)
	assert.Equal(t, time.Unix(newest, 0).UTC(), collected[0].PlayedAt)
	assert.Equal(t, time.Unix(newest-49, 0).UTC(), collected[len(collected)-1].PlayedAt)
	for _, track := range collected {
		assert.True(t, track.PlayedAt.After(since), "listen at %v is not after since %v", track.PlayedAt, since)
	}
}

func TestGetRecentListens_EmptyHistory(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		writeListens(t, w, nil)
	}))
	defer server.Close()

	p := createTestProvider(t, &config.Config{}, testUser(), server.URL)

	callbacks := 0
	err := p.GetRecentListens(context.Background(), time.Time{}, func(tracks []providers.Track) error {
		callbacks++
		return nil
	})

	require.NoError(t, err)
	assert.Equal(t, 1, requests)
	assert.Equal(t, 0, callbacks, "empty history must not invoke the callback")
}

func TestGetRecentListens_MalformedPayload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{{{not json"))
	}))
	defer server.Close()

	p := createTestProvider(t, &config.Config{}, testUser(), server.URL)

	err := p.GetRecentListens(context.Background(), time.Time{}, func(tracks []providers.Track) error {
		t.Error("callback must not run on a malformed payload")
		return nil
	})

	require.Error(t, err)
	// Governing: SPEC error-handling REQ-ERR-003 (unparseable response body is fatal)
	assert.ErrorIs(t, err, providers.ErrMalformedResponse)
}

// Governing: SPEC music-provider-integration REQ-PROV-047 (honor 429 + Retry-After)
func TestGetRecentListens_RateLimited(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		writeListens(t, w, []interface{}{
			lbListen(1700000100, "After 429", "Artist", "Album", nil),
		})
	}))
	defer server.Close()

	p := createTestProvider(t, &config.Config{}, testUser(), server.URL)

	var collected []providers.Track
	err := p.GetRecentListens(context.Background(), time.Time{}, func(tracks []providers.Track) error {
		collected = append(collected, tracks...)
		return nil
	})

	require.NoError(t, err)
	assert.Equal(t, 2, attempts, "must retry once after honoring Retry-After")
	require.Len(t, collected, 1)
	assert.Equal(t, "After 429", collected[0].Name)
}

// Governing: SPEC music-provider-integration REQ-PROV-002 (batched callback contract) —
// the callback receives one batch per page, newest-first, in the shape
// syncHistory consumes (non-empty batches of normalized Tracks with UTC
// PlayedAt set).
func TestGetRecentListens_CallbackBatches(t *testing.T) {
	const newest = int64(1700000999)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			writeListens(t, w, lbListensPage(newest, 100))
			return
		}
		writeListens(t, w, lbListensPage(newest-100, 2))
	}))
	defer server.Close()

	p := createTestProvider(t, &config.Config{}, testUser(), server.URL)

	var batches [][]providers.Track
	err := p.GetRecentListens(context.Background(), time.Time{}, func(tracks []providers.Track) error {
		// Copy: syncHistory persists synchronously, but the callback contract
		// does not promise the slice survives the call.
		batch := make([]providers.Track, len(tracks))
		copy(batch, tracks)
		batches = append(batches, batch)
		return nil
	})

	require.NoError(t, err)
	require.Len(t, batches, 2, "one callback per page")
	assert.Len(t, batches[0], 100)
	assert.Len(t, batches[1], 2)

	// Batches arrive newest-first across and within pages.
	prev := time.Unix(newest+1, 0).UTC()
	for _, batch := range batches {
		require.NotEmpty(t, batch, "syncHistory expects non-empty batches")
		for _, track := range batch {
			assert.NotEmpty(t, track.Name)
			assert.NotEmpty(t, track.Artist)
			assert.False(t, track.PlayedAt.IsZero(), "syncHistory requires PlayedAt")
			assert.Equal(t, time.UTC, track.PlayedAt.Location())
			assert.True(t, track.PlayedAt.Before(prev), "tracks must arrive newest-first")
			prev = track.PlayedAt
		}
	}
}

func TestGetRecentListens_CallbackErrorAborts(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		writeListens(t, w, lbListensPage(1700000999, 100))
	}))
	defer server.Close()

	p := createTestProvider(t, &config.Config{}, testUser(), server.URL)

	wantErr := fmt.Errorf("persist failed")
	err := p.GetRecentListens(context.Background(), time.Time{}, func(tracks []providers.Track) error {
		return wantErr
	})

	require.ErrorIs(t, err, wantErr)
	assert.Equal(t, 1, requests, "a callback error must stop pagination")
}

// Governing: SPEC music-provider-integration REQ-PROV-048 (pagination MUST
// terminate even when the server misbehaves)
func TestGetRecentListens_NonAdvancingCursorTerminates(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		// Always the same full page: a broken server that ignores max_ts.
		writeListens(t, w, lbListensPage(1700000999, 100))
	}))
	defer server.Close()

	p := createTestProvider(t, &config.Config{}, testUser(), server.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := p.GetRecentListens(ctx, time.Time{}, func(tracks []providers.Track) error {
		return nil
	})

	require.NoError(t, err)
	// Page 1 sets the cursor; page 2's oldest equals the cursor, which must
	// end the walk instead of looping forever.
	assert.Equal(t, 2, requests)
}

func TestGetRecentListens_SkipsListensWithoutTimestamp(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeListens(t, w, []interface{}{
			lbListen(1700000200, "Valid", "Artist", "Album", nil),
			lbListen(0, "No Timestamp", "Artist", "Album", nil),
		})
	}))
	defer server.Close()

	p := createTestProvider(t, &config.Config{}, testUser(), server.URL)

	var collected []providers.Track
	err := p.GetRecentListens(context.Background(), time.Time{}, func(tracks []providers.Track) error {
		collected = append(collected, tracks...)
		return nil
	})

	require.NoError(t, err)
	require.Len(t, collected, 1)
	assert.Equal(t, "Valid", collected[0].Name)
}

func TestInterfaceImplementation(t *testing.T) {
	// Governing: SPEC music-provider-integration REQ-PROV-048 — the provider
	// must satisfy HistoryFetcher so the syncer's type assertion in
	// syncHistory picks it up for listen-history sync.
	var _ providers.Provider = (*Provider)(nil)
	_, isFetcher := interface{}(&Provider{}).(providers.HistoryFetcher)
	assert.True(t, isFetcher, "provider must satisfy HistoryFetcher")
}
