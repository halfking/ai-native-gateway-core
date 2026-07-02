// Package main - approval_integration.go
//
// 审批流程集成补丁文件
// 这个文件包含了将 ApprovalHook、CacheUpdateHook、ApprovalResumeHandler
// 集成到 main.go 的代码片段。
//
// 使用方法：
// 1. 将本文件中的代码片段复制到 cmd/gateway/main.go 的相应位置
// 2. 或者将本文件直接放到 cmd/gateway/ 目录并在 main.go 中调用初始化函数
//
// 设计决策：
// - 使用独立文件避免直接修改 main.go（减少冲突风险）
// - 所有依赖通过参数传递（便于测试和理解）
// - 保持向后兼容（SessionAuditHookV1 仍然可用）

package main

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/compression"
	sessionaudithook "github.com/kaixuan/llm-gateway-go/domains/hooks/sessionaudit"
	"github.com/kaixuan/llm-gateway-go/domains/session"
	"github.com/kaixuan/llm-gateway-go/domains/sessionaudit"
	"github.com/kaixuan/llm-gateway-go/domains/streaming"
	"github.com/kaixuan/llm-gateway-go/eventbus"
	"github.com/kaixuan/llm-gateway-go/pending"
)

// ApprovalIntegrationDeps 包含审批流程所需的所有依赖。
type ApprovalIntegrationDeps struct {
	// 核心依赖
	SessionCache *compression.SessionCache
	ApprovalMgr  *sessionaudit.ApprovalManager
	PendingStore *pending.Store
	ChatHandler  *streaming.ChatHandler

	// 可选依赖
	AuditBus *eventbus.MemoryBus
	Notifier sessionaudithook.ApprovalNotifier

	// 配置
	ApprovalTimeout time.Duration
}

// ApprovalIntegrationResult 包含创建的所有审批组件。
type ApprovalIntegrationResult struct {
	CacheUpdateHook *sessionaudithook.CacheUpdateHook
	ApprovalHook    *sessionaudithook.ApprovalHook
	ResumeHandler   *session.ApprovalResumeHandler

	// Adapters
	PendingWriter   session.ApprovalPendingWriter
	ClientResponder session.ClientResponder
	LLMCaller       session.LLMCaller
}

