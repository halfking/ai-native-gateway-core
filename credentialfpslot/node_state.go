// NOTE (2026-08-11 audit): the original "DEPRECATED / Replaced by
// domains/ursm/state.go" header below was inaccurate — that target file
// never existed. This NodeState remains the live, production type used by
// domains/streaming/executors/router.go (per-credential/model health gating
// on the request hot path). domains/ursm/v2 manages a different concern
// (LRU/filter scoring) and does not supersede this struct. Treat this file
// as authoritative until a concrete migration lands; do not mark it
// deprecated against a non-existent target.

package credentialfpslot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/kaixuan/llm-gateway-go/metrics"
	"github.com/kaixuan/llm-gateway-go/settings"
	"github.com/redis/go-redis/v9"
)

const (
	nodeStateTTLSec      = 3600
	nodeCapabilityTTLSec = 3600

	// prefetchMaxAgeSec bounds how stale a router-supplied snapshot may be
	// before GetSupportsResponses stops trusting it and re-reads the key.
	//
	// NOT an expiry knob: expiry is judged against capability_expires_at
	// (3600s, on the Redis clock) and is enforced independently. This is the
	// separate question of "how old may a routing-time snapshot be before the
	// request insists on a fresh read", which exists because a request can sit
	// in the dispatch queue for an unbounded time between the router's MGET and
	// this gate. See GetSupportsResponses for the full argument.
	//
	// ⚠️ 5s IS NOW CALIBRATED AGAINST A MEASURED DISTRIBUTION (2026-10-03).
	// It was previously an engineering guess, and the audit called it the only
	// uncalibrated parameter in the project. Measured on request_logs_hot
	// (168 rows with a full waterfall, window filtered on t0_arrived_at,
	// 2026-10-02 21:19 → 2026-10-03 05:27), on the span that actually contains
	// the router's MGET — T2 total-dequeued → T5 cred-enqueued, which brackets
	// model resolution and candidate selection:
	//
	//	p50 0.005s   p90 0.040s   p95 5.41s   p99 17.88s   max 26.04s
	//	> 5s: 11/168 = 6.5%
	//
	// The distribution is BIMODAL, and that is the whole story: 93.5% of
	// requests route in tens of milliseconds, and a thin tail sits at 5–26s.
	// 5s therefore cuts *inside* the tail rather than above it — it keeps the
	// optimisation for ~93.5% of traffic and deliberately re-reads the ~6.5%
	// that queued long enough for the verdict to plausibly have been rewritten.
	//
	// Why 5s and not 30s (the observed max): raising it to cover the tail would
	// mean trusting snapshots up to 26s old, which is precisely the unbounded
	// staleness window the guard exists to close. The guard's job is to bound
	// divergence from a fresh read, not to maximise hit rate — a dropped
	// snapshot is always SAFE (it re-reads), a wrongly-trusted one is not.
	//
	// Why not lower it to p90 (0.04s): the tail is where the guard earns its
	// keep. Rejecting everything past 40ms would re-read on the requests that
	// actually queued, and would make the metric useless for its stated purpose
	// (seeing the tail approach the bound).
	//
	// ⚠️ The 5–26s tail is NOT queue depth — T1→T2 admission, T3→T4 model and
	// T5→T6 credential queues are ALL milliseconds (max 82ms) on the same rows.
	// The seconds live in T2→T5, i.e. in routing/selection itself. Anyone
	// re-deriving this must measure that span, not the total queue wait, or
	// they will calibrate against the wrong distribution.
	//
	// TO RE-DERIVE: see docs/audit/2026-10-03-prefetch-age-calibration.md for
	// the query and the reasoning. After deploying, prefer the live signal
	// (llmgw_node_state_prefetch_age_seconds and
	// llmgw_node_state_prefetch_dropped_total) over re-querying logs.
	prefetchMaxAgeSec = 5
)

