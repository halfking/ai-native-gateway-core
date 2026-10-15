package bg

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

type ProbeQueueWorkerConfig struct {
	Queue      *ProbeQueue
	Executor   *ActiveProbeExecutor
	Emitter    *ActiveProbeEmitter
	ResultSink IntegrityProbeResultSink
	// ProbeService (2026-08-13, 需求 6) owns execution of node_probe tasks
	// (two-round direct+gateway, side-effects, audit). When nil, node_probe
	// tasks fall back to the executor's direct-only RunCommand.
	ProbeService *ProbeService
	Scope        ProbeScope
	BatchSize    int
	Workers      int
	Lease        time.Duration
	PollInterval time.Duration

	// IdlePollCap 是**空闲退避**的封顶间隔（2026-10-13，runbook §10.110.15）。
	// ≤0 时取默认值 ProbeQueueIdlePollCapDefault。
	// 只在「这一轮一件都没取到」时生效；一取到任务立刻回到 PollInterval，
	// 所以**队列有活时的行为与改前逐字相同**。
	IdlePollCap time.Duration
}

// ProbeQueueIdlePollCapDefault 是空闲退避的默认封顶（30s）。
//
// 取 30s 的理由：探针是后台健康检查，**没有秒级时效要求**；
// 而生产实测 99.33% 的轮询都是空转（410 万次取件只回 27,443 行）。
// ⇒ 空转侧从 250ms 退到 30s，可省掉这部分 ~5.3% 的数据库时间。
const ProbeQueueIdlePollCapDefault = 30 * time.Second

// ProbeQueueWorker consumes durable tasks and delegates every probe to the
// same ActiveProbeExecutor used by the legacy active probe path.
type ProbeQueueWorker struct {
	cfg    ProbeQueueWorkerConfig
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// reviveMu/reviveAt throttle ReviveExpiredReady (every 30s, not every
	// 250ms poll) — it is a maintenance sweep, not a hot-path step.
	reviveMu sync.Mutex
	reviveAt time.Time

	// onQuotaRecovered is the dispatcher-facing notification fired from
	// processTask's success branch (the legacy executor path AND the
	// ProbeService path) once a credential's quota / availability state
	// has been flipped back to healthy. Wired from main.go via
	// SetOnQuotaRecovered; nil → processTask stays silent (the routing
	// layer falls back to candCache TTL).
	//
	// 2026-08-26 quota-recovery-notify fix: closes the "DB says ready but
	// cache still excludes credential" gap on the fast_probe path
	// (ProbeQueueWorker + ProbeService + NodeProbeWorker.Submit).
	onQuotaRecovered func(credID int, source string)

	// completeFn (audit 2026-09-11) is the test seam over complete(); nil
	// falls through to the real ProbeQueue.Complete path. Exists so tests
	// can assert that a necessity-skipped (ErrProbeNotNecessary) task is
	// NOT completed against its already-deleted queue row.
	completeFn func(ctx context.Context, task ProbeQueueTask, result ProbeQueueResult)
}

// SetProbeService injects the node-probe execution owner after construction
// (the worker is built before NodeProbeWorker in main.go, so ProbeService —
// which wraps NodeProbeWorker — is wired in a second step). Until set,
// node_probe tasks fall back to the executor's direct-only RunCommand.
func (w *ProbeQueueWorker) SetProbeService(ps *ProbeService) {
	if w != nil {
		w.cfg.ProbeService = ps
	}
}

// SetOnQuotaRecovered wires the dispatcher-facing notification fired from
// processTask's success branch. The (credID, source) signature lets the
// integrator route the label + invalidator from a single closure. Safe to
// call multiple times; the latest non-nil setter wins. nil → processTask
// stays silent (the routing layer falls back to candCache TTL).
//
// 2026-08-26 quota-recovery-notify fix: without this hook the durable
// probe queue's success path would complete the queue row but the routing
// layer's candidate cache would still exclude the credential until TTL
// elapses, so the first chat request after a recharge still picks a
// fallback node.
func (w *ProbeQueueWorker) SetOnQuotaRecovered(fn func(credID int, source string)) {
	if w == nil || fn == nil {
		return
	}
	w.onQuotaRecovered = fn
}

func NewProbeQueueWorker(cfg ProbeQueueWorkerConfig) *ProbeQueueWorker {
	if cfg.BatchSize <= 0 {
		// A worker claims one task at a time. Claiming a batch then executing it
		// serially lets later tasks lose their leases before they start.
		cfg.BatchSize = 1
	}
	if cfg.Workers <= 0 {
		cfg.Workers = 1
	}
	if cfg.Lease <= 0 {
		// 2026-08-18 Agent B: bumped 30s → ProbeQueueLeaseDefault (5m) so a
		// two-round probe + side effects run is not reclaimed by
		// RequeueExpiredLeases mid-flight. The lease heartbeat inside
		// ProbeService.Run keeps the window refreshed while side effects run.
		cfg.Lease = ProbeQueueLeaseDefault
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 250 * time.Millisecond
	}
	return &ProbeQueueWorker{cfg: cfg}
}

