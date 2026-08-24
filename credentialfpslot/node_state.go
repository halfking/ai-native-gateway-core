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
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	nodeStateTTLSec = 3600
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
	return !n.Disabled && n.ConsecutiveFailureStreak(now) < nodeFailStreakLimit
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
	if m.client == nil {
		return newZeroNodeState(credentialID, model), nil
	}
	key := nodeKey(credentialID, model)
	data, err := m.client.Get(ctx, key).Result()
	if err == redis.Nil {
		return newZeroNodeState(credentialID, model), nil
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
	out := make([]*NodeState, len(keys))
	if m.client == nil {
		for i, k := range keys {
			out[i] = newZeroNodeState(k.CredentialID, k.Model)
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
			continue
		}
		var state NodeState
		if err := json.Unmarshal([]byte(s), &state); err != nil {
			// One corrupt entry must not fail the whole batch — the caller
			// fail-opens on error, which would un-filter every node.
			out[i] = newZeroNodeState(keys[i].CredentialID, keys[i].Model)
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
			continue
		}
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

// RecordNodeSuccess records a successful request atomically via Lua.
func (m *Manager) RecordNodeSuccess(ctx context.Context, credentialID int, model, requestID string) error {
	return m.recordNodeOutcome(ctx, credentialID, model, "success", requestID, "")
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
		nodeFailStreakLimit,
		nodeDisabledCooldownSec,
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
	nodeWindowSeconds       = 300
	nodeFailStreakLimit     = 3
	nodeDisabledCooldownSec = 300
)

// recordNodeOutcomeScript atomically reads, updates, and writes NodeState.
// Entirely in Lua — no Go-side TOCTOU race.
//
// Cooldown expiry is handled by IsUsable so the router can send a real
// request to the node again. This script still handles the outcome
// atomically and re-disables the node after the failure threshold.
//
// KEYS[1] = llmgw:cred_fp_node:{credentialID}:{model}
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