// NodeState tracks health state for (credentialID, model) dimension.
// Stored as JSON in Redis key llmgw:cred_fp_node:{credentialID}:{model}.
// All write operations go through the atomic Lua script recordNodeOutcome
// to prevent lost updates under concurrent requests (same pattern as slot
// Lua scripts — no Go-side read-modify-write races).
//
// Merged into credentialfpslot package as part of P3 "深度整合"
// (2026-06-26): the slot pool owns both identity AND health tracking,
// eliminating the separate streaming.RouteNodeStore.
//
// P0 Update (2026-07-19): 新增恢复追踪字段，用于实际流量优先恢复机制。
type NodeState struct {
	CredentialID   int          `json:"credential_id"`
	Model          string       `json:"model"`
	SuccessCount   int64        `json:"success_count"`
	FailureCount   int64        `json:"failure_count"`
	SlideWindow    []NodeRecord `json:"slide_window,omitempty"`
	LastSuccessAt  int64        `json:"last_success_at,omitempty"` // unix seconds
	LastFailureAt  int64        `json:"last_failure_at,omitempty"` // unix seconds
	Disabled       bool         `json:"disabled"`
	DisabledUntil  int64        `json:"disabled_until,omitempty"` // unix seconds
	DisabledReason string       `json:"disabled_reason,omitempty"`

	// P0 新增字段（2026-07-19）：支持实际流量优先恢复
	LastDisabledAt int64 `json:"last_disabled_at,omitempty"` // 最后一次被禁用的时间
	DisableCount   int   `json:"disable_count,omitempty"`    // 累计禁用次数（用于动态调整冷却时间）

	// F04 (V3 持久化协议能力, 2026-09-30): 协议能力位 —— 与上面的 cooldown
	// 健康状态是**两件事**：cooldown 是临时降级（TTL 内自然过期），协议能力
	// 是「这个 credential 上的这个模型，永远不支持这个协议」的持久结论。
	// 没有它，每次新请求都会先打一发注定失败的 Responses 再回退。
	//
	// capability verdict 有独立的 3600s 过期时间。NodeState key 可能因健康
	// 请求续期；续期不能延长旧协议能力结论的寿命。
	//
	// POINTER, not a value: a value struct is never omitted by
	// `json:",omitempty"`, so every node would always carry `"capabilities":{}`.
	// Redis Lua (cjson) cannot tell an empty object from an empty array and
	// re-encodes `{}` as `[]`, which then fails to unmarshal back into a Go
	// struct — every node-state read would error. A nil pointer is omitted
	// from the payload entirely, so cjson never sees an empty object here.
	Capabilities        *NodeCapabilities `json:"capabilities,omitempty"`
	CapabilityUpdatedAt int64             `json:"capability_updated_at,omitempty"` // unix seconds
	CapabilityExpiresAt int64             `json:"capability_expires_at,omitempty"` // unix seconds

	// SnapshotReadAt is the instant at which THIS PROCESS read the key. It is
	// populated by the read paths (GetNodeStatesBatch / GetNodeState) and is
	// deliberately NOT serialised into Redis — it describes this process's
	// copy, not the stored verdict.
	//
	// json:"-" because it must never reach the payload: the Lua writers decode
	// and re-encode the whole state through cjson, and any extra key would
	// either be dropped on the next write or, worse, be read back as if it
	// were a stored field. It is also why a NodeState that has been through
	// SetNodeState is unstamped on the way back out — correct, because such a
	// value is no longer a snapshot of a live read.
	//
	// ⚠️ WHY THIS FIELD EXISTS (2026-10-03 复审, 能力位遗留 #3 续):
	// the snapshot-age guard needs "how long have I been holding this copy",
	// and CapabilityUpdatedAt is NOT that quantity — it is the verdict's WRITE
	// time with a 3600s TTL. Comparing `now` against a 3600s-scale timestamp
	// with a 5s bound rejects essentially every real snapshot: a verdict
	// written 30 minutes ago is perfectly live, yet the guard called its
	// 6ms-old snapshot "too old" and re-read the key, giving back the round
	// trip the optimisation exists to save (measured GET=1 TIME=2 in the gate,
	// i.e. 4 hot-path round trips against a pre-change 3).
	//
	// It is a time.Time rather than an int64 so it carries Go's MONOTONIC
	// reading: the age is a duration between two local reads, so an NTP step
	// must not be able to make a fresh snapshot look ancient (which would
	// silently re-disable the optimisation) or vice versa.
	SnapshotReadAt time.Time `json:"-"`
}

// UnmarshalJSON tolerates the "empty Lua table" shape for slide_window.
//
// Every writer of this payload is a Redis Lua script, and Lua's `{}` is an
// empty TABLE, which Redis' cjson re-encodes as the JSON OBJECT `{}` — not the
// array `[]` that `[]NodeRecord` needs. The same trap is already called out on
// the Capabilities field above (solved with a nil-able pointer); slide_window
// hit it too and was missed.
//
// The production consequence was not cosmetic. Observed live on
// api.vapeur.ai / credential 126 (2026-10-02):
//
//	llmgw:cred_fp_node:126:gpt-5.6-terra =
//	  {"slide_window":{},"disabled":false,"credential_id":126,...,
//	   "capabilities":{"supports_responses":true}, ...}
//
// Every GetNodeState on such a key returned
// `unmarshal node state: json: cannot unmarshal object into Go struct field
// NodeState.slide_window of type []credentialfpslot.NodeRecord`, so
// GetSupportsResponses always errored. Every caller treats a read error as
// "no usable verdict" — which silently disabled BOTH directions of the
// durable Responses capability gate (the F04 downgrade short-circuit added
// 2026-09-30 and the enable path added 2026-10-02). The verdict was being
// written correctly the whole time and simply never read back.
//
// Normalising `{}` → `[]` restores it: an empty Lua table means "no records",
// which is exactly what an empty Go slice means. A NON-empty object shape
// would be genuinely unexpected, so it is left to fail loudly.
func (n *NodeState) UnmarshalJSON(data []byte) error {
	// Hot-path guard. GetNodeStatesBatch decodes one NodeState per routing
	// candidate on every request, so the common case must stay a single parse.
	// A cheap substring test is enough to skip the fix-up work entirely for
	// every payload that cannot be affected.
	if !bytes.Contains(data, []byte(`"slide_window"`)) {
		type alias NodeState
		return json.Unmarshal(data, (*alias)(n))
	}
	// alias sheds the method set, so the inner decode does not recurse.
	type alias NodeState
	var probe struct {
		SlideWindow json.RawMessage `json:"slide_window"`
	}
	if err := json.Unmarshal(data, &probe); err == nil {
		trimmed := bytes.TrimSpace(probe.SlideWindow)
		if len(trimmed) > 0 && trimmed[0] == '{' {
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(data, &fields); err == nil {
				fields["slide_window"] = json.RawMessage("[]")
				if patched, err := json.Marshal(fields); err == nil {
					data = patched
				}
			}
		}
	}
	return json.Unmarshal(data, (*alias)(n))
}

// NodeCapabilities holds durable protocol-capability verdicts for one
// (credential, model) node. Every field is a *bool so that "never probed"
// (nil) is distinguishable from an explicit observation: a nil
// SupportsResponses MUST NOT short-circuit the live detection path, or every
// unprobed credential would be treated as "Responses unsupported".
type NodeCapabilities struct {
	// SupportsResponses=false means the provider verdict was "Responses API
	// unsupported" for this credential+model.
	SupportsResponses *bool `json:"supports_responses,omitempty"`
}

