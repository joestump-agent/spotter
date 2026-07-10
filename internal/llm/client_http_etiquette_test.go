// Governing: ADR-0020 (error handling and resilience), SPEC error-handling REQ-ERR-002 (429 retriable),
// AGENTS.md "External API Etiquette" (User-Agent, 429 handling)
// Tests for outbound HTTP etiquette in the LLM client: the shared User-Agent
// header and Retry-After-driven 429 retries.
package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"spotter/internal/httputil"
)

func chatOKBody(t *testing.T, w http.ResponseWriter) {
	t.Helper()
	resp := ChatResponse{
		ID: "chatcmpl-1",
		Choices: []ChatChoice{
			{FinishReason: "stop"},
		},
	}
	resp.Choices[0].Message.Role = "assistant"
	resp.Choices[0].Message.Content = "hello"
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		t.Errorf("encode response: %v", err)
	}
}

func TestChat_SetsUserAgent(t *testing.T) {
	var gotUserAgent string
	srv, client := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		gotUserAgent = r.Header.Get("User-Agent")
		chatOKBody(t, w)
	})
	defer srv.Close()

	_, err := client.Chat(context.Background(), ChatRequest{Model: "gpt-4"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotUserAgent != httputil.UserAgent {
		t.Errorf("User-Agent = %q, want %q", gotUserAgent, httputil.UserAgent)
	}
}

func TestChat_429RetryAfter(t *testing.T) {
	tests := []struct {
		name          string
		failures      int // number of leading 429 responses
		wantErr       bool
		wantErrSubstr string
		wantAttempts  int
	}{
		{
			name:         "recovers after one 429",
			failures:     1,
			wantErr:      false,
			wantAttempts: 2,
		},
		{
			name:          "gives up after exhausting retries",
			failures:      10,
			wantErr:       true,
			wantErrSubstr: "429",
			wantAttempts:  httputil.MaxRateLimitRetries + 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var mu sync.Mutex
			attempts := 0
			var bodies []string

			srv, client := newTestServer(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Errorf("read request body: %v", err)
				}

				mu.Lock()
				attempts++
				n := attempts
				bodies = append(bodies, string(body))
				mu.Unlock()

				if n <= tt.failures {
					w.Header().Set("Retry-After", "1")
					w.WriteHeader(http.StatusTooManyRequests)
					return
				}
				chatOKBody(t, w)
			})
			defer srv.Close()

			_, err := client.Chat(context.Background(), ChatRequest{Model: "gpt-4"})

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if !strings.Contains(err.Error(), tt.wantErrSubstr) {
					t.Errorf("error %q does not contain %q", err.Error(), tt.wantErrSubstr)
				}
			} else if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			mu.Lock()
			defer mu.Unlock()
			if attempts != tt.wantAttempts {
				t.Errorf("attempts = %d, want %d", attempts, tt.wantAttempts)
			}
			// Each retry must carry the complete request payload.
			for i, b := range bodies {
				if b == "" || b != bodies[0] {
					t.Errorf("attempt %d body = %q, want same non-empty body as first attempt", i+1, b)
				}
			}
		})
	}
}

func TestChat_429ContextCancelled(t *testing.T) {
	srv, client := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := client.Chat(ctx, ChatRequest{Model: "gpt-4"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("Chat did not honor context cancellation promptly, took %v", elapsed)
	}
}
