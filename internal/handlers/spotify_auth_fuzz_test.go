// Fuzz and adversarial tests for the Spotify OAuth callback (spotter-h1y).
// The prime target is the "state:encrypted_user_id" state format parsed by
// SpotifyCallback (spotify_auth.go).
package handlers_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"spotter/ent"
	"spotter/ent/spotifyauth"
	"spotter/internal/handlers"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// spotifyStateCookieName mirrors the unexported spotifyStateCookie constant in
// internal/handlers/spotify_auth.go.
const spotifyStateCookieName = "spotify_oauth_state"

// spotifyCallbackSafeLocations is the closed set of redirect targets the
// callback may produce for any request that never reaches the token exchange
// (i.e. any request without a valid authorization code). Membership in this
// set proves no attacker input is reflected into the redirect (no open
// redirect, no echoed error text).
var spotifyCallbackSafeLocations = map[string]bool{
	"/preferences/providers?error=spotify_denied": true,
	"/preferences/providers?error=missing_code":   true,
	"/auth/login?error=invalid_state":             true,
	"/auth/login?error=session_expired":           true,
}

// FuzzSpotifyCallbackState fuzzes the state query parameter, the state cookie,
// and the provider error parameter of GET /auth/spotify/callback. The `code`
// parameter is deliberately never set so the handler can never reach the
// (network-bound) token exchange; every outcome must be a 303 redirect to one
// of the fixed safe locations — never a panic, never a 5xx.
func FuzzSpotifyCallbackState(f *testing.F) {
	h, client, _, encryptor := newFuzzAuthHandler(f, "http://navidrome.invalid")

	u, err := client.User.Create().SetUsername("spotifyfuzz").Save(context.Background())
	require.NoError(f, err)
	encryptedID, err := encryptor.EncryptInt(u.ID)
	require.NoError(f, err)

	// (state, cookie, errParam)
	f.Add("", "", "")
	f.Add("csrftoken:"+encryptedID, "csrftoken", "")                                // valid format, matching cookie
	f.Add("csrftoken:"+encryptedID, "wrongcookie", "")                              // CSRF mismatch
	f.Add("csrftoken:"+encryptedID, "", "")                                         // missing cookie
	f.Add(":", "", "")                                                              // colon only
	f.Add(":"+encryptedID, "", "")                                                  // empty CSRF part
	f.Add("csrftoken:", "csrftoken", "")                                            // empty encrypted part
	f.Add("no-colon-at-all", "no-colon-at-all", "")                                 // malformed state
	f.Add("a:b:c:d", "a:b:c", "")                                                   // multiple colons (last wins)
	f.Add("csrftoken:AAAAAAAA", "csrftoken", "")                                    // garbage ciphertext
	f.Add("csrftoken:enc:v1:!!!!", "csrftoken", "")                                 // bad base64 with real prefix
	f.Add(encryptedID, "", "")                                                      // ciphertext alone (has colons from enc:v1:)
	f.Add("x://evil.example/phish", "x://evil.example", "")                         // open-redirect shape
	f.Add("irrelevant", "irrelevant", "access_denied")                              // provider-reported error
	f.Add(strings.Repeat("A", 8192)+":"+encryptedID, strings.Repeat("A", 8192), "") // oversized

	f.Fuzz(func(t *testing.T, state, cookie, errParam string) {
		q := url.Values{}
		if state != "" {
			q.Set("state", state)
		}
		if errParam != "" {
			q.Set("error", errParam)
		}
		target := "/auth/spotify/callback"
		if enc := q.Encode(); enc != "" {
			target += "?" + enc
		}
		req := httptest.NewRequest("GET", target, nil)
		if cookie != "" {
			req.AddCookie(&http.Cookie{Name: spotifyStateCookieName, Value: cookie})
		}
		w := httptest.NewRecorder()

		h.SpotifyCallback(w, req)

		res := w.Result()
		if res.StatusCode >= 500 {
			t.Fatalf("callback caused %d: state=%q cookie=%q err=%q", res.StatusCode, state, cookie, errParam)
		}
		if res.StatusCode != http.StatusSeeOther {
			t.Fatalf("callback returned %d, want 303: state=%q cookie=%q err=%q", res.StatusCode, state, cookie, errParam)
		}
		loc := res.Header.Get("Location")
		if !spotifyCallbackSafeLocations[loc] {
			t.Fatalf("callback redirected outside the safe set (possible reflection/open redirect): %q for state=%q cookie=%q err=%q", loc, state, cookie, errParam)
		}
	})
}

