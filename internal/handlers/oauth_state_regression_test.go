// Regression tests for a real bug surfaced by the spotter-h1y fuzz/adversarial
// work on OAuth callback state parsing.
//
// Bug: SpotifyCallback and LastFMCallback split the "state:encrypted_user_id"
// format at the LAST colon. That was correct while ciphertexts were bare
// base64 (no colons), but commit 05b7f43 (key rotation, SPEC key-rotation)
// prefixed all new ciphertexts with "enc:v1:" — which contains colons. The
// last-colon split then produced csrfState = "<state>:enc:v1", which can never
// equal the state cookie ("<state>"), so EVERY legitimate Spotify OAuth
// callback failed CSRF validation and bounced to /auth/login?error=invalid_state.
// (Last.fm kept working only by accident: it has no CSRF comparison, and the
// base64 tail after the last colon still decrypted via the legacy path.)
//
// Fix: split at the FIRST colon — the CSRF token is base64.URLEncoding and can
// never contain ':', while everything after the first colon is the ciphertext.
package handlers_test

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"log/slog"

	"spotter/ent"
	"spotter/internal/auth"
	"spotter/internal/config"
	"spotter/internal/crypto"
	"spotter/internal/events"
	"spotter/internal/handlers"
	"spotter/internal/services"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newLoggedAuthHandler wires a Handler like newFuzzAuthHandler but with a slog
// logger writing to a captured buffer, so tests can use the handler's distinct
// log messages as an oracle to tell apart failure paths that share a redirect
// (e.g. decrypt failure vs deleted-user lookup, both -> session_expired).
func newLoggedAuthHandler(t *testing.T) (*handlers.Handler, *ent.Client, *crypto.Encryptor, *bytes.Buffer) {
	t.Helper()
	client := setupTestDB(t)
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))
	cfg := &config.Config{}
	cfg.Navidrome.BaseURL = "http://navidrome.invalid"
	bus := events.NewBus()
	syncer := services.NewSyncer(client, cfg, logger, bus, nil)
	encryptor, err := crypto.NewEncryptor(make([]byte, 32))
	require.NoError(t, err)
	jwtManager := auth.NewJWTManager(testJWTSecret)
	h := handlers.New(client, cfg, logger, encryptor, jwtManager, syncer, nil, nil, nil, nil, nil, bus, nil)
	return h, client, encryptor, &logBuf
}

// Exact production log messages from LastFMCallback (lastfm_auth.go). Keep in
// sync with the handler; the regression tests below use them as oracles.
const (
	lastfmDecryptFailLog = "Last.fm callback: failed to decrypt user ID from state"
	lastfmUserLoadLog    = "Last.fm callback: failed to load user from database"
)

