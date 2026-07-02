// Package sessionaudithook — approval_hook.go
//
// ApprovalHook 是会话审计流水线中的"高风险 → 审批"门控。它会在
// SessionAuditHook 给出 DecisionNeedApproval 后：
//
//  1. 用 ApprovalManager 写入 approval_queue 行（持久化审批请求）
//  2. 通过 CacheUpdateHook 把 approvalID 回填到 SessionState
//  3. 发布 ApprovalNeededEvent（异步通知 admin/Notifier）
//  4. 返回 ErrApprovalRequired 让 pipeline 暂停请求
//
// ErrApprovalRequired 是 sentinel error，调用方用 errors.Is 识别。
// 它不是真正的失败——pipeline 不应回滚客户端连接，客户端可以通过
// /v1/sessions/:id/pending-response 轮询审批结果。
package sessionaudithook

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/kaixuan/llm-gateway-go/domain"               //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/pipeline"     //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/sessionaudit" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/eventbus"
)

// ApprovalNotifier 通知接口（依赖注入）。
//
// 不直接 import notification 包，避免 approval_hook 与具体渠道耦合；
// 真实实现 (lark/email/webhook) 由 main.go 注入 adapter。
type ApprovalNotifier interface {
	NotifyApproval(ctx context.Context, record *sessionaudit.ApprovalRecord) error
}

// ErrApprovalRequired 高风险请求触发审批流程时返回的 sentinel error。
//
// 调用方（包括 pipeline 编排器）通过 errors.Is(err, ErrApprovalRequired)
// 判断。错误本身携带 approvalID，便于客户端立即知道去哪里轮询结果。
//
// 与 fmt.Errorf("approval_required: %s", id) 不同：sentinel 不依赖错误
// 文本，便于语义化识别。
var ErrApprovalRequired = errors.New("sessionaudit: approval required")

// ApprovalRequiredError 携带 approvalID 的错误类型。
//
// Wrap（用 %w）时仍可被 errors.Is(err, ErrApprovalRequired) 识别。
type ApprovalRequiredError struct {
	ApprovalID string    `json:"approval_id"`
	SessionID  string    `json:"session_id"`
	TenantID   string    `json:"tenant_id"`
	Reason     string    `json:"reason"`
	ExpiresAt  time.Time `json:"expires_at"`
}

// Error 实现 error 接口。
func (e *ApprovalRequiredError) Error() string {
	return fmt.Sprintf("approval required: %s (session=%s tenant=%s reason=%s)",
		e.ApprovalID, e.SessionID, e.TenantID, e.Reason)
}

// Is 实现 errors.Is 支持：当 target 为 ErrApprovalRequired 时返回 true。
func (e *ApprovalRequiredError) Is(target error) bool {
	return target == ErrApprovalRequired
}

// Unwrap 让 errors.Is/As 走标准链路。
func (e *ApprovalRequiredError) Unwrap() error { return ErrApprovalRequired }

// ApprovalHook 高风险检测 → 审批流程。
//
// 依赖：
//   - approvalMgr   ：*sessionaudit.ApprovalManager
//   - eventBus      ：用于广播 ApprovalNeededEvent
//   - cacheUpdator  ：用于回填 approvalID 到 SessionState（可选）
//   - notifier      ：实际发送通知（可为 nil）
//   - timeout       ：单次审批默认超时（0 → 15min）
type ApprovalHook struct {
	approvalMgr  *sessionaudit.ApprovalManager
	eventBus     *eventbus.MemoryBus
	cacheUpdator *CacheUpdateHook
	notifier     ApprovalNotifier
	timeout      time.Duration
	now          func() time.Time
}

// NewApprovalHook 创建 Hook。所有依赖都可为 nil，对应能力自动降级。
func NewApprovalHook(
	mgr *sessionaudit.ApprovalManager,
	bus *eventbus.MemoryBus,
	cacheHook *CacheUpdateHook,
	notifier ApprovalNotifier,
	timeout time.Duration,
) *ApprovalHook {
	if timeout <= 0 {
		timeout = 15 * time.Minute
	}
	return &ApprovalHook{
		approvalMgr:  mgr,
		eventBus:     bus,
		cacheUpdator: cacheHook,
		notifier:     notifier,
		timeout:      timeout,
		now:          time.Now,
	}
}

// Name 返回 Hook 名称。
func (h *ApprovalHook) Name() string {
	return "sessionaudit.approval"
}

// Priority 110：在 SessionAuditHook (100) 之后、ApprovalGateHook (105) 之前。
//
// 实际生产中 ApprovalGateHook 是 v2 pipeline 的传统实现，ApprovalHook
// 是新的模块化实现；二者不应同时启用。
func (h *ApprovalHook) Priority() int {
	return 110
}

// Enabled 当 metadata 中存在 audit_result 且判定为 NeedApproval 时启用。
func (h *ApprovalHook) Enabled(ctx context.Context, env *domain.PipelineRequest) bool {
	if h == nil || h.approvalMgr == nil || env == nil {
		return false
	}
	result, ok := env.Metadata["audit_result"].(*sessionaudit.DetectResult)
	if !ok || result == nil {
		return false
	}
	return result.Decision == sessionaudit.DecisionNeedApproval
}

