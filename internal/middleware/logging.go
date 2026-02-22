package middleware

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"
)

// Governing: ADR-0019 (structured metrics), ADR-0010 (slog), SPEC observability REQ "REQ-001", REQ "REQ-002"
func Logger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)

			t1 := time.Now()
			defer func() {
				logger.Info("metric.request",
					slog.String("method", r.Method),
					slog.String("path", r.URL.Path),
					slog.Int("status", ww.Status()),
					slog.String("remote_ip", r.RemoteAddr),
					slog.Duration("latency", time.Since(t1)),
					slog.Int64("duration_ms", time.Since(t1).Milliseconds()),
					slog.String("request_id", middleware.GetReqID(r.Context())),
				)
			}()

			next.ServeHTTP(ww, r)
		})
	}
}
