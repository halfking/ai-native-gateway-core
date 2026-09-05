package authentication

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
)

// keystoreColumns must mirror keyStoreSelectSQL order.
var keystoreColumns = []string{
	"key_hash", "id", "tenant_id", "application_id", "application_code",
	"key_prefix", "default_client_profile", "owner_user", "rate_limit_rpm",
	"rate_limit_concurrent", "rate_limit_tpm", "key_tier", "budget_usd",
	"status", "key_alias", "customer_id", "expires_at",
}

func keystoreRow(hash string, id int, expiresAt any) *pgxmock.Rows {
	return pgxmock.NewRows(keystoreColumns).
		AddRow(hash, id, "tenant-x", 1, "app1", "sk-1", nil, nil,
			nil, nil, nil, "default", nil, "active", nil, nil, expiresAt)
}

// TestKeyStore_FullLoadServesWithZeroDBIO pins the core availability
// property: once the full load completes, Verify() answers entirely from
// memory — pgxmock fails the test on ANY unexpected DB call.
func TestKeyStore_FullLoadServesWithZeroDBIO(t *testing.T) {
	mp := newMockPool(t)
	defer mp.Close()

	kv := NewKeyVerifier()
	kv.setDBQuerier(mp, "secret")

	// The store is keyed by key_hash; use the real HMAC so Verify()'s
	// lookup actually hits the loaded row.
	storeHash := hashAPIKey("secret", "sk-storehit")
	mp.ExpectQuery(`WHERE ak.enabled = TRUE`).
		WillReturnRows(keystoreRow(storeHash, 7, nil))

	if err := kv.keyStoreFullLoad(context.Background()); err != nil {
		t.Fatalf("full load: %v", err)
	}
	if got := kv.keyStoreLen(); got != 1 {
		t.Fatalf("store len = %d, want 1", got)
	}

	// Suppress the throttled last_used_at write so "zero DB IO" is literal.
	kv.lastUsedTouchMu.Lock()
	kv.lastUsedTouch[7] = time.Now()
	kv.lastUsedTouchMu.Unlock()

	info, err := kv.Verify(context.Background(), "sk-storehit")
	if err != nil {
		t.Fatalf("Verify via store: %v", err)
	}
	if info.ID != 7 || info.TenantID != "tenant-x" {
		t.Fatalf("info = %+v", info)
	}
	if err := mp.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations (no DB call expected on store hit): %v", err)
	}
}

// TestKeyStore_DeltaRevocationPropagation pins that the invalid-set query
// removes revoked keys on the next sync, so Verify() falls back to the lazy
// path (which here confirms the key is gone → InvalidKeyError).
func TestKeyStore_DeltaRevocationPropagation(t *testing.T) {
	mp := newMockPool(t)
	defer mp.Close()

	kv := NewKeyVerifier()
	kv.setDBQuerier(mp, "secret")

	hash := "hash-revoked"
	kv.upsertStore(hash, &KeyInfo{ID: 9, TenantID: "tenant-x", KeyPrefix: "sk-9"})
	kv.keyStoreLoaded.Store(true)

	// Delta: watermark query returns nothing new…
	mp.ExpectQuery(`make_interval`).
		WithArgs(int64(10)).
		WillReturnRows(pgxmock.NewRows(keystoreColumns))
	// …and the invalid set names the revoked key.
	mp.ExpectQuery(`ak.enabled = FALSE`).
		WillReturnRows(pgxmock.NewRows([]string{"key_hash"}).AddRow(hash))

	if err := kv.keyStoreSyncDelta(context.Background(), 5*time.Minute); err != nil {
		t.Fatalf("delta sync: %v", err)
	}
	if got := kv.keyStoreLen(); got != 0 {
		t.Fatalf("store len after revocation delta = %d, want 0", got)
	}

	// Next Verify must fall through to the lazy path and see the DB truth.
	mp.ExpectQuery(`WHERE ak.key_hash = \$1`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "tenant_id", "application_id", "application_code", "key_prefix",
			"default_client_profile", "owner_user", "rate_limit_rpm", "rate_limit_concurrent",
			"rate_limit_tpm", "key_tier", "budget_usd", "status", "key_alias", "customer_id", "expires_at",
		}))
	_, err := kv.Verify(context.Background(), "sk-revoked-later")
	var invalid *InvalidKeyError
	if !errors.As(err, &invalid) {
		t.Fatalf("err = %T %v, want InvalidKeyError after revocation sync", err, err)
	}
}

// TestKeyStore_DeltaUpsertRefreshesRow pins the watermark path: a key used
// recently (thus touched by the throttled last_used_at write) re-enters the
// store with its freshest row — e.g. a changed tier.
func TestKeyStore_DeltaUpsertRefreshesRow(t *testing.T) {
	mp := newMockPool(t)
	defer mp.Close()

	kv := NewKeyVerifier()
	kv.setDBQuerier(mp, "secret")
	kv.upsertStore("hash-stale", &KeyInfo{ID: 5, TenantID: "t", KeyPrefix: "sk-5", KeyTier: "default"})
	kv.keyStoreLoaded.Store(true)

	mp.ExpectQuery(`make_interval`).
		WithArgs(int64(10)).
		WillReturnRows(pgxmock.NewRows(keystoreColumns).
			AddRow("hash-stale", 5, "t", 1, "app1", "sk-5", nil, nil,
				nil, nil, nil, "production", nil, "active", nil, nil, nil))
	mp.ExpectQuery(`ak.enabled = FALSE`).
		WillReturnRows(pgxmock.NewRows([]string{"key_hash"}))

	if err := kv.keyStoreSyncDelta(context.Background(), 5*time.Minute); err != nil {
		t.Fatalf("delta sync: %v", err)
	}
	info := kv.lookupStore("hash-stale")
	if info == nil {
		t.Fatal("refreshed key missing from store")
	}
	if info.KeyTier != "production" {
		t.Fatalf("row not refreshed: %+v", info)
	}
}

