// Package session — approval_resume.go
//
// ApprovalResumeHandler 实现了"审批完成 → 恢复会话"的核心逻辑。
//
// 流程：
//  1. 客户端 → ApprovalHook 创建 approval_queue 行 + 返回 202 + ErrApprovalRequired
//  2. 客户端轮询 /v1/sessions/:id/pending-response
//  3. 管理员通过 admin UI 调 approval API approve/reject
//  4. 审批 DB 行 status 变为 approved/rejected
//  5. （此处）ApprovalResumeHandler.ResumeAfterApproval 从快照恢复：
//     - approved → 用 LLMCaller 重新发起 LLM 调用
//     - rejected → 用 ClientResponder 把拒绝原因回写客户端
//
// 设计要点：
//   - ResumeAfterApproval 是幂等的：状态字段在 DB 已经决定，再次调用结果一致
//   - LLMCaller 和 ClientResponder 由 main.go 注入，本包不依赖 streaming
//     避免循环导入
//   - 恢复后的响应回到原客户端连接或 pending-response 轮询 endpoint
//   - 任何失败仅记录日志，不修改 approval_queue 行（status 由 admin API 控制）
package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/compression"
	"github.com/kaixuan/llm-gateway-go/domains/sessionaudit" //nolint:depguard // domain linkage intentional
)

// ──────────────────────────────────────────────────────────────────────────────
// Public errors
// ──────────────────────────────────────────────────────────────────────────────

// ErrResumeNotPending 审批行已是终态（approved/rejected/timeout），不能再 resume。
var ErrResumeNotPending = errors.New("session: approval already decided, cannot resume")

// ErrResumeSnapshotMissing 审批行缺少 snapshot，无法恢复 LLM 调用。
var ErrResumeSnapshotMissing = errors.New("session: approval snapshot missing")

// ErrResumeRejected 审批被拒绝（语义化 error，便于调用方识别）。
var ErrResumeRejected = errors.New("session: approval rejected")

// ErrResumeTimeout 审批超时。
var ErrResumeTimeout = errors.New("session: approval timed out")

// ──────────────────────────────────────────────────────────────────────────────
// Public interfaces (DI)
// ──────────────────────────────────────────────────────────────────────────────

// LLMCaller 抽象"调用一次 LLM 并把响应写回客户端"。
//
// 真实实现是 streaming.ChatHandler.serveWithExecutor 的封装；但本包
// 不直接 import streaming（避免循环导入），由 main.go 注入 adapter。
//
// 契约：
//   - 输入：ctx + 已恢复的 SessionContext + 原始 request bytes
//   - 副作用：直接写 http.ResponseWriter
//   - 返回：成功或失败；调用方在 approved 路径不需要再处理 ResponseWriter
type LLMCaller interface {
	CallFromSnapshot(ctx context.Context, snapshot *sessionaudit.RequestSnapshot) error
}

// ClientResponder 抽象"向客户端写一条异步响应"。
//
// 真实实现：
//   - 如果客户端还连接着 SSE，把消息 push 到流
//   - 否则写到 pending.Response 让客户端轮询 /pending-response 拿到
//
// 拒绝路径只需要写入一条简短 JSON；不需要再调用 LLM。
type ClientResponder interface {
	Respond(ctx context.Context, snapshot *sessionaudit.RequestSnapshot, payload any) error
	RespondRejection(ctx context.Context, snapshot *sessionaudit.RequestSnapshot, reason string) error
}

// ApprovalGetter 抽象"按 ID 获取审批记录"。
//
// 真实实现是 *sessionaudit.ApprovalManager.GetForTenant。
// 接口化便于测试 mock。
type ApprovalGetter interface {
	GetForTenant(ctx context.Context, approvalID, expectedTenantID string) (*sessionaudit.ApprovalRecord, error)
}

