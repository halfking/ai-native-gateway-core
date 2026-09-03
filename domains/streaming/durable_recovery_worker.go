package streaming

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
	"github.com/kaixuan/llm-gateway-go/durable"
	"github.com/kaixuan/llm-gateway-go/metrics"
	"github.com/kaixuan/llm-gateway-go/pending"
)

// DurableWorkerStore is the narrow durable repository surface used by the
// recovery loop. Keeping this interface here prevents durable from depending
// on streaming execution types.
type DurableWorkerStore interface {
	ClaimRunnable(context.Context, durable.ClaimOptions) ([]*durable.Task, error)
	LoadSnapshot(context.Context, string) (*durable.Snapshot, error)
	RenewLease(context.Context, string, string, int64, time.Time) error
	Reschedule(context.Context, durable.RescheduleParams) error
	CommitTerminal(context.Context, durable.TerminalCommit) (*durable.TerminalProjection, error)
	PersistSettlementIntent(context.Context, durable.TerminalCommit) error
	ClaimSettlementIntent(context.Context, string, string, time.Duration, time.Time) (*durable.ClaimedSettlement, error)
	ClaimSettlementIntents(context.Context, string, time.Duration, int, time.Time) ([]*durable.ClaimedSettlement, error)
	FinalizeSettlement(context.Context, durable.ClaimedSettlement) (*durable.TerminalProjection, error)
	RetrySettlementIntent(context.Context, durable.ClaimedSettlement, time.Time, error) error
	ReapDeadlines(context.Context, int, time.Time) ([]*durable.ReapedTaskInfo, error)
	ReapUnsafeCheckpointed(context.Context, int, time.Time) ([]*durable.Task, error)
	ProjectPendingOutbox(context.Context, *pending.Store, int, time.Time) (int, error)
	ActiveTaskCounts(context.Context) (map[string]int64, error)
}

// DurableAttempt is the worker-facing result of rebuilding and executing one
// detached attempt. The runner owns protocol conversion and never receives an
// http.ResponseWriter or client request context.
type DurableAttempt struct {
	Result      *AttemptResult
	Body        []byte
	ContentType string
	ErrorKind   string
	Attempt     int
}

type DurableAttemptRunner interface {
	Run(context.Context, *durable.Task, *durable.Snapshot) (*DurableAttempt, error)
}

type DurableWorkerOptions struct {
	Owner        string
	PollInterval time.Duration
	Lease        time.Duration
	Batch        int
	ReapLimit    int
	// StopGrace bounds how long Stop waits for an in-flight attempt that
	// ignores cancellation. 0 → 10s.
	StopGrace time.Duration
	// MaxRetries bounds retries after the initial execution. 0 → 100.
	MaxRetries int
	// RetryBase is the initial retry delay. 0 → 2s.
	RetryBase time.Duration
	// RetryMax bounds locally calculated exponential retry delays. An upstream
	// RetryAfter remains the authoritative schedule when present. 0 → 120s.
	RetryMax time.Duration
	// WorkerCount bounds how many claimed tasks execute concurrently. The
	// claim loop stays single-owner; execution fans out under this semaphore.
	// 0 → 1; values above 32 clamp to 32.
	WorkerCount int
}

func (o DurableWorkerOptions) withDefaults() DurableWorkerOptions {
	if o.Owner == "" {
		o.Owner = "gateway-durable-worker"
	}
	if o.PollInterval <= 0 {
		o.PollInterval = 5 * time.Second
	}
	if o.Lease <= 0 {
		o.Lease = 60 * time.Second
	}
	if o.Batch <= 0 {
		o.Batch = 8
	}
	if o.ReapLimit <= 0 {
		o.ReapLimit = 32
	}
	if o.StopGrace <= 0 {
		o.StopGrace = 10 * time.Second
	}
	if o.MaxRetries <= 0 {
		o.MaxRetries = 100
	}
	if o.RetryBase <= 0 {
		o.RetryBase = 2 * time.Second
	}
	if o.RetryMax <= 0 {
		o.RetryMax = 120 * time.Second
	}
	if o.WorkerCount <= 0 {
		o.WorkerCount = 1
	}
	if o.WorkerCount > 32 {
		o.WorkerCount = 32
	}
	return o
}

