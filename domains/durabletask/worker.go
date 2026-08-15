// worker.go — SR-W3 Wave 2 recovery worker (doc 18 §12).
//
// The RecoveryWorker is the background half of durable execution: it claims
// runnable tasks (ClaimRunnable with FOR UPDATE SKIP LOCKED so multiple
// workers never overlap), decrypts the request snapshot fail-closed, runs one
// attempt through an Executor while a renew loop keeps the lease alive, and
// maps the executor outcome back onto the fenced Store transitions. The
// Reaper loop (deadlines + unsafe checkpoints) terminalizes abandoned tasks
// in the same process. Wave 2 ships the skeleton with a NoopExecutor; the
// real replay executor plugs in without touching the scheduling code.
package durabletask

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/kaixuan/llm-gateway-go/metrics"
	"github.com/kaixuan/llm-gateway-go/secret"
)

// ExecutionOutcome classifies one executed attempt for the scheduler.
type ExecutionOutcome int

const (
	// OutcomeCompleted means the attempt produced a final response body.
	OutcomeCompleted ExecutionOutcome = iota
	// OutcomeRetryable means the attempt failed with a recoverable error.
	OutcomeRetryable
	// OutcomePermanent means the attempt failed unrecoverably.
	OutcomePermanent
)

// Execution is the executor's normalized result for one attempt.
type Execution struct {
	Outcome ExecutionOutcome
	// Body/ContentType are used when Outcome is OutcomeCompleted.
	Body        []byte
	ContentType string
	// ReasonCode/ErrorKind feed the transition event and terminal state.
	ReasonCode string
	ErrorKind  string
	// RetryDelay schedules the next attempt for OutcomeRetryable.
	RetryDelay time.Duration
	// NextStatus overrides the reschedule target; zero means retry_scheduled.
	NextStatus Status
}

// Executor rebuilds and runs one upstream attempt from the decrypted
// snapshot. Execute must honor ctx cancellation (lease lost, worker stop).
type Executor interface {
	Execute(ctx context.Context, task ClaimedTask, snapshot DurableRequestSnapshotV1) Execution
}

// ExecutorFunc adapts a function to Executor.
type ExecutorFunc func(ctx context.Context, task ClaimedTask, snapshot DurableRequestSnapshotV1) Execution

// Execute implements Executor.
func (f ExecutorFunc) Execute(ctx context.Context, task ClaimedTask, snapshot DurableRequestSnapshotV1) Execution {
	return f(ctx, task, snapshot)
}

// NoopExecutor keeps claimed tasks alive without performing upstream work —
// the Wave 2 stand-in until the replay executor lands. Every claim is
// rescheduled to waiting_recovery so no task is lost or marked failed while
// real execution is unbuilt.
type NoopExecutor struct{}

// Execute implements Executor by returning a short waiting_recovery cycle.
func (NoopExecutor) Execute(context.Context, ClaimedTask, DurableRequestSnapshotV1) Execution {
	return Execution{
		Outcome:    OutcomeRetryable,
		NextStatus: StatusWaitingRecovery,
		ReasonCode: ReasonRecoveryExecutorUnavailable,
		RetryDelay: 30 * time.Second,
	}
}

// ReasonRecoveryExecutorUnavailable marks noop reschedules while the replay
// executor is not wired (Wave 2 skeleton).
const ReasonRecoveryExecutorUnavailable = "recovery_executor_unavailable"

// ReasonSnapshotUndecryptable marks tasks whose encrypted snapshot can never
// be decrypted/replayed — a permanent, fail-closed terminal reason.
const ReasonSnapshotUndecryptable = "snapshot_undecryptable"

// WorkerConfig sizes the recovery worker loops.
type WorkerConfig struct {
	// Owner identifies this worker in leases and events; required.
	Owner string
	// ClaimLimit is the max tasks claimed per poll.
	ClaimLimit int
	// Lease is the claim lease duration; renewed at RenewInterval.
	Lease time.Duration
	// PollInterval paces ClaimRunnable; 0 = DefaultWorkerPollInterval.
	PollInterval time.Duration
	// RenewInterval paces lease renewal during execution; 0 = Lease/3.
	// Negative disables renewal (foreground-shaped short attempts only).
	RenewInterval time.Duration
	// ReaperInterval paces both reapers; 0 = DefaultReaperInterval.
	ReaperInterval time.Duration
	// ReaperLimit bounds rows terminalized per reaper pass.
	ReaperLimit int
}

// Default loop pacing.
const (
	DefaultWorkerPollInterval = 5 * time.Second
	DefaultWorkerLease        = 90 * time.Second
	DefaultReaperInterval     = 30 * time.Second
)

func (c WorkerConfig) withDefaults() WorkerConfig {
	if c.ClaimLimit <= 0 {
		c.ClaimLimit = 10
	}
	if c.Lease <= 0 {
		c.Lease = DefaultWorkerLease
	}
	if c.PollInterval <= 0 {
		c.PollInterval = DefaultWorkerPollInterval
	}
	if c.RenewInterval == 0 {
		c.RenewInterval = c.Lease / 3
	}
	if c.ReaperInterval <= 0 {
		c.ReaperInterval = DefaultReaperInterval
	}
	if c.ReaperLimit <= 0 {
		c.ReaperLimit = 100
	}
	return c
}

