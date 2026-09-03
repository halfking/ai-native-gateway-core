package middleware

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

const requestTracerName = "github.com/kaixuan/llm-gateway-go/middleware"

// TracingMiddleware creates one low-cardinality span for every HTTP request.
// Downstream handlers can enrich it with tenant, provider, and usage fields.
type TracingMiddleware struct{ BaseMiddleware }

func NewTracingMiddleware() *TracingMiddleware {
	return &TracingMiddleware{BaseMiddleware: BaseMiddleware{name: "tracing"}}
}

func (m *TracingMiddleware) Wrap(next http.Handler) http.Handler {
	tracer := otel.Tracer(requestTracerName)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		ctx, span := tracer.Start(r.Context(), "http.server "+requestRouteLabel(r), trace.WithSpanKind(trace.SpanKindServer))
		defer span.End()
		r = r.WithContext(ctx)
		span.SetAttributes(
			attribute.String("http.request.method", r.Method),
			attribute.String("http.route", requestRouteLabel(r)),
			attribute.Bool("llm.stream", isStreamingRequest(r)),
		)
		if requestID := r.Header.Get("X-Request-ID"); requestID != "" {
			span.SetAttributes(attribute.String("request.id", requestID))
		}
		rw := &statusRecordingWriter{ResponseWriter: w}
		next.ServeHTTP(rw, r)
		if rw.status == 0 {
			rw.status = http.StatusOK
		}
		if rw.status >= http.StatusInternalServerError {
			span.SetStatus(codes.Error, http.StatusText(rw.status))
		} else if rw.status >= http.StatusBadRequest {
			span.SetAttributes(attribute.Bool("http.client_error", true))
		}
		span.SetAttributes(attribute.Int("http.response.status_code", rw.status), attribute.Int64("http.server.duration_ms", time.Since(started).Milliseconds()))
	})
}

type statusRecordingWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusRecordingWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}
func (w *statusRecordingWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(p)
}
func (w *statusRecordingWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *statusRecordingWriter) Flush() {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
func (w *statusRecordingWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	return h.Hijack()
}
func (w *statusRecordingWriter) Push(target string, opts *http.PushOptions) error {
	p, ok := w.ResponseWriter.(http.Pusher)
	if !ok {
		return http.ErrNotSupported
	}
	return p.Push(target, opts)
}
func (w *statusRecordingWriter) ReadFrom(r io.Reader) (int64, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if rf, ok := w.ResponseWriter.(io.ReaderFrom); ok {
		return rf.ReadFrom(r)
	}
	return io.Copy(struct{ io.Writer }{w}, r)
}

func requestRouteLabel(r *http.Request) string {
	if r == nil || r.URL == nil {
		return "unknown"
	}
	if r.Pattern != "" {
		return r.Pattern
	}
	path := r.URL.Path
	switch {
	case path == "":
		return "unknown"
	case path == "/healthz" || path == "/readyz" || path == "/metrics":
		return path
	case strings.HasPrefix(path, "/v1/"):
		return "/v1/*"
	case strings.HasPrefix(path, "/api/"):
		return "/api/*"
	case strings.HasPrefix(path, "/admin/"):
		return "/admin/*"
	default:
		return "other"
	}
}

func isStreamingRequest(r *http.Request) bool {
	if r == nil {
		return false
	}
	return strings.Contains(strings.ToLower(r.Header.Get("Accept")), "text/event-stream")
}
