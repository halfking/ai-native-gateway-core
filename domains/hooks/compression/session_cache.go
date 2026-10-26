// Package compression stores exact outbound session state/body pairs.
//
// With Redis configured, session:sc:{tenant}:{session}:v2 is authoritative.
// Every acknowledged write replaces its complete payload and TTL atomically;
// L1 can only reuse a revision confirmed by Redis. The outbound body has the
// same privacy boundary as existing session persistence: it is tenant-scoped,
// TTL-bound, never logged here, and gzip compression is not encryption.
//
// v1 metadata is imported only when its exact body hash can be established.
// v2 writers do not mirror into v1: old readers/writers must be drained before
// promising cross-instance consistency. Mixed generations cannot coordinate
// through the old metadata-only protocol. SessionState schema remains 1;
// the shared pair envelope is independently versioned as pair_v=2.

package compression

import (
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/session/v2"
	"github.com/kaixuan/llm-gateway-go/internal/jsoncol"
	"github.com/kaixuan/llm-gateway-go/settings"
)

const (
	// schemaVersion is the current Redis Hash schema. Bump when SessionState
	// fields change in a backward-incompatible way.
	schemaVersion = 1

	// redisKeyTTL is the built-in fallback for cache.session_redis_ttl_minutes.
	// 30 minutes covers the typical idle-between-turns gap; the sliding-window
	// idle trigger fires at 5 minutes anyway, so 30 min is a generous safety
	// margin. Use sessionCacheRedisTTL() instead of this constant in hot-path
	// code so the value can be overridden via settings_kv without a restart.
	redisKeyTTL = 30 * time.Minute

	// l1MaxSessions is the built-in fallback for cache.session_l1_capacity.
	// The L1 is a true O(1) LRU (container/list doubly-linked list + map), so
	// when this capacity is exceeded the LEAST-RECENTLY-USED entry is evicted.
	// Use sessionCacheL1Capacity() instead of this constant in hot-path code.
	// (Previously a sync.Map with first-seen-first-evicted heuristic —
	// replaced 2026-07-06 after borrowing rtk's emphasis on deterministic,
	// recoverable state; the old heuristic could evict an active session and
	// force an L2/L3 round-trip on its next turn.)
	l1MaxSessions = 1024

	// l1MaxBytes is the built-in fallback for cache.session_l1_max_bytes
	// (docs/omni-ref3 D3). The L1 evicts on EITHER the count limit
	// (l1MaxSessions) OR this aggregate byte budget, whichever binds first.
	//
	// Rationale: a single agent session with a large tool history can hold a
	// multi-MB outbound body. 1024 such entries (the count limit alone) would
	// be gigabytes of RAM. The byte budget bounds worst-case memory regardless
	// of how few sessions are cached. 256 MiB is a conservative default: ~256
	// sessions at 1 MiB each, well under the count limit, but it caps a
	// pathological fleet of huge bodies. Use sessionCacheL1MaxBytes() in
	// hot-path code so operators can retune without a restart.
	l1MaxBytes = 256 << 20 // 256 MiB

	// l1EntryOverheadBytes is a flat per-entry accounting charge added on top
	// of the body + key length. It approximates the SessionState struct, the
	// list.Element, the map bucket, and pointer overhead so that many tiny
	// entries still cost something against the byte budget (otherwise the byte
	// limit would never bind for small-body sessions and only the count limit
	// would matter — which is fine, but the charge keeps the accounting honest
	// and monotonic). Not a precise sizeof; a deliberately generous estimate.
	l1EntryOverheadBytes = 512

	// compactionMarkerPrefix is the content prefix used to identify summary
	// boundary messages injected by the session compression. Any message
	// whose "content" field starts with this prefix is treated as a
	// compacted summary and is skipped by the LCS diff.
	CompactionMarkerPrefix = "[smm_v1:"
)

// sessionCacheRedisTTL returns the hot-reloadable Redis TTL for the session
// cache. It reads cache.session_redis_ttl_minutes from settings_kv on every
// call so that an operator can change the TTL without restarting the gateway.
// The value is clamped to [5, 1440] minutes and falls back to redisKeyTTL
// when settings.Global is unavailable.
func sessionCacheRedisTTL() time.Duration {
	minutes := settings.GetPlatformInt("cache.session_redis_ttl_minutes", int(redisKeyTTL.Minutes()))
	if minutes < 5 {
		minutes = 5
	}
	if minutes > 1440 {
		minutes = 1440
	}
	return time.Duration(minutes) * time.Minute
}

// sessionCacheL1Capacity returns the hot-reloadable L1 LRU capacity. It reads
// cache.session_l1_capacity from settings_kv on every call. The value is
// clamped to [64, 16384] and falls back to l1MaxSessions when settings.Global
// is unavailable. Note: reducing capacity takes effect gradually (entries are
// only evicted when a new Set pushes the list over the limit).
func sessionCacheL1Capacity() int {
	cap := settings.GetPlatformInt("cache.session_l1_capacity", l1MaxSessions)
	if cap < 64 {
		return 64
	}
	if cap > 16384 {
		return 16384
	}
	return cap
}

// sessionCacheL1MaxBytes returns the hot-reloadable L1 aggregate byte budget
// (docs/omni-ref3 D3). Reads cache.session_l1_max_bytes from settings_kv on
// every call. Clamped to [16 MiB, 2 GiB] and falls back to l1MaxBytes (256 MiB)
// when settings.Global is unavailable. Together with sessionCacheL1Capacity(),
// the L1 evicts on EITHER count OR bytes — whichever binds first. The byte
// budget prevents a small number of huge-body sessions from consuming gigabytes.
func sessionCacheL1MaxBytes() int {
	mb := settings.GetPlatformInt("cache.session_l1_max_bytes", l1MaxBytes)
	if mb < (16 << 20) {
		return 16 << 20 // 16 MiB floor
	}
	if mb > (2 << 30) {
		return 2 << 30 // 2 GiB ceiling
	}
	return mb
}

