// bg/hosted_task_reconciler.go — 任务托管 reconciler（§3.1 ④⑤/§6.1）。
//
// BaseWorker 单 goroutine tick 编排四件事（SSE 流为按 run 派生的子 goroutine）：
//
//  1. dispatch 轮次：delegated/dispatching → ACC dispatch（同键
//     gw-hosted-<id>-a1 重放，ACC 幂等保证不双发）；失败 → dispatch_degraded
//     事件，停留 dispatching 等下一轮（§3.1 ②）。
//  2. 执行投影：active runs = SSE 订阅（Last-Event-ID 持久游标断线恢复）+
//     定时轮询 GET command 兜底 → 终态判定强制校验 raw.stop_reason（§0-F4
//     pi 假成功），无法判定 → needs_review（不猜测，§3.1 ④）→ CAS 终态 +
//     事件 + result + 回调入队（§3.1 ⑤）。
//  3. deadline reaper：非终态过期 → expired（§4.2）。
//  4. 回调投递：ClaimDueCallbacks → hostedcallback 签名 POST →
//     delivered/退避/DLQ（矩阵 E）。
//
// 取消收尾：handler 已本地终态抢占；本 worker 对有 acc_command_id 的
// cancelled 任务补发 ACC cancel（尽力而为，P0 语义=请求受理，§3.2/§0-F4）。
package bg

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hostedtask"
)

// HostedTaskReconcilerConfig 装配参数。
type HostedTaskReconcilerConfig struct {
	Store              *hostedtask.Store
	ACC                *hostedtask.ACCClient
	Callbacks          hostedtask.CallbackDeps
	Interval           time.Duration // 主循环 tick（默认 5s）
	DispatchRetryAfter time.Duration // dispatch 失败重试间隔（默认 30s）
	StreamLimit        int           // 同时订阅的最大 run 数（默认 50）
	Logger             *slog.Logger
}

// HostedTaskReconciler 投影 ACC 执行真相到 hosted_tasks（网关不重试 dispatch
// 语义、不持租约 —— §1.3 不变量 2）。
type HostedTaskReconciler struct {
	*BaseWorker
	store              *hostedtask.Store
	acc                *hostedtask.ACCClient
	callbacks          hostedtask.CallbackDeps
	interval           time.Duration
	dispatchRetryAfter time.Duration
	streamLimit        int
	logger             *slog.Logger

	mu sync.Mutex
	// streams 值用 *streamHandle 指针而非裸 CancelFunc：goroutine 退出时的
	// defer 清理必须做指针身份比较（R29 审计）。裸值时代的无条件 delete 有
	// 竞争——旧订阅 goroutine 退出晚于新订阅注册时，会把新订阅的 cancel 项
	// 删掉，同一 runID 随后被再次注册成双订阅（重复 repoll/cursor 写）。
	streams map[string]*streamHandle

	// workerCtx 由 Start() 从 run 入口 ctx 派生；ensureStream 必须用它
	// 而不是 tick() 入口的 passCtx，否则 cancelPass() 每 tick 级联取消所有
	// SSE 长连接，SSE 流永活不过一个 tick —— 实质上把"长连接"降级成"5 秒
    // 重连一次"，状态权威回到 poll，SSE 沦为噪声通道（设计 §3.1 ④ 意图失效）。
	workerCtx context.Context
}

// NewHostedTaskReconciler 构造 reconciler。
func NewHostedTaskReconciler(cfg HostedTaskReconcilerConfig) *HostedTaskReconciler {
	if cfg.Interval <= 0 {
		cfg.Interval = 5 * time.Second
	}
	if cfg.DispatchRetryAfter <= 0 {
		cfg.DispatchRetryAfter = 30 * time.Second
	}
	if cfg.StreamLimit <= 0 {
		cfg.StreamLimit = 50
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	if cfg.Callbacks.Logger == nil {
		cfg.Callbacks.Logger = log
	}
	return &HostedTaskReconciler{
		BaseWorker:         NewBaseWorker("hosted_task_reconciler"),
		store:              cfg.Store,
		acc:                cfg.ACC,
		callbacks:          cfg.Callbacks,
		interval:           cfg.Interval,
		dispatchRetryAfter: cfg.DispatchRetryAfter,
		streamLimit:        cfg.StreamLimit,
		logger:             log,
		streams:            map[string]*streamHandle{},
	}
}

// Start 启动主循环。
func (r *HostedTaskReconciler) Start(ctx context.Context) {
	r.BaseWorker.Start(ctx, r.run)
}

func (r *HostedTaskReconciler) run(ctx context.Context) {
	defer r.BaseWorker.NotifyStopped()
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	// 把 run 入口 ctx 作为 workerCtx 缓存，确保 ensureStream 从这里派生
	// 流 ctx（独立于 tick() 的 passCtx），防止 R36 引入的 SSE ctx 级联取消
	// 回归（R62 审计 2026-09-17 后）：passCtx cancelPass() 每 tick 级联杀
	// 掉所有 SSE 长连接，SSE 流永活不过一个 tick。
	r.workerCtx = ctx

	if !r.acc.Configured() {
		r.logger.Warn("hostedtask reconciler: ACC not configured (LLM_GATEWAY_ACC_BASE_URL/TOKEN); " +
			"dispatches degrade with dispatch_degraded events until configured (matrix A gate missing)")
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.tick(ctx)
		}
	}
}

