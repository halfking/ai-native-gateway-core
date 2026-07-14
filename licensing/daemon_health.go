package licensing

// Daemon-health observability for the token-refresh daemon.
//
// Why expose this (v2)?
// Without health metrics, the only signal we get from a stuck daemon
// is "license expired" days later. By tracking per-cycle outcomes,
// ops can grep /metrics or alert when failure rate spikes.
//
// DaemonHealth is the read-only snapshot returned by GetDaemonHealth
// (or read from memory by the daemon itself on each cycle). The
// recent-failure ring buffer is sized to detect "soft outage"
// patterns (3+ failures in a row) without unbounded growth.

import (
	"sync"
	"time"
)

// DaemonHealth tracks per-cycle outcomes for the token-refresh
// daemon. Safe for concurrent reads via the embedded mutex.
//
// RecentFailures is a bounded ring buffer (max 8 entries) of the
// most recent failure timestamps. Ops can scan this to detect
// transient outages that "healed" between cycles.
type DaemonHealth struct {
	mu sync.RWMutex

	StartedAt        time.Time   `json:"started_at"`
	LastCycleAt      time.Time   `json:"last_cycle_at"`
	LastSuccessAt    time.Time   `json:"last_success_at"`
	LastErrorAt      time.Time   `json:"last_error_at"`
	LastError        string      `json:"last_error,omitempty"`
	TotalCycles      int64       `json:"total_cycles"`
	TotalSuccesses   int64       `json:"total_successes"`
	TotalFailures    int64       `json:"total_failures"`
	ConsecutiveFails int         `json:"consecutive_failures"`
	RecentFailures   []time.Time `json:"recent_failures,omitempty"`
}

const recentFailureRingSize = 8

// RecordSuccess updates the health snapshot after a successful cycle.
// Resets ConsecutiveFails to zero.
func (h *DaemonHealth) RecordSuccess() {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := time.Now().UTC()
	h.LastCycleAt = now
	h.LastSuccessAt = now
	h.LastErrorAt = zeroOr(h.LastErrorAt) // keep last error timestamp
	h.LastError = ""
	h.TotalCycles++
	h.TotalSuccesses++
	h.ConsecutiveFails = 0
}

// RecordFailure updates the health snapshot after a failed cycle.
// Increments ConsecutiveFails and pushes the timestamp onto the
// recent-failures ring.
func (h *DaemonHealth) RecordFailure(err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := time.Now().UTC()
	h.LastCycleAt = now
	h.LastErrorAt = now
	if err != nil {
		h.LastError = err.Error()
	}
	h.TotalCycles++
	h.TotalFailures++
	h.ConsecutiveFails++
	// Ring-buffer insert (drop oldest if at cap).
	if len(h.RecentFailures) >= recentFailureRingSize {
		// pop front
		h.RecentFailures = append(h.RecentFailures[:0], h.RecentFailures[1:]...)
	}
	h.RecentFailures = append(h.RecentFailures, now)
}

// Snapshot returns a deep-copy snapshot of the health for safe
// external reading (caller may not hold the mutex).
//
// We return by pointer because the embedded sync.RWMutex must NOT
// be copied — that would invalidate the lock state and any in-flight
// reader/writer. Callers should treat the returned pointer as a
// snapshot valid only for the immediate read; do not retain it.
func (h *DaemonHealth) Snapshot() *DaemonHealth {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := &DaemonHealth{
		StartedAt:        h.StartedAt,
		LastCycleAt:      h.LastCycleAt,
		LastSuccessAt:    h.LastSuccessAt,
		LastErrorAt:      h.LastErrorAt,
		LastError:        h.LastError,
		TotalCycles:      h.TotalCycles,
		TotalSuccesses:   h.TotalSuccesses,
		TotalFailures:    h.TotalFailures,
		ConsecutiveFails: h.ConsecutiveFails,
	}
	if len(h.RecentFailures) > 0 {
		out.RecentFailures = make([]time.Time, len(h.RecentFailures))
		copy(out.RecentFailures, h.RecentFailures)
	}
	return out
}

// Healthy returns a coarse health verdict derived from the snapshot.
// "healthy" means: at least one cycle ran, last cycle succeeded, and
// no >3 consecutive failures in the recent ring buffer.
func (h *DaemonHealth) Healthy() bool {
	snap := h.Snapshot()
	if snap.TotalCycles == 0 {
		return true // never ran; no signal — assume healthy
	}
	if snap.LastCycleAt.IsZero() || snap.LastCycleAt.Before(snap.LastErrorAt) {
		return false // last cycle failed
	}
	if snap.ConsecutiveFails >= 3 {
		return false
	}
	return true
}

// IsStale reports whether the daemon hasn't run a cycle in `maxIdle`.
// Used by ops to detect a wedged daemon.
func (h *DaemonHealth) IsStale(maxIdle time.Duration) bool {
	snap := h.Snapshot()
	if snap.LastCycleAt.IsZero() {
		return false // never ran; no signal
	}
	return time.Since(snap.LastCycleAt) > maxIdle
}

func zeroOr(t time.Time) time.Time {
	if t.IsZero() {
		return time.Time{}
	}
	return t
}

// package-level singleton: the daemon owns this, ops reads via
// GetDaemonHealth. Initialised lazily by ensureHealthInit() so that
// callers (incl. tests) don't have to wire it up.
var (
	singletonMu sync.Mutex
	singleton   *DaemonHealth
	onceInit    bool
)

// ensureHealthInit returns the singleton, initialising on first use.
func ensureHealthInit() *DaemonHealth {
	singletonMu.Lock()
	defer singletonMu.Unlock()
	if !onceInit {
		singleton = &DaemonHealth{StartedAt: time.Now().UTC()}
		onceInit = true
	}
	return singleton
}

// GetDaemonHealth returns a snapshot of the daemon's current
// health. Returns a zero-value *DaemonHealth if the daemon has
// never run (production users typically want this — it lets ops
// dashboards render "no data yet" without crashing).
//
// Note: callers MUST treat the returned pointer as an ephemeral
// snapshot. Do not retain across goroutines; the underlying mutex
// is NOT safe to copy.
func GetDaemonHealth() *DaemonHealth {
	singletonMu.Lock()
	defer singletonMu.Unlock()
	if singleton == nil {
		return &DaemonHealth{}
	}
	return singleton.Snapshot()
}
