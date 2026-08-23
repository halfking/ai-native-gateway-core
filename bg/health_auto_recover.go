package bg

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/kaixuan/llm-gateway-go/credentialhealth"
	met "github.com/kaixuan/llm-gateway-go/metrics" //nolint:depguard // routing credential observability (2026-08-23 hzx-2 audit)
)

// HealthAutoRecover checks for credentials with expired availability_recover_at
// and restores them to 'ready' state.
type HealthAutoRecover struct {
	db       credentialhealth.DBQuerier
	interval time.Duration // default 1 minute
	stopCh   chan struct{}

	// 2026-07-27 concurrency fix: Stop() did an unguarded close(stopCh),
	// so a second Stop() panicked with "close of closed channel".
	// Same guard as bg/routing_health_checker.go.
	stopOnce sync.Once

	// 2026-08-17 P0 fix: tickInterval is the live, possibly-updated interval.
	// 0 means "disabled" (recover() is skipped). guarded by tickMu so
	// cmd/gateway/main.go can install an env-driven override
	// (LLM_GATEWAY_AUTO_RECOVER_INTERVAL_SECONDS) at any point in the worker
	// lifecycle. tickIntervalEverSet distinguishes "no override yet, use the
	// boot-time default" from "operator just disabled us with SetTickInterval(0)".
	tickMu              sync.RWMutex
	tickInterval        time.Duration
	tickIntervalEverSet bool
}

// disabledProbeInterval is the heart-beat used while the loop is in the
// "disabled" state. We can't simply stop the ticker because then the loop
// couldn't notice when an operator sets the interval back to a positive
// value — long heart-beat (10 minutes) just to re-check the configured
// interval. The recover() body is skipped while disabled, so the loop is
// effectively idle.
const healthAutoRecoverDisabledProbeInterval = 10 * time.Minute

// NewHealthAutoRecover creates a recovery worker.
func NewHealthAutoRecover(
	db credentialhealth.DBQuerier,
	interval time.Duration,
) *HealthAutoRecover {
	if interval == 0 {
		interval = 1 * time.Minute
	}

	return &HealthAutoRecover{
		db:           db,
		interval:     interval,
		tickInterval: interval,
		stopCh:       make(chan struct{}),
	}
}

// SetTickInterval changes the recovery loop period at runtime.
//
// Semantics:
//   - d < 0: ignored (defensive; env parsing may yield -1).
//   - d == 0: the loop enters a disabled state — recover() is not called
//     until a subsequent SetTickInterval(<positive>) re-arms it.
//   - 0 < d < 1s: clamped to 1s.
//   - d >= 1s: used as-is.
//
// The change is picked up at the next ticker boundary.
func (w *HealthAutoRecover) SetTickInterval(d time.Duration) {
	if d < 0 {
		return
	}
	w.tickMu.Lock()
	w.tickIntervalEverSet = true
	if d == 0 {
		w.tickInterval = 0
	} else {
		if d < time.Second {
			d = time.Second
		}
		w.tickInterval = d
	}
	w.tickMu.Unlock()
}

func (w *HealthAutoRecover) currentInterval() time.Duration {
	w.tickMu.RLock()
	defer w.tickMu.RUnlock()
	// "no override yet" → boot-time default; "disabled" → 0.
	if !w.tickIntervalEverSet {
		return w.interval
	}
	return w.tickInterval
}

// Start begins the recovery loop.
func (w *HealthAutoRecover) Start(ctx context.Context) {
	slog.Info("health_auto_recover started", "interval", w.currentInterval().String())

	go func() {
		// resolveInterval maps the configured value to the ticker period we
		// actually use. 0 (disabled) → disabledProbeInterval so the loop can
		// still notice a subsequent SetTickInterval(<positive>). Any other
		// value is used as-is.
		resolveInterval := func() (period time.Duration, enabled bool) {
			v := w.currentInterval()
			if v == 0 {
				return healthAutoRecoverDisabledProbeInterval, false
			}
			return v, true
		}

		period, _ := resolveInterval()
		ticker := time.NewTicker(period)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				slog.Info("health_auto_recover stopping")
				return
			case <-w.stopCh:
				slog.Info("health_auto_recover stopped")
				return
			case <-ticker.C:
				newPeriod, newEnabled := resolveInterval()
				if newPeriod != period {
					period = newPeriod
					ticker.Reset(period)
				}
				if !newEnabled {
					// Disabled: heart-beat only. Don't call recover().
					continue
				}
				if err := w.recover(ctx); err != nil {
					slog.Error("health_auto_recover failed", "error", err)
				}
			}
		}
	}()
}

// Stop gracefully stops the worker. Idempotent; safe on a
// never-Started worker.
func (w *HealthAutoRecover) Stop() {
	w.stopOnce.Do(func() { close(w.stopCh) })
}

// recover restores expired credentials to 'ready' state.
func (w *HealthAutoRecover) recover(ctx context.Context) error {
	// 2026-08-23 (hzx-2 audit): record tick duration + outcome so operators
	// can spot silent slowdowns (DB pool exhaustion) and distinguish
	// successful recoveries from "nothing to do" or "errored out".
	start := time.Now()
	defer func() {
		met.RoutingHealthAutoRecoverTickDurationSeconds.Set(time.Since(start).Seconds())
	}()

	count, err := credentialhealth.RecoverExpired(ctx, w.db)
	if err != nil {
		met.RoutingHealthAutoRecoverTotal.WithLabelValues("error").Inc()
		return err
	}

	if count > 0 {
		slog.Info("health_auto_recover: recovered credentials", "count", count)
		met.RoutingHealthAutoRecoverTotal.WithLabelValues("recovered").Inc()
	} else {
		met.RoutingHealthAutoRecoverTotal.WithLabelValues("no_row").Inc()
	}

	return nil
}
