// bg/active_probe_worker.go — error-triggered active probe worker.
//
// Listens for "consecutive failure" submissions from
// CredentialStateManager.UpdateOnFailure and runs a direct-to-provider
// HTTP probe on the offending (credential, model) pair. Probe results
// flow into request_logs (task_type='probe_triggered') so the realtime
// request stream shows every probe attempt in real time.
//
// Probe cycle per (credential, model):
//
//	attempt 1 → 0s  delay (run immediately on Submit)
//	attempt 2 → 5s  delay after attempt 1 failed
//	attempt 3 → 30s delay after attempt 2 failed
//	attempt 4 → 2m  delay after attempt 3 failed
//	attempt 5 → 5m  delay after attempt 4 failed (max-attempts reached)
//
// After the first successful probe OR after max_attempts failures the
// entry is removed from the dedup map and control returns to the
// passive_probe_listener / credential_recovery goroutines.
package bg

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/domains/credentialstate" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
	"github.com/kaixuan/llm-gateway-go/secret"
)

// ActiveProbeWorkerConfig holds runtime configuration for the worker.
type ActiveProbeWorkerConfig struct {
	DB                   *pgxpool.Pool
	Keyring              *secret.Keyring
	EncKey               []byte
	Telemetry            *telemetry.Client
	StateManager         credentialstate.StateObserver
	Enabled              bool
	ConsecutiveThreshold int // 2 by default; the value from settings
	MaxAttempts          int // 5 by default
	TimeoutMs            int // 10000 by default
	QueueSize            int // 128 by default
}

// ActiveProbeWorker is the singleton orchestrator.
type ActiveProbeWorker struct {
	cfg      ActiveProbeWorkerConfig
	executor *ActiveProbeExecutor
	emitter  *ActiveProbeEmitter

	queue   chan probeTask
	mu      sync.Mutex
	running map[string]*probeState
	stopped bool
	started bool

	cancel   context.CancelFunc
	done     chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

type probeTask struct {
	CredID    int
	Model     string
	TenantID  string
	RequestID string
}

type probeState struct {
	CredentialID  int
	Model         string
	TenantID      string
	ParentReqID   string
	Attempt       int
	NextRunAt     time.Time
	LastTriggerAt time.Time
}

// NewActiveProbeWorker constructs a worker. Safe to call with cfg.Enabled
// = false (Start becomes a no-op).
func NewActiveProbeWorker(cfg ActiveProbeWorkerConfig) *ActiveProbeWorker {
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 5
	}
	if cfg.ConsecutiveThreshold <= 0 {
		cfg.ConsecutiveThreshold = 2
	}
	if cfg.TimeoutMs <= 0 {
		cfg.TimeoutMs = 10000
	}
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = 128
	}
	w := &ActiveProbeWorker{
		cfg:      cfg,
		executor: NewActiveProbeExecutor(cfg.DB, cfg.Keyring, cfg.EncKey, cfg.TimeoutMs),
		emitter:  NewActiveProbeEmitter(cfg.Telemetry),
		queue:    make(chan probeTask, cfg.QueueSize),
		running:  make(map[string]*probeState, 64),
		done:     make(chan struct{}),
	}
	return w
}

// probeKey returns the dedup map key. Exported via lowercase to keep
// the implementation detail internal.
func probeKey(credID int, model string) string {
	return fmt.Sprintf("%d|%s", credID, model)
}

// Submit is the entry point used by CredentialStateManager.UpdateOnFailure.
// It registers the (credID, model) pair in the dedup map (if not already
// present) and enqueues a task for the worker goroutine.
//
// parentReqID is the request_id of the failed business request that
// triggered the probe. Stored so the probe row in request_logs can be
// correlated with the original failure via parent_request_id.
func (w *ActiveProbeWorker) Submit(credID int, model string, tenantID string, parentReqID string) {
	if w == nil || !w.cfg.Enabled {
		return
	}
	key := probeKey(credID, model)

	w.mu.Lock()
	if w.stopped {
		w.mu.Unlock()
		return
	}
	if _, exists := w.running[key]; exists {
		w.mu.Unlock()
		slog.Debug("active_probe: dedup hit, already running",
			"cred_id", credID, "model", model)
		return
	}
	w.running[key] = &probeState{
		CredentialID:  credID,
		Model:         model,
		TenantID:      tenantID,
		Attempt:       0,
		NextRunAt:     time.Now(), // first attempt runs immediately
		ParentReqID:   parentReqID,
		LastTriggerAt: time.Now(),
	}
	w.mu.Unlock()

	select {
	case w.queue <- probeTask{CredID: credID, Model: model, TenantID: tenantID, RequestID: parentReqID}:
		slog.Info("active_probe: submitted",
			"cred_id", credID,
			"model", model,
			"tenant_id", tenantID,
			"parent_req_id", parentReqID,
		)
	default:
		slog.Warn("active_probe: queue full, dropping",
			"cred_id", credID, "model", model)
		w.mu.Lock()
		delete(w.running, key)
		w.mu.Unlock()
	}
}

