// Package compressor - session_cache.go (v3 T24)
//
// Three-tier session state cache for the v3 session-level compressor:
//
//	L1 — in-process sync.Map (< 0.1 ms, bounded by MaxL1Sessions)
//	L2 — Redis Hash session:sc:{tenantID}:{gwSessionID}:v1 (~2 ms RTT)
//	L3 — PostgreSQL request_logs fallback (cold-start / Redis miss / schema
//	     upgrade)
//
// SessionState captures what the gateway last forwarded to the LLM for a
// given gw_session_id so the next request can run a message-level LCS diff
// and only append new turns, avoiding redundant re-send of the full history.
//
// summary_marker protocol (smm_v1):
//
//	When the session compressor produces an LLM summary, it writes
//	"smm_v1:<sha256-of-summary-content-prefix>" into SessionState.SummaryMarker
//	and also serialises it into compression_meta JSONB under the key
//	"summary_marker". The diff algorithm (diff.go) treats any message whose
//	content starts with compactionMarkerPrefix as a summary boundary — it is
//	kept verbatim in the rebuilt body and skipped by LCS diff so it is never
//	re-summarised.
//
// Schema versioning:
//
//	Every Redis Hash entry carries schema_version=1. If GetOrLoad finds a
//	different version it drops the entry and returns nil (graceful downgrade:
//	the caller falls back to treating the request as a fresh session). This
//	prevents old-format state from poisoning a new schema.

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
}

// MsgHash is one entry in the outbound_msg_hashes JSONB array.
type MsgHash struct {
	Index  int    `json:"index"`
	SHA256 string `json:"sha256"`
}

