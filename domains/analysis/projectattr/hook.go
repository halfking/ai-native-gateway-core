package projectattr

import (
	"context"
	"log/slog"
	"sync/atomic"
)

// CloseHook 在会话关闭时执行项目归属推断。
//
// 实现 workers.SessionCloseHook，挂到 SessionSummaryWorker 上（与
// OptimizationCloseHook 同一挂载点）。会话级触发是这个设计的成本关键：
// 一次会话只推断一次，而不是每次请求都推断。
//
// 全链路 best-effort：任何一步失败只记日志，绝不影响会话关闭主流程，也
// 不向上返回错误触发事件总线重试——归集是辅助分析，不值得为它重放事件。
type CloseHook struct {
	store      Store
	attributor *Attributor
	logger     *slog.Logger

	// 可观测计数：便于运维评估规则层覆盖率，进而判断 LLM 层是否值得开。
	skippedAuthoritative atomic.Int64
	skippedExisting      atomic.Int64
	attributed           atomic.Int64
	unresolved           atomic.Int64
	failed               atomic.Int64
}

// NewCloseHook 构造 hook。store 或 attributor 为 nil 时 hook 静默禁用。
func NewCloseHook(store Store, attributor *Attributor, logger *slog.Logger) *CloseHook {
	if logger == nil {
		logger = slog.Default()
	}
	return &CloseHook{store: store, attributor: attributor, logger: logger}
}

// OnSessionClosed 实现 workers.SessionCloseHook。
func (h *CloseHook) OnSessionClosed(ctx context.Context, tenantID, gwSessionID string) error {
	if h == nil || h.store == nil || h.attributor == nil {
		return nil
	}
	if tenantID == "" || gwSessionID == "" {
		return nil
	}

	// 闸门一：ACC 认领路径已给出权威 project_id，推断必须让路。
	// 这是整个设计的红线——猜测绝不能覆盖真实值。
	if has, err := h.store.HasAuthoritativeProject(ctx, tenantID, gwSessionID); err == nil && has {
		h.skippedAuthoritative.Add(1)
		return nil
	}

	// 闸门二：已推断过（含已被人工否决的）就不再重复。
	if exists, err := h.store.HasAttribution(ctx, tenantID, gwSessionID); err != nil {
		h.failed.Add(1)
		h.logger.Warn("projectattr: check existing attribution failed",
			"tenant_id", tenantID, "session_id", gwSessionID, "error", err)
		return nil
	} else if exists {
		h.skippedExisting.Add(1)
		return nil
	}

	signals, err := h.store.LoadSignals(ctx, tenantID, gwSessionID)
	if err != nil {
		h.failed.Add(1)
		h.logger.Warn("projectattr: load signals failed",
			"tenant_id", tenantID, "session_id", gwSessionID, "error", err)
		return nil
	}

	res := h.attributor.Attribute(ctx, signals)
	if !res.Found() {
		// 判不出来就留空——这是符合预期的正确结果，不是错误。
		h.unresolved.Add(1)
		h.logger.Debug("projectattr: no project resolved",
			"tenant_id", tenantID, "session_id", gwSessionID)
		return nil
	}

	if err := h.store.SaveAttribution(ctx, tenantID, gwSessionID, res); err != nil {
		h.failed.Add(1)
		h.logger.Warn("projectattr: save attribution failed",
			"tenant_id", tenantID, "session_id", gwSessionID, "error", err)
		return nil
	}

	h.attributed.Add(1)
	h.logger.Info("projectattr: session attributed",
		"tenant_id", tenantID,
		"session_id", gwSessionID,
		"project_ref", res.ProjectRef,
		"method", string(res.Method),
		"confidence", res.Confidence,
		"status", string(res.Status))
	return nil
}

// Stats 返回累计计数，供 admin 面板评估各层命中率。
func (h *CloseHook) Stats() map[string]int64 {
	if h == nil {
		return nil
	}
	return map[string]int64{
		"skipped_authoritative": h.skippedAuthoritative.Load(),
		"skipped_existing":      h.skippedExisting.Load(),
		"attributed":            h.attributed.Load(),
		"unresolved":            h.unresolved.Load(),
		"failed":                h.failed.Load(),
	}
}
