package delta

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kaixuan/llm-gateway-go/cache/kv"
)

// MaxChainDepth caps how far DeltaStore.Lookup / Store will walk the parent
// chain when reconstructing a payload from nested deltas. Beyond this depth
// the chain is treated as broken (returns ErrMissingParent → cache miss).
//
// Why cap: a malformed / cyclic parentMap could cause infinite recursion.
// 32 covers realistic chat sessions (32 turns) with margin. Higher values
// increase worst-case Lookup latency (O(depth) per Lookup).
const MaxChainDepth = 32

// DeltaStore wraps a kv.Store with incremental encoding. Entries with a
// registered parent are stored as Deltas (parent key + operation + payload
// bytes); entries without a parent (or whose parent is no longer in the
// inner store) are stored as OpFull Deltas.
//
// DeltaStore implements kv.Store — callers can swap it in transparently.
//
// Concurrency: safe for concurrent use. The parentMap is a sync.Map; the
// inner Store is required to be concurrency-safe by the kv.Store contract;
// stats use atomic ops.
type DeltaStore struct {
	inner     kv.Store
	parentMap sync.Map // string (newKey) -> string (parentKey)
	stats     Stats
}

// NewDeltaStore wraps inner. The inner store is required to be non-nil.
func NewDeltaStore(inner kv.Store) *DeltaStore {
	return &DeltaStore{inner: inner}
}

// SetParent registers parentKey as the parent for newKey. When newKey is
// later passed to Store, Encode will attempt to compute a delta against the
// reconstructed payload of parentKey.
//
// SetParent is idempotent: re-registering overwrites the previous parent.
// SetParent("") clears the parent.
//
// v1 explicit registration — v1.1 may add automatic parent discovery
// (LRU of recent keys + LCP heuristic).
func (s *DeltaStore) SetParent(newKey, parentKey string) {
	if newKey == "" {
		return // defensive
	}
	if parentKey == "" {
		s.parentMap.Delete(newKey)
		return
	}
	s.parentMap.Store(newKey, parentKey)
}

// Lookup fetches the entry for key and reconstructs the full payload if
// necessary. Returns (nil, false, nil) on cache miss.
//
// Cache miss is returned (not error) when:
//   - the inner store doesn't have the key
//   - the stored Delta is malformed (corrupt JSON)
//   - the parent chain is broken (parent evicted or chain too deep)
//
// Errors are reserved for inner-store failures (network down, disk full).
func (s *DeltaStore) Lookup(ctx context.Context, key string) ([]byte, bool, error) {
	atomic.AddInt64(&s.stats.Lookups, 1)
	deltaBytes, ok, err := s.inner.Lookup(ctx, key)
	if err != nil {
		atomic.AddInt64(&s.stats.InnerErrors, 1)
		return nil, false, err
	}
	if !ok {
		atomic.AddInt64(&s.stats.Misses, 1)
		return nil, false, nil
	}

	var d Delta
	if err := json.Unmarshal(deltaBytes, &d); err != nil {
		// Malformed — treat as cache miss, not an error (corrupt entries
		// will be overwritten on next Store).
		atomic.AddInt64(&s.stats.CorruptEntries, 1)
		return nil, false, nil
	}

	payload, err := s.resolveChain(ctx, d, MaxChainDepth)
	if err != nil {
		// Parent chain broken — treat as miss.
		atomic.AddInt64(&s.stats.Misses, 1)
		return nil, false, nil
	}
	return payload, true, nil
}

// Store inserts the payload for key. If a parent is registered (via
// SetParent) and the parent chain is healthy, Encode is attempted. If the
// resulting delta saves enough bytes (>= MinCompressionRatio), the entry is
// stored as a Delta. Otherwise, falls back to OpFull.
//
// On every Store call, the entry is REPLACED (kv.Store contract).
func (s *DeltaStore) Store(ctx context.Context, key string, payload []byte, ttl time.Duration) error {
	if key == "" {
		return kv.ErrEmptyKey
	}
	if len(payload) == 0 {
		return ErrEmptyPayload
	}
	if len(payload) > MaxDeltaBytes {
		return kv.ErrTooLarge
	}

	delta, storedAsDelta := s.tryEncode(ctx, key, payload)

	deltaBytes, err := json.Marshal(delta)
	if err != nil {
		return err
	}

	if err := s.inner.Store(ctx, key, deltaBytes, ttl); err != nil {
		atomic.AddInt64(&s.stats.InnerErrors, 1)
		return err
	}

	if storedAsDelta {
		atomic.AddInt64(&s.stats.DeltaStores, 1)
		atomic.AddInt64(&s.stats.BytesSaved, int64(len(payload)-len(deltaBytes)))
	} else {
		atomic.AddInt64(&s.stats.FullStores, 1)
	}
	return nil
}

// Invalidate removes the entry for key. Missing key is a no-op (no error).
// Note: invalidating an entry does NOT cascade to children whose ParentKey
// refers to it. Callers must invalidate the whole sub-chain if they want
// consistency. (This matches kv.Store.Invalidate semantics.)
func (s *DeltaStore) Invalidate(ctx context.Context, key string) error {
	if key == "" {
		return kv.ErrEmptyKey
	}
	// Clean up the parentMap entry too — if the parent goes away, children
	// can no longer be reconstructed.
	s.parentMap.Delete(key)
	if err := s.inner.Invalidate(ctx, key); err != nil {
		return err
	}
	atomic.AddInt64(&s.stats.Invalidates, 1)
	return nil
}

