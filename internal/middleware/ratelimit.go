package middleware

import (
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"

	"golang.org/x/time/rate"
)

// Governing: SPEC user-authentication, issue #155

// IPRateLimiter provides per-IP rate limiting.
type IPRateLimiter struct {
	mu       sync.Mutex
	limiters map[string]*rate.Limiter
	r        rate.Limit
	b        int
}

// NewIPRateLimiter creates a rate limiter that allows r events per second with burst b.
func NewIPRateLimiter(r rate.Limit, b int) *IPRateLimiter {
	return &IPRateLimiter{
		limiters: make(map[string]*rate.Limiter),
		r:        r,
		b:        b,
	}
}

// GetLimiter returns the rate limiter for the given IP, creating one if needed.
func (i *IPRateLimiter) GetLimiter(ip string) *rate.Limiter {
	i.mu.Lock()
	defer i.mu.Unlock()
	limiter, exists := i.limiters[ip]
	if !exists {
		limiter = rate.NewLimiter(i.r, i.b)
		i.limiters[ip] = limiter
	}
	return limiter
}

// RateLimit returns middleware that rate-limits requests by client IP.
func RateLimit(limiter *IPRateLimiter, logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := clientIP(r)
			if !limiter.GetLimiter(ip).Allow() {
				logger.Warn("rate limit exceeded", "ip", ip, "path", r.URL.Path)
				w.Header().Set("Retry-After", "60")
				http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// clientIP extracts the real client IP from the request, checking
// X-Forwarded-For and X-Real-IP headers before falling back to RemoteAddr.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// X-Forwarded-For can contain multiple IPs; use the first (client) one
		if idx := strings.IndexByte(xff, ','); idx != -1 {
			return strings.TrimSpace(xff[:idx])
		}
		return strings.TrimSpace(xff)
	}
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return strings.TrimSpace(xri)
	}
	// Strip port from RemoteAddr
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return ip
}