// DurableRecoveryWorker claims only runnable none/metadata tasks and commits
// terminal results through the fencing-aware durable repository. It is inert
// until Start is called by durable-enabled production wiring.
type DurableRecoveryWorker struct {
	store  DurableWorkerStore
	redis  *pending.Store
	runner DurableAttemptRunner
	opts   DurableWorkerOptions
	now    func() time.Time

	mu          sync.Mutex
	cancel      context.CancelFunc
	done        chan struct{}
	start       bool
	lastTenants map[string]struct{}

	// budgets holds the process-lifetime upstream attempt budget per durable
	// task so every detached execution of one task shares a single call
	// ceiling (the durable analogue of the foreground coordinator's
	// request-wide budget). Entries are dropped on terminal settlement and
	// when the reapers take a task over; the map is also hard-capped so a
	// pathological claim stream cannot grow it without bound. Budgets do not
	// survive process restarts — that requires persisting the used count on
	// the task row and stays a documented follow-up.
	budgetMu sync.Mutex
	budgets  map[string]*executors.UpstreamAttemptBudget
}

// durableTaskBudgetCap bounds the per-task budget registry. Reaching it
// resets the registry: worst case a still-retrying task regains a fresh
// budget, which only widens the ceiling back toward the pre-fix behavior.
const durableTaskBudgetCap = 4096

func NewDurableRecoveryWorker(store DurableWorkerStore, redis *pending.Store, runner DurableAttemptRunner, opts DurableWorkerOptions) *DurableRecoveryWorker {
	return &DurableRecoveryWorker{store: store, redis: redis, runner: runner, opts: opts.withDefaults(), budgets: make(map[string]*executors.UpstreamAttemptBudget)}
}

// BudgetForTask returns the shared upstream attempt budget for one durable
// task, creating it on first use with the worker's retry ceiling. It is the
// producer side of DurableAttemptRunnerImpl.BudgetProvider.
func (w *DurableRecoveryWorker) BudgetForTask(taskID string) *executors.UpstreamAttemptBudget {
	if taskID == "" {
		return nil
	}
	w.budgetMu.Lock()
	defer w.budgetMu.Unlock()
	if w.budgets == nil {
		w.budgets = make(map[string]*executors.UpstreamAttemptBudget)
	}
	b, ok := w.budgets[taskID]
	if !ok {
		if len(w.budgets) >= durableTaskBudgetCap {
			w.budgets = make(map[string]*executors.UpstreamAttemptBudget)
		}
		limit := w.opts.MaxRetries + 1
		if limit > executors.MaxUpstreamAttemptLimit {
			limit = executors.MaxUpstreamAttemptLimit
		}
		b = executors.NewUpstreamAttemptBudget(limit)
		w.budgets[taskID] = b
	}
	return b
}

func (w *DurableRecoveryWorker) forgetBudget(taskID string) {
	w.budgetMu.Lock()
	defer w.budgetMu.Unlock()
	delete(w.budgets, taskID)
}

func (w *DurableRecoveryWorker) clock() time.Time {
	if w.now != nil {
		return w.now()
	}
	return time.Now()
}

