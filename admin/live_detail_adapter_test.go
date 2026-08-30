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

	meta, err := loadLiveDetailForStore(ctx, store, "tenant-a", "req-live-01")
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

	_, err := loadLiveDetailForStore(context.Background(), store, "tenant-a", "missing")
	require.ErrorIs(t, err, requestdetailNotFound())
}

// TestLiveDetailAdapter_TenantMismatchBlocked ensures a tenant_admin
// cannot read another tenant's live entry through the adapter — same
// fail-closed contract as the request_logs lookup.
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

	_, err = loadLiveDetailForStore(ctx, store, "tenant-b", "req-cross-tenant")
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

	_, err := loadLiveDetailForStore(ctx, store, "tenant-a", "req-empty-tenant")
	require.ErrorIs(t, err, requestdetailNotFound(), "tenant-scoped reads must reject an unscoped live row")
}