func (w *ProbeQueueWorker) Start(ctx context.Context) {
	if w == nil || w.cfg.Queue == nil || w.cfg.Executor == nil {
		return
	}
	ctx, w.cancel = context.WithCancel(ctx)
	w.wg.Add(w.cfg.Workers)
	for i := 0; i < w.cfg.Workers; i++ {
		Go("probe_queue_worker.run", func() { w.run(ctx) })
	}
}

func (w *ProbeQueueWorker) Stop() {
	if w == nil {
		return
	}
	if w.cancel != nil {
		w.cancel()
	}
	w.wg.Wait()
}

func (w *ProbeQueueWorker) run(ctx context.Context) {
	defer w.wg.Done()

	// 2026-10-13（runbook §10.110.15）：空闲退避。
	//
	// 改前是固定 250ms ticker，**队列空转时也照跑**。生产实测（61.7 小时窗口）：
	//   Claim 4,106,933 次只取到 27,443 行 ⇒ **99.33% 的轮询是空转**
	//   RequeueExpiredLeases 4,106,941 次只改 39 行 ⇒ 99.999% 空转
	// 两项合计 201.5 分钟 = llm_gateway 全库数据库时间的 **5.3%**。
	//
	// 改法：空转时把间隔翻倍（封顶 IdlePollCap），**一取到任务立刻复位**。
	// ⇒ 队列有活时仍是 250ms，延迟不变；只有「本来就没活」的那 99.33% 变慢。
	// 这是队列消费者的标准形态，不是「降频」，所以不改任何调度语义。
	base := w.cfg.PollInterval
	if base <= 0 {
		base = 250 * time.Millisecond
	}
	maxIdle := w.cfg.IdlePollCap
	if maxIdle <= 0 {
		// 未配置 ⇒ 取默认封顶。
		//
		// ⚠️ 2026-10-13 12h 审计轮（P1）修正：此前这里写的是
		//   if maxIdle < base { maxIdle = base }
		// 而**生产装配点从未给 IdlePollCap 赋值**（cmd/gateway/main.go 的
		// NewProbeQueueWorker 只设了 PollInterval: 250ms），于是 maxIdle=0
		// → 被抬成 base(250ms) → nextIdlePoll 的封顶恒等于 base
		// ⇒ 退避幅度恒为 0，行为与改前逐字相同。
		// 字段注释承诺的「≤0 时取 ProbeQueueIdlePollCapDefault」在代码里
		// **根本不存在** —— 这正是该缺陷能通过评审的原因，也是它藏住的
		// 原因：注释说有，代码没做。
		maxIdle = ProbeQueueIdlePollCapDefault
	}
	if maxIdle < base {
		// 配得比 base 还小 ⇒ 退避无处可退，退化成改前的固定间隔，
		// 而不是产生一个比 base 更慢的间隔。
		maxIdle = base
	}
	wait := base
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		n, err := w.processBatch(ctx)
		if err != nil && ctx.Err() == nil {
			slog.Warn("probe queue worker batch failed", "error", err)
		}
		wait = nextIdlePoll(wait, base, maxIdle, n)
		timer.Reset(wait)
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
	}
}

// nextIdlePoll 是空闲退避的**决策规则**，单独抽出来是为了能被直接判据覆盖：
// run() 里的循环结构（定时器用法、什么时候调用它）由
// TestProbeQueueIdlePollBackoffIsWiredIntoRun 钉住，决策本身由决策表钉住。
//
// current：本轮实际等待的间隔；base：有活时的间隔；cap：封顶；
// claimed：上一轮 processBatch 取到的任务数。
func nextIdlePoll(current, base, cap time.Duration, claimed int) time.Duration {
	if claimed > 0 {
		// 一取到活立刻复位 —— 这是「有活时延迟不变」这条承诺的全部实现。
		return base
	}
	if next := current * 2; next < cap {
		return next
	}
	return cap
}

