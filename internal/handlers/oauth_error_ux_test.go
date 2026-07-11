// End-to-end tests for the OAuth/login error UX fix (bead spotter-0fr):
// the login page must never echo the raw ?error= query value, and the
// preferences providers page must surface OAuth failure redirects.
package handlers_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"spotter/internal/handlers"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLogin_Regression_ErrorParamNotReflected verifies the login page no
// longer displays the raw ?error= query value.
//
// Regression (spotter-0fr): Login passed r.URL.Query().Get("error") straight
// into the template, so /auth/login?error=<anything> displayed arbitrary
// attacker-controlled text in the error alert (templ HTML-escapes, so it was
// reflected content rather than XSS — still a phishing vector: an attacker
// could make the login page display "your account is locked, go to ...").
func TestLogin_Regression_ErrorParamNotReflected(t *testing.T) {
	h, _, _, _ := newFuzzAuthHandler(t, "http://navidrome.invalid")

	const marker = "pwned-by-attacker-3cf29"
	payloads := []string{
		marker,
		"<script>" + marker + "</script>",
		"Your account is locked! Call " + marker,
	}

	for _, payload := range payloads {
		req := httptest.NewRequest("GET", "/auth/login?error="+url.QueryEscape(payload), nil)
		w := httptest.NewRecorder()
		h.Login(w, req)

		res := w.Result()
		require.Equal(t, http.StatusOK, res.StatusCode)
		body, _ := io.ReadAll(res.Body)

		assert.NotContains(t, string(body), marker,
			"raw ?error= value must never appear in the page")
		assert.Contains(t, string(body), "Something went wrong during sign-in",
			"unknown error codes must show the generic fallback message")
		assert.Contains(t, string(body), "alert-error", "the alert itself must render")
	}
}

// TestLogin_KnownErrorCodes_ShowFriendlyMessages verifies every error code our
// OAuth flows redirect to /auth/login with is rendered as plain English.
func TestLogin_KnownErrorCodes_ShowFriendlyMessages(t *testing.T) {
	h, _, _, _ := newFuzzAuthHandler(t, "http://navidrome.invalid")

	// Codes redirected to /auth/login (see spotify_auth.go, lastfm_auth.go).
	cases := map[string]string{
		"session_required": "Please log in before connecting a service",
		"session_expired":  "Your session expired during sign-in",
		"invalid_state":    "The sign-in request could not be verified",
		"missing_token":    "Last.fm did not return an authorization token",
	}

	for code, wantSnippet := range cases {
		t.Run(code, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/auth/login?error="+code, nil)
			w := httptest.NewRecorder()
			h.Login(w, req)

			res := w.Result()
			require.Equal(t, http.StatusOK, res.StatusCode)
			body, _ := io.ReadAll(res.Body)
			assert.Contains(t, string(body), wantSnippet)
			assert.Contains(t, string(body), "alert-error")
			assert.NotContains(t, string(body), ">"+code+"<",
				"the bare code must not be shown to the user")
		})
	}
}

// TestLogin_NoErrorParam_NoAlert verifies a clean login page renders without
// an error alert.
func TestLogin_NoErrorParam_NoAlert(t *testing.T) {
	h, _, _, _ := newFuzzAuthHandler(t, "http://navidrome.invalid")

	req := httptest.NewRequest("GET", "/auth/login", nil)
	w := httptest.NewRecorder()
	h.Login(w, req)

	res := w.Result()
	require.Equal(t, http.StatusOK, res.StatusCode)
	body, _ := io.ReadAll(res.Body)
	assert.NotContains(t, string(body), "alert-error")
	assert.NotContains(t, string(body), "Something went wrong during sign-in")
}

// TestPreferencesProviders_Regression_OAuthErrorSurfaced verifies the
// providers page surfaces OAuth failure redirects.
//
// Regression (spotter-0fr): PreferencesProviders ignored ?error= entirely, so
// the OAuth failure redirects to /preferences/providers?error=spotify_denied,
// ?error=missing_code, and ?error=exchange_failed (spotify_auth.go,
// lastfm_auth.go) rendered the page with no feedback at all — a failed
// connection looked identical to never having tried.
func TestPreferencesProviders_Regression_OAuthErrorSurfaced(t *testing.T) {
	h, client, _, _ := newFuzzAuthHandler(t, "http://navidrome.invalid")
	u, err := client.User.Create().SetUsername("prefsuser").Save(context.Background())
	require.NoError(t, err)

	render := func(t *testing.T, query string) string {
		t.Helper()
		req := httptest.NewRequest("GET", "/preferences/providers"+query, nil)
		req = req.WithContext(context.WithValue(req.Context(), handlers.UserContextKey, u))
		w := httptest.NewRecorder()
		h.PreferencesProviders(w, req)
		res := w.Result()
		require.Equal(t, http.StatusOK, res.StatusCode)
		body, _ := io.ReadAll(res.Body)
		return string(body)
	}

	// Codes redirected to /preferences/providers (see spotify_auth.go, lastfm_auth.go).
	t.Run("spotify_denied shows a message", func(t *testing.T) {
		body := render(t, "?error=spotify_denied")
		assert.Contains(t, body, "Spotify access was declined")
		assert.Contains(t, body, "alert-error")
	})

	t.Run("missing_code shows a message", func(t *testing.T) {
		body := render(t, "?error=missing_code")
		assert.Contains(t, body, "did not return an authorization code")
	})

	t.Run("exchange_failed shows a message", func(t *testing.T) {
		body := render(t, "?error=exchange_failed")
		assert.Contains(t, body, "finish connecting your account")
	})

	t.Run("unknown code shows generic fallback, never the raw value", func(t *testing.T) {
		const marker = "pwned-by-attacker-77b1a"
		body := render(t, "?error="+url.QueryEscape("<b>"+marker+"</b>"))
		assert.NotContains(t, body, marker, "raw ?error= value must never appear in the page")
		assert.Contains(t, body, "Something went wrong during sign-in")
	})

	t.Run("no error param renders no error alert", func(t *testing.T) {
		body := render(t, "")
		assert.NotContains(t, body, "alert alert-error")
		assert.NotContains(t, body, "Something went wrong during sign-in")
	})
}
