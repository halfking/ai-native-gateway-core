// Package bg — system_health.go
//
// SystemHealthWorker is the new 30s system-level health monitor mandated
// by the 2026-07-14 spec rewrite.  It supersedes the
// request-logs-scanning behavior of bg/passive_probe_listener.go (which
// is now gated behind LLM_GATEWAY_USE_NEW_PROBE_MODE).
//
// What it measures
// ────────────────
// Every 30s the worker calls system_health_status() (SQL helper
// introduced in migration 341) which reads the most recent 30s window
// of _request_logs_hot and returns:
//
//	status         = "ok"        (success rate >= 80%,  green)
//	                | "degraded" (< 80%,                red)
//	                | "suspect"  (0 requests,           grey)
//	success_rate   = 0.0 .. 1.0
//	sample_count   = rows in window
//	failure_count  = rows where success = false
//	last_check_at  = server time at query
//
// The result is held in atomic.Value so /api/health/system can serve
// it without DB hits, and the LiveStreamSSEHub re-broadcasts the new
// status to any browser connected to /api/health/system/stream (the
// GDRT H badge on the homepage listens on this stream).
package bg

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/domains/credential"
)

// SystemHealthInterval is the 30s tick mandated by the spec.
const SystemHealthInterval = 30 * time.Second

// SystemHealthStatus is the value type served by /api/health/system.
type SystemHealthStatus struct {
	Status         string                 `json:"status"`
	SuccessRate    float64                `json:"success_rate"`
	SampleCount    int64                  `json:"sample_count"`
	FailureCount   int64                  `json:"failure_count"`
	LastCheckAt    time.Time              `json:"last_check_at"`
	CircuitBreaker *CircuitBreakerSummary `json:"circuit_breaker,omitempty"` // W1-G4 (2026-11-19)
}

// CircuitBreakerSummary is the circuit breaker state summary (W1-G4, 2026-11-19).
// It aggregates the current state of all circuit breakers in the gateway.
type CircuitBreakerSummary struct {
	OpenCount        int       `json:"open_count"`        // Number of breakers in OPEN state
	HalfOpenCount    int       `json:"half_open_count"`   // Number of breakers in HALF_OPEN state
	QuarantinedCount int       `json:"quarantined_count"` // Number of breakers in QUARANTINED state
	TotalBreakers    int       `json:"total_breakers"`    // Total number of circuit breakers
	LastTransition   time.Time `json:"last_transition,omitempty"` // Most recent state transition (if any)
}

// SystemHealthWorker runs the 30s system-health scan.
type SystemHealthWorker struct {
	pool           *pgxpool.Pool
	circuitManager *credential.Manager // W1-G4: circuit breaker manager for state aggregation
	interval       time.Duration
	lastStatus     atomic.Value // SystemHealthStatus

	stopCh   chan struct{}
	stopOnce sync.Once
}

// NewSystemHealthWorker constructs a worker.  Pass nil pool to disable
// the worker; the badge will display "suspect" and /api/health/system
// will return 503. Pass nil circuitManager to omit circuit breaker
// summary from the health response (W1-G4).
func NewSystemHealthWorker(pool *pgxpool.Pool, cm *credential.Manager) *SystemHealthWorker {
	w := &SystemHealthWorker{
		pool:           pool,
		circuitManager: cm,
		interval:       SystemHealthInterval,
		stopCh:         make(chan struct{}),
	}
	w.lastStatus.Store(SystemHealthStatus{Status: "suspect"})
	return w
}

// Start launches the worker goroutine.  No-op when pool is nil.
func (w *SystemHealthWorker) Start(ctx context.Context) {
	if w == nil || w.pool == nil {
		return
	}
	go w.loop(ctx)
	slog.Info("system_health_worker started", "interval", w.interval)
}

// Stop requests termination. It is safe to call repeatedly, including before Start.
func (w *SystemHealthWorker) Stop() {
	if w == nil {
		return
	}
	w.stopOnce.Do(func() { close(w.stopCh) })
}

func (w *SystemHealthWorker) loop(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("system_health_worker panic", "recover", r)
		}
	}()
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	w.tick(ctx) // run once immediately so the badge has data early
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.stopCh:
			return
		case <-ticker.C:
			w.tick(ctx)
		}
	}
}

func (w *SystemHealthWorker) tick(ctx context.Context) {
	if w.pool == nil {
		return
	}
	queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	row := w.pool.QueryRow(queryCtx,
		`SELECT status, success_rate, sample_count, failure_count, last_check_at
		   FROM system_health_status(30)`)
	var (
		status                    string
		successRate               *float64
		sampleCount, failureCount int64
		lastCheck                 time.Time
	)
	if err := row.Scan(&status, &successRate, &sampleCount, &failureCount, &lastCheck); err != nil {
		slog.Warn("system_health_worker: query failed", "error", err)
		return
	}
	sr := 0.0
	if successRate != nil {
		sr = *successRate
	}
	
	// W1-G4 (2026-11-19): Aggregate circuit breaker state
	var cbSummary *CircuitBreakerSummary
	if w.circuitManager != nil {
		cbSummary = aggregateCircuitStats(w.circuitManager.Stats())
	}
	
	w.lastStatus.Store(SystemHealthStatus{
		Status:         status,
		SuccessRate:    sr,
		SampleCount:    sampleCount,
		FailureCount:   failureCount,
		LastCheckAt:    lastCheck,
		CircuitBreaker: cbSummary,
	})
}

// Last returns the most recently observed health status.
func (w *SystemHealthWorker) Last() SystemHealthStatus {
	if w == nil {
		return SystemHealthStatus{Status: "suspect"}
	}
	v, _ := w.lastStatus.Load().(SystemHealthStatus)
	if v.Status == "" {
		return SystemHealthStatus{Status: "suspect"}
	}
	return v
}

// aggregateCircuitStats aggregates circuit breaker statistics from Manager.Stats()
// into a summary suitable for the health endpoint (W1-G4, 2026-11-19).
//
// Returns nil if stats is empty (no breakers exist).
func aggregateCircuitStats(stats []map[string]any) *CircuitBreakerSummary {
	if len(stats) == 0 {
		return nil
	}
	
	summary := &CircuitBreakerSummary{
		TotalBreakers: len(stats),
	}
	
	var lastTransition time.Time
	for _, stat := range stats {
		// State is a string: "closed", "open", "half_open", "quarantined"
		state, _ := stat["state"].(string)
		switch state {
		case "open":
			summary.OpenCount++
		case "half_open":
			summary.HalfOpenCount++
		case "quarantined":
			summary.QuarantinedCount++
		}
		
		// Find the most recent state transition across all breakers
		if t, ok := stat["last_transition"].(time.Time); ok && t.After(lastTransition) {
			lastTransition = t
		}
	}
	
	if !lastTransition.IsZero() {
		summary.LastTransition = lastTransition
	}
	
	return summary
}