// SetStateManager wires the state observer after construction. The gateway
// builds the worker before the legacy credential-state manager is available.
func (w *ActiveProbeWorker) SetStateManager(observer credentialstate.StateObserver) {
	if w == nil {
		return
	}
	w.cfg.StateManager = observer
}

// Start spawns the worker goroutine. Idempotent on cfg.Enabled=false.
func (w *ActiveProbeWorker) Start(ctx context.Context) {
	if w == nil || !w.cfg.Enabled {
		if w != nil {
			slog.Info("active_probe: disabled, not starting")
		}
		return
	}
	w.mu.Lock()
	if w.stopped || w.started {
		w.mu.Unlock()
		return
	}
	w.started = true
	wctx, cancel := context.WithCancel(ctx)
	w.cancel = cancel
	w.wg.Add(1)
	w.mu.Unlock()
	go w.runLoop(wctx)
	slog.Info("active_probe worker started",
		"consecutive_threshold", w.cfg.ConsecutiveThreshold,
		"max_attempts", w.cfg.MaxAttempts,
		"timeout_ms", w.cfg.TimeoutMs,
		"queue_size", w.cfg.QueueSize,
	)
}

// Stop closes the queue, cancels the worker goroutine and waits for it
// to drain.
func (w *ActiveProbeWorker) Stop() {
	if w == nil {
		return
	}
	w.stopOnce.Do(func() {
		w.mu.Lock()
		w.stopped = true
		w.mu.Unlock()
		if w.cancel != nil {
			w.cancel()
		}
		w.wg.Wait()
		w.mu.Lock()
		clear(w.running)
		w.mu.Unlock()
		close(w.done)
	})
}

func (w *ActiveProbeWorker) runLoop(ctx context.Context) {
	defer w.wg.Done()
	defer func() {
		if ctx.Err() != nil {
			w.mu.Lock()
			w.stopped = true
			clear(w.running)
			w.mu.Unlock()
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case task, ok := <-w.queue:
			if !ok {
				return
			}
			w.processOne(ctx, task)
		}
	}
}

// processOne handles a single (credID, model) probe cycle: it pulls the
// latest state from the dedup map, waits for the backoff delay if the
// previous attempt just failed, runs the probe, then either:
//   - marks success (clears dedup, recovers routing via stateManager), or
//   - re-enqueues for the next backoff attempt, or
//   - marks failed_final after MaxAttempts.
func (w *ActiveProbeWorker) processOne(ctx context.Context, task probeTask) {
	key := probeKey(task.CredID, task.Model)

	w.mu.Lock()
	state, ok := w.running[key]
	if !ok {
		w.mu.Unlock()
		return
	}
	state.Attempt++
	attempt := state.Attempt
	w.mu.Unlock()

	// Wait until NextRunAt (implements the backoff between attempts).
	if !state.NextRunAt.IsZero() {
		now := time.Now()
		if state.NextRunAt.After(now) {
			wait := state.NextRunAt.Sub(now)
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
		}
	}

	// Re-check dedup: the state may have been cleared during the wait
	// (e.g. ctx canceled by Stop, or another path).
	w.mu.Lock()
	if _, ok := w.running[key]; !ok {
		w.mu.Unlock()
		return
	}
	w.mu.Unlock()

	slog.Info("active_probe: executing",
		"cred_id", task.CredID,
		"model", task.Model,
		"attempt", attempt,
		"max_attempts", w.cfg.MaxAttempts,
	)

	// 1. Load the probe target.
	target, err := w.executor.LoadTarget(ctx, task.CredID, task.Model)
	if err != nil {
		slog.Warn("active_probe: load target failed",
			"cred_id", task.CredID, "model", task.Model, "error", err)
		w.markFailedFinal(task.CredID, task.Model)
		return
	}

	// 2. Run the direct probe.
	result := w.executor.Run(ctx, target)
	result.Log(task.CredID, task.Model, attempt)

	// 3. Emit to request_logs (auto-pushed to live-stream SSE).
	w.emitter.Emit(ctx, target.CredentialID, target.ProviderID, state.TenantID,
		task.Model, target.OutboundModel, state.ParentReqID, attempt, result)

	// 4. Close the loop with the state manager.
	if result.Status == ProbeStatusSuccess {
		w.markSuccess(task.CredID, task.Model)
		if w.cfg.StateManager != nil {
			now := time.Now()
			w.cfg.StateManager.UpdateFromProbe(ctx, &credentialstate.State{
				CredentialID:  task.CredID,
				Model:         task.Model,
				Available:     true,
				LastSuccessAt: &now,
				Source:        "probe_direct",
			})
		}
		return
	}

	// Probe failed → cool the credential for 5 minutes + schedule next attempt.
	if w.cfg.StateManager != nil {
		recoverAt := time.Now().Add(5 * time.Minute)
		w.cfg.StateManager.UpdateFromProbe(ctx, &credentialstate.State{
			CredentialID: task.CredID,
			Model:        task.Model,
			Available:    false,
			LastError:    classifyProbeErrorKind(result),
			RecoverAt:    &recoverAt,
			Source:       "probe_direct",
		})
	}

	// 5. Decide whether to retry.
	if attempt >= w.cfg.MaxAttempts {
		w.markFailedFinal(task.CredID, task.Model)
		return
	}
	// 2026-07-13 (BUG #1 fix): the chain `DefaultErrorProbeBackoffChain =
	// [5s, 30s, 2m, 5m, 15m]` is the delay BEFORE the (attempt+1)-th
	// probe runs — i.e. the wait after the just-failed `attempt`-th
	// probe. Therefore the chain entry we want here is the one whose
	// 1-based index equals the just-failed `attempt`. That is simply
	// `computeBackoff(attempt)`.
	//
	// The previous code passed `attempt + 1`, which silently dropped
	// the 5s entry of the chain: after attempt 1 failed we waited 30s
	// (= chain[1]) instead of 5s (= chain[0]). The effective schedule
	// became [30s, 2m, 5m, 15m] (only 4 retries fired) and the
	// failed_final fallthrough landed at T+22m30s instead of the
	// documented T+7m35s.
	//
	// After this fix the schedule is exactly the documented
	// `[5s, 30s, 2m, 5m, 15m]` matching the header in
	// bg/active_probe_backoff.go and §3.2 of the design doc.
	backoff := computeBackoff(attempt)
	w.markFailedRetry(task.CredID, task.Model, attempt, time.Now().Add(backoff), backoff)
}

