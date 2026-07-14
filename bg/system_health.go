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
// of request_logs_hot and returns:
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
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// SystemHealthInterval is the 30s tick mandated by the spec.
const SystemHealthInterval = 30 * time.Second

// SystemHealthStatus is the value type served by /api/health/system.
type SystemHealthStatus struct {
	Status       string    `json:"status"`
	SuccessRate  float64   `json:"success_rate"`
	SampleCount  int64     `json:"sample_count"`
	FailureCount int64     `json:"failure_count"`
	LastCheckAt  time.Time `json:"last_check_at"`
}

// SystemHealthWorker runs the 30s system-health scan.
type SystemHealthWorker struct {
	pool      *pgxpool.Pool
	interval  time.Duration
	lastStatus atomic.Value // SystemHealthStatus
}

// NewSystemHealthWorker constructs a worker.  Pass nil to disable
// the worker; the badge will display "suspect" and
// /api/health/system will return 503.
func NewSystemHealthWorker(pool *pgxpool.Pool) *SystemHealthWorker {
	w := &SystemHealthWorker{
		pool:     pool,
		interval: SystemHealthInterval,
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
		status       string
		successRate  float64
		sampleCount  int64
		failureCount int64
		lastCheck    time.Time
	)
	if err := row.Scan(&status, &successRate, &sampleCount, &failureCount, &lastCheck); err != nil {
		slog.Warn("system_health_worker: query failed", "error", err)
		return
	}
	w.lastStatus.Store(SystemHealthStatus{
		Status:       status,
		SuccessRate:  successRate,
		SampleCount:  sampleCount,
		FailureCount: failureCount,
		LastCheckAt:  lastCheck,
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
