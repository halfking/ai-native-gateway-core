// Package delta implements incremental encoding for cache payloads to
// compress storage when consecutive cache entries share a byte-stable prefix
// (the common chat-completions pattern: same session, same system prompt,
// growing conversation history).
//
// Why this package exists:
//
//	The cache/kv/ package (B2) computes a stable SHA256 fingerprint of the
//	cacheable prefix and stores (key -> payload). For chat traffic where N
//	consecutive requests in a session share ~90% of bytes (system prompt +
//	older turns) and only the most recent turn changes, this design stores
//	N copies of nearly the same payload.
//
//	delta/ wraps kv.Store with a DeltaStore that, when a parent key is
//	registered, stores only the BYTE-LEVEL DIFFERENCE between the new entry
//	and its parent. On Lookup, the delta is replayed against the parent
//	chain to reconstruct the full payload. In synthetic chat traces this
//	halves storage (AC-5) without changing the kv.Store interface contract.
//
// Domain boundary (see docs/llm-gateway-go/2026-07-01-cache-delta-plan.md):
//
//	delta/ OWNS:
//	  - Delta encoding (parent payload + tail bytes -> compressed Delta)
//	  - Delta application (parent + Delta -> reconstructed payload)
//	  - DeltaStore: a kv.Store that opportunistically stores deltas
//
//	delta/ does NOT own:
//	  - cache key computation (cache/kv/)
//	  - message stabilization / reordering (cache/prefix/)
//	  - semantic similarity (cache/semantic/)
//	  - actual byte storage (delegated to inner kv.Store implementation)
//
// What delta/ guarantees:
//   - Encode is a pure function of (parent, new). Same inputs -> same Delta.
//   - Apply is the exact inverse of Encode on chat-completion bodies.
//   - DeltaStore implements kv.Store; callers can swap it in transparently.
//   - Lookup on a missing parent returns (nil, false, nil) — a cache miss,
//     not an error. The cache layer is an optimization, never a correctness
//     requirement.
//   - The package is concurrency-safe (the parentMap uses sync.Map; the
//     inner Store is required to be concurrency-safe by kv.Store contract).
package delta

import "errors"

// Op is the kind of incremental operation a Delta represents.
//
// OpFull is the no-compression fallback: when parent and new diverge too
// much (or no parent exists), the entire payload is stored inline as a Delta
// with Op == OpFull. This keeps DeltaStore's interface uniform — every stored
// value is a Delta; the Op field tells the store how to interpret it.
//
// Op is a string (not a numeric enum) so its JSON representation is
// self-describing ("full" / "append" / "replace_tail") instead of numeric.
// Callers can compare directly to the constants or to telemetry labels.
type Op string

const (
	// OpFull stores the complete payload inline. No parent required.
	// Used as fallback when no parent exists or when compression would
	// not save bytes.
	OpFull Op = "full"

	// OpAppend stores only the bytes that are appended to the parent's
	// end. Reconstruction: parent || delta.Payload.
	// Used for chat history growth: new request = old request + 1 turn.
	OpAppend Op = "append"

	// OpReplaceTail stores bytes that replace the parent's tail
	// (parent[Cutoff:] is replaced by delta.Payload).
	// Reconstruction: parent[:Cutoff] || delta.Payload.
	// Used when the latest 1-2 turns change but history is identical.
	OpReplaceTail Op = "replace_tail"
)

// String returns a stable lowercase name for telemetry (cache.delta.op.*).
func (o Op) String() string {
	switch o {
	case OpFull:
		return "full"
	case OpAppend:
		return "append"
	case OpReplaceTail:
		return "replace_tail"
	default:
		return "unknown"
	}
}

// IsValid reports whether o is a known Op. Used by tests + by the wire
// deserializer to reject malformed data from disk/Redis.
func (o Op) IsValid() bool {
	return o == OpFull || o == OpAppend || o == OpReplaceTail
}

// Delta is the on-wire representation of an incremental entry. It is what
// the inner kv.Store actually receives for storage (encoded as JSON or the
// transport's preferred format by DeltaStore's encode path).
//
// On Lookup, Apply is called with (parent_payload, delta) to reconstruct.
//
// Semantics of Cutoff:
//   - OpFull, OpAppend: Cutoff is ignored (must be 0)
//   - OpReplaceTail: parent[Cutoff:] is replaced by Payload
type Delta struct {
	// ParentKey is the kv.Store key of the parent payload. Empty when
	// Op == OpFull (this Delta IS the root). DeltaStore fills this in
	// when wrapping a full payload as an OpFull delta.
	ParentKey string `json:"parent_key,omitempty"`

	// Op tells Apply how to combine parent + Payload.
	Op Op `json:"op"`

	// Payload is the bytes being added or replacing the tail.
	// For OpFull, this is the complete payload (== full entry).
	Payload []byte `json:"payload"`

	// Cutoff is used only by OpReplaceTail: parent[:Cutoff] is kept,
	// parent[Cutoff:] is replaced by Payload. Zero for other ops.
	Cutoff int `json:"cutoff,omitempty"`
}

