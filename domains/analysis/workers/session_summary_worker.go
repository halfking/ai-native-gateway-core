// Package workers — SessionSummaryWorker (PR-V4-11)。
//
// 订阅 EventSessionClosed 事件，触发会话摘要生成。
// 与 IntentWorker 的区别：
//   - 没有 in-memory 累计状态（无需 flusher）
//   - 调用 LLM 生成摘要 → 直接写 DB（由 Summarizer 内部完成）
//   - 一次事件 → 一次 LLM 调用 → 一次 DB 写
package workers

import (
	"context"
	"log/slog"
	"sync/atomic"

	"github.com/kaixuan/llm-gateway-go/domain/analysis" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
)

// SessionSummarizer 是 Summarizer.GenerateSummary 的最小接口。
// 由 domains/sessionsummary.Summarizer 天然实现；测试可注入桩。
type SessionSummarizer interface {
	GenerateSummary(ctx context.Context, tenantID, sessionKey string) (any, error)
}

// SessionSummaryWorker 订阅 session.closed，异步触发摘要生成。
type SessionSummaryWorker struct {
	logger     *slog.Logger
	summarizer SessionSummarizer
	// closeHooks 在总结生成后顺序执行（如优化建议检测）。
	// 任一失败仅记日志，不影响后续。
	closeHooks []SessionCloseHook

	processed atomic.Int64
	failed    atomic.Int64
}

// SessionCloseHook 在会话关闭、总结生成后执行的扩展钩子。
// 用于挂载 OptimizationAdviser 等重任务。
type SessionCloseHook interface {
	OnSessionClosed(ctx context.Context, tenantID, gwSessionID string) error
}

// NewSessionSummaryWorker 构造 worker。summarizer 为 nil 时 Handle 直接跳过。
func NewSessionSummaryWorker(summarizer SessionSummarizer, logger *slog.Logger) *SessionSummaryWorker {
	if logger == nil {
		logger = slog.Default()
	}
	return &SessionSummaryWorker{
		logger:     logger,
		summarizer: summarizer,
	}
}

// AddCloseHook 追加一个会话关闭扩展钩子（如 OptimizationAdviser）。
func (w *SessionSummaryWorker) AddCloseHook(h SessionCloseHook) {
	if h != nil {
		w.closeHooks = append(w.closeHooks, h)
	}
}

// Name 实现 analysis.Worker。
func (w *SessionSummaryWorker) Name() string { return "session_summary_worker" }

// SubscribedTypes 只订阅 session.closed。
func (w *SessionSummaryWorker) SubscribedTypes() []analysis.EventType {
	return []analysis.EventType{analysis.EventSessionClosed}
}

// Handle 提取 session_key 调用 summarizer。
//
// 容错策略：
//   - summarizer 为 nil：返回 nil（worker 仅是占位）
//   - tenant_id 或 session_key 缺失：返回 nil（无法处理）
//   - GenerateSummary 失败：返回错误，由事件总线记录 attempts 并重试
func (w *SessionSummaryWorker) Handle(ctx context.Context, evt analysis.AnalysisEvent) error {
	if evt.Type != analysis.EventSessionClosed {
		return nil
	}
	if w.summarizer == nil {
		return nil
	}
	sessionKey := extractSessionKey(evt)
	if sessionKey == "" || evt.TenantID == "" {
		w.logger.Debug("session_summary_worker: missing tenant or session_key, skip",
			"event_id", evt.EventID, "tenant_id", evt.TenantID)
		return nil
	}
	w.processed.Add(1)
	_, err := w.summarizer.GenerateSummary(ctx, evt.TenantID, sessionKey)
	if err != nil {
		w.failed.Add(1)
		w.logger.Warn("session_summary_worker: GenerateSummary failed",
			"event_id", evt.EventID,
			"tenant_id", evt.TenantID,
			"session_key", sessionKey,
			"error", err)
		return err
	}
	w.logger.Info("session_summary_worker: summary generated",
		"event_id", evt.EventID,
		"tenant_id", evt.TenantID,
		"session_key", sessionKey,
	)

	// 执行会话关闭扩展钩子（优化建议检测等）。
	// 任一失败仅记日志，不影响主流程。
	for _, h := range w.closeHooks {
		if err := h.OnSessionClosed(ctx, evt.TenantID, sessionKey); err != nil {
			w.logger.Warn("session_summary_worker: close hook failed",
				"event_id", evt.EventID,
				"tenant_id", evt.TenantID,
				"session_key", sessionKey,
				"error", err)
		}
	}
	return nil
}

// Stats 返回 (processed, failed) 计数。
func (w *SessionSummaryWorker) Stats() (int64, int64) {
	return w.processed.Load(), w.failed.Load()
}

// extractSessionKey 从事件 payload 中提取 session_key。
func extractSessionKey(evt analysis.AnalysisEvent) string {
	if evt.SessionID != "" {
		return evt.SessionID
	}
	if m, ok := evt.Payload.(map[string]any); ok {
		if s, ok := m["session_key"].(string); ok {
			return s
		}
		if s, ok := m["session_id"].(string); ok {
			return s
		}
	}
	return ""
}
