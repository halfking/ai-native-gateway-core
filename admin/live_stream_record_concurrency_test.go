package admin

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 2026-07-27: Regression tests for the Record() read-modify-write race.
//
// Before the per-request_id SETNX lock, two concurrent Records for the same
// request_id (in_progress + terminal, common under the telemetry batcher)
// both read the same old payload, each computed the same ZREM set, and each
// ZADDed its own slim tile. Because the slim tile carries status + error_kind
// (which differ between in_progress and terminal), the two ZADDs inserted two
// DISTINCT JSON members into the same dimension ZSET → the request showed up
// twice on the swim lane.
//
// These tests assert that does not happen.

func setupLiveStreamStore(t *testing.T) (*LiveStreamRedisStore, *miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { rdb.Close() })
	store := NewLiveStreamRedisStore(rdb)
	return store, mr, rdb
}

// countMembersForRequest scans a dimension queue and counts how many ZSET
// members are slim tiles belonging to requestID. The slim tile JSON embeds the
// request_id as "rid":"<id>", so a substring match is reliable.
func countMembersForRequest(t *testing.T, rdb *redis.Client, key, requestID string) int {
	t.Helper()
	ctx := context.Background()
	members, err := rdb.ZRange(ctx, key, 0, -1).Result()
	require.NoError(t, err, "ZRange %s", key)
	needle := fmt.Sprintf(`"rid":%q`, requestID)
	count := 0
	for _, m := range members {
		// Main queues store the bare request_id (no JSON wrapper).
		if m == requestID {
			count++
			continue
		}
		if strings.Contains(m, needle) || strings.Contains(m, requestID) {
			count++
		}
	}
	return count
}

// TestRecord_ConcurrentSameRequestID_NoDuplicateMembers is THE core regression
// test. 50 goroutines all Record the SAME request_id with mixed statuses
// concurrently. After they finish, every queue the request belongs to must
// contain EXACTLY ONE member for that id.
func TestRecord_ConcurrentSameRequestID_NoDuplicateMembers(t *testing.T) {
	store, _, rdb := setupLiveStreamStore(t)
	ctx := context.Background()

	const requestID = "req-concurrent-1"
	statuses := []string{"in_progress", "success", "failure", "rate_limited"}
	const goroutines = 50
	var wg sync.WaitGroup
	var errs atomic.Int32
	start := make(chan struct{})

	for i := 0; i < goroutines; i++ {
		status := statuses[i%len(statuses)]
		wg.Add(1)
		go func(status string) {
			defer wg.Done()
			req := LiveRequest{
				RequestID:     requestID,
				Ts:            time.Now().UTC().Format(time.RFC3339Nano),
				TenantID:      "tenant-a",
				Model:         "gpt-4o",
				ModelCategory: "openai",
				ProviderCode:  "openai",
				Status:        status,
			}
			<-start // release all goroutines together to maximize contention
			if err := store.Record(ctx, req, ""); err != nil {
				errs.Add(1)
			}
		}(status)
	}
	close(start)
	wg.Wait()
	require.Zero(t, errs.Load(), "some Record calls returned errors")

	// The request has vendor=openai, provider=openai, model=gpt-4o, so it lands
	// in the vendor/provider/model dimension queues + main + a status queue.
	// Every one of them must contain the request_id exactly once.
	queues := []string{
		liveStreamMainKey,
		tenantLiveStreamKey("tenant-a", "main"),
		liveStreamDimPrefix + "vendor:openai",
		tenantLiveStreamKey("tenant-a", "dim:vendor:openai"),
		liveStreamDimPrefix + "provider:openai",
		tenantLiveStreamKey("tenant-a", "dim:provider:openai"),
		liveStreamDimPrefix + "model:gpt-4o",
		tenantLiveStreamKey("tenant-a", "dim:model:gpt-4o"),
	}
	for _, q := range queues {
		got := countMembersForRequest(t, rdb, q, requestID)
		assert.Equalf(t, 1, got, "queue %s has %d members for %s (want exactly 1)", q, got, requestID)
	}
}

