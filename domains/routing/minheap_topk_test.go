package routing

import (
	"math/rand"
	"sort"
	"sync"
	"testing"
)

// TestTopK_BasicRetainsLargest verifies the heap keeps the K
// largest weights and drops smaller ones.
func TestTopK_BasicRetainsLargest(t *testing.T) {
	h := NewTopK[int](3)
	for i, w := range []float64{0.1, 0.9, 0.5, 0.7, 0.2, 1.0} {
		h.Push(i, w)
	}
	if h.Len() != 3 {
		t.Fatalf("expected len=3, got %d", h.Len())
	}
	items := h.SortedDesc()
	// The 3 largest weights are 1.0, 0.9, 0.7; the corresponding
	// items were pushed with indexes 5, 1, 3.
	wantItems := []int{5, 1, 3}
	for i, w := range wantItems {
		if items[i] != w {
			t.Fatalf("SortedDesc[%d] = %d, want %d (full=%v)", i, items[i], w, items)
		}
	}
}

// TestTopK_ZeroCapIsSafe ensures callers can pass a "user top-N"
// value of 0 without panicking. The heap normalises 0→1 (one
// item is always retained so callers can still observe a non-nil
// result for "give me your best, even if I asked for nothing"),
// which is a more useful contract than a silent no-op.
func TestTopK_ZeroCapIsSafe(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("zero cap must not panic: %v", r)
		}
	}()
	h := NewTopK[int](0)
	if h.Cap() != 1 {
		t.Fatalf("zero-cap heap normalises to 1; got %d", h.Cap())
	}
	h.Push(1, 0.5)
	if h.Len() != 1 {
		t.Fatalf("zero-cap heap normalises to 1; got len=%d", h.Len())
	}
}

// TestTopK_NaNDropped verifies NaN weights never evict a real entry.
func TestTopK_NaNDropped(t *testing.T) {
	h := NewTopK[int](2)
	h.Push(1, 0.5)
	h.Push(2, 0.7)
	// NaN must not evict.
	h.Push(99, NaN())
	// Smaller real weight must not evict.
	h.Push(98, 0.1)
	items := h.SortedDesc()
	if len(items) != 2 {
		t.Fatalf("NaN/small must not evict: got %v", items)
	}
	// The two largest real weights are 0.7 and 0.5.
	if items[0] != 2 || items[1] != 1 {
		t.Fatalf("expected [2,1], got %v", items)
	}
}

// TestTopK_ResetClearsButPreservesCap verifies Reset is in-place
// and a second use cycle still respects the cap.
func TestTopK_ResetClearsButPreservesCap(t *testing.T) {
	h := NewTopK[int](2)
	h.Push(1, 0.5)
	h.Push(2, 0.9)
	h.Reset()
	if h.Len() != 0 {
		t.Fatalf("Reset must clear; len=%d", h.Len())
	}
	if h.Cap() != 2 {
		t.Fatalf("Reset must preserve cap; got %d", h.Cap())
	}
	h.Push(10, 0.1)
	h.Push(20, 0.2)
	h.Push(30, 0.3)
	if h.Len() != 2 {
		t.Fatalf("post-reset cap must still be 2; got %d", h.Len())
	}
}

// TestTopK_SortedDescStableOrder verifies ties break in stable
// (insertion) order. This matters for tests/local/gateway which
// expects deterministic credential ordering on tied weights.
func TestTopK_SortedDescStableOrder(t *testing.T) {
	h := NewTopK[int](3)
	// Three entries with the same weight; insertion order: 7, 3, 9.
	h.Push(7, 1.0)
	h.Push(3, 1.0)
	h.Push(9, 1.0)
	got := h.SortedDesc()
	want := []int{7, 3, 9}
	for i, v := range want {
		if got[i] != v {
			t.Fatalf("stable order broken at %d: got %v want %v", i, got, want)
		}
	}
}

