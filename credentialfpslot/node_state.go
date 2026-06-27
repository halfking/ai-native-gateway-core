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
	n.recoverIfCooldownExpired(now.Unix())
	return n.ConsecutiveFailureStreak(now) < nodeFailStreakLimit
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
	if state.Disabled {
		before := state.Disabled
		state.recoverIfCooldownExpired(time.Now().Unix())
		if before && !state.Disabled {
			if err := m.SetNodeState(ctx, &state); err != nil {
				return nil, err
			}
		}
	}
	return &state, nil
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
	now := time.Now().Unix()
	_, err := recordNodeOutcomeScript.Run(ctx, m.client,
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
		state = cjson.decode(raw)
	end

	if not state.success_count then state.success_count = 0 end
	if not state.failure_count then state.failure_count = 0 end
	if not state.slide_window then state.slide_window = {} end
	if not state.disabled then state.disabled = false end
	if not state.credential_id then state.credential_id = 0 end
	if not state.model then state.model = '' end
	if state.credential_id == 0 then state.credential_id = tonumber(string.match(key, '^llmgw:cred_fp_node:(%d+):')) or 0 end
	if state.model == '' then state.model = string.match(key, '^llmgw:cred_fp_node:%d+:(.*)$') or '' end

	local cutoff = now - window_sec
	local pruned = {}
	for i, rec in ipairs(state.slide_window) do
		if rec.timestamp >= cutoff then
			table.insert(pruned, rec)
		end
	end
	state.slide_window = pruned

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

	local streak = 0
	for i = #state.slide_window, 1, -1 do
		if not state.slide_window[i].success then
			streak = streak + 1
		else
			break
		end
	end

	if streak >= streak_limit and not state.disabled then
		state.disabled = true
		state.disabled_until = now + cooldown
		state.disabled_reason = 'consecutive ' .. streak_limit .. ' failures'
	end

	if state.disabled and state.disabled_until and now >= state.disabled_until then
		state.disabled = false
		state.failure_count = 0
		state.slide_window = {}
	end

	redis.call('SET', key, cjson.encode(state), 'EX', 3600)
	return 1
`)