// TestKeyStore_ReadTimeExpiry pins that expires_at is enforced at READ
// time: an entry whose expiry crossed now between syncs stops authorizing
// immediately, with no DB round-trip.
func TestKeyStore_ReadTimeExpiry(t *testing.T) {
	kv := NewKeyVerifier()
	past := time.Now().Add(-time.Minute)
	kv.upsertStore("hash-exp", &KeyInfo{ID: 3, TenantID: "t", ExpiresAt: &past})
	kv.keyStoreLoaded.Store(true)

	if kv.lookupStore("hash-exp") != nil {
		t.Fatal("expired entry must not authorize at read time")
	}

	future := time.Now().Add(time.Hour)
	kv.upsertStore("hash-ok", &KeyInfo{ID: 4, TenantID: "t", ExpiresAt: &future})
	if kv.lookupStore("hash-ok") == nil {
		t.Fatal("unexpired entry must authorize")
	}
}

// TestKeyStore_HardDeleteReconcilesViaLazyPath pins the SECONDARY hard-delete
// reconciliation: when a key's entry is not currently served from the store
// (between syncs, after invalidation) and the lazy path observes ErrNoRows,
// the stale store copy is dropped so it cannot resurface. (The PRIMARY
// defense is the hourly full refresh; a store HIT during an outage keeps
// serving by design — that is the availability tradeoff.)
func TestKeyStore_HardDeleteReconcilesViaLazyPath(t *testing.T) {
	mp := newMockPool(t)
	defer mp.Close()

	kv := NewKeyVerifier()
	kv.setDBQuerier(mp, "secret")
	// Seed under the hash Verify will actually compute for this raw key.
	storeHash := hashAPIKey("secret", "sk-deleted")
	kv.upsertStore(storeHash, &KeyInfo{ID: 11, TenantID: "t", KeyPrefix: "sk-11"})
	// Store not serving (fresh boot / between loads) → lazy path runs.
	kv.keyStoreLoaded.Store(false)

	mp.ExpectQuery(`WHERE ak.key_hash = \$1`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "tenant_id", "application_id", "application_code", "key_prefix",
			"default_client_profile", "owner_user", "rate_limit_rpm", "rate_limit_concurrent",
			"rate_limit_tpm", "key_tier", "budget_usd", "status", "key_alias", "customer_id", "expires_at",
		}))

	_, err := kv.Verify(context.Background(), "sk-deleted")
	var invalid *InvalidKeyError
	if !errors.As(err, &invalid) {
		t.Fatalf("err = %T %v, want InvalidKeyError", err, err)
	}
	kv.keyStoreLoaded.Store(true)
	if kv.lookupStore(storeHash) != nil {
		t.Fatal("hard-deleted key must be dropped from the store copy")
	}
}

// TestKeyStore_LazySuccessUpserts pins convergence: a key created between
// syncs is verified lazily once and then served from the store.
func TestKeyStore_LazySuccessUpserts(t *testing.T) {
	mp := newMockPool(t)
	defer mp.Close()

	kv := NewKeyVerifier()
	kv.setDBQuerier(mp, "secret")
	kv.keyStoreLoaded.Store(true) // store active but does not know this key

	mp.ExpectQuery(`WHERE ak.key_hash = \$1`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "tenant_id", "application_id", "application_code", "key_prefix",
			"default_client_profile", "owner_user", "rate_limit_rpm", "rate_limit_concurrent",
			"rate_limit_tpm", "key_tier", "budget_usd", "status", "key_alias", "customer_id", "expires_at",
		}).AddRow(21, "t", 1, "app1", "sk-21", nil, nil, nil, nil, nil,
			"default", nil, "active", nil, nil, nil))
	mp.ExpectExec(`UPDATE api_keys SET last_used_at`).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	info, err := kv.Verify(context.Background(), "sk-brand-new")
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if info.ID != 21 {
		t.Fatalf("info = %+v", info)
	}
	time.Sleep(50 * time.Millisecond) // let the async last_used_at write drain

	if kv.lookupStore(hashAPIKey("secret", "sk-brand-new")) == nil {
		t.Fatal("lazy success must upsert into the store")
	}
}

// TestKeyStore_InvalidateKeyIDPurgesStore pins same-process admin
// revocation immediacy: the store entry dies with the raw-key cache entry.
func TestKeyStore_InvalidateKeyIDPurgesStore(t *testing.T) {
	kv := NewKeyVerifier()
	kv.upsertStore("hash-inv", &KeyInfo{ID: 33, TenantID: "t", KeyPrefix: "sk-33"})
	kv.keyStoreLoaded.Store(true)

	kv.InvalidateKeyID(33)
	if kv.lookupStore("hash-inv") != nil {
		t.Fatal("InvalidateKeyID must purge the store entry")
	}
}

// TestKeyStore_NotLoadedFallsBack pins that an unloaded store (DB down at
// boot) never answers from memory — Verify uses the lazy path unchanged.
func TestKeyStore_NotLoadedFallsBack(t *testing.T) {
	mp := newMockPool(t)
	defer mp.Close()

	kv := NewKeyVerifier()
	kv.setDBQuerier(mp, "secret")
	// keyStoreLoaded is false; even a stray entry must not be served.
	kv.upsertStore("hash-early", &KeyInfo{ID: 2, TenantID: "t", KeyPrefix: "sk-2"})

	if kv.lookupStore("hash-early") != nil {
		t.Fatal("unloaded store must not answer")
	}
}
