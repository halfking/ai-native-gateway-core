package pluginruntime

import (
	"context"
	"sync"
	"time"
)

// HealthCheckFunc checks one plugin's health. nil = healthy.
type HealthCheckFunc func(pluginID string) error

// HealthLoopConfig configures a HealthLoop.
type HealthLoopConfig struct {
	Interval         time.Duration // tick period
	FailureThreshold int           // consecutive failures before degraded
}

// HealthLoop periodically runs check against each registered plugin, updating
// the Registry status (ready/degraded). It does NOT auto-restart (P6 scope).
type HealthLoop struct {
	reg   *Registry
	check HealthCheckFunc
	cfg   HealthLoopConfig

	mu     sync.Mutex
	fail   map[string]int
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewHealthLoop constructs a HealthLoop. Zero-value config fields are replaced
// with sensible defaults (30s interval, 3 failures).
func NewHealthLoop(reg *Registry, check HealthCheckFunc, cfg HealthLoopConfig) *HealthLoop {
	if cfg.Interval == 0 {
		cfg.Interval = 30 * time.Second
	}
	if cfg.FailureThreshold == 0 {
		cfg.FailureThreshold = 3
	}
	return &HealthLoop{reg: reg, check: check, cfg: cfg, fail: map[string]int{}}
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
// degraded; a single success resets the counter and restores ready.
func (h *HealthLoop) tick(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	ids := h.pluginIDs()
	for _, id := range ids {
		if ctx.Err() != nil {
			return
		}
		err := h.check(id)
		h.mu.Lock()
		if err != nil {
			h.fail[id]++
			if h.fail[id] >= h.cfg.FailureThreshold {
				h.reg.SetPluginStatus(id, "degraded")
			}
		} else {
			// Recover: a successful check clears the failure count and, if the
			// plugin is not already ready, restores it. This also recovers
			// plugins marked degraded outside this loop.
			prev := h.fail[id]
			h.fail[id] = 0
			h.mu.Unlock()
			if prev > 0 || h.currentStatus(id) != "ready" {
				h.reg.SetPluginStatus(id, "ready")
			}
			continue
		}
		h.mu.Unlock()
	}
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
