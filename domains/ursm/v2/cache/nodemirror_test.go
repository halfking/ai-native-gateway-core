package cache

import (
	"sync"
	"testing"
	"time"
)

func TestNodeMirrorNewerGenerationWins(t *testing.T) {
	m := NewNodeMirror(100, 30*time.Second)
	old := NodeView{CredentialID: 1, RawModel: "m", Generation: 5, SourcePriority: 10, Available: true}
	m.applyToLRU(old)
	// 迟到的旧 gen 不应覆盖
	stale := NodeView{CredentialID: 1, RawModel: "m", Generation: 4, SourcePriority: 20, Available: false}
	m.applyToLRU(stale)
	got, ok := m.Peek(1, "m")
	if !ok || !got.Available {
		t.Fatalf("stale gen overwrote newer: %+v ok=%v", got, ok)
	}
	if got.Generation != 5 {
		t.Fatalf("gen regressed to %d", got.Generation)
	}
	// 同 gen 但更高 priority 应覆盖
	newer := NodeView{CredentialID: 1, RawModel: "m", Generation: 5, SourcePriority: 30, Available: false}
	m.applyToLRU(newer)
	got, _ = m.Peek(1, "m")
	if got.Available {
		t.Fatalf("same-gen higher-pri did not overwrite: %+v", got)
	}
}

func TestNodeMirrorSoftExpiry(t *testing.T) {
	m := NewNodeMirror(100, 10*time.Millisecond)
	m.applyToLRU(NodeView{CredentialID: 1, RawModel: "m", Generation: 1, Available: true})
	if _, ok := m.Get(1, "m"); !ok {
		t.Fatal("fresh entry should hit")
	}
	time.Sleep(20 * time.Millisecond)
	if _, ok := m.Get(1, "m"); ok {
		t.Fatal("soft-expired entry should miss")
	}
}

func TestNodeMirrorConcurrentApplyNoRegress(t *testing.T) {
	// 并发写入不同 generation, 最终应为最大 gen, Available 跟随最大 gen 的值
	m := NewNodeMirror(100, time.Minute)
	done := make(chan struct{})
	for g := 0; g < 20; g++ {
		go func(gen int64) {
			m.applyToLRU(NodeView{
				CredentialID: 1, RawModel: "m",
				Generation:     gen,
				SourcePriority: 10,
				Available:      gen%2 == 0,
			})
			done <- struct{}{}
		}(int64(g))
	}
	for g := 0; g < 20; g++ {
		<-done
	}
	got, ok := m.Peek(1, "m")
	if !ok {
		t.Fatal("no entry after concurrent writes")
	}
	if got.Generation != 19 {
		t.Fatalf("expected max gen 19, got %d", got.Generation)
	}
	// gen 19 is odd → Available false
	if got.Available {
		t.Fatalf("gen 19 should be Available=false, got true")
	}
}

func TestNodeMirrorEqualGenPriAccepts(t *testing.T) {
	// 与 apply_decision.lua:25 对齐: 相等 (gen, pri) 时接受幂等刷新
	// (lua 用 cur_pri > in_pri 拒绝, 即仅严格大于才拒; 相等 = apply)
	m := NewNodeMirror(100, time.Second)
	first := NodeView{CredentialID: 1, RawModel: "m", Generation: 5, SourcePriority: 10, Available: true, Reason: "old"}
	m.applyToLRU(first)

	// 同 gen 同 pri, 不同 Reason — 应被接受(幂等刷新)
	refresh := NodeView{CredentialID: 1, RawModel: "m", Generation: 5, SourcePriority: 10, Available: true, Reason: "refreshed"}
	m.applyToLRU(refresh)
	got, ok := m.Peek(1, "m")
	if !ok {
		t.Fatal("entry missing after equal-(gen,pri) apply")
	}
	if got.Reason != "refreshed" {
		t.Fatalf("equal-(gen,pri) apply should refresh; Reason=%q want %q", got.Reason, "refreshed")
	}
}

