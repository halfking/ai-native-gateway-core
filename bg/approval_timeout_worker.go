// bg/approval_timeout_worker.go — 2026-06-27
//
// 定期把超时的 pending 审批标记为 timeout 状态。
// 模式与 EnvelopeCleaner 完全一致：Start/Stop + 内部 ticker + 单 goroutine。
//
// 设计：
//   - 每 60s 扫一次（timeout 默认 15min → 60s 周期足够）
//   - 调用 sessionaudit.ApprovalManager.MarkTimeout
//   - MarkTimeout 内显式事务 + setSuperAdminGUC (R41 P1-6) —
//     approval_queue policy 的 role 分支即满足
//   - Stop() 等待 goroutine 退出
//
// 接入点：cmd/gateway/main.go 在 init bg services 时构造 + Start。

package bg

import (
	"context"
	"log/slog"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/sessionaudit" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
)

// ApprovalTimeoutWorker 把超时审批自动标记为 timeout。
type ApprovalTimeoutWorker struct {
	mgr *sessionaudit.ApprovalManager
	// actionResolver 每轮 sweep 前重读超时动作（R50 审计 P2）：spec 声明
	// HotReload:true，R49 的装配期一次性读取违背该契约——admin UI 把
	// auto_approve 改回 deny（收紧）后清扫仍按旧值放行直至重启。
	actionResolver func() sessionaudit.TimeoutAction
	cancel         context.CancelFunc
	done           chan struct{}
}

// NewApprovalTimeoutWorker 构造 worker。
func NewApprovalTimeoutWorker(mgr *sessionaudit.ApprovalManager) *ApprovalTimeoutWorker {
	return &ApprovalTimeoutWorker{mgr: mgr, done: make(chan struct{})}
}

// WithActionResolver 注入超时动作热解析器（每轮 sweep 调用一次）。
// nil 表示维持装配期动作不变。
func (w *ApprovalTimeoutWorker) WithActionResolver(fn func() sessionaudit.TimeoutAction) *ApprovalTimeoutWorker {
	w.actionResolver = fn
	return w
}

// Start 启动后台 goroutine。Stop 之前不能重复 Start。
func (w *ApprovalTimeoutWorker) Start(ctx context.Context) {
	ctx, w.cancel = context.WithCancel(ctx)
	go w.run(ctx)
	slog.Info("approval timeout worker started", "interval", "60s")
}

// Stop 取消并等待 goroutine 退出。
func (w *ApprovalTimeoutWorker) Stop() {
	if w.cancel != nil {
		w.cancel()
	}
	<-w.done
}

func (w *ApprovalTimeoutWorker) run(ctx context.Context) {
	defer close(w.done)

	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	// 启动后立即跑一次，避免冷启期间积压超时
	w.sweep(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.sweep(ctx)
		}
	}
}

func (w *ApprovalTimeoutWorker) sweep(ctx context.Context) {
	timeoutCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	if w.actionResolver != nil {
		w.mgr = w.mgr.WithTimeoutAction(w.actionResolver())
	}

	n, err := w.mgr.MarkTimeout(timeoutCtx)
	if err != nil {
		// R50 审计 P3：错误分支带上已推进计数——大积压首扫被预算截断时
		// 实际已提交 N 批，只 log failure 会让 ops 误判零进展。
		slog.Warn("approval timeout sweep failed", "error", err, "marked_before_error", n)
		return
	}
	if n > 0 {
		slog.Info("approval timeout sweep marked", "count", n)
	}
}