// Execute 触发审批流程。返回 *ApprovalRequiredError 让 pipeline 暂停。
func (h *ApprovalHook) Execute(ctx context.Context, env *domain.PipelineRequest) error {
	if h == nil || h.approvalMgr == nil {
		return nil
	}

	result, ok := env.Metadata["audit_result"].(*sessionaudit.DetectResult)
	if !ok || result == nil {
		return nil
	}

	// 1. 构造快照（持久化到 approval_queue）
	snapshot := h.buildSnapshot(env, result)
	if snapshot == nil {
		// 快照构造失败：降级为 warn，不阻断
		slog.Warn("approval_hook: snapshot build failed, degrading",
			"session_id", env.SessionID, "tenant_id", env.TenantID)
		return nil
	}

	// 2. 创建审批记录
	req := &sessionaudit.ApprovalRequest{
		SessionID:    env.SessionID,
		TenantID:     env.TenantID,
		RequestID:    snapshot.RequestID,
		DetectResult: result,
		Snapshot:     snapshot,
		Timeout:      h.timeout,
	}
	approvalID, err := h.approvalMgr.Create(ctx, req)
	if err != nil {
		// 审批创建失败：阻断主流程（DB 写不进去 = 数据完整性受损）
		slog.Error("approval_hook: create approval failed",
			"session_id", env.SessionID, "tenant_id", env.TenantID, "error", err)
		return fmt.Errorf("create approval: %w", err)
	}

	// 3. 回填 approvalID 到 SessionState
	if h.cacheUpdator != nil {
		if cerr := h.cacheUpdator.UpdateApprovalID(ctx, env.TenantID, env.SessionID, approvalID); cerr != nil {
			// 回填失败仅记录日志（approvalID 已在 DB，恢复流程可用 GetForTenant 找回）
			slog.Warn("approval_hook: cache write approval id failed",
				"session_id", env.SessionID, "approval_id", approvalID, "error", cerr)
		}
	}

	// 4. 发布 ApprovalNeededEvent（异步处理）
	if h.eventBus != nil {
		expiresAt := h.now().Add(h.timeout)
		evt := &sessionaudit.ApprovalNeededEvent{
			ApprovalID:   approvalID,
			SessionID:    env.SessionID,
			TenantID:     env.TenantID,
			RequestID:    snapshot.RequestID,
			DetectResult: result,
			Snapshot:     snapshot,
			ExpiresAt:    expiresAt,
		}
		if perr := h.eventBus.Publish(evt); perr != nil {
			slog.Warn("approval_hook: publish event failed",
				"approval_id", approvalID, "error", perr)
		}
	}

	// 5. 发送审批通知（best-effort）
	if h.notifier != nil {
		record, gerr := h.approvalMgr.GetForTenant(ctx, approvalID, env.TenantID)
		if gerr != nil {
			slog.Warn("approval_hook: fetch approval for notify failed",
				"approval_id", approvalID, "error", gerr)
		} else if record != nil {
			if nerr := h.notifier.NotifyApproval(ctx, record); nerr != nil {
				slog.Error("approval_hook: notify approval failed",
					"approval_id", approvalID, "error", nerr)
			}
		}
	}

	// 6. 写入响应头（如果有可写的 ResponseWriter）
	if env.Envelope != nil && env.Envelope.Transport != nil && env.Envelope.Transport.W != nil {
		w := env.Envelope.Transport.W
		w.Header().Set("X-Approval-ID", approvalID)
		w.Header().Set("X-Approval-Status-URL", fmt.Sprintf("/v1/approvals/%s/status", approvalID))
	}

	// 7. 返回 sentinel error 暂停 pipeline
	slog.Info("approval_hook: paused for approval",
		"approval_id", approvalID,
		"session_id", env.SessionID,
		"tenant_id", env.TenantID,
		"reason", result.Reason,
	)
	return &ApprovalRequiredError{
		ApprovalID: approvalID,
		SessionID:  env.SessionID,
		TenantID:   env.TenantID,
		Reason:     result.Reason,
		ExpiresAt:  h.now().Add(h.timeout),
	}
}

// OnError pipeline.Hook 接口。审批错误已通过 err 表达，不再二次处理。
func (h *ApprovalHook) OnError(ctx context.Context, env *domain.PipelineRequest, err error) error {
	return err
}

// buildSnapshot 构造 RequestSnapshot 用于持久化 + 后续 resume。
//
// 没有 Envelope（因此没有 BodyBytes）时返回 nil，调用方据此降级。
// 这是合理的：resume 必须能拿到原始请求字节，没字节就不能继续。
func (h *ApprovalHook) buildSnapshot(env *domain.PipelineRequest, result *sessionaudit.DetectResult) *sessionaudit.RequestSnapshot {
	if env == nil || env.Envelope == nil || env.Envelope.Transport == nil {
		return nil
	}
	transport := env.Envelope.Transport

	snapshot := &sessionaudit.RequestSnapshot{
		SessionID:   env.SessionID,
		TenantID:    env.TenantID,
		RequestID:   generateRequestID(env),
		BodyBytes:   transport.BodyBytes,
		ClientModel: transport.ClientModel,
		ClientInfo: sessionaudit.ClientInfo{
			IP:        getClientIP(env),
			UserAgent: getUserAgent(env),
			Model:     transport.ClientModel,
		},
		DetectResult: result,
		CreatedAt:    h.now(),
	}
	return snapshot
}

// IsApprovalRequired 工具函数：判断错误是否为审批暂停错误。
//
// 推荐在 pipeline 编排器、客户端响应处理处使用：
//
//	if sessionaudithook.IsApprovalRequired(err) {
//	    // pending response / SSE keep-alive
//	}
func IsApprovalRequired(err error) bool {
	return errors.Is(err, ErrApprovalRequired)
}

// MarshalApprovalRequiredError 把错误转成可读的 JSON（用于日志/响应）。
func MarshalApprovalRequiredError(err error) ([]byte, bool) {
	var are *ApprovalRequiredError
	if !errors.As(err, &are) {
		return nil, false
	}
	b, mErr := json.Marshal(are)
	if mErr != nil {
		return nil, false
	}
	return b, true
}

// 编译期断言
var _ pipeline.Hook = (*ApprovalHook)(nil)