// TestSpotifyCallback_Regression_StateSurvivesEncV1Prefix drives the real
// SpotifyLogin → SpotifyCallback round trip: the state parameter Spotify would
// echo back and the CSRF cookie both come from the actual login handler.
// Without the first-colon fix, the callback misparses the state and rejects
// the flow with invalid_state; with the fix it passes CSRF validation,
// recovers the user, and proceeds to the missing_code check (no code is sent,
// keeping the test network-free).
func TestSpotifyCallback_Regression_StateSurvivesEncV1Prefix(t *testing.T) {
	h, client, _, _ := newFuzzAuthHandler(t, "http://navidrome.invalid")
	h.Config.Spotify.ClientID = "test-client-id"
	h.Config.Spotify.ClientSecret = "test-client-secret"

	u, err := client.User.Create().SetUsername("roundtrip").Save(context.Background())
	require.NoError(t, err)

	// 1. Initiate the OAuth flow exactly as production does.
	loginReq := httptest.NewRequest("GET", "/auth/spotify/login", nil)
	loginReq = loginReq.WithContext(context.WithValue(loginReq.Context(), handlers.UserContextKey, u))
	loginW := httptest.NewRecorder()
	h.SpotifyLogin(loginW, loginReq)

	loginRes := loginW.Result()
	require.Equal(t, http.StatusSeeOther, loginRes.StatusCode)

	// Extract the state parameter Spotify would echo back on the callback.
	authURL, err := url.Parse(loginRes.Header.Get("Location"))
	require.NoError(t, err)
	state := authURL.Query().Get("state")
	require.NotEmpty(t, state, "login must embed a state parameter in the auth URL")

	// Extract the CSRF state cookie set by the login handler.
	var stateCookie *http.Cookie
	for _, c := range loginRes.Cookies() {
		if c.Name == spotifyStateCookieName {
			stateCookie = c
		}
	}
	require.NotNil(t, stateCookie, "login must set the OAuth state cookie")

	// 2. Simulate Spotify redirecting back (without a code, so the flow stops
	// at the missing_code check instead of the network-bound token exchange).
	cbReq := httptest.NewRequest("GET", "/auth/spotify/callback?state="+url.QueryEscape(state), nil)
	cbReq.AddCookie(stateCookie)
	cbW := httptest.NewRecorder()
	h.SpotifyCallback(cbW, cbReq)

	cbRes := cbW.Result()
	require.Equal(t, http.StatusSeeOther, cbRes.StatusCode)
	loc := cbRes.Header.Get("Location")
	assert.NotEqual(t, "/auth/login?error=invalid_state", loc,
		"a legitimate callback must not fail CSRF validation — the enc:v1: "+
			"ciphertext prefix must not corrupt state parsing")
	assert.NotEqual(t, "/auth/login?error=session_expired", loc,
		"a legitimate callback must decrypt the user ID from the state")
	assert.Equal(t, "/preferences/providers?error=missing_code", loc,
		"with state and cookie valid, the flow must reach the code check")
}

// TestLastFMCallback_Regression_LoginCookieRoundTrip drives the real
// LastFMLogin → LastFMCallback round trip with the state cookie produced by
// the login handler. The user is deleted between the two steps so the callback
// stops at the user lookup — the last observable stage before the
// network-bound Last.fm token exchange.
//
// The terminal redirect alone is an ambiguous oracle: decrypt failure and the
// deleted-user lookup both redirect to session_expired. This test therefore
// captures the handler's slog output and asserts on the distinct per-stage log
// messages: the decrypt-failure message must be absent and the user-lookup
// failure message must be present with exactly the user ID that LastFMLogin
// encrypted — proving the parsed ciphertext decrypted successfully to the
// right value and the flow proceeded past decryption.
//
// Note: for a login-issued cookie ("<state>:enc:v1:<base64>") a last-colon
// parse is behaviorally EQUIVALENT to the first-colon parse, because the
// legacy bare-base64 decrypt path accepts the post-last-colon tail and yields
// the same user ID. This test pins the correct end-to-end behavior; the
// parse-boundary mutant itself is killed by
// TestLastFMCallback_Regression_FirstColonParseBoundary below, which uses an
// input where the two parses diverge.
func TestLastFMCallback_Regression_LoginCookieRoundTrip(t *testing.T) {
	h, client, _, logBuf := newLoggedAuthHandler(t)
	h.Config.LastFM.APIKey = "test-api-key"
	h.Config.LastFM.SharedSecret = "test-shared-secret"

	ctx := context.Background()
	u, err := client.User.Create().SetUsername("lastfmtrip").Save(ctx)
	require.NoError(t, err)

	// 1. Initiate the flow to obtain the real state cookie.
	loginReq := httptest.NewRequest("GET", "/auth/lastfm/login", nil)
	loginReq = loginReq.WithContext(context.WithValue(loginReq.Context(), handlers.UserContextKey, u))
	loginW := httptest.NewRecorder()
	h.LastFMLogin(loginW, loginReq)

	loginRes := loginW.Result()
	require.Equal(t, http.StatusSeeOther, loginRes.StatusCode)

	var stateCookie *http.Cookie
	for _, c := range loginRes.Cookies() {
		if c.Name == lastfmStateCookieName {
			stateCookie = c
		}
	}
	require.NotNil(t, stateCookie, "login must set the Last.fm state cookie")

	// 2. Delete the user so the callback must stop at the user lookup instead
	// of reaching the network-bound token exchange.
	require.NoError(t, client.User.DeleteOneID(u.ID).Exec(ctx))

	cbReq := httptest.NewRequest("GET", "/auth/lastfm/callback?token=sometoken", nil)
	cbReq.AddCookie(stateCookie)
	cbW := httptest.NewRecorder()
	h.LastFMCallback(cbW, cbReq)

	cbRes := cbW.Result()
	require.Equal(t, http.StatusSeeOther, cbRes.StatusCode)
	assert.Equal(t, "/auth/login?error=session_expired", cbRes.Header.Get("Location"))

	logs := logBuf.String()
	assert.NotContains(t, logs, lastfmDecryptFailLog,
		"the login-issued state cookie must decrypt cleanly — a decrypt failure "+
			"here means state parsing or ciphertext handling regressed")
	assert.Contains(t, logs, lastfmUserLoadLog,
		"the flow must get past decryption all the way to the user lookup")
	assert.Contains(t, logs, fmt.Sprintf("user_id=%d", u.ID),
		"the decrypted user ID must be exactly the one LastFMLogin encrypted")
}