// TestRecord_InProgressToSuccess_NoStaleDimMember: the in_progress slim tile
// must be removed from the dim queue when the terminal update arrives, leaving
// only the terminal tile. Before the lock, both could coexist.
func TestRecord_InProgressToSuccess_NoStaleDimMember(t *testing.T) {
	store, _, rdb := setupLiveStreamStore(t)
	ctx := context.Background()
	const requestID = "req-transition-1"

	require.NoError(t, store.Record(ctx, LiveRequest{
		RequestID: requestID,
		Ts:        time.Now().UTC().Add(-2 * time.Second).Format(time.RFC3339Nano),
		TenantID:  "tenant-a", Model: "gpt-4o", ModelCategory: "openai",
		ProviderCode: "openai", Status: "in_progress",
	}, ""))

	// Capture the in_progress slim tile that was written.
	vendorKey := liveStreamDimPrefix + "vendor:openai"
	before, err := rdb.ZRange(ctx, vendorKey, 0, -1).Result()
	require.NoError(t, err)
	require.Len(t, before, 1, "expected one in_progress tile before transition")

	require.NoError(t, store.Record(ctx, LiveRequest{
		RequestID: requestID,
		Ts:        time.Now().UTC().Format(time.RFC3339Nano),
		TenantID:  "tenant-a", Model: "gpt-4o", ModelCategory: "openai",
		ProviderCode: "openai", Status: "success",
	}, ""))

	// Exactly one member, and it must be the success tile (not in_progress).
	assert.Equal(t, 1, countMembersForRequest(t, rdb, vendorKey, requestID),
		"vendor queue should hold exactly one member after transition")
	members, err := rdb.ZRange(ctx, vendorKey, 0, -1).Result()
	require.NoError(t, err)
	require.Len(t, members, 1)
	assert.Contains(t, members[0], `"st":"success"`, "remaining tile should be success, not in_progress")

	// Status queues: the in_progress tile must be gone, success present.
	assert.Equal(t, 0, countMembersForRequest(t, rdb, liveStreamStatPrefix+"in_progress", requestID),
		"in_progress status queue must not retain the tile after transition")
	assert.Equal(t, 1, countMembersForRequest(t, rdb, liveStreamStatPrefix+"success", requestID),
		"success status queue must hold the tile after transition")
}

// TestRecord_LateUpdateDoesNotMoveBackwards: a stale update with an older
// timestamp must not push the request earlier in the window.
func TestRecord_LateUpdateDoesNotMoveBackwards(t *testing.T) {
	store, mr, rdb := setupLiveStreamStore(t)
	ctx := context.Background()
	const requestID = "req-mono-1"

	newer := time.Now().UTC()
	older := newer.Add(-10 * time.Second)
	require.NoError(t, store.Record(ctx, LiveRequest{
		RequestID: requestID, Ts: newer.Format(time.RFC3339Nano),
		TenantID: "tenant-a", Model: "gpt-4o", ModelCategory: "openai",
		ProviderCode: "openai", Status: "success",
	}, ""))
	// Late-arriving update with an OLDER timestamp.
	require.NoError(t, store.Record(ctx, LiveRequest{
		RequestID: requestID, Ts: older.Format(time.RFC3339Nano),
		TenantID: "tenant-a", Model: "gpt-4o", ModelCategory: "openai",
		ProviderCode: "openai", Status: "success",
	}, ""))

	// The score in the main queue must equal the newer timestamp (ms).
	score, err := rdb.ZScore(ctx, liveStreamMainKey, requestID).Result()
	require.NoError(t, err)
	assert.InDelta(t, float64(newer.UnixMilli()), score, 1500, // allow small parse drift
		"late update must not move request backwards in the queue")

	// Lock key should be cleaned up (released) after Record returns.
	assert.False(t, mr.Exists(liveStreamRecordLockKey(requestID)),
		"per-request lock key should be released after Record")
}

