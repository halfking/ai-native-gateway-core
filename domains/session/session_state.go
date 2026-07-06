package session

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// Session 扩展字段常量定义
//
// 这些字段通过 session:{id} Hash 持久化。
// 命名约定：snake_case，与 JSON 字段名一致。
const (
	FieldStatus                = "status"
	FieldStoppedAt             = "stopped_at"
	FieldStopReason            = "stop_reason"
	FieldRecoveredAt           = "recovered_at"
	FieldClientIP              = "client_ip"
	FieldClientFP              = "client_fp"
	FieldAPIKeyID              = "api_key_id"
	FieldTenantID              = "tenant_id"
	FieldCurrentCredentialID   = "current_credential_id"
	FieldCurrentModel          = "current_model"
	FieldCurrentProvider       = "current_provider"
	FieldTotalTurns            = "total_turns"
	FieldFirstRequestAt        = "first_request_at"
	FieldLastRequestAt         = "last_request_at"
	FieldTotalPromptTokens     = "total_prompt_tokens"
	FieldTotalCompletionTokens = "total_completion_tokens"
	FieldTotalCostUSDCents     = "total_cost_usd_cents"
	FieldCurrentCredTurns      = "current_cred_turns"
	FieldCurrentCredStartAt    = "current_cred_start_at"
	FieldCurrentCredStartTurn  = "current_cred_start_turn"
	FieldTitle                 = "title"
	FieldAnnotation            = "annotation"
	FieldTags                  = "tags"
	FieldFPSlotIndex           = "fp_slot_index"
	FieldFPSlotCredentialID    = "fp_slot_credential_id"

	// Session 状态值
	StatusActive    = "active"
	StatusStopped   = "stopped"
	StatusRecovered = "recovered"
	StatusExpired   = "expired"

	// 凭据轮换原因
	SwitchReasonInitial     = "initial"
	SwitchReasonSticky      = "sticky"
	SwitchReasonRotate      = "rotate"
	SwitchReasonFallback    = "fallback"
	SwitchReasonModelSwitch = "model_switch"
	SwitchReasonManual      = "manual"
	SwitchReasonSlotExhaust = "slot_exhaust"
	SwitchReasonProbeFail   = "probe_fail"
)

// SessionStats 会话统计
type SessionStats struct {
	TotalTurns            int64     `json:"total_turns"`
	TotalPromptTokens     int64     `json:"total_prompt_tokens"`
	TotalCompletionTokens int64     `json:"total_completion_tokens"`
	TotalCostUSDCents     int64     `json:"total_cost_usd_cents"`
	TotalCostUSD          float64   `json:"total_cost_usd"`
	FirstRequestAt        time.Time `json:"first_request_at,omitempty"`
	LastRequestAt         time.Time `json:"last_request_at,omitempty"`
}

// CredRotationEntry 凭据轮换历史记录
type CredRotationEntry struct {
	CredentialID     int        `json:"credential_id"`
	Model            string     `json:"model"`
	Provider         string     `json:"provider"`
	StartedAt        time.Time  `json:"started_at"`
	EndedAt          *time.Time `json:"ended_at,omitempty"`
	Turns            int        `json:"turns"`
	PromptTokens     int64      `json:"prompt_tokens"`
	CompletionTokens int64      `json:"completion_tokens"`
	CostUSDCents     int64      `json:"cost_usd_cents"`
	SwitchReason     string     `json:"switch_reason"`
	FPSlotIndex      int        `json:"fp_slot_index,omitempty"`
}

// UsageUpdate 请求用量更新
type UsageUpdate struct {
	PromptTokens     int
	CompletionTokens int
	CostUSD          float64 // 单位：美元
	Model            string
	Provider         string
	CredentialID     int
}

