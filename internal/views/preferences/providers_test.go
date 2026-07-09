package preferences_test

import (
	"bytes"
	"context"
	"regexp"
	"testing"

	"spotter/ent"
	"spotter/internal/config"
	"spotter/internal/views/preferences"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNavidromeLogoutModal_LogoutIsPOSTForm verifies the Navidrome logout
// confirmation modal terminates the session via a POST form to /logout, never
// a GET link. Logout is POST-only server-side (see cmd/server/main.go), so a
// GET control here would 405.
//
// Governing: ADR-0028
func TestNavidromeLogoutModal_LogoutIsPOSTForm(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, preferences.NavidromeLogoutModal().Render(context.Background(), &buf))
	html := buf.String()

	assert.Contains(t, html, `<form method="POST" action="/logout">`,
		"modal logout must be a POST form")
	assert.NotRegexp(t, regexp.MustCompile(`<a[^>]+href="(/logout|/auth/logout)"`), html,
		"logout must never be a GET anchor")
	assert.Contains(t, html, "Log Out", "confirm button label must be present")
}

// TestProvidersPage_WiresLogoutModalWithPOSTForm renders the full Providers
// preferences page (with a connected Navidrome account) and asserts the
// logout modal is wired in with its POST form intact — i.e. the page a user
// actually loads contains no GET-based logout control anywhere.
//
// Governing: ADR-0028
func TestProvidersPage_WiresLogoutModalWithPOSTForm(t *testing.T) {
	user := &ent.User{Username: "testuser"}
	navidromeAuth := &ent.NavidromeAuth{}
	cfg := &config.Config{}

	var buf bytes.Buffer
	require.NoError(t, preferences.Providers(user, nil, nil, navidromeAuth, cfg).Render(context.Background(), &buf))
	html := buf.String()

	assert.Contains(t, html, `id="navidrome-logout-modal"`, "logout modal must be present on the page")
	// Two POST /logout forms are expected: the sidebar (dashboard layout) and
	// the Navidrome disconnect modal. Both must be forms, and no GET anchor to
	// a logout endpoint may exist anywhere on the page.
	assert.GreaterOrEqual(t,
		len(regexp.MustCompile(`<form method="POST" action="/logout">`).FindAllString(html, -1)), 2,
		"both the sidebar and the modal must log out via POST forms")
	assert.NotRegexp(t, regexp.MustCompile(`<a[^>]+href="(/logout|/auth/logout)"`), html,
		"logout must never be a GET anchor")
}