// Stats returns a snapshot of cumulative counters. Safe for concurrent use.
// The snapshot is atomic — each counter is read independently — so it may
// reflect in-flight increments; this is acceptable for telemetry.
func (s *DeltaStore) Stats() kv.Stats {
	atomic.AddInt64(&s.stats.Lookups, 0) // memory barrier for visibility
	return kv.Stats{
		Lookups: atomic.LoadInt64(&s.stats.Lookups),
		Hits:    atomic.LoadInt64(&s.stats.Hits),
		Misses:  atomic.LoadInt64(&s.stats.Misses),
		Stores:  atomic.LoadInt64(&s.stats.FullStores) + atomic.LoadInt64(&s.stats.DeltaStores),
	}
}

// DeltaStats exposes the DeltaStore-specific counters (FullStores, DeltaStores,
// Reconstructions, BytesSaved, etc.) that the kv.Stats interface does not cover.
// Callers that need this granularity (e.g. compression dashboards) type-assert
// the kv.Store to *DeltaStore.
func (s *DeltaStore) DeltaStats() Stats {
	return Stats{
		Lookups:         atomic.LoadInt64(&s.stats.Lookups),
		Hits:            atomic.LoadInt64(&s.stats.Hits),
		Misses:          atomic.LoadInt64(&s.stats.Misses),
		FullStores:      atomic.LoadInt64(&s.stats.FullStores),
		DeltaStores:     atomic.LoadInt64(&s.stats.DeltaStores),
		Invalidates:     atomic.LoadInt64(&s.stats.Invalidates),
		Reconstructions: atomic.LoadInt64(&s.stats.Reconstructions),
		BytesSaved:      atomic.LoadInt64(&s.stats.BytesSaved),
		CorruptEntries:  atomic.LoadInt64(&s.stats.CorruptEntries),
		InnerErrors:     atomic.LoadInt64(&s.stats.InnerErrors),
	}
}

// resolveChain walks the parent chain from d back to a root OpFull Delta,
// then applies the chain forward to reconstruct the payload. Returns the
// reconstructed payload on success; ErrMissingParent if the chain is broken
// or deeper than maxDepth.
func (s *DeltaStore) resolveChain(ctx context.Context, d Delta, maxDepth int) ([]byte, error) {
	if maxDepth <= 0 {
		return nil, ErrMissingParent
	}
	if d.Op == OpFull {
		atomic.AddInt64(&s.stats.Hits, 1)
		return d.Payload, nil
	}

	// Fetch the parent delta.
	parentBytes, ok, err := s.inner.Lookup(ctx, d.ParentKey)
	if err != nil || !ok {
		return nil, ErrMissingParent
	}
	var parentDelta Delta
	if err := json.Unmarshal(parentBytes, &parentDelta); err != nil {
		return nil, ErrMissingParent
	}

	parentPayload, err := s.resolveChain(ctx, parentDelta, maxDepth-1)
	if err != nil {
		return nil, err
	}
	atomic.AddInt64(&s.stats.Reconstructions, 1)
	return Apply(parentPayload, d)
}

// tryEncode attempts to encode payload as a delta against the registered
// parent. Returns (Delta{OpFull, payload}, false) when no parent exists,
// when the parent chain is broken, or when Encode decides not to save.
//
// On success, returns the delta and storedAsDelta=true.
func (s *DeltaStore) tryEncode(ctx context.Context, key string, payload []byte) (Delta, bool) {
	parentKeyIface, ok := s.parentMap.Load(key)
	if !ok {
		// No parent registered — fall through to OpFull.
		return Delta{Op: OpFull, Payload: payload}, false
	}
	parentKey, ok := parentKeyIface.(string)
	if !ok || parentKey == "" {
		return Delta{Op: OpFull, Payload: payload}, false
	}

	parentBytes, ok, err := s.inner.Lookup(ctx, parentKey)
	if err != nil || !ok {
		// Parent evicted or unreachable — fall through.
		return Delta{Op: OpFull, Payload: payload}, false
	}
	var parentDelta Delta
	if err := json.Unmarshal(parentBytes, &parentDelta); err != nil {
		return Delta{Op: OpFull, Payload: payload}, false
	}
	parentPayload, err := s.resolveChain(ctx, parentDelta, MaxChainDepth)
	if err != nil {
		// Parent chain broken — fall through.
		return Delta{Op: OpFull, Payload: payload}, false
	}

	d, saved := Encode(parentPayload, payload)
	if !saved {
		return Delta{Op: OpFull, Payload: payload}, false
	}
	d.ParentKey = parentKey
	return d, true
}

// Sentinel errors specific to DeltaStore (in addition to those in delta.go).
// These are returned as-is (not wrapped) for direct comparison.
var (
	// ErrInnerStoreRequired is returned by code paths that need a non-nil
	// inner store. Currently only used internally; reserved for future
	// constructors that might lazily set the inner store.
	ErrInnerStoreRequired = errors.New("delta: inner kv.Store is required")
)

// Compile-time check: DeltaStore satisfies kv.Store. If this line fails to
// compile after a kv.Store interface change, the dependency has drifted and
// this file needs updating.
var _ kv.Store = (*DeltaStore)(nil)
