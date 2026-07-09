// Governing: SPEC event-bus-sse REQ-SSE-012, issue #13 — the toast container
// must subscribe to every SSE event type that internal/handlers/sse.go renders
// as a Toast, or those toasts never appear in the DOM.
package components_test

import (
	"context"
	"strings"
	"testing"

	"spotter/internal/views/components"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestToastContainer_ListensForAllToastEvents(t *testing.T) {
	var sb strings.Builder
	require.NoError(t, components.ToastContainer().Render(context.Background(), &sb))
	html := sb.String()

	// Every event type that sse.go renders as Toast HTML.
	toastEvents := []string{
		"notification",
		"mixtape-created",
		"mixtape-updated",
		"mixtape-deleted",
		"mixtape-generating",
		"mixtape-generated",
		"mixtape-error",
		"playlist-enhancing",
		"playlist-enhanced",
		"playlist-enhance-error",
		"similar-artists-searching",
		"similar-artists-found",
		"similar-artists-error",
	}

	require.Contains(t, html, `sse-swap="`)
	start := strings.Index(html, `sse-swap="`) + len(`sse-swap="`)
	end := strings.Index(html[start:], `"`)
	require.Greater(t, end, 0)
	sseSwap := html[start : start+end]

	listened := make(map[string]bool)
	for _, name := range strings.Split(sseSwap, ",") {
		listened[strings.TrimSpace(name)] = true
	}

	for _, event := range toastEvents {
		assert.True(t, listened[event], "toast container must listen for %q", event)
	}

	assert.Contains(t, html, `hx-swap="beforeend"`)
	assert.Contains(t, html, `id="toasts"`)
}
