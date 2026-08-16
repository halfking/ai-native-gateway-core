package credentialquota

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

type PolicySource interface {
	LoadPolicies(ctx context.Context) ([]Policy, error)
}

type Resolver struct {
	src    PolicySource
	snap   atomic.Pointer[snapshot]
	lastOK atomic.Pointer[time.Time]
}

type snapshot struct {
	policies map[policyKeyValue]Policy
}

func NewResolver(src PolicySource) *Resolver {
	r := &Resolver{src: src}
	r.snap.Store(&snapshot{policies: map[policyKeyValue]Policy{}})
	return r
}

// Reload reads the full policy set and, when the load succeeds and contains
// no duplicate normalised keys, atomically replaces the snapshot. On error
// the previous snapshot is kept; callers can check lastOK to inspect freshness.
func (r *Resolver) Reload(ctx context.Context) error {
	rows, err := r.src.LoadPolicies(ctx)
	if err != nil {
		return fmt.Errorf("credential client quota reload: %w", err)
	}
	normalized, dup := normalizePolicies(rows)
	if dup != nil {
		return fmt.Errorf("credential client quota reload: %w", dup)
	}
	next := &snapshot{policies: make(map[policyKeyValue]Policy, len(normalized))}
	for _, p := range normalized {
		next.policies[policyKey(p.CredentialID, p.ClientType)] = p
	}
	r.snap.Store(next)
	now := time.Now()
	r.lastOK.Store(&now)
	return nil
}

// Resolve returns the policy for (credentialID, clientType). The clientType
// is normalised so lookups are case-insensitive and unknown values map to
// "unknown". A missing policy means unlimited.
func (r *Resolver) Resolve(ctx context.Context, credentialID int64, clientType string) (Policy, bool) {
	if r == nil {
		return Policy{}, false
	}
	snap := r.snap.Load()
	if snap == nil {
		return Policy{}, false
	}
	p, ok := snap.policies[policyKey(credentialID, clientType)]
	return p, ok
}

// LastSuccessfulReload reports when the snapshot was last replaced. nil when
// the resolver has never observed a successful reload.
func (r *Resolver) LastSuccessfulReload() time.Time {
	t := r.lastOK.Load()
	if t == nil {
		return time.Time{}
	}
	return *t
}

// logResolverState logs a one-line summary after each reload attempt so
// operators can confirm the snapshot is fresh.
func (r *Resolver) logResolverState() {
	if r == nil {
		return
	}
	snap := r.snap.Load()
	if snap == nil {
		return
	}
	slog.Info("credential client quota snapshot refreshed",
		"policies", len(snap.policies),
		"last_ok", r.LastSuccessfulReload().Format(time.RFC3339),
	)
}

// atomicMap is a tiny helper kept local for race-free safe clone.
type atomicMap struct {
	mu sync.Mutex
	m  map[string]struct{}
}
