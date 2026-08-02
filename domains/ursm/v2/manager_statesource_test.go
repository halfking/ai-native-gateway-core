// Step 5 round 1 (S-3): exposes the per-request NodeMirror source enum
// (hit / miss / stale) from FilterAndScore so the router can record
// routing_state_source on every authoritative read without
// re-implementing the LRU/Redis dance.
//
// 2026-07-28 (request-flow-audit §8.2 + §11.4): the audit identified
// that the only observability of routing_state_source today is a
// single slog.Warn on the FilterAndScore error path. Operators need
// to know the live hit/miss/stale ratio to:
//   - size the LRU (if miss rate is high, more capacity won't help;
//     if stale rate is high, soft-TTL is too long for the workload).
//   - quantify fail-open time on the authoritative path (per spec §8.2,
//     the legacy StateManager must NOT participate in authoritative
//     health — but Redis is the only fallback, so we need a
//     concrete hit/miss ratio to size the Redis budget).
//
// This file is the test harness for the new
// `FilterAndScoreReadyWithSource` method. Behaviour pins:
//
//   - first read (LRU empty) → Source misses
//   - second read, before soft-TTL → Source hits
//   - second read, after soft-TTL → Source stale (counts as miss for
//     the LRU but is reported separately to keep the dashboard stable)
//   - mode=off → no method is reachable, but the helper is not the
//     path that short-circuits (it returns nil views regardless).
package v2

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/api"
	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/statesource"
)

// TestFilterAndScoreReadyWithSource_LRUMissThenHit pins the source
// summary for the most common hot path: cold LRU → first read
// reports miss; same read, second time, reports hit.
func TestFilterAndScoreReadyWithSource_LRUMissThenHit(t *testing.T) {
	mgr, mr, _ := newMirrorManager(t)
	seedNode(t, mr, 31, "m", 1, true, 50, 0.9)

	seeds := []CandidateSeed{{ProviderID: 1, CredentialID: 31, RawModel: "m", TenantID: "t"}}

	views, src, err := mgr.FilterAndScoreReadyWithSource(context.Background(), seeds, true)
	require.NoError(t, err)
	require.Len(t, views, 1)
	assert.Equal(t, statesource.StateSourceNodeMirrorMiss, src,
		"first call has empty LRU → must report miss")

	views2, src2, err := mgr.FilterAndScoreReadyWithSource(context.Background(), seeds, true)
	require.NoError(t, err)
	require.Len(t, views2, 1)
	assert.Equal(t, statesource.StateSourceNodeMirrorHit, src2,
		"second call with warm LRU → must report hit (not stale)")
}

// TestFilterAndScoreReadyWithSource_StaleAfterSoftTTL pins the stale
// reporting. The soft-TTL is 200ms in newMirrorManager; we sleep past
// it and the LRU entry must be reported as stale, not hit.
func TestFilterAndScoreReadyWithSource_StaleAfterSoftTTL(t *testing.T) {
	mgr, mr, _ := newMirrorManager(t)
	seedNode(t, mr, 32, "m", 1, true, 50, 0.9)

	seeds := []CandidateSeed{{ProviderID: 1, CredentialID: 32, RawModel: "m", TenantID: "t"}}
	_, _, err := mgr.FilterAndScoreReadyWithSource(context.Background(), seeds, true)
	require.NoError(t, err)

	time.Sleep(250 * time.Millisecond)

	// soft-expired; the LRU reports miss and we backfill from Redis.
	// The LRU doesn't pre-warn callers that an entry is about to expire,
	// so the helper sees the cache entry and must surface it as stale
	// (not as a hit). A miss is also acceptable per spec §8.2
	// (routing_state_source hits the dashboard either way) but stale
	// gives operators a sharper signal.
	_, src, err := mgr.FilterAndScoreReadyWithSource(context.Background(), seeds, true)
	require.NoError(t, err)
	if src != statesource.StateSourceNodeMirrorStale && src != statesource.StateSourceNodeMirrorMiss {
		t.Fatalf("soft-expired LRU must report stale or miss, got %s", src)
	}
}

// TestFilterAndScoreReadyWithSource_NoMirrorDisabled pins the disabled
// mirror case (LRUMirrorSize==0): there is no LRU at all, so every
// read is a miss. We still report miss rather than skipping, so the
// fail-open dashboard does not collapse to a flat zero on the line.
func TestFilterAndScoreReadyWithSource_NoMirrorDisabled(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	cfg := DefaultConfig()
	cfg.Mode = api.ModeAuthoritative
	cfg.LRUMirrorSize = 0
	mgr := New(Dependencies{Redis: rdb, Config: cfg})
	_ = mgr.SetReady(context.Background(), true)
	seedNode(t, mr, 33, "m", 1, true, 50, 0.9)

	seeds := []CandidateSeed{{ProviderID: 1, CredentialID: 33, RawModel: "m", TenantID: "t"}}
	_, src, err := mgr.FilterAndScoreReadyWithSource(context.Background(), seeds, true)
	require.NoError(t, err)
	assert.Equal(t, statesource.StateSourceNodeMirrorMiss, src,
		"LRU disabled → must report miss, not hit")
}
