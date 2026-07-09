// HTTP wire-format test for the artist provider-history chart partial (issue
// #15, companion to issue #13 / PR #28). #28's artists_weeks_test.go proved
// the weekly buckets align internally; this test pins the shape actually sent
// over the wire by GET /library/artist/{id}/chart — the HTMX-swapped partial
// whose inline script feeds Chart.js — so labels and data arrays can never
// drift apart on the wire.
//
// The chart payload is JSON embedded in a <script> (there is no server-side
// chart markup beyond the canvas), so per issue #15 the wire assertion is on
// that JSON rather than on rendered chart HTML.
package handlers_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"spotter/ent"
	"spotter/ent/enttest"
	"spotter/internal/config"
	"spotter/internal/handlers"
	"spotter/internal/views/components"

	"github.com/go-chi/chi/v5"
	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupChartWireHandler(t *testing.T) (*ent.Client, *handlers.Handler) {
	t.Helper()
	dbName := strings.NewReplacer("/", "_", " ", "_", "=", "_").Replace(t.Name())
	client := enttest.Open(t, "sqlite3", "file:"+dbName+"?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { client.Close() })

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := handlers.New(client, &config.Config{}, logger, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	return client, h
}

// unescapeTemplJSString reverses templ's JS-string-literal escaping
// (runtime.ScriptContentInsideStringLiteral) so the embedded chart JSON can be
// parsed the same way the browser's JSON.parse sees it.
func unescapeTemplJSString(s string) string {
	return strings.NewReplacer(
		"\\u0022", "\"",
		"\\u0026", "&",
		"\\u0027", "'",
		"\\u0060", "`",
		"\\u002b", "+",
		"\\u003c", "<",
		"\\u003e", ">",
		"\\/", "/",
		"\\\\", "\\",
	).Replace(s)
}

// extractChartJSON pulls the JSON.parse('...') payload out of the chart
// partial's inline script.
func extractChartJSON(t *testing.T, body string) string {
	t.Helper()
	const marker = "JSON.parse('"
	start := strings.Index(body, marker)
	require.GreaterOrEqual(t, start, 0, "chart partial must embed a JSON.parse payload")
	rest := body[start+len(marker):]
	end := strings.Index(rest, "')")
	require.Greater(t, end, 0, "unterminated JSON.parse string literal")
	return unescapeTemplJSString(rest[:end])
}

func TestArtistChart_WeeklyWirePayloadAligned(t *testing.T) {
	client, h := setupChartWireHandler(t)
	ctx := context.Background()

	u, err := client.User.Create().
		SetUsername("chart_wire_user").
		SetPaginationSize(25).
		Save(ctx)
	require.NoError(t, err)

	a, err := client.Artist.Create().
		SetName("Wire Chart Artist").
		SetUser(u).
		Save(ctx)
	require.NoError(t, err)

	// Listens spread across the 6-month window (weekly bucketing).
	now := time.Now()
	offsets := []int{-2, -9, -40, -95, -160}
	for i, days := range offsets {
		_, err := client.Listen.Create().
			SetUser(u).
			SetTrackName("Wire Track " + strconv.Itoa(i)).
			SetArtistName(a.Name).
			SetAlbumName("Wire Album").
			SetSource("spotify").
			SetPlayedAt(now.AddDate(0, 0, days)).
			Save(ctx)
		require.NoError(t, err)
	}

	req := httptest.NewRequest(http.MethodGet, "/library/artist/"+strconv.Itoa(a.ID)+"/chart?timeframe=6m", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", strconv.Itoa(a.ID))
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	req = req.WithContext(context.WithValue(req.Context(), handlers.UserContextKey, u))
	w := httptest.NewRecorder()

	h.ArtistChart(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()
	assert.Contains(t, body, "artist-"+strconv.Itoa(a.ID)+"-provider-history",
		"partial must render the canvas the inline script targets")

	raw := extractChartJSON(t, body)

	// Go's json.Unmarshal matches struct fields case-insensitively, so this
	// decode works regardless of the payload's key casing and stays green if
	// the key-casing mismatch between this JSON and the inline script's
	// chartData.labels/datasets reads (reported in issue #15's PR) is fixed.
	var data components.StackedChartData
	require.NoError(t, json.Unmarshal([]byte(raw), &data),
		"embedded chart payload must be valid JSON: %s", raw)

	// ~26 weekly buckets over 6 months; anchor and month lengths add slack.
	assert.GreaterOrEqual(t, len(data.Labels), 25, "6m timeframe must produce weekly labels")
	assert.LessOrEqual(t, len(data.Labels), 29)

	require.Len(t, data.Datasets, 1, "one provider was seeded, one dataset expected")
	ds := data.Datasets[0]
	assert.Equal(t, "Spotify", ds.Label)

	// The wire contract issue #13 was about: every label has a data slot and
	// every listen lands in a labelled bucket (no all-zero chart).
	require.Len(t, ds.Data, len(data.Labels),
		"data array must align 1:1 with the label axis on the wire")
	total := 0.0
	for _, v := range ds.Data {
		total += v
	}
	assert.Equal(t, float64(len(offsets)), total,
		"all seeded listens must appear in the wire payload (labels: %v, data: %v)",
		data.Labels, ds.Data)
}

// TestArtistChart_ForeignArtistRejected pins the ownership check on the chart
// partial endpoint: another user's artist ID must 404, not leak chart data.
func TestArtistChart_ForeignArtistRejected(t *testing.T) {
	client, h := setupChartWireHandler(t)
	ctx := context.Background()

	owner, err := client.User.Create().
		SetUsername("chart_owner").
		SetPaginationSize(25).
		Save(ctx)
	require.NoError(t, err)
	other, err := client.User.Create().
		SetUsername("chart_other").
		SetPaginationSize(25).
		Save(ctx)
	require.NoError(t, err)

	a, err := client.Artist.Create().
		SetName("Owned Artist").
		SetUser(owner).
		Save(ctx)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/library/artist/"+strconv.Itoa(a.ID)+"/chart", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", strconv.Itoa(a.ID))
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	req = req.WithContext(context.WithValue(req.Context(), handlers.UserContextKey, other))
	w := httptest.NewRecorder()

	h.ArtistChart(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}
