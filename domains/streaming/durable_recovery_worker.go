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
	<-done
	w.mu.Lock()
	w.cancel = nil
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
	}
	tasks, err := w.store.ClaimRunnable(ctx, durable.ClaimOptions{Owner: w.opts.Owner, Lease: w.opts.Lease, Batch: w.opts.Batch, Now: now})
	if err != nil {
		slog.Warn("durable claim failed", "error", err)
		return
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
		w.failTask(ctx, task, fmt.Errorf("load durable snapshot: %w", err))
		return
	}
	leaseUntil := w.clock().Add(w.opts.Lease)
	if err := w.store.RenewLease(ctx, task.ID, task.LeaseOwner, task.FencingToken, leaseUntil); err != nil {
		if errors.Is(err, durable.ErrLeaseLost) {
			metrics.SurvivalLeaseConflictsTotal.Inc()
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
					metrics.SurvivalLeaseConflictsTotal.Inc()
				}
				cancelAttempt()
				<-attemptDone
				return
			}
		case <-ctx.Done():
			cancelAttempt()
			<-attemptDone
			return
		}
	}
attemptFinished:
	if err != nil || attempt == nil || attempt.Result == nil {
		w.failTask(ctx, task, err)
		return
	}
	decision := AggregateTaskOutcome(attempt.Result)
	if decision.Action == TaskActionSucceed {
		projection, commitErr := w.store.CommitTerminal(ctx, durable.TerminalCommit{Task: task, Outcome: durable.StatusCompleted, Body: attempt.Body, ContentType: attempt.ContentType, Attempt: attempt.Attempt, ErrorKind: attempt.ErrorKind})
		if commitErr != nil {
			if errors.Is(commitErr, durable.ErrLeaseLost) {
				metrics.SurvivalLeaseConflictsTotal.Inc()
			}
			return
		}
		if projection != nil && !projection.Committed {
			metrics.SurvivalLeaseConflictsTotal.Inc()
		}
		return
	}
	if decision.Action == TaskActionRetryNow || decision.Action == TaskActionWaitRecovery {
		next := w.clock().Add(decision.NextRetryAfter)
		if err := w.store.Reschedule(ctx, durable.RescheduleParams{TaskID: task.ID, LeaseOwner: task.LeaseOwner, FencingToken: task.FencingToken, NextRetryAt: next, ErrorKind: attempt.ErrorKind, Reason: decision.Reason, Attempt: attempt.Attempt}); err != nil && errors.Is(err, durable.ErrLeaseLost) {
			metrics.SurvivalLeaseConflictsTotal.Inc()
		}
		return
	}
	w.failTask(ctx, task, fmt.Errorf("durable task terminal: %s", decision.Reason))
}

func (w *DurableRecoveryWorker) failTask(ctx context.Context, task *durable.Task, cause error) {
	reason := "durable_snapshot_invalid"
	if cause != nil {
		reason = cause.Error()
	}
	projection, err := w.store.CommitTerminal(ctx, durable.TerminalCommit{Task: task, Outcome: durable.StatusFailed, ReasonCode: reason, ErrorKind: "durable_recovery"})
	if err != nil && errors.Is(err, durable.ErrLeaseLost) {
		metrics.SurvivalLeaseConflictsTotal.Inc()
	}
	if projection != nil && !projection.Committed {
		metrics.SurvivalLeaseConflictsTotal.Inc()
	}
}
