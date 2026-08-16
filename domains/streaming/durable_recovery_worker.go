package streaming

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

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
}

func NewDurableRecoveryWorker(store DurableWorkerStore, redis *pending.Store, runner DurableAttemptRunner, opts DurableWorkerOptions) *DurableRecoveryWorker {
	return &DurableRecoveryWorker{store: store, redis: redis, runner: runner, opts: opts.withDefaults()}
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
		w.runOnce(ctx)
		ticker := time.NewTicker(w.opts.PollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				w.runOnce(ctx)
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
	if _, err := w.store.ReapDeadlines(ctx, w.opts.ReapLimit, now); err != nil {
		slog.Warn("durable deadline reaper failed", "error", err)
	}
	if _, err := w.store.ReapUnsafeCheckpointed(ctx, w.opts.ReapLimit, now); err != nil {
		slog.Warn("durable safety reaper failed", "error", err)
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
	for _, task := range tasks {
		w.runTask(ctx, task)
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
		attempt, err = w.runner.Run(attemptCtx, task, snapshot)
		if err == nil && attempt == nil {
			err = errors.New("durable runner returned nil attempt")
		}
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
			waitAttemptDone(attemptDone, w.opts.StopGrace)
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
		projection, commitErr := w.store.CommitTerminal(ctx, durable.TerminalCommit{Task: task, Outcome: durable.StatusCompleted, Body: attempt.Body, ContentType: attempt.ContentType, Attempt: attempt.Attempt, ErrorKind: attempt.ErrorKind})
		if commitErr != nil {
			if errors.Is(commitErr, durable.ErrLeaseLost) {
				w.noteLeaseLost()
			}
			return
		}
		if projection != nil && !projection.Committed {
			w.noteLeaseLost()
		}
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

// waitAttemptDone waits at most grace for a cancelled attempt to finish. A
// runner that ignores cancellation (streaming upstreams use WithoutCancel)
// must not block the worker loop or Stop forever; the goroutine still exits
// on its own and its post-exit writes are to variables nobody reads again.
func waitAttemptDone(done <-chan struct{}, grace time.Duration) {
	select {
	case <-done:
	case <-time.After(grace):
	}
}

func (w *DurableRecoveryWorker) failTask(ctx context.Context, task *durable.Task, cause error) {
	reason := "durable_snapshot_invalid"
	if cause != nil {
		reason = cause.Error()
	}
	projection, err := w.store.CommitTerminal(ctx, durable.TerminalCommit{Task: task, Outcome: durable.StatusFailed, ReasonCode: reason, ErrorKind: "durable_recovery"})
	if err != nil && errors.Is(err, durable.ErrLeaseLost) {
		w.noteLeaseLost()
	}
	if projection != nil && !projection.Committed {
		w.noteLeaseLost()
	}
}
