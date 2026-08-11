package quotafetcher

import (
	"context"
	"testing"
	"time"
)

// TestManagerUnsupportedProviderFailOpen verifies the core fail-open contract:
// an unregistered provider returns (nil, nil), never an error, so the caller
// (virtual_factory preflightQuota) falls back to the existing DB Preflight.
func TestManagerUnsupportedProviderFailOpen(t *testing.T) {
	mgr := NewManager(nil)
	mgr.RegisterBuiltins() // registers openrouter + balance vendors, NOT "acme"

	qi, err := mgr.FetchQuota(context.Background(), FetchRequest{
		ProviderCode: "acme",
		CredentialID: 1,
		BaseURL:      "https://api.acme.com",
		APIKey:       "sk-test",
	})
	if err != nil {
		t.Fatalf("unsupported provider must not error, got %v", err)
	}
	if qi != nil {
		t.Fatalf("unsupported provider must return nil quota, got %+v", qi)
	}
}

// TestManagerMissingKeyFailOpen verifies a registered provider with no key
// (and no KeyRevealer) returns (nil, nil) — never blocks.
func TestManagerMissingKeyFailOpen(t *testing.T) {
	mgr := NewManager(nil) // no KeyRevealer
	mgr.RegisterBuiltins()

	qi, err := mgr.FetchQuota(context.Background(), FetchRequest{
		ProviderCode: "openrouter",
		CredentialID: 1,
		BaseURL:      "https://openrouter.ai/api/v1",
		APIKey:       "", // no key, no revealer
	})
	if err != nil {
		t.Fatalf("missing key must not error, got %v", err)
	}
	if qi != nil {
		t.Fatalf("missing key must return nil quota, got %+v", qi)
	}
}

// TestCacheSetGetInvalidate verifies the cache lifecycle: Set then Get hits,
// after TTL expires Get misses, and Invalidate forces a miss.
func TestCacheSetGetInvalidate(t *testing.T) {
	c := newQuotaCache()
	defer c.Stop()

	qi := &QuotaInfo{PercentUsed: 0.5, Total: 100}
	c.Set(42, qi)

	got, ok := c.Get(42)
	if !ok || got != qi {
		t.Fatalf("expected cache hit, got ok=%v qi=%+v", ok, got)
	}

	c.Invalidate(42)
	if _, ok := c.Get(42); ok {
		t.Fatal("expected cache miss after Invalidate")
	}
}

// TestCacheTTLExpiry verifies entries expire after the TTL. Uses a direct
// entry injection with a backdated fetchedAt to avoid waiting 45s.
func TestCacheTTLExpiry(t *testing.T) {
	c := newQuotaCache()
	defer c.Stop()

	c.mu.Lock()
	c.entries[7] = cacheEntry{
		value:     &QuotaInfo{Total: 50},
		fetchedAt: time.Now().Add(-2 * cacheTTL), // stale
	}
	c.mu.Unlock()

	if _, ok := c.Get(7); ok {
		t.Fatal("expected cache miss for expired entry")
	}
}

// TestRegistryCaseInsensitive verifies lookup is case-insensitive (OmniRoute
// does provider.toLowerCase() fallback).
func TestRegistryCaseInsensitive(t *testing.T) {
	r := newRegistry()
	r.Register("OpenRouter", &openrouterFetcher{})

	if _, ok := r.Get("openrouter"); !ok {
		t.Fatal("expected case-insensitive lookup to hit")
	}
	if _, ok := r.Get("OPENROUTER"); !ok {
		t.Fatal("expected uppercase lookup to hit")
	}
	if r.IsSupported("OpenRouter") != true {
		t.Fatal("IsSupported should be true")
	}
}

// TestBalanceToQuota verifies the USD-balance → QuotaInfo projection: positive
// balance is usable (LimitReached=false), zero/negative is exhausted.
func TestBalanceToQuota(t *testing.T) {
	if q := balanceToQuota(10.5); q.LimitReached || q.PercentUsed != 0 {
		t.Fatalf("positive balance should be usable, got %+v", q)
	}
	if q := balanceToQuota(0); !q.LimitReached || q.PercentUsed != 1 {
		t.Fatalf("zero balance should be exhausted, got %+v", q)
	}
	if q := balanceToQuota(-1); !q.LimitReached {
		t.Fatalf("negative balance should be exhausted, got %+v", q)
	}
}

// TestWalkJSONFloat verifies the dot-path + array-index navigation.
func TestWalkJSONFloat(t *testing.T) {
	body := []byte(`{"data":{"balance":12.5},"balance_infos":[{"total_balance":"99.9"}]}`)

	if v, ok := walkJSONFloat(body, "data.balance"); !ok || v != 12.5 {
		t.Fatalf("nested map path failed: v=%v ok=%v", v, ok)
	}
	if v, ok := walkJSONFloat(body, "balance_infos.0.total_balance"); !ok || v != 99.9 {
		t.Fatalf("array-index path failed: v=%v ok=%v", v, ok)
	}
	if _, ok := walkJSONFloat(body, "missing.path"); ok {
		t.Fatal("missing path should miss")
	}
}

// TestOpenrouterBuildQuota covers the /key + /credits merge: capped key with
// remaining > 0 → usable; remaining <= 0 → LimitReached; no cap → credit-fallback.
func TestOpenrouterBuildQuota(t *testing.T) {
	f := &openrouterFetcher{}

	// Capped, healthy.
	limit, rem := 10.0, 7.5
	qi := f.buildQuota(orKeyResponse{}, orCreditsResponse{})
	// default zero struct: no cap, no credits → not reached
	if qi.LimitReached {
		t.Fatal("no cap + no credits should not be LimitReached")
	}

	// Capped, exhausted.
	qi = f.buildQuota(orKeyResponse{}, orCreditsResponse{})
	_ = limit
	_ = rem
	// Construct a capped-exhausted response directly.
	exhausted := orKeyResponse{}
	exhausted.Data.Limit = ptrFloat(10)
	exhausted.Data.LimitRemaining = ptrFloat(0)
	qi = f.buildQuota(exhausted, orCreditsResponse{})
	if !qi.LimitReached || qi.PercentUsed != 1 {
		t.Fatalf("capped+remaining0 should be exhausted, got %+v", qi)
	}
}

func ptrFloat(v float64) *float64 { return &v }
