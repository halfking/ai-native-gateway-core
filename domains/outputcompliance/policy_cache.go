package outputcompliance

import (
	"context"
	"sync"
)

type requestPolicyCacheKey struct{}

type requestPolicyCache struct {
	mu       sync.Mutex
	policies map[policyCacheLookup]*Policy
}

type policyCacheLookup struct {
	checker *Checker
	tenant  string
}

// WithRequestPolicyCache reuses a policy for all visible text fields in one
// response (including every SSE event). Its lifetime is the request context;
// there is no process-wide tenant map or stale TTL to manage. In-flight policy
// decisions remain consistent if an operator changes settings mid-stream.
func WithRequestPolicyCache(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Value(requestPolicyCacheKey{}) != nil {
		return ctx
	}
	return context.WithValue(ctx, requestPolicyCacheKey{}, &requestPolicyCache{policies: make(map[policyCacheLookup]*Policy)})
}
