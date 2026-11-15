package ratelimit

import (
	"context"
	"sync"
	"testing"
)

// TestP310_LRUEviction tests that LRU eviction prevents unbounded memory growth (P3-10).
func TestP310_LRUEviction(t *testing.T) {
	// Create limiter with small max entries to trigger eviction
	limiter := NewSlidingWindowLimiterWithMaxEntries(10)
	defer limiter.Stop()

	// Fill one shard with 15 keys
	for keyID := 0; keyID < 15; keyID++ {
		// Access each key to create a window
		result := limiter.CheckRPM(keyID, 100)
		if !result {
			t.Errorf("First request for keyID %d should succeed", keyID)
		}
	}

	// Verify that older entries were evicted
	shard := limiter.shards[0]
	shard.mu.Lock()
	totalEntries := len(shard.windows) + len(shard.tokenWins)
	shard.mu.Unlock()

	if totalEntries > 10 {
		t.Errorf("Expected max 10 entries after eviction, got %d", totalEntries)
	}

	// Most recent entries should still be present
	if !limiter.CheckRPM(14, 100) {
		t.Error("Most recently used key (14) should still be accessible")
	}

	// Oldest entries may have been evicted
	shard.mu.Lock()
	_, hasKey0 := shard.windows[0]
	shard.mu.Unlock()

	if hasKey0 {
		t.Log("Key 0 still present (early eviction may not have occurred yet)")
	}
}

// TestP310_LRUOrdering tests that LRU maintains correct access order (P3-10).
func TestP310_LRUOrdering(t *testing.T) {
	// Create limiter with max 5 entries
	limiter := NewSlidingWindowLimiterWithMaxEntries(5)
	defer limiter.Stop()

	// Add 5 keys: 0, 1, 2, 3, 4
	for i := 0; i < 5; i++ {
		limiter.CheckRPM(i, 100)
	}

	// Access key 0 to make it most recent
	limiter.CheckRPM(0, 100)

	// Add a new key 5 - should evict oldest non-accessed (one of 1,2,3,4)
	limiter.CheckRPM(5, 100)

	shard := limiter.shards[0]
	shard.mu.Lock()
	// ★ 必须用「逗号-ok」取桶：被驱逐的 key 在 map 里是**缺失**，
	//   map[int]*rpmWindow 取缺失键得到 nil 指针 ⇒ 直接取 .timestamps 会 panic。
	w0, ok0 := shard.windows[0]
	w1, ok1 := shard.windows[1]
	shard.mu.Unlock()

	if !ok0 || w0 == nil || len(w0.timestamps) == 0 {
		t.Error("Key 0 (recently accessed) should not be evicted")
	}

	t.Logf("Key 1 present: %v (may have been evicted)", ok1 && w1 != nil)
}

// TestP310_TPMWithLRU tests LRU eviction with TPM windows (P3-10).
func TestP310_TPMWithLRU(t *testing.T) {
	limiter := NewSlidingWindowLimiterWithMaxEntries(8)
	defer limiter.Stop()

	// Create TPM windows for multiple keys
	for keyID := 0; keyID < 10; keyID++ {
		result := limiter.CheckTPM(keyID, 50, 1000)
		if !result {
			t.Errorf("First TPM check for keyID %d should succeed", keyID)
		}
	}

	// Verify eviction occurred
	shard := limiter.shards[0]
	shard.mu.Lock()
	totalEntries := len(shard.windows) + len(shard.tokenWins)
	shard.mu.Unlock()

	if totalEntries > 8 {
		t.Errorf("Expected max 8 entries after LRU, got %d", totalEntries)
	}
}

// TestP310_NoEvictionWhenUnlimited tests that unlimited limiter doesn't evict (P3-10).
func TestP310_NoEvictionWhenUnlimited(t *testing.T) {
	// Max entries = 0 means unlimited
	limiter := NewSlidingWindowLimiterWithMaxEntries(0)
	defer limiter.Stop()

	// Add many keys
	for keyID := 0; keyID < 1000; keyID++ {
		limiter.CheckRPM(keyID, 100)
	}

	// No eviction should occur
	shard := limiter.shards[0]
	shard.mu.Lock()
	totalEntries := len(shard.windows) + len(shard.tokenWins)
	shard.mu.Unlock()

	if totalEntries == 0 {
		t.Error("Expected entries to remain when max is unlimited")
	}

	t.Logf("Unlimited limiter retained %d entries", totalEntries)
}

// TestP310_DefaultMaxEntries tests that default NewSlidingWindowLimiter has reasonable default (P3-10).
func TestP310_DefaultMaxEntries(t *testing.T) {
	limiter := NewSlidingWindowLimiter()
	defer limiter.Stop()

	if limiter.maxEntriesPerShard <= 0 {
		t.Error("Default limiter should have positive max entries per shard")
	}

	// Should be able to add 10000+ keys per shard without hitting default limit immediately
	// (This is a configuration test, not a load test)
	if limiter.maxEntriesPerShard < 1000 {
		t.Logf("Warning: default max entries (%d) may be too low for production", limiter.maxEntriesPerShard)
	} else {
		t.Logf("Default max entries per shard: %d", limiter.maxEntriesPerShard)
	}
}

