// Package admin — OBS-BE2 (V3.3-OBS, 2026-08-15): request_lifecycle /
// child_request SSE envelope extensions (docs/会话优化v3/24号 §3/§4).
//
// BE2 only CONSUMES the stable llmgw:live:actions contract written by
// internal/liveactions (OBS-BE1). The request hot path is untouched: the
// hub polls the bounded Redis LIST on a 250ms tick, aggregates the unseen
// entries and pushes them as batched request_lifecycle frames, keeping
// end-to-end delivery inside the ≤500ms budget (27号 §3).
//
// Tenant isolation: ActionEvent froze its vocabulary WITHOUT a tenant id
// (24号 §2), while llmgw:live:actions is a global list. The hub therefore
// resolves request ownership before any delivery:
//
//   - an in-memory request_id → tenant index fed by the request stream
//     (broadcast path) and the initial replay;
//   - a pipelined fallback GET against the Redis global request detail key.
//
// Actions whose owner cannot be proven are delivered to super admins only
// — never guessed into a tenant lane. state_change (node-dimension, empty
// request_id) is excluded from lifecycle frames entirely: 24号 §2 routes
// it through the node_update / state_transition channels instead.
package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"sync/atomic"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/liveactions"
	"github.com/redis/go-redis/v9"
)

const (
	// defaultActionPollInterval is the llmgw:live:actions poll tick. Two
	// consecutive ticks (poll + one aggregation window) bound delivery
	// inside the ≤500ms contract budget.
	defaultActionPollInterval = 250 * time.Millisecond
	// defaultActionReplayLimit matches 24号 §4 (initial replay 最近 200 条).
	defaultActionReplayLimit = 200
	// actionScanPerPoll caps one poll's LRANGE window. Newest entries live
	// at the head (LPUSH), so a scan that never meets the cursor delivers
	// the newest 500 at-least-once; the frontend collapses duplicates by
	// (request_id, seq).
	actionScanPerPoll = 500
	// actionTenantIndexCap bounds the ownership index (in-flight requests
	// only; evicted entries fall back to the Redis detail lookup).
	actionTenantIndexCap = 4096
	// actionTenantNegCacheTTL bounds re-lookups of request ids that have no
	// Redis detail (e.g. actions fired before any request record existed).
	actionTenantNegCacheTTL = 5 * time.Second
	// actionReadTimeout bounds one poll/replay/ownership read.
	actionReadTimeout = 2 * time.Second
)

// actionUniqueKey identifies one stored list entry for cursor tracking.
// (request_id, seq) is unique per emitter process; action + RFC3339Nano ts
// additionally separates any residual node-dimension rows (seq 0).
func actionUniqueKey(ev liveactions.ActionEvent) string {
	return fmt.Sprintf("%s|%d|%s|%s", ev.RequestID, ev.Seq, ev.Action, ev.Ts.UTC().Format(time.RFC3339Nano))
}

// isRequestScopedAction reports whether one decoded entry belongs on the
// request_lifecycle channel. Node-dimension state_change rows (the only
// kind allowed to carry an empty request_id, 24号 §2) are dropped here —
// they ride node_update / state_transition, and their (empty id, seq 0)
// shape would also defeat client-side (request_id, seq) deduplication.
func isRequestScopedAction(ev liveactions.ActionEvent) bool {
	return ev.RequestID != ""
}

// actionLess orders events ascending by (ts, seq) — the replay contract
// order (24号 §4); the frontend sorts by seq within a request but the
// batch frame itself is delivered oldest → newest.
func actionLess(a, b liveactions.ActionEvent) bool {
	if !a.Ts.Equal(b.Ts) {
		return a.Ts.Before(b.Ts)
	}
	return a.Seq < b.Seq
}

// sortActionsStable sorts a batch oldest → newest.
func sortActionsStable(actions []liveactions.ActionEvent) {
	sort.SliceStable(actions, func(i, j int) bool { return actionLess(actions[i], actions[j]) })
}

