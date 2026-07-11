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
	// listened_at of page 1 PLUS ONE on page 2 — max_ts is exclusive, so
	// oldest+1 re-fetches the boundary second and never drops ties that
	// share the oldest timestamp (REQ-PROV-048).
	require.Equal(t, []string{"", strconv.FormatInt(newest-99+1, 10)}, maxTSSeen)
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
	// Listens at or after since are delivered: newest-50 .. newest. The
	// listen AT since is included — the watermark second may hold unsynced
	// ties, and the idempotent persist layer absorbs the re-delivery
	// (REQ-PROV-048). Listens strictly before since are excluded.
	require.Len(t, collected, 51)
	assert.Equal(t, time.Unix(newest, 0).UTC(), collected[0].PlayedAt)
	assert.Equal(t, time.Unix(newest-50, 0).UTC(), collected[len(collected)-1].PlayedAt)
	for _, track := range collected {
		assert.False(t, track.PlayedAt.Before(since), "listen at %v is before since %v", track.PlayedAt, since)
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

// faithfulListen is a listen served by newFaithfulListensServer.
type faithfulListen struct {
	ts   int64
	name string
}

// newFaithfulListensServer serves the given listens (which must be sorted
// newest-first) with the real ListenBrainz GET /1/user/{u}/listens semantics:
// max_ts is EXCLUSIVE (only listens with listened_at strictly less than
// max_ts are returned), listens are returned newest-first, and at most
// listensPageSize items are returned per request. requests counts calls.
func newFaithfulListensServer(t *testing.T, listens []faithfulListen, requests *int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*requests++
		require.LessOrEqual(t, *requests, 20, "runaway pagination: too many requests")

		maxTS := int64(0)
		if v := r.URL.Query().Get("max_ts"); v != "" {
			parsed, err := strconv.ParseInt(v, 10, 64)
			require.NoError(t, err)
			maxTS = parsed
		}

		var page []interface{}
		for _, l := range listens {
			if maxTS > 0 && l.ts >= maxTS {
				continue // max_ts is exclusive: strictly-less only
			}
			page = append(page, lbListen(l.ts, l.name, "Artist", "Album", nil))
			if len(page) == listensPageSize {
				break
			}
		}
		writeListens(t, w, page)
	}))
}

// collectNames runs GetRecentListens against the provider and returns the set
// of distinct track names delivered to the callback (duplicate re-deliveries
// across pages are allowed and collapsed here).
func collectNames(t *testing.T, p *Provider, since time.Time) map[string]bool {
	t.Helper()
	delivered := map[string]bool{}
	err := p.GetRecentListens(context.Background(), since, func(tracks []providers.Track) error {
		for _, track := range tracks {
			delivered[track.Name] = true
		}
		return nil
	})
	require.NoError(t, err)
	return delivered
}

// Regression test for the data-loss bug found by adversarial review on PR #47:
// the pagination cursor was set to maxTS = oldest listened_at of the previous
// page, but the ListenBrainz max_ts parameter is EXCLUSIVE (returns listens
// strictly older). When two listens share listened_at == T and one of them is
// the 100th item of a page, the follow-up request with max_ts=T only returns
// listens < T — the second tie was silently and permanently lost. The fix
// paginates with max_ts = oldest+1 so boundary ties are re-fetched
// (re-delivery is safe: persistListens de-duplicates idempotently).
// Governing: SPEC music-provider-integration REQ-PROV-048
func TestGetRecentListens_Regression_PageBoundaryTieLoss(t *testing.T) {
	const base = int64(1700000999)

	// 102 listens, newest-first. listens[99] (last item of page 1) and
	// listens[100] (first item beyond the page boundary) share a timestamp.
	var listens []faithfulListen
	for i := 0; i < 100; i++ {
		listens = append(listens, faithfulListen{ts: base - int64(i), name: fmt.Sprintf("Listen-%03d", i)})
	}
	listens = append(listens,
		faithfulListen{ts: base - 99, name: "Listen-100"}, // tie with listens[99]
		faithfulListen{ts: base - 100, name: "Listen-101"},
	)

	requests := 0
	server := newFaithfulListensServer(t, listens, &requests)
	defer server.Close()

	p := createTestProvider(t, &config.Config{}, testUser(), server.URL)
	delivered := collectNames(t, p, time.Time{})

	// Every listen must be delivered at least once; dupes across pages are
	// fine (the persist layer is idempotent), losing one is not.
	for _, l := range listens {
		assert.True(t, delivered[l.name], "listen %s (ts %d) was never delivered", l.name, l.ts)
	}
	assert.GreaterOrEqual(t, requests, 2, "a full first page must trigger a second request")
}