// markSuccess removes the entry from the dedup map. The credential is
// already updated by the caller via UpdateFromProbe.
func (w *ActiveProbeWorker) markSuccess(credID int, model string) {
	key := probeKey(credID, model)
	w.mu.Lock()
	delete(w.running, key)
	w.mu.Unlock()
	slog.Info("active_probe: success, dedup cleared",
		"cred_id", credID, "model", model)
}

// markFailedRetry re-enqueues the task for the next backoff attempt.
// On queue-full (shouldn't happen with QueueSize=128 but defensively)
// we drop into failed_final to avoid infinite blocking.
func (w *ActiveProbeWorker) markFailedRetry(credID int, model string, attempt int, nextRunAt time.Time, backoff time.Duration) {
	key := probeKey(credID, model)
	w.mu.Lock()
	if s, ok := w.running[key]; ok {
		s.Attempt = attempt
		s.NextRunAt = nextRunAt
	}
	w.mu.Unlock()

	select {
	case w.queue <- probeTask{CredID: credID, Model: model}:
		slog.Info("active_probe: failed, scheduled next attempt",
			"cred_id", credID,
			"model", model,
			"attempt", attempt,
			"next_attempt_in", backoff.String(),
		)
	default:
		slog.Warn("active_probe: re-enqueue full, terminating",
			"cred_id", credID, "model", model)
		w.markFailedFinal(credID, model)
	}
}

// markFailedFinal removes the dedup entry. After this the worker no
// longer owns the (credID, model) pair — passive_probe_listener /
// credential_recovery take over recovery duties.
func (w *ActiveProbeWorker) markFailedFinal(credID int, model string) {
	key := probeKey(credID, model)
	w.mu.Lock()
	delete(w.running, key)
	w.mu.Unlock()
	slog.Warn("active_probe: max attempts reached, terminating",
		"cred_id", credID, "model", model)
}

// RunningCount returns the number of (cred, model) pairs currently being
// probed. Used by admin status endpoints + tests.
func (w *ActiveProbeWorker) RunningCount() int {
	if w == nil {
		return 0
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.running)
}

// RunningKeys returns a snapshot of the dedup map keys. Used by tests.
func (w *ActiveProbeWorker) RunningKeys() []string {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	keys := make([]string, 0, len(w.running))
	for k := range w.running {
		keys = append(keys, k)
	}
	return keys
}

// Done returns a channel that is closed when Stop() finishes.
func (w *ActiveProbeWorker) Done() <-chan struct{} {
	if w == nil {
		ch := make(chan struct{})
		close(ch)
		return ch
	}
	return w.done
}