// SessionState is the per-session state persisted in L1/L2/L3.
type SessionState struct {
	SchemaVersion        int    `json:"v"`
	LastOutboundHash     string `json:"loh"`           // sha256 hex of last outbound body
	LastCompressedAt     int64  `json:"lcat"`          // unix seconds of last LLM summary
	MsgCount             int    `json:"mc"`            // outbound message count
	TokenEstimate        int    `json:"te"`            // outbound token estimate
	SummaryMarker        string `json:"smm"`           // "smm_v1:<sha256>" or ""
	RecentlyCompressedAt int64  `json:"rcat"`          // unix seconds (60s mutual-exclusion)
	ToolsHash            string `json:"th,omitempty"`  // sha256 hex of tools array (Phase 1 optimization)
	SystemPrompt         string `json:"sys,omitempty"` // cached system prompt (Phase 1 optimization)

	// v4: Full session tracking
	FullSessionHash    string `json:"fsh,omitempty"`  // sha256 of the complete (uncompressed) session body
	LastStripAt        int64  `json:"lsat,omitempty"` // unix seconds of last tool/thinking strip
	StripsApplied      int    `json:"sa,omitempty"`   // count of strip operations applied
	CompletedTasks     int    `json:"ct,omitempty"`   // count of completed tasks detected
	CompressionMode    string `json:"cm,omitempty"`   // current v4 compression mode: "off"|"delta"|"smart"|"aggressive"
	MessagesAfterStrip int    `json:"mas,omitempty"`  // message count after last strip
	TokensAfterStrip   int    `json:"tas,omitempty"`  // token estimate after last strip

	// v5: Smart compression cut tracking. When a context_length_exceeded
	// 4xx triggers smart compression, the CutMarker records WHERE the cut
	// happened so the next request for the same session can use
	// [summary + messages_after_cut + new_messages] without re-compressing.
	// The SummaryText is stored in L1 only (not Redis) to avoid large blobs.
	HasCutMarker   bool   `json:"hcm,omitempty"` // true when CutMarker fields below are valid
	CutCreatedAt   int64  `json:"cm_ts,omitempty"`
	CutSourceMsgs  int    `json:"cm_src,omitempty"`
	CutSystemMsgs  int    `json:"cm_sys,omitempty"`
	CutIndex       int    `json:"cm_ci,omitempty"`
	CutStrategy    string `json:"cm_strat,omitempty"`
	CutBytesBefore int    `json:"cm_bb,omitempty"`
	CutBytesAfter  int    `json:"cm_ba,omitempty"`
	// CutPreSanitizeStart / CutPreSanitizeEnd (2026-09-01, audit §五)：把压缩
	// 覆盖的 message 在 sanitize 之前的 index 范围持久化到 Redis hash。
	// 与 SessionState.SanitizeMapRef 配合可还原三层 offset 全貌。
	CutPreSanitizeStart int `json:"cm_psor0,omitempty"`
	CutPreSanitizeEnd   int `json:"cm_psor1,omitempty"`

	// v6: Audited state (Cache 2 concept).
	//
	// Captures the latest result of the session-audit hook so that
	// (a) downstream hooks can branch without re-running detection, and
	// (b) the approval resume flow can recover what decision was made
	// even after Redis eviction.
	//
	// SchemaVersion is intentionally kept at 1 — all new fields are
	// optional (omitempty) so that legacy readers silently ignore them
	// and new readers simply observe zero values when the keys are
	// absent. This is a non-breaking schema evolution.
	AuditedAt           int64  `json:"aud_at,omitempty"`  // unix seconds of last audit
	AuditScore          int    `json:"aud_sc,omitempty"`  // composite audit score 0-10
	SecurityScore       int    `json:"sec_sc,omitempty"`  // security sub-score 0-10
	SensitiveDetected   bool   `json:"sen_det,omitempty"` // true when sensitive words hit
	PIIStripped         bool   `json:"pii_strip,omitempty"`
	ApprovalStatus      string `json:"app_st,omitempty"`  // pending|approved|rejected|""
	ApprovalID          string `json:"app_id,omitempty"`  // approval_queue row UUID
	OptimizationApplied string `json:"opt_app,omitempty"` // strip_tools|compress_thinking|summarize

	// v7 (O-2, 2026-08-09): original→compressed message index mapping from
	// the most recent compression. Populated only when a window-triggered
	// rewrite (summary/trim) fired; nil otherwise. Non-breaking addition —
	// a missing "algn" hash key decodes to nil for legacy entries.
	AlignmentMap []AlignmentInfo `json:"alignment_map,omitempty"`

	// v8 (Phase 2, 2026-08-13): Three-tier cache semantic alignment.
	// Separates L1 (raw), L2 (compressed), L3 (audited) state for better
	// observability and quality scoring.
	//
	// L1 fields (raw session, true values):
	RawSnapshot      MessageSnapshot `json:"raw_snapshot,omitempty"`
	RawTokenEstimate int             `json:"raw_te,omitempty"` // token count before sanitization/compression
	RawMsgCount      int             `json:"raw_mc,omitempty"` // message count before compression

	// L2 fields (compressed session, placeholders):
	CompressionSourceSnapshot MessageSnapshot         `json:"compression_source_snapshot,omitempty"`
	CompressedSnapshot        MessageSnapshot         `json:"compressed_snapshot,omitempty"`
	CompressedTokens          int                     `json:"cmp_te,omitempty"`      // token count after compression
	CompressedMsgs            int                     `json:"cmp_mc,omitempty"`      // message count after compression
	CompressedPrefixHash      string                  `json:"cmp_ph,omitempty"`      // stable compressed prefix fingerprint
	CompressionQuality        CompressionQualityScore `json:"cmp_quality,omitempty"` // quality metrics

	// L3 fields (audited session, sanitize map):
	SanitizeMapRef        string                `json:"sanitize_ref,omitempty"` // Redis key: session:{id}:sanitize
	SanitizeStats         SanitizeStats         `json:"sanitize_stats,omitempty"`
	SanitizeMessageRefs   []SanitizedMessageRef `json:"sanitize_message_refs,omitempty"`
	SanitizedSnapshot     MessageSnapshot       `json:"sanitized_snapshot,omitempty"`
	SanitizeMapGeneration string                `json:"sanitize_map_generation,omitempty"`
}

// MsgHash is one entry in the outbound_msg_hashes JSONB array.
type MsgHash struct {
	Index  int    `json:"index"`
	SHA256 string `json:"sha256"`
}

