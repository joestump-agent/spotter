// Governing: ADR-0030, SPEC music-provider-integration REQ "ListenBrainz Provider" (REQ-PROV-053)
package listenbrainz

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"spotter/internal/config"
	"spotter/internal/providers"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// radioBodyJSON wraps a JSPF playlist body in the lb-radio response envelope
// (payload.jspf.playlist + payload.feedback).
func radioBodyJSON(playlistJSON string, feedback ...string) string {
	fb := ""
	for i, f := range feedback {
		if i > 0 {
			fb += ","
		}
		fb += fmt.Sprintf("%q", f)
	}
	return fmt.Sprintf(`{"payload": {"jspf": {"playlist": %s}, "feedback": [%s]}}`, playlistJSON, fb)
}

// Governing: SPEC music-provider-integration REQ-PROV-053 (lb-radio endpoint,
// prompt/mode query params, token sent when available, JSPF parsed with the
// REQ-PROV-049 rules — recording MBIDs ride Track.ID)
func TestRadioPlaylist_HappyPath(t *testing.T) {
	const prompt = "artist:(nina simone):2 tag:(soul):1"

	var gotPrompt, gotMode, gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/1/explore/lb-radio", r.URL.Path)
		// r.URL.Query() decodes — capturing here asserts the prompt survived
		// URL-encoding round-trip (spaces, parens, colons).
		gotPrompt = r.URL.Query().Get("prompt")
		gotMode = r.URL.Query().Get("mode")
		gotAuth = r.Header.Get("Authorization")
		fmt.Fprint(w, radioBodyJSON(fmt.Sprintf(`{
			"title": "LB Radio playlist (server title, overridden locally)",
			"annotation": "<p>server annotation</p>",
			"track": [
				{
					"title": "Feeling Good",
					"creator": "Nina Simone",
					"album": "I Put a Spell on You",
					"duration": 174000,
					"identifier": ["https://musicbrainz.org/recording/%s"]
				},
				{
					"title": "No MBID Track",
					"creator": "Some Artist"
				}
			]
		}`, recordingMBID1), "using artist nina simone"))
	}))
	defer server.Close()

	p := createTestProvider(t, &config.Config{}, testUser(), server.URL)
	pl, err := p.RadioPlaylist(context.Background(), prompt, RadioModeEasy)
	require.NoError(t, err)

	assert.Equal(t, prompt, gotPrompt, "prompt must be URL-encoded and decode back verbatim")
	assert.Equal(t, "easy", gotMode)
	assert.Equal(t, "Token user-token-123", gotAuth, "token sent when available (higher rate limit)")

	// Deterministic local identity (regenerate-in-place key), not the
	// server-assigned title/identifier.
	assert.Equal(t, providers.ListenBrainzRadioRemoteIDPrefix+prompt, pl.ID)
	assert.Equal(t, "LB Radio: "+prompt, pl.Name)
	assert.Contains(t, pl.Description, "mode: easy")
	assert.Contains(t, pl.Description, prompt)
	assert.Empty(t, pl.ExternalURL, "generated on the fly; no page to deep-link")

	require.Len(t, pl.Tracks, 2)
	assert.Equal(t, recordingMBID1, pl.Tracks[0].ID, "recording MBID rides the provider track ID slot")
	assert.Equal(t, "Feeling Good", pl.Tracks[0].Name)
	assert.Equal(t, "Nina Simone", pl.Tracks[0].Artist)
	assert.Equal(t, "I Put a Spell on You", pl.Tracks[0].Album)
	assert.Equal(t, 174000, pl.Tracks[0].DurationMs)
	assert.Empty(t, pl.Tracks[1].ID, "tracks without a recording identifier are still delivered")
	assert.Equal(t, 2, pl.TrackCount)
	assert.Equal(t, 2, pl.UniqueArtists)
}

