package compression

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// ── docs/omni-ref3 C3: compression result memo ───────────────────────────────
//
// The expensive part of Prepare is the post-window work: an LLM summary call
// (seconds + upstream quota) or a mechanical trim over a large body. When a
// client retries the SAME turn (network retry, follow-up with an identical
// body, or a failover round), we recompute that work from scratch.
//
// The memo caches the *result* of that work keyed by
// (tenantID, sessionID, mode, protocol, contextWindow, sha256(bodyIntoCompression)).
//
// Two invariants, both learned from omniroute's principalId incident:
//
//  1. tenant AND session are ALWAYS part of the key — a memo entry can never
//     be served to a different tenant or a different session, even if two
//     bodies hash identically.
//  2. TTL is short (default 5 min) so a stale summary cannot outlive the turn
//     it was computed for.

// memoStore is the storage backend behind ResultMemo. Redis in production;
// tests inject an in-memory implementation so the memo logic is testable
// without a live Redis.
type memoStore interface {
	get(ctx context.Context, key string) ([]byte, bool, error)
	set(ctx context.Context, key string, val []byte, ttl time.Duration) error
	del(ctx context.Context, key string) error
}

// MemoValue is the cached compression result. It carries everything Prepare
// would otherwise recompute, so a hit can rebuild the PrepareResult verbatim
// instead of re-running the summary/trim.
type MemoValue struct {
	// CompressedBody is the post-compression outbound body (JSON messages).
	CompressedBody []byte `json:"compressed_body"`

	// Strategy is the CompressionStrategy that produced CompressedBody
	// ("mechanical_trim" / "sliding_window_*").
	Strategy string `json:"strategy"`

	// SummaryMarker is the smm_v1 marker when an LLM summary produced the body.
	SummaryMarker string `json:"summary_marker,omitempty"`

	// WindowTriggered is the window trigger reason recorded on the original run.
	WindowTriggered string `json:"window_triggered,omitempty"`

	// Degraded mirrors PrepareResult.Degraded (mutual-exclusion window active).
	Degraded bool `json:"degraded,omitempty"`

	// MsgCount / TokenEst / MsgHashes describe CompressedBody.
	MsgCount  int             `json:"msg_count"`
	TokenEst  int             `json:"token_est"`
	MsgHashes json.RawMessage `json:"msg_hashes,omitempty"`

	// CompressedPrefixHash is the C8/D7 stable-prefix hash of CompressedBody.
	CompressedPrefixHash string `json:"compressed_prefix_hash,omitempty"`

	// CachedAt is set by Set; used for staleness debugging.
	CachedAt time.Time `json:"cached_at"`
}

// ResultMemo caches compression results to avoid redundant computation.
// A nil *ResultMemo (or one with a nil store) is a graceful no-op, so callers
// never need a nil check.
type ResultMemo struct {
	store memoStore
	ttl   time.Duration
}

// DefaultMemoTTL is the fallback TTL when none is supplied.
const DefaultMemoTTL = 5 * time.Minute

// NewResultMemo creates a Redis-backed result memo. A nil client yields a
// no-op memo (feature disabled) rather than an error, so wiring code can pass
// whatever Redis handle it has without branching.
func NewResultMemo(rdb *redis.Client, ttl time.Duration) *ResultMemo {
	if ttl <= 0 {
		ttl = DefaultMemoTTL
	}
	if rdb == nil {
		return &ResultMemo{store: nil, ttl: ttl}
	}
	return &ResultMemo{store: redisMemoStore{rdb: rdb}, ttl: ttl}
}

// newResultMemoWithStore is the test seam: it accepts any memoStore.
func newResultMemoWithStore(store memoStore, ttl time.Duration) *ResultMemo {
	if ttl <= 0 {
		ttl = DefaultMemoTTL
	}
	return &ResultMemo{store: store, ttl: ttl}
}

// enabled reports whether the memo has a usable backend.
func (m *ResultMemo) enabled() bool {
	return m != nil && m.store != nil
}

// MemoKeyParts are the non-body dimensions of the memo key. Every field is
// part of the key: a change in any of them changes the compression outcome,
// so sharing a memo entry across them would serve a wrong result.
type MemoKeyParts struct {
	TenantID      string
	SessionID     string
	Mode          string
	Protocol      string
	ContextWindow int
}

// memoKey builds the Redis key. The body is hashed (sha256) so the key length
// stays bounded regardless of conversation size.
//
// Format: compression:memo:v1:{tenant}:{session}:{mode}:{protocol}:{ctxWindow}:{bodyHash}
func memoKey(p MemoKeyParts, bodyIntoCompression []byte) string {
	sum := sha256.Sum256(bodyIntoCompression)
	return fmt.Sprintf("compression:memo:v1:%s:%s:%s:%s:%d:%s",
		p.TenantID, p.SessionID, p.Mode, p.Protocol, p.ContextWindow,
		hex.EncodeToString(sum[:]))
}

// Get returns the cached result, or (nil, nil) on miss. Errors are returned so
// the caller can decide; Prepare treats any error as a miss.
func (m *ResultMemo) Get(ctx context.Context, p MemoKeyParts, bodyIntoCompression []byte) (*MemoValue, error) {
	if !m.enabled() || p.TenantID == "" || p.SessionID == "" {
		return nil, nil
	}

	raw, found, err := m.store.get(ctx, memoKey(p, bodyIntoCompression))
	if err != nil || !found {
		return nil, err
	}

	var val MemoValue
	if err := json.Unmarshal(raw, &val); err != nil {
		return nil, err
	}
	if len(val.CompressedBody) == 0 || val.Strategy == "" {
		// Malformed / partially written entry — treat as a miss.
		return nil, nil
	}
	return &val, nil
}

// Set stores a compression result. Refuses to cache entries that lack a body
// or a strategy so a hit can always be replayed faithfully.
func (m *ResultMemo) Set(ctx context.Context, p MemoKeyParts, bodyIntoCompression []byte, val *MemoValue) error {
	if !m.enabled() || val == nil || p.TenantID == "" || p.SessionID == "" {
		return nil
	}
	if len(val.CompressedBody) == 0 || val.Strategy == "" {
		return nil
	}

	val.CachedAt = time.Now()
	raw, err := json.Marshal(val)
	if err != nil {
		return err
	}
	return m.store.set(ctx, memoKey(p, bodyIntoCompression), raw, m.ttl)
}

// Delete evicts one entry (invalidation hook for future callers).
func (m *ResultMemo) Delete(ctx context.Context, p MemoKeyParts, bodyIntoCompression []byte) error {
	if !m.enabled() {
		return nil
	}
	return m.store.del(ctx, memoKey(p, bodyIntoCompression))
}

// ── Redis backend ────────────────────────────────────────────────────────────

type redisMemoStore struct {
	rdb *redis.Client
}

func (s redisMemoStore) get(ctx context.Context, key string) ([]byte, bool, error) {
	raw, err := s.rdb.Get(ctx, key).Bytes()
	if err == redis.Nil {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return raw, true, nil
}

func (s redisMemoStore) set(ctx context.Context, key string, val []byte, ttl time.Duration) error {
	return s.rdb.Set(ctx, key, val, ttl).Err()
}

func (s redisMemoStore) del(ctx context.Context, key string) error {
	return s.rdb.Del(ctx, key).Err()
}