func (w *DurableRecoveryWorker) Start(parent context.Context) {
	w.mu.Lock()
	if w.start || w.store == nil || w.runner == nil {
		w.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(parent)
	w.cancel, w.done, w.start = cancel, make(chan struct{}), true
	w.mu.Unlock()
	go func() {
		defer close(w.done)
		// Audit-2026-08-29 (§5.2 hardening): wrap runOnce in a recover so a
		// single tick's panic doesn't silently kill the worker goroutine.
		// Without this, a panic in store.ReapDeadlines / ClaimRunnable /
		// runTask leaves durable_recovery_runs_total frozen until process
		// restart. The deferred recover logs and lets the loop continue so
		// subsequent ticks still run.
		safeRunOnce := func() {
			defer func() {
				if r := recover(); r != nil {
					slog.Warn("durable recovery worker panicked during runOnce",
						"panic", r,
						"stack", string(debug.Stack()))
				}
			}()
			w.runOnce(ctx)
		}
		safeRunOnce()
		ticker := time.NewTicker(w.opts.PollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				safeRunOnce()
			}
		}
	}()
}

func (w *DurableRecoveryWorker) Stop() {
	w.mu.Lock()
	cancel, done := w.cancel, w.done
	w.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	// 有界等待：生产 runner 的流式上游可能不响应取消（WithoutCancel），
	// 无限等待会让 Stop 永久挂起。超时后放弃等待，goroutine 自行退出时
	// close(done) 依然安全（无人再读）。
	select {
	case <-done:
	case <-time.After(w.opts.StopGrace):
		metrics.DurableRecoveryStopGraceExceededTotal.Inc()
		slog.Warn("durable recovery worker stop grace exceeded; background attempt still running")
	}
	w.mu.Lock()
	w.cancel = nil
	w.start = false
	w.mu.Unlock()
}

func (w *DurableRecoveryWorker) runOnce(ctx context.Context) {
	now := w.clock()
	w.drainSettlementIntents(ctx, now)
	if reaped, err := w.store.ReapDeadlines(ctx, w.opts.ReapLimit, now); err != nil {
		slog.Warn("durable deadline reaper failed", "error", err)
	} else {
		for _, rt := range reaped {
			w.forgetBudget(rt.ID)
		}
	}
	if unsafe, err := w.store.ReapUnsafeCheckpointed(ctx, w.opts.ReapLimit, now); err != nil {
		slog.Warn("durable safety reaper failed", "error", err)
	} else {
		for _, t := range unsafe {
			w.forgetBudget(t.ID)
		}
	}
	if w.redis != nil && w.redis.Enabled() {
		if _, err := w.store.ProjectPendingOutbox(ctx, w.redis, w.opts.ReapLimit, now); err != nil && !errors.Is(err, context.Canceled) {
			slog.Warn("durable pending repair failed", "error", err)
		}
	}
	counts, err := w.store.ActiveTaskCounts(ctx)
	if err == nil {
		w.reconcileActiveTaskGauge(counts)
		metrics.SetDurableTasksActive(counts)
	}
	tasks, err := w.store.ClaimRunnable(ctx, durable.ClaimOptions{Owner: w.opts.Owner, Lease: w.opts.Lease, Batch: w.opts.Batch, Now: now})
	if err != nil {
		metrics.DurableRecoveryRunsTotal.WithLabelValues("error").Inc()
		slog.Warn("durable claim failed", "error", err)
		return
	}
	if len(tasks) == 0 {
		metrics.DurableRecoveryRunsTotal.WithLabelValues("empty").Inc()
	} else {
		metrics.DurableRecoveryRunsTotal.WithLabelValues("claimed").Inc()
	}
	// Fan claimed tasks out under the WorkerCount semaphore: the claim loop
	// stays single-owner while detached executions overlap. With the default
	// count of 1 this is exactly the previous serial behavior.
	sem := make(chan struct{}, w.opts.WorkerCount)
	var wg sync.WaitGroup
	for _, task := range tasks {
		sem <- struct{}{}
		wg.Add(1)
		go func(t *durable.Task) {
			defer wg.Done()
			defer func() { <-sem }()
			w.runTask(ctx, t)
		}(task)
	}
	wg.Wait()
}

