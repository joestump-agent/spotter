// Governing: ADR-0005 (Navidrome primary identity), SPEC user-authentication (OAuth error redirects)
package handlers

// oauthErrorMessages maps the error codes produced by our own auth redirects
// (?error=<code> on /auth/login and /preferences/providers) to plain-English,
// user-facing messages. The full inventory of codes comes from the redirect
// sites in auth.go, spotify_auth.go, and lastfm_auth.go.
var oauthErrorMessages = map[string]string{
	// spotify_auth.go, lastfm_auth.go — OAuth initiated without a session
	"session_required": "Please log in before connecting a service.",
	// spotify_auth.go, lastfm_auth.go — state cookie missing/expired, or the
	// user could not be recovered from the OAuth state
	"session_expired": "Your session expired during sign-in. Please log in and try again.",
	// spotify_auth.go, lastfm_auth.go — missing/malformed state or CSRF mismatch
	"invalid_state": "The sign-in request could not be verified. Please try connecting again.",
	// spotify_auth.go — user declined the Spotify consent screen (or Spotify errored)
	"spotify_denied": "Spotify access was declined. To connect Spotify, approve access when prompted.",
	// spotify_auth.go — callback arrived without an authorization code
	"missing_code": "Spotify did not return an authorization code. Please try connecting again.",
	// lastfm_auth.go — callback arrived without an authorization token
	"missing_token": "Last.fm did not return an authorization token. Please try connecting again.",
	// spotify_auth.go, lastfm_auth.go — token/code exchange with the provider failed
	"exchange_failed": "We couldn't finish connecting your account. Please try again in a moment.",
}

// getOAuthErrorMessage translates a ?error= code from an auth redirect into a
// user-facing message. Unknown codes get a generic fallback: the raw query
// value is attacker-controlled and must never be echoed back to the page
// (reflected-content smell), even though templ escapes output.
func getOAuthErrorMessage(code string) string {
	if code == "" {
		return ""
	}
	if msg, ok := oauthErrorMessages[code]; ok {
		return msg
	}
	return "Something went wrong during sign-in. Please try again."
}
