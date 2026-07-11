package handlers_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"spotter/ent"
	"spotter/ent/user"
	"spotter/internal/auth"
	"spotter/internal/config"
	"spotter/internal/crypto"
	"spotter/internal/events"
	"spotter/internal/handlers"
	"spotter/internal/services"

	entsql "entgo.io/ent/dialect/sql"
	"github.com/go-chi/chi/v5"
	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testJWTSecret = "test-jwt-secret-at-least-32-chars"

func setupTestDB(t testing.TB) *ent.Client {
	// Use a unique DB name per test to prevent cross-test SQLite write-lock races
	// when background goroutines (e.g. the syncer) outlive a test's cleanup.
	dbName := strings.NewReplacer("/", "_", " ", "_", "=", "_").Replace(t.Name())
	drv, err := entsql.Open("sqlite3", "file:"+dbName+"?mode=memory&cache=shared&_fk=1")
	require.NoError(t, err)
	// SQLite shared-cache mode uses table-level locks and returns SQLITE_LOCKED
	// ("database table is locked") IMMEDIATELY on cross-connection contention —
	// _busy_timeout does not apply to shared-cache locks. Handlers dispatch
	// sync/pair work in background goroutines that write on one pooled
	// connection while the test (or the handler's own response path) reads on
	// another, which makes those errors a coin flip. Cap the pool at a single
	// connection so all queries on this client serialize instead of erroring.
	drv.DB().SetMaxOpenConns(1)
	client := ent.NewClient(ent.Driver(drv))
	require.NoError(t, client.Schema.Create(context.Background()))
	t.Cleanup(func() {
		client.Close()
	})
	return client
}

func TestLogin_Get(t *testing.T) {
	client := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{}
	bus := events.NewBus()
	syncer := services.NewSyncer(client, cfg, logger, bus, nil)
	encryptor, _ := crypto.NewEncryptor(make([]byte, 32))
	jwtManager := auth.NewJWTManager(testJWTSecret)
	h := handlers.New(client, cfg, logger, encryptor, jwtManager, syncer, nil, nil, nil, nil, nil, bus, nil)

	req := httptest.NewRequest("GET", "/auth/login", nil)
	w := httptest.NewRecorder()

	h.Login(w, req)

	resp := w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(body), "Spotter uses your Navidrome credentials")
	assert.Contains(t, string(body), "Log in with Navidrome")
}

func TestPostLogin_Success(t *testing.T) {
	// 1. Mock Navidrome Server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/rest/ping.view", r.URL.Path)
		assert.Equal(t, "testuser", r.URL.Query().Get("u"))

		w.Header().Set("Content-Type", "application/json")
		response := map[string]interface{}{
			"subsonic-response": map[string]interface{}{
				"status":  "ok",
				"version": "1.16.1",
			},
		}
		json.NewEncoder(w).Encode(response)
	}))
	defer ts.Close()

	// 2. Setup
	client := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{}
	cfg.Navidrome.BaseURL = ts.URL
	bus := events.NewBus()
	syncer := services.NewSyncer(client, cfg, logger, bus, nil)
	encryptor, _ := crypto.NewEncryptor(make([]byte, 32))
	jwtManager := auth.NewJWTManager(testJWTSecret)
	h := handlers.New(client, cfg, logger, encryptor, jwtManager, syncer, nil, nil, nil, nil, nil, bus, nil)

	// 3. Request
	form := url.Values{}
	form.Set("username", "testuser")
	form.Set("password", "secret123")
	req := httptest.NewRequest("POST", "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true") // Simulate HTMX request
	w := httptest.NewRecorder()

	// 4. Execute
	h.PostLogin(w, req)

	// 5. Assert Response
	resp := w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "/", w.Header().Get("HX-Redirect"))

	// Check Cookies - should be JWT token now
	cookies := resp.Cookies()
	require.Len(t, cookies, 1)
	assert.Equal(t, handlers.CookieName, cookies[0].Name)
	// Verify it's a valid JWT by parsing it
	claims, err := jwtManager.ValidateToken(cookies[0].Value)
	require.NoError(t, err)
	assert.Equal(t, "testuser", claims.Username)

	// 6. Assert Database State
	u, err := client.User.Query().
		Where(user.Username("testuser")).
		WithNavidromeAuth().
		Only(context.Background())
	assert.NoError(t, err)
	assert.NotNil(t, u)
	assert.NotNil(t, u.Edges.NavidromeAuth)
	assert.Equal(t, "secret123", u.Edges.NavidromeAuth.Password)
	// Verify JWT claims match the created user
	assert.Equal(t, u.ID, claims.UserID)
}