func (w *ProbeQueueWorker) processBatch(ctx context.Context) (int, error) {
	// 2026-10-13（runbook §10.110.15）：返回值从 error 扩成 (claimed, error)，
	// 让 run() 能据此决定要不要退避。调用点只有 run() 一处。
	// 2026-09-17 (R39): the instance-level decrypt circuit must gate the
	// queue path too, not just the legacy drainDue loop — a wrong-key
	// instance claiming durable queue rows fails every direct round with
	// endpoint_build and (before the mirrorNodeProbeState CASE WHEN guard)
	// advanced the shared consecutive_failures ladder. Mirrors drainDue.
	if w.cfg.ProbeService != nil && w.cfg.ProbeService.worker.decryptCircuitTripped() {
		// 熔断期间不取件 ⇒ 按「这一轮没活」退避，别继续空转。
		return 0, nil
	}
	if _, err := w.cfg.Queue.RequeueExpiredLeases(ctx); err != nil {
		return 0, err
	}
	w.maybeReviveExpiredReady(ctx)
	tasks, err := w.cfg.Queue.Claim(ctx, 1, w.cfg.Lease)
	if err != nil {
		return 0, err
	}
	for _, task := range tasks {
		w.processTask(ctx, task)
	}
	return len(tasks), nil
}

// maybeReviveExpiredReady runs the zombie-ready rescue at most once per 30s
// across all worker goroutines (2026-08-18: leftover ready rows with expired
// expires_at could never be claimed, silently killing the probe pipeline).
func (w *ProbeQueueWorker) maybeReviveExpiredReady(ctx context.Context) {
	w.reviveMu.Lock()
	due := time.Since(w.reviveAt) >= 30*time.Second
	if due {
		w.reviveAt = time.Now()
	}
	w.reviveMu.Unlock()
	if !due {
		return
	}
	if n, err := w.cfg.Queue.ReviveExpiredReady(ctx); err != nil {
		slog.Warn("probe queue revive expired-ready failed", "error", err)
	} else if n > 0 {
		slog.Info("probe queue revived expired ready tasks", "count", n)
	}
}

func (w *ProbeQueueWorker) processTask(ctx context.Context, task ProbeQueueTask) {
	if w.cfg.Scope != nil && !w.cfg.Scope.AllowsIdentity(task.TenantID, int(task.CredentialID), task.RawModel) {
		slog.Info("probe queue skipped out-of-scope task", "queue_id", task.ID)
		w.complete(ctx, task, ProbeQueueResult{Status: ProbeQueueSuccess, ReasonCode: "probe_out_of_scope"})
		return
	}
	// Unified node_probe path (需求 6): ProbeService.Run does the two-round
	// direct+gateway probe with all side-effects + audit, and returns a result
	// whose NextRunAt already reflects the 7-step node-probe backoff chain.
	if task.Command == "node_probe" && w.cfg.ProbeService != nil {
		result, err := w.cfg.ProbeService.Run(ctx, task)
		if err != nil {
			// Necessity gate (2026-09-11 自检必要性检查): the probe was
			// skipped before execution and its queue row + node_probe_state
			// mirror were already deleted by the skip path. There is nothing
			// left to Complete — completing would only log a confusing
			// 0-rows "lease lost" warning against a deleted row.
			if errors.Is(err, ErrProbeNotNecessary) {
				slog.Info("probe_service skipped unnecessary task", "queue_id", task.ID, "reason", result.ReasonCode)
				return
			}
			if errors.Is(err, ErrProbeOutOfScope) || errors.Is(err, ErrProbeAutomaticIneligible) {
				slog.Info("probe_service skipped task", "queue_id", task.ID, "reason", result.ReasonCode)
				w.complete(ctx, task, result)
				return
			}
			// Run may have completed both probe rounds and applied routing
			// side effects before the audit INSERT failed. Settle the result
			// it returned instead of re-running the probe on the next retry.
			if errors.Is(err, ErrProbeAuditPersistFailed) {
				slog.Warn("probe_service audit persistence failed after probe completion", "queue_id", task.ID, "error", err)
				w.completeNodeProbe(ctx, task, result)
				return
			}
			// A reclaimed lease belongs to the new owner. The stale owner must
			// not complete or re-arm the row with its old lease token.
			if errors.Is(err, ErrProbeLeaseLost) {
				slog.Info("probe_service stopped after lease loss", "queue_id", task.ID)
				return
			}
			slog.Warn("probe_service run failed", "queue_id", task.ID, "error", err)
			w.completeFailure(ctx, task, "probe_service_error", err.Error(), 0, 0, "")
			return
		}
		w.completeNodeProbe(ctx, task, result)
		// 2026-08-26 quota-recovery-notify fix: when the unified node_probe
		// path returned a successful ProbeQueueResult, notify the dispatcher
		// so the per-credential candidate cache invalidates immediately and
		// the next chat request re-plans with the recovered binding visible.
		// nil hook → silent, routing layer falls back to candCache TTL.
		if result.Status == ProbeQueueSuccess && w.onQuotaRecovered != nil {
			w.onQuotaRecovered(int(task.CredentialID), "fast_probe")
		}
		return
	}
	target, err := w.cfg.Executor.LoadTarget(ctx, int(task.CredentialID), task.RawModel)
	if err != nil {
		w.recordIntegrityResult(ctx, task, nil, nil, err)
		w.completeFailure(ctx, task, "load_target", err.Error(), 0, 0, "")
		return
	}
	result := w.cfg.Executor.RunCommand(ctx, ProbeCommand{
		Target: target, Mode: probeMode(task.Mode), Attempt: task.Attempt,
		Origin: task.Source, ParentID: task.ParentReqID,
	})
	w.recordIntegrityResult(ctx, task, target, result, nil)
	if w.cfg.Emitter != nil {
		w.cfg.Emitter.Emit(ctx, target.CredentialID, target.ProviderID, task.TenantID,
			task.RawModel, target.OutboundModel, task.Source, task.ParentReqID, task.Attempt, result)
	}
	if result.Status == ProbeStatusSuccess {
		w.complete(ctx, task, ProbeQueueResult{
			Status: ProbeQueueSuccess, ReasonCode: result.ErrCode, ReasonDetail: result.ErrMsg,
			HTTPStatus: result.HTTPStatus, LatencyMs: result.LatencyMs,
			BodyPreview: result.RespPreview,
		})
		// 2026-08-26 quota-recovery-notify fix: on the legacy executor
		// success branch, notify the dispatcher so the per-credential
		// candidate cache invalidates immediately. nil hook → silent.
		if w.onQuotaRecovered != nil {
			w.onQuotaRecovered(int(task.CredentialID), "fast_probe")
		}
		return
	}
	w.completeFailure(ctx, task, result.ErrCode, result.ErrMsg, result.HTTPStatus, result.LatencyMs, result.RespPreview)
}