func (w *DurableRecoveryWorker) drainSettlementIntents(ctx context.Context, now time.Time) {
	intents, err := w.store.ClaimSettlementIntents(ctx, w.opts.Owner, w.opts.Lease, w.opts.Batch, now)
	if err != nil {
		slog.Warn("durable settlement claim failed", "error", err)
		return
	}
	for _, intent := range intents {
		if _, err := w.store.FinalizeSettlement(ctx, *intent); err != nil {
			if errors.Is(err, durable.ErrLeaseLost) {
				w.noteLeaseLost()
				continue
			}
			next := w.clock().Add(w.retryDelay(intent.Attempts, 0))
			if retryErr := w.store.RetrySettlementIntent(ctx, *intent, next, err); retryErr != nil {
				if errors.Is(retryErr, durable.ErrLeaseLost) {
					w.noteLeaseLost()
				} else {
					slog.Warn("durable settlement retry scheduling failed", "task_id", intent.TaskID, "error", retryErr)
				}
			}
		}
	}
}

func (w *DurableRecoveryWorker) reconcileActiveTaskGauge(counts map[string]int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for tenant := range w.lastTenants {
		if _, ok := counts[tenant]; !ok {
			metrics.SurvivalActiveTasks.WithLabelValues(tenant).Set(0)
		}
	}
	next := make(map[string]struct{}, len(counts))
	for tenant, count := range counts {
		metrics.SurvivalActiveTasks.WithLabelValues(tenant).Set(float64(count))
		next[tenant] = struct{}{}
	}
	w.lastTenants = next
}

