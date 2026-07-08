// Package sessionaudithook — cache_update_hook.go
//
// CacheUpdateHook mirrors a finished detection result into the
// three-tier SessionCache so subsequent requests for the same
// session can branch on the audit verdict without re-running
// detection.
//
// Why a separate hook (not folded into SessionAuditHook):
//   - SessionAuditHook runs in PreRouting, where the request body is
//     still mutable. Stamping the cache there couples detection with
//     cache-write latency in the hot path.
//   - CacheUpdateHook runs in PostRouting (after LLM response), so the
//     stamp reflects the *final* audit decision (including any
//     PII-strip / optimization step the audit pipeline may have run
//     in the background).
//
// The hook is intentionally idempotent: it only writes the v6 fields
// on top of whatever the cache currently holds, never touching the
// v1-v5 state.
package sessionaudithook

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/kaixuan/llm-gateway-go/domain" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/analysis"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/compression"
	"github.com/kaixuan/llm-gateway-go/domains/pipeline"     //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/sessionaudit" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
)

// CacheUpdateHook 审计完成后更新 SessionState 缓存。
//
// 该 Hook 在 PostRouting 阶段执行（priority 400，路由 + LLM 调用之后），
// 将最新的审计结果（分数、敏感词、审批状态）写入 SessionState 的 v6 字段。
// 失败仅记录日志，不阻断主流程。
type CacheUpdateHook struct {
	sessionCache   *compression.SessionCache
	now            func() time.Time // 可注入测试时钟
	stateProjector *analysis.SessionStateProjector // 可选：把 v6 投影到 session_tags
}

// NewCacheUpdateHook 创建 Hook。sessionCache 可为 nil（Hook 自动禁用）。
func NewCacheUpdateHook(sc *compression.SessionCache) *CacheUpdateHook {
	return &CacheUpdateHook{
		sessionCache: sc,
		now:          time.Now,
	}
}

// SetStateProjector 注入 SessionStateProjector（可选）。设置后，每次审计
// 结果写入 SessionState 后会顺带投影到 session_tags，让安全/合规/审批结论
// 跨模块可读（统一打标层）。nil 表示禁用投影（默认）。
func (h *CacheUpdateHook) SetStateProjector(p *analysis.SessionStateProjector) {
	if h != nil {
		h.stateProjector = p
	}
}

// Name 返回 Hook 名称（pipeline 路由用）。
func (h *CacheUpdateHook) Name() string {
	return "sessionaudit.cache_update"
}

// Priority 返回执行优先级。PostRouting 阶段，越大越靠后。
func (h *CacheUpdateHook) Priority() int {
	return 400
}

// Enabled 当 sessionCache 已配置且审计元数据存在时启用。
func (h *CacheUpdateHook) Enabled(ctx context.Context, env *domain.PipelineRequest) bool {
	if h == nil || h.sessionCache == nil || env == nil {
		return false
	}
	if env.SessionID == "" || env.TenantID == "" {
		return false
	}
	_, ok := env.Metadata["audit_result"].(*sessionaudit.DetectResult)
	return ok
}

