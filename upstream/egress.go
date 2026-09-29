package upstream

import (
	"context"
	"net/http"
)

// R28-P-1 (2026-09-30 round 29): per-request egress dispatch. Providers
// carry an egress_profile ('direct' | 'proxy') and an optional
// proxy_subscription_id (providers table). Historically only free-pool
// probes consumed the subscription node pool — production chat traffic
// always dialed through the env single proxy or direct, so the
// "overseas models must go through a proxy" contract did not hold on the
// data plane. The executors stamp the provider id on each upstream
// request; the shared Client consults an EgressTransportProvider before
// dialing and routes proxy-profile providers through their subscription
// node pool.

type egressMetaKey struct{}

// EgressMeta is the per-request egress hint carried in the request
// context. Only ProviderID is required — the policy (profile +
// subscription) is resolved by the EgressTransportProvider, which owns a
// short-TTL cache, so candidate plumbing and the big candidate SQL stay
// untouched.
type EgressMeta struct {
	ProviderID int
}

// WithEgressMeta stamps egress metadata onto an upstream request.
func WithEgressMeta(req *http.Request, providerID int) *http.Request {
	if req == nil || providerID <= 0 {
		return req
	}
	return req.WithContext(context.WithValue(req.Context(), egressMetaKey{}, EgressMeta{ProviderID: providerID}))
}

// EgressMetaFrom extracts the egress hint, if any.
func EgressMetaFrom(ctx context.Context) (EgressMeta, bool) {
	meta, ok := ctx.Value(egressMetaKey{}).(EgressMeta)
	return meta, ok
}

// EgressTransportProvider resolves the outbound transport for a request to
// a given provider. A nil transport means "use the default transport"
// (direct, or the env single proxy via ProxyFunc). An error fails the
// request — used for providers explicitly marked as requiring a proxy
// when no healthy node is available (fail-closed for marked providers).
type EgressTransportProvider interface {
	TransportFor(ctx context.Context, providerID int) (http.RoundTripper, error)
}
