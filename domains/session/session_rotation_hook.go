// Package session — session_rotation_hook.go
//
// 凭据轮换检测 Hook，在请求完成时检测凭据是否发生切换，
// 并记录轮换历史到 Redis + 更新会话统计。
package session

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"
)

// RotationHook 凭据轮换检测 Hook
type RotationHook struct {
	manager        *Manager
	sessionPref    *SessionPreference
	enableTracking bool // 是否启用轮换追踪（可通过 Settings 控制）
}

// NewRotationHook 创建凭据轮换检测 Hook
func NewRotationHook(manager *Manager, sessionPref *SessionPreference) *RotationHook {
	return &RotationHook{
		manager:        manager,
		sessionPref:    sessionPref,
		enableTracking: true,
	}
}

// SetEnableTracking 设置是否启用追踪
func (h *RotationHook) SetEnableTracking(enable bool) {
	h.enableTracking = enable
}

// RotationContext 轮换上下文
type RotationContext struct {
	SessionID       string
	TenantID        string
	OldCredentialID int
	NewCredentialID int
	Model           string
	Provider        string
	SwitchReason    string
	FPSlotIndex     int
}

// OnRequestComplete 请求完成时调用
// 检测凭据是否切换，如果切换则：
// 1. 结束旧凭据的轮换记录
// 2. 开始新凭据的轮换记录
// 3. 更新 session_pref
func (h *RotationHook) OnRequestComplete(ctx context.Context, rotCtx *RotationContext) error {
	if h == nil || !h.enableTracking || rotCtx == nil {
		return nil
	}
	if h.manager == nil || h.sessionPref == nil {
		return nil
	}
	if rotCtx.SessionID == "" {
		return nil
	}

	// 1. 获取旧的凭据 ID
	oldCredID := rotCtx.OldCredentialID
	if oldCredID == 0 {
		// 尝试从 session_pref 读取
		pref, found := h.sessionPref.Get(ctx, rotCtx.SessionID)
		if found && pref != nil {
			oldCredID = pref.CredentialID
		}
	}

	newCredID := rotCtx.NewCredentialID
	if newCredID == 0 {
		return nil // 没有新凭据，跳过
	}

	// 2. 检测是否切换
	if oldCredID == newCredID && oldCredID > 0 {
		// 没有切换，跳过
		return nil
	}

	slog.Debug("session_rotation_hook: detected credential rotation",
		"session_id", rotCtx.SessionID,
		"old_cred", oldCredID,
		"new_cred", newCredID,
		"reason", rotCtx.SwitchReason,
	)

	// 3. 结束旧凭据轮换记录
	if oldCredID > 0 {
		if err := h.manager.EndCredRotation(ctx, rotCtx.SessionID); err != nil {
			slog.Warn("session_rotation_hook: end old rotation failed",
				"session_id", rotCtx.SessionID,
				"old_cred", oldCredID,
				"error", err,
			)
		}
	}

	// 4. 开始新凭据轮换记录
	reason := rotCtx.SwitchReason
	if reason == "" {
		if oldCredID == 0 {
			reason = SwitchReasonInitial
		} else {
			reason = SwitchReasonRotate
		}
	}

	if err := h.manager.StartCredRotation(ctx, rotCtx.SessionID, newCredID, rotCtx.Model, rotCtx.Provider, reason); err != nil {
		slog.Warn("session_rotation_hook: start new rotation failed",
			"session_id", rotCtx.SessionID,
			"new_cred", newCredID,
			"error", err,
		)
		return err
	}

	// 5a. 更新 FP Slot（如果有）
	if rotCtx.FPSlotIndex >= 0 {
		_ = h.manager.SetFPSlot(ctx, rotCtx.SessionID, rotCtx.FPSlotIndex, newCredID)
	}

	// 5. 更新 session_pref
	if err := h.sessionPref.Set(ctx, rotCtx.SessionID, newCredID, rotCtx.Model); err != nil {
		slog.Warn("session_rotation_hook: update session_pref failed",
			"session_id", rotCtx.SessionID,
			"new_cred", newCredID,
			"error", err,
		)
	}

	return nil
}

