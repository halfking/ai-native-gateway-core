// Package routing provides weighted routing primitives. This file
// introduces MinHeapTopK — a small min-heap used to extract the
// top-K weighted candidates in O(N log K) instead of the O(N²)
// selection sort the legacy WeightedRouter.SelectTopN path uses.
//
// Background (Handoff-B #2 in tests/stress/CAPACITY_HANDOVER.md):
// the production routing layer sees credential pools with up to ~200
// active candidates per (provider, model). The old selection-sort
// variant of SelectTopN therefore dominates P95 once the pool
// crosses ~32 entries, because every selection pass re-scans the
// remaining slice. With K typically ≤ 8, a min-heap that keeps the
// current K-largest-so-far in the heap reduces the per-call cost
// to roughly N*log8 = 3*N comparisons — a meaningful drop on the
// hot path that the benchmark suite must confirm.
//
// The heap is generic over comparable values (typically float64
// weights) and any item type T (typically *WeightedCandidate or a
// snapshot). It is intentionally allocation-light: the only state
// beyond the caller's backing slice is the small slices Heap holds
// internally (weights + indices). On the hot path Heap can be
// reused across calls via AcquireFromPool to avoid repeated
// allocation, matching the sync.Pool ctx-pool pattern (Handoff-B #4).
package routing

import (
	"sort"
	"sync"
)

// TopK is a tiny generic min-heap that keeps the K largest items
// from a stream. Items smaller than the current minimum are dropped
// so the heap never grows past K+1 entries.
//
// The zero value is NOT ready to use — call NewTopK. The struct
// deliberately does not embed sort.Interface because the production
// call sites already know K and weight type at construction time
// and want to avoid reflection in the hot path.
//
// T is constrained to comparable because SortedDesc's stable-sort
// comparator falls back to pointer-identity (the indexOf helper)
// for tie-breaking. Anything *Candidate satisfies this naturally.
type TopK[T comparable] struct {
	weights []float64 // min-heap on these; smaller = lower priority
	items   []T       // parallel slice; items[i] corresponds to weights[i]
	cap     int       // K (target size)
}

// NewTopK returns a TopK ready to accept at most K items. cap <= 0
// is normalised to 1 so callers can pass the user's "default topN"
// without a guard.
func NewTopK[T comparable](cap int) *TopK[T] {
	if cap <= 0 {
		cap = 1
	}
	return &TopK[T]{
		weights: make([]float64, 0, cap+1),
		items:   make([]T, 0, cap+1),
		cap:     cap,
	}
}

// Len reports how many items are currently retained. Always ≤ cap.
func (h *TopK[T]) Len() int { return len(h.items) }

// Cap reports the K target.
func (h *TopK[T]) Cap() int { return h.cap }

// Push offers an (item, weight) pair. If the heap is not full the
// pair is appended and the heap is restored. If the heap is full
// AND the new weight is ≤ the current minimum, the pair is dropped
// without touching the heap; otherwise the current minimum is
// replaced and the heap is re-sunk. NaN weights are treated as
// "smallest" and never evict a real entry (defensive: callers can
// legitimately pass 0 or NaN for unhealthy candidates).
func (h *TopK[T]) Push(item T, weight float64) {
	if h.cap == 0 {
		return
	}
	if len(h.items) < h.cap {
		h.weights = append(h.weights, weight)
		h.items = append(h.items, item)
		h.siftUp(len(h.items) - 1)
		return
	}
	// heap full: only evict if strictly better than the current min.
	if weight <= h.weights[0] || isNaN(weight) {
		return
	}
	h.weights[0] = weight
	h.items[0] = item
	h.siftDown(0)
}

// SortedDesc returns the retained items in descending weight order.
// O(K log K) sort; safe to call multiple times. The returned slice
// aliases the internal storage and MUST NOT be retained past the
// next Push — callers who need a stable copy should clone it via
// the standard `append([]T(nil), s...)` idiom.
func (h *TopK[T]) SortedDesc() []T {
	out := make([]T, len(h.items))
	copy(out, h.items)
	sort.SliceStable(out, func(i, j int) bool {
		return h.weights[indexOf(h.items, out[i])] >
			h.weights[indexOf(h.items, out[j])]
	})
	return out
}

// SortedDescWithWeights returns parallel slices (items, weights) in
// descending weight order. Same aliasing rules as SortedDesc.
func (h *TopK[T]) SortedDescWithWeights() ([]T, []float64) {
	idx := make([]int, len(h.items))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(i, j int) bool {
		return h.weights[idx[i]] > h.weights[idx[j]]
	})
	outItems := make([]T, len(h.items))
	outWeights := make([]float64, len(h.weights))
	for rank, i := range idx {
		outItems[rank] = h.items[i]
		outWeights[rank] = h.weights[i]
	}
	return outItems, outWeights
}

// Reset clears the heap in-place so the same TopK can be reused
// across calls without reallocating the backing slices. The cap
// stays the same.
func (h *TopK[T]) Reset() {
	h.weights = h.weights[:0]
	h.items = h.items[:0]
}

