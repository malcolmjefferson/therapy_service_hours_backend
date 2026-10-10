package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
)

type loggerKey struct{}

// LoggerFrom returns the request-scoped logger, which already carries request_id and function (GO-63).
// Outside a request it returns a logger that discards output, so callers never need a nil check.
func LoggerFrom(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(loggerKey{}).(*slog.Logger); ok {
		return l
	}
	return slog.New(slog.DiscardHandler)
}

// Clock returns the current time. It is injected so durations are deterministic in tests (GO-24).
type Clock func() time.Time

// accessLog attaches a request-scoped logger and writes one line per request.
// It logs the route pattern, never the raw path or query, which could hold search terms (GO-64).
func accessLog(base *slog.Logger, now Clock) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := now()
			logger := base.With(slog.String("request_id", RequestIDFrom(r.Context())))
			ctx := context.WithValue(r.Context(), loggerKey{}, logger)
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r.WithContext(ctx))

			route := "unmatched"
			if rctx := chi.RouteContext(ctx); rctx != nil && rctx.RoutePattern() != "" {
				route = rctx.RoutePattern()
			}
			logger.LogAttrs(ctx, slog.LevelInfo, "request",
				slog.String("method", r.Method),
				slog.String("route", route),
				slog.Int("status", rec.status),
				slog.Int64("duration_ms", now().Sub(start).Milliseconds()),
			)
		})
	}
}

// statusRecorder captures the status code written by the handler.
type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.wroteHeader {
		s.status = code
		s.wroteHeader = true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	s.wroteHeader = true
	return s.ResponseWriter.Write(b) //nolint:wrapcheck // Pass-through writer; wrapping adds nothing.
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }
