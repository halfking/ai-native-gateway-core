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

// get returns the cached policy for one checker/tenant pair, or nil.
func (r *requestPolicyCache) get(c *Checker, tenant string) *Policy {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.policies[policyCacheLookup{checker: c, tenant: tenant}]
}

// put stores a successfully loaded policy. Callers must only cache
// error-free loads (including the ErrNoRows → defaultPolicy form) so a
// transient DB failure is retried on the next field instead of being
// pinned for the whole request.
func (r *requestPolicyCache) put(c *Checker, tenant string, p *Policy) {
	if r == nil || p == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.policies[policyCacheLookup{checker: c, tenant: tenant}] = p
}
