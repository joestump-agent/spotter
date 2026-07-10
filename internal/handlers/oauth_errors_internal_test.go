package handlers

import (
	"strings"
	"testing"
)

// producedErrorCodes is the full inventory of ?error= codes produced by the
// redirect sites in spotify_auth.go and lastfm_auth.go (bead spotter-0fr).
var producedErrorCodes = []string{
	"session_required", // spotify_auth.go:40, lastfm_auth.go:31
	"session_expired",  // spotify_auth.go:132,151,164; lastfm_auth.go:96,124,137
	"invalid_state",    // spotify_auth.go:105,121,141; lastfm_auth.go:112
	"spotify_denied",   // spotify_auth.go:97
	"missing_code",     // spotify_auth.go:181
	"missing_token",    // lastfm_auth.go:88
	"exchange_failed",  // spotify_auth.go:191, lastfm_auth.go:156
}

func TestGetOAuthErrorMessage_MapsEveryProducedCode(t *testing.T) {
	generic := getOAuthErrorMessage("__definitely_not_a_known_code__")
	for _, code := range producedErrorCodes {
		msg := getOAuthErrorMessage(code)
		if msg == "" {
			t.Errorf("code %q must map to a message", code)
		}
		if msg == generic {
			t.Errorf("code %q fell through to the generic fallback; add it to oauthErrorMessages", code)
		}
		if strings.Contains(msg, code) {
			t.Errorf("message for %q must be plain English, not echo the code: %q", code, msg)
		}
	}
}

func TestGetOAuthErrorMessage_EmptyCodeYieldsNoMessage(t *testing.T) {
	if msg := getOAuthErrorMessage(""); msg != "" {
		t.Errorf("empty code must yield empty message, got %q", msg)
	}
}

func TestGetOAuthErrorMessage_UnknownCodesGetGenericFallback(t *testing.T) {
	generic := getOAuthErrorMessage("__definitely_not_a_known_code__")
	for _, code := range []string{
		"<script>alert(1)</script>",
		"SPOTIFY_DENIED",  // case-sensitive: not a known code
		"spotify_denied ", // trailing space: not a known code
		"totally_made_up",
		"https://evil.example/phish",
	} {
		msg := getOAuthErrorMessage(code)
		if msg != generic {
			t.Errorf("unknown code %q must get the generic fallback, got %q", code, msg)
		}
	}
}

// FuzzGetOAuthErrorMessage proves the mapping is a closed set: for ANY input,
// the output is empty, one of the fixed known messages, or the generic
// fallback — the attacker-controlled input can never appear in the output.
// Regression guard for spotter-0fr (login page reflected the raw ?error=
// query value verbatim).
func FuzzGetOAuthErrorMessage(f *testing.F) {
	allowed := map[string]bool{"": true}
	for _, m := range oauthErrorMessages {
		allowed[m] = true
	}
	allowed[getOAuthErrorMessage("__definitely_not_a_known_code__")] = true

	f.Add("")
	f.Add("spotify_denied")
	f.Add("<script>alert(document.cookie)</script>")
	f.Add("session_expired\x00")
	f.Add(strings.Repeat("A", 65536))
	f.Add("%3Cimg%20src=x%20onerror=alert(1)%3E")

	f.Fuzz(func(t *testing.T, code string) {
		msg := getOAuthErrorMessage(code)
		if !allowed[msg] {
			t.Fatalf("output escaped the fixed message set (reflected content): %q -> %q", code, msg)
		}
		if code == "" && msg != "" {
			t.Fatalf("empty code must yield empty message, got %q", msg)
		}
		if code != "" && msg == "" {
			t.Fatalf("non-empty code %q must yield a message", code)
		}
	})
}
