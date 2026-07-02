// Package main — approval_integration.go
//
// 审批恢复 (Approval Resume) 集成模块。
//
// 为 main.go 提供：
//   - NewApprovalResumeHandler: 创建 ApprovalResumeHandler 及其 adapter 链
//   - InitializeApprovalIntegration: 完整初始化（含 ApprovalHook/CacheUpdateHook，
//     供未来 PreRouting/PostRouting 管道使用，当前 main.go 未调用）
//   - createLLMCaller: 从 RequestSnapshot 重建 HTTP 请求并调用 ChatHandler
//
// 架构说明：
//   审批触发由现有 ApprovalGateHook (pipeline priority 105) 完成，
//   它在请求进入时检测敏感内容并创建 approval_queue 记录 + snapshot。
//   本模块只负责 resume 侧：管理员 approve 后，从 DB record 中的 snapshot
//   恢复 LLM 调用并将响应写入 pending.Store 供客户端轮询。

package main

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/compression"
	sessionaudithook "github.com/kaixuan/llm-gateway-go/domains/hooks/sessionaudit"
	"github.com/kaixuan/llm-gateway-go/domains/session"
	"github.com/kaixuan/llm-gateway-go/domains/sessionaudit"
	"github.com/kaixuan/llm-gateway-go/domains/streaming"
	"github.com/kaixuan/llm-gateway-go/eventbus"
	"github.com/kaixuan/llm-gateway-go/pending"
)

// NewApprovalResumeHandler 创建 ApprovalResumeHandler 并完成所有 adapter 装配。
//
// 这是 main.go 实际调用的入口。它只创建 resume 路径需要的组件，
// 不创建 ApprovalHook/CacheUpdateHook（后者需要 PreRouting/PostRouting 管道支持，
// ChatHandler 尚未实现）。
func NewApprovalResumeHandler(
	scCache *compression.SessionCache,
	approvalMgr *sessionaudit.ApprovalManager,
	chatHandler *streaming.ChatHandler,
	pendingStore *pending.Store,
	_ time.Duration,
) (*session.ApprovalResumeHandler, error) {
	if scCache == nil {
		return nil, fmt.Errorf("approval resume: SessionCache is required")
	}
	if approvalMgr == nil {
		return nil, fmt.Errorf("approval resume: ApprovalMgr is required")
	}
	if chatHandler == nil {
		return nil, fmt.Errorf("approval resume: ChatHandler is required")
	}
	if pendingStore == nil {
		return nil, fmt.Errorf("approval resume: PendingStore is required")
	}

	llmCaller := createLLMCaller(chatHandler, pendingStore)
	responder := session.NewPendingStoreResponder(pendingStore)
	pendingWriter := session.NewPendingStoreAdapter(pendingStore)

	return session.NewApprovalResumeHandler(
		scCache, approvalMgr, llmCaller, responder, pendingWriter,
	), nil
}

// ──────────────────────────────────────────────────────────────────────────────
// 完整初始化（含 ApprovalHook/CacheUpdateHook，供未来管道使用）
// ──────────────────────────────────────────────────────────────────────────────

// ApprovalIntegrationDeps 包含审批流程所需的所有依赖。
type ApprovalIntegrationDeps struct {
	SessionCache    *compression.SessionCache
	ApprovalMgr     *sessionaudit.ApprovalManager
	PendingStore    *pending.Store
	ChatHandler     *streaming.ChatHandler
	AuditBus        *eventbus.MemoryBus
	Notifier        sessionaudithook.ApprovalNotifier
	ApprovalTimeout time.Duration
}

// ApprovalIntegrationResult 包含创建的所有审批组件。
type ApprovalIntegrationResult struct {
	CacheUpdateHook *sessionaudithook.CacheUpdateHook
	ApprovalHook    *sessionaudithook.ApprovalHook
	ResumeHandler   *session.ApprovalResumeHandler

	PendingWriter   session.ApprovalPendingWriter
	ClientResponder session.ClientResponder
	LLMCaller       session.LLMCaller
}