// SessionCacheBackend is the minimal Redis interface required by SessionCache.
// Using an interface allows tests to inject a mock without a real Redis.
type SessionCacheBackend interface {
	// HSet sets fields in a Redis hash and resets the TTL.
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
	// LastOutboundForSession queries request_logs for the most recent row
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
	key   string // tenantID + ":" + gwSessionID (back-reference for eviction)
	state *SessionState
	body  []byte // last outbound body bytes (nil if evicted from L1 to save RAM)
	bytes int    // len(body) + len(key) + l1EntryOverheadBytes (D3: byte-budget tracking)
	elem  *list.Element
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

	redis      SessionCacheBackend // nil = L2 disabled (tests / no Redis)
	db         SessionCacheDB      // nil = L3 disabled (tests / no DB)
	turnReader *v2.TurnReader      // V2-P2.5: L3 reads session_bodies when wired
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
// to the legacy request_logs reader.
func (c *SessionCache) SetTurnReader(reader *v2.TurnReader) {
	if c == nil {
		return
	}
	c.turnReader = reader
}

func l1Key(tenantID, gwSessionID string) string {
	return tenantID + ":" + gwSessionID
}

func redisKey(tenantID, gwSessionID string) string {
	return "session:sc:" + tenantID + ":" + gwSessionID + ":v1"
}

// GetOrLoad returns the SessionState and last outbound body for the session,
// or (nil, nil, nil) when the session is new / unknown.
// Tier priority: L1 → L2 → L3. A cache-miss at one tier is back-filled
// from the next tier before returning.
//
// KILL-SWITCH (2026-07-12 incident): when settings.IsEnabled("session_cache")
// is false (env KILL_SESSION_CACHE=1), the entire cache subsystem is bypassed
// and we behave as if every session is brand-new. This isolates compression
// issues from request hot-paths during incident response.
func (c *SessionCache) GetOrLoad(ctx context.Context, tenantID, gwSessionID string) (state *SessionState, lastOutboundBody []byte, err error) {
	if gwSessionID == "" {
		return nil, nil, nil
	}
	if !settings.IsEnabled("session_cache") {
		return nil, nil, nil
	}
	key := l1Key(tenantID, gwSessionID)

	// L1 hit.
	c.mu.Lock()
	if e, ok := c.l1[key]; ok {
		c.ll.MoveToFront(e.elem) // O(1) LRU promote.
		st := *e.state           // copy
		body := e.body
		c.mu.Unlock()
		return &st, body, nil
	}
	c.mu.Unlock()

	// L2: Redis. Redis intentionally stores metadata only, so a metadata hit
	// must rehydrate the last outbound body from L3 before returning. Without
	// this step an L1 eviction/process switch would look like a brand-new
	// session and undo the previously compressed history.
	if c.redis != nil {
		st, body, rerr := c.loadFromRedis(ctx, tenantID, gwSessionID)
		if rerr != nil {
			slog.Warn("session_cache: redis load error", "session", gwSessionID, "error", rerr)
		} else if st != nil {
			if len(body) == 0 && (c.turnReader != nil || c.db != nil) {
				_, persistedBody, derr := c.loadFromDB(ctx, tenantID, gwSessionID)
				if derr != nil {
					slog.Warn("session_cache: l2 body rehydrate failed", "session", gwSessionID, "error", derr)
				} else if len(persistedBody) > 0 {
					// V2 stores the message array rather than the full provider
					// request envelope, so its serialized hash cannot be compared
					// directly with the legacy full-body hash.
					if c.turnReader != nil || st.LastOutboundHash == "" || sha256Hex(persistedBody) == st.LastOutboundHash {
						body = persistedBody
					} else {
						slog.Warn("session_cache: l3 body hash mismatch, trying legacy source",
							"session", gwSessionID)
						_, legacyBody, legacyErr := c.loadFromLegacyDB(ctx, tenantID, gwSessionID)
						if legacyErr != nil {
							slog.Warn("session_cache: legacy body rehydrate failed",
								"session", gwSessionID, "error", legacyErr)
						} else if len(legacyBody) > 0 &&
							(st.LastOutboundHash == "" || sha256Hex(legacyBody) == st.LastOutboundHash) {
							body = legacyBody
						}
					}
				}
			}
			c.setL1(key, st, body)
			return st, body, nil
		}
	}

	// L3: DB cold-start.
	if c.turnReader != nil || c.db != nil {
		st, body, derr := c.loadFromDB(ctx, tenantID, gwSessionID)
		if derr != nil {
			slog.Warn("session_cache: db load error", "session", gwSessionID, "error", derr)
		} else if st != nil {
			c.setL1(key, st, body)
			// Back-fill L2 so subsequent requests are fast.
			if c.redis != nil {
				if werr := c.saveToRedis(ctx, tenantID, gwSessionID, st); werr != nil {
					slog.Warn("session_cache: redis backfill failed", "session", gwSessionID, "error", werr)
				}
			}
			return st, body, nil
		}
	}

	return nil, nil, nil
}

// Set persists updated session state to L1 and L2 after a request completes.
// outboundBody is stored in L1 only (not Redis) to avoid large blobs in Redis.
//
// KILL-SWITCH (2026-07-12 incident): see GetOrLoad. When session_cache is
// disabled, Set is a no-op.
func (c *SessionCache) Set(ctx context.Context, tenantID, gwSessionID string, state *SessionState, outboundBody []byte) error {
	if gwSessionID == "" || state == nil {
		return nil
	}
	if !settings.IsEnabled("session_cache") {
		return nil
	}
	key := l1Key(tenantID, gwSessionID)
	c.setL1(key, state, outboundBody)

	if c.redis != nil {
		if err := c.saveToRedis(ctx, tenantID, gwSessionID, state); err != nil {
			slog.Warn("session_cache: redis save error", "session", gwSessionID, "error", err)
			// Non-fatal: L1 still has fresh data.
		}
	}
	return nil
}

// Invalidate removes a session from all tiers (e.g., on session destruction).
//
// KILL-SWITCH (2026-07-12 incident): when session_cache is disabled,
// Invalidate is a no-op.
func (c *SessionCache) Invalidate(ctx context.Context, tenantID, gwSessionID string) {
	if !settings.IsEnabled("session_cache") {
		return
	}
	key := l1Key(tenantID, gwSessionID)
	c.mu.Lock()
	if e, ok := c.l1[key]; ok {
		c.ll.Remove(e.elem)
		delete(c.l1, key)
		c.curBytes -= e.bytes // D3: decrement aggregate byte tracker
	}
	c.mu.Unlock()
	if c.redis != nil {
		if err := c.redis.Del(ctx, redisKey(tenantID, gwSessionID)); err != nil {
			slog.Warn("session_cache: redis invalidate error", "session", gwSessionID, "error", err)
		}
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Internal helpers
// ──────────────────────────────────────────────────────────────────────────────

func (c *SessionCache) setL1(key string, state *SessionState, body []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := *state // copy
	entryBytes := len(body) + len(key) + l1EntryOverheadBytes

	// Update-in-place + promote to front if the key already exists.
	if existing, ok := c.l1[key]; ok {
		// Adjust curBytes for the delta (new - old).
		c.curBytes -= existing.bytes
		c.curBytes += entryBytes
		existing.state = &st
		existing.body = body
		existing.bytes = entryBytes
		c.ll.MoveToFront(existing.elem)
		return
	}

	// New entry: push to front, evict the LRU (back) if over capacity OR bytes.
	entry := &l1Entry{key: key, state: &st, body: body, bytes: entryBytes}
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

func (c *SessionCache) loadFromRedis(ctx context.Context, tenantID, gwSessionID string) (*SessionState, []byte, error) {
	fields, err := c.redis.HGetAll(ctx, redisKey(tenantID, gwSessionID))
	if err != nil {
		return nil, nil, err
	}
	if len(fields) == 0 {
		return nil, nil, nil
	}
	var st SessionState
	if err := decodeSessionStateFields(fields, &st); err != nil {
		slog.Warn("session_cache: redis schema mismatch, dropping entry",
			"session", gwSessionID, "error", err)
		return nil, nil, nil
	}
	if st.SchemaVersion != schemaVersion {
		// Schema changed — treat as cache miss.
		return nil, nil, nil
	}
	// Body is not stored in Redis (too large); caller will re-read from L3 if needed.
	return &st, nil, nil
}

func (c *SessionCache) saveToRedis(ctx context.Context, tenantID, gwSessionID string, state *SessionState) error {
	key := redisKey(tenantID, gwSessionID)
	fields := encodeSessionStateFields(state)
	if err := c.redis.HSet(ctx, key, fields...); err != nil {
		return err
	}
	return c.redis.Expire(ctx, key, sessionCacheRedisTTL())
}

func (c *SessionCache) loadFromDB(ctx context.Context, tenantID, gwSessionID string) (*SessionState, []byte, error) {
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
	if c.db == nil {
		return nil, nil, nil
	}
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
	// v5: Smart compression cut marker
	if st.HasCutMarker {
		fields = append(fields, "hcm", "1")
		if st.CutCreatedAt > 0 {
			fields = append(fields, "cm_ts", fmt.Sprintf("%d", st.CutCreatedAt))
		}
		if st.CutSourceMsgs > 0 {
			fields = append(fields, "cm_src", fmt.Sprintf("%d", st.CutSourceMsgs))
		}
		if st.CutSystemMsgs > 0 {
			fields = append(fields, "cm_sys", fmt.Sprintf("%d", st.CutSystemMsgs))
		}
		if st.CutIndex >= 0 {
			fields = append(fields, "cm_ci", fmt.Sprintf("%d", st.CutIndex))
		}
		if st.CutStrategy != "" {
			fields = append(fields, "cm_strat", st.CutStrategy)
		}
		if st.CutBytesBefore > 0 {
			fields = append(fields, "cm_bb", fmt.Sprintf("%d", st.CutBytesBefore))
		}
		if st.CutBytesAfter > 0 {
			fields = append(fields, "cm_ba", fmt.Sprintf("%d", st.CutBytesAfter))
		}
	}
	// v6: Audited state — only emit non-zero values to keep Redis hash small
	// and to remain backward compatible with older readers that only know
	// the v5 field set.
	if st.AuditedAt > 0 {
		fields = append(fields, "aud_at", fmt.Sprintf("%d", st.AuditedAt))
	}
	if st.AuditScore > 0 {
		fields = append(fields, "aud_sc", fmt.Sprintf("%d", st.AuditScore))
	}
	if st.SecurityScore > 0 {
		fields = append(fields, "sec_sc", fmt.Sprintf("%d", st.SecurityScore))
	}
	if st.SensitiveDetected {
		fields = append(fields, "sen_det", "1")
	}
	if st.PIIStripped {
		fields = append(fields, "pii_strip", "1")
	}
	if st.ApprovalStatus != "" {
		fields = append(fields, "app_st", st.ApprovalStatus)
	}
	if st.ApprovalID != "" {
		fields = append(fields, "app_id", st.ApprovalID)
	}
	if st.OptimizationApplied != "" {
		fields = append(fields, "opt_app", st.OptimizationApplied)
	}
	// v7 (O-2): AlignmentMap — serialized as a JSON array under "algn".
	if len(st.AlignmentMap) > 0 {
		if b, err := json.Marshal(st.AlignmentMap); err == nil {
			fields = append(fields, "algn", string(b))
		}
	}
	return fields
}

func decodeSessionStateFields(fields map[string]string, st *SessionState) error {
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
	// v7 (O-2): AlignmentMap — best-effort parse; a corrupt entry degrades
	// to nil rather than failing the whole state decode.
	if raw, ok := fields["algn"]; ok && raw != "" {
		if err := json.Unmarshal([]byte(raw), &st.AlignmentMap); err != nil {
			st.AlignmentMap = nil
		}
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
// 128 chars of a summary message content. The marker is stored in
// SessionState.SummaryMarker and also injected into compression_meta JSONB.
func BuildSummaryMarker(summaryContent string) string {
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
}

// ToCutMarker reconstructs a CutMarker from SessionState fields.
// SummaryText is NOT available from Redis (only L1) — callers that need
// the actual summary text should use the L1-cached body instead.
func (s *SessionState) ToCutMarker(summaryText string) *CutMarker {
	if s == nil || !s.HasCutMarker {
		return nil
	}
	return &CutMarker{
		Version:        cutMarkerSchemaVersion,
		CreatedAt:      s.CutCreatedAt,
		SourceMsgCount: s.CutSourceMsgs,
		SystemMsgCount: s.CutSystemMsgs,
		CutIndex:       s.CutIndex,
		SummaryMarker:  s.SummaryMarker,
		Strategy:       s.CutStrategy,
		BytesBefore:    s.CutBytesBefore,
		BytesAfter:     s.CutBytesAfter,
		SummaryText:    summaryText,
	}
}

// ClearCutMarker removes any cached cut marker state.
func (s *SessionState) ClearCutMarker() {
	s.HasCutMarker = false
	s.CutCreatedAt = 0
	s.CutSourceMsgs = 0
	s.CutSystemMsgs = 0
	s.CutIndex = 0
	s.CutStrategy = ""
	s.CutBytesBefore = 0
	s.CutBytesAfter = 0
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
