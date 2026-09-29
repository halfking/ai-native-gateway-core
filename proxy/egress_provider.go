package proxy

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

// ErrEgressNotProxied reports that the provider's egress profile does not
// require a proxy — the caller should use its default transport.
var ErrEgressNotProxied = errors.New("provider egress profile does not require a proxy")

// EgressPolicy is a provider's egress configuration row.
type EgressPolicy struct {
	Profile        string // 'direct' | 'proxy'
	SubscriptionID *int
}

// egressRowQuerier is satisfied by *pgxpool.Pool (and anything else that
// speaks the pgx row API).
type egressRowQuerier interface {
	QueryRow(ctx context.Context, query string, args ...any) pgx.Row
}

// EgressProvider implements upstream.EgressTransportProvider: it resolves a
// provider's egress_profile from the providers table (short-TTL cache so
// the per-request path never dials the DB hot) and hands out the
// subscription node pool transport for proxy-profile providers.
//
// R28-P-1 (2026-09-30 round 29): before this, providers.egress_profile /
// proxy_subscription_id were written and displayed but never consumed by
// the data plane, so "overseas providers must go through a proxy" did not
// hold for production chat traffic. For a provider explicitly marked
// 'proxy', an unavailable node pool FAILS the request (fail-closed for
// marked providers) instead of silently dialing direct.
type EgressProvider struct {
	manager *Manager
	db      egressRowQuerier
	ttl     time.Duration

	mu    sync.RWMutex
	cache map[int]cachedEgressPolicy
}

type cachedEgressPolicy struct {
	policy  EgressPolicy
	expires time.Time
}

// NewEgressProvider builds the per-request egress dispatcher. ttl bounds
// policy staleness after an operator flips a provider's profile; 0 defaults
// to 60s.
func NewEgressProvider(manager *Manager, db egressRowQuerier, ttl time.Duration) *EgressProvider {
	if ttl <= 0 {
		ttl = 60 * time.Second
	}
	return &EgressProvider{manager: manager, db: db, ttl: ttl, cache: make(map[int]cachedEgressPolicy)}
}

// TransportFor returns the subscription node-pool transport for
// proxy-profile providers, (nil, nil) for direct-profile providers, and an
// error when a proxy-profile provider has no healthy node.
func (p *EgressProvider) TransportFor(ctx context.Context, providerID int) (http.RoundTripper, error) {
	if p == nil || p.manager == nil {
		return nil, nil
	}
	policy, err := p.policy(ctx, providerID)
	if err != nil {
		return nil, err
	}
	if policy.Profile != "proxy" {
		return nil, nil
	}
	transport, err := p.manager.GetProxyTransport(ctx, policy.SubscriptionID)
	if err != nil {
		return nil, err
	}
	return transport, nil
}

func (p *EgressProvider) policy(ctx context.Context, providerID int) (EgressPolicy, error) {
	now := time.Now()
	p.mu.RLock()
	cached, ok := p.cache[providerID]
	p.mu.RUnlock()
	if ok && now.Before(cached.expires) {
		return cached.policy, nil
	}

	policy := EgressPolicy{Profile: "direct"}
	if p.db != nil {
		var profile string
		var subscriptionID *int
		if err := p.db.QueryRow(ctx,
			`SELECT egress_profile, proxy_subscription_id FROM providers WHERE id = $1`, providerID).
			Scan(&profile, &subscriptionID); err != nil {
			// Unknown provider id or transient DB error: keep direct
			// (fail-open) — egress marking is opt-in per provider, so the
			// safe default for unmarked rows is the legacy path.
			policy = EgressPolicy{Profile: "direct"}
		} else if profile == "proxy" {
			// subscriptionID may be nil: the manager then selects the best
			// node across all subscriptions.
			policy = EgressPolicy{Profile: "proxy", SubscriptionID: subscriptionID}
		}
	}

	p.mu.Lock()
	p.cache[providerID] = cachedEgressPolicy{policy: policy, expires: now.Add(p.ttl)}
	p.mu.Unlock()
	return policy, nil
}

// Invalidate drops the cached policy for one provider (call after provider
// egress settings change).
func (p *EgressProvider) Invalidate(providerID int) {
	p.mu.Lock()
	delete(p.cache, providerID)
	p.mu.Unlock()
}
