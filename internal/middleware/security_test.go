package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"spotter/internal/middleware"

	"github.com/stretchr/testify/assert"
)

// TestSecurityHeaders_CSRFDefenseInDepth verifies that SecurityHeaders sets
// headers that provide defense-in-depth against CSRF variants (clickjacking,
// frame-based attacks). Per ADR-0028, SameSite=Lax on the session cookie is
// the primary CSRF protection; these headers are supplementary.
func TestSecurityHeaders_CSRFDefenseInDepth(t *testing.T) {
	handler := middleware.SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	resp := w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// X-Frame-Options: DENY prevents the page from being embedded in an iframe,
	// blocking clickjacking-based CSRF attacks (ADR-0028 defense-in-depth).
	assert.Equal(t, "DENY", resp.Header.Get("X-Frame-Options"),
		"X-Frame-Options must be DENY to prevent clickjacking")

	// Content-Security-Policy restricts resource loading origins.
	csp := resp.Header.Get("Content-Security-Policy")
	assert.Contains(t, csp, "default-src 'self'",
		"CSP must restrict default-src to same origin")

	// X-Content-Type-Options prevents MIME-sniffing attacks.
	assert.Equal(t, "nosniff", resp.Header.Get("X-Content-Type-Options"),
		"X-Content-Type-Options must be nosniff")

	// Referrer-Policy limits information leakage on cross-origin navigations.
	assert.Equal(t, "strict-origin-when-cross-origin", resp.Header.Get("Referrer-Policy"),
		"Referrer-Policy must be strict-origin-when-cross-origin")
}