// SupportsResponsesKnown reports whether a durable verdict exists. Callers
// short-circuiting the live Responses attempt must consult this first: a nil
// pointer means "never observed", never "unsupported".
func (c *NodeCapabilities) SupportsResponsesKnown() bool {
	return c != nil && c.SupportsResponses != nil
}

// ResponsesUnsupported is the durable "do not attempt Responses" verdict.
func (c *NodeCapabilities) ResponsesUnsupported() bool {
	return c != nil && c.SupportsResponses != nil && !*c.SupportsResponses
}

// NodeRecord is one request record in the sliding window.
type NodeRecord struct {
	RequestID string `json:"request_id,omitempty"`
	Success   bool   `json:"success"`
	ErrorKind string `json:"error_kind,omitempty"`
	Timestamp int64  `json:"timestamp"` // unix seconds
}

// IsUsable determines if the node is currently routable.
func (n *NodeState) IsUsable(now time.Time) bool {
	if n == nil {
		return true
	}
	if n.Disabled && n.DisabledUntil > 0 && now.Unix() < n.DisabledUntil {
		return false
	}
	// Cooldown expiry puts the node back into the routing pool. The following
	// real request is still recorded by the atomic Lua transition and can
	// immediately disable the node again if it fails.
	n.recoverIfCooldownExpired(now.Unix())
	return !n.Disabled && n.ConsecutiveFailureStreak(now) < settings.NodeFailStreakLimit()
}

// ConsecutiveFailureStreak counts tail failures within the active sliding window.
func (n *NodeState) ConsecutiveFailureStreak(now time.Time) int {
	if n == nil {
		return 0
	}
	n.pruneWindow(now.Unix())
	streak := 0
	for i := len(n.SlideWindow) - 1; i >= 0; i-- {
		if !n.SlideWindow[i].Success {
			streak++
			continue
		}
		break
	}
	return streak
}

// nodeKey returns the Redis key for a node state.
func nodeKey(credentialID int, model string) string {
	return fmt.Sprintf("llmgw:cred_fp_node:%d:%s", credentialID, model)
}

// GetNodeState reads node health state from Redis.
// Returns a zero-value state (never nil) when no key exists.
func (m *Manager) GetNodeState(ctx context.Context, credentialID int, model string) (*NodeState, error) {
	// One stamp for the whole read, mirroring GetNodeStatesBatch: every value
	// returned below was observed at this instant. The two early exits are
	// included on purpose — a zero state carries no verdict, but leaving its
	// read stamp zero makes this path's shape differ from the batch path for
	// no reason. Today that difference is harmless (the capability gate
	// returns early on !SupportsResponsesKnown and never reaches the age
	// guard), which is exactly why it needs a criterion rather than a
	// comment: if that early return ever moves, the zero state starts
	// counting as reason="unstamped" and the metric stops meaning
	// "a construction site bypassed the read path".
	readAt := time.Now()
	if m.client == nil {
		state := newZeroNodeState(credentialID, model)
		state.SnapshotReadAt = readAt
		return state, nil
	}
	key := nodeKey(credentialID, model)
	data, err := m.client.Get(ctx, key).Result()
	if err == redis.Nil {
		state := newZeroNodeState(credentialID, model)
		state.SnapshotReadAt = readAt
		return state, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get node state: %w", err)
	}

	var state NodeState
	if err := json.Unmarshal([]byte(data), &state); err != nil {
		return nil, fmt.Errorf("unmarshal node state: %w", err)
	}
	if state.CredentialID == 0 {
		state.CredentialID = credentialID
	}
	if state.Model == "" {
		state.Model = model
	}
	// A payload carrying a different non-zero identity than its key is
	// treated as corruption: fail the single read closed so callers can
	// observe it, instead of silently routing on another node's health data.
	if state.CredentialID != credentialID || state.Model != model {
		return nil, fmt.Errorf("node state identity mismatch: key=(%d,%s) payload=(%d,%s)",
			credentialID, model, state.CredentialID, state.Model)
	}
	state.SnapshotReadAt = readAt
	return &state, nil
}

// NodeStateKey identifies one (credentialID, model) node-state entry.
type NodeStateKey struct {
	CredentialID int
	Model        string
}

