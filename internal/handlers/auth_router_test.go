package handlers_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"spotter/internal/auth"
	"spotter/internal/config"
	"spotter/internal/crypto"
	"spotter/internal/events"
	"spotter/internal/handlers"
	"spotter/internal/services"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newAuthRouter builds a chi router with the auth routes registered exactly
// as cmd/server/main.go registers them (public group; rate limiting omitted),
// so these tests exercise the same method/path wiring production uses.
func newAuthRouter(h *handlers.Handler) *chi.Mux {
	r := chi.NewRouter()
	r.Get("/auth/login", h.Login)
	r.Post("/login", h.PostLogin)
	// Governing: ADR-0028 — logout is POST-only.
	r.Post("/logout", h.Logout)
	r.Post("/auth/logout", h.Logout)
	return r
}

// newAuthTestHandler wires a Handler against an in-memory DB and the given
// Navidrome base URL, mirroring the setup used by the handler-level tests.
func newAuthTestHandler(t *testing.T, navidromeURL string) (*handlers.Handler, *auth.JWTManager) {
	t.Helper()
	client := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{}
	cfg.Navidrome.BaseURL = navidromeURL
	bus := events.NewBus()
	syncer := services.NewSyncer(client, cfg, logger, bus, nil)
	encryptor, err := crypto.NewEncryptor(make([]byte, 32))
	require.NoError(t, err)
	jwtManager := auth.NewJWTManager(testJWTSecret)
	h := handlers.New(client, cfg, logger, encryptor, jwtManager, syncer, nil, nil, nil, nil, nil, bus, nil)
	return h, jwtManager
}

func loginForm(username, password string) *strings.Reader {
	form := url.Values{}
	form.Set("username", username)
	form.Set("password", password)
	return strings.NewReader(form.Encode())
}

// TestRouter_HTMXLoginFailure_ErrorVisibleInSwappedBody drives a failed HTMX
// login through the router (not the bare handler) and asserts the response
// body — the document htmx swaps into hx-target="body" — carries the error
// alert. This is the end-to-end shape of the "wrong password shows feedback"
// fix; TestPostLogin_InvalidCredentials_HTMX covers the same contract at the
// handler level.
//
// Governing: SPEC user-authentication
func TestRouter_HTMXLoginFailure_ErrorVisibleInSwappedBody(t *testing.T) {
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

	h, _ := newAuthTestHandler(t, ts.URL)
	r := newAuthRouter(h)

	req := httptest.NewRequest("POST", "/login", loginForm("testuser", "wrongpassword"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	resp := w.Result()
	require.Equal(t, http.StatusOK, resp.StatusCode,
		"HTMX failure must be 200 so htmx 1.9 performs the body swap")
	body, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(body), "alert-error",
		"swapped body must contain the error alert markup")
	assert.Contains(t, string(body), "Invalid username or password",
		"swapped body must contain the error message")
	assert.Contains(t, string(body), `hx-target="body"`,
		"re-rendered form must keep the body swap target so retries behave the same")
	assert.Empty(t, w.Header().Get("HX-Redirect"), "failed login must not redirect")
	assert.Empty(t, resp.Cookies(), "failed login must not set a session cookie")
}

// TestRouter_SessionSurvivesGETLogout_ExpiredByPOSTLogout runs the full
// login → GET /logout → POST /logout flow through the router and asserts the
// session cookie survives the GET attempt (405, no Set-Cookie) but is expired
// by the POST. Extends TestLogout_RequiresPOST by starting from a real login
// and verifying the surviving cookie is still a valid session token.
//
// Governing: ADR-0028
func TestRouter_SessionSurvivesGETLogout_ExpiredByPOSTLogout(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"subsonic-response": map[string]interface{}{"status": "ok"},
		})
	}))
	defer ts.Close()

	h, jwtManager := newAuthTestHandler(t, ts.URL)
	r := newAuthRouter(h)

	// 1. Log in through the router and capture the session cookie.
	loginReq := httptest.NewRequest("POST", "/login", loginForm("testuser", "secret123"))
	loginReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	loginW := httptest.NewRecorder()
	r.ServeHTTP(loginW, loginReq)

	require.Equal(t, http.StatusSeeOther, loginW.Result().StatusCode)
	var session *http.Cookie
	for _, c := range loginW.Result().Cookies() {
		if c.Name == handlers.CookieName {
			session = c
		}
	}
	require.NotNil(t, session, "login must set the session cookie")

	// 2. A (cross-site-shaped) GET /logout must not touch the session.
	getReq := httptest.NewRequest("GET", "/logout", nil)
	getReq.AddCookie(session)
	getW := httptest.NewRecorder()
	r.ServeHTTP(getW, getReq)

	assert.Equal(t, http.StatusMethodNotAllowed, getW.Result().StatusCode)
	assert.Empty(t, getW.Result().Cookies(),
		"GET /logout must not send any Set-Cookie — the browser keeps the session")
	// The token the client still holds must remain a valid session.
	claims, err := jwtManager.ValidateToken(session.Value)
	require.NoError(t, err, "session token must still be valid after the GET attempt")
	assert.Equal(t, "testuser", claims.Username)

	// 3. POST /logout with the same cookie ends the session.
	postReq := httptest.NewRequest("POST", "/logout", nil)
	postReq.AddCookie(session)
	postW := httptest.NewRecorder()
	r.ServeHTTP(postW, postReq)

	resp := postW.Result()
	assert.Equal(t, http.StatusSeeOther, resp.StatusCode)
	assert.Equal(t, "/auth/login", resp.Header.Get("Location"))
	cookies := resp.Cookies()
	require.Len(t, cookies, 1, "POST /logout must expire the session cookie")
	expired := cookies[0]
	assert.Equal(t, handlers.CookieName, expired.Name)
	assert.Empty(t, expired.Value)
	assert.Negative(t, expired.MaxAge, "cookie must be expired")
	assert.Equal(t, "/", expired.Path, "expiry must target the same cookie path as login")
}
