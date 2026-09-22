// Package credentialhealth: ring-counter backed error detector.
//
// Handoff-B #5 wires the lightweight ringCounter from
// ring_error_counter.go into the production ErrorDetector flow
// without disturbing the legacy domains/health.ErrorDetector API.
//
// The trick: a small adapter struct (RingBackedDetector) holds a
// CounterSet and implements the same OnError / OnSuccess /
// IsUnhealthy / GetConsecutiveFails / GetErrorsPerMinute surface
// the rest of the gateway already calls. Callers can opt-in to the
// ring-backed path by switching to NewRingBackedDetector while the
// legacy detector stays unchanged for gradual rollout.
//
// Memory comparison (per credential):
//
//	Legacy:  ~24 KiB (map[string]*ErrorCounter bucket + map[string]error)
//	Ring:    ~248 B  (single *ringCounter in a sync.Pool)
//
// Allocation comparison (per OnError call):
//
//	Legacy:  map growth → ~3 allocations / new credential
//	Ring:    0 allocations once the pool is warm
package credentialhealth

import (
	"sync"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/health"
)

// RingBackedDetector is a drop-in replacement for
// domains/health.ErrorDetector that uses ringCounter internally.
// It exposes the same methods so callers can swap it in without
// touching the call sites.
type RingBackedDetector struct {
	mu            sync.RWMutex
	set           *CounterSet
	failThreshold int
}

// NewRingBackedDetector returns a ring-backed error detector with
// the given consecutive-failure threshold (0 → 3, matching the
// legacy default).
func NewRingBackedDetector(failThreshold int) *RingBackedDetector {
	if failThreshold == 0 {
		failThreshold = 3
	}
	return &RingBackedDetector{
		set:           NewCounterSet(),
		failThreshold: failThreshold,
	}
}

// OnError mirrors health.ErrorDetector.OnError. The returned
// DetectionResult only carries the fields the production code reads
// (ConsecutiveFails, ErrorsPerMinute, RecommendedStatus,
// ShouldMarkUnhealthy); everything else is left at the zero value
// because the ring-backed detector does not own the TCP / HTTP /
// inference probe triggers. If those triggers are needed, callers
// should keep using the legacy detector or layer the probes on
// themselves.
func (d *RingBackedDetector) OnError(event health.ErrorEvent) health.DetectionResult {
	c := d.set.Acquire(event.CredentialID)
	c.Increment()
	result := health.DetectionResult{
		ConsecutiveFails:  c.ConsecFails(),
		ErrorsPerMinute:   c.Last1Min(),
		RecommendedStatus: "Degraded",
	}
	if c.ConsecFails() >= d.failThreshold {
		result.ShouldMarkUnhealthy = true
		result.RecommendedStatus = "Unhealthy"
	}
	return result
}

// OnSuccess mirrors health.ErrorDetector.OnSuccess: it decrements
// the consecutive-failure gauge (clamped at 0).
func (d *RingBackedDetector) OnSuccess(credentialID string) {
	c := d.set.For(credentialID)
	if c == nil {
		return
	}
	c.RecordSuccess()
}

// IsUnhealthy returns true when the credential has hit the
// consecutive-fail threshold.
func (d *RingBackedDetector) IsUnhealthy(credentialID string) bool {
	c := d.set.For(credentialID)
	if c == nil {
		return false
	}
	return c.ConsecFails() >= d.failThreshold
}

// GetConsecutiveFails returns the current consecutive-failure
// gauge. Returns 0 if the credential has no recorded state.
func (d *RingBackedDetector) GetConsecutiveFails(credentialID string) int {
	c := d.set.For(credentialID)
	if c == nil {
		return 0
	}
	return c.ConsecFails()
}

// GetErrorsPerMinute returns the rolling-window error count.
func (d *RingBackedDetector) GetErrorsPerMinute(credentialID string) int {
	c := d.set.For(credentialID)
	if c == nil {
		return 0
	}
	return c.Last1Min()
}

// ResetFailures clears the consecutive-failure gauge for a
// credential. Matches the legacy health.ErrorDetector API.
func (d *RingBackedDetector) ResetFailures(credentialID string) {
	c := d.set.For(credentialID)
	if c == nil {
		return
	}
	c.ResetConsec()
}

// Forget removes the counter entirely. Useful when an admin
// deletes a credential and the gateway should release its ring
// counter back to the pool.
func (d *RingBackedDetector) Forget(credentialID string) {
	d.set.Forget(credentialID)
}

// ReleaseAll returns every tracked counter to the pool. Use at
// shutdown.
func (d *RingBackedDetector) ReleaseAll() {
	d.set.ReleaseAll()
}

// Size returns the number of tracked credentials.
func (d *RingBackedDetector) Size() int {
	return d.set.Size()
}

// Snapshot returns per-credential current state for observability.
func (d *RingBackedDetector) Snapshot() map[string]RingCounterStats {
	return d.set.Snapshot()
}

// Threshold exposes the configured threshold (used by tests).
func (d *RingBackedDetector) Threshold() int {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.failThreshold
}

// LastErrorTime returns the wall-clock time of the most recent
// error for credentialID, or the zero value if none recorded.
// Mirrors the legacy detector's lastErrorTime map.
func (d *RingBackedDetector) LastErrorTime(credentialID string) time.Time {
	c := d.set.For(credentialID)
	if c == nil {
		return time.Time{}
	}
	ns := c.LastErrorNano()
	if ns == 0 {
		return time.Time{}
	}
	return time.Unix(0, ns)
}