// RecoveryWorker schedules durable task attempts and runs both reapers.
type RecoveryWorker struct {
	store    *Store
	kr       *secret.Keyring
	executor Executor
	cfg      WorkerConfig

	// OutboxHandler, when set, receives every terminal projection produced
	// by the reapers (deadline expiry, unsafe checkpoint). The outbox
	// deliverer (Wave 2 slice 3) attaches here.
	OutboxHandler func(ctx context.Context, item OutboxItem)
}

// NewRecoveryWorker builds a worker. executor nil falls back to NoopExecutor.
func NewRecoveryWorker(store *Store, kr *secret.Keyring, executor Executor, cfg WorkerConfig) *RecoveryWorker {
	if executor == nil {
		executor = NoopExecutor{}
	}
	return &RecoveryWorker{store: store, kr: kr, executor: executor, cfg: cfg.withDefaults()}
}

// Run loops until ctx is done: claim/execute polls, reaper passes, and the
// active-task gauge refresh. Returns ctx.Err() on shutdown.
func (w *RecoveryWorker) Run(ctx context.Context) error {
	poll := time.NewTicker(w.cfg.PollInterval)
	defer poll.Stop()
	reap := time.NewTicker(w.cfg.ReaperInterval)
	defer reap.Stop()
	w.RefreshActiveGauge(ctx)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-poll.C:
			w.RunCycle(ctx)
		case <-reap.C:
			w.RunReapers(ctx)
			w.RefreshActiveGauge(ctx)
		}
	}
}

// RunCycle claims due tasks once and executes them serially.
func (w *RecoveryWorker) RunCycle(ctx context.Context) {
	tasks, err := w.store.ClaimRunnable(ctx, w.cfg.Owner, w.cfg.ClaimLimit, w.cfg.Lease)
	if err != nil {
		metrics.DurableRecoveryRunsTotal.WithLabelValues("error").Inc()
		slog.Warn("durabletask: claim runnable failed", "error", err, "owner", w.cfg.Owner)
		return
	}
	if len(tasks) == 0 {
		metrics.DurableRecoveryRunsTotal.WithLabelValues("empty").Inc()
		return
	}
	metrics.DurableRecoveryRunsTotal.WithLabelValues("claimed").Inc()
	for _, task := range tasks {
		w.executeClaimed(ctx, task)
	}
}

// RunReapers terminalizes due tasks via both reapers and forwards the
// resulting outbox projections to OutboxHandler.
func (w *RecoveryWorker) RunReapers(ctx context.Context) {
	for _, pass := range []struct {
		name string
		reap func(context.Context, int) ([]OutboxItem, error)
	}{
		{"deadlines", w.store.ReapDeadlines},
		{"unsafe_checkpoints", w.store.ReapUnsafeCheckpoints},
	} {
		items, err := pass.reap(ctx, w.cfg.ReaperLimit)
		if err != nil {
			slog.Warn("durabletask: reaper pass failed", "reaper", pass.name, "error", err)
			continue
		}
		for _, item := range items {
			if w.OutboxHandler != nil {
				w.OutboxHandler(ctx, item)
			}
		}
	}
}

// RefreshActiveGauge publishes authoritative per-tenant active task counts.
func (w *RecoveryWorker) RefreshActiveGauge(ctx context.Context) {
	counts, err := w.store.ActiveTaskCounts(ctx)
	if err != nil {
		slog.Warn("durabletask: active task counts failed", "error", err)
		return
	}
	metrics.SetDurableTasksActive(counts)
}

// executeClaimed runs one attempt end-to-end: decrypt fail-closed, execute
// under a lease-renewing context, then map the outcome onto fenced writes.
// ErrLeaseLost at any fenced write means another owner moved the task on —
// the outcome is discarded, never double-applied.
func (w *RecoveryWorker) executeClaimed(ctx context.Context, task ClaimedTask) {
	snapshot, _, err := DecryptRequestSnapshotV1(task.SnapshotCiphertext, task.SnapshotVersion, w.kr,
		secret.AADBinding{TenantID: task.TenantID, TaskID: task.TaskID, RequestHash: task.RequestHash})
	if err != nil {
		// Fail closed: an undecryptable snapshot can never be replayed.
		slog.Error("durabletask: snapshot decrypt failed; failing task",
			"task_id", task.TaskID, "error", err)
		if _, ferr := w.store.Fail(ctx, task.Lease, FailureParams{
			Status:     StatusPermanentFailed,
			ReasonCode: ReasonSnapshotUndecryptable,
			ErrorKind:  "snapshot_decrypt_failed",
		}); ferr != nil {
			if errors.Is(ferr, ErrLeaseLost) {
				w.noteLeaseLost(task.TaskID, "fail_undecryptable")
			} else {
				slog.Warn("durabletask: fail undecryptable task errored", "task_id", task.TaskID, "error", ferr)
			}
		}
		return
	}

	execCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	leaseLost := newLeaseLossSignal()
	go func() {
		defer leaseLost.finish()
		w.renewUntilDone(execCtx, cancel, task.Lease, leaseLost)
	}()

	exec := w.executor.Execute(execCtx, task, snapshot)
	cancel()
	leaseLost.wait() // renew goroutine has fully exited before we touch the store
	if leaseLost.lost() {
		// The fencing token moved on while executing: this attempt's
		// outcome is void; the current owner owns the transition.
		w.noteLeaseLost(task.TaskID, "renew_during_execution")
		slog.Warn("durabletask: lease lost during execution; discarding outcome",
			"task_id", task.TaskID, "outcome", int(exec.Outcome))
		return
	}
	w.applyOutcome(ctx, task.Lease, exec)
}

