package streaming

import (
	"context"
	"log/slog"
)

// streamingRequestContext carries the correlation IDs every survival/stream
// log line should thread into its slog attrs. The values live on the
// per-request RequestLogContext and (when the survival coordinator runs) on
// the coordinator's ExecParams, but the survival/stream paths run below
// those layers, so the helper exposes a flat struct that any decision point
// can fill in without dragging the full RequestLogContext through.
//
// 2026-08-19 observability pass — every request should be reconstructible
// from `request_id` alone across survival, attempt-outcome and discard
// log lines.
type streamingRequestContext struct {
	RequestID       string
	ParentRequestID string
	SessionID       string
	TenantID        string
	ClientModel     string
}

// logAttrs returns the correlation attrs that always travel with a
// survival/streaming log line. Returns nil when no correlation is available
// so call sites that operate outside a request (e.g. test helpers) can
// still emit logs without leaking an empty struct into the output.
func (rc streamingRequestContext) logAttrs() []any {
	if rc.RequestID == "" && rc.ParentRequestID == "" && rc.SessionID == "" && rc.TenantID == "" {
		return nil
	}
	attrs := make([]any, 0, 8)
	if rc.RequestID != "" {
		attrs = append(attrs, "request_id", rc.RequestID)
	}
	if rc.ParentRequestID != "" {
		attrs = append(attrs, "parent_request_id", rc.ParentRequestID)
	}
	if rc.SessionID != "" {
		attrs = append(attrs, "session_id", rc.SessionID)
	}
	if rc.TenantID != "" {
		attrs = append(attrs, "tenant_id", rc.TenantID)
	}
	if rc.ClientModel != "" {
		attrs = append(attrs, "client_model", rc.ClientModel)
	}
	return attrs
}

// streamLogger is a thin wrapper around slog that keeps the correlation
// attrs sticky. Callers add per-event attrs via the With-style helpers
// below; the request_id/parent_request_id/session_id/tenant_id/client_model
// fields never need to be repeated at each call site.
//
// The wrapper preserves log/slog's default logger so production callers can
// rely on the configured level/handler without further wiring.
type streamLogger struct {
	logger *slog.Logger
	base   []any
}

// streamLogFromContext builds a streamLogger carrying whatever request
// correlation IDs are known at the call site. nil ctx → the global logger
// with no extra attrs (useful for tests / coordinator bootstrap).
func streamLogFromContext(ctx context.Context, base []any) *streamLogger {
	logger := slog.Default()
	var prefix []any
	if rc, ok := streamingContextValue(ctx); ok {
		prefix = rc.logAttrs()
	}
	if len(prefix) > 0 {
		logger = logger.With(prefix...)
	}
	return &streamLogger{logger: logger, base: base}
}

type requestContextKey struct{}

// withStreamingContext attaches a streamingRequestContext to ctx so
// helpers deep in the survival/stream stack can pull correlation IDs
// without threading them through every signature.
func withStreamingContext(ctx context.Context, rc streamingRequestContext) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, requestContextKey{}, rc)
}

// streamingContextValue returns the streamingRequestContext stored on ctx
// (and the boolean reports whether one was set). Nil-safe.
func streamingContextValue(ctx context.Context) (streamingRequestContext, bool) {
	if ctx == nil {
		return streamingRequestContext{}, false
	}
	v, ok := ctx.Value(requestContextKey{}).(streamingRequestContext)
	return v, ok
}

// With returns a derived logger with extra attrs merged in.
func (l *streamLogger) With(args ...any) *streamLogger {
	if l == nil {
		return &streamLogger{logger: slog.Default(), base: args}
	}
	combined := make([]any, 0, len(l.base)+len(args))
	combined = append(combined, l.base...)
	combined = append(combined, args...)
	return &streamLogger{logger: l.logger, base: combined}
}

// Debug logs at the debug level.
func (l *streamLogger) Debug(msg string, args ...any) {
	if l == nil {
		slog.Default().Debug(msg, args...)
		return
	}
	combined := append(append([]any{}, l.base...), args...)
	l.logger.Debug(msg, combined...)
}

// Info logs at the info level.
func (l *streamLogger) Info(msg string, args ...any) {
	if l == nil {
		slog.Default().Info(msg, args...)
		return
	}
	combined := append(append([]any{}, l.base...), args...)
	l.logger.Info(msg, combined...)
}

// Warn logs at the warn level.
func (l *streamLogger) Warn(msg string, args ...any) {
	if l == nil {
		slog.Default().Warn(msg, args...)
		return
	}
	combined := append(append([]any{}, l.base...), args...)
	l.logger.Warn(msg, combined...)
}

// Error logs at the error level.
func (l *streamLogger) Error(msg string, args ...any) {
	if l == nil {
		slog.Default().Error(msg, args...)
		return
	}
	combined := append(append([]any{}, l.base...), args...)
	l.logger.Error(msg, combined...)
}