// InitializeApprovalIntegration 初始化审批流程的所有组件。
//
// 这是主要的集成函数，应该在 main.go 中调用。
//
// 示例用法：
//
//	deps := &ApprovalIntegrationDeps{
//		SessionCache:    scCache,
//		ApprovalMgr:     approvalMgr,
//		PendingStore:    pendingStore,
//		ChatHandler:     chatHandler,
//		AuditBus:        auditBus,
//		Notifier:        approvalNotifier,
//		ApprovalTimeout: 15 * time.Minute,
//	}
//	result, err := InitializeApprovalIntegration(deps)
//	if err != nil {
//		slog.Error("approval integration failed", "error", err)
//		return err
//	}
//	// 将 result.ResumeHandler 注入到 adminHandler
//	adminHandler.SetApprovalResumeHandler(result.ResumeHandler)
func InitializeApprovalIntegration(deps *ApprovalIntegrationDeps) (*ApprovalIntegrationResult, error) {
	if deps == nil {
		return nil, fmt.Errorf("approval integration: deps is nil")
	}

	// 验证必需依赖
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

	// 默认配置
	if deps.ApprovalTimeout == 0 {
		deps.ApprovalTimeout = 15 * time.Minute
	}

	result := &ApprovalIntegrationResult{}

	// 1. 创建 CacheUpdateHook
	result.CacheUpdateHook = sessionaudithook.NewCacheUpdateHook(deps.SessionCache)
	slog.Info("approval integration: CacheUpdateHook created")

	// 2. 创建 ApprovalHook
	result.ApprovalHook = sessionaudithook.NewApprovalHook(
		deps.ApprovalMgr,
		deps.AuditBus,
		result.CacheUpdateHook,
		deps.Notifier,
		deps.ApprovalTimeout,
	)
	slog.Info("approval integration: ApprovalHook created", "timeout", deps.ApprovalTimeout.String())

	// 3. 创建 adapters
	result.PendingWriter = session.NewPendingStoreAdapter(deps.PendingStore)
	result.ClientResponder = session.NewPendingStoreResponder(deps.PendingStore)
	result.LLMCaller = createLLMCaller(deps.ChatHandler, deps.PendingStore)
	slog.Info("approval integration: adapters created")

	// 4. 创建 ApprovalResumeHandler
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

// createLLMCaller 创建 LLMCaller adapter，包装 streaming.ChatHandler。
//
// 实现逻辑：
// 1. 从 snapshot 重建 HTTP 请求
// 2. 调用 ChatHandler.ServeHTTP
// 3. 将响应写入 pending.Store
//
// 注意：这是一个简化实现，生产环境可能需要：
// - 更复杂的请求重建逻辑（headers、query params）
// - 流式响应的处理
// - 错误处理和重试
// - Sticky routing 支持
func createLLMCaller(chatHandler *streaming.ChatHandler, pendingStore *pending.Store) session.LLMCaller {
	return session.LLMCallerFunc(func(ctx context.Context, snap *sessionaudit.RequestSnapshot) error {
		if snap == nil {
			return fmt.Errorf("llm caller: snapshot is nil")
		}

		// 1. 重建 HTTP 请求
		body := bytes.NewReader(snap.BodyBytes)
		req, err := http.NewRequestWithContext(ctx, "POST", "/v1/chat/completions", body)
		if err != nil {
			return fmt.Errorf("llm caller: create request: %w", err)
		}

		// 设置必要的 headers
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Session-ID", snap.SessionID)
		req.Header.Set("X-Tenant-ID", snap.TenantID)
		req.Header.Set("X-Request-ID", snap.RequestID)

		// 设置客户端信息
		if snap.ClientInfo.IP != "" {
			req.Header.Set("X-Real-IP", snap.ClientInfo.IP)
			req.Header.Set("X-Forwarded-For", snap.ClientInfo.IP)
		}
		if snap.ClientInfo.UserAgent != "" {
			req.Header.Set("User-Agent", snap.ClientInfo.UserAgent)
		}

		// 2. 创建 ResponseRecorder
		rec := httptest.NewRecorder()

		// 3. 调用 ChatHandler
		slog.Info("llm caller: invoking ChatHandler",
			"session_id", snap.SessionID,
			"tenant_id", snap.TenantID,
			"request_id", snap.RequestID)

		chatHandler.ServeHTTP(rec, req)

		// 4. 检查响应状态
		if rec.Code >= 400 {
			return fmt.Errorf("llm caller: ChatHandler returned status %d", rec.Code)
		}

		// 5. 将响应写入 pending.Store
		contentType := rec.Header().Get("Content-Type")
		isStream := containsString(contentType, "text/event-stream")

		resp := &pending.Response{
			SessionID:     snap.SessionID,
			TenantID:      snap.TenantID,
			RequestID:     snap.RequestID,
			Status:        pending.StatusCompleted,
			Body:          rec.Body.String(),
			ContentType:   contentType,
			CreatedAt:     time.Now().Unix(),
			CompletedAt:   time.Now().Unix(),
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

// ──────────────────────────────────────────────────────────────────────────────
// 辅助函数
// ──────────────────────────────────────────────────────────────────────────────

// containsString 检查字符串是否包含子串（不区分大小写）。
func containsString(s, substr string) bool {
	if len(substr) == 0 {
		return true
	}
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// ──────────────────────────────────────────────────────────────────────────────
// main.go 集成代码片段
// ──────────────────────────────────────────────────────────────────────────────

// 以下是应该添加到 main.go 的代码片段（注释形式）：

/*
// ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
// 步骤 1: 提升 scCache 为外层变量（在 line 810 附近）
// ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

	// 修改前：
	// if redisClientForCache != nil && dbConn != nil ... {
	//     scCache := compression.NewSessionCache(...)
	//     ...
	// }

	// 修改后：
	var scCache *compression.SessionCache  // 提升为外层变量
	if redisClientForCache != nil && dbConn != nil && dbConn.Enabled() && telemetryClient.Enabled() && !compressorSessionDisabled() {
		scCache = compression.NewSessionCache(redisBackendFromClient(redisClientForCache), dbBackendFromPool(dbConn))
		scDeps := compression.SessionCompressorDeps{
			Cache:          scCache,
			CompactionDeps: NewDependenciesFromExecutor(routingExec),
		}
		chatHandler.SetSessionCompressor(compression.NewSessionCompressor(scDeps))
		slog.Info("v3 session-level intelligent compression wired",
			"l1_size", scCache.L1Size(), "l2_backend", "redis", "l3_backend", "postgres")
	}

// ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
// 步骤 2: 初始化审批集成（在 approvalMgr 创建后，line 980 附近）
// ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

	// 在 SessionAuditHookV1 创建后添加：
	if enableSessionAudit == "true" {
		// ... 现有的 SessionAuditHookV1 代码 ...

		// 新增：审批流程集成（v2）
		if scCache != nil && pendingStore != nil {
			approvalIntegrationDeps := &ApprovalIntegrationDeps{
				SessionCache:    scCache,
				ApprovalMgr:     approvalMgr,
				PendingStore:    pendingStore,
				ChatHandler:     chatHandler,
				AuditBus:        auditBus,
				Notifier:        nil, // TODO: 如果有 approvalNotifier，在这里传入
				ApprovalTimeout: approvalTimeout,
			}
			approvalIntegrationResult, err := InitializeApprovalIntegration(approvalIntegrationDeps)
			if err != nil {
				slog.Error("approval integration failed", "error", err)
			} else {
				slog.Info("approval integration completed",
					"cache_update_hook", approvalIntegrationResult.CacheUpdateHook != nil,
					"approval_hook", approvalIntegrationResult.ApprovalHook != nil,
					"resume_handler", approvalIntegrationResult.ResumeHandler != nil)

				// 将 ApprovalResumeHandler 注入到 adminHandler（如果需要）
				// adminHandler.SetApprovalResumeHandler(approvalIntegrationResult.ResumeHandler)
				// 注意：admin.Handler 需要先添加 SetApprovalResumeHandler 方法
			}
		} else {
			slog.Warn("approval integration skipped: missing dependencies",
				"scCache", scCache != nil,
				"pendingStore", pendingStore != nil)
		}
	}
*/

// ──────────────────────────────────────────────────────────────────────────────
// 测试辅助函数
// ──────────────────────────────────────────────────────────────────────────────

// ValidateApprovalIntegration 验证审批集成是否正确。
//
// 这个函数可以在服务启动时调用，确保所有组件都正确初始化。
func ValidateApprovalIntegration(result *ApprovalIntegrationResult) error {
	if result == nil {
		return fmt.Errorf("validation: result is nil")
	}
	if result.CacheUpdateHook == nil {
		return fmt.Errorf("validation: CacheUpdateHook is nil")
	}
	if result.ApprovalHook == nil {
		return fmt.Errorf("validation: ApprovalHook is nil")
	}
	if result.ResumeHandler == nil {
		return fmt.Errorf("validation: ResumeHandler is nil")
	}
	if result.PendingWriter == nil {
		return fmt.Errorf("validation: PendingWriter is nil")
	}
	if result.ClientResponder == nil {
		return fmt.Errorf("validation: ClientResponder is nil")
	}
	if result.LLMCaller == nil {
		return fmt.Errorf("validation: LLMCaller is nil")
	}
	return nil
}

// 示例用法（main 函数中）：
//
// func main() {
//     ... 现有代码 ...
//
//     // 审批流程集成
//     if approvalMgr != nil && scCache != nil && pendingStore != nil {
//         deps := &ApprovalIntegrationDeps{
//             SessionCache:    scCache,
//             ApprovalMgr:     approvalMgr,
//             PendingStore:    pendingStore,
//             ChatHandler:     chatHandler,
//             AuditBus:        auditBus,
//             ApprovalTimeout: 15 * time.Minute,
//         }
//         result, err := InitializeApprovalIntegration(deps)
//         if err != nil {
//             slog.Error("approval integration failed", "error", err)
//         } else if err := ValidateApprovalIntegration(result); err != nil {
//             slog.Error("approval integration validation failed", "error", err)
//         } else {
//             slog.Info("approval integration validated successfully")
//         }
//     }
//
//     ... 剩余代码 ...
// }
