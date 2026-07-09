// Per-variant render tests for the Toast component (issue #15, companion to
// issue #13). internal/handlers/sse.go renders every lifecycle event
// (mixtape-*, playlist-enhanc*, similar-artists-*) through Toast with an
// iconType of "success", "info", or "error", so the iconType -> DaisyUI
// alert-class mapping is the styling contract for all SSE toasts.
// #28's toast_test.go only covered the container's sse-swap subscription list;
// these tests pin the markup of each toast variant itself.
//
// Governing: SPEC event-bus-sse REQ-BUS-013 (IconType field), REQ-SSE-002
// (HTML fragment rendering)
package components_test

import (
	"context"
	"strings"
	"testing"

	"spotter/internal/views/components"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func renderToast(t *testing.T, title, message, iconType string) string {
	t.Helper()
	var sb strings.Builder
	require.NoError(t, components.Toast(title, message, iconType).Render(context.Background(), &sb))
	return sb.String()
}

// allAlertClasses are the mutually exclusive DaisyUI alert variants Toast can
// emit; exactly one must be present per rendered toast.
var allAlertClasses = []string{"alert-success", "alert-info", "alert-warning", "alert-error"}

func TestToast_VariantStyling(t *testing.T) {
	tests := []struct {
		name       string
		iconType   string
		alertClass string
		icon       string
	}{
		// "success" is what sse.go passes for mixtape-created, mixtape-updated,
		// mixtape-generated, playlist-enhanced, and similar-artists-found.
		{"success", "success", "alert-success", "icon-[heroicons--check-circle]"},
		// "play" is the legacy success alias used by notification payloads.
		{"play alias of success", "play", "alert-success", "icon-[heroicons--check-circle]"},
		// "info" is what sse.go passes for mixtape-deleted, mixtape-generating,
		// playlist-enhancing, and similar-artists-searching.
		{"info", "info", "alert-info", "icon-[heroicons--information-circle]"},
		{"playlist alias of info", "playlist", "alert-info", "icon-[heroicons--queue-list]"},
		{"empty defaults to info", "", "alert-info", "icon-[heroicons--information-circle]"},
		{"warning", "warning", "alert-warning", "icon-[heroicons--exclamation-triangle]"},
		// "error" is what sse.go passes for mixtape-error,
		// playlist-enhance-error, and similar-artists-error.
		{"error", "error", "alert-error", "icon-[heroicons--x-circle]"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			html := renderToast(t, "Toast Title", "Toast message body", tt.iconType)

			assert.Contains(t, html, tt.alertClass,
				"iconType %q must style the toast with %s", tt.iconType, tt.alertClass)
			assert.Contains(t, html, tt.icon,
				"iconType %q must render icon %s", tt.iconType, tt.icon)

			// The alert variants are mutually exclusive: any extra one would
			// let a later DaisyUI class override the intended styling.
			for _, other := range allAlertClasses {
				if other == tt.alertClass {
					continue
				}
				assert.NotContains(t, html, other,
					"iconType %q must not also carry %s", tt.iconType, other)
			}

			assert.Contains(t, html, `role="alert"`, "toast must be announced as an alert")
			assert.Contains(t, html, "Toast Title")
			assert.Contains(t, html, "Toast message body")
		})
	}
}

// TestToast_UnknownIconTypeHasNoVariantClass documents current behavior for an
// unrecognized iconType: the base alert renders (with the fallback info icon)
// but no alert-* variant class is applied.
func TestToast_UnknownIconTypeHasNoVariantClass(t *testing.T) {
	html := renderToast(t, "Odd", "Unknown icon type", "bogus")

	for _, cls := range allAlertClasses {
		assert.NotContains(t, html, cls, "unknown iconType must not pick up %s", cls)
	}
	assert.Contains(t, html, "icon-[heroicons--information-circle]",
		"unknown iconType falls back to the info icon")
	assert.Contains(t, html, `role="alert"`)
}

// TestToast_EscapesUserContent verifies that titles/messages built from user
// data (mixtape names, upstream error strings) cannot inject markup into the
// toast that the SSE stream swaps into the DOM.
func TestToast_EscapesUserContent(t *testing.T) {
	html := renderToast(t, `<b>title</b>`, `<script>alert(1)</script>`, "error")

	assert.NotContains(t, html, "<script>alert(1)</script>", "message must be HTML-escaped")
	assert.NotContains(t, html, "<b>title</b>", "title must be HTML-escaped")
	assert.Contains(t, html, "&lt;script&gt;", "escaped form of the message should be present")
}

// TestToast_SelfDismissWiring pins the client-side lifecycle plumbing: a
// unique element id shared by the toast div and its init script, which handles
// click-to-dismiss and auto-dismiss.
func TestToast_SelfDismissWiring(t *testing.T) {
	html := renderToast(t, "Wired", "Dismiss plumbing", "success")

	start := strings.Index(html, `id="toast-`)
	require.GreaterOrEqual(t, start, 0, "toast must carry a toast-<nanos> id")
	rest := html[start+len(`id="`):]
	end := strings.Index(rest, `"`)
	require.Greater(t, end, 0)
	id := rest[:end]

	assert.Equal(t, 2, strings.Count(html, id),
		"the toast id must appear exactly twice: on the element and in the init script call")
	assert.Contains(t, html, "<script", "init script must be emitted alongside the toast")
}