// siftUp restores the min-heap invariant after appending at index i.
func (h *TopK[T]) siftUp(i int) {
	for i > 0 {
		parent := (i - 1) / 2
		if h.weights[parent] <= h.weights[i] {
			return
		}
		h.weights[parent], h.weights[i] = h.weights[i], h.weights[parent]
		h.items[parent], h.items[i] = h.items[i], h.items[parent]
		i = parent
	}
}

// siftDown restores the min-heap invariant after replacing the root.
func (h *TopK[T]) siftDown(i int) {
	n := len(h.items)
	for {
		left := 2*i + 1
		if left >= n {
			return
		}
		smallest := left
		if right := left + 1; right < n && h.weights[right] < h.weights[left] {
			smallest = right
		}
		if h.weights[i] <= h.weights[smallest] {
			return
		}
		h.weights[i], h.weights[smallest] = h.weights[smallest], h.weights[i]
		h.items[i], h.items[smallest] = h.items[smallest], h.items[i]
		i = smallest
	}
}

// indexOf returns the first index of v in items, or -1 if absent.
// Used by the sort comparator; only invoked once per Swap so the
// O(N) scan cost is amortised across the whole sort.
func indexOf[T comparable](items []T, v T) int {
	for i, x := range items {
		if x == v {
			return i
		}
	}
	return -1
}

// isNaN is the float64 self-equality NaN check; math.IsNaN pulls
// in a hot dependency for a single instruction in the hot path,
// so we inline it.
func isNaN(f float64) bool { return f != f }

// ---- Pool reuse (sync.Pool ctx-pool pattern, Handoff-B #4) ----

// topKPool caches TopK instances per cap to amortise allocations
// across calls. The pool key is the cap value because TopK
// initialises the backing slices with the right size; reusing a
// heap with a different cap would force a reallocation on the
// next Push anyway, which defeats the point.
//
// This is the same pattern the sync.Pool ctx-pool wrapper uses
// (Handoff-B #4): the hot path requests a fresh heap, mutates it,
// and lets the next caller reuse it. We do not call Reset
// automatically — the caller does, because it knows when the prior
// data is safe to overwrite (typically right before reuse).
var topKPool = sync.Pool{
	New: func() any {
		// default cap is overwritten by AcquireTopK; we just need a
		// non-nil heap so the type assertion in Get() succeeds.
		return &topKAny{h: NewTopK[any](1)}
	},
}

type topKAny struct {
	h *TopK[any]
}

// AcquireTopK returns a pooled TopK[any] with the given cap. The
// returned heap is reset; callers can immediately Push into it.
// The cap is preserved if the pooled heap already matches;
// otherwise a fresh heap is allocated and the old one is dropped.
func AcquireTopK(cap int) *TopK[any] {
	v := topKPool.Get().(*topKAny)
	if v.h == nil || v.h.cap != cap {
		v.h = NewTopK[any](cap)
	} else {
		v.h.Reset()
	}
	return v.h
}

// ReleaseTopK returns the heap to the pool. The heap's slices are
// NOT cleared (Reset is the caller's responsibility before reuse),
// so a borrowed-and-forgotten heap does not leak state into the
// next caller. This matches sync.Pool's typical safety contract.
func ReleaseTopK(h *TopK[any]) {
	if h == nil {
		return
	}
	topKPool.Put(&topKAny{h: h})
}

// ---- Typed convenience for the common Candidate path ----

// candidateHeapEntry bundles a Candidate with its computed weight so
// we can avoid recomputing weights inside the WeightedRouter.
//
// The WeightedRouter.SelectTopN path is the only hot consumer right
// now; the helpers below are exported so other routers can adopt
// the same shape later without copy-pasting the heap plumbing.
type candidateHeapEntry struct {
	c      *Candidate
	weight float64
}

// SelectTopKWeighted is the heap-backed replacement for
// WeightedRouter.SelectTopN. It pulls weights + candidates through
// a reusable TopK and returns the K largest candidates in
// descending weight order. K is clamped to the slice length.
//
// Compared to the prior O(N²) selection sort, this is O(N log K)
// comparisons and avoids the swap-on-every-iteration cost. The
// returned slice is freshly allocated; callers may mutate or
// retain it without aliasing the router's internal state.
func SelectTopKWeighted(candidates []*Candidate, weights []float64, k int) []*Candidate {
	if len(candidates) == 0 || len(candidates) != len(weights) {
		return nil
	}
	if k <= 0 || k > len(candidates) {
		k = len(candidates)
	}
	h := AcquireTopK(k)
	defer ReleaseTopK(h)
	for i, c := range candidates {
		if c == nil {
			continue
		}
		h.Push(c, weights[i])
	}
	out := h.SortedDesc()
	// SortedDesc returns a []T where T=any — the heap stores the
	// items as `any` because TopK is generic. We cast them back to
	// *Candidate before the ReleaseTopK call invalidates the backing
	// storage. The cast is guaranteed correct because Push always
	// receives a *Candidate at this call site.
	result := make([]*Candidate, len(out))
	for i, v := range out {
		c, ok := v.(*Candidate)
		if !ok {
			// Defensive: should never happen given how Push is called.
			return nil
		}
		result[i] = c
	}
	return result
}