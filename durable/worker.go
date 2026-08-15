package durable

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// RecoveryWorker claims durable tasks and delegates execution to an injected executor.
type RecoveryWorker struct {
	store    Store
	executor Executor
	cfg      WorkerConfig
}

// NewRecoveryWorker constructs a recovery worker and rejects incomplete wiring.
func NewRecoveryWorker(store Store, executor Executor, cfg WorkerConfig) (*RecoveryWorker, error) {
	if store == nil {
		return nil, fmt.Errorf("%w: store is nil", ErrWorkerNotConfigured)
	}
	if executor == nil {
		return nil, fmt.Errorf("%w: executor is nil", ErrWorkerNotConfigured)
	}
	return &RecoveryWorker{store: store, executor: executor, cfg: cfg.withDefaults()}, nil
}

// Run polls for tasks and periodically executes both safety reapers until ctx is cancelled.
func (w *RecoveryWorker) Run(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	poll := time.NewTicker(w.cfg.PollInterval)
	reap := time.NewTicker(w.cfg.ReapInterval)
	defer poll.Stop()
	defer reap.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-reap.C:
			if r, ok := w.store.(Reaper); ok {
				_, _ = r.ReapUnsafeCommitState(ctx)
				_, _ = r.ReapDeadlines(ctx)
			}
		case <-poll.C:
			_ = w.RunOnce(ctx)
		}
	}
}

// RunOnce processes at most BatchSize runnable tasks and isolates per-task failures.
func (w *RecoveryWorker) RunOnce(ctx context.Context) error {
	if w == nil || w.store == nil || w.executor == nil {
		return ErrWorkerNotConfigured
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var firstErr error
	for i := 0; i < w.cfg.BatchSize; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		task, err := w.store.Claim(ctx, w.cfg.Owner, w.cfg.LeaseDuration)
		if errors.Is(err, ErrNoTask) {
			break
		}
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("claim durable task: %w", err)
			}
			break
		}
		result, execErr := w.executor.Execute(ctx, task)
		if execErr != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("execute durable task %s: %w", task.ID, execErr)
			}
			next := w.cfg.Now().Add(time.Second)
			if err := w.store.Reschedule(ctx, task.ID, task.LeaseOwner, task.FencingToken, next, "executor_error"); err != nil && firstErr == nil {
				firstErr = fmt.Errorf("reschedule durable task %s: %w", task.ID, err)
			}
			continue
		}
		if result.Reschedule {
			if result.NextRetryAt.IsZero() {
				result.NextRetryAt = w.cfg.Now().Add(time.Second)
			}
			if err := w.store.Reschedule(ctx, task.ID, task.LeaseOwner, task.FencingToken, result.NextRetryAt, result.ReasonCode); err != nil && firstErr == nil {
				firstErr = fmt.Errorf("reschedule durable task %s: %w", task.ID, err)
			}
			continue
		}
		if err := w.store.CommitTerminal(ctx, task.ID, task.LeaseOwner, task.FencingToken, result.Terminal); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("commit durable task %s: %w", task.ID, err)
		}
	}
	return firstErr
}