// InitializeApprovalIntegration 创建审批流程的所有组件。
//
// WARNING: 返回的 ApprovalHook 和 CacheUpdateHook 是为未来的
// PreRouting/PostRouting 管道设计的。ChatHandler 当前不支持注册它们，
// 因此这两个 hook 目前是"已创建但未接线"状态。
// main.go 应优先使用 NewApprovalResumeHandler。
func InitializeApprovalIntegration(deps *ApprovalIntegrationDeps) (*ApprovalIntegrationResult, error) {
	if deps == nil {
		return nil, fmt.Errorf("approval integration: deps is nil")
	}
	if deps.SessionCache == nil {
		return nil, fmt.Errorf("approval integration: SessionCache is required")
	}
	if deps.ApprovalMgr == nil {
		return nil, fmt.Errorf("approval integration: ApprovalMgr is required")
	}
	if deps.PendingStore == nil {
		return nil, fmt.Errorf("approval integration: PendingStore is required")
	}
	if deps.ChatHandler == nil {
		return nil, fmt.Errorf("approval integration: ChatHandler is required")
	}

	if deps.ApprovalTimeout == 0 {
		deps.ApprovalTimeout = 15 * time.Minute
	}

	result := &ApprovalIntegrationResult{}

	result.CacheUpdateHook = sessionaudithook.NewCacheUpdateHook(deps.SessionCache)
	slog.Info("approval integration: CacheUpdateHook created")

	result.ApprovalHook = sessionaudithook.NewApprovalHook(
		deps.ApprovalMgr,
		deps.AuditBus,
		result.CacheUpdateHook,
		deps.Notifier,
		deps.ApprovalTimeout,
	)
	slog.Info("approval integration: ApprovalHook created", "timeout", deps.ApprovalTimeout.String())

	result.PendingWriter = session.NewPendingStoreAdapter(deps.PendingStore)
	result.ClientResponder = session.NewPendingStoreResponder(deps.PendingStore)
	result.LLMCaller = createLLMCaller(deps.ChatHandler, deps.PendingStore)
	slog.Info("approval integration: adapters created")

	result.ResumeHandler = session.NewApprovalResumeHandler(
		deps.SessionCache,
		deps.ApprovalMgr,
		result.LLMCaller,
		result.ClientResponder,
		result.PendingWriter,
	)
	slog.Info("approval integration: ApprovalResumeHandler created")

	return result, nil
}

// ──────────────────────────────────────────────────────────────────────────────
// LLMCaller 实现
// ──────────────────────────────────────────────────────────────────────────────

// createLLMCaller 创建 LLMCaller adapter，包装 streaming.ChatHandler。
//
// 从 RequestSnapshot 重建 HTTP 请求，调用 ChatHandler.ServeHTTP，
// 将响应写入 pending.Store。
func createLLMCaller(chatHandler *streaming.ChatHandler, pendingStore *pending.Store) session.LLMCaller {
	return session.LLMCallerFunc(func(ctx context.Context, snap *sessionaudit.RequestSnapshot) error {
		if snap == nil {
			return fmt.Errorf("llm caller: snapshot is nil")
		}

		body := bytes.NewReader(snap.BodyBytes)
		req, err := http.NewRequestWithContext(ctx, "POST", "/v1/chat/completions", body)
		if err != nil {
			return fmt.Errorf("llm caller: create request: %w", err)
		}

		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Session-ID", snap.SessionID)
		req.Header.Set("X-Tenant-ID", snap.TenantID)
		req.Header.Set("X-Request-ID", snap.RequestID)

		if snap.ClientInfo.IP != "" {
			req.Header.Set("X-Real-IP", snap.ClientInfo.IP)
			req.Header.Set("X-Forwarded-For", snap.ClientInfo.IP)
		}
		if snap.ClientInfo.UserAgent != "" {
			req.Header.Set("User-Agent", snap.ClientInfo.UserAgent)
		}

		rec := httptest.NewRecorder()

		slog.Info("llm caller: invoking ChatHandler",
			"session_id", snap.SessionID,
			"tenant_id", snap.TenantID,
			"request_id", snap.RequestID)

		chatHandler.ServeHTTP(rec, req)

		if rec.Code >= 400 {
			return fmt.Errorf("llm caller: ChatHandler returned status %d: %s",
				rec.Code, rec.Body.String())
		}

		contentType := rec.Header().Get("Content-Type")
		isStream := strings.Contains(contentType, "text/event-stream")

		now := time.Now().Unix()
		resp := &pending.Response{
			SessionID:     snap.SessionID,
			TenantID:      snap.TenantID,
			RequestID:     snap.RequestID,
			Status:        pending.StatusCompleted,
			Body:          rec.Body.String(),
			ContentType:   contentType,
			CreatedAt:     now,
			CompletedAt:   now,
			BytesBuffered: rec.Body.Len(),
			IsStream:      isStream,
		}

		if err := pendingStore.Save(ctx, resp); err != nil {
			return fmt.Errorf("llm caller: save to pending store: %w", err)
		}

		slog.Info("llm caller: response saved to pending store",
			"session_id", snap.SessionID,
			"request_id", snap.RequestID,
			"bytes", resp.BytesBuffered,
			"is_stream", isStream)

		return nil
	})
}
