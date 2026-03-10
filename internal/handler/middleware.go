package handler

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"
)

// StructuredLogger returns a chi-compatible middleware that emits one
// structured slog entry per request (JSON in production, text in dev).
func StructuredLogger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			start := time.Now()

			next.ServeHTTP(ww, r)

			logger.InfoContext(r.Context(), "request",
				"method",      r.Method,
				"path",        r.URL.Path,
				"status",      ww.Status(),
				"bytes",       ww.BytesWritten(),
				"duration_ms", time.Since(start).Milliseconds(),
				"request_id",  middleware.GetReqID(r.Context()),
				"remote",      r.RemoteAddr,
			)
		})
	}
}