// GetNodeStatesBatch reads several node health states in ONE Redis round
// trip (MGET) and returns states aligned with keys (entry is never nil; a
// missing, corrupt, or identity-mismatched key yields a zero — i.e.
// usable — state, so one poisoned entry cannot fail the whole batch and
// un-filter every node).
//
// The router calls this once per candidate list on the request hot path;
// the previous per-candidate GET loop amplified routing latency by
// Redis RTT × N for up to 12 candidates (docs/会话优化v3/29 §B1).
func (m *Manager) GetNodeStatesBatch(ctx context.Context, keys []NodeStateKey) ([]*NodeState, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	// One stamp for the whole batch: every entry in this MGET is by definition
	// read at the same instant, and taking it before the call keeps it
	// independent of how long the round trip took.
	readAt := time.Now()
	out := make([]*NodeState, len(keys))
	if m.client == nil {
		for i, k := range keys {
			out[i] = newZeroNodeState(k.CredentialID, k.Model)
			out[i].SnapshotReadAt = readAt
		}
		return out, nil
	}
	redisKeys := make([]string, len(keys))
	for i, k := range keys {
		redisKeys[i] = nodeKey(k.CredentialID, k.Model)
	}
	vals, err := m.client.MGet(ctx, redisKeys...).Result()
	if err != nil {
		return nil, fmt.Errorf("mget node states: %w", err)
	}
	for i, v := range vals {
		s, ok := v.(string)
		if !ok || s == "" {
			out[i] = newZeroNodeState(keys[i].CredentialID, keys[i].Model)
			out[i].SnapshotReadAt = readAt
			continue
		}
		var state NodeState
		if err := json.Unmarshal([]byte(s), &state); err != nil {
			// One corrupt entry must not fail the whole batch — the caller
			// fail-opens on error, which would un-filter every node.
			out[i] = newZeroNodeState(keys[i].CredentialID, keys[i].Model)
			out[i].SnapshotReadAt = readAt
			continue
		}
		if state.CredentialID == 0 {
			state.CredentialID = keys[i].CredentialID
		}
		if state.Model == "" {
			state.Model = keys[i].Model
		}
		// Identity mismatch is corruption for routing purposes; fail open
		// to a zero state the same way as malformed JSON.
		if state.CredentialID != keys[i].CredentialID || state.Model != keys[i].Model {
			out[i] = newZeroNodeState(keys[i].CredentialID, keys[i].Model)
			out[i].SnapshotReadAt = readAt
			continue
		}
		state.SnapshotReadAt = readAt
		out[i] = &state
	}
	return out, nil
}

// SetNodeState stores node health state directly.
// Used by tests to inject specific cooldown timestamps.
func (m *Manager) SetNodeState(ctx context.Context, state *NodeState) error {
	if state == nil {
		return nil
	}
	if m.client == nil {
		return nil
	}
	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("marshal node state: %w", err)
	}
	if err := m.client.Set(ctx, nodeKey(state.CredentialID, state.Model), data, time.Duration(nodeStateTTLSec)*time.Second).Err(); err != nil {
		return fmt.Errorf("set node state: %w", err)
	}
	return nil
}

// ResetNodeHealthState clears routing health/cooldown fields for one node
// while preserving its independent protocol-capability verdict. The reset is
// atomic with request outcome writes, so concurrent failures cannot be lost
// through a Go-side read-modify-write.
func (m *Manager) ResetNodeHealthState(ctx context.Context, credentialID int, model string) error {
	if !m.Enabled() || m.client == nil {
		return nil
	}
	if _, err := resetNodeHealthStateScript.Run(ctx, m.client,
		[]string{nodeKey(credentialID, model)},
		credentialID,
		model,
		nodeStateTTLSec,
	).Result(); err != nil {
		return fmt.Errorf("reset node health state: %w", err)
	}
	return nil
}

// RecordNodeSuccess records a successful request atomically via Lua.
func (m *Manager) RecordNodeSuccess(ctx context.Context, credentialID int, model, requestID string) error {
	return m.recordNodeOutcome(ctx, credentialID, model, "success", requestID, "")
}

// SetSupportsResponses records a protocol-capability verdict for one
// (credential, model) node. Its independent expiry must not be extended by
// the NodeState health-key TTL.
//
// It runs its own Lua script rather than read-modify-write in Go, because
// SetNodeState would overwrite the concurrent health fields written by
// recordNodeOutcomeScript (sliding window / cooldown / counters). The two
// writers touch disjoint sub-trees of the same JSON key, so each script only
// changes its own fields and preserves the other subtrees semantically.
//
// supported=false  → "do not attempt Responses" until CapabilityExpiresAt.
// supported=true   → "Responses works" until CapabilityExpiresAt; an actual
// native success or probe can also replace a still-live negative verdict.
//
// A nil/disabled Redis (lite mode) is a no-op by contract: capabilities stay
// absent and the read path keeps using live detection.
func (m *Manager) SetSupportsResponses(ctx context.Context, credentialID int, model string, supported bool) error {
	if !m.Enabled() || m.client == nil {
		return nil
	}
	if _, err := setNodeCapabilityScript.Run(ctx, m.client,
		[]string{nodeKey(credentialID, model)},
		supported,
		nodeStateTTLSec,
		nodeCapabilityTTLSec,
	).Result(); err != nil {
		return fmt.Errorf("set node capability: %w", err)
	}
	return nil
}

