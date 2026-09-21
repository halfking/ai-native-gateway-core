package routingopt

import "context"

// RequestMeta carries per-request identity into the optimizer hooks.
//
// autoroute.Decider injects it into the request context before calling
// PreClassify/RecommendModel/RecordFeedback, so the plugin can personalise
// routing (user affinity) and attribute feedback without changing the
// RoutingOptimizer interface signatures.
type RequestMeta struct {
	// UserID is the resolved API key ID (0 = unauthenticated).
	UserID int
	// SessionID is X-Gw-Session-Id ("" = no session).
	SessionID string
	// ClientType is the IDE/client origin from ClassificationSignals
	// (e.g. "cursor", "claude-code"); "" when unknown.
	ClientType string
}

// requestMetaKey is the context key for RequestMeta. Exported helpers
// WithRequestMeta/RequestMetaFrom keep the key private while letting
// autoroute (which imports routingopt) attach and read the metadata.
type requestMetaKey struct{}

// WithRequestMeta returns a child context carrying meta.
func WithRequestMeta(ctx context.Context, meta RequestMeta) context.Context {
	return context.WithValue(ctx, requestMetaKey{}, meta)
}

// RequestMetaFrom extracts RequestMeta from ctx. Returns the zero meta
// (UserID 0, empty strings) when absent — callers must treat that as
// "unauthenticated / no session".
func RequestMetaFrom(ctx context.Context) RequestMeta {
	if ctx == nil {
		return RequestMeta{}
	}
	if meta, ok := ctx.Value(requestMetaKey{}).(RequestMeta); ok {
		return meta
	}
	return RequestMeta{}
}
