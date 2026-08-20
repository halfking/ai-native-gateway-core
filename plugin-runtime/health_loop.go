package pluginruntime

import (
	"context"
	"sync"
	"time"
)

// HealthCheckFunc checks one plugin's health. nil = healthy.
type HealthCheckFunc func(pluginID string) error

// Restarter attempts to restart a plugin (e.g. supervisor.Restart). nil error
// means the restart was attempted; the health loop will re-check on the next
// tick to confirm recovery.
type Restarter func(pluginID string) error

// HealthLoopConfig configures a HealthLoop.
type HealthLoopConfig struct {
	Interval         time.Duration // tick period
	FailureThreshold int           // consecutive failures before degraded
	// P7: auto-restart on degraded.
	Restarter    Restarter     // nil = no auto-restart (P6 behavior)
	MaxRestarts  int           // after this many restart attempts, mark failed (0 = unlimited)
	BackoffStart time.Duration // initial backoff before the first restart attempt
	BackoffMax   time.Duration // backoff cap; further attempts stay at this value
}

// HealthLoop periodically runs check against each registered plugin, updating
// the Registry status (ready/degraded). When a Restarter is configured (P7),
// degraded plugins are auto-restarted with exponential backoff, up to
// MaxRestarts attempts, after which the plugin is marked failed (terminal).
type HealthLoop struct {
	reg   *Registry
	check HealthCheckFunc
	cfg   HealthLoopConfig

	mu              sync.Mutex
	fail            map[string]int       // consecutive failures per plugin
	restartAttempts map[string]int       // P7: restart attempts per plugin
	lastRestart     map[string]time.Time // P7: time of last restart attempt per plugin
	cancel          context.CancelFunc
	wg              sync.WaitGroup
}

// NewHealthLoop constructs a HealthLoop. Zero-value config fields are replaced
// with sensible defaults (30s interval, 3 failures). Restart-related fields
// default to "no restart" (P6 behavior) when Restarter is nil.
func NewHealthLoop(reg *Registry, check HealthCheckFunc, cfg HealthLoopConfig) *HealthLoop {
	if cfg.Interval == 0 {
		cfg.Interval = 30 * time.Second
	}
	if cfg.FailureThreshold == 0 {
		cfg.FailureThreshold = 3
	}
	return &HealthLoop{
		reg:             reg,
		check:           check,
		cfg:             cfg,
		fail:            map[string]int{},
		restartAttempts: map[string]int{},
		lastRestart:     map[string]time.Time{},
	}
}

// Start launches the background loop. It runs one tick immediately, then ticks
// on Interval. Calling Start more than once is not supported.
func (h *HealthLoop) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	h.mu.Lock()
	h.cancel = cancel
	h.mu.Unlock()
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		ticker := time.NewTicker(h.cfg.Interval)
		defer ticker.Stop()
		h.tick(ctx) // run once immediately
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				h.tick(ctx)
			}
		}
	}()
}

// tick runs the health check against every registered plugin and updates the
// registry status. Consecutive failures past FailureThreshold mark the plugin
// degraded (and, when a Restarter is configured, trigger an auto-restart with
// backoff); a single success resets the failure and restart counters and
// restores ready.
func (h *HealthLoop) tick(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	ids := h.pluginIDs()
	for _, id := range ids {
		if ctx.Err() != nil {
			return
		}
		status := h.currentStatus(id)
		if status != "ready" && status != "degraded" {
			continue
		}
		err := h.check(id)
		h.mu.Lock()
		if err != nil {
			h.fail[id]++
			if h.fail[id] >= h.cfg.FailureThreshold {
				h.reg.SetPluginStatus(id, "degraded")
				h.tryRestart(id)
			}
		} else {
			// Recover: a successful check clears the failure count and, if the
			// plugin is not already ready, restores it. This also recovers
			// plugins marked degraded/failed outside this loop. Both counters
			// (failure and restart attempts) are reset so a subsequent
			// degradation starts fresh.
			prevFail := h.fail[id]
			prevRestarts := h.restartAttempts[id]
			h.fail[id] = 0
			h.restartAttempts[id] = 0
			h.mu.Unlock()
			if status == "degraded" || prevFail > 0 || prevRestarts > 0 {
				h.reg.SetPluginStatus(id, "ready")
			}
			continue
		}
		h.mu.Unlock()
	}
}

// tryRestart is invoked by tick when a plugin reaches the degraded threshold.
// It is called while h.mu is held and does NOT acquire its own lock.
//
// Behavior:
//   - No Restarter configured: do nothing (P6 behavior — degraded only).
//   - MaxRestarts reached: mark the plugin failed (terminal, no more retries).
//   - Otherwise, if the backoff window since the last restart attempt has
//     elapsed, call the Restarter and record the attempt.
//
// The Restarter is invoked synchronously under h.mu; it should be fast
// (sup.Restart performs an exec). P8 may release the lock and run async.
func (h *HealthLoop) tryRestart(id string) {
	if h.cfg.Restarter == nil {
		return // P6 behavior: mark degraded only
	}
	if h.cfg.MaxRestarts > 0 && h.restartAttempts[id] >= h.cfg.MaxRestarts {
		h.reg.SetPluginStatus(id, "failed") // terminal, no more retries
		return
	}
	backoff := h.backoffFor(h.restartAttempts[id])
	if last, ok := h.lastRestart[id]; ok && time.Since(last) < backoff {
		return // too soon, skip this tick
	}
	h.restartAttempts[id]++
	h.lastRestart[id] = time.Now()
	_ = h.cfg.Restarter(id)
}

// backoffFor returns the backoff duration before the (n+1)-th restart attempt.
// With BackoffStart = b and BackoffMax = m, the sequence is b, 2b, 4b, ...,
// capped at m. A zero BackoffStart means no backoff.
func (h *HealthLoop) backoffFor(n int) time.Duration {
	b := h.cfg.BackoffStart
	if b <= 0 {
		return 0
	}
	max := h.cfg.BackoffMax
	for i := 0; i < n; i++ {
		b *= 2
		if max > 0 && b > max {
			b = max
			break
		}
	}
	return b
}

// pluginIDs returns a snapshot of the currently registered plugin IDs.
func (h *HealthLoop) pluginIDs() []string {
	h.reg.mu.RLock()
	defer h.reg.mu.RUnlock()
	ids := make([]string, 0, len(h.reg.plugins))
	for id := range h.reg.plugins {
		ids = append(ids, id)
	}
	return ids
}

// currentStatus returns the plugin's current Status, or "" if unregistered.
func (h *HealthLoop) currentStatus(pluginID string) string {
	h.reg.mu.RLock()
	defer h.reg.mu.RUnlock()
	if st, ok := h.reg.plugins[pluginID]; ok {
		return st.Status
	}
	return ""
}

// Stop signals the loop to exit and blocks until the goroutine has returned.
// Safe to call multiple times.
func (h *HealthLoop) Stop() {
	h.mu.Lock()
	c := h.cancel
	h.cancel = nil
	h.mu.Unlock()
	if c != nil {
		c()
	}
	h.wg.Wait()
}
