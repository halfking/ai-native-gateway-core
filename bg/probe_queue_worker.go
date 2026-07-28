package bg

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

type ProbeQueueWorkerConfig struct {
	Queue        *ProbeQueue
	Executor     *ActiveProbeExecutor
	Emitter      *ActiveProbeEmitter
	BatchSize    int
	Workers      int
	Lease        time.Duration
	PollInterval time.Duration
}

// ProbeQueueWorker consumes durable tasks and delegates every probe to the
// same ActiveProbeExecutor used by the legacy active probe path.
type ProbeQueueWorker struct {
	cfg    ProbeQueueWorkerConfig
	cancel context.CancelFunc
	wg     sync.WaitGroup
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
		cfg.Lease = 30 * time.Second
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
		go w.run(ctx)
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
	ticker := time.NewTicker(w.cfg.PollInterval)
	defer ticker.Stop()
	for {
		if err := w.processBatch(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("probe queue worker batch failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (w *ProbeQueueWorker) processBatch(ctx context.Context) error {
	if _, err := w.cfg.Queue.RequeueExpiredLeases(ctx); err != nil {
		return err
	}
	tasks, err := w.cfg.Queue.Claim(ctx, 1, w.cfg.Lease)
	if err != nil {
		return err
	}
	for _, task := range tasks {
		w.processTask(ctx, task)
	}
	return nil
}

func (w *ProbeQueueWorker) processTask(ctx context.Context, task ProbeQueueTask) {
	target, err := w.cfg.Executor.LoadTarget(ctx, int(task.CredentialID), task.RawModel)
	if err != nil {
		w.completeFailure(ctx, task, "load_target", err.Error(), 0, 0, "")
		return
	}
	result := w.cfg.Executor.RunCommand(ctx, ProbeCommand{
		Target: target, Mode: probeMode(task.Mode), Attempt: task.Attempt,
		Origin: task.Source, ParentID: task.ParentReqID,
	})
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
		return
	}
	w.completeFailure(ctx, task, result.ErrCode, result.ErrMsg, result.HTTPStatus, result.LatencyMs, result.RespPreview)
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

func (w *ProbeQueueWorker) complete(ctx context.Context, task ProbeQueueTask, result ProbeQueueResult) {
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
