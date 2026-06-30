package middleware

import (
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
)

// RecoveryMiddleware catches any panic in downstream handlers
// and converts it into a 500 response. The panic, the request
// context, and the full goroutine stack are written to the
// structured log stream (slog → file rotation in
// internal/logging) so operators can correlate the crash with
// the request that triggered it.
//
// Why "log even on recovered panic"
// ─────────────────────────────────
// Without the structured log, a recovered panic is invisible
// to log queries because the only signal is the 500 status
// (which is the same as any other handler error). Capturing
// the full stack + request_id lets ops pivot from "5xx
// between 02:00–02:05" directly to the panic message and
// goroutine stack.
type RecoveryMiddleware struct {
	BaseMiddleware
}

func NewRecoveryMiddleware() *RecoveryMiddleware {
	return &RecoveryMiddleware{
		BaseMiddleware: BaseMiddleware{name: "recovery"},
	}
}

func (m *RecoveryMiddleware) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			slog.ErrorContext(r.Context(), "panic_recovered",
				"error.kind", "panic",
				"error.message", panicString(rec),
				"stack", string(debug.Stack()),
				"request_id", r.Header.Get("X-Request-Id"),
				"client_request_id", r.Header.Get("X-Gw-Client-Request-Id"),
				"method", r.Method,
				"path", r.URL.Path,
				"query", compactQuery(r.URL.RawQuery),
				"remote", r.RemoteAddr,
				"user_agent", r.UserAgent(),
				"content_type", r.Header.Get("Content-Type"),
				"request_bytes", requestBytes(r),
			)
			writePanicResponse(w)
		}()
		next.ServeHTTP(w, r)
	})
}

// panicString coerces a recovered value to a printable string.
// `recover()` returns `any`; the underlying value is most often
// an error or a string, but a misbehaving library might panic
// with a struct or an int. fmt.Sprint handles all of these
// without panicking itself.
func panicString(v any) string {
	if err, ok := v.(error); ok {
		return err.Error()
	}
	switch s := v.(type) {
	case string:
		return s
	default:
		// Fall back to fmt's default formatter; never panic
		// from the panic handler.
		return fmt.Sprintf("%v", s)
	}
}

// writePanicResponse writes a stable 500 JSON body. We keep the
// shape OpenAI-compatible because the gateway is a chat API and
// client libraries expect {"error": {...}} on failures.
func writePanicResponse(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusInternalServerError)
	//nolint:errcheck // HTTP write error non-recoverable
	w.Write([]byte(`{"error":{"message":"internal server error","type":"server_error","code":"panic"}}`))
}