// hostedTaskPassBudget bounds each reconciler pass (R36 2026-09-17 audit,
// closes R34 遗留#3): dispatch/project passes iterate up to 50 tasks against
// the ACC control plane serially — with ACC hanging on a 30s client timeout
// the old unbounded ctx let one black-holed tick run ~25 minutes and starve
// ClaimExpiredTasks / DeliverDueCallbacks entirely.
const hostedTaskPassBudget = 45 * time.Second

func (r *HostedTaskReconciler) tick(ctx context.Context) {
	now := time.Now().UTC()

	// R36: each pass gets an independent budget so no single pass can starve
	// the reaper/callback passes behind it.
	passCtx, cancelPass := context.WithTimeout(ctx, hostedTaskPassBudget)
	// 1. dispatch 轮次。
	r.dispatchPass(passCtx, now)
	// 2. active runs：SSE 订阅 + 轮询兜底。
	r.projectPass(passCtx, now)
	cancelPass()
	// 3. deadline reaper。
	if n, err := r.store.ClaimExpiredTasks(ctx, now, 50); err != nil {
		r.logger.Error("hostedtask reaper failed", "error", err)
	} else if n > 0 {
		r.logger.Info("hostedtask reaper expired tasks", "count", n)
	}
	// 4. 回调投递（终态已在 SettleTask 入队）。
	if _, err := hostedtask.DeliverDueCallbacks(ctx, r.callbacks, now, 20); err != nil && !errors.Is(err, context.Canceled) {
		r.logger.Error("hostedtask callback delivery pass failed", "error", err)
	}
}

// ─── 1. dispatch 轮次（§3.1 ②）────────────────────────────────────────────

func (r *HostedTaskReconciler) dispatchPass(ctx context.Context, now time.Time) {
	tasks, err := r.store.ListDispatchDue(ctx, now, r.dispatchRetryAfter, 50)
	if err != nil {
		r.logger.Error("hostedtask dispatch scan failed", "error", err)
		return
	}
	for _, task := range tasks {
		select {
		case <-ctx.Done():
			return
		default:
		}
		ok, err := r.store.BeginDispatch(ctx, task.ID)
		if err != nil || !ok {
			continue
		}
		attempt := task.DispatchAttempts + 1
		key := "gw-hosted-" + task.ID + "-a1" // §3.1 ②：同键重放防双发
		if !r.acc.Configured() {
			_ = r.store.RecordDispatchDegraded(ctx, task.ID, attempt, "acc client not configured")
			continue
		}
		payload := buildDispatchPayload(task)
		res, err := r.acc.Dispatch(ctx, hostedtask.DispatchRequest{
			RuntimeID:     r.acc.RuntimeID(),
			TaskID:        "hosted-" + task.ID,
			Payload:       payload,
			CorrelationID: task.ID,
		}, key)
		if err != nil {
			r.logger.Warn("hostedtask dispatch degraded", "task_id", task.ID, "attempt", attempt, "error", err)
			_ = r.store.RecordDispatchDegraded(ctx, task.ID, attempt, err.Error())
			continue
		}
		if err := r.store.RecordDispatchSuccess(ctx, task.ID, res.CommandID, res.RunID, key); err != nil {
			r.logger.Error("hostedtask record dispatch failed", "task_id", task.ID, "error", err)
			continue
		}
		r.logger.Info("hostedtask dispatched", "task_id", task.ID, "command_id", res.CommandID, "run_id", res.RunID)
		// 立即订阅 run 事件流（若 ACC 已返回 run_id）。
		if res.RunID != "" {
			fresh := task
			fresh.AccRunID = res.RunID
			r.ensureStream(ctx, fresh)
		}
	}
}

