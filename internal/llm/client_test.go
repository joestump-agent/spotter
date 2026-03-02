package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func newTestServer(handler http.HandlerFunc) (*httptest.Server, *Client) {
	srv := httptest.NewServer(handler)
	client := NewClient(ClientConfig{
		APIKey:  "test-key",
		BaseURL: srv.URL,
		Timeout: 5 * time.Second,
	})
	return srv, client
}

func TestChat_ValidResponse(t *testing.T) {
	resp := ChatResponse{
		ID:    "chatcmpl-123",
		Model: "gpt-4",
		Choices: []ChatChoice{
			{
				Index:        0,
				FinishReason: "stop",
				Message: struct {
					Role    string `json:"role"`
					Content string `json:"content"`
				}{
					Role:    "assistant",
					Content: "Hello, world!",
				},
			},
		},
		Usage: &ChatUsage{
			PromptTokens:     10,
			CompletionTokens: 5,
			TotalTokens:      15,
		},
	}

	srv, client := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("expected Authorization header 'Bearer test-key', got %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("expected Content-Type 'application/json', got %q", r.Header.Get("Content-Type"))
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})
	defer srv.Close()

	got, err := client.Chat(context.Background(), ChatRequest{
		Model:    "gpt-4",
		Messages: []ChatMessage{{Role: "user", Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Choices[0].Message.Content != "Hello, world!" {
		t.Errorf("expected content 'Hello, world!', got %q", got.Choices[0].Message.Content)
	}
	if got.Usage.TotalTokens != 15 {
		t.Errorf("expected total_tokens 15, got %d", got.Usage.TotalTokens)
	}
}

func TestChat_APIError4xx(t *testing.T) {
	srv, client := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"message":"invalid api key","type":"invalid_request_error","code":"invalid_api_key"}}`))
	})
	defer srv.Close()

	_, err := client.Chat(context.Background(), ChatRequest{
		Model:    "gpt-4",
		Messages: []ChatMessage{{Role: "user", Content: "Hi"}},
	})
	if err == nil {
		t.Fatal("expected error for 401 response")
	}
	if got := err.Error(); !contains(got, "401") {
		t.Errorf("expected error to contain '401', got %q", got)
	}
}

func TestChat_APIError5xx(t *testing.T) {
	srv, client := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":{"message":"server error"}}`))
	})
	defer srv.Close()

	_, err := client.Chat(context.Background(), ChatRequest{
		Model:    "gpt-4",
		Messages: []ChatMessage{{Role: "user", Content: "Hi"}},
	})
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
	if got := err.Error(); !contains(got, "500") {
		t.Errorf("expected error to contain '500', got %q", got)
	}
}

func TestChat_MalformedJSON(t *testing.T) {
	srv, client := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{not valid json`))
	})
	defer srv.Close()

	_, err := client.Chat(context.Background(), ChatRequest{
		Model:    "gpt-4",
		Messages: []ChatMessage{{Role: "user", Content: "Hi"}},
	})
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
	if got := err.Error(); !contains(got, "decode response") {
		t.Errorf("expected error to contain 'decode response', got %q", got)
	}
}

func TestChat_EmptyChoices(t *testing.T) {
	resp := ChatResponse{
		ID:      "chatcmpl-123",
		Model:   "gpt-4",
		Choices: []ChatChoice{},
	}

	srv, client := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})
	defer srv.Close()

	_, err := client.Chat(context.Background(), ChatRequest{
		Model:    "gpt-4",
		Messages: []ChatMessage{{Role: "user", Content: "Hi"}},
	})
	if err == nil {
		t.Fatal("expected error for empty choices")
	}
	if got := err.Error(); !contains(got, "no choices") {
		t.Errorf("expected error to contain 'no choices', got %q", got)
	}
}

func TestChat_ContextCancelled(t *testing.T) {
	srv, client := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		// Block until context is cancelled
		<-r.Context().Done()
	})
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	_, err := client.Chat(ctx, ChatRequest{
		Model:    "gpt-4",
		Messages: []ChatMessage{{Role: "user", Content: "Hi"}},
	})
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
}

func TestChat_APIErrorInBody(t *testing.T) {
	resp := ChatResponse{
		Error: &ChatError{
			Message: "rate limit exceeded",
			Type:    "rate_limit_error",
			Code:    "rate_limit_exceeded",
		},
	}

	srv, client := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})
	defer srv.Close()

	_, err := client.Chat(context.Background(), ChatRequest{
		Model:    "gpt-4",
		Messages: []ChatMessage{{Role: "user", Content: "Hi"}},
	})
	if err == nil {
		t.Fatal("expected error for API error in body")
	}
	if got := err.Error(); !contains(got, "rate limit exceeded") {
		t.Errorf("expected error to contain 'rate limit exceeded', got %q", got)
	}
}

func TestNewClient_Defaults(t *testing.T) {
	c := NewClient(ClientConfig{APIKey: "key"})
	if c.cfg.BaseURL != "https://api.openai.com/v1" {
		t.Errorf("expected default base URL, got %q", c.cfg.BaseURL)
	}
	if c.cfg.Timeout != 60*time.Second {
		t.Errorf("expected 60s default timeout, got %v", c.cfg.Timeout)
	}
}

func TestNewClient_TrailingSlashTrimmed(t *testing.T) {
	c := NewClient(ClientConfig{APIKey: "key", BaseURL: "http://localhost:8080/v1/"})
	if c.cfg.BaseURL != "http://localhost:8080/v1" {
		t.Errorf("expected trailing slash trimmed, got %q", c.cfg.BaseURL)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchStr(s, substr)
}

func searchStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
