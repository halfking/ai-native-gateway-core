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
)

const (
	// schemaVersion is the current Redis Hash schema. Bump when SessionState
	// fields change in a backward-incompatible way.
	schemaVersion = 1

	// redisKeyTTL is how long Redis keeps the session hash after the last
	// write. 30 minutes covers the typical idle-between-turns gap; the
	// sliding-window idle trigger fires at 5 minutes anyway, so 30 min is
	// a generous safety margin.
	redisKeyTTL = 30 * time.Minute

	// l1MaxSessions is the maximum number of sessions held in the in-process
	// L1. The L1 is a true O(1) LRU (container/list doubly-linked list +
	// map), so when this capacity is exceeded the LEAST-RECENTLY-USED entry
	// is evicted on the next access. This keeps hot sessions resident while
	// bounding memory. (Previously a sync.Map with first-seen-first-evicted
	// heuristic — replaced 2026-07-06 after borrowing rtk's emphasis on
	// deterministic, recoverable state; the old heuristic could evict an
	// active session and force an L2/L3 round-trip on its next turn.)
	l1MaxSessions = 1024

	// compactionMarkerPrefix is the content prefix used to identify summary
	// boundary messages injected by the session compression. Any message
	// whose "content" field starts with this prefix is treated as a
	// compacted summary and is skipped by the LCS diff.
	CompactionMarkerPrefix = "[smm_v1:"
)

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
	elem  *list.Element
}

// SessionCache provides three-tier session state caching.
//
// L1 is a true O(1) LRU: ll (a doubly-linked list via container/list) orders
// entries from most-recently-used (front) to least-recently-used (back), and
// l1 maps the key to its list.Element for O(1) lookup + promotion. An access
// moves the element to the front; eviction pops the back element.
type SessionCache struct {
	mu sync.Mutex
	ll *list.List          // front = MRU, back = LRU; O(1) promote/evict
	l1 map[string]*l1Entry // key = tenantID+":"+gwSessionID → entry (entry.elem is the list node)

	redis SessionCacheBackend // nil = L2 disabled (tests / no Redis)
	db    SessionCacheDB      // nil = L3 disabled (tests / no DB)
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
func (c *SessionCache) GetOrLoad(ctx context.Context, tenantID, gwSessionID string) (state *SessionState, lastOutboundBody []byte, err error) {
	if gwSessionID == "" {
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

	// L2: Redis.
	if c.redis != nil {
		st, body, rerr := c.loadFromRedis(ctx, tenantID, gwSessionID)
		if rerr != nil {
			slog.Warn("session_cache: redis load error", "session", gwSessionID, "error", rerr)
		} else if st != nil {
			c.setL1(key, st, body)
			return st, body, nil
		}
	}

	// L3: DB cold-start.
	if c.db != nil {
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
func (c *SessionCache) Set(ctx context.Context, tenantID, gwSessionID string, state *SessionState, outboundBody []byte) error {
	if gwSessionID == "" || state == nil {
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
func (c *SessionCache) Invalidate(ctx context.Context, tenantID, gwSessionID string) {
	key := l1Key(tenantID, gwSessionID)
	c.mu.Lock()
	if e, ok := c.l1[key]; ok {
		c.ll.Remove(e.elem)
		delete(c.l1, key)
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
	// Update-in-place + promote to front if the key already exists.
	if existing, ok := c.l1[key]; ok {
		existing.state = &st
		existing.body = body
		c.ll.MoveToFront(existing.elem)
		return
	}
	// New entry: push to front, evict the LRU (back) if over capacity.
	entry := &l1Entry{key: key, state: &st, body: body}
	entry.elem = c.ll.PushFront(entry)
	c.l1[key] = entry
	for c.ll.Len() > l1MaxSessions {
		// Evict least-recently-used (back of the list).
		if back := c.ll.Back(); back != nil {
			if ev, ok := back.Value.(*l1Entry); ok {
				c.ll.Remove(back)
				delete(c.l1, ev.key)
			} else {
				break // defensive: should never happen
			}
		} else {
			break
		}
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
	return c.redis.Expire(ctx, key, redisKeyTTL)
}

func (c *SessionCache) loadFromDB(ctx context.Context, tenantID, gwSessionID string) (*SessionState, []byte, error) {
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
