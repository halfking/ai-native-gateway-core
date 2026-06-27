package sessionaudithook

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/kaixuan/llm-gateway-go/domain"
	"github.com/kaixuan/llm-gateway-go/domains/pipeline"
	"github.com/kaixuan/llm-gateway-go/domains/sessionaudit"
	"github.com/kaixuan/llm-gateway-go/eventbus"
)

// SessionAuditHook 会话审计 Hook（PreRouting 阶段）
//
// 职责：
//   - 执行快速检测（≤5ms）
//   - 根据评分决策：Pass/Warn/Block/NeedApproval
//   - 发布审计事件到 EventBus（异步处理）
type SessionAuditHook struct {
	detector *sessionaudit.FastDetector
	eventBus *eventbus.MemoryBus
	enabled  bool
}

// NewSessionAuditHook 创建 Hook
func NewSessionAuditHook(detector *sessionaudit.FastDetector, bus *eventbus.MemoryBus) *SessionAuditHook {
	return &SessionAuditHook{
		detector: detector,
		eventBus: bus,
		enabled:  true, // 默认启用，可从配置加载
	}
}

func (h *SessionAuditHook) Name() string {
	return "session.audit"
}

func (h *SessionAuditHook) Priority() int {
	return 100 // 在认证后（50）、路由前（200）
}

func (h *SessionAuditHook) Enabled(ctx context.Context, env *domain.PipelineRequest) bool {
	return h.enabled && env != nil
}

func (h *SessionAuditHook) Execute(ctx context.Context, env *domain.PipelineRequest) error {
	// 1. 提取用户内容
	content, err := extractUserContent(env)
	if err != nil || content == "" {
		// 无法提取内容不阻断
		return nil
	}

	// 2. 快速检测（同步，≤5ms）
	result, err := h.detector.Detect(ctx, content)
	if err != nil {
		// 检测器失败降级，不阻断主流程
		slog.Warn("detector failed, degrading", "error", err, "session_id", env.SessionID)
		return nil
	}

	// 3. 写入元数据（供后续 Hook 使用）
	if env.Metadata == nil {
		env.Metadata = make(map[string]any)
	}
	env.Metadata["audit_result"] = result
	env.Metadata["audit_checked_at"] = time.Now()

	// 4. 发布事件（异步处理）
	event := &sessionaudit.SessionAuditEvent{
		SessionID:    env.SessionID,
		TenantID:     env.TenantID,
		Content:      content,
		DetectResult: result,
		ClientInfo: sessionaudit.ClientInfo{
			IP:        getClientIP(env),
			UserAgent: getUserAgent(env),
			Model:     getClientModel(env),
		},
	}

	if err := h.eventBus.Publish(event); err != nil {
		// 发布失败不阻断
		slog.Warn("publish audit event failed", "error", err)
	}

	// 5. 如果是 Block 级别，直接阻断
	if result.Decision == sessionaudit.DecisionBlock {
		env.StatusCode = 403
		// 如果有 http.ResponseWriter 可用，直接写响应
		if env.Envelope != nil && env.Envelope.Transport != nil && env.Envelope.Transport.W != nil {
			w := env.Envelope.Transport.W
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(403)
			_, _ = fmt.Fprintf(w, `{
				"error": {
					"message": "Request blocked by security policy: %s",
					"type": "security_violation",
					"code": "blocked"
				}
			}`, result.Reason)
		}
		return fmt.Errorf("request blocked: %s", result.Reason)
	}

	// 6. Warn 级别：记录日志，继续执行
	if result.Decision == sessionaudit.DecisionWarn {
		slog.Warn("security warning detected",
			"session_id", env.SessionID,
			"score", result.Score,
			"reason", result.Reason,
			"threats", len(result.Threats))
	}

	return nil
}

func (h *SessionAuditHook) OnError(ctx context.Context, env *domain.PipelineRequest, err error) error {
	// 审计失败可降级，返回 nil 不影响主流程
	return nil
}

// 编译期断言
var _ pipeline.Hook = (*SessionAuditHook)(nil)
