package kv

import (
	"context"
	"errors"
	"time"
)

// Store is the pluggable cache backend for KV cache entries.
//
// A cache entry stores a precomputed payload (typically the upstream response
// or any annotation computed against the cacheable prefix). The Store is
// keyed by the value returned by Key() — callers compute the key, then
// Store/Lookup against it.
//
// Implementations MUST be safe for concurrent use.
//
// Domain note: This is a hash-keyed store, NOT a semantic cache. For
// embedding-based similarity matching, see cache/semantic/.
type Store interface {
	// Lookup returns (payload, true) if a live entry exists for key.
	// Returns (nil, false, nil) on miss — never errors on simple misses.
	// Errors are reserved for backend failures (disk full, network down).
	Lookup(ctx context.Context, key string) ([]byte, bool, error)

	// Store inserts or overwrites the entry for key. Idempotent on (key).
	// ttl controls expiration; pass 0 for "use default TTL".
	Store(ctx context.Context, key string, payload []byte, ttl time.Duration) error

	// Invalidate removes the entry for key. Missing key is a no-op (no error).
	Invalidate(ctx context.Context, key string) error

	// Stats returns cumulative counters since construction.
	Stats() Stats
}

// Stats reports cumulative Store effectiveness since process start.
// Mirrors cache/semantic.Stats shape so dashboards can combine both.
type Stats struct {
	Lookups     int64 // total Lookup calls
	Hits        int64 // Lookup returned ok=true
	Misses      int64 // Lookup returned ok=false
	Stores      int64 // total Store calls
	Invalidates int64 // total Invalidate calls
	Expirations int64 // Lookup saw an expired entry (counts as miss)
}

// HitRate returns hits / lookups. Returns 0 if Lookups==0.
func (s Stats) HitRate() float64 {
	if s.Lookups == 0 {
		return 0
	}
	return float64(s.Hits) / float64(s.Lookups)
}

// ErrEmptyKey is returned by Store/Lookup/Invalidate when key == "".
// Defensive: an empty key would otherwise collapse every "unkeyed" call
// into a single global bucket and is almost always a programming bug.
var ErrEmptyKey = errors.New("kv: empty cache key")

// ErrTooLarge is returned when payload exceeds MaxPayloadBytes.
// Matches cache/semantic.ErrTooLarge so callers can reuse error handling.
var ErrTooLarge = errors.New("kv: payload exceeds max entry size")

// DefaultTTL is the default time-to-live applied when Store is called with
// ttl <= 0. Production callers typically pass a longer TTL based on their
// upstream cache window (Anthropic's is ~5min; OpenAI's automatic caching
// is up to several hours for popular prefixes).
const DefaultTTL = 5 * time.Minute
