// Fuzz and adversarial tests for the Last.fm auth callback (spotter-h1y).
// Last.fm stores the "state:encrypted_user_id" format in the state COOKIE
// (not the query string), so the cookie value is the parsing target here.
package handlers_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"spotter/ent/lastfmauth"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// lastfmStateCookieName mirrors the unexported lastfmStateCookie constant in
// internal/handlers/lastfm_auth.go.
const lastfmStateCookieName = "lastfm_oauth_state"

// lastfmCallbackSafeLocations is the closed set of redirect targets the
// callback may produce before the token exchange stage.
var lastfmCallbackSafeLocations = map[string]bool{
	"/auth/login?error=missing_token":   true,
	"/auth/login?error=session_expired": true,
	"/auth/login?error=invalid_state":   true,
}

// FuzzLastFMCallbackState fuzzes the token query parameter and the state
// cookie of GET /auth/lastfm/callback.
//
// IMPORTANT: no user rows exist in this fixture's database, and the only
// seeded ciphertext encrypts a nonexistent user ID. The handler therefore
// always fails at (or before) the user lookup and can never reach
// ExchangeCode, which would perform a real network call to Last.fm.
// Every outcome must be a 303 redirect to a fixed safe location.
func FuzzLastFMCallbackState(f *testing.F) {
	h, _, _, encryptor := newFuzzAuthHandler(f, "http://navidrome.invalid")

	// Ciphertext that decrypts successfully but names a user that does not
	// exist — exercises the full decrypt path without reaching the exchange.
	encryptedGhostID, err := encryptor.EncryptInt(999999)
	require.NoError(f, err)

	// (token, cookie)
	f.Add("", "")                                               // missing token
	f.Add("sometoken", "")                                      // missing state cookie
	f.Add("sometoken", "csrf:"+encryptedGhostID)                // decrypts, user missing
	f.Add("sometoken", "no-colon")                              // malformed state cookie
	f.Add("sometoken", ":")                                     // colon only
	f.Add("sometoken", "csrf:")                                 // empty encrypted part
	f.Add("sometoken", ":"+encryptedGhostID)                    // empty CSRF part
	f.Add("sometoken", "a:b:c:d")                               // multiple colons
	f.Add("sometoken", "csrf:AAAAAAAA")                         // garbage ciphertext
	f.Add("sometoken", "csrf:enc:v1:!!!!")                      // bad base64 with real prefix
	f.Add("tok<script>", "csrf:"+encryptedGhostID)              // injection-shaped token
	f.Add(strings.Repeat("t", 8192), strings.Repeat("c", 8192)) // oversized

	f.Fuzz(func(t *testing.T, token, cookie string) {
		q := url.Values{}
		if token != "" {
			q.Set("token", token)
		}
		target := "/auth/lastfm/callback"
		if enc := q.Encode(); enc != "" {
			target += "?" + enc
		}
		req := httptest.NewRequest("GET", target, nil)
		if cookie != "" {
			req.AddCookie(&http.Cookie{Name: lastfmStateCookieName, Value: cookie})
		}
		w := httptest.NewRecorder()

		h.LastFMCallback(w, req)

		res := w.Result()
		if res.StatusCode >= 500 {
			t.Fatalf("callback caused %d: token=%q cookie=%q", res.StatusCode, token, cookie)
		}
		if res.StatusCode != http.StatusSeeOther {
			t.Fatalf("callback returned %d, want 303: token=%q cookie=%q", res.StatusCode, token, cookie)
		}
		loc := res.Header.Get("Location")
		if !lastfmCallbackSafeLocations[loc] {
			t.Fatalf("callback redirected outside the safe set (possible reflection/open redirect): %q for token=%q cookie=%q", loc, token, cookie)
		}
	})
}

// TestLastFMCallback_Adversarial pins the exact redirect for each rejection
// path and asserts no LastFMAuth row is ever created.
func TestLastFMCallback_Adversarial(t *testing.T) {
	h, client, _, encryptor := newFuzzAuthHandler(t, "http://navidrome.invalid")
	encryptedGhostID, err := encryptor.EncryptInt(424242)
	require.NoError(t, err)

	drive := func(t *testing.T, token, cookie string) *http.Response {
		t.Helper()
		q := url.Values{}
		if token != "" {
			q.Set("token", token)
		}
		target := "/auth/lastfm/callback"
		if enc := q.Encode(); enc != "" {
			target += "?" + enc
		}
		req := httptest.NewRequest("GET", target, nil)
		if cookie != "" {
			req.AddCookie(&http.Cookie{Name: lastfmStateCookieName, Value: cookie})
		}
		w := httptest.NewRecorder()
		h.LastFMCallback(w, req)
		return w.Result()
	}

	cases := []struct {
		name         string
		token        string
		cookie       string
		wantLocation string
	}{
		{"missing token", "", "csrf:" + encryptedGhostID, "/auth/login?error=missing_token"},
		{"missing state cookie", "tok", "", "/auth/login?error=session_expired"},
		{"state cookie without colon", "tok", "no-colon-here", "/auth/login?error=invalid_state"},
		{"tampered ciphertext", "tok", "csrf:" + encryptedGhostID[:len(encryptedGhostID)-2] + "!!", "/auth/login?error=session_expired"},
		{"decrypts to nonexistent user", "tok", "csrf:" + encryptedGhostID, "/auth/login?error=session_expired"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := drive(t, tc.token, tc.cookie)
			assert.Equal(t, http.StatusSeeOther, res.StatusCode)
			assert.Equal(t, tc.wantLocation, res.Header.Get("Location"))
		})
	}

	n, err := client.LastFMAuth.Query().Where(lastfmauth.IDGT(0)).Count(context.Background())
	require.NoError(t, err)
	assert.Zero(t, n, "no LastFMAuth row may be created on rejected callbacks")
}