// GetSupportsResponses returns the durable verdict. ok=false means "no
// current verdict" (never probed, capability expiry elapsed, or the node key
// expired), which callers must treat as "keep using live detection", not as
// "unsupported".
//
// prefetched (2026-10-02, vapeur 遗留 #3) is an OPTIONAL node state the caller
// already read for this exact (credential, model) — the router's batched MGET
// hands it down so the hot path does not GET the same key twice. nil means "I
// have nothing", and this function then reads the key itself; it never means
// "there is no verdict".
//
// ⚠️ CLOCK SOURCE — the expiry decision deliberately uses Redis TIME, NOT
// local time, and this must not be "optimised" into time.Now() without a
// separate decision. The stored deadline is written by setNodeCapabilityScript
// as `redis.call('TIME')[1] + ttl`, i.e. capability_expires_at is an ABSOLUTE
// timestamp on the REDIS clock. Comparing it against the local clock compares
// two different epochs, so any clock skew between this process and Redis
// becomes error in the expiry decision — a verdict could outlive its TTL or
// expire early. The skew is bounded and small, but the deadline is 3600s and
// the verdict gates protocol selection, so "bounded and small" is not a
// licence to silently change the epoch. Round 48 chose Redis TIME for exactly
// this reason.
//
// Consequence, stated plainly so nobody re-derives it as a bug: passing
// `prefetched` removes the node-key GET but does NOT remove the TIME
// round trip. The hot path goes 2 Redis round trips → 1, not → 0. Eliminating
// the remaining TIME means folding TIME into the router's existing batched
// read (a Lua script returning TIME + the states in one shot), which changes
// GetNodeStatesBatch's contract and is deliberately NOT done here.
//
// ⚠️ SNAPSHOT AGE (2026-10-03 审计补充；同日根修后按校准实况订正) —
// `prefetched` can be arbitrarily old. Measured, not assumed: the batch read
// stamps the whole MGET batch once, and by the time the request reaches this
// gate it has been through the routing/selection segments — that is where the
// seconds live in the 168-request calibration (T2→T5); the queue segments are
// milliseconds. (An earlier revision of this note blamed the totalQueue wait;
// the calibration disproved that attribution.) The gap has no upper bound in
// code — it is whatever routing and upstream latency make it. Measured
// staleness is therefore unbounded even though the common case is milliseconds.
//
// What is still safe without an age guard: EXPIRY. The deadline check below
// samples Redis TIME, so a prefetched verdict whose 3600s TTL elapsed is still
// correctly read as unknown — the snapshot never revives an expired verdict.
//
// What is NOT safe: a verdict REWRITTEN inside its TTL. SetSupportsResponses is
// called from three places (executor_chat recordResponsesCapability,
// bg/credential_probe_v2, bg/responses_capability) and a positive re-probe can
// flip false→true while a request sits in the queue. Such a request would keep
// routing to Chat on a stale negative — conservative, self-healing on the next
// request, but it means the optimisation is not behaviour-identical to a fresh
// read, and that difference must be bounded rather than assumed away.
//
// Hence prefetchMaxAge: a snapshot older than this is ignored and the key is
// re-read. The bound is deliberately far below the 3600s TTL — it is not about
// expiry, it is about "how stale may a routing decision be before we insist on
// re-reading". 5s keeps the optimisation for the overwhelmingly common
// fast path (queue waits are the exception, not the rule) while capping the
// divergence from a fresh read to a window no operator would call a lie.
//
// The age is a LOCAL monotonic duration — time.Since(prefetched.SnapshotReadAt)
// — never a cross-clock comparison of absolute stamps. The deadline check above
// still samples Redis TIME, because the deadline itself is a Redis-clock
// absolute stamp written by Lua TIME; but snapshot age is an *elapsed* time,
// and elapsed time is exactly what the local monotonic clock is for (time.Time
// carries the monotonic reading; NTP steps cannot touch time.Since). An
// earlier revision of this guard measured the age with Redis TIME against the
// verdict's CapabilityUpdatedAt — two wall-clock stamps on different scales —
// which made nearly every fresh snapshot look stale and re-read on the hot
// path; the clock-domain split above is the fix (2026-10-03).
//
// ⚠️ ORDERING IS LOAD-BEARING — Redis TIME MUST be sampled AFTER the state is
// in hand, not before. Sampling it first (to share one round trip between the
// age check and the deadline check) looks like a free saving and is not:
// TestF04_CapabilityReadRechecksRedisTimeAfterStateFetch advances the Redis
// clock between the GET and the TIME call and requires the crossing deadline to
// be detected. Sampling TIME first means a verdict that expires while the
// state is being fetched is still judged against the pre-fetch instant and is
// wrongly honoured. An earlier revision of this function did exactly that and
// the test caught it. One extra round trip is the correct price; do not
// "optimise" the order.
//
// ⚠️ THE AGE CHECK MUST NOT SAMPLE ITS OWN CLOCK (2026-10-03 复审).
// A revision checked snapshot freshness by taking a TIME sample *before*
// deciding whether to trust the snapshot, then took a second one for the
// deadline. That is two TIME round trips on the hot path, and the accounting
// came out at: router MGET + 2×TIME = 3, versus 3 before the optimisation
// (MGET + GET + TIME). **Net zero saving, and worse when the snapshot is
// rejected** (MGET + TIME + GET + TIME = 4). The whole point of the change was
// to remove a round trip; that revision quietly gave it back and then some.
//
// Both checks need only the scalar `now`, so one sample serves both — as long
// as it is taken in the right place, i.e. AFTER the state is in hand. The age
// check therefore runs *after* the freshness decision has been made possible,
// reusing that same sample.
func (m *Manager) GetSupportsResponses(ctx context.Context, credentialID int, model string, prefetched *NodeState) (supported bool, ok bool, err error) {
	state := prefetched
	if state == nil {
		var err error
		state, err = m.GetNodeState(ctx, credentialID, model)
		if err != nil {
			return false, false, err
		}
	}
	if state == nil || !state.Capabilities.SupportsResponsesKnown() {
		return false, false, nil
	}
	if state.CapabilityExpiresAt <= 0 {
		// Older payloads have no independent expiry. Treat them as unknown so
		// traffic-driven NodeState TTL refreshes cannot keep legacy verdicts alive.
		return false, false, nil
	}
	// ONE Redis clock sample, taken after the state is in hand, serving the
	// deadline check below. The snapshot-age check deliberately does NOT use
	// it: age is a LOCAL duration between two reads by this process, so
	// comparing it against a Redis-clock instant would reintroduce the very
	// cross-clock comparison the deadline note above forbids. The local sample
	// below costs no round trip.
	now, err := m.redisNow(ctx)
	if err != nil {
		return false, false, fmt.Errorf("get redis time for capability read failed (credential_id=%d, model=%s): %w", credentialID, model, err)
	}
	// A prefetched snapshot may be arbitrarily old (the request can sit in the
	// dispatch queue for an unbounded time — see the prefetchMaxAgeSec note),
	// so a verdict rewritten inside its TTL would be missed. Re-read instead.
	//
	// ⚠️ THE AGE MUST BE MEASURED AGAINST THE SNAPSHOT'S READ TIME, not against
	// CapabilityUpdatedAt (2026-10-03 复审，实测推翻上一轮的修法). They are
	// different quantities on wildly different scales:
	//
	//	CapabilityUpdatedAt = when the VERDICT was written; TTL 3600s
	//	snapshot age        = when THIS PROCESS read the key; ~6ms typical
	//
	// The previous revision compared `now` against CapabilityUpdatedAt with a
	// 5s bound. A verdict written 30 minutes ago is perfectly live, yet its
	// 6ms-old snapshot was rejected as "too old" and the key was re-read —
	// on essentially every production request. Measured cost inside the gate:
	// GET=1 TIME=2, i.e. MGET + TIME + GET + TIME = 4 hot-path round trips
	// against a pre-change baseline of 3. The optimisation was net-negative,
	// and every existing criterion stayed green because they all used
	// same-second fixtures where the two clocks coincide.
	//
	// `readAt` is stamped by the read paths (GetNodeStatesBatch / GetNodeState).
	// Zero means "this copy did not come from a read" ⇒ age unknowable ⇒
	// re-read, which is also why the `unstamped` reason exists.
	if prefetched != nil {
		if readAt := prefetched.SnapshotReadAt; !readAt.IsZero() {
			// Monotonic duration: immune to an NTP step between the read and
			// this check, which a wall-clock subtraction would not be.
			age := time.Since(readAt).Seconds()
			if age > float64(prefetchMaxAgeSec) {
				metrics.RecordNodeStatePrefetchDropped(metrics.PrefetchDropReasonStale)
				return m.GetSupportsResponses(ctx, credentialID, model, nil)
			}
			metrics.ObserveNodeStatePrefetchAge(age)
		} else {
			metrics.RecordNodeStatePrefetchDropped(metrics.PrefetchDropReasonUnstamped)
			return m.GetSupportsResponses(ctx, credentialID, model, nil)
		}
	}
	// Sampled AFTER the state is in hand on purpose: a verdict whose deadline
	// elapses between the GET and this check must be seen as expired, not
	// honoured. See the ordering note above.
	if state.CapabilityExpiresAt <= now {
		return false, false, nil
	}
	return *state.Capabilities.SupportsResponses, true, nil
}

