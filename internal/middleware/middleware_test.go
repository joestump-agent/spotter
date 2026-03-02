package middleware

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"golang.org/x/time/rate"
)

func TestSecurityHeaders_AllSet(t *testing.T) {
	handler := SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	expected := map[string]string{
		"X-Frame-Options":        "DENY",
		"X-Content-Type-Options": "nosniff",
		"X-XSS-Protection":       "1; mode=block",
		"Referrer-Policy":        "strict-origin-when-cross-origin",
	}

	for header, want := range expected {
		got := w.Header().Get(header)
		if got != want {
			t.Errorf("header %s: want %q, got %q", header, want, got)
		}
	}

	csp := w.Header().Get("Content-Security-Policy")
	if csp == "" {
		t.Error("Content-Security-Policy header is empty")
	}
}

func TestSecurityHeaders_PassesThrough(t *testing.T) {
	called := false
	handler := SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if !called {
		t.Error("next handler was not called")
	}
}

func TestRateLimit_AllowsUnderLimit(t *testing.T) {
	limiter := NewIPRateLimiter(rate.Limit(10), 10)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	handler := RateLimit(limiter, logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/login", nil)
	req.RemoteAddr = "192.168.1.1:12345"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestRateLimit_Returns429WhenExceeded(t *testing.T) {
	// Allow only 1 request with burst of 1
	limiter := NewIPRateLimiter(rate.Limit(0.001), 1)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	handler := RateLimit(limiter, logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// First request should succeed
	req := httptest.NewRequest("GET", "/login", nil)
	req.RemoteAddr = "10.0.0.1:12345"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("first request: expected 200, got %d", w.Code)
	}

	// Second request should be rate limited
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("second request: expected 429, got %d", w.Code)
	}
	if w.Header().Get("Retry-After") != "60" {
		t.Errorf("expected Retry-After header '60', got %q", w.Header().Get("Retry-After"))
	}
}

func TestRateLimit_PerIPIsolation(t *testing.T) {
	// Allow only 1 request per IP
	limiter := NewIPRateLimiter(rate.Limit(0.001), 1)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	handler := RateLimit(limiter, logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Exhaust IP1's limit
	req1 := httptest.NewRequest("GET", "/login", nil)
	req1.RemoteAddr = "10.0.0.1:12345"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req1)

	// IP2 should still be allowed
	req2 := httptest.NewRequest("GET", "/login", nil)
	req2.RemoteAddr = "10.0.0.2:12345"
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req2)
	if w.Code != http.StatusOK {
		t.Errorf("different IP should not be rate limited, got %d", w.Code)
	}
}

func TestLogger_LogsRequestFields(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	handler := Logger(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}))

	req := httptest.NewRequest("POST", "/api/test", nil)
	req.RemoteAddr = "127.0.0.1:9999"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	logOutput := buf.String()

	checks := []string{"method=POST", "path=/api/test", "status=201", "duration_ms="}
	for _, check := range checks {
		if !containsStr(logOutput, check) {
			t.Errorf("log output missing %q\nlog: %s", check, logOutput)
		}
	}
}

func TestClientIP_XForwardedFor(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Forwarded-For", "1.2.3.4, 5.6.7.8")
	if ip := clientIP(req); ip != "1.2.3.4" {
		t.Errorf("expected 1.2.3.4, got %q", ip)
	}
}

func TestClientIP_XRealIP(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Real-IP", "9.8.7.6")
	if ip := clientIP(req); ip != "9.8.7.6" {
		t.Errorf("expected 9.8.7.6, got %q", ip)
	}
}

func TestClientIP_RemoteAddr(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "192.168.1.100:54321"
	if ip := clientIP(req); ip != "192.168.1.100" {
		t.Errorf("expected 192.168.1.100, got %q", ip)
	}
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