// ApprovalPendingWriter 抽象"写入 pending response 供客户端轮询"。
//
// 与 session.Handler 的 PendingStore（reader）不同——这里只关心写
// （resume 路径是 producer），读路径由 session.Handler 处理。
//
// 真实实现是 *pending.Store.Save。
type ApprovalPendingWriter interface {
	Save(ctx context.Context, entry *PendingResumeEntry) error
}

// PendingResumeEntry resume 写入的 pending entry（最小字段）。
type PendingResumeEntry struct {
	SessionID    string
	TenantID     string
	RequestID    string
	Status       string // "completed" | "failed"
	Body         string
	ContentType  string
	CompletedAt  int64
	ErrorMessage string
}

// ──────────────────────────────────────────────────────────────────────────────
// Handler
// ──────────────────────────────────────────────────────────────────────────────

// ApprovalResumeHandler 审批恢复处理器。
type ApprovalResumeHandler struct {
	sessionCache *compression.SessionCache
	approvalMgr  ApprovalGetter
	llmCaller    LLMCaller
	responder    ClientResponder
	pendingStore ApprovalPendingWriter
	now          func() time.Time
}

// NewApprovalResumeHandler 构造处理器。
func NewApprovalResumeHandler(
	sc *compression.SessionCache,
	mgr ApprovalGetter,
	llm LLMCaller,
	resp ClientResponder,
	pending ApprovalPendingWriter,
) *ApprovalResumeHandler {
	return &ApprovalResumeHandler{
		sessionCache: sc,
		approvalMgr:  mgr,
		llmCaller:    llm,
		responder:    resp,
		pendingStore: pending,
		now:          time.Now,
	}
}

// SetNow 注入时钟（测试用）。
func (h *ApprovalResumeHandler) SetNow(now func() time.Time) {
	if now != nil {
		h.now = now
	}
}

// ResumeAfterApproval 入口：审批完成后恢复会话。
//
// 参数：
//   - approvalID：审批队列 UUID
//   - expectedTenantID：调用方租户（防止跨租户访问）；空字符串 = super_admin
//
// 返回：
//   - nil：成功（approved → 调 LLM 完成；rejected → 拒绝响应已发出）
//   - error：恢复失败（DB/网络/快照缺失）
//
// 幂等：approval_queue.status 已是终态时返回 ErrResumeNotPending。
func (h *ApprovalResumeHandler) ResumeAfterApproval(ctx context.Context, approvalID, expectedTenantID string) error {
	if approvalID == "" {
		return errors.New("session: approval_id required")
	}

	// 1. 拉取审批记录
	record, err := h.approvalMgr.GetForTenant(ctx, approvalID, expectedTenantID)
	if err != nil {
		return fmt.Errorf("get approval: %w", err)
	}
	if record == nil {
		return errors.New("session: approval record nil")
	}

	// 2. 根据终态分发
	switch record.Status {
	case sessionaudit.ApprovalApproved:
		return h.continueToLLM(ctx, record)
	case sessionaudit.ApprovalRejected:
		return h.respondRejection(ctx, record)
	case sessionaudit.ApprovalTimeout:
		return h.respondTimeout(ctx, record)
	default:
		// pending：尚未终态，不应 resume
		return ErrResumeNotPending
	}
}

// ResumeApproved 仅处理 approved 路径（admin 主动 approve 触发）。
func (h *ApprovalResumeHandler) ResumeApproved(ctx context.Context, approvalID, tenantID string) error {
	return h.ResumeAfterApproval(ctx, approvalID, tenantID)
}

// ResumeRejected 仅处理 rejected 路径。
func (h *ApprovalResumeHandler) ResumeRejected(ctx context.Context, approvalID, tenantID string) error {
	return h.ResumeAfterApproval(ctx, approvalID, tenantID)
}