// callbackFixture bundles the handler and DB client for callback table tests.
type callbackFixture struct {
	h      *handlers.Handler
	client *ent.Client
}

// callback drives GET /auth/spotify/callback with the given state parameter,
// state cookie value, and provider error parameter.
func (fx *callbackFixture) callback(t *testing.T, state, cookie, errParam string) *http.Response {
	t.Helper()
	q := url.Values{}
	if state != "" {
		q.Set("state", state)
	}
	if errParam != "" {
		q.Set("error", errParam)
	}
	target := "/auth/spotify/callback"
	if enc := q.Encode(); enc != "" {
		target += "?" + enc
	}
	req := httptest.NewRequest("GET", target, nil)
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: spotifyStateCookieName, Value: cookie})
	}
	w := httptest.NewRecorder()
	fx.h.SpotifyCallback(w, req)
	return w.Result()
}

func (fx *callbackFixture) assertNoSpotifyAuth(t *testing.T) {
	t.Helper()
	n, err := fx.client.SpotifyAuth.Query().Where(spotifyauth.IDGT(0)).Count(context.Background())
	require.NoError(t, err)
	assert.Zero(t, n, "no SpotifyAuth row may be created on a rejected callback")
}

// TestSpotifyCallback_Adversarial covers state-parsing outcomes that need
// exact assertions the fuzz target cannot make (which redirect fires, that no
// SpotifyAuth row is created, that the state cookie is cleared).
func TestSpotifyCallback_Adversarial(t *testing.T) {
	newFixture := func(t *testing.T) (fx *callbackFixture, encryptedID string) {
		t.Helper()
		hh, client, _, encryptor := newFuzzAuthHandler(t, "http://navidrome.invalid")
		u, err := client.User.Create().SetUsername("spotifyadv").Save(context.Background())
		require.NoError(t, err)
		enc, err := encryptor.EncryptInt(u.ID)
		require.NoError(t, err)
		return &callbackFixture{h: hh, client: client}, enc
	}

	t.Run("CSRF mismatch redirects to invalid_state and creates no auth", func(t *testing.T) {
		fx, enc := newFixture(t)
		res := fx.callback(t, "csrf:"+enc, "different-cookie", "")
		assert.Equal(t, http.StatusSeeOther, res.StatusCode)
		assert.Equal(t, "/auth/login?error=invalid_state", res.Header.Get("Location"))
		fx.assertNoSpotifyAuth(t)
	})

	t.Run("tampered ciphertext with matching CSRF redirects to session_expired", func(t *testing.T) {
		fx, enc := newFixture(t)
		// Flip the tail of the ciphertext: fails GCM authentication (or base64).
		tampered := enc[:len(enc)-2] + "!!"
		res := fx.callback(t, "csrf:"+tampered, "csrf", "")
		assert.Equal(t, http.StatusSeeOther, res.StatusCode)
		assert.Equal(t, "/auth/login?error=session_expired", res.Header.Get("Location"))
		fx.assertNoSpotifyAuth(t)
	})

	t.Run("valid state without code redirects to missing_code and clears state cookie", func(t *testing.T) {
		fx, enc := newFixture(t)
		res := fx.callback(t, "csrf:"+enc, "csrf", "")
		assert.Equal(t, http.StatusSeeOther, res.StatusCode)
		assert.Equal(t, "/preferences/providers?error=missing_code", res.Header.Get("Location"))
		var cleared bool
		for _, c := range res.Cookies() {
			if c.Name == spotifyStateCookieName && c.Value == "" {
				cleared = true
			}
		}
		assert.True(t, cleared, "state cookie must be cleared once CSRF validation passes")
		fx.assertNoSpotifyAuth(t)
	})

	t.Run("provider error param never reaches the redirect verbatim", func(t *testing.T) {
		fx, _ := newFixture(t)
		res := fx.callback(t, "", "", "https://evil.example/phish?x=<script>")
		assert.Equal(t, http.StatusSeeOther, res.StatusCode)
		assert.Equal(t, "/preferences/providers?error=spotify_denied", res.Header.Get("Location"),
			"the provider-supplied error string must be replaced by our fixed code")
	})
}