// Regression test for the sibling tie-loss at the since watermark (PR #47
// adversarial review): listens with played_at exactly equal to since were
// filtered out client-side, so a tie sharing the watermark second that had
// not been synced yet was permanently lost. They are now delivered (the
// idempotent persist layer absorbs the re-delivered watermark listen), while
// listens strictly before since remain excluded, and pagination still
// terminates when ties at since span a page boundary.
// Governing: SPEC music-provider-integration REQ-PROV-048
func TestGetRecentListens_Regression_SinceBoundaryTieDelivered(t *testing.T) {
	const base = int64(1700001000)
	sinceTS := base - 97
	since := time.Unix(sinceTS, 0).UTC()

	// 106 listens newest-first: 97 strictly after since, then FOUR ties at
	// exactly since (three end page 1, the fourth spills onto page 2), then
	// five strictly before since.
	var listens []faithfulListen
	for i := 0; i < 97; i++ {
		listens = append(listens, faithfulListen{ts: base - int64(i), name: fmt.Sprintf("Listen-%03d", i)})
	}
	for i := 97; i <= 100; i++ {
		listens = append(listens, faithfulListen{ts: sinceTS, name: fmt.Sprintf("Listen-%03d", i)})
	}
	for i := 101; i <= 105; i++ {
		listens = append(listens, faithfulListen{ts: sinceTS - int64(i-100), name: fmt.Sprintf("Listen-%03d", i)})
	}

	requests := 0
	server := newFaithfulListensServer(t, listens, &requests)
	defer server.Close()

	p := createTestProvider(t, &config.Config{}, testUser(), server.URL)
	delivered := collectNames(t, p, since)

	// All ties at exactly since must be delivered, including the one past
	// the page boundary.
	for i := 97; i <= 100; i++ {
		name := fmt.Sprintf("Listen-%03d", i)
		assert.True(t, delivered[name], "tie at since (%s) was never delivered", name)
	}
	// Listens strictly before since must not be delivered.
	for i := 101; i <= 105; i++ {
		name := fmt.Sprintf("Listen-%03d", i)
		assert.False(t, delivered[name], "listen strictly before since (%s) must not be delivered", name)
	}
	assert.Equal(t, 97+4, len(delivered))
	assert.Equal(t, 2, requests, "walk must fetch exactly one page past the since-tie boundary and stop")
}

// A pathological page where 100+ listens share one timestamp: with the
// tie-safe cursor (max_ts = oldest+1) a faithful server re-serves the same
// page forever, so the strict-decrease termination guard must end the walk.
// Governing: SPEC music-provider-integration REQ-PROV-048 (pagination MUST
// terminate even when the cursor cannot strictly decrease)
func TestGetRecentListens_MassTiePageTerminates(t *testing.T) {
	const ts = int64(1700000500)
	var listens []faithfulListen
	for i := 0; i < 150; i++ {
		listens = append(listens, faithfulListen{ts: ts, name: fmt.Sprintf("Listen-%03d", i)})
	}

	requests := 0
	server := newFaithfulListensServer(t, listens, &requests)
	defer server.Close()

	p := createTestProvider(t, &config.Config{}, testUser(), server.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := p.GetRecentListens(ctx, time.Time{}, func(tracks []providers.Track) error {
		return nil
	})

	require.NoError(t, err)
	assert.LessOrEqual(t, requests, 3, "cursor cannot strictly decrease past a >=100-listen tie: the guard must stop the walk")
}

// Regression test for the duration fallback nit from the PR #47 review:
// some ListenBrainz submitters populate track_metadata.additional_info with
// `duration` (seconds) instead of `duration_ms`. Both must be read, with
// duration_ms preferred when both are present.
func TestGetRecentListens_Regression_DurationSecondsFallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeListens(t, w, []interface{}{
			lbListen(1700000300, "Millis", "Artist", "Album", map[string]interface{}{
				"duration_ms": 215000,
			}),
			lbListen(1700000200, "Both", "Artist", "Album", map[string]interface{}{
				"duration_ms": 215000,
				"duration":    999,
			}),
			lbListen(1700000100, "Seconds Only", "Artist", "Album", map[string]interface{}{
				"duration": 187,
			}),
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
	require.Len(t, collected, 3)
	assert.Equal(t, 215000, collected[0].DurationMs)
	assert.Equal(t, 215000, collected[1].DurationMs, "duration_ms must win over duration")
	assert.Equal(t, 187000, collected[2].DurationMs, "duration (seconds) must be converted to ms")
}

func TestInterfaceImplementation(t *testing.T) {
	// Governing: SPEC music-provider-integration REQ-PROV-048 — the provider
	// must satisfy HistoryFetcher so the syncer's type assertion in
	// syncHistory picks it up for listen-history sync.
	var _ providers.Provider = (*Provider)(nil)
	_, isFetcher := interface{}(&Provider{}).(providers.HistoryFetcher)
	assert.True(t, isFetcher, "provider must satisfy HistoryFetcher")
}