// continueToLLM 批准后调用 LLM。
func (h *ApprovalResumeHandler) continueToLLM(ctx context.Context, record *sessionaudit.ApprovalRecord) error {
	if record.Snapshot == nil {
		return ErrResumeSnapshotMissing
	}

	// 1. 先把 SessionState 标记为 approved（业务态）
	h.markSessionState(record, compression.ApprovalStateApproved)

	// 2. 把 approval 事件落到 pending-response（让正在轮询的客户端停止 202 等待）
	if err := h.respondApprovalPending(record); err != nil {
		// 写 pending 失败不阻断 LLM 调用（pending 只是辅助通道）
		slog.Warn("approval_resume: pending notify failed",
			"approval_id", record.ID, "error", err)
	}

	// 3. 调用 LLM
	if h.llmCaller == nil {
		return errors.New("session: llm caller not configured")
	}
	if err := h.llmCaller.CallFromSnapshot(ctx, record.Snapshot); err != nil {
		// LLM 调用失败 → 写一条 failed pending 让客户端拿到错误
		slog.Error("approval_resume: llm call failed",
			"approval_id", record.ID, "session_id", record.SessionID, "error", err)
		_ = h.respondLLMFailure(ctx, record, err)
		return fmt.Errorf("llm call from snapshot: %w", err)
	}

	slog.Info("approval_resume: llm call completed",
		"approval_id", record.ID,
		"session_id", record.SessionID,
		"tenant_id", record.TenantID,
	)
	return nil
}

// respondRejection 拒绝时回写客户端。
func (h *ApprovalResumeHandler) respondRejection(ctx context.Context, record *sessionaudit.ApprovalRecord) error {
	h.markSessionState(record, compression.ApprovalStateRejected)

	reason := record.Reason
	if reason == "" {
		reason = "Request rejected by approval"
	}

	// 1. 通过 responder 写一条拒绝消息（如果客户端还连着）
	if h.responder != nil && record.Snapshot != nil {
		if err := h.responder.RespondRejection(ctx, record.Snapshot, reason); err != nil {
			slog.Warn("approval_resume: responder write rejection failed",
				"approval_id", record.ID, "error", err)
		}
	}

	// 2. 写 pending-response（兜底：客户端断开后重连轮询）
	if err := h.writeRejectionPending(record, reason); err != nil {
		slog.Warn("approval_resume: pending rejection write failed",
			"approval_id", record.ID, "error", err)
	}

	slog.Info("approval_resume: rejection delivered",
		"approval_id", record.ID, "session_id", record.SessionID)
	return nil
}

// respondTimeout 超时时回写客户端（语义同拒绝，但提示不同）。
func (h *ApprovalResumeHandler) respondTimeout(ctx context.Context, record *sessionaudit.ApprovalRecord) error {
	h.markSessionState(record, compression.ApprovalStateTimeout)

	reason := "Approval timed out — request auto-rejected"

	if h.responder != nil && record.Snapshot != nil {
		if err := h.responder.RespondRejection(ctx, record.Snapshot, reason); err != nil {
			slog.Warn("approval_resume: responder write timeout failed",
				"approval_id", record.ID, "error", err)
		}
	}
	if err := h.writeRejectionPending(record, reason); err != nil {
		slog.Warn("approval_resume: pending timeout write failed",
			"approval_id", record.ID, "error", err)
	}

	slog.Info("approval_resume: timeout delivered",
		"approval_id", record.ID, "session_id", record.SessionID)
	return nil
}

// markSessionState 写 SessionState 的 approval status（best-effort）。
func (h *ApprovalResumeHandler) markSessionState(record *sessionaudit.ApprovalRecord, state string) {
	if h.sessionCache == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	st, _, err := h.sessionCache.GetOrLoad(ctx, record.TenantID, record.SessionID)
	if err != nil || st == nil {
		// 没有 state：新建一个只含 v6 字段的最小条目
		st = &compression.SessionState{SchemaVersion: 1}
	}
	st.SetApprovalResult(state)
	if err := h.sessionCache.Set(ctx, record.TenantID, record.SessionID, st, nil); err != nil {
		slog.Warn("approval_resume: cache write approval result failed",
			"approval_id", record.ID, "session_id", record.SessionID, "error", err)
	}
}

