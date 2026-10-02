// bg/hosted_task_reconciler.go — 任务托管 reconciler（§3.1 ④⑤/§6.1）。
//
// BaseWorker 单 goroutine tick 编排四件事：
//
//  1. dispatch 轮次：delegated/dispatching → ACC dispatch（同键
//     gw-hosted-<id>-a1 重放，ACC 幂等保证不双发）；失败 → dispatch_degraded
//     事件，停留 dispatching 等下一轮（§3.1 ②）。
//  2. 执行投影：active runs = 每 tick 轮询 GET command（状态权威；R65 仲裁
//     移除 SSE 订阅——见 docs/design §10 D7）→ 终态判定强制校验
//     raw.stop_reason（§0-F4 pi 假成功），无法判定 → needs_review（不猜测，
//     §3.1 ④）→ CAS 终态 + 事件 + result + 回调入队（§3.1 ⑤）。
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
	StreamLimit        int           // 单 tick 投影扫描的 active run 上限（默认 50）
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
	// 2. active runs：轮询为状态权威（R65 已删 SSE 订阅，设计文档 §10 D7——
	// R59 审计 S2-F7 注释勘误）。
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

// ─── 2. 执行投影（轮询状态权威，§3.1 ④）────────────────────────────────────

func (r *HostedTaskReconciler) projectPass(ctx context.Context, now time.Time) {
	tasks, err := r.store.ListActiveRuns(ctx, r.streamLimit)
	if err != nil {
		r.logger.Error("hostedtask active run scan failed", "error", err)
		return
	}
	for _, task := range tasks {
		// 每 tick 对 active command 轮询（状态权威；终态最迟下一个 tick 被投影）。
		r.pollOne(ctx, task)
		if ctx.Err() != nil {
			return
		}
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
		// 非终态：ACC running/queued 与本地一致，无需写（P0 不产 progress
		// 事件，避免每 tick 刷事件；进度观测走事件时间线之外的轮询读）。
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
