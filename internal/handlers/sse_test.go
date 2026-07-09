// End-to-end tests for the /events SSE handler (issue #15, companion to issue
// #13 / PR #28): publish mixtape lifecycle events through the real events.Bus
// to a connected client and assert the SSE wire framing, response headers, and
// the write-deadline clearing that keeps the stream alive past
// http.Server.WriteTimeout. #28 verified this live; these tests automate it.
//
// Governing: SPEC event-bus-sse REQ-SSE-001 (auth-gated, required headers),
// REQ-SSE-002 (HTML fragment payloads), REQ-SSE-003 (Flusher check),
// REQ-SSE-004 (long-lived stream), REQ-SSE-005 (named event field)
package handlers_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"spotter/ent"
	"spotter/internal/config"
	"spotter/internal/events"
	"spotter/internal/handlers"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sseRecorder is a concurrency-safe ResponseWriter that implements
// http.Flusher and SetWriteDeadline, so http.NewResponseController in the
// handler can clear the write deadline exactly as it does on a real
// *http.response. httptest.ResponseRecorder supports neither concurrent reads
// nor write deadlines.
type sseRecorder struct {
	mu             sync.Mutex
	header         http.Header
	body           strings.Builder
	status         int
	flushes        int
	writeDeadlines []time.Time
}

func newSSERecorder() *sseRecorder {
	return &sseRecorder{header: http.Header{}, status: http.StatusOK}
}

func (r *sseRecorder) Header() http.Header {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.header
}

func (r *sseRecorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.body.Write(p)
}

func (r *sseRecorder) WriteHeader(code int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.status = code
}

func (r *sseRecorder) Flush() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.flushes++
}

// SetWriteDeadline records each deadline the handler sets; the ResponseController
// in Events discovers it through this interface.
func (r *sseRecorder) SetWriteDeadline(t time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.writeDeadlines = append(r.writeDeadlines, t)
	return nil
}

func (r *sseRecorder) Body() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.body.String()
}

func (r *sseRecorder) Deadlines() []time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]time.Time, len(r.writeDeadlines))
	copy(out, r.writeDeadlines)
	return out
}

func newSSEHandler() *handlers.Handler {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := events.NewBus()
	return handlers.New(nil, &config.Config{}, logger, nil, nil, nil, nil, nil, nil, nil, nil, bus, nil)
}

// parseSSEBlocks splits a raw SSE stream into event blocks, requiring every
// block to be well-formed: an "event: <name>" line followed only by
// "data: ..." lines. It returns eventName -> concatenated data payload for the
// first block of each event type.
func parseSSEBlocks(t *testing.T, raw string) map[string]string {
	t.Helper()
	blocks := map[string]string{}
	for _, block := range strings.Split(raw, "\n\n") {
		if strings.TrimSpace(block) == "" {
			continue
		}
		lines := strings.Split(strings.TrimRight(block, "\n"), "\n")
		require.True(t, strings.HasPrefix(lines[0], "event: "),
			"every SSE block must start with an event field, got %q", lines[0])
		name := strings.TrimPrefix(lines[0], "event: ")
		require.Greater(t, len(lines), 1, "event %q must carry data lines", name)
		var data strings.Builder
		for _, line := range lines[1:] {
			require.True(t, strings.HasPrefix(line, "data: "),
				"non-data line %q inside SSE block for %q", line, name)
			data.WriteString(strings.TrimPrefix(line, "data: "))
		}
		if _, seen := blocks[name]; !seen {
			blocks[name] = data.String()
		}
	}
	return blocks
}

func TestEvents_RequiresAuthentication(t *testing.T) {
	h := newSSEHandler()
	req := httptest.NewRequest(http.MethodGet, "/events", nil)
	w := httptest.NewRecorder()

	h.Events(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code, "unauthenticated /events must 401")
}

// nonFlushingWriter deliberately lacks http.Flusher.
type nonFlushingWriter struct {
	header http.Header
	status int
}

