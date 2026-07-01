package delta

import (
	"bytes"
)

// approximateJSONOverhead is the estimated wire-format byte overhead for a
// Delta JSON record (Op tag + ParentKey + Cutoff field). The estimate is
// generous — it overestimates the cost of OpReplaceTail to bias toward OpFull
// in close calls, ensuring we only encode deltas that are clearly worth it.
//
// Used by Encode to compute the compression ratio. Not exported; tune via
// MinCompressionRatio if you need different bias.
const approximateJSONOverhead = 32

// Encode computes the smallest delta between parent and new payloads and
// returns (delta, true) when the delta is meaningfully smaller than the
// full new payload (savings >= MinCompressionRatio * len(new)).
//
// When compression is not beneficial, Encode returns (Delta{OpFull, new}, false).
// The caller can use this OpFull delta directly with the inner kv.Store —
// it stores the full payload as the delta. This means DeltaStore can always
// pass Encode's return value to inner.Store regardless of the second return.
//
// Algorithm (byte-level LCP, sufficient for chat-completion bodies):
//
//  1. If new is empty: returns (zero Delta, false) — encoding empty is meaningless.
//  2. If parent is empty or equal to new: returns OpFull(new, false).
//  3. Compute the longest common byte prefix (LCP) of parent and new.
//  4. If LCP == len(parent): parent is a prefix of new → OpAppend with new[LCP:].
//  5. If LCP > 0: parent and new share a prefix → OpReplaceTail(Cutoff=LCP, new[LCP:]).
//  6. Else: no shared prefix → OpFull(new, false).
//
// In cases 4-5, the chosen Op's savings ratio is computed against new's size.
// If savings/float64(len(new)) < MinCompressionRatio, we fall back to OpFull.
//
// Edge cases handled:
//   - parent longer than new: LCP capped at len(new); if LCP > 0 → OpReplaceTail.
//   - new is a perfect prefix of parent (shrinking conversation): OpReplaceTail
//     with Cutoff=len(new), payload=empty → effectively a "truncate" delta.
//     This is technically valid but unusual; the caller may choose to discard it.
//   - both parent and new are OpFull candidates → the OpFull form is returned.
func Encode(parent, new []byte) (Delta, bool) {
	if len(new) == 0 {
		// Empty new payload — nothing to encode. Caller should not invoke
		// Encode with empty new; if they do, return a sentinel zero Delta
		// with saved=false so the caller knows nothing was produced.
		return Delta{}, false
	}

	// No parent → full payload.
	if len(parent) == 0 {
		return Delta{Op: OpFull, Payload: new}, false
	}

	// Identical → no benefit.
	if bytes.Equal(parent, new) {
		return Delta{Op: OpFull, Payload: new}, false
	}

	// Longest common byte prefix.
	lcp := commonPrefixLen(parent, new)

	// Case 1: parent is a perfect prefix of new → OpAppend.
	if lcp == len(parent) {
		d := Delta{Op: OpAppend, Payload: new[lcp:]}
		if ratioOK(len(parent), len(new)) {
			return d, true
		}
		return Delta{Op: OpFull, Payload: new}, false
	}

	// Case 2: shared prefix but not full parent → OpReplaceTail.
	if lcp > 0 {
		savings := lcp - approximateJSONOverhead
		if savings > 0 && float64(savings)/float64(len(new)) >= MinCompressionRatio {
			return Delta{Op: OpReplaceTail, Cutoff: lcp, Payload: new[lcp:]}, true
		}
	}

	// No shared prefix (or savings too small) → full payload.
	return Delta{Op: OpFull, Payload: new}, false
}

// commonPrefixLen returns the length of the longest byte prefix shared by
// a and b. Capped at min(len(a), len(b)).
func commonPrefixLen(a, b []byte) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}

// ratioOK reports whether the absolute savings (bytes) on a payload of size
// total meets the MinCompressionRatio threshold. Pure size-based — does not
// consider wire overhead because OpAppend / OpFull cases have identical
// overhead (~20 bytes for Op + ParentKey), so the ratio cancels out.
func ratioOK(saved, total int) bool {
	if total == 0 {
		return false
	}
	return float64(saved)/float64(total) >= MinCompressionRatio
}
