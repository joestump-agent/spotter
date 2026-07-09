package services

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"spotter/ent"
	"spotter/ent/enttest"
	"spotter/ent/lidarrqueue"
	"spotter/internal/config"

	_ "github.com/mattn/go-sqlite3"
)

func TestComputeBackoff(t *testing.T) {
	tests := []struct {
		name     string
		attempts int
		minDelay time.Duration
		maxDelay time.Duration
	}{
		{
			name:     "first attempt: ~1m base + up to 1m jitter",
			attempts: 1,
			minDelay: 1 * time.Minute,
			maxDelay: 2 * time.Minute,
		},
		{
			name:     "second attempt: ~2m base + up to 1m jitter",
			attempts: 2,
			minDelay: 2 * time.Minute,
			maxDelay: 3 * time.Minute,
		},
		{
			name:     "third attempt: ~4m base + up to 1m jitter",
			attempts: 3,
			minDelay: 4 * time.Minute,
			maxDelay: 5 * time.Minute,
		},
		{
			name:     "tenth attempt: capped at 1h",
			attempts: 10,
			minDelay: 59 * time.Minute, // Could be near cap
			maxDelay: 1 * time.Hour,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := time.Now()
			retryAt := ComputeBackoff(tt.attempts)
			after := time.Now()

			// Verify the retry time is in the future
			if retryAt.Before(before) {
				t.Error("retry_at should be in the future")
			}

			delay := retryAt.Sub(before)

			// For attempts >= 7, base delay exceeds maxBackoff, so cap applies
			expectedBase := baseBackoff * time.Duration(math.Pow(2, float64(tt.attempts-1)))
			if expectedBase > maxBackoff {
				// Capped: delay should be <= maxBackoff
				if delay > maxBackoff+time.Second {
					t.Errorf("delay %v exceeds max backoff %v", delay, maxBackoff)
				}
			} else {
				// Not capped: delay should be >= base
				if delay < tt.minDelay-time.Second {
					t.Errorf("delay %v is less than minimum %v", delay, tt.minDelay)
				}
				if delay > tt.maxDelay+time.Second {
					t.Errorf("delay %v exceeds maximum %v (including jitter tolerance)", delay, tt.maxDelay)
				}
			}

			_ = after // used for bounds
		})
	}
}

func TestComputeBackoff_MaxCap(t *testing.T) {
	// Very high attempts should still be capped at maxBackoff
	for i := 0; i < 100; i++ {
		retryAt := ComputeBackoff(20)
		delay := time.Until(retryAt)
		if delay > maxBackoff+time.Second {
			t.Errorf("attempt 20 delay %v exceeds max backoff %v", delay, maxBackoff)
		}
	}
}

func TestMaxAttemptsConstant(t *testing.T) {
	if maxAttempts != 10 {
		t.Errorf("maxAttempts should be 10, got %d", maxAttempts)
	}
}

// TestTick_StatusUpdateFailureDoesNotResubmit verifies the hot-loop guard:
// when the post-submit status UPDATE fails, the still-queued item must not be
// re-queried and resubmitted within the same tick.
// Governing: SPEC-0017 REQ "Background Submitter Goroutine"
func TestTick_StatusUpdateFailureDoesNotResubmit(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:lidarr_hotloop?mode=memory&cache=shared&_fk=1")
	defer client.Close()

	ctx := context.Background()

	u, err := client.User.Create().
		SetUsername("submitter_hotloop").
		SetPaginationSize(25).
		Save(ctx)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	const mbid = "11111111-2222-3333-4444-555555555555"
	artist, err := client.Artist.Create().
		SetName("Test Artist").
		SetMusicbrainzID(mbid).
		SetUser(u).
		Save(ctx)
	if err != nil {
		t.Fatalf("create artist: %v", err)
	}

	if _, err := client.LidarrQueue.Create().
		SetEntityType(lidarrqueue.EntityTypeArtist).
		SetEntityID(artist.ID).
		SetMusicbrainzID(mbid).
		SetStatus(lidarrqueue.StatusQueued).
		SetUser(u).
		Save(ctx); err != nil {
		t.Fatalf("create queue item: %v", err)
	}

	// Simulate a persistently failing status UPDATE on the queue row.
	client.LidarrQueue.Use(func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
			if m.Op().Is(ent.OpUpdateOne) || m.Op().Is(ent.OpUpdate) {
				return nil, fmt.Errorf("simulated update failure")
			}
			return next.Mutate(ctx, m)
		})
	})

	// Fake Lidarr API that accepts artist submissions and counts POSTs.
	var artistPosts int64
	mux := http.NewServeMux()
	writeJSON := func(w http.ResponseWriter, v interface{}) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(v); err != nil {
			t.Errorf("encode response: %v", err)
		}
	}
	mux.HandleFunc("/api/v1/queue", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]int{"totalRecords": 0})
	})
	mux.HandleFunc("/api/v1/artist/lookup", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, []map[string]interface{}{{"artistName": "Test Artist", "foreignArtistId": mbid}})
	})
	mux.HandleFunc("/api/v1/rootfolder", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, []map[string]string{{"path": "/music"}})
	})
	mux.HandleFunc("/api/v1/qualityprofile", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, []map[string]interface{}{{"id": 1, "name": "Any"}})
	})
	mux.HandleFunc("/api/v1/metadataprofile", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, []map[string]interface{}{{"id": 1, "name": "Standard"}})
	})
	mux.HandleFunc("/api/v1/artist", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			atomic.AddInt64(&artistPosts, 1)
			writeJSON(w, map[string]interface{}{"id": 42, "artistName": "Test Artist", "foreignArtistId": mbid})
			return
		}
		writeJSON(w, []map[string]interface{}{})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	cfg := &config.Config{}
	cfg.Lidarr.BaseURL = server.URL
	cfg.Lidarr.APIKey = "test-key"
	cfg.Lidarr.QueueMax = 50

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := NewLidarrSubmitter(client, cfg, logger)

	// Bound the tick: without the hot-loop guard it would resubmit the same
	// still-queued item until the context deadline.
	tickCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	s.tick(tickCtx)

	if got := atomic.LoadInt64(&artistPosts); got != 1 {
		t.Errorf("expected exactly 1 artist submission despite failing status update, got %d", got)
	}

	// The row is still queued (the update failed), so the next tick retries it.
	item, err := client.LidarrQueue.Query().Only(ctx)
	if err != nil {
		t.Fatalf("query queue item: %v", err)
	}
	if item.Status != lidarrqueue.StatusQueued {
		t.Errorf("expected item to remain queued after failed update, got %s", item.Status)
	}
}