func TestPostLogin_InvalidCredentials(t *testing.T) {
	// 1. Mock Navidrome Server to return error
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		response := map[string]interface{}{
			"subsonic-response": map[string]interface{}{
				"status": "failed",
				"error": map[string]interface{}{
					"code":    40,
					"message": "Wrong username or password",
				},
			},
		}
		json.NewEncoder(w).Encode(response)
	}))
	defer ts.Close()

	// 2. Setup
	client := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{}
	cfg.Navidrome.BaseURL = ts.URL
	bus := events.NewBus()
	syncer := services.NewSyncer(client, cfg, logger, bus, nil)
	encryptor, _ := crypto.NewEncryptor(make([]byte, 32))
	jwtManager := auth.NewJWTManager(testJWTSecret)
	h := handlers.New(client, cfg, logger, encryptor, jwtManager, syncer, nil, nil, nil, nil, nil, bus, nil)

	// 3. Request
	form := url.Values{}
	form.Set("username", "testuser")
	form.Set("password", "wrongpassword")
	req := httptest.NewRequest("POST", "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	// 4. Execute
	h.PostLogin(w, req)

	// 5. Assert
	assert.Equal(t, http.StatusUnauthorized, w.Result().StatusCode)

	// User should not exist
	exists, _ := client.User.Query().Where(user.Username("testuser")).Exist(context.Background())
	assert.False(t, exists)
}