// Governing: SPEC music-provider-integration REQ-PROV-053 (empty prompt and
// invalid mode rejected before any request; empty mode defaults to easy)
func TestRadioPlaylist_InputValidation(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		assert.Equal(t, "easy", r.URL.Query().Get("mode"), "empty mode defaults to easy")
		fmt.Fprint(w, radioBodyJSON(`{"title": "x", "track": []}`))
	}))
	defer server.Close()

	p := createTestProvider(t, &config.Config{}, testUser(), server.URL)

	_, err := p.RadioPlaylist(context.Background(), "", RadioModeEasy)
	assert.ErrorContains(t, err, "prompt is required")

	_, err = p.RadioPlaylist(context.Background(), "   ", RadioModeEasy)
	assert.ErrorContains(t, err, "prompt is required")

	_, err = p.RadioPlaylist(context.Background(), "tag:(jazz)", "extreme")
	assert.ErrorContains(t, err, "invalid lb-radio mode")

	assert.Equal(t, 0, requests, "validation failures must not hit the API")

	// Empty mode defaults to easy (asserted inside the handler above).
	_, err = p.RadioPlaylist(context.Background(), "tag:(jazz)", "")
	require.NoError(t, err)
	assert.Equal(t, 1, requests)
}

// Governing: SPEC music-provider-integration REQ-PROV-053 (zero usable tracks
// is not a provider-layer error — the caller decides how to surface it)
func TestRadioPlaylist_EmptyResult(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, radioBodyJSON(`{"title": "empty", "track": []}`, "no recordings found for tag obscure"))
	}))
	defer server.Close()

	p := createTestProvider(t, &config.Config{}, testUser(), server.URL)
	pl, err := p.RadioPlaylist(context.Background(), "tag:(obscure)", RadioModeHard)
	require.NoError(t, err)
	assert.Empty(t, pl.Tracks)
	assert.Equal(t, 0, pl.TrackCount)
	assert.Equal(t, providers.ListenBrainzRadioRemoteIDPrefix+"tag:(obscure)", pl.ID)
}

// Governing: SPEC error-handling REQ-ERR-003 (unparseable body is fatal and
// marked with providers.ErrMalformedResponse)
func TestRadioPlaylist_MalformedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"payload": {"jspf": "not-a-playlist-object"`)
	}))
	defer server.Close()

	p := createTestProvider(t, &config.Config{}, testUser(), server.URL)
	_, err := p.RadioPlaylist(context.Background(), "tag:(jazz)", RadioModeEasy)
	require.Error(t, err)
	assert.ErrorIs(t, err, providers.ErrMalformedResponse)
}

// Governing: SPEC music-provider-integration REQ-PROV-047, REQ-PROV-053 —
// lb-radio requests inherit the strict 429 semantics: retry only after the
// advertised Retry-After, and abort (rather than retry early) when the
// advertised interval exceeds the cap.
func TestRadioPlaylist_RateLimit429(t *testing.T) {
	t.Run("retries after advertised interval and succeeds", func(t *testing.T) {
		attempts := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			attempts++
			if attempts == 1 {
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(http.StatusTooManyRequests)
				return
			}
			fmt.Fprint(w, radioBodyJSON(`{"title": "x", "track": [{"title": "T", "creator": "A"}]}`))
		}))
		defer server.Close()

		p := createTestProvider(t, &config.Config{}, testUser(), server.URL)
		pl, err := p.RadioPlaylist(context.Background(), "tag:(jazz)", RadioModeEasy)
		require.NoError(t, err)
		assert.Equal(t, 2, attempts)
		assert.Len(t, pl.Tracks, 1)
	})

	t.Run("aborts when advertised interval exceeds the cap", func(t *testing.T) {
		attempts := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			attempts++
			w.Header().Set("Retry-After", "3600")
			w.WriteHeader(http.StatusTooManyRequests)
		}))
		defer server.Close()

		p := createTestProvider(t, &config.Config{}, testUser(), server.URL)
		_, err := p.RadioPlaylist(context.Background(), "tag:(jazz)", RadioModeEasy)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "rate limited")
		assert.Equal(t, 1, attempts, "must not retry earlier than advertised")
	})
}