// TestLastFMCallback_Regression_FirstColonParseBoundary pins the state-parse
// contract itself: everything after the FIRST colon is the ciphertext and must
// decrypt AS A WHOLE. It crafts the one input class where a first-colon and a
// last-colon parse diverge observably: a cookie whose post-first-colon tail is
// corrupted but whose post-last-colon tail is a valid legacy (bare base64)
// ciphertext:
//
//	"<state>:corrupted:<bare-base64-ciphertext>"
//
// Correct (first-colon) behavior: the ciphertext is "corrupted:<base64>",
// which fails base64 decoding — the handler must log the decrypt failure and
// stop before any user lookup.
//
// Under a last-colon mutant the parser salvages just "<base64>", which the
// legacy decrypt path accepts, so the flow wrongly proceeds to the user lookup
// (an attacker-mangled cookie treated as valid). The log assertions below then
// fail on both counts, killing the mutant (verified by applying the mutant to
// lastfm_auth.go and watching this test fail).
func TestLastFMCallback_Regression_FirstColonParseBoundary(t *testing.T) {
	h, _, encryptor, logBuf := newLoggedAuthHandler(t)
	h.Config.LastFM.APIKey = "test-api-key"
	h.Config.LastFM.SharedSecret = "test-shared-secret"

	// A real ciphertext, stripped to the legacy bare-base64 form the decryptor
	// still accepts. The encrypted ID deliberately matches no user so that even
	// a mutant that wrongly decrypts the tail stops at the user lookup and
	// never reaches the network-bound token exchange.
	enc, err := encryptor.EncryptInt(424242)
	require.NoError(t, err)
	bare := strings.TrimPrefix(enc, crypto.EncPrefixV1)
	require.NotEqual(t, enc, bare, "ciphertexts must carry the enc:v1: marker")

	cookieVal := "csrfstatetoken:corrupted:" + bare

	cbReq := httptest.NewRequest("GET", "/auth/lastfm/callback?token=sometoken", nil)
	cbReq.AddCookie(&http.Cookie{Name: lastfmStateCookieName, Value: cookieVal})
	cbW := httptest.NewRecorder()
	h.LastFMCallback(cbW, cbReq)

	cbRes := cbW.Result()
	require.Equal(t, http.StatusSeeOther, cbRes.StatusCode)
	assert.Equal(t, "/auth/login?error=session_expired", cbRes.Header.Get("Location"))

	logs := logBuf.String()
	assert.Contains(t, logs, lastfmDecryptFailLog,
		"the full post-first-colon tail is corrupted and must fail decryption — "+
			"its absence means the parser salvaged a trailing ciphertext fragment "+
			"(last-colon split) instead of splitting at the first colon")
	assert.NotContains(t, logs, lastfmUserLoadLog,
		"a cookie whose ciphertext fails decryption must never reach the user lookup")
}