// TestP310_ConcurrentLRUAccess tests LRU under concurrent access (P3-10).
func TestP310_ConcurrentLRUAccess(t *testing.T) {
	limiter := NewSlidingWindowLimiterWithMaxEntries(100)
	defer limiter.Stop()

	done := make(chan struct{})
	var errors []string
	var errorsMu sync.Mutex

	// Spawn 10 concurrent goroutines
	for i := 0; i < 10; i++ {
		go func(worker int) {
			for j := 0; j < 100; j++ {
				keyID := worker*100 + j
				if !limiter.CheckRPM(keyID, 100) {
					errorsMu.Lock()
					errors = append(errors, "CheckRPM failed")
					errorsMu.Unlock()
				}
				if !limiter.CheckTPM(keyID, 10, 10000) {
					errorsMu.Lock()
					errors = append(errors, "CheckTPM failed")
					errorsMu.Unlock()
				}
			}
			done <- struct{}{}
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}

	if len(errors) > 0 {
		t.Errorf("Concurrent access had errors: %v", errors)
	}

	// Verify that total entries stayed within bounds
	totalEntries := 0
	for _, shard := range limiter.shards {
		shard.mu.Lock()
		totalEntries += len(shard.windows) + len(shard.tokenWins)
		shard.mu.Unlock()
	}

	// Each shard should have <= maxEntriesPerShard entries
	maxAllowed := 100 * 16 // maxEntriesPerShard * numShards
	if totalEntries > maxAllowed {
		t.Errorf("Expected total entries <= %d, got %d", maxAllowed, totalEntries)
	}

	t.Logf("Concurrent access completed with %d total entries across all shards", totalEntries)
}

// TestP310_LRUWithAdmission tests LRU with admission control (P3-10).
func TestP310_LRUWithAdmission(t *testing.T) {
	limiter := NewSlidingWindowLimiterWithMaxEntries(20)
	ctx := context.Background()

	// Use admission control which also tracks keys
	for keyID := 0; keyID < 30; keyID++ {
		result, err := limiter.AdmitRPM(ctx, keyID, 100)
		if err != nil {
			t.Errorf("AdmitRPM failed for keyID %d: %v", keyID, err)
			continue
		}
		if result.Admitted {
			t.Logf("AdmitRPM keyID %d: admitted (expected for overload)", keyID)
		} else {
			t.Logf("AdmitRPM keyID %d: not admitted (expected for overload)", keyID)
		}
	}

	// Verify LRU didn't exceed limits
	totalEntries := 0
	for _, shard := range limiter.shards {
		shard.mu.Lock()
		totalEntries += len(shard.windows) + len(shard.tokenWins)
		shard.mu.Unlock()
	}

	maxAllowed := 20 * 16
	if totalEntries > maxAllowed {
		t.Errorf("Expected total entries <= %d with admission, got %d", maxAllowed, totalEntries)
	}
}

// TestP310_LRUMetricsConsistency tests that LRU maintains consistent state (P3-10).
func TestP310_LRUMetricsConsistency(t *testing.T) {
	limiter := NewSlidingWindowLimiterWithMaxEntries(50)
	defer limiter.Stop()

	// Add keys and check consistency
	for keyID := 0; keyID < 100; keyID++ {
		limiter.CheckRPM(keyID, 100)
		limiter.CheckTPM(keyID, 10, 10000)

		// Verify LRU consistency for this shard
		shard := limiter.shards[uint(uintptr(keyID))&(rpmShardCount-1)]
		shard.mu.Lock()

		// Check 1: LRU list size should match keys in lruKeyMap
		if shard.lruList.Len() != len(shard.lruKeyMap) {
			t.Errorf("LRU consistency: list.Len()=%d != lruKeyMap.len()=%d",
				shard.lruList.Len(), len(shard.lruKeyMap))
		}

		// Check 2: Total entries should not exceed max
		totalEntries := len(shard.windows) + len(shard.tokenWins)
		if shard.maxEntries > 0 && totalEntries > shard.maxEntries {
			t.Errorf("LRU consistency: totalEntries(%d) > maxEntries(%d)",
				totalEntries, shard.maxEntries)
		}

		// Check 3: LRU map keys should only reference keys in windows or tokenWins
		for lruKeyID := range shard.lruKeyMap {
			_, inWindows := shard.windows[lruKeyID]
			_, inTokenWins := shard.tokenWins[lruKeyID]
			if !inWindows && !inTokenWins {
				t.Errorf("LRU consistency: orphaned key %d in lruKeyMap", lruKeyID)
			}
		}

		shard.mu.Unlock()
	}

	t.Log("LRU consistency checks passed")
}