func (w *nonFlushingWriter) Header() http.Header         { return w.header }
func (w *nonFlushingWriter) Write(p []byte) (int, error) { return len(p), nil }
func (w *nonFlushingWriter) WriteHeader(code int)        { w.status = code }

func TestEvents_StreamingUnsupportedReturns500(t *testing.T) {
	h := newSSEHandler()
	req := httptest.NewRequest(http.MethodGet, "/events", nil)
	req = req.WithContext(context.WithValue(req.Context(), handlers.UserContextKey, &ent.User{ID: 1}))
	w := &nonFlushingWriter{header: http.Header{}}

	h.Events(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.status,
		"a writer without http.Flusher must be rejected with 500")
}

// TestEvents_StreamsMixtapeLifecycleFraming publishes a mixtape-generated and
// a mixtape-error event through the real Bus to a connected /events client and
// asserts the exact SSE framing HTMX consumes: a named event field plus toast
// HTML split across data: lines.
func TestEvents_StreamsMixtapeLifecycleFraming(t *testing.T) {
	h := newSSEHandler()
	u := &ent.User{ID: 42}

	ctx, cancelReq := context.WithCancel(context.Background())
	defer cancelReq()
	req := httptest.NewRequest(http.MethodGet, "/events", nil)
	req = req.WithContext(context.WithValue(ctx, handlers.UserContextKey, u))

	rec := newSSERecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.Events(rec, req)
	}()

	// The bus drops events published before Subscribe runs, so retry the
	// success event until it shows up on the wire.
	require.Eventually(t, func() bool {
		h.Bus.PublishMixtapeGenerated(u.ID, 7, "Road Trip", "DJ Test", 12, 10, 345)
		return strings.Contains(rec.Body(), "event: mixtape-generated")
	}, 5*time.Second, 10*time.Millisecond, "mixtape-generated never reached the SSE client")

	// Subscription is live now; a single error publish must arrive.
	h.Bus.PublishMixtapeError(u.ID, 7, "Road Trip", "the DJ went home")
	require.Eventually(t, func() bool {
		body := rec.Body()
		return strings.Contains(body, "event: mixtape-error") && strings.HasSuffix(body, "\n\n")
	}, 5*time.Second, 10*time.Millisecond, "mixtape-error never reached the SSE client")

	// Disconnect the client and wait for the handler to unwind.
	cancelReq()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Events did not return after client disconnect (REQ-SSE-004)")
	}

	// REQ-SSE-001: required stream headers.
	assert.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"))
	assert.Equal(t, "no-cache", rec.Header().Get("Cache-Control"))
	assert.Equal(t, "keep-alive", rec.Header().Get("Connection"))
	assert.GreaterOrEqual(t, rec.flushes, 3, "initial flush plus one per delivered event")

	// Issue #13 / REQ-SSE-004: the handler must clear (not merely shorten) the
	// per-connection write deadline so http.Server.WriteTimeout (60s in
	// production) cannot kill the stream.
	deadlines := rec.Deadlines()
	require.Len(t, deadlines, 1, "exactly one SetWriteDeadline call expected")
	assert.True(t, deadlines[0].IsZero(),
		"write deadline must be cleared with the zero time, got %v", deadlines[0])

	blocks := parseSSEBlocks(t, rec.Body())

	generated, ok := blocks["mixtape-generated"]
	require.True(t, ok, "stream must contain a mixtape-generated block")
	assert.Contains(t, generated, "alert-success", "generated toast must use success styling")
	assert.Contains(t, generated, "Mixtape Generated")
	assert.Contains(t, generated, "ready with 12 tracks (10 matched)")

	errBlock, ok := blocks["mixtape-error"]
	require.True(t, ok, "stream must contain a mixtape-error block")
	assert.Contains(t, errBlock, "alert-error", "error toast must use error styling")
	assert.Contains(t, errBlock, "Generation Failed")
	assert.Contains(t, errBlock, "the DJ went home")
}