// TestTopK_SortedDescWithWeightsAlignsPairs checks the dual sort
// keeps items and weights in lock-step.
func TestTopK_SortedDescWithWeightsAlignsPairs(t *testing.T) {
	h := NewTopK[int](4)
	pairs := []struct {
		v int
		w float64
	}{{1, 0.2}, {2, 0.8}, {3, 0.5}, {4, 0.1}, {5, 1.0}}
	for _, p := range pairs {
		h.Push(p.v, p.w)
	}
	items, weights := h.SortedDescWithWeights()
	if len(items) != 4 || len(weights) != 4 {
		t.Fatalf("len mismatch: items=%d weights=%d", len(items), len(weights))
	}
	// Expected top-4 in descending order: 5 (1.0), 2 (0.8), 3 (0.5), 1 (0.2).
	wantItems := []int{5, 2, 3, 1}
	wantWeights := []float64{1.0, 0.8, 0.5, 0.2}
	for i := range wantItems {
		if items[i] != wantItems[i] {
			t.Fatalf("items[%d]=%d want %d", i, items[i], wantItems[i])
		}
		if weights[i] != wantWeights[i] {
			t.Fatalf("weights[%d]=%v want %v", i, weights[i], wantWeights[i])
		}
	}
}

// TestSelectTopKWeighted_MatchesReferenceSort is the equivalence
// check against the legacy selection-sort output: for random
// inputs the heap must return the same K-largest candidates in
// the same descending order.
func TestSelectTopKWeighted_MatchesReferenceSort(t *testing.T) {
	rng := rand.New(rand.NewSource(20260921))
	const N = 200
	candidates := make([]*Candidate, N)
	weights := make([]float64, N)
	for i := 0; i < N; i++ {
		candidates[i] = &Candidate{CredentialID: idFor(i), Provider: "p", Model: "m"}
		weights[i] = rng.Float64()
	}
	for _, k := range []int{1, 5, 8, 16, 32, 100} {
		got := SelectTopKWeighted(candidates, weights, k)
		// Reference: sort indices by weight desc, slice top-k.
		idx := make([]int, N)
		for i := range idx {
			idx[i] = i
		}
		sort.SliceStable(idx, func(i, j int) bool { return weights[idx[i]] > weights[idx[j]] })
		want := make([]*Candidate, k)
		for i := range want {
			want[i] = candidates[idx[i]]
		}
		if len(got) != k {
			t.Fatalf("k=%d len(got)=%d", k, len(got))
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("k=%d i=%d got=%s want=%s", k, i,
					got[i].CredentialID, want[i].CredentialID)
			}
		}
	}
}

// TestSelectTopKWeighted_KBeyondSlice panics: if k > len it must
// clamp to len, not panic.
func TestSelectTopKWeighted_KBeyondSlice(t *testing.T) {
	cs := []*Candidate{{CredentialID: "a"}, {CredentialID: "b"}}
	ws := []float64{0.1, 0.2}
	got := SelectTopKWeighted(cs, ws, 10)
	if len(got) != 2 {
		t.Fatalf("k>len must clamp: got %d", len(got))
	}
	if got[0].CredentialID != "b" || got[1].CredentialID != "a" {
		t.Fatalf("expected [b,a], got %v", got)
	}
}

// TestAcquireReleaseTopK_PreservesCap exercises the pool wrapper.
func TestAcquireReleaseTopK_PreservesCap(t *testing.T) {
	h1 := AcquireTopK(8)
	if h1.Cap() != 8 {
		t.Fatalf("acquired cap=%d want 8", h1.Cap())
	}
	h1.Push("a", 1.0)
	ReleaseTopK(h1)
	h2 := AcquireTopK(8)
	if h2.Len() != 0 {
		t.Fatalf("acquired-after-release should be reset, len=%d", h2.Len())
	}
	ReleaseTopK(h2)
}

// TestTopK_ConcurrentAcquireRelease fires many goroutines through
// the pool wrapper to ensure the sync.Pool is safe (and that the
// heap itself can be Reset/Push'd from one goroutine while another
// owns a separate instance).
func TestTopK_ConcurrentAcquireRelease(t *testing.T) {
	var wg sync.WaitGroup
	for g := 0; g < 32; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				h := AcquireTopK(4)
				h.Push(g, float64(i))
				h.Push(g, float64(i+1))
				h.Push(g, float64(i+2))
				if h.Len() > 4 {
					t.Errorf("cap violated: %d", h.Len())
				}
				_ = h.SortedDesc()
				ReleaseTopK(h)
			}
		}(g)
	}
	wg.Wait()
}