// RecordNodeFailure records a failed request atomically via Lua.
func (m *Manager) RecordNodeFailure(ctx context.Context, credentialID int, model, requestID, errorKind string) error {
	return m.recordNodeOutcome(ctx, credentialID, model, "failure", requestID, errorKind)
}

func (m *Manager) recordNodeOutcome(ctx context.Context, credentialID int, model, kind, requestID, errorKind string) error {
	if !m.Enabled() {
		return nil
	}
	if m.client == nil {
		return nil
	}
	key := nodeKey(credentialID, model)
	now, err := m.redisNow(ctx)
	if err != nil {
		return fmt.Errorf("get redis time for node outcome failed: %w (credential_id=%d, model=%s)", err, credentialID, model)
	}
	_, err = recordNodeOutcomeScript.Run(ctx, m.client,
		[]string{key},
		kind,
		requestID,
		errorKind,
		now,
		nodeWindowSeconds,
		settings.NodeFailStreakLimit(),
		settings.NodeDisabledCooldownSeconds(),
	).Result()
	if err != nil {
		return fmt.Errorf("record node outcome: %w", err)
	}
	return nil
}

func (m *Manager) redisNow(ctx context.Context) (int64, error) {
	current, err := m.client.Time(ctx).Result()
	if err != nil {
		return 0, err
	}
	return current.Unix(), nil
}

func newZeroNodeState(credentialID int, model string) *NodeState {
	return &NodeState{
		CredentialID: credentialID,
		Model:        model,
		SlideWindow:  []NodeRecord{},
	}
}

func (n *NodeState) pruneWindow(nowUnix int64) {
	cutoff := nowUnix - nodeWindowSeconds
	pruned := make([]NodeRecord, 0, len(n.SlideWindow))
	for _, rec := range n.SlideWindow {
		if rec.Timestamp >= cutoff {
			pruned = append(pruned, rec)
		}
	}
	n.SlideWindow = pruned
}

func (n *NodeState) recoverIfCooldownExpired(nowUnix int64) {
	if n.Disabled && n.DisabledUntil > 0 && nowUnix >= n.DisabledUntil {
		n.Disabled = false
		n.FailureCount = 0
		n.SlideWindow = []NodeRecord{}
		n.DisabledUntil = 0
		n.DisabledReason = ""
		// 2026-08-11 fix: keep DisableCount in sync with the Lua transition
		// (recordNodeOutcomeScript cooldown-expired + success branch resets
		// disable_count = 0). Without this the Go read path (IsUsable → here)
		// reports a stale non-zero DisableCount after cooldown expiry, which
		// corrupts the "dynamic cooldown adjustment" signal the field exists
		// to provide. The next Lua write would fix it, but in between the
		// in-memory copy read by callers diverges from Redis.
		n.DisableCount = 0
	}
}

const (
	nodeWindowSeconds = 300
	// nodeFailStreakLimit（=3）与 nodeDisabledCooldownSec（=300）已于 2026-09-22
	// Wave 2 集中化为 settings 热更键 routing.node_fail_streak_limit /
	// routing.node_disabled_cooldown_seconds（settings.NodeFailStreakLimit /
	// NodeDisabledCooldownSeconds），默认值不变。
)