// TestNodeMirrorShardedConcurrentWrites is the M3 (2026-07-28) regression
// test for the per-shard LRU split. It exercises:
//   - many goroutines writing DIFFERENT (cred, raw) keys (the FilterAndScore
//     hot-path shape): they MUST all complete without deadlock or race
//   - the same goroutines ALSO hammering ApplyFromAPI concurrently with
//     reads via Get / Peek — the shard-level mutex MUST not panic, must not
//     interleave a get with itself, and must respect soft-expire
//
// A pre-M3 (single mutex) version of this test would still pass for small N,
// but the run time scales poorly with contention; the sharded variant keeps
// throughput steady as N grows because each shard's lock is independent.
func TestNodeMirrorShardedConcurrentWrites(t *testing.T) {
	const goroutines = 32
	const writes = 100
	// Per-shard capacity must comfortably exceed total/N so the LRU does
	// not evict keys we still need to assert at the end. 1024 per shard
	// gives plenty of headroom without slowing the test.
	m := NewNodeMirror(NodeMirrorShards*1024, time.Minute)

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(off int) {
			defer wg.Done()
			for i := 0; i < writes; i++ {
				credID := off*writes + i
				v := NodeView{
					CredentialID:   credID,
					RawModel:       "m",
					Available:      true,
					Generation:     int64(i + 1),
					SourcePriority: 10,
				}
				m.applyToLRU(v)
				if _, ok := m.Get(credID, "m"); !ok {
					t.Errorf("Get miss immediately after Apply for key=%d", credID)
				}
				if _, ok := m.Peek(credID, "m"); !ok {
					t.Errorf("Peek miss for key=%d", credID)
				}
			}
		}(g)
	}
	wg.Wait()

	// Every distinct (cred, raw) we wrote should still be Peek-able.
	for g := 0; g < goroutines; g++ {
		for i := 0; i < writes; i++ {
			credID := g*writes + i
			if _, ok := m.Peek(credID, "m"); !ok {
				t.Fatalf("key %d missing after concurrent writes", credID)
			}
		}
	}
}

// TestNodeMirrorShardedGenMonotonicPerKey exercises the per-shard CAS in
// applyToLRU when many goroutines race to update the SAME (cred, raw).
// Even though the shards split unrelated keys, a single key's updates are
// strictly serialised by its shard's mutex, so generation MUST remain
// monotonic — never regress — under racy concurrent calls.
//
// Without the per-shard serialisation, a slow writer holding a stale gen
// could land AFTER a faster writer holding a newer gen, leaving a regressed
// entry in the cache and breaking the lua apply_decision contract.
func TestNodeMirrorShardedGenMonotonicPerKey(t *testing.T) {
	m := NewNodeMirror(100, time.Minute)
	const goroutines = 32
	const writes = 200

	// Seed the key at a high generation so every concurrent writer sees an
	// existing entry and must respect the monotonic contract.
	m.applyToLRU(NodeView{
		CredentialID:   7,
		RawModel:       "racey",
		Generation:     int64(writes + 1),
		SourcePriority: 10,
		Available:      true,
	})

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(off int) {
			defer wg.Done()
			for i := 0; i < writes; i++ {
				// Write monotonically decreasing gen — every iteration is
				// strictly older than the seed, so all of these MUST be
				// rejected by applyToLRU's per-shard CAS.
				v := NodeView{
					CredentialID:   7,
					RawModel:       "racey",
					Generation:     int64(writes - i),
					SourcePriority: 10,
					Available:      true,
				}
				m.applyToLRU(v)
			}
		}(g)
	}
	wg.Wait()

	// The original seed must still be there with its original generation —
	// every concurrent caller wrote a strictly older gen and was rejected.
	got, ok := m.Peek(7, "racey")
	if !ok {
		t.Fatal("seed entry evicted by concurrent older-gen writers")
	}
	if got.Generation != int64(writes+1) {
		t.Fatalf("gen regressed: seed had gen=%d, after writes got %d", writes+1, got.Generation)
	}
}