// TouchUsage 原子更新会话统计（Lua 脚本实现）
//
// 单次请求完成后调用，避免多个字段并发更新冲突。
func (sm *Manager) TouchUsage(ctx context.Context, sessionID string, u UsageUpdate) error {
	if sm == nil || sm.redis == nil || sessionID == "" {
		return nil
	}

	now := time.Now().UTC()
	nowRFC3339 := now.Format(time.RFC3339)
	costCents := int64(u.CostUSD * 10000) // 转换为万分之一美元单位

	// Lua 脚本：原子更新会话统计
	luaScript := `
local key = KEYS[1]
local turns_inc = tonumber(ARGV[1])
local prompt_tokens_inc = tonumber(ARGV[2])
local completion_tokens_inc = tonumber(ARGV[3])
local cost_cents_inc = tonumber(ARGV[4])
local now = ARGV[5]
local model = ARGV[6]
local provider = ARGV[7]
local cred_id = tonumber(ARGV[8])

-- 仅在活跃状态更新
local status = redis.call('HGET', key, 'status')
if status == false or status == '' then
    redis.call('HSET', key, 'status', 'active')
end

-- 更新统计
redis.call('HINCRBY', key, 'total_turns', turns_inc)
redis.call('HINCRBY', key, 'current_cred_turns', turns_inc)
redis.call('HINCRBY', key, 'total_prompt_tokens', prompt_tokens_inc)
redis.call('HINCRBY', key, 'total_completion_tokens', completion_tokens_inc)
redis.call('HINCRBY', key, 'total_cost_usd_cents', cost_cents_inc)

-- 首次请求时间
local first_req = redis.call('HGET', key, 'first_request_at')
if first_req == false or first_req == '' then
    redis.call('HSET', key, 'first_request_at', now)
end

-- 当前路由状态
redis.call('HSET', key, 'last_request_at', now)
redis.call('HSET', key, 'last_active', now)
if model ~= '' then
    redis.call('HSET', key, 'current_model', model)
end
if provider ~= '' then
    redis.call('HSET', key, 'current_provider', provider)
end
if cred_id > 0 then
    redis.call('HSET', key, 'current_credential_id', cred_id)
end

return 'OK'
`

	client := sm.redis.Client()
	if client == nil {
		return fmt.Errorf("redis client not available")
	}
	_, err := client.Eval(ctx, luaScript,
		[]string{"session:" + sessionID},
		1, u.PromptTokens, u.CompletionTokens, costCents,
		nowRFC3339, u.Model, u.Provider, u.CredentialID,
	).Result()

	return err
}

// GetStats 读取会话统计
func (sm *Manager) GetStats(ctx context.Context, sessionID string) (*SessionStats, error) {
	if sm == nil || sm.redis == nil {
		return nil, ErrSessionNotFound
	}

	data, err := sm.redis.HGetAll(ctx, "session:"+sessionID)
	if err != nil || len(data) == 0 {
		return nil, ErrSessionNotFound
	}

	stats := &SessionStats{
		TotalTurns:            parseInt64(data[FieldTotalTurns]),
		TotalPromptTokens:     parseInt64(data[FieldTotalPromptTokens]),
		TotalCompletionTokens: parseInt64(data[FieldTotalCompletionTokens]),
		TotalCostUSDCents:     parseInt64(data[FieldTotalCostUSDCents]),
		FirstRequestAt:        parseTimeOrZero(data[FieldFirstRequestAt]),
		LastRequestAt:         parseTimeOrZero(data[FieldLastRequestAt]),
	}
	stats.TotalCostUSD = float64(stats.TotalCostUSDCents) / 10000.0

	return stats, nil
}

// StartCredRotation 开始新凭据轮换（LPUSH 新记录）
func (sm *Manager) StartCredRotation(ctx context.Context, sessionID string, credID int, model, provider, reason string) error {
	if sm == nil || sm.redis == nil || sessionID == "" {
		return nil
	}

	now := time.Now().UTC()
	entry := CredRotationEntry{
		CredentialID: credID,
		Model:        model,
		Provider:     provider,
		StartedAt:    now,
		EndedAt:      nil,
		Turns:        0,
		SwitchReason: reason,
	}
	entryJSON, _ := json.Marshal(entry)

	// 获取总轮次作为起始轮次
	totalTurns := parseInt64(getFieldFromRedis(ctx, sm, "session:"+sessionID, FieldTotalTurns))

	client := sm.redis.Client()
	if client == nil {
		return fmt.Errorf("redis client not available")
	}

	pipe := client.Pipeline()
	pipe.LPush(ctx, credRotationsKey(sessionID), entryJSON)
	pipe.HSet(ctx, "session:"+sessionID, map[string]any{
		FieldCurrentCredentialID:  credID,
		FieldCurrentCredTurns:     0,
		FieldCurrentCredStartAt:   now.Format(time.RFC3339),
		FieldCurrentCredStartTurn: totalTurns,
		FieldCurrentModel:         model,
		FieldCurrentProvider:      provider,
	})
	_, err := pipe.Exec(ctx)
	return err
}

