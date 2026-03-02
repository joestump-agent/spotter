package middleware

import "net/http"

// Governing: ADR-0028 (CSRF defense-in-depth: X-Frame-Options, CSP prevent clickjacking-based CSRF),
// SPEC-0014 REQ "HTTP Server Timeouts", SPEC user-authentication REQ "Security Headers"
// SecurityHeaders sets standard security headers on every response to mitigate
// clickjacking, MIME-sniffing, XSS, and other common web vulnerabilities.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-XSS-Protection", "1; mode=block")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline' unpkg.com cdn.jsdelivr.net; style-src 'self' 'unsafe-inline' cdn.jsdelivr.net; img-src 'self' data: https:; connect-src 'self'")
		next.ServeHTTP(w, r)
	})
}