// respondApprovalPending approved 路径：先回 200 让客户端解除 pending 状态。
//
// 实际 LLM 响应会在 CallFromSnapshot 期间到达。pending 只承担"我开始处理了"
// 这个语义，避免客户端轮询空转。
func (h *ApprovalResumeHandler) respondApprovalPending(record *sessionaudit.ApprovalRecord) error {
	if h.pendingStore == nil || record.Snapshot == nil {
		return nil
	}
	body := map[string]any{
		"status":      "in_progress",
		"approval_id": record.ID,
		"message":     "Approval granted — resuming LLM call",
	}
	bodyJSON, _ := json.Marshal(body)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return h.pendingStore.Save(ctx, &PendingResumeEntry{
		SessionID:   record.SessionID,
		TenantID:    record.TenantID,
		RequestID:   record.RequestID,
		Status:      "in_progress",
		Body:        string(bodyJSON),
		ContentType: "application/json",
		CompletedAt: h.now().Unix(),
	})
}

// writeRejectionPending 拒绝/超时：写一条 completed pending 让客户端拿到结果。
func (h *ApprovalResumeHandler) writeRejectionPending(record *sessionaudit.ApprovalRecord, reason string) error {
	if h.pendingStore == nil || record.Snapshot == nil {
		return nil
	}
	body := map[string]any{
		"error": map[string]string{
			"message":     reason,
			"type":        "approval_rejected",
			"code":        "REQUEST_REJECTED_BY_APPROVAL",
			"approval_id": record.ID,
		},
		"approval_id": record.ID,
		"status":      "completed",
	}
	bodyJSON, _ := json.Marshal(body)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return h.pendingStore.Save(ctx, &PendingResumeEntry{
		SessionID:    record.SessionID,
		TenantID:     record.TenantID,
		RequestID:    record.RequestID,
		Status:       "completed",
		Body:         string(bodyJSON),
		ContentType:  "application/json",
		CompletedAt:  h.now().Unix(),
		ErrorMessage: reason,
	})
}

// respondLLMFailure approved 但 LLM 失败：写 failed pending。
func (h *ApprovalResumeHandler) respondLLMFailure(ctx context.Context, record *sessionaudit.ApprovalRecord, llmErr error) error {
	if h.pendingStore == nil || record.Snapshot == nil {
		return nil
	}
	body := map[string]any{
		"status":        "failed",
		"approval_id":   record.ID,
		"error_message": llmErr.Error(),
	}
	bodyJSON, _ := json.Marshal(body)
	saveCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return h.pendingStore.Save(saveCtx, &PendingResumeEntry{
		SessionID:    record.SessionID,
		TenantID:     record.TenantID,
		RequestID:    record.RequestID,
		Status:       "failed",
		Body:         string(bodyJSON),
		ContentType:  "application/json",
		CompletedAt:  h.now().Unix(),
		ErrorMessage: llmErr.Error(),
	})
}

// BuildSnapshotFromApproval 工具函数：从 approval 行取出 snapshot，
// 供 main.go / handler 在 v1 ChatHandler 路径上直接使用 snapshot。
//
// 返回 nil 表示 snapshot 缺失（错误已经在 DB 一致性检查中报告）。
func BuildSnapshotFromApproval(record *sessionaudit.ApprovalRecord) *sessionaudit.RequestSnapshot {
	if record == nil {
		return nil
	}
	return record.Snapshot
}

// ──────────────────────────────────────────────────────────────────────────────
// Rejection 响应构造
// ──────────────────────────────────────────────────────────────────────────────

// RejectionResponse 拒绝响应体（用于 JSON 序列化）。
type RejectionResponse struct {
	Error      string `json:"error"`
	Reason     string `json:"reason"`
	ApprovalID string `json:"approval_id"`
	StatusCode int    `json:"-"`
}

// NewRejectionResponse 构造拒绝响应。
func NewRejectionResponse(approvalID, reason string) *RejectionResponse {
	return &RejectionResponse{
		Error:      "Request rejected by approval",
		Reason:     reason,
		ApprovalID: approvalID,
		StatusCode: http.StatusForbidden,
	}
}