// NaN returns a float64 NaN without pulling in the math package
// (which keeps this test file's dependency surface minimal).
func NaN() float64 {
	var z float64
	return z / z
}

// idFor returns a stable 4-character credential ID for tests so
// failure messages stay readable even when N is large.
func idFor(i int) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	return string(alphabet[i%len(alphabet)]) + string(alphabet[(i/len(alphabet))%len(alphabet)])
}

// ---- Benchmarks ----
//
// These are required by Handoff-B #2: every optimization ships with
// a benchmark. Run with `go test -bench=. -benchmem ./domains/routing/...`
// and the comparison vs the legacy selection sort is exercised by
// BenchmarkSelectTopKVsLegacy below.

// BenchmarkTopK_PushOnly measures the per-Push cost for a heap
// already at cap (the realistic steady-state case during routing).
func BenchmarkTopK_PushOnly(b *testing.B) {
	h := NewTopK[int](8)
	for i := 0; i < 8; i++ {
		h.Push(i, float64(i))
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h.Push(i, float64(i%10))
	}
}

// BenchmarkSelectTopK_64Candidates exercises the heap-backed
// SelectTopKWeighted path at the typical production pool size.
func BenchmarkSelectTopK_64Candidates(b *testing.B) {
	const N = 64
	candidates := make([]*Candidate, N)
	weights := make([]float64, N)
	for i := 0; i < N; i++ {
		candidates[i] = &Candidate{CredentialID: idFor(i)}
		weights[i] = float64(i)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = SelectTopKWeighted(candidates, weights, 8)
	}
}

// BenchmarkSelectTopK_256Candidates mirrors the 2026-08 production
// observation: large pools were the P95 trigger. The benchmark
// shows the heap path stays roughly flat as N grows.
func BenchmarkSelectTopK_256Candidates(b *testing.B) {
	const N = 256
	candidates := make([]*Candidate, N)
	weights := make([]float64, N)
	for i := 0; i < N; i++ {
		candidates[i] = &Candidate{CredentialID: idFor(i)}
		weights[i] = float64(i)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = SelectTopKWeighted(candidates, weights, 8)
	}
}

// BenchmarkSelectTopKVsLegacy compares the heap-backed path
// against the legacy O(N²) selection sort the original
// WeightedRouter.SelectTopN used. We re-implement the legacy
// version inline so the benchmark survives the file-level
// deprecation of WeightedRouter.
func BenchmarkSelectTopKVsLegacy(b *testing.B) {
	const N = 256
	candidates := make([]*Candidate, N)
	weights := make([]float64, N)
	for i := 0; i < N; i++ {
		candidates[i] = &Candidate{CredentialID: idFor(i)}
		weights[i] = float64(i)
	}
	b.Run("heap_k=8", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_ = SelectTopKWeighted(candidates, weights, 8)
		}
	})
	b.Run("legacy_k=8", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_ = legacySelectTopN(candidates, weights, 8)
		}
	})
}

// legacySelectTopN mirrors the original WeightedRouter.SelectTopN
// selection-sort implementation. Kept here so the benchmark can
// compare the two paths in a single binary without relying on the
// deprecated router symbol.
func legacySelectTopN(candidates []*Candidate, weights []float64, k int) []*Candidate {
	type cw struct {
		c *Candidate
		w float64
	}
	all := make([]cw, len(candidates))
	for i, c := range candidates {
		all[i] = cw{c: c, w: weights[i]}
	}
	for i := 0; i < len(all); i++ {
		maxIdx := i
		for j := i + 1; j < len(all); j++ {
			if all[j].w > all[maxIdx].w {
				maxIdx = j
			}
		}
		all[i], all[maxIdx] = all[maxIdx], all[i]
	}
	if k <= 0 || k > len(all) {
		k = len(all)
	}
	out := make([]*Candidate, k)
	for i := 0; i < k; i++ {
		out[i] = all[i].c
	}
	return out
}