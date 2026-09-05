package authentication

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
)

// seedFreshThenExpire simulates a key that was verified successfully (so a
// cache entry exists) and whose TTL has since elapsed: the entry stays in
// the map for the DB-outage stale-serve gear.
func seedFreshThenExpire(kv *KeyVerifier, rawKey string, id int) {
	kv.setCache(rawKey, &KeyInfo{ID: id, TenantID: "tenant-x", KeyPrefix: "sk-1", KeyTier: "default"})
	kv.mu.Lock()
	if e, ok := kv.cache[rawKey]; ok {
		e.expiresAt = time.Now().Add(-time.Second)
	}
	kv.mu.Unlock()
}

// TestKeyVerifier_Verify_StaleServeOnDBOutage pins the 2026-09-04
// availability gear: with an infrastructure DB error and an expired cache
// entry inside the stale grace window, Verify must serve the stale entry
// instead of failing the request with 503.
func TestKeyVerifier_Verify_StaleServeOnDBOutage(t *testing.T) {
	mp := newMockPool(t)
	defer mp.Close()

	kv := NewKeyVerifier()
	kv.setDBQuerier(mp, "secret")
	seedFreshThenExpire(kv, "sk-outage", 7)

	mp.ExpectQuery(`SELECT`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnError(errors.New("connection refused"))

	got, err := kv.Verify(context.Background(), "sk-outage")
	if err != nil {
		t.Fatalf("Verify must serve stale on DB outage, got %v", err)
	}
	if got.ID != 7 {
		t.Fatalf("served stale info ID = %d, want 7", got.ID)
	}
}

// TestKeyVerifier_Verify_InvalidKeyNeverStaleServed ensures a genuine
// InvalidKeyError (DB reachable, key unknown/disabled) is never masked by
// the stale gear — revoked keys must keep failing while anything else fails.
func TestKeyVerifier_Verify_InvalidKeyNeverStaleServed(t *testing.T) {
	mp := newMockPool(t)
	defer mp.Close()

	kv := NewKeyVerifier()
	kv.setDBQuerier(mp, "secret")
	seedFreshThenExpire(kv, "sk-revoked", 9)

	mp.ExpectQuery(`SELECT`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "tenant_id", "application_id", "application_code", "key_prefix",
			"default_client_profile", "owner_user", "rate_limit_rpm", "rate_limit_concurrent",
			"rate_limit_tpm", "key_tier", "budget_usd", "status", "key_alias", "customer_id",
		}))

	_, err := kv.Verify(context.Background(), "sk-revoked")
	var invalid *InvalidKeyError
	if !errors.As(err, &invalid) {
		t.Fatalf("err = %T %v, want InvalidKeyError", err, err)
	}
}

// TestKeyVerifier_Verify_StaleBeyondGraceRefused pins the grace bound: an
// entry older than staleGrace must NOT be served.
func TestKeyVerifier_Verify_ExpiredKeyInfoNeverServedFromCache(t *testing.T) {
	mp := newMockPool(t)
	defer mp.Close()
	kv := NewKeyVerifier()
	kv.setDBQuerier(mp, "secret")
	expired := time.Now().Add(-time.Minute)
	kv.setCache("sk-expired", &KeyInfo{ID: 4, TenantID: "t", KeyPrefix: "sk-4", ExpiresAt: &expired})
	mp.ExpectQuery(`SELECT`).WithArgs(pgxmock.AnyArg()).WillReturnError(errors.New("connection refused"))
	if _, err := kv.Verify(context.Background(), "sk-expired"); err == nil {
		t.Fatal("expired KeyInfo must not be served from cache during outage")
	}
}

func TestKeyVerifier_Verify_StaleBeyondGraceRefused(t *testing.T) {
	mp := newMockPool(t)
	defer mp.Close()

	kv := NewKeyVerifier()
	kv.staleGrace = 50 * time.Millisecond
	kv.setDBQuerier(mp, "secret")
	kv.setCache("sk-ancient", &KeyInfo{ID: 3, TenantID: "t", KeyPrefix: "sk-3"})
	kv.mu.Lock()
	if e, ok := kv.cache["sk-ancient"]; ok {
		e.expiresAt = time.Now().Add(-time.Minute)
	}
	kv.mu.Unlock()

	mp.ExpectQuery(`SELECT`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnError(errors.New("connection refused"))

	_, err := kv.Verify(context.Background(), "sk-ancient")
	if err == nil {
		t.Fatal("entry past staleGrace must not be served")
	}
}

// TestKeyVerifier_Verify_StaleGraceZeroDisables pins the kill switch:
// LLM_GATEWAY_AUTH_STALE_GRACE_SECONDS=0 restores strict 503-on-outage.
func TestKeyVerifier_Verify_StaleGraceZeroDisables(t *testing.T) {
	mp := newMockPool(t)
	defer mp.Close()

	kv := NewKeyVerifier()
	kv.staleGrace = 0
	kv.setDBQuerier(mp, "secret")
	seedFreshThenExpire(kv, "sk-strict", 5)

	mp.ExpectQuery(`SELECT`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnError(errors.New("connection refused"))

	if _, err := kv.Verify(context.Background(), "sk-strict"); err == nil {
		t.Fatal("staleGrace=0 must disable stale serving")
	}
}

// TestKeyVerifier_Verify_NoCacheStillFails pins that the gear only helps
// keys this process previously authenticated — a cold process still fails
// closed during an outage (the documented residual limitation).
func TestKeyVerifier_Verify_NoCacheStillFails(t *testing.T) {
	mp := newMockPool(t)
	defer mp.Close()

	kv := NewKeyVerifier()
	kv.setDBQuerier(mp, "secret")

	mp.ExpectQuery(`SELECT`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnError(errors.New("connection refused"))

	if _, err := kv.Verify(context.Background(), "sk-cold"); err == nil {
		t.Fatal("no cached entry + DB error must fail")
	}
}

// TestAuthStaleGraceFromEnv pins the env parsing contract.
func TestAuthStaleGraceFromEnv(t *testing.T) {
	t.Setenv("LLM_GATEWAY_AUTH_STALE_GRACE_SECONDS", "0")
	if got := authStaleGraceFromEnv(); got != 0 {
		t.Fatalf("explicit 0 must disable, got %v", got)
	}
	t.Setenv("LLM_GATEWAY_AUTH_STALE_GRACE_SECONDS", "300")
	if got := authStaleGraceFromEnv(); got != 5*time.Minute {
		t.Fatalf("300 → 5m, got %v", got)
	}
	t.Setenv("LLM_GATEWAY_AUTH_STALE_GRACE_SECONDS", "garbage")
	if got := authStaleGraceFromEnv(); got != 10*time.Minute {
		t.Fatalf("malformed → default 10m, got %v", got)
	}
	t.Setenv("LLM_GATEWAY_AUTH_STALE_GRACE_SECONDS", "")
	if got := authStaleGraceFromEnv(); got != 10*time.Minute {
		t.Fatalf("missing → default 10m, got %v", got)
	}
}