// EndCredRotation 结束当前凭据轮换（更新 List 最后一条）
func (sm *Manager) EndCredRotation(ctx context.Context, sessionID string) error {
	if sm == nil || sm.redis == nil || sessionID == "" {
		return nil
	}

	client := sm.redis.Client()
	if client == nil {
		return fmt.Errorf("redis client not available")
	}

	rotationsKey := credRotationsKey(sessionID)
	lastJSON, err := client.LIndex(ctx, rotationsKey, 0).Result()
	if err == redis.Nil {
		return nil // 没有轮换记录
	}
	if err != nil {
		return err
	}

	var entry CredRotationEntry
	if err := json.Unmarshal([]byte(lastJSON), &entry); err != nil {
		return err
	}
	if entry.EndedAt != nil {
		return nil // 已经结束了
	}

	now := time.Now().UTC()
	entry.EndedAt = &now

	// 计算 turns 和 cost
	data, _ := sm.redis.HGetAll(ctx, "session:"+sessionID)
	entry.Turns = int(parseInt64(data[FieldCurrentCredTurns]))
	entry.PromptTokens = parseInt64(data[FieldTotalPromptTokens])
	entry.CompletionTokens = parseInt64(data[FieldTotalCompletionTokens])
	entry.CostUSDCents = parseInt64(data[FieldTotalCostUSDCents])

	updatedJSON, _ := json.Marshal(entry)
	client.LSet(ctx, rotationsKey, 0, updatedJSON)
	return nil
}

// GetCredRotations 读取凭据轮换历史
func (sm *Manager) GetCredRotations(ctx context.Context, sessionID string, limit int) ([]CredRotationEntry, error) {
	if sm == nil || sm.redis == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 100
	}

	client := sm.redis.Client()
	if client == nil {
		return nil, fmt.Errorf("redis client not available")
	}

	results, err := client.LRange(ctx, credRotationsKey(sessionID), 0, int64(limit-1)).Result()
	if err != nil {
		return nil, err
	}

	entries := make([]CredRotationEntry, 0, len(results))
	for _, r := range results {
		var entry CredRotationEntry
		if err := json.Unmarshal([]byte(r), &entry); err == nil {
			entries = append(entries, entry)
		}
	}
	return entries, nil
}

// TrimCredRotations 截断凭据轮换历史到最大条数
func (sm *Manager) TrimCredRotations(ctx context.Context, sessionID string, max int) error {
	if sm == nil || sm.redis == nil || max <= 0 {
		return nil
	}
	client := sm.redis.Client()
	if client == nil {
		return fmt.Errorf("redis client not available")
	}
	length, err := client.LLen(ctx, credRotationsKey(sessionID)).Result()
	if err != nil {
		return err
	}
	if length > int64(max+10) {
		// 只在超出较多时 trim，避免频繁操作
		return client.LTrim(ctx, credRotationsKey(sessionID), 0, int64(max-1)).Err()
	}
	return nil
}

