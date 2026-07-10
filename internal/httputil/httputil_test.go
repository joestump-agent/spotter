package httputil

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestRetryAfter(t *testing.T) {
	tests := []struct {
		name   string
		header string
		want   time.Duration
	}{
		{name: "missing header falls back to default", header: "", want: DefaultRetryAfter},
		{name: "valid seconds", header: "2", want: 2 * time.Second},
		{name: "capped at max", header: "3600", want: MaxRetryAfter},
		{name: "zero falls back to default", header: "0", want: DefaultRetryAfter},
		{name: "negative falls back to default", header: "-5", want: DefaultRetryAfter},
		{name: "unparseable falls back to default", header: "soon", want: DefaultRetryAfter},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := &http.Response{Header: http.Header{}}
			if tt.header != "" {
				resp.Header.Set("Retry-After", tt.header)
			}
			if got := RetryAfter(resp); got != tt.want {
				t.Errorf("RetryAfter() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSleep_CancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	err := Sleep(ctx, time.Minute)
	if err == nil {
		t.Fatal("expected context error, got nil")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Sleep did not return promptly on cancellation, took %v", elapsed)
	}
}

func TestSleep_Elapses(t *testing.T) {
	if err := Sleep(context.Background(), time.Millisecond); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
