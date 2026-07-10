// Governing: AGENTS.md "External API Etiquette" (User-Agent)
// Test for the shared User-Agent header on Spotify enricher requests. The
// 429/Retry-After behavior (now delegated to internal/httputil) is covered by
// the pre-existing TestDoRequest_429* tests in spotify_test.go.
package spotify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"spotter/internal/httputil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDoRequest_SetsUserAgent(t *testing.T) {
	var gotUserAgent string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUserAgent = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id": "123", "name": "Test"}`))
	}))
	defer server.Close()

	enricher := createTestEnricher(t, server.URL)
	e := enricher.(*Enricher)

	_, err := e.doRequest(context.Background(), "artists/test")
	require.NoError(t, err)
	assert.Equal(t, httputil.UserAgent, gotUserAgent)
}