// Sentinel errors. Callers compare with errors.Is (so wrapping is allowed).
var (
	// ErrEmptyPayload is returned when Encode/Apply receives an empty
	// payload. Empty payloads are not storable in the chat-completion
	// domain — callers should not pass them.
	ErrEmptyPayload = errors.New("delta: empty payload")

	// ErrEmptyParent is returned by Apply when ParentKey is set but the
	// parent payload is nil. The caller is responsible for fetching
	// the parent first; if the parent has been evicted, callers should
	// treat it as a cache miss rather than retry.
	ErrEmptyParent = errors.New("delta: empty parent payload")

	// ErrInvalidCutoff is returned when OpReplaceTail has Cutoff < 0 or
	// Cutoff > len(parent). Cutoff must be within [0, len(parent)].
	ErrInvalidCutoff = errors.New("delta: invalid cutoff")

	// ErrInvalidOp is returned by Apply when the Delta's Op field is
	// not a recognized value (defensive against wire corruption).
	ErrInvalidOp = errors.New("delta: invalid op")

	// ErrMissingParent is returned by Apply when a Delta has a non-empty
	// ParentKey but the parent could not be fetched. The caller (typically
	// DeltaStore.Lookup) should treat this as a cache miss and may attempt
	// to recover by re-storing.
	ErrMissingParent = errors.New("delta: missing parent")
)

// MinCompressionRatio is the minimum parent/new size ratio for Encode to
// return a non-Full delta. If the delta would save less than this fraction
// of bytes (relative to the new payload), Encode returns OpFull instead.
//
// Rationale: a delta that saves 5% of bytes is not worth the Apply overhead
// and chain length. 30% is empirically where storage + Lookup cost balance
// for chat-completion append patterns.
const MinCompressionRatio = 0.30

// MaxDeltaBytes is a safety cap on the size of an individual Delta's Payload.
// Enforced by Encode (silently degrades to OpFull if the delta would exceed
// this size) and by DeltaStore.Store (returns ErrTooLarge if violated).
// Mirrors cache/kv.MaxPayloadBytes so callers can use one constant.
const MaxDeltaBytes = 64 * 1024

// Stats are DeltaStore-specific counters (in addition to kv.Stats).
// Exposed via DeltaStore.DeltaStats(). The counters track the package's
// compression effectiveness in addition to raw hit/miss rate.
type Stats struct {
	// Lookups / Hits / Misses mirror kv.Stats but may be larger because
	// they include reconstruction walks (a delta lookup counts as 1
	// Lookup but may count as 0 or more Reconstructions).
	Lookups int64
	Hits    int64
	Misses  int64

	// FullStores counts Store calls that fell back to OpFull (no parent
	// available, parent chain broken, or compression not beneficial).
	FullStores int64

	// DeltaStores counts Store calls that successfully encoded as a delta.
	DeltaStores int64

	// Invalidates counts Invalidate calls (matches kv.Stats.Invalidates).
	Invalidates int64

	// Reconstructions counts Apply calls during Lookup chain walks. A single
	// Lookup that walks a 3-level chain counts as 1 Lookup + 2 Reconstructions.
	// Used to size CPU cost of the delta strategy.
	Reconstructions int64

	// BytesSaved is the cumulative bytes saved by storing as a delta
	// instead of full payload (i.e. sum of len(payload) - len(delta_bytes)).
	// Excludes OpFull stores (which save nothing).
	BytesSaved int64

	// CorruptEntries counts Lookup hits on entries that failed JSON decode.
	// Indicates either wire corruption or a version mismatch; alerts ops.
	CorruptEntries int64

	// InnerErrors counts errors propagated from the inner kv.Store
	// (network, disk full, etc). Useful for ops alerting.
	InnerErrors int64
}

// CompressionRatio returns BytesSaved / (BytesSaved + BytesFull). When both
// are 0 (no Stores yet), returns 0.
//
// BytesFull is approximated as (FullStores + DeltaStores) * avg_full_size;
// without per-call accounting, we use the simple proxy:
// total_payload_bytes_stored ≈ FullStores * avg_size_proxy + BytesSaved
// where avg_size_proxy is set externally. For accurate per-call metrics,
// callers can compute from their own accounting.
func (s Stats) CompressionRatio(avgFullPayloadBytes int64) float64 {
	if s.BytesSaved == 0 {
		return 0
	}
	totalBytes := s.BytesSaved + int64(s.FullStores+s.DeltaStores)*avgFullPayloadBytes
	if totalBytes == 0 {
		return 0
	}
	return float64(s.BytesSaved) / float64(totalBytes)
}