// buildDispatchPayload 从任务投影构造 payload.kind=pi（§3.1 ②：
// prompt(goal+done_when+context 摘要)、cwd(白名单映射结果)、timeout_ms）。
func buildDispatchPayload(task hostedtask.Task) hostedtask.DispatchPayload {
	p := hostedtask.DispatchPayload{
		Kind: "pi",
		Prompt: hostedtask.BuildPrompt(hostedtask.CreateInput{
			Goal: task.Goal, DoneWhen: task.DoneWhen,
			Context: task.Context, Deadline: task.DeadlineAt,
		}),
	}
	if task.Environment != nil {
		if cwd, ok := task.Environment["cwd"].(string); ok {
			p.Cwd = cwd // 网关白名单映射产物，非客户端裸路径
		}
		if m, ok := task.Environment["model"].(string); ok {
			p.Model = m
		}
		if t, ok := task.Environment["timeout_seconds"].(float64); ok && t > 0 {
			p.TimeoutMs = int64(t * float64(time.Second/time.Millisecond))
		} else if t, ok := task.Environment["timeout_seconds"].(int); ok && t > 0 {
			p.TimeoutMs = int64(t) * int64(time.Second/time.Millisecond)
		}
	}
	return p
}

// ─── 2. 执行投影（SSE + 轮询兜底，§3.1 ④）────────────────────────────────

func (r *HostedTaskReconciler) projectPass(ctx context.Context, now time.Time) {
	tasks, err := r.store.ListActiveRuns(ctx, r.streamLimit)
	if err != nil {
		r.logger.Error("hostedtask active run scan failed", "error", err)
		return
	}
	seen := map[string]bool{}
	for _, task := range tasks {
		seen[task.AccRunID] = true
		// 轮询兜底：每 tick 对 active command 轮询（P0 以 poll 为状态权威）。
		r.pollOne(ctx, task)
		if ctx.Err() != nil {
			return
		}
		r.ensureStream(ctx, task)
	}
	// 清理已不在 active 集的流（cancel 使订阅 goroutine 退出）。
	r.mu.Lock()
	var stale []*streamHandle
	for runID, h := range r.streams {
		if !seen[runID] {
			stale = append(stale, h)
			delete(r.streams, runID)
		}
	}
	r.mu.Unlock()
	for _, h := range stale {
		h.cancel()
	}
}

// pollOne 轮询单个 command 并投影（状态权威路径）。
func (r *HostedTaskReconciler) pollOne(ctx context.Context, task hostedtask.Task) {
	if !r.acc.Configured() || task.AccCommandID == "" {
		return
	}
	cmd, err := r.acc.GetCommand(ctx, task.AccCommandID)
	if err != nil {
		// 轮询失败不打断投影：下一 tick 重试（deadline reaper 兜底）。
		r.logger.Debug("hostedtask poll failed", "task_id", task.ID, "error", err)
		return
	}
	r.applyCommand(ctx, task, cmd)
}

// applyCommand 把 ACC command 状态投影到 hosted_tasks（终态判定 + CAS）。
func (r *HostedTaskReconciler) applyCommand(ctx context.Context, task hostedtask.Task, cmd hostedtask.ACCCommand) {
	if task.Status.Terminal() || !cmd.Done() {
		// 非终态：ACC running/queued 与本地一致，无需写（progress 事件由
		// SSE 事件触发，避免每 tick 刷事件）。
		return
	}
	in, ok := deriveSettlement(cmd)
	if !ok {
		return
	}
	r.settle(ctx, task.ID, in)
}