// recordNodeOutcomeScript atomically reads, updates, and writes NodeState.
// Entirely in Lua — no Go-side TOCTOU race.
//
// Cooldown expiry is handled by IsUsable so the router can send a real
// request to the node again. This script still handles the outcome
// atomically and re-disables the node after the failure threshold.
//
// KEYS[1] = llmgw:cred_fp_node:{credentialID}:{model}
// F04: durable protocol-capability write. Deliberately a SEPARATE script from
// recordNodeOutcomeScript so the capability writer never rewrites health
// fields (sliding window / cooldown / counters) and the outcome writer never
// drops capabilities: both decode the same JSON key, touch only their own
// sub-tree, and re-encode. Doing this in Go would be a read-modify-write race
// against the per-request outcome path.
//
// KEYS[1] = node state key.
// ARGV[1] = supported bool (go-redis uses "1"/"0"; "true"/"false" also work).
// ARGV[2] = node-state TTL in seconds; do not shorten an active cooldown.
// ARGV[3] = independent capability TTL in seconds.
var setNodeCapabilityScript = redis.NewScript(`
	local key = KEYS[1]
	-- go-redis serializes a Go bool arg as "1"/"0", not "true"/"false".
	-- Accept both spellings so the script does not silently store a wrong
	-- verdict if the caller ever switches to a string argument.
	local raw_supported = ARGV[1]
	local supported = (raw_supported == 'true' or raw_supported == '1')
	local ttl = tonumber(ARGV[3])
	local redis_time = redis.call('TIME')
	local now = tonumber(redis_time[1])

	local raw = redis.call('GET', key)
	local state = {}
	if raw then
		-- 损坏 payload 不阻塞能力写：pcall 防御性解码，失败则以全新 state
		-- 继续，本次 SET 自愈（与 recordNodeOutcomeScript 同一约定）。
		local ok, decoded = pcall(cjson.decode, raw)
		if ok and type(decoded) == 'table' then
			state = decoded
		end
	end

	-- 身份字段回填：key 形如 llmgw:cred_fp_node:<credID>:<model>，保证
	-- GetNodeState 的 identity 校验（credential_id / model 必须与 key 匹配）
	-- 不会因新建 state 而失败。
	if type(state.credential_id) ~= 'number' or state.credential_id == 0 then
		state.credential_id = tonumber(string.match(key, '^llmgw:cred_fp_node:(%d+):')) or 0
	end
	if type(state.model) ~= 'string' or state.model == '' then
		state.model = string.match(key, '^llmgw:cred_fp_node:%d+:(.*)$') or ''
	end
	if type(state.slide_window) ~= 'table' then state.slide_window = {} end
	if type(state.disabled) ~= 'boolean' then state.disabled = false end
	if type(state.success_count) ~= 'number' then state.success_count = 0 end
	if type(state.failure_count) ~= 'number' then state.failure_count = 0 end

	-- 只写 capabilities 子树。
	if type(state.capabilities) ~= 'table' then state.capabilities = {} end
	state.capabilities.supports_responses = supported
	state.capability_updated_at = now
	state.capability_expires_at = now + ttl

	local ok, encoded = pcall(cjson.encode, state)
	if not ok then return redis.error_reply('encode node state failed') end
	redis.call('SET', key, encoded, 'EX', ARGV[2])
	return 1
`)

// resetNodeHealthStateScript resets health/circuit fields without discarding
// the independent capability sub-tree. It runs atomically with the outcome
// and capability scripts because all three mutate the same Redis JSON key.
var resetNodeHealthStateScript = redis.NewScript(`
	local key = KEYS[1]
	local raw = redis.call('GET', key)
	local state = {}
	if raw then
		local ok, decoded = pcall(cjson.decode, raw)
		if ok and type(decoded) == 'table' then state = decoded end
	end

	state.credential_id = tonumber(ARGV[1])
	state.model = ARGV[2]
	state.success_count = 0
	state.failure_count = 0
	state.slide_window = {}
	state.last_success_at = nil
	state.last_failure_at = nil
	state.disabled = false
	state.disabled_until = nil
	state.disabled_reason = nil
	state.last_disabled_at = nil
	state.disable_count = nil

	if type(state.capabilities) ~= 'table' or type(state.capabilities.supports_responses) ~= 'boolean' then
		state.capabilities = nil
		state.capability_updated_at = nil
		state.capability_expires_at = nil
	else
		if type(state.capability_updated_at) ~= 'number' then state.capability_updated_at = nil end
		if type(state.capability_expires_at) ~= 'number' then state.capability_expires_at = nil end
	end

	local ok, encoded = pcall(cjson.encode, state)
	if not ok then return redis.error_reply('encode node state failed') end
	redis.call('SET', key, encoded, 'EX', ARGV[3])
	return 1
`)