// Execute 把检测结果写入 SessionState。如果 SessionState 不存在（全新会话），
// 创建一个仅含 v6 字段的新条目。错误仅记录日志，不阻断主流程。
func (h *CacheUpdateHook) Execute(ctx context.Context, env *domain.PipelineRequest) error {
	if h == nil || h.sessionCache == nil {
		return nil
	}

	result, ok := env.Metadata["audit_result"].(*sessionaudit.DetectResult)
	if !ok || result == nil {
		return nil
	}

	state, _, err := h.sessionCache.GetOrLoad(ctx, env.TenantID, env.SessionID)
	if err != nil {
		slog.Warn("cache_update: load session state failed",
			"session_id", env.SessionID, "tenant_id", env.TenantID, "error", err)
		return nil
	}
	if state == nil {
		// 新会话：创建仅含 v6 字段的空 state。
		state = &compression.SessionState{SchemaVersion: 1}
	}

	// 1. 写入审计元数据
	multiScore := sessionaudit.CalculateMultiDimensionScore(result)
	now := h.now()
	state.MarkAudited(
		now,
		result.Score,
		multiScore.Security,
		len(result.SensitiveWords) > 0,
		extractPIIStripped(env),
		result.Decision == sessionaudit.DecisionNeedApproval,
	)

	// 2. 写入审计阶段执行的优化（如果有）
	if tag, ok := env.Metadata["optimization_applied"].(string); ok && tag != "" {
		state.ApplyOptimization(tag)
	}

	// 3. 写入 SessionState — outbound body 留 nil（L2 不存 body）。
	if setErr := h.sessionCache.Set(ctx, env.TenantID, env.SessionID, state, nil); setErr != nil {
		slog.Warn("cache_update: save session state failed",
			"session_id", env.SessionID, "tenant_id", env.TenantID, "error", setErr)
		return nil
	}

	// 4. 投影 v6 → session_tags（统一打标层）。best-effort，失败不阻断。
	if h.stateProjector != nil {
		proj := analysis.SessionStateProjection{
			GwSessionID:       env.SessionID,
			TenantID:          env.TenantID,
			AuditScore:        state.AuditScore,
			SecurityScore:     state.SecurityScore,
			SensitiveDetected: state.SensitiveDetected,
			PIIStripped:       state.PIIStripped,
			ApprovalStatus:    state.ApprovalStatus,
			OptimizationTag:   state.OptimizationApplied,
		}
		if perr := h.stateProjector.Project(ctx, proj); perr != nil {
			slog.Warn("cache_update: state projection partial failure",
				"session_id", env.SessionID, "error", perr)
		}
	}

	slog.Debug("cache_update: stamped audit state",
		"session_id", env.SessionID,
		"tenant_id", env.TenantID,
		"audit_score", state.AuditScore,
		"security_score", state.SecurityScore,
		"approval_status", state.ApprovalStatus,
	)
	return nil
}

// OnError 实现 pipeline.Hook。审计缓存写失败可降级。
func (h *CacheUpdateHook) OnError(ctx context.Context, env *domain.PipelineRequest, err error) error {
	// 不修改 env.Error：缓存写失败不应掩盖真正的请求错误。
	return nil
}

// UpdateApprovalID 在审批创建后回填 approval_id。
//
// 该方法暴露给 ApprovalHook 调用：ApprovalHook 创建审批记录后拿到
// approvalID，必须把 ID 写回 SessionState 才能让 resume handler 通过
// cache 找到对应的审批行。
//
// 如果 SessionState 不存在则新建一个最小条目。失败仅记录日志。
func (h *CacheUpdateHook) UpdateApprovalID(ctx context.Context, tenantID, sessionID, approvalID string) error {
	if h == nil || h.sessionCache == nil {
		return errors.New("cache_update: hook not configured")
	}
	if tenantID == "" || sessionID == "" {
		return errors.New("cache_update: tenant and session required")
	}

	state, _, err := h.sessionCache.GetOrLoad(ctx, tenantID, sessionID)
	if err != nil {
		return err
	}
	if state == nil {
		state = &compression.SessionState{SchemaVersion: 1}
	}
	state.SetApprovalID(approvalID)
	return h.sessionCache.Set(ctx, tenantID, sessionID, state, nil)
}

// extractPIIStripped 从 metadata 读取 PII 脱敏标记。
// metadata key "pii_stripped" 由 pipeline 中 PII strip step 写入。
func extractPIIStripped(env *domain.PipelineRequest) bool {
	if env == nil || env.Metadata == nil {
		return false
	}
	if v, ok := env.Metadata["pii_stripped"].(bool); ok {
		return v
	}
	return false
}

// 编译期断言
var _ pipeline.Hook = (*CacheUpdateHook)(nil)
