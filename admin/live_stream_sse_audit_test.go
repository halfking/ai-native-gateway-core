package admin

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestLiveStreamSSEHub_ComputeScopeDeltaPreservesBaselineAcrossEmptyRead(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	hub := NewLiveStreamSSEHub(nil, LiveStreamConfig{RedisClient: rdb})
	ctx := context.Background()
	first := LiveRequest{
		RequestID:     "request-a",
		Ts:            time.Now().UTC().Format(time.RFC3339),
		Model:         "model-a",
		ModelCategory: "vendor-a",
		ProviderCode:  "provider-a",
		Status:        "success",
	}
	if err := hub.store.Record(ctx, first); err != nil {
		t.Fatalf("record first request: %v", err)
	}

	hub.computeScopeDelta(ctx, "", false)

	scope := newLiveStreamScope("default", false)
	hub.cachedSnapshotMu.RLock()
	entryBeforeEmptyRead := hub.cachedSnapshot[scope.cacheKey]
	hub.cachedSnapshotMu.RUnlock()
	if entryBeforeEmptyRead == nil || entryBeforeEmptyRead.snapshot.Summary.Total != 1 {
		t.Fatalf("expected default scope baseline with request-a, got %#v", entryBeforeEmptyRead)
	}
	accessedBeforeEmptyRead := entryBeforeEmptyRead.lastAccessed

	if err := rdb.Del(ctx, tenantLiveStreamKey("default", "main")).Err(); err != nil {
		t.Fatalf("delete tenant main queue: %v", err)
	}
	if delta := hub.computeScopeDelta(ctx, "default", false); delta != nil {
		t.Fatalf("empty Redis read should not emit a delta, got %#v", delta)
	}

	hub.cachedSnapshotMu.RLock()
	entryAfterEmptyRead := hub.cachedSnapshot[scope.cacheKey]
	entryCount := len(hub.cachedSnapshot)
	hub.cachedSnapshotMu.RUnlock()
	if entryAfterEmptyRead == nil || entryAfterEmptyRead.snapshot.Summary.Total != 1 {
		t.Fatalf("empty read must retain the request-a baseline, got %#v", entryAfterEmptyRead)
	}
	if !entryAfterEmptyRead.lastAccessed.After(accessedBeforeEmptyRead) {
		t.Fatalf("empty read should refresh baseline access time: before=%v after=%v", accessedBeforeEmptyRead, entryAfterEmptyRead.lastAccessed)
	}
	if entryCount != 1 {
		t.Fatalf("empty tenant ID and default tenant ID must share one cache entry, got %d", entryCount)
	}

	second := LiveRequest{
		RequestID:     "request-b",
		Ts:            time.Now().UTC().Add(time.Second).Format(time.RFC3339),
		TenantID:      "default",
		Model:         "model-b",
		ModelCategory: "vendor-b",
		ProviderCode:  "provider-b",
		Status:        "success",
	}
	if err := hub.store.Record(ctx, second); err != nil {
		t.Fatalf("record second request: %v", err)
	}
	if delta := hub.computeScopeDelta(ctx, "", false); delta == nil || delta.Summary.Total != 1 {
		t.Fatalf("second request should evolve the retained baseline, got %#v", delta)
	}

	hub.cachedSnapshotMu.RLock()
	entryAfterSecondRequest := hub.cachedSnapshot[scope.cacheKey]
	hub.cachedSnapshotMu.RUnlock()
	if entryAfterSecondRequest == nil || entryAfterSecondRequest.snapshot.Summary.Total != 1 {
		t.Fatalf("expected request-b snapshot after recovery, got %#v", entryAfterSecondRequest)
	}
	vendorLanes := entryAfterSecondRequest.snapshot.Dimensions["vendor"]
	if len(vendorLanes) != 1 || len(vendorLanes[0].Requests) != 1 || vendorLanes[0].Requests[0].RequestID != second.RequestID {
		t.Fatalf("expected retained baseline to advance to request-b, got %#v", vendorLanes)
	}
}

func TestLiveStreamSSEHub_RunKeepsSubscribedScopeBaseline(t *testing.T) {
	hub := NewLiveStreamSSEHub(nil, LiveStreamConfig{
		IdleTickInterval:              time.Hour,
		KeepaliveInterval:             time.Hour,
		CachedSnapshotTTL:             20 * time.Millisecond,
		CachedSnapshotCleanupInterval: 5 * time.Millisecond,
	})
	done := make(chan struct{})
	go func() {
		hub.Run()
		close(done)
	}()
	t.Cleanup(func() {
		hub.Stop()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("hub Run did not stop")
		}
	})

	client := &liveStreamClient{tenantID: "tenant-active"}
	hub.register <- client
	waitForLiveStreamCondition(t, time.Second, func() bool {
		stats := hub.Stats()
		return stats["active_scope_subscriptions"] == 1
	})

	scope := newLiveStreamScope("tenant-active", false)
	hub.cachedSnapshotMu.Lock()
	hub.cachedSnapshot[scope.cacheKey] = &cachedSnapshotEntry{
		snapshot:     &LiveStreamSnapshot{},
		lastAccessed: time.Now().Add(-time.Hour),
	}
	hub.cachedSnapshotMu.Unlock()
	waitForLiveStreamCondition(t, 100*time.Millisecond, func() bool {
		hub.cachedSnapshotMu.RLock()
		defer hub.cachedSnapshotMu.RUnlock()
		_, exists := hub.cachedSnapshot[scope.cacheKey]
		return exists
	})

	hub.unregister <- client
	waitForLiveStreamCondition(t, time.Second, func() bool {
		stats := hub.Stats()
		return stats["active_scope_subscriptions"] == 0
	})
	waitForLiveStreamCondition(t, time.Second, func() bool {
		hub.cachedSnapshotMu.RLock()
		defer hub.cachedSnapshotMu.RUnlock()
		_, exists := hub.cachedSnapshot[scope.cacheKey]
		return !exists
	})
}

func waitForLiveStreamCondition(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition was not met before timeout")
}
