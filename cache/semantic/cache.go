// Package semantic implements semantic caching for LLM responses (C2).
//
// A semantic cache answers: "given this prompt, have we recently served a
// semantically equivalent response that we can reuse?" Two query strategies
// are supported via the SemanticCache interface:
//
//   - ExactMatchCache: prompt + model + tenant matches a recent entry (free,
//     no embedding cost). The baseline.
//   - EmbeddingMatchCache: cosine similarity above a configured threshold.
//
// Domain boundary (see docs/产品方案/2026-06-23-llmgw-domain-architecture-refactor.md):
//   - semantic OWNS: cache key generation, similarity math, hit/miss telemetry
//   - semantic does NOT own: LLM call dispatch (relay/), embedding model
//     hosting, storage persistence (each backend implements Store)
//
// Multi-tenancy: every Cache Key includes tenantID. A hit NEVER crosses
// tenants — enforced by the contract (Lookup requires tenantID).
package semantic

import (
	"errors"
	"time"
)

// Cache is the public interface. The default production implementation
// combines an exact-match L1 cache (free) with an embedding-match L2 cache
// (one round-trip to an embedding model) — see composite.go.
//
// Implementations MUST be safe for concurrent use.
type Cache interface {
	// Lookup returns (payload, true) if a semantically equivalent response
	// is cached for (tenantID, model, prompt). Cache miss → ok=false, nil err.
	//
	// The promptHash is a content-derived fingerprint the caller computes
	// (e.g. SHA256 of normalized prompt) so the cache never needs to hash
	// the raw prompt (privacy + speed).
	Lookup(ctx Context, tenantID, model, promptHash, prompt string) (payload []byte, ok bool, err error)

	// Store inserts a new entry. Stores are idempotent on (tenantID, model,
	// promptHash) — re-storing the same key overwrites the existing entry.
	Store(ctx Context, tenantID, model, promptHash, prompt string, payload []byte, ttl time.Duration) error

	// Invalidate removes all entries for a model across the tenant (used
	// when model config changes or admin manually clears cache).
	Invalidate(ctx Context, tenantID, model string) (removed int, err error)

	// Stats returns aggregate hit/miss counters since process start.
	Stats() Stats
}

// Context carries request-scoped data (deadline, trace). A thin wrapper over
// stdlib context.Context is used here so the cache package has no upstream
// dependency on our internal context types.
type Context interface {
	Deadline() (time.Time, bool)
	Done() <-chan struct{}
	Err() error
	Value(key any) any
}

// Stats reports cumulative cache effectiveness.
type Stats struct {
	Lookups     int64 // total Lookup calls
	ExactHits   int64 // L1 hits
	VectorHits  int64 // L2 (semantic) hits
	Misses      int64 // neither layer matched
	Stores      int64 // total Store calls
	Invalidates int64 // total Invalidate calls
}

// HitRate returns exact+vector hits / total lookups. Returns 0 if Lookups==0.
func (s Stats) HitRate() float64 {
	if s.Lookups == 0 {
		return 0
	}
	return float64(s.ExactHits+s.VectorHits) / float64(s.Lookups)
}

// ErrInvalidInput is returned for empty tenantID/model/prompt.
var ErrInvalidInput = errors.New("semantic: tenantID, model, prompt are required")

// ErrTooLarge is returned when payload exceeds the per-entry size cap.
var ErrTooLarge = errors.New("semantic: payload exceeds max entry size")

// MaxPayloadBytes is the per-entry size cap. Larger responses (rare; most
// LLM completions are well under 64KB) bypass caching to avoid memory pressure.
const MaxPayloadBytes = 64 * 1024
