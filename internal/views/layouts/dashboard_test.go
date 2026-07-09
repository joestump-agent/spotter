package layouts_test

import (
	"bytes"
	"context"
	"regexp"
	"testing"

	"spotter/internal/config"
	"spotter/internal/views/layouts"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDashboardTemplate_LogoutIsPOSTForm verifies the sidebar logout control
// is a POST form, not a GET link. Logout is state-changing and the session
// cookie is SameSite=Lax, which is still sent on top-level GET navigations —
// so a GET logout link would be CSRF-able.
//
// Governing: ADR-0028
func TestDashboardTemplate_LogoutIsPOSTForm(t *testing.T) {
	cfg := &config.Config{}
	var buf bytes.Buffer
	require.NoError(t, layouts.Dashboard("Test | Spotter", cfg, "/").Render(context.Background(), &buf))
	html := buf.String()

	assert.Contains(t, html, `<form method="POST" action="/logout">`,
		"sidebar logout must be a POST form")
	assert.NotRegexp(t, regexp.MustCompile(`<a[^>]+href="(/logout|/auth/logout)"`), html,
		"logout must never be a GET anchor")
	// The visible control is a submit button inside that form.
	assert.Contains(t, html, `type="submit"`, "logout form must be submitted via a button")
	assert.Contains(t, html, ">Logout<", "logout button label must be present")
}
