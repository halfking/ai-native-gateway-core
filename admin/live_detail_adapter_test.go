package admin

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/kaixuan/llm-gateway-go/domains/requestdetail"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func requestdetailNotFound() error { return requestdetail.ErrNotFound }

// TestLiveDetailAdapter_HitReturnsMeta pins that a Redis-hit returns the
// live-store meta via the Locator's LiveDetailReader interface. Without
// this, dashboard swim-lane clicks 404 until the request_logs write lands.
func TestLiveDetailAdapter_HitReturnsMeta(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	store := NewLiveStreamRedisStore(rdb)
	ctx := context.Background()

	latency := 120
	err := store.Record(ctx, LiveRequest{
		RequestID:   "req-live-01",
		TenantID:    "tenant-a",
		GwSessionID: "session-1",
		Model:       "gpt-4o-mini",
		Status:      "in_progress",
		LatencyMs:   &latency,
	}, "")
	require.NoError(t, err)

	meta, err := loadLiveDetailForStore(ctx, store, requestdetail.LookupScope{TenantID: "tenant-a"}, "req-live-01")
	require.NoError(t, err)
	require.Equal(t, "req-live-01", meta.RequestID)
	require.Equal(t, "tenant-a", meta.TenantID)
	require.NotNil(t, meta.GwSessionID)
	require.Equal(t, "session-1", *meta.GwSessionID)
	require.NotNil(t, meta.ClientModel)
	require.Equal(t, "gpt-4o-mini", *meta.ClientModel)
	require.NotNil(t, meta.LatencyMs)
	require.Equal(t, 120, *meta.LatencyMs)
}

// TestLiveDetailAdapter_MissReturnsErrNotFound guards the Locator's
// fall-through: when Redis has nothing, the adapter must surface
// ErrNotFound so the DB layers run next.
func TestLiveDetailAdapter_MissReturnsErrNotFound(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	store := NewLiveStreamRedisStore(rdb)

	_, err := loadLiveDetailForStore(context.Background(), store, requestdetail.LookupScope{TenantID: "tenant-a"}, "missing")
	require.ErrorIs(t, err, requestdetailNotFound())
}

// TestLiveDetailAdapter_SuperAdminCanReadAnyTenant ensures an unrestricted
// (super_admin / legacy admin-key) scope can read a live entry regardless
// of its tenant.
func TestLiveDetailAdapter_SuperAdminCanReadAnyTenant(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	store := NewLiveStreamRedisStore(rdb)
	ctx := context.Background()

	err := store.Record(ctx, LiveRequest{
		RequestID: "req-super-live",
		TenantID:  "tenant-a",
		Model:     "gpt-4o-mini",
		Status:    "in_progress",
	}, "")
	require.NoError(t, err)

	meta, err := loadLiveDetailForStore(ctx, store, requestdetail.LookupScope{Unrestricted: true}, "req-super-live")
	require.NoError(t, err)
	require.Equal(t, "tenant-a", meta.TenantID)
}

func TestLiveDetailAdapter_TenantMismatchBlocked(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	store := NewLiveStreamRedisStore(rdb)
	ctx := context.Background()

	err := store.Record(ctx, LiveRequest{
		RequestID: "req-cross-tenant",
		TenantID:  "tenant-a",
		Model:     "gpt-4o-mini",
		Status:    "in_progress",
	}, "")
	require.NoError(t, err)

	_, err = loadLiveDetailForStore(ctx, store, requestdetail.LookupScope{TenantID: "tenant-b"}, "req-cross-tenant")
	require.ErrorIs(t, err, requestdetailNotFound(),
		"cross-tenant live lookup must fail closed with ErrNotFound")
}

func TestLiveDetailAdapter_EmptyLoadedTenantBlockedForTenantScope(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	store := NewLiveStreamRedisStore(rdb)
	ctx := context.Background()

	require.NoError(t, store.Record(ctx, LiveRequest{
		RequestID: "req-empty-tenant",
		Model:     "gpt-4o-mini",
		Status:    "in_progress",
	}, ""))

	_, err := loadLiveDetailForStore(ctx, store, requestdetail.LookupScope{TenantID: "tenant-a"}, "req-empty-tenant")
	require.ErrorIs(t, err, requestdetailNotFound(), "tenant-scoped reads must reject an unscoped live row")
}

// TestLiveStreamRedisStore_LoadRequest_NotFoundSentinel pins the sentinel
// contract added in 2026-08-31 audit P3-1 cleanup: callers must use
// errors.Is(err, ErrLiveStreamRequestNotFound) instead of byte-prefix
// matching on the wrapped "request not found: <id>" message.
func TestLiveStreamRedisStore_LoadRequest_NotFoundSentinel(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	store := NewLiveStreamRedisStore(rdb)

	_, err := store.LoadRequest(context.Background(), "tenant-a", "rid-missing")
	require.ErrorIs(t, err, ErrLiveStreamRequestNotFound)
	// Wrapped context (request id) must be preserved for logs.
	require.Contains(t, err.Error(), "rid-missing")
}

// TestLiveStreamRedisStore_LoadRequest_StoreUnavailableSentinel covers
// the nil-receiver / empty-id branch — adapter surfaces it as a miss so
// the Locator falls through to DB-backed lookups.
func TestLiveStreamRedisStore_LoadRequest_StoreUnavailableSentinel(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:0"}) // unroutable, but rdb != nil
	store := NewLiveStreamRedisStore(rdb)

	_, err := store.LoadRequest(context.Background(), "tenant-a", "")
	require.ErrorIs(t, err, ErrLiveStreamStoreUnavailable)

	// nil receiver must also return the sentinel, not panic.
	var nilStore *LiveStreamRedisStore
	_, err = nilStore.LoadRequest(context.Background(), "tenant-a", "rid-x")
	require.ErrorIs(t, err, ErrLiveStreamStoreUnavailable)
}

// TestLoadLiveDetailForStore_AdapterBridgesSentinels ensures the adapter
// translates both LoadRequest sentinels to requestdetail.ErrNotFound so
// the Locator's fall-through contract holds without string matching.
func TestLoadLiveDetailForStore_AdapterBridgesSentinels(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	store := NewLiveStreamRedisStore(rdb)

	// NotFound branch.
	_, err := loadLiveDetailForStore(context.Background(), store, requestdetail.LookupScope{TenantID: "tenant-a"}, "rid-missing")
	require.ErrorIs(t, err, requestdetail.ErrNotFound)

	// Store-unavailable branch via nil store.
	_, err = loadLiveDetailForStore(context.Background(), nil, requestdetail.LookupScope{TenantID: "tenant-a"}, "rid-x")
	require.ErrorIs(t, err, requestdetail.ErrNotFound)
}