// ARGV[1] = kind ("success" | "failure")
// ARGV[2] = request_id
// ARGV[3] = error_kind (empty for success)
// ARGV[4] = now (unix seconds, as string)
// ARGV[5] = window_seconds
// ARGV[6] = fail_streak_limit
// ARGV[7] = disabled_cooldown_seconds
var recordNodeOutcomeScript = redis.NewScript(`
	local key = KEYS[1]
	local kind = ARGV[1]
	local request_id = ARGV[2]
	local error_kind = ARGV[3]
	local now = tonumber(ARGV[4])
	local window_sec = tonumber(ARGV[5])
	local streak_limit = tonumber(ARGV[6])
	local cooldown = tonumber(ARGV[7])

	local raw = redis.call('GET', key)
	local state = {}
	if raw then
		-- 损坏的 payload 不能永久卡死写入路径：pcall 防御性解码，失败时
		-- 以全新 state 继续，本次 SET 会覆盖损坏 JSON，key 在下一次真实
		-- outcome 时自愈。
		local ok, decoded = pcall(cjson.decode, raw)
		if ok and type(decoded) == 'table' then
			state = decoded
		end
	end

	-- 字段级类型收敛：任何不符合 Go NodeState 编码形状的字段都归零，
	-- 保证写回的 JSON 始终能被 Go 读取路径反序列化。
	if type(state.success_count) ~= 'number' then state.success_count = 0 end
	if type(state.failure_count) ~= 'number' then state.failure_count = 0 end
	if type(state.slide_window) ~= 'table' then state.slide_window = {} end
	if type(state.disabled) ~= 'boolean' then state.disabled = false end
	state.credential_id = tonumber(state.credential_id) or 0
	if type(state.model) ~= 'string' then state.model = '' end
	if type(state.disabled_reason) ~= 'string' then state.disabled_reason = nil end
	if type(state.disable_count) ~= 'number' then state.disable_count = 0 end
	if type(state.disabled_until) ~= 'number' then state.disabled_until = nil end
	if type(state.last_success_at) ~= 'number' then state.last_success_at = nil end
	if type(state.last_failure_at) ~= 'number' then state.last_failure_at = nil end
	if type(state.last_disabled_at) ~= 'number' then state.last_disabled_at = nil end
	if state.credential_id == 0 then state.credential_id = tonumber(string.match(key, '^llmgw:cred_fp_node:(%d+):')) or 0 end
	if state.model == '' then state.model = string.match(key, '^llmgw:cred_fp_node:%d+:(.*)$') or '' end

	local cutoff = now - window_sec
	local pruned = {}
	for i, rec in ipairs(state.slide_window) do
		-- 逐条重建记录，丢弃任何无法回写为合法 NodeRecord 的条目。
		if type(rec) == 'table' and type(rec.timestamp) == 'number' then
			local clean = {
				success = (rec.success == true),
				timestamp = rec.timestamp,
			}
			if type(rec.request_id) == 'string' and rec.request_id ~= '' then
				clean.request_id = rec.request_id
			end
			if type(rec.error_kind) == 'string' and rec.error_kind ~= '' then
				clean.error_kind = rec.error_kind
			end
			if clean.timestamp >= cutoff then
				table.insert(pruned, clean)
			end
		end
	end
	state.slide_window = pruned

	-- Check whether the cooldown has expired before recording this outcome.
	-- 如果冷却期到期，根据本次请求类型决定恢复或延长
	-- 这个检查必须在添加新记录之前进行
	local cooldown_expired = state.disabled and state.disabled_until and now >= state.disabled_until
	
	if cooldown_expired then
		if kind == 'success' then
			-- 冷却期到期 + 成功请求 → 恢复节点
			state.disabled = false
			state.failure_count = 0
			state.slide_window = {}  -- 清空历史失败记录
			state.disabled_reason = 'recovered_with_actual_success'
			state.disabled_until = 0
			if not state.disable_count then
				state.disable_count = 0
			end
			state.disable_count = 0
			-- 添加本次成功记录
			local record = {
				request_id = request_id,
				success = true,
				timestamp = now,
			}
			table.insert(state.slide_window, record)
			state.success_count = state.success_count + 1
			state.last_success_at = now
			-- 直接保存并返回，不再执行后续逻辑
			redis.call('SET', key, cjson.encode(state), 'EX', 3600)
			return 1
		else
			-- 冷却期到期 + 失败请求 → 延长冷却期
			state.slide_window = {}  -- 清空历史记录
			state.failure_count = 0
			state.disabled_until = now + cooldown
			state.disabled_reason = 'cooldown_extended_due_to_failure'
			-- 添加本次失败记录
			local record = {
				request_id = request_id,
				success = false,
				timestamp = now,
			}
			if error_kind ~= '' then
				record.error_kind = error_kind
			end
			table.insert(state.slide_window, record)
			state.failure_count = state.failure_count + 1
			state.last_failure_at = now
			-- 直接保存并返回
			redis.call('SET', key, cjson.encode(state), 'EX', 3600)
			return 1
		end
	end

	-- 正常路径：添加新记录
	local record = {
		request_id = request_id,
		success = (kind == 'success'),
		timestamp = now,
	}
	if error_kind ~= '' then
		record.error_kind = error_kind
	end
	table.insert(state.slide_window, record)

	if kind == 'success' then
		state.success_count = state.success_count + 1
		state.last_success_at = now
	else
		state.failure_count = state.failure_count + 1
		state.last_failure_at = now
	end

	-- 计算连续失败次数（从滑动窗口尾部开始）
	local streak = 0
	for i = #state.slide_window, 1, -1 do
		if not state.slide_window[i].success then
			streak = streak + 1
		else
			break
		end
	end

	-- P0 Fix: 连续失败达到阈值时禁用节点
	if not state.disabled and streak >= streak_limit then
		local recovered_before_disable = state.disabled_reason == 'recovered_with_actual_success'
		state.disabled = true
		state.disabled_until = now + cooldown
		state.last_disabled_at = now
		if not state.disable_count then
			state.disable_count = 0
		end
		state.disable_count = state.disable_count + 1
		-- 区分是否是恢复后再次失败
		if recovered_before_disable or (state.last_success_at and state.last_success_at > (state.last_disabled_at or 0)) then
			state.disabled_reason = 'consecutive_' .. streak_limit .. '_failures_after_recovery'
		else
			state.disabled_reason = 'consecutive_' .. streak_limit .. '_failures'
		end
	end

	redis.call('SET', key, cjson.encode(state), 'EX', 3600)
	return 1
`)