// TestRecord_TrimsToLaneLimitKeepingNewest: 21 distinct requests to one lane
// must leave 20 members, and the dropped one must be the oldest.
func TestRecord_TrimsToLaneLimitKeepingNewest(t *testing.T) {
	store, _, rdb := setupLiveStreamStore(t)
	ctx := context.Background()
	base := time.Now().UTC().Add(-30 * time.Second)

	for i := 0; i < LiveStreamLaneVisibleLimit+1; i++ {
		require.NoError(t, store.Record(ctx, LiveRequest{
			RequestID: fmt.Sprintf("req-trim-%d", i),
			Ts:        base.Add(time.Duration(i) * time.Second).Format(time.RFC3339Nano),
			TenantID:  "tenant-a", Model: "gpt-4o", ModelCategory: "openai",
			ProviderCode: "openai", Status: "success",
		}, ""))
	}

	vendorKey := liveStreamDimPrefix + "vendor:openai"
	card, err := rdb.ZCard(ctx, vendorKey).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(LiveStreamLaneVisibleLimit), card,
		"vendor queue must be trimmed to the lane limit")

	// Oldest (req-trim-0) must have been evicted; newest (req-trim-20) present.
	assert.Equal(t, 0, countMembersForRequest(t, rdb, vendorKey, "req-trim-0"),
		"oldest request should have been trimmed")
	assert.Equal(t, 1, countMembersForRequest(t, rdb, vendorKey, "req-trim-20"),
		"newest request should be retained")
}

// TestRecord_DimensionFiltering: dimension queues are created only when the
// dimension resolves to a non-empty value. A bare request (no model/provider/
// category) still resolves vendor through the full fallback chain
// (resolveVendorForRequest returns "其他" as the last resort), so the vendor
// queue IS created — but provider and model queues are NOT (those require
// explicit non-empty values). This documents the actual, intended behavior.
func TestRecord_DimensionFiltering(t *testing.T) {
	store, mr, _ := setupLiveStreamStore(t)
	ctx := context.Background()
	require.NoError(t, store.Record(ctx, LiveRequest{
		RequestID: "req-bare-1", Ts: time.Now().UTC().Format(time.RFC3339Nano),
		TenantID: "tenant-a", Status: "success",
		// Model, ModelCategory, ProviderCode all empty
	}, ""))

	keys := mr.Keys()
	hasProviderQueue := false
	hasModelQueue := false
	for _, k := range keys {
		if strings.HasPrefix(k, liveStreamDimPrefix+"provider:") {
			hasProviderQueue = true
		}
		if strings.HasPrefix(k, liveStreamDimPrefix+"model:") {
			hasModelQueue = true
		}
	}
	assert.False(t, hasProviderQueue, "no provider queue should be created without a provider_code")
	assert.False(t, hasModelQueue, "no model queue should be created without a model name")
	// Vendor fallback chain yields "其他" for a fully-bare request, so a vendor
	// queue IS expected — this is by design (never show "unknown"/"other" as
	// empty; the lane still aggregates).
	assert.True(t, containsPrefix(keys, liveStreamDimPrefix+"vendor:"),
		"vendor queue should exist via the fallback chain for a bare request")
}

func containsPrefix(keys []string, prefix string) bool {
	for _, k := range keys {
		if strings.HasPrefix(k, prefix) {
			return true
		}
	}
	return false
}

// TestRecord_TenantIsolation: a tenant-a request must not appear in tenant-b's
// queues.
func TestRecord_TenantIsolation(t *testing.T) {
	store, _, rdb := setupLiveStreamStore(t)
	ctx := context.Background()
	require.NoError(t, store.Record(ctx, LiveRequest{
		RequestID: "req-iso-1", Ts: time.Now().UTC().Format(time.RFC3339Nano),
		TenantID: "tenant-a", Model: "gpt-4o", ModelCategory: "openai",
		ProviderCode: "openai", Status: "success",
	}, ""))

	bVendorKey := tenantLiveStreamKey("tenant-b", "dim:vendor:openai")
	assert.Equal(t, 0, countMembersForRequest(t, rdb, bVendorKey, "req-iso-1"),
		"tenant-a request must not appear in tenant-b queues")
	bMain := tenantLiveStreamKey("tenant-b", "main")
	assert.Equal(t, 0, countMembersForRequest(t, rdb, bMain, "req-iso-1"),
		"tenant-a request must not appear in tenant-b main queue")
}