// renewUntilDone extends the lease every RenewInterval until execCtx ends.
// A fencing failure cancels the execution context (stopping the executor) and
// flags the loss so the outcome is discarded rather than double-applied.
func (w *RecoveryWorker) renewUntilDone(ctx context.Context, cancel context.CancelFunc, lease Lease, lost *leaseLossSignal) {
	if w.cfg.RenewInterval < 0 {
		<-ctx.Done()
		return
	}
	ticker := time.NewTicker(w.cfg.RenewInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := w.store.RenewLease(ctx, lease, time.Now().Add(w.cfg.Lease)); err != nil {
				if errors.Is(err, ErrLeaseLost) {
					w.noteLeaseLost(lease.TaskID, "renew")
					lost.trigger()
					cancel()
				} else {
					slog.Warn("durabletask: lease renew errored", "task_id", lease.TaskID, "error", err)
				}
				return
			}
		}
	}
}

// noteLeaseLost records a fenced-off write for observability.
func (w *RecoveryWorker) noteLeaseLost(taskID, stage string) {
	metrics.DurableLeaseLostTotal.Inc()
	slog.Warn("durabletask: fenced write rejected", "task_id", taskID, "stage", stage)
}

func (w *RecoveryWorker) applyOutcome(ctx context.Context, lease Lease, exec Execution) {
	switch exec.Outcome {
	case OutcomeCompleted:
		if _, err := w.store.Complete(ctx, lease, CompleteParams{Body: exec.Body, ContentType: exec.ContentType}); err != nil {
			if errors.Is(err, ErrLeaseLost) {
				w.noteLeaseLost(lease.TaskID, "complete")
				return
			}
			slog.Error("durabletask: completion write failed", "task_id", lease.TaskID, "error", err)
		}
	case OutcomeRetryable:
		next := exec.NextStatus
		if next != StatusWaitingRecovery && next != StatusRetryScheduled {
			next = StatusRetryScheduled
		}
		delay := exec.RetryDelay
		if delay <= 0 {
			delay = time.Minute
		}
		if err := w.store.Reschedule(ctx, lease, RescheduleParams{
			Status:      next,
			NextRetryAt: time.Now().Add(delay),
			ErrorKind:   exec.ErrorKind,
			ReasonCode:  exec.ReasonCode,
		}); err != nil {
			if errors.Is(err, ErrLeaseLost) {
				w.noteLeaseLost(lease.TaskID, "reschedule")
				return
			}
			slog.Error("durabletask: reschedule write failed", "task_id", lease.TaskID, "error", err)
		}
	case OutcomePermanent:
		if _, err := w.store.Fail(ctx, lease, FailureParams{
			Status:     StatusPermanentFailed,
			ReasonCode: exec.ReasonCode,
			ErrorKind:  exec.ErrorKind,
		}); err != nil {
			if errors.Is(err, ErrLeaseLost) {
				w.noteLeaseLost(lease.TaskID, "fail")
				return
			}
			slog.Error("durabletask: failure write failed", "task_id", lease.TaskID, "error", err)
		}
	default:
		slog.Error("durabletask: unknown execution outcome", "task_id", lease.TaskID, "outcome", int(exec.Outcome))
	}
}

// leaseLossSignal distinguishes renew-loss cancellation from worker shutdown
// so a fenced-off attempt discards its outcome. trigger() is one-shot;
// finish() marks the renew goroutine's exit so wait() is race-free.
type leaseLossSignal struct {
	mu     sync.Mutex
	closed bool
	c      chan struct{}
	done   chan struct{}
}

func newLeaseLossSignal() *leaseLossSignal {
	return &leaseLossSignal{c: make(chan struct{}), done: make(chan struct{})}
}

func (s *leaseLossSignal) trigger() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		close(s.c)
	}
}

func (s *leaseLossSignal) lost() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// finish marks the renew goroutine exited; call exactly once via defer.
func (s *leaseLossSignal) finish() { close(s.done) }

// wait blocks until the renew goroutine has exited.
func (s *leaseLossSignal) wait() { <-s.done }