func (w *DurableRecoveryWorker) runTask(ctx context.Context, task *durable.Task) {
	snapshot, err := w.store.LoadSnapshot(ctx, task.ID)
	if err != nil {
		// LoadSnapshot 失败可能是瞬态 DB 错误或 keyring 暂缺——fail closed
		// 的语义是「不声称已接管」，不是销毁任务。改走重排（带退避），
		// 由 deadline reaper 在 deadline 到期时给出安全的 expired 终态。
		slog.Warn("durable snapshot load failed; rescheduling", "task_id", task.ID, "error", err)
		w.rescheduleWithError(ctx, task, "durable_snapshot_unavailable")
		return
	}
	leaseUntil := w.clock().Add(w.opts.Lease)
	if err := w.store.RenewLease(ctx, task.ID, task.LeaseOwner, task.FencingToken, leaseUntil); err != nil {
		if errors.Is(err, durable.ErrLeaseLost) {
			w.noteLeaseLost()
		}
		return
	}
	attemptCtx, cancelAttempt := context.WithCancel(ctx)
	defer cancelAttempt()
	leaseInterval := w.opts.Lease / 2
	if leaseInterval <= 0 {
		leaseInterval = time.Second
	}
	leaseTicker := time.NewTicker(leaseInterval)
	defer leaseTicker.Stop()
	attemptDone := make(chan struct{})
	var attempt *DurableAttempt
	go func() {
		defer close(attemptDone)
		// Audit-2026-08-29 (§5.2 hardening): w.runner.Run is an external
		// interface that may panic on a misbehaving implementation. Without
		// recover, the outer-scope `attempt, err` assignment is skipped and
		// `err` retains its previous value; the existing reschedule branch
		// at attemptFinished would log a generic message with no traceback
		// to the actual panic. Capture the panic value into `err` so the
		// reschedule reason and the slog line carry the panic message.
		func() {
			defer func() {
				if r := recover(); r != nil {
					err = fmt.Errorf("durable attempt runner panicked: %v", r)
					slog.Warn("durable attempt runner panicked",
						"task_id", task.ID,
						"panic", r,
						"stack", string(debug.Stack()))
				}
			}()
			attempt, err = w.runner.Run(attemptCtx, task, snapshot)
			if err == nil && attempt == nil {
				err = errors.New("durable runner returned nil attempt")
			}
		}()
	}()
	for {
		select {
		case <-attemptDone:
			goto attemptFinished
		case <-leaseTicker.C:
			if err := w.store.RenewLease(ctx, task.ID, task.LeaseOwner, task.FencingToken, w.clock().Add(w.opts.Lease)); err != nil {
				if errors.Is(err, durable.ErrLeaseLost) {
					w.noteLeaseLost()
				}
				cancelAttempt()
				waitAttemptDone(attemptDone, w.opts.StopGrace)
				return
			}
		case <-ctx.Done():
			cancelAttempt()
			if !waitAttemptDone(attemptDone, w.opts.StopGrace) {
				metrics.DurableRecoveryStopGraceExceededTotal.Inc()
				slog.Warn("durable recovery attempt ignored cancellation past stop grace", "task_id", task.ID)
			}
			return
		}
	}
attemptFinished:
	if err != nil || attempt == nil || attempt.Result == nil {
		// runner 级错误（重建失败/上游基础设施异常）无法区分类别时不做
		// 永久终态——重排重试，deadline reaper 兜底。任务级永久判定只能
		// 来自 AggregateTaskOutcome（下方 fallthrough）。
		slog.Warn("durable attempt runner failed; rescheduling", "task_id", task.ID, "error", err)
		w.rescheduleWithError(ctx, task, "durable_runner_error")
		return
	}
	decision := AggregateTaskOutcome(attempt.Result)
	if decision.Action == TaskActionSucceed {
		if len(attempt.Body) == 0 {
			// The durable store rejects completed terminals without a body.
			// Mirror the foreground settlement: record an honest failure
			// instead of leaving the task running until lease reclaim.
			w.settleTerminal(ctx, durable.TerminalCommit{
				Task: task, Outcome: durable.StatusFailed,
				ReasonCode: "durable_result_unavailable", ErrorKind: "durable_result",
				Attempt: attempt.Attempt,
			})
			return
		}
		w.settleTerminal(ctx, durable.TerminalCommit{Task: task, Outcome: durable.StatusCompleted, Body: attempt.Body, ContentType: attempt.ContentType, Attempt: attempt.Attempt, ErrorKind: attempt.ErrorKind})
		return
	}
	if decision.Action == TaskActionRetryNow || decision.Action == TaskActionWaitRecovery {
		if !w.canRetry(task) {
			w.failTask(ctx, task, fmt.Errorf("durable task exhausted retry budget (attempt=%d, max_retries=%d)", task.AttemptCount, w.opts.MaxRetries))
			return
		}
		next := w.clock().Add(w.retryDelay(task.AttemptCount, decision.NextRetryAfter))
		if err := w.store.Reschedule(ctx, durable.RescheduleParams{TaskID: task.ID, LeaseOwner: task.LeaseOwner, FencingToken: task.FencingToken, NextRetryAt: next, ErrorKind: attempt.ErrorKind, Reason: decision.Reason, Attempt: attempt.Attempt}); err != nil && errors.Is(err, durable.ErrLeaseLost) {
			w.noteLeaseLost()
		}
		return
	}
	w.failTask(ctx, task, fmt.Errorf("durable task terminal: %s", decision.Reason))
}

// noteLeaseLost records one fenced-off write in both observability families:
// gateway_survival_lease_conflicts_total (per-request coordinator view) and
// durable_lease_lost_total (worker-path view, doc 18 §15).
func (w *DurableRecoveryWorker) noteLeaseLost() {
	metrics.SurvivalLeaseConflictsTotal.Inc()
	metrics.DurableLeaseLostTotal.Inc()
}