// SessionCacheBackend is the minimal Redis interface required by SessionCache.
// Using an interface allows tests to inject a mock without a real Redis.
type SessionCacheBackend interface {
	// HSet writes legacy fields; shared commits require SessionCacheScriptBackend.
	HSet(ctx context.Context, key string, values ...any) error
	// HGetAll returns all fields of a Redis hash. Returns an empty map (not
	// an error) when the key does not exist.
	HGetAll(ctx context.Context, key string) (map[string]string, error)
	// Expire resets the TTL for a key.
	Expire(ctx context.Context, key string, ttl time.Duration) error
	// Del deletes a key.
	Del(ctx context.Context, key string) error
}

// SessionCacheDB is the minimal PostgreSQL interface required by SessionCache.
type SessionCacheDB interface {
	// LastOutboundForSession queries _request_logs for the most recent row
	// that has an outbound_body for the given session, returning the
	// outbound_msg_hashes, compression_meta (for summary_marker), and
	// the msg count / token estimate.
	LastOutboundForSession(ctx context.Context, tenantID, gwSessionID string) (*LastOutboundRow, error)
}

// LastOutboundRow is the DB result for a cold-start L3 fallback.
type LastOutboundRow struct {
	OutboundMsgHashes json.RawMessage // [{index, sha256}]
	OutboundBody      json.RawMessage
	OutboundMsgCount  int
	OutboundTokenEst  int
	CompressionMeta   json.RawMessage // may contain summary_marker
}

// l1Entry is the in-process cache entry. It is stored as the Value of a
// list.Element in the LRU ordering, AND its elem pointer lets the LRU
// promote an entry to the front in O(1) on access.
type l1Entry struct {
	key      string // tenantID + ":" + gwSessionID (back-reference for eviction)
	state    *SessionState
	body     []byte // last outbound body bytes (nil if evicted from L1 to save RAM)
	bytes    int    // len(body) + len(key) + l1EntryOverheadBytes (D3: byte-budget tracking)
	elem     *list.Element
	revision string // immutable shared commit token; empty for L1-only entries
}

// SessionCache provides three-tier session state caching.
//
// L1 is a true O(1) LRU: ll (a doubly-linked list via container/list) orders
// entries from most-recently-used (front) to least-recently-used (back), and
// l1 maps the key to its list.Element for O(1) lookup + promotion. An access
// moves the element to the front; eviction pops the back element.
type SessionCache struct {
	mu       sync.Mutex
	ll       *list.List          // front = MRU, back = LRU; O(1) promote/evict
	l1       map[string]*l1Entry // key = tenantID+":"+gwSessionID → entry (entry.elem is the list node)
	curBytes int                 // aggregate bytes across all l1 entries (D3: enforces l1MaxBytes)

	// updateStripes serializes multi-step read-modify-write updates for one
	// session without retaining an unbounded lock per session.
	updateStripes [256]sync.Mutex

	redis      SessionCacheBackend // nil = L2 disabled (tests / no Redis)
	db         SessionCacheDB      // nil = L3 disabled (tests / no DB)
	turnReader TurnReaderSource    // V2-P2.5: L3 reads session_bodies when wired
}

// TurnReaderSource is the subset of *v2.TurnReader the L3 read path uses.
// Declared as an interface so the rehydrate behavior is unit-testable
// without a live PostgreSQL; production wires v2.NewTurnReader(pool).
type TurnReaderSource interface {
	LoadLatestOutbound(ctx context.Context, tenantID, sessionID string) ([]v2.Message, error)
	LoadChain(ctx context.Context, tenantID, sessionID string, lastN int) ([]v2.Message, error)
}

// NewSessionCache creates a SessionCache. redis and db are optional.
func NewSessionCache(redis SessionCacheBackend, db SessionCacheDB) *SessionCache {
	return &SessionCache{
		ll:    list.New(),
		l1:    make(map[string]*l1Entry, 64),
		redis: redis,
		db:    db,
	}
}

// SetTurnReader wires the Sessions V2 body reader used by the L3 cold-start
// path. It is optional: when V2 tables are unavailable, loadFromDB falls back
// to the legacy _request_logs reader.
func (c *SessionCache) SetTurnReader(reader TurnReaderSource) {
	if c == nil {
		return
	}
	c.turnReader = reader
}

func l1Key(tenantID, gwSessionID string) string {
	return tenantID + ":" + gwSessionID
}

func redisKey(tenantID, gwSessionID string) string {
	return "session:sc:" + tenantID + ":" + gwSessionID + ":v2"
}

func (c *SessionCache) updateStripe(key string) *sync.Mutex {
	sum := sha256.Sum256([]byte(key))
	return &c.updateStripes[sum[0]]
}

