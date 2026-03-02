package services_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"spotter/internal/config"
	"spotter/internal/events"
	"spotter/internal/llm"
	"spotter/internal/services"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFindSimilarArtists_LLMTimeout_ReturnsError(t *testing.T) {
	client := setupSimilarArtistsTestDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := events.NewBus()

	// Create a mock server that simulates a timeout by taking longer than the context deadline
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := &config.Config{}
	cfg.OpenAI.APIKey = "test-key"
	cfg.OpenAI.BaseURL = srv.URL + "/v1"

	svc := services.NewSimilarArtistsService(client, cfg, logger, bus)

	user := similarArtistsCreateTestUser(t, client, "testuser")
	artist := similarArtistsCreateTestArtist(t, client, user, "Test Artist", []string{"Rock"})
	_ = similarArtistsCreateTestArtist(t, client, user, "Another Artist", []string{"Rock"})

	// Use a context with a very short deadline to force a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := svc.FindSimilarArtists(ctx, user.ID, artist.ID)
	assert.Error(t, err, "FindSimilarArtists should return error on timeout")
	assert.Contains(t, err.Error(), "failed to call OpenAI")
}

func TestFindSimilarArtists_LLMUnparseable_ReturnsParseError(t *testing.T) {
	client := setupSimilarArtistsTestDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := events.NewBus()

	// Create a mock server that returns a valid API response with unparseable content
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := llm.ChatResponse{
			ID:      "chatcmpl-test",
			Object:  "chat.completion",
			Created: time.Now().Unix(),
			Model:   "gpt-4o",
			Choices: []llm.ChatChoice{
				{
					Index: 0,
					Message: struct {
						Role    string `json:"role"`
						Content string `json:"content"`
					}{
						Role:    "assistant",
						Content: "this is not valid json at all {{{",
					},
					FinishReason: "stop",
				},
			},
			Usage: &llm.ChatUsage{
				PromptTokens:     100,
				CompletionTokens: 50,
				TotalTokens:      150,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	cfg := &config.Config{}
	cfg.OpenAI.APIKey = "test-key"
	cfg.OpenAI.BaseURL = srv.URL + "/v1"

	svc := services.NewSimilarArtistsService(client, cfg, logger, bus)

	user := similarArtistsCreateTestUser(t, client, "testuser2")
	artist := similarArtistsCreateTestArtist(t, client, user, "Test Artist", []string{"Rock"})
	_ = similarArtistsCreateTestArtist(t, client, user, "Another Artist", []string{"Rock"})

	err := svc.FindSimilarArtists(context.Background(), user.ID, artist.ID)
	require.Error(t, err, "FindSimilarArtists should return error on unparseable response")
	assert.Contains(t, err.Error(), "failed to parse AI response")
}