// rescheduleWithError releases the claim with the normal bounded backoff and a
// stable reason code. Used for errors whose recoverability is unknown (snapshot
// load, runner infrastructure); the same retry budget still applies.
func (w *DurableRecoveryWorker) rescheduleWithError(ctx context.Context, task *durable.Task, reason string) {
	if !w.canRetry(task) {
		w.failTask(ctx, task, fmt.Errorf("durable task exhausted retry budget (attempt=%d, max_retries=%d)", task.AttemptCount, w.opts.MaxRetries))
		return
	}
	next := w.clock().Add(w.retryDelay(task.AttemptCount, 0))
	if err := w.store.Reschedule(ctx, durable.RescheduleParams{
		TaskID: task.ID, LeaseOwner: task.LeaseOwner, FencingToken: task.FencingToken,
		NextRetryAt: next, ErrorKind: "durable_recovery", Reason: reason, Attempt: task.AttemptCount,
	}); err != nil && errors.Is(err, durable.ErrLeaseLost) {
		w.noteLeaseLost()
	}
}

func (w *DurableRecoveryWorker) canRetry(task *durable.Task) bool {
	return task.AttemptCount < w.opts.MaxRetries+1
}

func (w *DurableRecoveryWorker) retryDelay(attempt int, suggested time.Duration) time.Duration {
	if suggested > 0 {
		return suggested
	}
	if attempt < 1 {
		attempt = 1
	}
	delay := w.opts.RetryBase
	for retry := 1; retry < attempt && delay < w.opts.RetryMax; retry++ {
		delay *= 2
		if delay > w.opts.RetryMax {
			delay = w.opts.RetryMax
		}
	}
	return delay
}

// waitAttemptDone waits at most grace for a cancelled attempt and reports
// whether it exited before the bound. A runner that ignores cancellation
// (streaming upstreams use WithoutCancel) must not block the worker loop or
// Stop forever; the goroutine still exits on its own afterward.
func waitAttemptDone(done <-chan struct{}, grace time.Duration) bool {
	select {
	case <-done:
		return true
	case <-time.After(grace):
		return false
	}
}

func (w *DurableRecoveryWorker) settleTerminal(ctx context.Context, c durable.TerminalCommit) {
	// The task will not execute again; its shared budget can be released even
	// when the settlement write below fails (the intent drain / lease expiry
	// owns the retry from here).
	w.forgetBudget(c.Task.ID)
	if err := w.store.PersistSettlementIntent(ctx, c); err != nil {
		if errors.Is(err, durable.ErrLeaseLost) {
			w.noteLeaseLost()
			return
		}
		metrics.DurableSettlementStageFailuresTotal.WithLabelValues("worker_persist_intent").Inc()
		slog.Warn("durable worker settlement persist failed; intent drain or lease expiry owns the task",
			"task_id", c.Task.ID, "error", err)
		return
	}
	claim, err := w.store.ClaimSettlementIntent(ctx, c.Task.ID, w.opts.Owner, w.opts.Lease, w.clock())
	if err != nil || claim == nil {
		if err != nil {
			slog.Warn("durable worker settlement claim failed", "task_id", c.Task.ID, "error", err)
		}
		return
	}
	if _, err := w.store.FinalizeSettlement(ctx, *claim); err != nil {
		if errors.Is(err, durable.ErrLeaseLost) {
			w.noteLeaseLost()
			return
		}
		next := w.clock().Add(w.retryDelay(claim.Attempts, 0))
		if retryErr := w.store.RetrySettlementIntent(ctx, *claim, next, err); retryErr != nil {
			if errors.Is(retryErr, durable.ErrLeaseLost) {
				w.noteLeaseLost()
			} else {
				slog.Warn("durable settlement retry scheduling failed", "task_id", claim.TaskID, "error", retryErr)
			}
		}
	}
}

func (w *DurableRecoveryWorker) failTask(ctx context.Context, task *durable.Task, cause error) {
	reason := "durable_snapshot_invalid"
	if cause != nil {
		reason = cause.Error()
	}
	w.settleTerminal(ctx, durable.TerminalCommit{Task: task, Outcome: durable.StatusFailed, ReasonCode: reason, ErrorKind: "durable_recovery", Attempt: task.AttemptCount})
}