// deriveSettlement 是终态判定的纯函数权威（§3.1 ④/§0-F4）：
//   - pi 假成功（stop_reason=error）→ failed；
//   - completed 但无 stop_reason 可校验 → needs_review（unknown_outcome，
//     不猜测；API 暴露 failed(unknown)）；
//   - ACC cancelled/expired → 对应终态事件。
func deriveSettlement(cmd hostedtask.ACCCommand) (hostedtask.SettleInput, bool) {
	if !cmd.Done() {
		// 防御：非终态不得结算（applyCommand 已守卫，这里兜底）。
		return hostedtask.SettleInput{}, false
	}
	base := map[string]any{
		"acc_command_id": cmd.CommandID,
		"stop_reason":    cmd.StopReason,
	}
	switch normalizeTerminal(cmd) {
	case "completed":
		result := buildResult(cmd)
		if cmd.StopIsError() {
			base["outcome"] = "failed"
			base["reason"] = "pi stop_reason=error (facade success masked failure)"
			return hostedtask.SettleInput{
				To: hostedtask.StatusFailed, EventType: hostedtask.EventFailed,
				Result: result, Payload: base,
			}, true
		}
		if cmd.StopReason == "" {
			base["outcome"] = "unknown_outcome"
			base["reason"] = "command completed without verifiable raw.stop_reason; manual reconciliation required"
			return hostedtask.SettleInput{
				To: hostedtask.StatusNeedsReview, EventType: hostedtask.EventFailed,
				Result: result, Payload: base,
			}, true
		}
		base["outcome"] = "completed"
		return hostedtask.SettleInput{
			To: hostedtask.StatusCompleted, EventType: hostedtask.EventCompleted,
			Result: result, Payload: base,
		}, true
	case "failed":
		base["outcome"] = "failed"
		return hostedtask.SettleInput{
			To: hostedtask.StatusFailed, EventType: hostedtask.EventFailed,
			Result: buildResult(cmd), Payload: base,
		}, true
	case "cancelled":
		base["reason"] = "acc_cancelled"
		return hostedtask.SettleInput{
			To: hostedtask.StatusCancelled, EventType: hostedtask.EventCancelled,
			Result: buildResult(cmd), Payload: base,
		}, true
	case "expired":
		base["reason"] = "acc_terminal_" + cmd.Status
		return hostedtask.SettleInput{
			To: hostedtask.StatusExpired, EventType: hostedtask.EventExpired,
			Payload: base,
		}, true
	}
	return hostedtask.SettleInput{}, false
}

// normalizeTerminal 把 ACC 终态归一为 completed/failed/cancelled/expired。
// 非 Done 状态不会走到这里（applyCommand 已守卫）。
func normalizeTerminal(cmd hostedtask.ACCCommand) string {
	switch cmd.Status {
	case "completed", "complete", "succeeded", "success":
		return "completed"
	case "failed", "error":
		return "failed"
	case "cancelled", "canceled":
		return "cancelled"
	default: // expired / timed_out / timeout
		return "expired"
	}
}

// buildResult 组装 PG 权威 result（§4.1 result 契约字段 + 原始结果保留）。
func buildResult(cmd hostedtask.ACCCommand) map[string]any {
	res := map[string]any{}
	for k, v := range cmd.ResultRaw {
		res[k] = v
	}
	res["stop_reason"] = cmd.StopReason
	if v, ok := res["summary"]; !ok || v == "" {
		if o, ok := res["output"].(string); ok {
			res["summary"] = truncateUTF8(o, 512)
		}
	}
	if v, ok := res["pi_session_ref"]; !ok || v == "" {
		if s, ok := cmd.ResultRaw["session_id"].(string); ok && s != "" {
			res["pi_session_ref"] = s
		} else if s, ok := cmd.ResultRaw["session"].(string); ok && s != "" {
			res["pi_session_ref"] = s
		}
	}
	if raw, err := json.Marshal(cmd.ResultRaw); err == nil {
		sum := sha256.Sum256(raw)
		res["content_hash"] = "sha256:" + hex.EncodeToString(sum[:])
	}
	return res
}

// truncateUTF8 按 rune 截断（bg 包已有字节版 truncate，这里避免同名冲突）。
func truncateUTF8(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}

// settle 执行终态落定并记录（CAS 失败/竞争只是不再投影——终态 sticky）。
func (r *HostedTaskReconciler) settle(ctx context.Context, taskID string, in hostedtask.SettleInput) {
	settled, seq, err := r.store.SettleTask(ctx, taskID, in)
	if err != nil {
		r.logger.Error("hostedtask settle failed", "task_id", taskID, "to", string(in.To), "error", err)
		return
	}
	if settled {
		r.logger.Info("hostedtask settled", "task_id", taskID, "to", string(in.To), "event_seq", seq)
	}
}

// ─── SSE 订阅（断线 after 恢复；§3.1 ④/G 组）─────────────────────────────

var errStopStream = errors.New("hostedtask: task settled; stop stream")

// streamHandle 是一条 SSE 订阅的登记句柄，指针身份用于 defer 清理时
// 区分"自己"与"后继订阅"（见 streams 字段注释）。
type streamHandle struct {
	cancel context.CancelFunc
}