// Update atomically loads, transforms, and stores one session state. The
// callback receives caller-owned copies and runs at most once per call.
// Returning nil state leaves the entry unchanged and acknowledges no new write.
func (c *SessionCache) Update(ctx context.Context, tenantID, gwSessionID string, fn func(*SessionState, []byte) (*SessionState, []byte, error)) error {
	if c == nil || gwSessionID == "" || fn == nil || !settings.IsEnabled("session_cache") {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.redis != nil {
		return c.withSharedLease(ctx, tenantID, gwSessionID, func(token string) error {
			state, body, err := c.loadSharedLocked(ctx, tenantID, gwSessionID, token)
			if err != nil {
				return err
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			state, body, err = fn(state, body)
			if err != nil {
				return err
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			// nil state is an explicit no-op, not an acknowledgment of a new write.
			if state == nil {
				return nil
			}
			return c.commitShared(ctx, tenantID, gwSessionID, token, state, body)
		})
	}
	stripe := c.updateStripe(l1Key(tenantID, gwSessionID))
	stripe.Lock()
	defer stripe.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	state, body, err := c.getLocal(ctx, tenantID, gwSessionID)
	if err != nil {
		return err
	}
	state, body, err = fn(state, body)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if state != nil {
		runThreeTierCheck(ctx, tenantID, gwSessionID, state)
		c.setL1(l1Key(tenantID, gwSessionID), state, body)
	}
	return nil
}

// GetOrLoad uses Redis as the authority whenever it is configured. L1 can
// only reuse the exact revision Redis just confirmed. With no Redis, the
// per-object stripe protects local backfill and read-modify-write operations.
func (c *SessionCache) GetOrLoad(ctx context.Context, tenantID, gwSessionID string) (*SessionState, []byte, error) {
	if c == nil || gwSessionID == "" || !settings.IsEnabled("session_cache") {
		return nil, nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if c.redis != nil {
		return c.getShared(ctx, tenantID, gwSessionID)
	}
	stripe := c.updateStripe(l1Key(tenantID, gwSessionID))
	stripe.Lock()
	defer stripe.Unlock()
	return c.getLocal(ctx, tenantID, gwSessionID)
}
func (c *SessionCache) getLocal(ctx context.Context, tenantID, gwSessionID string) (*SessionState, []byte, error) {
	key := l1Key(tenantID, gwSessionID)
	c.mu.Lock()
	if e, ok := c.l1[key]; ok {
		c.ll.MoveToFront(e.elem)
		st, body := cloneSessionState(e.state), append([]byte(nil), e.body...)
		c.mu.Unlock()
		return st, body, nil
	}
	c.mu.Unlock()
	if c.turnReader != nil || c.db != nil {
		st, body, err := c.loadFromDB(ctx, tenantID, gwSessionID)
		if err != nil {
			slog.Warn("session_cache: db load error", "session", gwSessionID, "error", err)
			return nil, nil, nil
		}
		if st != nil {
			c.setL1(key, st, body)
			return st, body, nil
		}
	}
	return nil, nil, nil
}

// Set replaces the complete state/body pair. nil body is an exact empty-body
// replacement. A nil error with Redis configured confirms the shared commit;
// errors never publish unconfirmed data to L1.
func (c *SessionCache) Set(ctx context.Context, tenantID, gwSessionID string, state *SessionState, outboundBody []byte) error {
	if c == nil || gwSessionID == "" || state == nil || !settings.IsEnabled("session_cache") {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.redis != nil {
		return c.withSharedLease(ctx, tenantID, gwSessionID, func(token string) error {
			return c.commitShared(ctx, tenantID, gwSessionID, token, state, outboundBody)
		})
	}
	stripe := c.updateStripe(l1Key(tenantID, gwSessionID))
	stripe.Lock()
	defer stripe.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	runThreeTierCheck(ctx, tenantID, gwSessionID, state)
	c.setL1(l1Key(tenantID, gwSessionID), state, outboundBody)
	return nil
}

// Invalidate removes a session from all tiers (e.g., on session destruction).
//
// KILL-SWITCH (2026-07-12 incident): when session_cache is disabled,
// Invalidate is a no-op.
//
// SC-1 (docs/修订0811/19): invalidation is session-state-driven — the
// sanitize map/offsets hashes share the session's lifecycle and must die with
// it, otherwise plaintext values outlive the session cache that references
// them.
func (c *SessionCache) Invalidate(ctx context.Context, tenantID, gwSessionID string) {
	if !settings.IsEnabled("session_cache") {
		return
	}
	key := l1Key(tenantID, gwSessionID)
	clearLocal := func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if e, ok := c.l1[key]; ok {
			c.ll.Remove(e.elem)
			delete(c.l1, key)
			c.curBytes -= e.bytes
		}
	}
	if c.redis == nil {
		stripe := c.updateStripe(key)
		stripe.Lock()
		defer stripe.Unlock()
		clearLocal()
		return
	}
	if err := c.withSharedLease(ctx, tenantID, gwSessionID, func(token string) error {
		if err := c.invalidateShared(ctx, tenantID, gwSessionID, token); err != nil {
			return err
		}
		clearLocal()
		return nil
	}); err != nil {
		slog.WarnContext(ctx, "session_cache: redis invalidate error", "session", gwSessionID, "error", err)
		return
	}

	// Best-effort cascade: the sanitize hashes live in the same Redis DB
	// as the L2 state (both written per session), so deleting them here
	// closes the lifecycle gap without a new dependency on the sanitize
	// package (which imports this one).
	//
	// T11-P0 v2: sanitize key 现在带 tenantHash 段；tenantID 由 caller 在
	// Invalidate() 入参传入，落到与 L2 redisKey 同一 tenant。空 tenant 时
	// mirror 函数会落到 "_unknown" 桶 — 与输入中间件降级路径一致。
	// T11-P0 v2 fix: 之前直接把 raw tenantID 传给 SessionSanitizeRedisKey，
	// 但该函数的第一个参数应是 tenantHash (sha256[:8])。写入侧
	// (smart_sani_guard.go) 用 HashTenant 派生 hash，所以 key 永远匹配不上，
	// 删除是静默 no-op — stale sanitize map/offset 泄漏到 TTL。这里镜像同样
	// 的 hash 派生后再调用 key builder。
	tenantHash := hashTenantForSanitizeKey(tenantID)
	_ = c.redis.Del(ctx, SessionSanitizeRedisKey(tenantHash, gwSessionID))
	_ = c.redis.Del(ctx, SessionSanitizeOffsetRedisKey(tenantHash, gwSessionID))
	_ = c.redis.Del(ctx, SessionSanitizeRedisKey(tenantHash, gwSessionID)+":generation")
}

// ──────────────────────────────────────────────────────────────────────────────
// Internal helpers
// ──────────────────────────────────────────────────────────────────────────────

func cloneSessionState(state *SessionState) *SessionState {
	if state == nil {
		return nil
	}
	clone := *state
	clone.AlignmentMap = append([]AlignmentInfo(nil), state.AlignmentMap...)
	clone.SanitizeMessageRefs = append([]SanitizedMessageRef(nil), state.SanitizeMessageRefs...)
	return &clone
}

func (c *SessionCache) setL1(key string, state *SessionState, body []byte) {
	c.setL1Revision(key, state, body, "")
}

func (c *SessionCache) setL1Revision(key string, state *SessionState, body []byte, revision string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := cloneSessionState(state)
	bodyCopy := append([]byte(nil), body...)
	entryBytes := len(bodyCopy) + len(key) + l1EntryOverheadBytes

	// Update-in-place + promote to front if the key already exists.
	if existing, ok := c.l1[key]; ok {
		// Adjust curBytes for the delta (new - old).
		c.curBytes -= existing.bytes
		c.curBytes += entryBytes
		existing.state = st
		existing.body = bodyCopy
		existing.revision = revision

		existing.bytes = entryBytes
		c.ll.MoveToFront(existing.elem)
		return
	}

	// New entry: push to front, evict the LRU (back) if over capacity OR bytes.
	entry := &l1Entry{key: key, state: st, body: bodyCopy, bytes: entryBytes, revision: revision}
	entry.elem = c.ll.PushFront(entry)
	c.l1[key] = entry
	c.curBytes += entryBytes

	maxCount := sessionCacheL1Capacity()
	maxBytes := sessionCacheL1MaxBytes()
	// Evict least-recently-used entries while EITHER limit is exceeded.
	for c.ll.Len() > maxCount || c.curBytes > maxBytes {
		back := c.ll.Back()
		if back == nil {
			break
		}
		ev, ok := back.Value.(*l1Entry)
		if !ok {
			break // defensive: should never happen
		}
		c.ll.Remove(back)
		delete(c.l1, ev.key)
		c.curBytes -= ev.bytes
	}
}

// loadFromRedis reads the complete current shared pair. Legacy imports are
// deliberately handled under the lease by loadSharedLocked instead.
func (c *SessionCache) loadFromRedis(ctx context.Context, tenantID, sessionID string) (*SessionState, []byte, error) {
	st, body, _, err := c.readShared(ctx, tenantID, sessionID)
	return st, body, err
}

func (c *SessionCache) loadFromDB(ctx context.Context, tenantID, gwSessionID string) (*SessionState, []byte, error) {
	// ★ §143.86（落法 ⓪）★ G-1 守卫：★ 本次请求由网关【自动新建】了会话
	// ⇒ ⇒ 该会话 ID 刚被创建 ⇒ ⇒ 库里【必然】没有它的历史 outbound body
	// ⇒ ⇒ ⇒ 腿①②③ 整条读链【必然落空】⇒ ⇒ 直接短路。
	//
	// ★ ★★ 语义等价性（★ 上线前提）：★ 腿①② 本来也会落空 ⇒ ⇒ 短路后上游
	// ★ 走的是「st == nil ⇒ return nil, nil, nil」这条【本来就存在】的分支
	// ★ （★ session_cache.go getLocal / session_cache_shared.go loadSharedLocked）
	// ⇒ ⇒ ★★ 【可观测行为完全不变】★ ⇒ ⇒ 只省掉一次注定落空的读。
	//
	// ★ ★★ 开关默认【关】（★ ENABLE_SESSION_CACHE_SKIP_AUTO_CREATED）⇒ ⇒
	// ★ ★★ 未打开时本守卫恒为 false ⇒ ⇒ 线上行为与改动前逐字一致。
	if ShouldSkipLegacyLoad(ctx, gwSessionID) {
		slog.InfoContext(ctx, "session_cache: skip db load, auto-created session",
			"session", gwSessionID, "tenant", tenantID)
		return nil, nil, nil
	}
	if c.turnReader != nil {
		// The latest outbound snapshot is the exact body previously forwarded to
		// the upstream model, including any compression summary marker. Prefer it
		// over reconstructing the uncompressed request/response deltas.
		msgs, err := c.turnReader.LoadLatestOutbound(ctx, tenantID, gwSessionID)
		if err != nil {
			slog.Warn("session_cache: v2 latest outbound load failed, falling back",
				"session", gwSessionID, "error", err)
		} else if len(msgs) > 0 {
			body, marshalErr := json.Marshal(map[string]any{"messages": msgs})
			if marshalErr != nil {
				return nil, nil, fmt.Errorf("marshal latest outbound body: %w", marshalErr)
			}
			return &SessionState{
				SchemaVersion:    schemaVersion,
				LastOutboundHash: sha256Hex(body),
				MsgCount:         len(msgs),
				TokenEstimate:    estimateBodyTokens(body),
			}, body, nil
		}

		// Older/partial V2 rows may not have outbound_body. Reconstruct recent
		// turns from deltas before falling back to legacy request_logs.
		msgs, err = c.turnReader.LoadChain(ctx, tenantID, gwSessionID, 10)
		if err != nil {
			slog.Warn("session_cache: v2 delta chain load failed, falling back",
				"session", gwSessionID, "error", err)
		} else if len(msgs) > 0 {
			body, marshalErr := json.Marshal(map[string]any{"messages": msgs})
			if marshalErr != nil {
				return nil, nil, fmt.Errorf("marshal reconstructed body: %w", marshalErr)
			}
			return &SessionState{
				SchemaVersion:    schemaVersion,
				LastOutboundHash: sha256Hex(body),
				MsgCount:         len(msgs),
				TokenEstimate:    estimateBodyTokens(body),
			}, body, nil
		}
	}
	return c.loadFromLegacyDB(ctx, tenantID, gwSessionID)
}

func (c *SessionCache) loadFromLegacyDB(ctx context.Context, tenantID, gwSessionID string) (*SessionState, []byte, error) {
	// ★ §143.86（落法 ⓪）★ G-2 守卫：★ L-2（★ 共享路径的 legacy 校验分支，
	// ★ session_cache_shared.go）★ 【直连本函数】、★ 绕过腿①② ⇒ ⇒ G-1 覆盖不到它
	// ⇒ ⇒ ⇒ 本守卫覆盖 L-2。
	//
	// ★ ★★ 语义等价性：★ 短路后 body 为空 ⇒ ⇒ 调用方的
	// ★ `if len(body) == 0 || sha256Hex(body) != old.LastOutboundHash` 判定
	// ★ 【照旧成立】⇒ ⇒ 返回【同一个 error】（★ legacy session cache has no exact
	// matching outbound body）⇒ ⇒ ★★ 【连错误消息都不变】★ ⇒ ⇒ 只省掉一次查询。
	//
	// ★ ★★ 现实收益：★ §143.84 实测 L-2 本窗口 = 0 次 ⇒ ⇒ 本守卫当前是
	// ★ 【防御性】的（★ 理由与代价见 runbook §143.85.3）。
	if ShouldSkipLegacyLoad(ctx, gwSessionID) {
		slog.InfoContext(ctx, "session_cache: skip legacy load, auto-created session",
			"session", gwSessionID, "tenant", tenantID)
		return nil, nil, nil
	}
	// ★ §143.103（R146）P-1 守卫：探测型合成 session 短路（§143.101 ㉕）。
	// ★ 这类会话（sys:probe:{cred|prov|gw}:{yyyymmdd}，按日聚合）不进 mirror
	// ⇒ bodies 族无行 ⇒ L3 结构性恒 0 行（§143.99.4 / §143.100.4 生产实测），
	// 但命中数万轮次会话时单次可达 24,763 ms（§143.100 实跑）。
	// ★ 短路返回 (nil, nil, nil) = 「查询返回 0 行」的既有映射 ⇒ 可观测行为不变；
	// ★ 腿①②（turn_reader）不受影响。★ 开关默认关（EnvSkipProbeSessions）。
	// ★ 位置在【LogL3Reached 之前】⇒ 被短路的调用【不算】走到 L3
	// ⇒ 开关打开后服务侧 L3 计数的降幅 = 本守卫的实际收益（可直接对拍）。
	if ShouldSkipProbeLoad(gwSessionID) {
		slog.InfoContext(ctx, "session_cache: skip L3 load, probe synthetic session",
			"session", gwSessionID, "tenant", tenantID)
		return nil, nil, nil
	}
	if c.db == nil {
		return nil, nil, nil
	}
	// ★ §143.89：★ 「读链走到 L3」的服务侧埋点（★ 纯观测，★ 不改控制流）。
	// ★ ⇒ ⇒ 位置在【L3 查询之前】⇒ ⇒ 每次真正发起 L3 查询【恰好一条】日志；
	// ★ ⇒ ⇒ 位置在【G-2 守卫之后】⇒ ⇒ 被短路的那些【不算】走到 L3；
	// ★ ⇒ ⇒ 它同时覆盖【本地链尾】与【共享路径直连】⇒ ⇒ 服务侧计数
	// ★ ⇒ ⇒ 可与库端 L3 Δcalls 直接对拍（★ 同侧对应物）。
	LogL3Reached(ctx, gwSessionID, tenantID)
	row, err := c.db.LastOutboundForSession(ctx, tenantID, gwSessionID)
	if err != nil || row == nil {
		return nil, nil, err
	}
	// Extract summary_marker from compression_meta JSONB if present.
	summaryMarker := ""
	if len(row.CompressionMeta) > 0 {
		var meta map[string]any
		if json.Unmarshal(row.CompressionMeta, &meta) == nil {
			if v, ok := meta["summary_marker"].(string); ok {
				summaryMarker = v
			}
		}
	}
	st := &SessionState{
		SchemaVersion:    schemaVersion,
		LastOutboundHash: sha256Hex(row.OutboundBody),
		MsgCount:         row.OutboundMsgCount,
		TokenEstimate:    row.OutboundTokenEst,
		SummaryMarker:    summaryMarker,
	}
	return st, row.OutboundBody, nil
}

// ──────────────────────────────────────────────────────────────────────────────
// Redis field serialisation
// ──────────────────────────────────────────────────────────────────────────────

func encodeSessionStateFields(st *SessionState) []any {
	fields := []any{
		"v", fmt.Sprintf("%d", st.SchemaVersion),
		"loh", st.LastOutboundHash,
		"lcat", fmt.Sprintf("%d", st.LastCompressedAt),
		"mc", fmt.Sprintf("%d", st.MsgCount),
		"te", fmt.Sprintf("%d", st.TokenEstimate),
		"smm", st.SummaryMarker,
		"rcat", fmt.Sprintf("%d", st.RecentlyCompressedAt),
	}
	// Phase 1 optimization: persist ToolsHash and SystemPrompt
	if st.ToolsHash != "" {
		fields = append(fields, "th", st.ToolsHash)
	}
	if st.SystemPrompt != "" {
		fields = append(fields, "sys", st.SystemPrompt)
	}
	// v4: Full session tracking
	if st.FullSessionHash != "" {
		fields = append(fields, "fsh", st.FullSessionHash)
	}
	if st.LastStripAt > 0 {
		fields = append(fields, "lsat", fmt.Sprintf("%d", st.LastStripAt))
	}
	if st.StripsApplied > 0 {
		fields = append(fields, "sa", fmt.Sprintf("%d", st.StripsApplied))
	}
	if st.CompressionMode != "" {
		fields = append(fields, "cm", st.CompressionMode)
	}
	if st.CompletedTasks > 0 {
		fields = append(fields, "ct", fmt.Sprintf("%d", st.CompletedTasks))
	}
	if st.MessagesAfterStrip > 0 {
		fields = append(fields, "mas", fmt.Sprintf("%d", st.MessagesAfterStrip))
	}
	// Reset recovery coordinates explicitly: stale HSET fields must never be
	// rebound to a newly written dictionary generation.
	hasCut := "0"
	if st.HasCutMarker {
		hasCut = "1"
	}
	fields = append(fields, "hcm", hasCut,
		"cm_ts", fmt.Sprint(st.CutCreatedAt), "cm_src", fmt.Sprint(st.CutSourceMsgs),
		"cm_sys", fmt.Sprint(st.CutSystemMsgs), "cm_ci", fmt.Sprint(st.CutIndex),
		"cm_strat", st.CutStrategy, "cm_bb", fmt.Sprint(st.CutBytesBefore),
		"cm_ba", fmt.Sprint(st.CutBytesAfter), "cm_psor0", fmt.Sprint(st.CutPreSanitizeStart),
		"cm_psor1", fmt.Sprint(st.CutPreSanitizeEnd), "cmp_ph", st.CompressedPrefixHash)

	// A clean audit replaces the prior verdict/approval state. HSET must
	// persist zero values too, or a cold reader resurrects old decisions.
	boolField := func(value bool) string {
		if value {
			return "1"
		}
		return "0"
	}
	fields = append(fields, "aud_at", fmt.Sprint(st.AuditedAt),
		"aud_sc", fmt.Sprint(st.AuditScore), "sec_sc", fmt.Sprint(st.SecurityScore),
		"sen_det", boolField(st.SensitiveDetected), "pii_strip", boolField(st.PIIStripped),
		"app_st", st.ApprovalStatus, "app_id", st.ApprovalID, "opt_app", st.OptimizationApplied)
	fields = append(fields, "raw_te", fmt.Sprint(st.RawTokenEstimate), "raw_mc", fmt.Sprint(st.RawMsgCount),
		"cmp_te", fmt.Sprint(st.CompressedTokens), "cmp_mc", fmt.Sprint(st.CompressedMsgs))
	// HSET is a patch, so omitted zero values would resurrect previous metadata
	// on an L2 cold read. Write every stage/safety field, including resets.
	fields = append(fields, "san_ref", st.SanitizeMapRef, "san_gen", st.SanitizeMapGeneration)
	for _, field := range []struct {
		key   string
		value any
	}{
		{"algn", st.AlignmentMap}, {"cmp_q", st.CompressionQuality},
		{"san_stats", st.SanitizeStats}, {"cmp_src", st.CompressionSourceSnapshot},
		{"raw_snap", st.RawSnapshot}, {"cmp_snap", st.CompressedSnapshot},
		{"san_snap", st.SanitizedSnapshot}, {"san_msg_refs", st.SanitizeMessageRefs},
	} {
		if b, err := json.Marshal(field.value); err == nil {
			fields = append(fields, field.key, string(b))
		}
	}
	return fields
}

func decodeSessionStateFields(fields map[string]string, st *SessionState) error {
	if raw, ok := fields["state"]; ok {
		return json.Unmarshal([]byte(raw), st)
	}
	parseInt := func(s string) int64 {
		var v int64
		//nolint:errcheck // best-effort parse, non-critical
		fmt.Sscanf(s, "%d", &v)
		return v
	}
	st.SchemaVersion = int(parseInt(fields["v"]))
	st.LastOutboundHash = fields["loh"]
	st.LastCompressedAt = parseInt(fields["lcat"])
	st.MsgCount = int(parseInt(fields["mc"]))
	st.TokenEstimate = int(parseInt(fields["te"]))
	st.SummaryMarker = fields["smm"]
	st.RecentlyCompressedAt = parseInt(fields["rcat"])
	// Phase 1 optimization: restore ToolsHash and SystemPrompt
	st.ToolsHash = fields["th"]
	st.SystemPrompt = fields["sys"]
	// v4: Full session tracking
	st.FullSessionHash = fields["fsh"]
	st.LastStripAt = parseInt(fields["lsat"])
	st.StripsApplied = int(parseInt(fields["sa"]))
	st.CompressionMode = fields["cm"]
	st.CompletedTasks = int(parseInt(fields["ct"]))
	st.MessagesAfterStrip = int(parseInt(fields["mas"]))
	// v5: Smart compression cut marker
	st.HasCutMarker = fields["hcm"] == "1" || fields["hcm"] == "true"
	st.CutCreatedAt = parseInt(fields["cm_ts"])
	st.CutSourceMsgs = int(parseInt(fields["cm_src"]))
	st.CutSystemMsgs = int(parseInt(fields["cm_sys"]))
	st.CutIndex = int(parseInt(fields["cm_ci"]))
	st.CutStrategy = fields["cm_strat"]
	st.CutBytesBefore = int(parseInt(fields["cm_bb"]))
	st.CutBytesAfter = int(parseInt(fields["cm_ba"]))
	// v9 (2026-09-01, audit §五)：缺省即零值，旧 Redis hash 自动读为零。
	st.CutPreSanitizeStart = int(parseInt(fields["cm_psor0"]))
	st.CutPreSanitizeEnd = int(parseInt(fields["cm_psor1"]))
	st.CompressedPrefixHash = fields["cmp_ph"]
	// v6: Audited state — missing keys decode to zero value, which is the
	// intended "no audit yet" semantic.
	st.AuditedAt = parseInt(fields["aud_at"])
	st.AuditScore = int(parseInt(fields["aud_sc"]))
	st.SecurityScore = int(parseInt(fields["sec_sc"]))
	st.SensitiveDetected = fields["sen_det"] == "1" || fields["sen_det"] == "true"
	st.PIIStripped = fields["pii_strip"] == "1" || fields["pii_strip"] == "true"
	st.ApprovalStatus = fields["app_st"]
	st.ApprovalID = fields["app_id"]
	st.OptimizationApplied = fields["opt_app"]
	// v7 (O-2): AlignmentMap — 0458 §1.3 同族收口（2026-10-03）：坏数据此前
	// 被裸 Unmarshal+静默 nil 吞掉零痕迹，与下方 cmp_q 等六字段口径割裂。
	// jsoncol.Decode 失败留 Warn 可反查，fresh 语义同时封住
	// UnmarshalTypeError 的半填充逃逸。
	if raw := fields["algn"]; raw != "" {
		jsoncol.Decode("compression.decodeSessionStateFields/algn", []byte(raw), &st.AlignmentMap)
	}
	st.TokensAfterStrip = int(parseInt(fields["tas"]))
	st.RawTokenEstimate = int(parseInt(fields["raw_te"]))
	st.RawMsgCount = int(parseInt(fields["raw_mc"]))
	st.CompressedTokens = int(parseInt(fields["cmp_te"]))
	st.CompressedMsgs = int(parseInt(fields["cmp_mc"]))
	st.SanitizeMapRef = fields["san_ref"]
	st.SanitizeMapGeneration = fields["san_gen"]
	if raw := fields["cmp_q"]; raw != "" {
		// 0458 §1.3 压缩域收口（2026-10-01）：L2 Redis Hash 的序列化字段与
		// DB jsonb 列同族——坏数据此前被 `_ =` 静默吞成零值/半填充值，继续
		// 驱动压缩决策。Decode 的 fresh 语义保证失败时字段保原值并留 Warn。
		jsoncol.Decode("compression.decodeSessionStateFields/cmp_q", []byte(raw), &st.CompressionQuality)
	}
	if raw := fields["san_stats"]; raw != "" {
		jsoncol.Decode("compression.decodeSessionStateFields/san_stats", []byte(raw), &st.SanitizeStats)
	}
	if raw := fields["cmp_src"]; raw != "" {
		jsoncol.Decode("compression.decodeSessionStateFields/cmp_src", []byte(raw), &st.CompressionSourceSnapshot)
	}
	if raw := fields["raw_snap"]; raw != "" {
		jsoncol.Decode("compression.decodeSessionStateFields/raw_snap", []byte(raw), &st.RawSnapshot)
	}
	if raw := fields["cmp_snap"]; raw != "" {
		jsoncol.Decode("compression.decodeSessionStateFields/cmp_snap", []byte(raw), &st.CompressedSnapshot)
	}
	if raw := fields["san_snap"]; raw != "" {
		jsoncol.Decode("compression.decodeSessionStateFields/san_snap", []byte(raw), &st.SanitizedSnapshot)
	}
	if raw := fields["san_msg_refs"]; raw != "" {
		// 0458 §1.3 同族收口（2026-10-03）：与 algn 同批，坏数据留 Warn 不再静默。
		jsoncol.Decode("compression.decodeSessionStateFields/san_msg_refs", []byte(raw), &st.SanitizeMessageRefs)
	}
	return nil
}

// ──────────────────────────────────────────────────────────────────────────────
// Helpers
// ──────────────────────────────────────────────────────────────────────────────

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return fmt.Sprintf("%x", h)
}

// BuildSummaryMarker constructs the smm_v1 marker string from the first
// 128 bytes of a summary message content. The marker is stored in
// SessionState.SummaryMarker and also injected into compression_meta JSONB.
// An empty summary has no marker; callers use that to represent a mechanical
// fallback, which must not masquerade as an LLM summary.
func BuildSummaryMarker(summaryContent string) string {
	if summaryContent == "" {
		return ""
	}
	prefix := summaryContent
	if len(prefix) > 128 {
		prefix = prefix[:128]
	}
	h := sha256.Sum256([]byte(prefix))
	return fmt.Sprintf("%s%x]", CompactionMarkerPrefix, h[:8])
}

// SetCutMarker stores a CutMarker into the SessionState fields.
// The SummaryText is preserved for L1 but NOT serialised to Redis
// (it's too large). Callers should retrieve SummaryText from L1 only.
func (s *SessionState) SetCutMarker(cm CutMarker) {
	s.HasCutMarker = true
	s.CutCreatedAt = cm.CreatedAt
	s.CutSourceMsgs = cm.SourceMsgCount
	s.CutSystemMsgs = cm.SystemMsgCount
	s.CutIndex = cm.CutIndex
	s.CutStrategy = cm.Strategy
	s.CutBytesBefore = cm.BytesBefore
	s.CutBytesAfter = cm.BytesAfter
	s.SummaryMarker = cm.SummaryMarker
	s.CutPreSanitizeStart = cm.PreSanitizeOffsetRange[0]
	s.CutPreSanitizeEnd = cm.PreSanitizeOffsetRange[1]
}

// ToCutMarker reconstructs a CutMarker from SessionState fields.
// SummaryText is NOT available from Redis (only L1) — callers that need
// the actual summary text should use the L1-cached body instead.
func (s *SessionState) ToCutMarker(summaryText string) *CutMarker {
	if s == nil || !s.HasCutMarker {
		return nil
	}
	return &CutMarker{
		Version:                cutMarkerSchemaVersion,
		CreatedAt:              s.CutCreatedAt,
		SourceMsgCount:         s.CutSourceMsgs,
		SystemMsgCount:         s.CutSystemMsgs,
		CutIndex:               s.CutIndex,
		SummaryMarker:          s.SummaryMarker,
		Strategy:               s.CutStrategy,
		BytesBefore:            s.CutBytesBefore,
		BytesAfter:             s.CutBytesAfter,
		PreSanitizeOffsetRange: [2]int{s.CutPreSanitizeStart, s.CutPreSanitizeEnd},
		SummaryText:            summaryText,
	}
}

// ClearCutMarker removes any cached cut marker state.
func (s *SessionState) ClearCutMarker() {
	if s == nil {
		return
	}
	s.HasCutMarker = false
	s.CutCreatedAt = 0
	s.CutSourceMsgs = 0
	s.CutSystemMsgs = 0
	s.CutIndex = 0
	s.CutStrategy = ""
	s.CutBytesBefore = 0
	s.CutBytesAfter = 0
	s.CutPreSanitizeStart = 0
	s.CutPreSanitizeEnd = 0
	s.SummaryMarker = ""
}

// ──────────────────────────────────────────────────────────────────────────────
// v6: Audited state helpers
// ──────────────────────────────────────────────────────────────────────────────

// ApprovalState values that may appear in SessionState.ApprovalStatus.
// These are mirrored from sessionaudit.ApprovalStatus to keep this package
// import-free (callers in hot paths may read the field without needing to
// import the audit domain).
const (
	ApprovalStatePending  = "pending"
	ApprovalStateApproved = "approved"
	ApprovalStateRejected = "rejected"
	ApprovalStateTimeout  = "timeout"
)

// Optimization tag values for SessionState.OptimizationApplied.
const (
	OptStripTools       = "strip_tools"
	OptCompressThinking = "compress_thinking"
	OptSummarize        = "summarize"
)

// MarkAudited stamps the audit metadata into the session state.
//
// score and security are 0-10 integers (caller is responsible for clamping).
// sensitiveWordsHit / piiStripped reflect the boolean outcomes of the
// detection phase. approvalPending should be true when the detection
// decided DecisionNeedApproval — this triggers the ApprovalHook to create
// an approval record.
func (s *SessionState) MarkAudited(now time.Time, score, security int, sensitiveWordsHit, piiStripped, approvalPending bool) {
	if s == nil {
		return
	}
	s.AuditedAt = now.Unix()
	s.AuditScore = score
	s.SecurityScore = security
	s.SensitiveDetected = sensitiveWordsHit
	s.PIIStripped = piiStripped
	if approvalPending {
		s.ApprovalStatus = ApprovalStatePending
	}
}

// SetApprovalID records the approval_queue UUID once ApprovalManager.Create
// has returned it. Cleared when the state is no longer pending.
func (s *SessionState) SetApprovalID(id string) {
	if s == nil {
		return
	}
	s.ApprovalID = id
	if id != "" {
		s.ApprovalStatus = ApprovalStatePending
	}
}

// SetApprovalResult updates the final approval verdict and clears the
// pending ID. Callers should invoke this from the resume handler.
func (s *SessionState) SetApprovalResult(state string) {
	if s == nil {
		return
	}
	s.ApprovalStatus = state
	// When the approval lifecycle ends we drop the ID — keep ApprovalID
	// for audit trail purposes (it can be looked up in approval_queue).
}

// ApplyOptimization stamps the optimization tag that was applied to the
// session (strip_tools / compress_thinking / summarize). The tag is
// informational and lets downstream hooks skip redundant work.
func (s *SessionState) ApplyOptimization(tag string) {
	if s == nil {
		return
	}
	if tag == "" {
		return
	}
	s.OptimizationApplied = tag
}

// IsApprovalPending reports whether this session is currently waiting on
// human review. Returns false for nil receivers.
func (s *SessionState) IsApprovalPending() bool {
	if s == nil {
		return false
	}
	return s.ApprovalStatus == ApprovalStatePending
}