// flattenActionEvent renders one ActionEvent as the frontend wire shape:
// the emitter's Detail map (e.g. queue_depth, ttfb_ms, from_credential_id)
// is flattened to the top level so the FE1 ActionEvent contract is met
// without a second DTO. Detail never carries body content or secrets
// (BE1 安全红线), so promotion across the wire is safe.
func flattenActionEvent(ev liveactions.ActionEvent) map[string]any {
	b, err := json.Marshal(ev)
	if err != nil {
		slog.Debug("live actions flatten: marshal failed", "action", ev.Action, "request_id", ev.RequestID, "err", err.Error())
		return map[string]any{}
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil || m == nil {
		slog.Debug("live actions flatten: unmarshal failed", "action", ev.Action, "request_id", ev.RequestID, "err", err)
		return map[string]any{}
	}
	if detail, ok := m["detail"].(map[string]any); ok {
		delete(m, "detail")
		for k, v := range detail {
			if _, taken := m[k]; !taken {
				m[k] = v
			}
		}
	}
	return m
}

// normalizeLiveRequestType maps the persisted request_type vocabulary
// (request_logs.request_type, migration 510: main/title_gen/summary/
// sensitive_check/compression/probe) onto the frozen 24号 §3 wire enum
// chat|title|summary|sensitive_word|probe|unknown. Main requests return ""
// so the requestType alias stays absent on ordinary frames (optional 字段,
// 零值不冒充).
func normalizeLiveRequestType(raw string) string {
	switch raw {
	case "", "main":
		return ""
	case "title_gen", "title":
		return "title"
	case "summary":
		return "summary"
	case "sensitive_check", "sensitive_word":
		return "sensitive_word"
	case "probe":
		return "probe"
	default:
		// compression 及任何未分类值：诚实归为 unknown，不伪造词表。
		return "unknown"
	}
}

// actionWirePayload shapes the envelope's "action" field: a single object
// for one event (24号 §3 示例), an array for an aggregated batch frame.
// Both shapes are accepted by the frontend store.
func actionWirePayload(actions []liveactions.ActionEvent) any {
	if len(actions) == 1 {
		return flattenActionEvent(actions[0])
	}
	out := make([]map[string]any, len(actions))
	for i, a := range actions {
		out[i] = flattenActionEvent(a)
	}
	return out
}

// MarshalJSON emits the frozen snake_case fields plus the V3.3 camelCase
// lifecycle aliases (24号 §3) when the corresponding values are set.
// Legacy keys are never removed — 13号 contract compatibility.
func (r LiveRequest) MarshalJSON() ([]byte, error) {
	type liveRequestWire LiveRequest // sheds the MarshalJSON method, no recursion
	base, err := json.Marshal(liveRequestWire(r))
	if err != nil {
		return nil, fmt.Errorf("marshal live request wire payload: %w", err)
	}
	if r.ParentRequestID == "" && r.RequestType == "" {
		return base, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(base, &m); err != nil {
		// Defensive: fall back to the plain payload rather than failing the frame.
		return base, nil
	}
	if r.ParentRequestID != "" {
		if v, mErr := json.Marshal(r.ParentRequestID); mErr == nil {
			m["parentRequestId"] = v
		}
	}
	if r.RequestType != "" {
		if v, mErr := json.Marshal(r.RequestType); mErr == nil {
			m["requestType"] = v
		}
	}
	return json.Marshal(m)
}

// ── tenant ownership index ─────────────────────────────────────────────────

// rememberActionTenant records request ownership for the lifecycle filter.
// Called from the broadcast path and the initial replay; safe for any
// tenant spelling (normalized here).
func (h *LiveStreamSSEHub) rememberActionTenant(requestID, tenantID string) {
	if h == nil || requestID == "" {
		return
	}
	h.actionMu.Lock()
	h.actionTenantIndex[requestID] = normalizeLiveStreamTenant(tenantID)
	delete(h.actionTenantMiss, requestID)
	if len(h.actionTenantIndex) > actionTenantIndexCap {
		// Bounded cache, not an LRU: random eviction is fine because the
		// Redis detail fallback re-resolves anything evicted.
		for k := range h.actionTenantIndex {
			delete(h.actionTenantIndex, k)
			if len(h.actionTenantIndex) <= actionTenantIndexCap {
				break
			}
		}
	}
	h.actionMu.Unlock()
}

// actionTenant returns the known owner tenant of one request id.
func (h *LiveStreamSSEHub) actionTenant(requestID string) (string, bool) {
	if h == nil || requestID == "" {
		return "", false
	}
	h.actionMu.Lock()
	defer h.actionMu.Unlock()
	t, ok := h.actionTenantIndex[requestID]
	return t, ok
}

// resolveActionTenants resolves ownership for every request-scoped action
// that is not yet in the index, via one pipelined GET against the global
// request detail keys. Misses enter a bounded negative cache so a burst of
// pre-record actions does not re-query Redis on every tick.
func (h *LiveStreamSSEHub) resolveActionTenants(ctx context.Context, actions []liveactions.ActionEvent) {
	if h == nil || h.cfg.RedisClient == nil {
		return
	}
	now := time.Now()
	var missing []string
	seen := make(map[string]struct{})
	h.actionMu.Lock()
	for _, a := range actions {
		if a.RequestID == "" {
			continue
		}
		if _, known := h.actionTenantIndex[a.RequestID]; known {
			continue
		}
		if t, recent := h.actionTenantMiss[a.RequestID]; recent && now.Sub(t) < actionTenantNegCacheTTL {
			continue
		}
		if _, dup := seen[a.RequestID]; dup {
			continue
		}
		seen[a.RequestID] = struct{}{}
		missing = append(missing, a.RequestID)
	}
	h.actionMu.Unlock()
	if len(missing) == 0 {
		return
	}

	ctx, cancel := context.WithTimeout(ctx, actionReadTimeout)
	defer cancel()
	pipe := h.cfg.RedisClient.Pipeline()
	cmds := make([]*redis.StringCmd, len(missing))
	for i, id := range missing {
		cmds[i] = pipe.Get(ctx, liveStreamGlobalRequestDetailKey(id))
	}
	if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
		slog.Debug("live actions ownership lookup failed", "err", err.Error())
	}
	for i, cmd := range cmds {
		data, err := cmd.Result()
		if err != nil {
			// Miss (redis.Nil) or read error: negative-cache and let the
			// super-admin-only policy cover this action until resolved.
			h.actionMu.Lock()
			h.actionTenantMiss[missing[i]] = now
			h.actionMu.Unlock()
			continue
		}
		req, err := unmarshalLiveRequestRedisPayload(data)
		if err != nil {
			continue
		}
		h.rememberActionTenant(missing[i], req.TenantID)
	}
}

// actionsVisibleToTenant filters one action batch for a tenant-admin
// client down to requests it owns. Unknown ownership is dropped — never
// broadcast cross-tenant.
func (h *LiveStreamSSEHub) actionsVisibleToTenant(actions []liveactions.ActionEvent, tenantID string) []liveactions.ActionEvent {
	tenantID = normalizeLiveStreamTenant(tenantID)
	out := make([]liveactions.ActionEvent, 0, len(actions))
	for _, a := range actions {
		if owner, ok := h.actionTenant(a.RequestID); ok && owner == tenantID {
			out = append(out, a)
		}
	}
	return out
}

// ── live polling ────────────────────────────────────────────────────────────

// pollLiveActions reads the newest llmgw:live:actions entries and fans out
// everything appended since the previous tick as one aggregated batch.
func (h *LiveStreamSSEHub) pollLiveActions() {
	if h == nil || h.cfg.RedisClient == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), actionReadTimeout)
	defer cancel()
	entries, err := h.cfg.RedisClient.LRange(ctx, liveactions.RedisKey, 0, actionScanPerPoll-1).Result()
	if err != nil {
		atomic.AddInt64(&h.actionScanErrors, 1)
		slog.Debug("live actions poll failed", "err", err.Error())
		return
	}
	h.deliverNewActions(ctx, entries)
}

