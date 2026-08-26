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
}

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
	w.maybeReviveExpiredReady(ctx)
	tasks, err := w.cfg.Queue.Claim(ctx, 1, w.cfg.Lease)
	if err != nil {
		return err
	}
	for _, task := range tasks {
		w.processTask(ctx, task)
	}
	return nil
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
			if errors.Is(err, ErrProbeOutOfScope) {
				slog.Info("probe_service skipped out-of-scope task", "queue_id", task.ID)
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