// StopSession 停止会话
//
// 执行：
//  1. 设置 status=stopped
//  2. 结束当前凭据轮换
//  3. 维护 stopped 索引
//  4. 释放 slot
func (sm *Manager) StopSession(ctx context.Context, sessionID, reason string) error {
	if sm == nil || sm.redis == nil {
		return ErrSessionNotFound
	}

	data, err := sm.redis.HGetAll(ctx, "session:"+sessionID)
	if err != nil || len(data) == 0 {
		return ErrSessionNotFound
	}

	now := time.Now().UTC()
	apiKeyID := parseInt64(data[FieldAPIKeyID])
	tenantID := data[FieldTenantID]

	// 结束当前凭据轮换
	if err := sm.EndCredRotation(ctx, sessionID); err != nil {
		// 记录日志但不阻止
	}

	client := sm.redis.Client()
	if client == nil {
		return fmt.Errorf("redis client not available")
	}

	pipe := client.Pipeline()
	pipe.HSet(ctx, "session:"+sessionID, map[string]any{
		FieldStatus:     StatusStopped,
		FieldStoppedAt:  now.Format(time.RFC3339),
		FieldStopReason: reason,
	})

	// 维护停止索引
	stoppedKey := fmt.Sprintf("session:stopped:%s", tenantID)
	pipe.SAdd(ctx, stoppedKey, sessionID)
	// stopped 索引 TTL 由清理 Worker 维护，或使用更长 TTL
	pipe.Expire(ctx, stoppedKey, 24*time.Hour)

	// 从 active 集合移除
	if apiKeyID > 0 {
		pipe.SRem(ctx, fmt.Sprintf("session:apiKey:%d:active", apiKeyID), sessionID)
	}

	_, err = pipe.Exec(ctx)
	return err
}

// RecoverSession 恢复已停止的会话
func (sm *Manager) RecoverSession(ctx context.Context, sessionID string) error {
	if sm == nil || sm.redis == nil {
		return ErrSessionNotFound
	}

	data, err := sm.redis.HGetAll(ctx, "session:"+sessionID)
	if err != nil || len(data) == 0 {
		return ErrSessionNotFound
	}

	status := data[FieldStatus]
	if status != StatusStopped {
		return fmt.Errorf("session is not stopped (current status: %s)", status)
	}

	now := time.Now().UTC()
	apiKeyID := parseInt64(data[FieldAPIKeyID])
	tenantID := data[FieldTenantID]

	client := sm.redis.Client()
	if client == nil {
		return fmt.Errorf("redis client not available")
	}

	pipe := client.Pipeline()
	pipe.HSet(ctx, "session:"+sessionID, map[string]any{
		FieldStatus:      StatusRecovered,
		FieldRecoveredAt: now.Format(time.RFC3339),
	})
	pipe.SRem(ctx, fmt.Sprintf("session:stopped:%s", tenantID), sessionID)
	if apiKeyID > 0 {
		pipe.SAdd(ctx, fmt.Sprintf("session:apiKey:%d:active", apiKeyID), sessionID)
	}

	_, err = pipe.Exec(ctx)
	return err
}

// SetTitle 设置会话标题
func (sm *Manager) SetTitle(ctx context.Context, sessionID, title string) error {
	if sm == nil || sm.redis == nil {
		return nil
	}
	if len(title) > 200 {
		title = title[:200]
	}
	if err := sm.redis.HSet(ctx, "session:"+sessionID, map[string]any{FieldTitle: title}); err != nil {
		return err
	}
	client := sm.redis.Client()
	if client != nil {
		return client.Expire(ctx, "session:"+sessionID, sm.ttl).Err()
	}
	return nil
}

// SetAnnotation 设置会话标注
func (sm *Manager) SetAnnotation(ctx context.Context, sessionID, annotation string) error {
	if sm == nil || sm.redis == nil {
		return nil
	}
	if len(annotation) > 500 {
		annotation = annotation[:500]
	}
	if err := sm.redis.HSet(ctx, "session:"+sessionID, map[string]any{FieldAnnotation: annotation}); err != nil {
		return err
	}
	client := sm.redis.Client()
	if client != nil {
		return client.Expire(ctx, "session:"+sessionID, sm.ttl).Err()
	}
	return nil
}