func (w *ProbeQueueWorker) recordIntegrityResult(ctx context.Context, task ProbeQueueTask, target *ProbeTarget, result *ProbeResult, targetErr error) {
	if w == nil || w.cfg.ResultSink == nil || task.Command != "integrity_verify" {
		return
	}
	if err := w.cfg.ResultSink.Record(ctx, task, target, result, targetErr); err != nil {
		slog.Warn("integrity probe result persistence failed", "queue_id", task.ID, "error", err)
	}
}

func (w *ProbeQueueWorker) completeFailure(ctx context.Context, task ProbeQueueTask, code, detail string, httpStatus, latencyMs int, preview string) {
	status := ProbeQueueFailed
	var nextRunAt *time.Time
	if task.Attempt < task.MaxAttempts && task.ExpiresAt.After(time.Now()) {
		next := time.Now().Add(computeBackoff(task.Attempt))
		status = ProbeQueueReady
		nextRunAt = &next
	}
	w.complete(ctx, task, ProbeQueueResult{
		Status: status, ReasonCode: code, ReasonDetail: detail,
		HTTPStatus: httpStatus, LatencyMs: latencyMs, BodyPreview: preview,
		NextRunAt: nextRunAt,
	})
}

// completeNodeProbe maps a ProbeService result onto a queue completion. On
// failure with attempts remaining it re-arms (status=ready) preserving the
// 7-step NextRunAt ProbeService computed; once attempts are exhausted (or the
// task expired) it records terminal failed.
func (w *ProbeQueueWorker) completeNodeProbe(ctx context.Context, task ProbeQueueTask, result ProbeQueueResult) {
	if result.Status == ProbeQueueSuccess {
		w.complete(ctx, task, result)
		return
	}
	if task.Attempt < task.MaxAttempts && task.ExpiresAt.After(time.Now()) && result.NextRunAt != nil {
		result.Status = ProbeQueueReady // re-arm in-place along the 7-step chain
	} else {
		result.Status = ProbeQueueFailed
		result.NextRunAt = nil
	}
	w.complete(ctx, task, result)
}

func (w *ProbeQueueWorker) complete(ctx context.Context, task ProbeQueueTask, result ProbeQueueResult) {
	if w == nil {
		return
	}
	if w.completeFn != nil {
		w.completeFn(ctx, task, result)
		return
	}
	if err := w.cfg.Queue.Complete(ctx, task, result); err != nil {
		if errors.Is(err, ErrProbeLeaseLost) {
			slog.Info("probe queue task completion skipped after lease loss", "queue_id", taskID(task))
			return
		}
		slog.Warn("probe queue task completion failed", "queue_id", taskID(task), "error", err)
	}
}

func probeMode(mode string) ProbeMode {
	if mode == "multi_round" {
		return ProbeModeChatPing
	}
	return ProbeModeChatPing
}

func taskID(task ProbeQueueTask) int64 {
	return task.ID
}