// OnUsageUpdate 更新会话使用统计
// 在每次请求完成后调用，累加 tokens 和 cost
func (h *RotationHook) OnUsageUpdate(ctx context.Context, sessionID string, usage *UsageUpdate) error {
	if h == nil || !h.enableTracking || h.manager == nil {
		return nil
	}
	if sessionID == "" || usage == nil {
		return nil
	}

	if err := h.manager.TouchUsage(ctx, sessionID, *usage); err != nil {
		slog.Warn("session_rotation_hook: touch usage failed",
			"session_id", sessionID,
			"error", err,
		)
		return err
	}

	return nil
}

// ExtractRotationContextFromMetadata 从请求 metadata 提取轮换上下文
// metadata 通常由 credential hooks 在 PreRouting 阶段写入
func ExtractRotationContextFromMetadata(metadata map[string]interface{}) *RotationContext {
	if metadata == nil {
		return nil
	}

	ctx := &RotationContext{}

	// 提取 session_id
	if v, ok := metadata["session_id"].(string); ok {
		ctx.SessionID = v
	}

	// 提取 tenant_id
	if v, ok := metadata["tenant_id"].(string); ok {
		ctx.TenantID = v
	}

	// 提取 credential_id
	if v, ok := metadata["credential_id"].(int); ok {
		ctx.NewCredentialID = v
	} else if v, ok := metadata["credential_cred_id"].(int); ok {
		ctx.NewCredentialID = v
	} else if v, ok := metadata["cred_id"].(int); ok {
		ctx.NewCredentialID = v
	}

	// 提取 model
	if v, ok := metadata["model"].(string); ok {
		ctx.Model = v
	}

	// 提取 provider
	if v, ok := metadata["provider"].(string); ok {
		ctx.Provider = v
	}

	// 提取 switch_reason
	if v, ok := metadata["switch_reason"].(string); ok {
		ctx.SwitchReason = v
	}

	// 提取 fp_slot_index
	if v, ok := metadata["fp_slot_index"].(int); ok {
		ctx.FPSlotIndex = v
	}

	return ctx
}

// Helper: 从 string 解析 int
func parseIntFromMetadata(v interface{}) int {
	switch val := v.(type) {
	case int:
		return val
	case int64:
		return int(val)
	case string:
		n, _ := strconv.Atoi(val)
		return n
	default:
		return 0
	}
}

// UsageUpdate 使用统计更新（复用 session_state.go 中的定义）
// 这里仅作为文档说明，实际使用 session.UsageUpdate

// RotationSummary 轮换摘要（用于统计报表）
type RotationSummary struct {
	SessionID          string
	TotalRotations     int
	RotationReasons    map[string]int // reason -> count
	AverageRotationGap time.Duration  // 平均轮换间隔
}

// GetRotationSummary 获取会话轮换摘要
func (h *RotationHook) GetRotationSummary(ctx context.Context, sessionID string) (*RotationSummary, error) {
	if h == nil || h.manager == nil {
		return nil, fmt.Errorf("manager not initialized")
	}

	rotations, err := h.manager.GetCredRotations(ctx, sessionID, 1000)
	if err != nil {
		return nil, err
	}

	summary := &RotationSummary{
		SessionID:       sessionID,
		TotalRotations:  len(rotations),
		RotationReasons: make(map[string]int),
	}

	if len(rotations) == 0 {
		return summary, nil
	}

	// 统计原因分布
	for _, r := range rotations {
		summary.RotationReasons[r.SwitchReason]++
	}

	// 计算平均轮换间隔
	var totalGap time.Duration
	validGaps := 0
	for i := 1; i < len(rotations); i++ {
		prev := rotations[i]
		curr := rotations[i-1]
		if !prev.StartedAt.IsZero() && !curr.StartedAt.IsZero() {
			gap := curr.StartedAt.Sub(prev.StartedAt)
			if gap > 0 {
				totalGap += gap
				validGaps++
			}
		}
	}
	if validGaps > 0 {
		summary.AverageRotationGap = totalGap / time.Duration(validGaps)
	}

	return summary, nil
}
