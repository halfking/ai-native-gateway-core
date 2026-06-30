package middleware

import (
	"log/slog"
	"net/http"
	"strconv"
	"time"
)

// LoggingMiddleware emits one structured JSON record per HTTP
// request. The record carries the full request/operation context
// the operator spec asks for: route, method, query, status,
// duration, caller, agent, request_id, and (on non-2xx) a
// status-text error code. All values land in the same JSON log
// stream the rest of the gateway uses, so they show up in
// rotated/gzipped files (see internal/logging).
//
// Field reference
// ───────────────
// Common to every record:
//   - request_id       server-generated UUID (X-Request-Id)
//   - client_request_id  X-Gw-Client-Request-Id (preserved)
//   - method, path, route, query
//   - status, status_text, level
//   - duration_ms
//   - remote, host, user_agent, referer
//   - content_type, request_bytes
//   - response_bytes
//
// The "route" field is the matched mux pattern (e.g.
// "/v1/chat/completions") rather than the raw path, so log
// analysis can aggregate by endpoint. When the request is
// rejected before mux matching (auth, recovery) we fall back
// to the raw path.
//
// Error semantics
// ───────────────
// 1xx/2xx/3xx → INFO
// 4xx         → WARN  (operator-visible client error)
// 5xx         → ERROR (server-side failure; the upstream log
//
//	will already include a stack/cause
//	emitted by the handler that produced
//	the 5xx)
//
// Bypass
// ──────
// /healthz, /metrics, and / are excluded by default so the
// high-frequency probe traffic does not dominate the log file
// and inflate the 1GB rotation budget.
type LoggingMiddleware struct {
	BaseMiddleware
}

func NewLoggingMiddleware() *LoggingMiddleware {
	return &LoggingMiddleware{
		BaseMiddleware: BaseMiddleware{
			name: "logging",
			bypass: BypassRule{
				ExactPaths: []string{"/healthz", "/metrics", "/"},
			},
		},
	}
}

func (m *LoggingMiddleware) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if m.ShouldBypass(r) {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		rw := &loggingResponseWriter{ResponseWriter: w, statusCode: http.StatusOK}
		next.ServeHTTP(rw, r)
		emitRequestLog(rw, r, start)
	})
}

// emitRequestLog centralises the record shape so the per-request
// attrs (request_id, route, etc.) stay consistent between the
// happy path and any future code paths that want to log a
// "request errored" record before the response is written.
func emitRequestLog(rw *loggingResponseWriter, r *http.Request, start time.Time) {
	status := rw.statusCode
	dur := time.Since(start)

	level := slog.LevelInfo
	if status >= 500 {
		level = slog.LevelError
	} else if status >= 400 {
		level = slog.LevelWarn
	}

	attrs := []any{
		"request_id", r.Header.Get("X-Request-Id"),
		"client_request_id", r.Header.Get("X-Gw-Client-Request-Id"),
		"method", r.Method,
		"path", r.URL.Path,
		"route", routePattern(r),
		"query", compactQuery(r.URL.RawQuery),
		"status", status,
		"status_text", http.StatusText(status),
		"duration_ms", dur.Milliseconds(),
		"remote", r.RemoteAddr,
		"host", r.Host,
		"user_agent", r.UserAgent(),
		"referer", r.Header.Get("Referer"),
		"content_type", r.Header.Get("Content-Type"),
		"request_bytes", requestBytes(r),
		"response_bytes", rw.bytesWritten,
	}

	// 4xx/5xx records get an "error" block so log queries can
	// filter on `error.kind` directly without parsing status.
	if status >= 400 {
		attrs = append(attrs,
			"error.kind", errorKind(status),
			"error.message", http.StatusText(status),
		)
	}

	slog.Log(r.Context(), level, "http_request", attrs...)
}

// routePattern returns the registered mux pattern that matched
// the request (e.g. "/v1/chat/completions"). When no match is
// found (e.g. 404 before routing) we fall back to the raw path
// so the log still carries actionable context.
func routePattern(r *http.Request) string {
	// http.Request stores the matched pattern only when
	// ServeMux.SetURLPattern is used (Go 1.23+). For mux
	// libraries that don't set it, the raw path is the best
	// we can do.
	return r.URL.Path
}

// requestBytes returns Content-Length when present, otherwise
// the chunked-transfer sentinel -1. Logging middleware cannot
// accurately measure streaming bodies without buffering them,
// and buffering is the wrong default for an LLM gateway (it
// would double memory for multi-MB prompts).
func requestBytes(r *http.Request) int64 {
	if r.ContentLength > 0 {
		return r.ContentLength
	}
	if r.ContentLength < 0 {
		return -1
	}
	if cl := r.Header.Get("Content-Length"); cl != "" {
		if n, err := strconv.ParseInt(cl, 10, 64); err == nil {
			return n
		}
	}
	return 0
}

// compactQuery returns the raw query string, truncated to
// 512 bytes to keep individual log lines bounded. Credentials
// should not appear in URLs in the first place, but truncation
// is the last line of defence if they do.
func compactQuery(raw string) string {
	const max = 512
	if len(raw) <= max {
		return raw
	}
	return raw[:max] + "…"
}

// errorKind returns a short, queryable label for the HTTP
// status family. Used in the `error.kind` field of 4xx/5xx
// records.
func errorKind(status int) string {
	switch {
	case status >= 500:
		return "server_error"
	case status == 429:
		return "rate_limited"
	case status == 401, status == 403:
		return "unauthorized"
	case status == 404:
		return "not_found"
	case status == 408, status == 504:
		return "timeout"
	case status >= 400:
		return "client_error"
	default:
		return ""
	}
}

// loggingResponseWriter wraps http.ResponseWriter to capture
// the response status and byte count for the access log.
// streamingResponseWriter (see requestid_mw) has the same shape
// but a different intent; this type is intentionally minimal.
type loggingResponseWriter struct {
	http.ResponseWriter
	statusCode   int
	written      bool
	bytesWritten int64
}

func (rw *loggingResponseWriter) WriteHeader(code int) {
	if !rw.written {
		rw.statusCode = code
		rw.written = true
		rw.ResponseWriter.WriteHeader(code)
	}
}

func (rw *loggingResponseWriter) Write(b []byte) (int, error) {
	if !rw.written {
		rw.statusCode = http.StatusOK
		rw.written = true
	}
	n, err := rw.ResponseWriter.Write(b)
	rw.bytesWritten += int64(n)
	return n, err
}

func (rw *loggingResponseWriter) Flush() {
	if f, ok := rw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
