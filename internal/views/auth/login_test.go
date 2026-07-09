package auth_test

import (
	"bytes"
	"context"
	"testing"

	"spotter/internal/views/auth"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// renderLogin renders the Login templ component directly (no handler) so the
// markup contract can be asserted independently of HTTP plumbing.
func renderLogin(t *testing.T, navidromeURL, errorMsg string) string {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, auth.Login(navidromeURL, errorMsg).Render(context.Background(), &buf))
	return buf.String()
}

// TestLoginTemplate_ErrorAlert verifies that when an error message is passed,
// the login page renders a visible error alert containing that message.
//
// Governing: SPEC user-authentication — failed logins must surface feedback;
// the HTMX flow (hx-target="body") swaps this whole page in, so the alert
// markup is what the user actually sees after a wrong password.
func TestLoginTemplate_ErrorAlert(t *testing.T) {
	html := renderLogin(t, "https://navidrome.example.com", "Invalid username or password")

	assert.Contains(t, html, "alert-error", "error alert must be rendered when errorMsg is set")
	assert.Contains(t, html, "Invalid username or password", "error message text must be visible")
}

// TestLoginTemplate_NoErrorAlertByDefault verifies the error alert is absent
// when no error message is provided (initial GET of the login page).
func TestLoginTemplate_NoErrorAlertByDefault(t *testing.T) {
	html := renderLogin(t, "https://navidrome.example.com", "")

	assert.NotContains(t, html, "alert-error", "no error alert on a clean login page")
}

// TestLoginTemplate_ErrorMessageIsEscaped verifies templ escapes the error
// message so a hostile upstream error string cannot inject markup.
func TestLoginTemplate_ErrorMessageIsEscaped(t *testing.T) {
	html := renderLogin(t, "https://navidrome.example.com", `<script>alert(1)</script>`)

	assert.NotContains(t, html, "<script>alert(1)</script>", "error message must be HTML-escaped")
	assert.Contains(t, html, "&lt;script&gt;", "escaped form of the payload should be present")
}

// TestLoginTemplate_FormStructure pins the login form contract that the
// HTMX failure flow depends on:
//   - hx-post="/login" with hx-target="body" (the 200-with-error re-render
//     from PostLogin is swapped into the body),
//   - action="/login" method="POST" as the non-JS fallback,
//   - username/password inputs named as PostLogin expects.
func TestLoginTemplate_FormStructure(t *testing.T) {
	html := renderLogin(t, "https://navidrome.example.com", "")

	assert.Contains(t, html, `hx-post="/login"`, "form must submit via HTMX to /login")
	assert.Contains(t, html, `hx-target="body"`, "HTMX swap target must be body so the re-rendered page replaces the current one")
	assert.Contains(t, html, `action="/login"`, "non-JS fallback action must be /login")
	assert.Contains(t, html, `method="POST"`, "non-JS fallback must POST")
	assert.Contains(t, html, `name="username"`, "username input must be present")
	assert.Contains(t, html, `name="password"`, "password input must be present")
	assert.Contains(t, html, `type="password"`, "password input must not echo its value")
	assert.Contains(t, html, "https://navidrome.example.com", "connected Navidrome URL must be shown")
}