// deliverNewActions advances the cursor over the scanned entries. The list
// is newest-first (LPUSH): iteration stops at the cursor; everything before
// it is fresh. On the very first poll the cursor is armed at the head and
// nothing is delivered — connected clients rebuild history via the
// initial_data action replay instead.
func (h *LiveStreamSSEHub) deliverNewActions(ctx context.Context, entries []string) {
	if len(entries) == 0 {
		return
	}
	type keyed struct {
		ev  liveactions.ActionEvent
		key string
	}
	decoded := make([]keyed, 0, len(entries))
	for _, raw := range entries {
		var ev liveactions.ActionEvent
		if err := json.Unmarshal([]byte(raw), &ev); err != nil || ev.Action == "" {
			continue // malformed entry: skip, never kill the stream
		}
		if !isRequestScopedAction(ev) {
			continue // state_change 等：不占 lifecycle 光标（见 isRequestScopedAction）
		}
		decoded = append(decoded, keyed{ev: ev, key: actionUniqueKey(ev)})
	}
	if len(decoded) == 0 {
		return
	}

	h.actionMu.Lock()
	cursor := h.actionCursor
	h.actionMu.Unlock()

	var fresh []liveactions.ActionEvent
	if cursor == "" {
		// Arm only: history is replayed per-client on connect.
		fresh = nil
	} else {
		for _, k := range decoded {
			if k.key == cursor {
				break
			}
			fresh = append(fresh, k.ev)
		}
		// Cursor not found within the scan window (burst beyond 500 or the
		// entry trimmed): deliver the scanned newest entries at-least-once;
		// the frontend replaces duplicates by (request_id, seq).
	}

	h.actionMu.Lock()
	h.actionCursor = decoded[0].key
	h.actionMu.Unlock()

	if len(fresh) == 0 {
		return
	}
	sortActionsStable(fresh)
	h.resolveActionTenants(ctx, fresh)
	h.fanOutLifecycleActions(fresh)
}

