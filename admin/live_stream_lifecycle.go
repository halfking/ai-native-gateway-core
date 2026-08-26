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
	"strconv"
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
//
// 2026-08-23 (Agent C): if labels is non-nil and contains an entry for
// ev.CredentialID, the resolved label is written as `credential_label`
// so the dashboard can render "供应商+凭据" without a second fetch. The
// ActionEvent contract (liveactions/liveactions.go) stays unchanged —
// credential_label is purely a wire-side projection, optional, omitted
// when the lookup misses.
func flattenActionEvent(ev liveactions.ActionEvent, labels map[int]string) map[string]any {
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
	if labels != nil && ev.CredentialID > 0 {
		if label, ok := labels[ev.CredentialID]; ok && label != "" {
			// Don't clobber an explicit `credential_label` already on the
			// wire (e.g. a future emitter that promotes its own label).
			if _, taken := m["credential_label"]; !taken {
				m["credential_label"] = label
			}
		}
	}
	return m
}

// collectCredentialIDs returns the deduplicated set of credential IDs that
// appear on a batch of ActionEvents (credential_id plus from_credential_id /
// to_credential_id for node_switch). The caller uses the slice as the
// `WHERE id = ANY(...)` argument to a single batched credentials lookup so
// a 50-action tick does not fan out 50 round-trips.
func collectCredentialIDs(actions []liveactions.ActionEvent) []int {
	if len(actions) == 0 {
		return nil
	}
	seen := make(map[int]struct{})
	out := make([]int, 0, len(actions))
	add := func(id int) {
		if id <= 0 {
			return
		}
		if _, dup := seen[id]; dup {
			return
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	for _, a := range actions {
		add(a.CredentialID)
		if a.Action == liveactions.ActionNodeSwitch {
			if v, ok := extractIntFromDetail(a.Detail, "from_credential_id"); ok {
				add(v)
			}
			if v, ok := extractIntFromDetail(a.Detail, "to_credential_id"); ok {
				add(v)
			}
		}
	}
	return out
}

// extractIntFromDetail parses an int from the emitter's Detail map. Detail
// values are typed as strings (liveactions.ActionEvent.Detail) but the wire
// contract uses int-valued keys for from/to_credential_id — accept either
// representation defensively.
func extractIntFromDetail(detail map[string]string, key string) (int, bool) {
	if detail == nil {
		return 0, false
	}
	raw, ok := detail[key]
	if !ok || raw == "" {
		return 0, false
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
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
//
// 2026-08-23 (Agent C): optional labels map (credential_id → label) is
// forwarded to flattenActionEvent so each emitted action carries a
// `credential_label` projection. Pass nil when the lookup is unavailable
// (no DB / batch with no credential_id) — flattenActionEvent treats nil
// as a no-op and the wire shape stays identical to the pre-credential-
// label contract.
func actionWirePayload(actions []liveactions.ActionEvent, labels map[int]string) any {
	if len(actions) == 1 {
		return flattenActionEvent(actions[0], labels)
	}
	out := make([]map[string]any, len(actions))
	for i, a := range actions {
		out[i] = flattenActionEvent(a, labels)
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
//
// Idle-cost guard: with no connected dashboard client there is nobody to
// deliver to, and the 4/s × ≤500-entry LRANGE would sit permanently on the
// shared Redis (the repo has a documented history of Redis slowlog
// incidents). Skipping is safe: delivery is at-least-once — the cursor goes
// stale while idle, the next connected client rebuilds history via the
// initial_data action replay, and the first poll's ≤500-entry catch-up
// duplicates collapse client-side by (request_id, seq).
func (h *LiveStreamSSEHub) pollLiveActions() {
	if h == nil || h.cfg.RedisClient == nil {
		return
	}
	h.mu.RLock()
	hasClients := len(h.clients) > 0
	h.mu.RUnlock()
	if !hasClients {
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
// it is fresh. An empty cursor (fresh hub, or idle with no clients) treats
// EVERYTHING scanned as new — there is deliberately no arming/swallow step:
// a swallow window between a client's initial_data replay read and the
// cursor's first arm could drop genuinely-new events, while delivering the
// backlog instead only duplicates what the replay already sent, and the
// frontend replaces duplicates by (request_id, seq).
func (h *LiveStreamSSEHub) deliverNewActions(ctx context.Context, entries []string) {
	if len(entries) == 0 {
		return
	}
	decoded := decodeActionEntries(entries)
	if len(decoded) == 0 {
		return
	}

	h.actionMu.Lock()
	cursor := h.actionCursor
	h.actionMu.Unlock()

	var fresh []liveactions.ActionEvent
	if cursor == "" {
		// Fresh cursor: everything scanned is new (at-least-once catch-up).
		for _, k := range decoded {
			fresh = append(fresh, k.ev)
		}
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

// keyedAction pairs one decoded action with its cursor key.
type keyedAction struct {
	ev  liveactions.ActionEvent
	key string
}

// decodeActionEntries parses stored list entries, skipping malformed rows
// and node-dimension events (they never ride request_lifecycle, 24号 §2).
func decodeActionEntries(entries []string) []keyedAction {
	decoded := make([]keyedAction, 0, len(entries))
	for _, raw := range entries {
		var ev liveactions.ActionEvent
		if err := json.Unmarshal([]byte(raw), &ev); err != nil || ev.Action == "" {
			continue // malformed entry: skip, never kill the stream
		}
		if !isRequestScopedAction(ev) {
			continue // state_change 等：不进 lifecycle 帧/光标
		}
		decoded = append(decoded, keyedAction{ev: ev, key: actionUniqueKey(ev)})
	}
	return decoded
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
		// 2026-08-23 (Agent C): resolve credential labels once per distinct
		// payload. The lookup is best-effort (nil when no DB / cache cold)
		// and the wire contract remains valid without it. Use a fresh,
		// bounded background context so the broadcast path does not depend
		// on any per-client deadline (it never gets one).
		labels := h.CredentialLabelsFor(context.Background(), collectCredentialIDs(subset))
		data, err := json.Marshal(LiveStreamEnvelope{
			Type:      "request_lifecycle",
			Timestamp: time.Now().UTC(),
			Action:    actionWirePayload(subset, labels),
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

func snapshotRequestIDs(snapshot *LiveStreamSnapshot) map[string]struct{} {
	if snapshot == nil {
		return nil
	}
	ids := make(map[string]struct{})
	add := func(dimensions map[string][]LiveStreamLane) {
		for _, lanes := range dimensions {
			for _, lane := range lanes {
				for _, tile := range lane.Requests {
					if tile.RequestID != "" {
						ids[tile.RequestID] = struct{}{}
					}
				}
			}
		}
	}
	if len(snapshot.DetailDimensions) > 0 {
		add(snapshot.DetailDimensions)
	}
	if len(ids) == 0 {
		add(snapshot.Dimensions)
	}
	return ids
}

// replayLifecycleActions preserves the legacy unrestricted helper used by
// focused unit tests and callers that do not have an initial snapshot.
func (h *LiveStreamSSEHub) replayLifecycleActions(ctx context.Context, client *liveStreamClient) {
	h.replayLifecycleActionsFor(ctx, client, nil)
}

// replayLifecycleActionsFor replays lifecycle actions for a freshly connected
// client. When requestIDs is non-nil, only actions belonging to request cards
// included in the just-sent initial snapshot are replayed. This keeps the
// global action list from filling the frontend timeline index with unrelated
// high-volume requests.
func (h *LiveStreamSSEHub) replayLifecycleActionsFor(ctx context.Context, client *liveStreamClient, requestIDs map[string]struct{}) {
	if h == nil || h.cfg.RedisClient == nil || client == nil {
		return
	}
	limit := h.cfg.ActionReplayLimit
	if limit <= 0 {
		limit = defaultActionReplayLimit
	}
	ctx, cancel := context.WithTimeout(ctx, actionReadTimeout)
	defer cancel()
	scanLimit := limit
	if requestIDs != nil && actionScanPerPoll > scanLimit {
		scanLimit = actionScanPerPoll
	}
	entries, err := h.cfg.RedisClient.LRange(ctx, liveactions.RedisKey, 0, int64(scanLimit)-1).Result()
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
		if requestIDs != nil {
			if _, ok := requestIDs[ev.RequestID]; !ok {
				continue
			}
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
	if len(subset) > limit {
		subset = subset[len(subset)-limit:]
	}
	if len(subset) == 0 {
		return
	}
	// 2026-08-23 (Agent C): pre-resolve credential labels so the initial
	// action replay already carries `credential_label`. Same best-effort
	// semantics as the live broadcast path.
	labels := h.CredentialLabelsFor(ctx, collectCredentialIDs(subset))
	data, err := json.Marshal(LiveStreamEnvelope{
		Type:      "request_lifecycle",
		Timestamp: time.Now().UTC(),
		Action:    actionWirePayload(subset, labels),
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