// SetTags 设置会话标签（逗号分隔）
func (sm *Manager) SetTags(ctx context.Context, sessionID string, tags []string) error {
	if sm == nil || sm.redis == nil {
		return nil
	}
	tagsStr := strings.Join(tags, ",")
	if err := sm.redis.HSet(ctx, "session:"+sessionID, map[string]any{FieldTags: tagsStr}); err != nil {
		return err
	}
	client := sm.redis.Client()
	if client != nil {
		return client.Expire(ctx, "session:"+sessionID, sm.ttl).Err()
	}
	return nil
}

// SetClientInfo 设置客户端信息
func (sm *Manager) SetClientInfo(ctx context.Context, sessionID, ip, fp string) error {
	if sm == nil || sm.redis == nil {
		return nil
	}
	fields := map[string]any{}
	if ip != "" {
		fields[FieldClientIP] = ip
	}
	if fp != "" {
		if len(fp) > 16 {
			fp = fp[:16]
		}
		fields[FieldClientFP] = fp
	}
	if len(fields) == 0 {
		return nil
	}
	if err := sm.redis.HSet(ctx, "session:"+sessionID, fields); err != nil {
		return err
	}
	client := sm.redis.Client()
	if client != nil {
		return client.Expire(ctx, "session:"+sessionID, sm.ttl).Err()
	}
	return nil
}

// SetFPSlot 设置 fingerprint slot 快照
func (sm *Manager) SetFPSlot(ctx context.Context, sessionID string, slotIndex, credID int) error {
	if sm == nil || sm.redis == nil {
		return nil
	}
	if err := sm.redis.HSet(ctx, "session:"+sessionID, map[string]any{
		FieldFPSlotIndex:        slotIndex,
		FieldFPSlotCredentialID: credID,
	}); err != nil {
		return err
	}
	client := sm.redis.Client()
	if client != nil {
		return client.Expire(ctx, "session:"+sessionID, sm.ttl).Err()
	}
	return nil
}

// ListActiveSessions 列出活跃会话
func (sm *Manager) ListActiveSessions(ctx context.Context, apiKeyID int64, limit int) ([]string, error) {
	if sm == nil || sm.redis == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 100
	}
	client := sm.redis.Client()
	if client == nil {
		return nil, fmt.Errorf("redis client not available")
	}
	return client.SRandMemberN(ctx, fmt.Sprintf("session:apiKey:%d:active", apiKeyID), int64(limit)).Result()
}

// ListStoppedSessions 列出已停止会话
func (sm *Manager) ListStoppedSessions(ctx context.Context, tenantID string, limit int) ([]string, error) {
	if sm == nil || sm.redis == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 100
	}
	client := sm.redis.Client()
	if client == nil {
		return nil, fmt.Errorf("redis client not available")
	}
	return client.SRandMemberN(ctx, fmt.Sprintf("session:stopped:%s", tenantID), int64(limit)).Result()
}

// GetEnrichedSession 读取完整的会话详情（含统计与轮换历史）
func (sm *Manager) GetEnrichedSession(ctx context.Context, sessionID string) (*Session, *SessionStats, []CredRotationEntry, error) {
	session, err := sm.Get(ctx, sessionID)
	if err != nil {
		return nil, nil, nil, err
	}
	stats, err := sm.GetStats(ctx, sessionID)
	if err != nil {
		stats = &SessionStats{}
	}
	rotations, err := sm.GetCredRotations(ctx, sessionID, 100)
	if err != nil {
		rotations = []CredRotationEntry{}
	}
	return session, stats, rotations, nil
}

// ── 辅助函数 ─────────────────────────────────────────────────────────

func credRotationsKey(sessionID string) string {
	return "session:" + sessionID + ":cred_rotations"
}

func getFieldFromRedis(ctx context.Context, sm *Manager, key, field string) string {
	if sm == nil || sm.redis == nil {
		return ""
	}
	client := sm.redis.Client()
	if client == nil {
		return ""
	}
	val, _ := client.HGet(ctx, key, field).Result()
	return val
}

func parseInt64(s string) int64 {
	if s == "" {
		return 0
	}
	v, _ := strconv.ParseInt(s, 10, 64)
	return v
}

func parseTimeOrZero(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, _ := parseTime(s)
	return t
}