// TestPostLogin_Regression_HTMXRedirect tests that login provides proper redirect
// for both HTMX and non-HTMX requests.
//
// Original issue: PostLogin only set HX-Redirect header without fallback.
// When form submitted without HTMX (before JS loads or if CDN fails), the
// response had no body or redirect, resulting in a white screen.
//
// Related commits:
// - "Add HTTP redirect fallback for non-HTMX login submissions"
func TestPostLogin_Regression_HTMXRedirect(t *testing.T) {
	// Setup mock Navidrome server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Mock successful ping response
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"subsonic-response":{"status":"ok"}}`))
	}))
	defer ts.Close()

	client := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{}
	cfg.Navidrome.BaseURL = ts.URL
	bus := events.NewBus()
	syncer := services.NewSyncer(client, cfg, logger, bus, nil)
	encryptor, _ := crypto.NewEncryptor(make([]byte, 32))
	jwtManager := auth.NewJWTManager(testJWTSecret)
	h := handlers.New(client, cfg, logger, encryptor, jwtManager, syncer, nil, nil, nil, nil, nil, bus, nil)

	testCases := []struct {
		name               string
		htmxRequest        bool
		expectedStatusCode int
		checkRedirect      bool
		checkHXHeader      bool
	}{
		{
			name:               "HTMX request should get HX-Redirect header",
			htmxRequest:        true,
			expectedStatusCode: http.StatusOK,
			checkRedirect:      false,
			checkHXHeader:      true,
		},
		{
			name:               "Non-HTMX request should get HTTP redirect",
			htmxRequest:        false,
			expectedStatusCode: http.StatusSeeOther,
			checkRedirect:      true,
			checkHXHeader:      false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Prepare request
			form := url.Values{}
			form.Set("username", "testuser")
			form.Set("password", "secret123")
			req := httptest.NewRequest("POST", "/login", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

			if tc.htmxRequest {
				req.Header.Set("HX-Request", "true")
			}

			w := httptest.NewRecorder()

			// Execute
			h.PostLogin(w, req)

			// Assert status code
			assert.Equal(t, tc.expectedStatusCode, w.Result().StatusCode)

			// Check for HTTP redirect
			if tc.checkRedirect {
				location := w.Header().Get("Location")
				assert.Equal(t, "/", location, "Should redirect to home page")
			}

			// Check for HTMX redirect header
			if tc.checkHXHeader {
				hxRedirect := w.Header().Get("HX-Redirect")
				assert.Equal(t, "/", hxRedirect, "Should set HX-Redirect header")
			}

			// Verify session cookie is set in both cases
			cookies := w.Result().Cookies()
			var sessionCookie *http.Cookie
			for _, c := range cookies {
				if c.Name == handlers.CookieName {
					sessionCookie = c
					break
				}
			}
			require.NotNil(t, sessionCookie, "Session cookie should be set")
			// Verify it's a valid JWT token
			claims, err := jwtManager.ValidateToken(sessionCookie.Value)
			require.NoError(t, err)
			assert.Equal(t, "testuser", claims.Username)
			assert.True(t, sessionCookie.HttpOnly, "Cookie should be HttpOnly")
		})
	}
}

// TestPostLogin_Regression_PasswordUpdatedOnRelogin verifies that when an existing
// user logs in (e.g. after a password reset), the stored NavidromeAuth password is
// updated to the new value.
//
// Regression: the original code captured u.Edges.NavidromeAuth after u.Update().Save(),
// which returns a fresh entity with no edges loaded. This caused existingNavidromeAuth
// to always be nil, silently skipping the password update. Users who reset their
// Navidrome password could still log in (auth calls Navidrome directly) but all
// subsequent syncs would fail because the stale encrypted credential was used.
func TestPostLogin_Regression_PasswordUpdatedOnRelogin(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"subsonic-response": map[string]interface{}{"status": "ok"},
		})
	}))
	defer ts.Close()

	client := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{}
	cfg.Navidrome.BaseURL = ts.URL
	bus := events.NewBus()
	syncer := services.NewSyncer(client, cfg, logger, bus, nil)
	encryptor, _ := crypto.NewEncryptor(make([]byte, 32))
	jwtManager := auth.NewJWTManager(testJWTSecret)
	h := handlers.New(client, cfg, logger, encryptor, jwtManager, syncer, nil, nil, nil, nil, nil, bus, nil)

	ctx := context.Background()

	// Pre-create the user with the old password, simulating a prior login.
	existingUser, err := client.User.Create().
		SetUsername("testuser").
		Save(ctx)
	require.NoError(t, err)
	_, err = client.NavidromeAuth.Create().
		SetUser(existingUser).
		SetPassword("oldpassword").
		Save(ctx)
	require.NoError(t, err)

	// Log in with the new password (e.g. after a Navidrome password reset).
	form := url.Values{}
	form.Set("username", "testuser")
	form.Set("password", "newpassword")
	req := httptest.NewRequest("POST", "/auth/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	w := httptest.NewRecorder()

	h.PostLogin(w, req)

	require.Equal(t, http.StatusOK, w.Result().StatusCode)

	// The stored credential must reflect the new password so that sync works.
	u, err := client.User.Query().
		Where(user.Username("testuser")).
		WithNavidromeAuth().
		Only(ctx)
	require.NoError(t, err)
	require.NotNil(t, u.Edges.NavidromeAuth, "NavidromeAuth edge must be present")
	assert.Equal(t, "newpassword", u.Edges.NavidromeAuth.Password,
		"stored password must be updated on login; stale credential breaks sync after a password reset")
}

// TestPostLogin_NavidromeAuthCreatedOnRelogin verifies that when an existing
// user WITHOUT a NavidromeAuth edge logs in, the credential record is created.
//
// Regression: a half-failed first login (User row saved, NavidromeAuth create
// failed) left the account permanently without sync credentials because the
// existing-user branch only updated NavidromeAuth when the edge was present.
// Governing: SPEC user-authentication REQ-AUTH-007
func TestPostLogin_NavidromeAuthCreatedOnRelogin(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"subsonic-response": map[string]interface{}{"status": "ok"},
		})
	}))
	defer ts.Close()

	client := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{}
	cfg.Navidrome.BaseURL = ts.URL
	bus := events.NewBus()
	syncer := services.NewSyncer(client, cfg, logger, bus, nil)
	encryptor, _ := crypto.NewEncryptor(make([]byte, 32))
	jwtManager := auth.NewJWTManager(testJWTSecret)
	h := handlers.New(client, cfg, logger, encryptor, jwtManager, syncer, nil, nil, nil, nil, nil, bus, nil)

	ctx := context.Background()

	// Pre-create the user WITHOUT a NavidromeAuth record, simulating a
	// half-failed first login.
	_, err := client.User.Create().
		SetUsername("testuser").
		Save(ctx)
	require.NoError(t, err)

	form := url.Values{}
	form.Set("username", "testuser")
	form.Set("password", "secret123")
	req := httptest.NewRequest("POST", "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	w := httptest.NewRecorder()

	h.PostLogin(w, req)

	require.Equal(t, http.StatusOK, w.Result().StatusCode)

	// The credential record must now exist so background sync works.
	u, err := client.User.Query().
		Where(user.Username("testuser")).
		WithNavidromeAuth().
		Only(ctx)
	require.NoError(t, err)
	require.NotNil(t, u.Edges.NavidromeAuth,
		"NavidromeAuth must be created on login when the edge is missing")
	assert.Equal(t, "secret123", u.Edges.NavidromeAuth.Password,
		"created credential must hold the just-validated password")
}

// TestPostLogin_InvalidCredentials_HTMX verifies that a failed HTMX login
// returns HTTP 200 with the re-rendered login page carrying the error alert.
//
// Regression: PostLogin responded 401 + full-page render, but the login form
// is hx-post with hx-target="body" and htmx 1.9 does not swap non-2xx
// responses — wrong-password attempts showed nothing. Plain-browser requests
// keep the 401 pinned by the spec (see TestPostLogin_InvalidCredentials).
func TestPostLogin_InvalidCredentials_HTMX(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"subsonic-response": map[string]interface{}{
				"status": "failed",
				"error": map[string]interface{}{
					"code":    40,
					"message": "Wrong username or password",
				},
			},
		})
	}))
	defer ts.Close()

	client := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{}
	cfg.Navidrome.BaseURL = ts.URL
	bus := events.NewBus()
	syncer := services.NewSyncer(client, cfg, logger, bus, nil)
	encryptor, _ := crypto.NewEncryptor(make([]byte, 32))
	jwtManager := auth.NewJWTManager(testJWTSecret)
	h := handlers.New(client, cfg, logger, encryptor, jwtManager, syncer, nil, nil, nil, nil, nil, bus, nil)

	form := url.Values{}
	form.Set("username", "testuser")
	form.Set("password", "wrongpassword")
	req := httptest.NewRequest("POST", "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	w := httptest.NewRecorder()

	h.PostLogin(w, req)

	resp := w.Result()
	// htmx 1.9 only swaps 2xx responses, so the error page must come back 200.
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(body), "Invalid username or password",
		"error alert must be present in the swapped login page")
	assert.Contains(t, string(body), "Log in with Navidrome",
		"login form must be re-rendered")

	// No session cookie may be set on failed auth.
	assert.Empty(t, resp.Cookies(), "no session cookie on failed login")

	// User must not have been created.
	exists, _ := client.User.Query().Where(user.Username("testuser")).Exist(context.Background())
	assert.False(t, exists)
}

// TestLogout_RequiresPOST verifies logout is POST-only, mirroring the route
// registration in cmd/server/main.go. A cross-site GET must not terminate the
// session because SameSite=Lax sends the cookie on top-level GET navigations.
// Governing: ADR-0028
func TestLogout_RequiresPOST(t *testing.T) {
	client := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{}
	bus := events.NewBus()
	syncer := services.NewSyncer(client, cfg, logger, bus, nil)
	encryptor, _ := crypto.NewEncryptor(make([]byte, 32))
	jwtManager := auth.NewJWTManager(testJWTSecret)
	h := handlers.New(client, cfg, logger, encryptor, jwtManager, syncer, nil, nil, nil, nil, nil, bus, nil)

	// Mirror the POST-only registration in cmd/server/main.go.
	r := chi.NewRouter()
	r.Post("/logout", h.Logout)
	r.Post("/auth/logout", h.Logout)

	for _, path := range []string{"/logout", "/auth/logout"} {
		t.Run("GET "+path+" does not terminate session", func(t *testing.T) {
			req := httptest.NewRequest("GET", path, nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			resp := w.Result()
			assert.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
			assert.Empty(t, resp.Cookies(), "GET must not clear the session cookie")
		})

		t.Run("POST "+path+" clears session and redirects", func(t *testing.T) {
			req := httptest.NewRequest("POST", path, nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			resp := w.Result()
			assert.Equal(t, http.StatusSeeOther, resp.StatusCode)
			assert.Equal(t, "/auth/login", resp.Header.Get("Location"))

			cookies := resp.Cookies()
			require.Len(t, cookies, 1)
			c := cookies[0]
			assert.Equal(t, handlers.CookieName, c.Name)
			assert.Empty(t, c.Value)
			assert.Negative(t, c.MaxAge, "cookie must be expired")
			assert.True(t, c.HttpOnly)
			assert.Equal(t, http.SameSiteLaxMode, c.SameSite,
				"logout cookie must preserve SameSite=Lax so the browser clears the login cookie")
		})
	}
}

func TestPostLogin_SecureCookieFlag(t *testing.T) {
	// Test that Secure flag is set based on config

	testCases := []struct {
		name           string
		secureCookies  bool
		expectedSecure bool
	}{
		{
			name:           "Secure cookies enabled",
			secureCookies:  true,
			expectedSecure: true,
		},
		{
			name:           "Secure cookies disabled",
			secureCookies:  false,
			expectedSecure: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Mock Navidrome server
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				response := map[string]interface{}{
					"subsonic-response": map[string]interface{}{
						"status":  "ok",
						"version": "1.16.1",
					},
				}
				if err := json.NewEncoder(w).Encode(response); err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
				}
			}))
			defer ts.Close()

			client := setupTestDB(t)
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			cfg := &config.Config{}
			cfg.Navidrome.BaseURL = ts.URL
			cfg.Security.SecureCookies = tc.secureCookies

			bus := events.NewBus()
			syncer := services.NewSyncer(client, cfg, logger, bus, nil)
			encryptor, err := crypto.NewEncryptor(make([]byte, 32))
			require.NoError(t, err)
			jwtManager := auth.NewJWTManager(testJWTSecret)
			h := handlers.New(client, cfg, logger, encryptor, jwtManager, syncer, nil, nil, nil, nil, nil, bus, nil)

			// Create POST request
			form := url.Values{}
			form.Add("username", "testuser")
			form.Add("password", "secret123")
			req := httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

			w := httptest.NewRecorder()
			h.PostLogin(w, req)

			// Find the session cookie
			cookies := w.Result().Cookies()
			var sessionCookie *http.Cookie
			for _, c := range cookies {
				if c.Name == handlers.CookieName {
					sessionCookie = c
					break
				}
			}
			require.NotNil(t, sessionCookie, "Session cookie should be set")
			assert.Equal(t, tc.expectedSecure, sessionCookie.Secure, "Cookie Secure flag should match config")
			assert.True(t, sessionCookie.HttpOnly, "Cookie should be HttpOnly")
			assert.Equal(t, http.SameSiteLaxMode, sessionCookie.SameSite, "Session cookie should use SameSite=Lax for OAuth compatibility")
		})
	}
}
