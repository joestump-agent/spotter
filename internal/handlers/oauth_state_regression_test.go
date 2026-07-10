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
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"spotter/internal/handlers"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
// stops at the user lookup (session_expired) instead of reaching the
// network-bound Last.fm token exchange. If state parsing or decryption ever
// regresses, this surfaces as invalid_state instead.
func TestLastFMCallback_Regression_LoginCookieRoundTrip(t *testing.T) {
	h, client, _, _ := newFuzzAuthHandler(t, "http://navidrome.invalid")
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

	// 2. Delete the user so the callback must stop before the token exchange.
	require.NoError(t, client.User.DeleteOneID(u.ID).Exec(ctx))

	cbReq := httptest.NewRequest("GET", "/auth/lastfm/callback?token=sometoken", nil)
	cbReq.AddCookie(stateCookie)
	cbW := httptest.NewRecorder()
	h.LastFMCallback(cbW, cbReq)

	cbRes := cbW.Result()
	require.Equal(t, http.StatusSeeOther, cbRes.StatusCode)
	assert.Equal(t, "/auth/login?error=session_expired", cbRes.Header.Get("Location"),
		"the login-issued cookie must parse and decrypt (failing only at the "+
			"deleted-user lookup) — invalid_state here means state parsing regressed")
}
