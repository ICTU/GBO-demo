// Package logctx correlates log lines with traces. It is shared by main (the
// access log) and portalhttp (handler error logs).
package logctx

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"go.opentelemetry.io/otel/trace"
)

// From returns the default slog logger enriched with trace_id and span_id
// from the active OTel span. With these fields each logline can be correlated
// with the corresponding trace in Jaeger.
func From(ctx context.Context) *slog.Logger {
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		return slog.Default()
	}
	return slog.Default().With(
		slog.String("trace_id", sc.TraceID().String()),
		slog.String("span_id", sc.SpanID().String()),
	)
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(s int) {
	w.status = s
	w.ResponseWriter.WriteHeader(s)
}

// Unwrap exposes the underlying ResponseWriter so http.ResponseController can
// reach the Flusher an SSE stream needs.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// WithAccessLog logs one line per request, carrying the trace fields.
func WithAccessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		From(r.Context()).Info("http",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", sw.status),
			slog.Int64("duration_ms", time.Since(start).Milliseconds()),
		)
	})
}