// ensureStream 为 runID 维护至多一条订阅 goroutine（断线退避重连；run 退出
// active 集时由 projectPass 调 cancel 回收）。
//
// 入参 ctx 在 P0 历史里被用作 streamCtx 的 parent，但 R36 给 tick() 加了
// passCtx 预算后，传入 ctx 是 passCtx，cancelPass() 每 tick 级联取消所有
// SSE 流——SSE 长连接被降级为"每 tick 重连一次"。本方法忽略入参 ctx，
// 强制使用 workerCtx（run() 入口 ctx），确保 SSE 流 lifetime 等于 worker
// lifetime。
func (r *HostedTaskReconciler) ensureStream(_ context.Context, task hostedtask.Task) {
	if !r.acc.Configured() || task.AccRunID == "" {
		return
	}
	parentCtx := r.workerCtx
	if parentCtx == nil {
		// 安全退化：run() 尚未启动（Start 之前/异常路径）。ACC 没配置时
		// 上面已 return；这里仍兜底，避免 nil deref。
		parentCtx = context.Background()
	}
	r.mu.Lock()
	if _, active := r.streams[task.AccRunID]; active {
		r.mu.Unlock()
		return
	}
	streamCtx, cancel := context.WithCancel(parentCtx)
	handle := &streamHandle{cancel: cancel}
	r.streams[task.AccRunID] = handle
	r.mu.Unlock()

	go func(runID, taskID, cursor string) {
		defer func() {
			r.mu.Lock()
			// 只清理仍属于自己的登记项：若已有新订阅（同 runID 重入 active
			// 集）注册，绝不能替它删项——否则第三次 ensureStream 会再开一条
			// 订阅 goroutine，同 runID 双订阅并发（R29 审计 #G）。
			if cur, ok := r.streams[runID]; ok && cur == handle {
				delete(r.streams, runID)
			}
			r.mu.Unlock()
		}()
		backoff := time.Second
		for streamCtx.Err() == nil {
			err := r.acc.StreamEvents(streamCtx, runID, cursor, func(ev hostedtask.ACCEvent) error {
				if ev.ID != "" {
					cursor = ev.ID
					if err := r.store.SaveSSECursor(streamCtx, taskID, ev.ID); err != nil {
						r.logger.Warn("hostedtask save sse cursor failed", "task_id", taskID, "error", err)
					}
				}
				// P0 不同 ACC 事件分类学：SSE 作为“醒来就查”的触发器 +
				// 明确终态事件的加速器；状态权威始终是 poll（§3.1 ④ 兜底语义）。
				if task.TenantID != "" {
					r.repoll(streamCtx, task.TenantID, taskID)
				}
				if ev.CmdTerminalish() {
					return errStopStream
				}
				return nil
			})
			if streamCtx.Err() != nil {
				return
			}
			if errors.Is(err, errStopStream) {
				return
			}
			if err != nil {
				r.logger.Warn("hostedtask sse stream exited; will reconnect with cursor",
					"run_id", runID, "cursor", cursor, "error", err)
			}
			select {
			case <-streamCtx.Done():
				return
			case <-time.After(backoff):
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			// 重连前刷新任务（可能已终态/换 run）。
			fresh, err := r.store.GetTask(streamCtx, task.TenantID, taskID)
			if err != nil || fresh.Status.Terminal() {
				return
			}
			task = *fresh
		}
	}(task.AccRunID, task.ID, task.SSECursor)
}

// repoll 立即轮询一次（SSE 触发器语义）。
func (r *HostedTaskReconciler) repoll(ctx context.Context, tenantID, taskID string) {
	task, err := r.store.GetTask(ctx, tenantID, taskID)
	if err != nil || task.Status.Terminal() {
		return
	}
	r.pollOne(ctx, *task)
}

// ─── 取消收尾（§3.2：handler 本地终态后补发 ACC cancel）───────────────────

// CancelOnACC 对已本地取消的任务尽力补发 ACC cancel（requested 语义）。
func (r *HostedTaskReconciler) CancelOnACC(ctx context.Context, commandID string) {
	if !r.acc.Configured() || commandID == "" {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := r.acc.Cancel(cctx, commandID); err != nil {
		r.logger.Warn("hostedtask acc cancel failed (task remains cancelled locally; P0 semantics=requested)",
			"command_id", commandID, "error", err)
	}
}