// fanOutLifecycleActions delivers one aggregated request_lifecycle frame
// per distinct audience: super admins receive the full batch, each tenant
// its filtered subset. Serialisation happens once per distinct payload.
func (h *LiveStreamSSEHub) fanOutLifecycleActions(actions []liveactions.ActionEvent) {
	h.mu.RLock()
	clients := make([]*liveStreamClient, 0, len(h.clients))
	for c := range h.clients {
		clients = append(clients, c)
	}
	h.mu.RUnlock()
	if len(clients) == 0 {
		return
	}

	payloads := make(map[string][]byte) // audience key → frame bytes
	for _, c := range clients {
		key := "super"
		if !c.isSuper {
			key = "tenant:" + normalizeLiveStreamTenant(c.tenantID)
		}
		if _, done := payloads[key]; done {
			continue
		}
		subset := actions
		if !c.isSuper {
			subset = h.actionsVisibleToTenant(actions, c.tenantID)
		}
		if len(subset) == 0 {
			payloads[key] = nil
			continue
		}
		data, err := json.Marshal(LiveStreamEnvelope{
			Type:      "request_lifecycle",
			Timestamp: time.Now().UTC(),
			Action:    actionWirePayload(subset),
		})
		if err != nil {
			slog.Warn("live actions marshal failed", "err", err.Error())
			payloads[key] = nil
			continue
		}
		payloads[key] = data
	}

	delivered := false
	for _, c := range clients {
		key := "super"
		if !c.isSuper {
			key = "tenant:" + normalizeLiveStreamTenant(c.tenantID)
		}
		data := payloads[key]
		if len(data) == 0 {
			continue
		}
		if h.writeEvent(c, data) {
			delivered = true
		} else {
			h.evict(c)
		}
	}
	if delivered {
		atomic.AddInt64(&h.actionsDelivered, int64(len(actions)))
	}
}

// ── initial replay ──────────────────────────────────────────────────────────

// replayLifecycleActions replays the most recent actions to a freshly
// connected client, ordered ascending by (ts, seq) per 24号 §4, scoped to
// the client's tenant visibility.
func (h *LiveStreamSSEHub) replayLifecycleActions(ctx context.Context, client *liveStreamClient) {
	if h == nil || h.cfg.RedisClient == nil || client == nil {
		return
	}
	limit := h.cfg.ActionReplayLimit
	if limit <= 0 {
		limit = defaultActionReplayLimit
	}
	ctx, cancel := context.WithTimeout(ctx, actionReadTimeout)
	defer cancel()
	entries, err := h.cfg.RedisClient.LRange(ctx, liveactions.RedisKey, 0, int64(limit)-1).Result()
	if err != nil {
		atomic.AddInt64(&h.actionScanErrors, 1)
		slog.Debug("live actions replay failed", "err", err.Error())
		return
	}

	var events []liveactions.ActionEvent
	for _, raw := range entries {
		var ev liveactions.ActionEvent
		if err := json.Unmarshal([]byte(raw), &ev); err != nil || ev.Action == "" {
			continue
		}
		if !isRequestScopedAction(ev) {
			continue // 节点维度 state_change 不进 lifecycle 回放（24号 §2）
		}
		events = append(events, ev)
	}
	if len(events) == 0 {
		return
	}
	sortActionsStable(events)
	h.resolveActionTenants(ctx, events)

	subset := events
	if !client.isSuper {
		subset = h.actionsVisibleToTenant(events, client.tenantID)
	}
	if len(subset) == 0 {
		return
	}
	data, err := json.Marshal(LiveStreamEnvelope{
		Type:      "request_lifecycle",
		Timestamp: time.Now().UTC(),
		Action:    actionWirePayload(subset),
	})
	if err != nil {
		slog.Warn("live actions replay marshal failed", "err", err.Error())
		return
	}
	if h.writeEvent(client, data) {
		atomic.AddInt64(&h.actionsDelivered, int64(len(subset)))
	}
}

// fanOutChildRequest emits the OBS-BE2 child_request frame for an extended
// request (requestType ∈ title|summary|sensitive_word|unknown, 24号 §3).
// The legacy "request" frame for the same row is emitted separately by the
// broadcast path; both share shouldDeliver's tenant check.
func (h *LiveStreamSSEHub) fanOutChildRequest(req LiveRequest) {
	r := req
	h.fanOut(LiveStreamEnvelope{
		Type:            "child_request",
		Timestamp:       time.Now().UTC(),
		Request:         &r,
		ParentRequestID: req.ParentRequestID,
	})
}
