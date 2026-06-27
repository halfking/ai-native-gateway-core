package sessionaudithook

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/kaixuan/llm-gateway-go/domain"
	"github.com/kaixuan/llm-gateway-go/domains/pipeline"
	"github.com/kaixuan/llm-gateway-go/domains/sessionaudit"
	"github.com/kaixuan/llm-gateway-go/eventbus"
	"github.com/kaixuan/llm-gateway-go/pending"
)

// ApprovalGateHook 审批门控 Hook
//
// 职责：
//   - 检查是否需要审批（metadata 中的 audit_result.Decision）
//   - 缓存请求快照到 pending.Store
//   - 创建审批记录
//   - 返回 202 Accepted 并阻断后续 Hook
type ApprovalGateHook struct {
	pendingStore *pending.Store
	approvalMgr  *sessionaudit.ApprovalManager
	eventBus     *eventbus.MemoryBus
}

// NewApprovalGateHook 创建审批门控 Hook
func NewApprovalGateHook(store *pending.Store, mgr *sessionaudit.ApprovalManager, bus *eventbus.MemoryBus) *ApprovalGateHook {
	return &ApprovalGateHook{
		pendingStore: store,
		approvalMgr:  mgr,
		eventBus:     bus,
	}
}

func (h *ApprovalGateHook) Name() string {
	return "session.approval_gate"
}

func (h *ApprovalGateHook) Priority() int {
	return 105 // 在 SessionAuditHook (100) 之后
}

func (h *ApprovalGateHook) Enabled(ctx context.Context, env *domain.PipelineRequest) bool {
	return env != nil && h.approvalMgr != nil
}

func (h *ApprovalGateHook) Execute(ctx context.Context, env *domain.PipelineRequest) error {
	// 1. 检查是否需要审批
	result, ok := env.Metadata["audit_result"].(*sessionaudit.DetectResult)
	if !ok || result.Decision != sessionaudit.DecisionNeedApproval {
		return nil // 不需要审批，继续执行
	}

	slog.Info("approval required",
		"session_id", env.SessionID,
		"tenant_id", env.TenantID,
		"score", result.Score,
		"reason", result.Reason)

	// 2. 构造请求快照
	snapshot := h.buildSnapshot(env, result)
	if snapshot == nil {
		// 快照构建失败降级：记录日志但不阻断
		slog.Warn("failed to build snapshot, degrading", "session_id", env.SessionID)
		return nil
	}

	// 3. 缓存到 pending.Store（用于恢复）
	if err := h.cacheSnapshot(ctx, snapshot); err != nil {
		// 缓存失败降级：记录日志但不阻断
		slog.Warn("failed to cache snapshot", "error", err, "session_id", env.SessionID)
		return nil
	}

	// 4. 创建审批记录
	approvalID, err := h.approvalMgr.Create(ctx, &sessionaudit.ApprovalRequest{
		SessionID:    env.SessionID,
		TenantID:     env.TenantID,
		RequestID:    snapshot.RequestID,
		DetectResult: result,
		Snapshot:     snapshot,
		Timeout:      15 * time.Minute,
	})

	if err != nil {
		slog.Error("failed to create approval", "error", err)
		return fmt.Errorf("create approval: %w", err)
	}

	// 5. 发布事件（通知管理员）
	event := &sessionaudit.ApprovalNeededEvent{
		ApprovalID:   approvalID,
		SessionID:    env.SessionID,
		TenantID:     env.TenantID,
		RequestID:    snapshot.RequestID,
		DetectResult: result,
		Snapshot:     snapshot,
		ExpiresAt:    time.Now().Add(15 * time.Minute),
	}
	_ = h.eventBus.Publish(event) // 发布失败不阻断

	// 6. 返回 202 Accepted
	env.StatusCode = 202
	if env.Envelope != nil && env.Envelope.Transport != nil && env.Envelope.Transport.W != nil {
		w := env.Envelope.Transport.W
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Approval-ID", approvalID)
		w.Header().Set("X-Approval-Status-URL", fmt.Sprintf("/v1/approvals/%s/status", approvalID))
		w.WriteHeader(202)

		response := map[string]interface{}{
			"status":           "pending_approval",
			"approval_id":      approvalID,
			"message":          "Request requires manual review due to security policy",
			"reason":           result.Reason,
			"estimated_wait":   "5-15 minutes",
			"poll_url":         fmt.Sprintf("/v1/approvals/%s/status", approvalID),
			"threats_detected": len(result.Threats),
			"sensitive_words":  len(result.SensitiveWords),
		}

		if body, err := json.Marshal(response); err == nil {
			_, _ = w.Write(body)
		}
	}

	// 7. 阻断后续 Hook（不继续路由到上游）
	// 返回特殊错误表示"审批暂停"，调用方应识别此状态
	return fmt.Errorf("approval_required: %s", approvalID)
}

func (h *ApprovalGateHook) OnError(ctx context.Context, env *domain.PipelineRequest, err error) error {
	// 审批门控失败应该保留错误
	return err
}

// buildSnapshot 构造请求快照
func (h *ApprovalGateHook) buildSnapshot(env *domain.PipelineRequest, result *sessionaudit.DetectResult) *sessionaudit.RequestSnapshot {
	if env.Envelope == nil || env.Envelope.Transport == nil {
		return nil
	}

	transport := env.Envelope.Transport
	snapshot := &sessionaudit.RequestSnapshot{
		SessionID:    env.SessionID,
		TenantID:     env.TenantID,
		RequestID:    generateRequestID(env),
		BodyBytes:    transport.BodyBytes,
		ClientModel:  transport.ClientModel,
		DetectResult: result,
		CreatedAt:    time.Now(),
	}

	// 填充客户端信息
	if transport.R != nil {
		snapshot.ClientInfo = sessionaudit.ClientInfo{
			IP:        getClientIP(env),
			UserAgent: getUserAgent(env),
			Model:     transport.ClientModel,
		}
	}

	return snapshot
}

// cacheSnapshot 缓存快照到 pending.Store
func (h *ApprovalGateHook) cacheSnapshot(ctx context.Context, snapshot *sessionaudit.RequestSnapshot) error {
	if h.pendingStore == nil {
		return fmt.Errorf("pending store not available")
	}

	snapshotJSON, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("marshal snapshot: %w", err)
	}

	return h.pendingStore.Save(ctx, &pending.Response{
		SessionID:     snapshot.SessionID,
		TenantID:      snapshot.TenantID,
		RequestID:     snapshot.RequestID,
		Status:        pending.StatusInProgress,
		Body:          string(snapshotJSON),
		ContentType:   "application/json",
		CreatedAt:     time.Now().Unix(),
		BytesBuffered: len(snapshotJSON),
		IsStream:      false,
	})
}

// 编译期断言
var _ pipeline.Hook = (*ApprovalGateHook)(nil)
