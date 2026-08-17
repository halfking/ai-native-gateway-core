// Package bg — integrity_probe_planner.go
//
// 2026-07-28: durable integrity probe producer.
//
// Closes the audit gap where credential_probe_queue has a consumer but
// no real Enqueue caller. The planner scans model_integrity_events for
// unresolved `model_mismatch` and `fingerprint_drift` events and, per
// (cred, model), schedules a single integrity probe task with a long
// dedup TTL so we don't flood the queue after every drift alert.
//
// Lifecycle
// ────────
//  1. PlanTick: every 10 minutes, scan new integrity events and
//     enqueue a probe for any (cred, model) we have not probed in
//     24h. dedup_key = `integrity:<credID>:<model>` is the unique
//     de-duplication handle inside credential_probe_queue.
//  2. The queue's existing consumer (ProbeQueueWorker) runs the
//     probe via the same ActiveProbeExecutor used by legacy active
//     probes, so no new executor is needed.
//  3. probe_command is `integrity_verify`; the existing queue
//     schema does not constrain command, so this is a forward-only
//     marker. Operators can later filter queue/admin views by
//     command to surface integrity probes specifically.
//
// Synthetic-traffic safety
// ──────────────────────
// The plan only fires for (cred, model) pairs where the upstream
// is still routable (v_routable_credential_models) so we never enqueue
// a probe for a model the gateway has already retired. Worker
// integration tests cover the basic dedup logic; the full
// execute-and-evaluate path is covered by the existing
// ProbeQueueWorker / ActiveProbeExecutor suites.
package bg

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// IntegrityProbePlannerConfig controls the planner's cadence and the
// dedup window applied at enqueue time.
type IntegrityProbePlannerConfig struct {
	Interval    time.Duration
	DedupWindow time.Duration
	MaxPerTick  int
}

// DefaultIntegrityProbePlannerConfig mirrors the production
// defaults: 10-minute plan, 24-hour dedup, 25 probes per tick.
func DefaultIntegrityProbePlannerConfig() IntegrityProbePlannerConfig {
	return IntegrityProbePlannerConfig{
		Interval:    parseDurationEnv("LLM_GATEWAY_INTEGRITY_PROBE_INTERVAL", 10*time.Minute),
		DedupWindow: parseDurationEnv("LLM_GATEWAY_INTEGRITY_PROBE_DEDUP", 24*time.Hour),
		MaxPerTick:  parseIntEnv("LLM_GATEWAY_INTEGRITY_PROBE_MAX_PER_TICK", 25),
	}
}

// IntegrityProbePlanner bridges model_integrity_events into
// credential_probe_queue so the durable queue has a real producer.
type IntegrityProbePlanner struct {
	db    *pgxpool.Pool
	queue *ProbeQueue
	cfg   IntegrityProbePlannerConfig

	cancel       context.CancelFunc
	done         chan struct{}
	started      atomic.Bool
	cycles       atomic.Uint64
	enqueued     atomic.Uint64
	skippedQueue atomic.Uint64
}

// NewIntegrityProbePlanner constructs a planner. queue may be nil
// during tests; the planner becomes a no-op in that case.
func NewIntegrityProbePlanner(db *pgxpool.Pool, queue *ProbeQueue, cfg IntegrityProbePlannerConfig) *IntegrityProbePlanner {
	if cfg.MaxPerTick <= 0 {
		cfg.MaxPerTick = 25
	}
	if cfg.Interval <= 0 {
		cfg.Interval = 10 * time.Minute
	}
	if cfg.DedupWindow <= 0 {
		cfg.DedupWindow = 24 * time.Hour
	}
	return &IntegrityProbePlanner{
		db:    db,
		queue: queue,
		cfg:   cfg,
		done:  make(chan struct{}),
	}
}

// Start launches the background loop. Idempotent.
func (p *IntegrityProbePlanner) Start(ctx context.Context) {
	if p == nil || p.db == nil || p.queue == nil {
		return
	}
	if !p.started.CompareAndSwap(false, true) {
		return
	}
	runCtx, cancel := context.WithCancel(ctx)
	p.cancel = cancel
	go p.run(runCtx)
	slog.Info("integrity_probe_planner started",
		"interval", p.cfg.Interval,
		"dedup_window", p.cfg.DedupWindow,
		"max_per_tick", p.cfg.MaxPerTick,
	)
}

// Stop signals the loop to exit and waits for it.
func (p *IntegrityProbePlanner) Stop() {
	if p == nil || !p.started.Load() {
		return
	}
	if p.cancel != nil {
		p.cancel()
	}
	<-p.done
	slog.Info("integrity_probe_planner stopped",
		"cycles", p.cycles.Load(),
		"enqueued", p.enqueued.Load(),
		"skipped_queue_disabled", p.skippedQueue.Load(),
	)
}

func (p *IntegrityProbePlanner) run(ctx context.Context) {
	defer close(p.done)
	ticker := time.NewTicker(p.cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.cycles.Add(1)
			if err := p.cycle(ctx); err != nil {
				slog.Warn("integrity_probe_planner: cycle failed", "error", err)
			}
		}
	}
}

// cycle picks at most MaxPerTick unresolved integrity events and
// enqueues a probe per (cred, model). The queue's own dedup keeps
// a single live task per dedup_key, so re-enqueueing within the
// dedup window is safe (returns false, no row).
func (p *IntegrityProbePlanner) cycle(ctx context.Context) error {
	stepCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	dedupInterval := fmt.Sprintf("%f seconds", p.cfg.DedupWindow.Seconds())
	rows, err := p.db.Query(stepCtx, `
		WITH pick AS (
		    SELECT DISTINCT ON (COALESCE(NULLIF(e.tenant_id, ''), 'default'), e.credential_id, e.raw_model_name)
		         COALESCE(NULLIF(e.tenant_id, ''), 'default') AS tenant_id,
		         e.credential_id, e.raw_model_name, e.anomaly_type,
		         COALESCE(p.code, 'unknown') AS provider_code
		    FROM model_integrity_events e
		    LEFT JOIN credentials c
		      ON c.id = e.credential_id
		    LEFT JOIN providers p
		      ON p.id = c.provider_id
				WHERE e.severity IN ('critical', 'high')
				  AND e.resolved = false
				  AND e.credential_id IS NOT NULL
				  AND e.raw_model_name IS NOT NULL
				  AND NOT EXISTS (
				      SELECT 1
				      FROM credential_probe_queue q
				      WHERE q.tenant_id = COALESCE(NULLIF(e.tenant_id, ''), 'default')
				        AND q.credential_id = e.credential_id
				        AND q.raw_model = e.raw_model_name
				        AND q.probe_command = 'integrity_verify'
				        AND q.created_at >= now() - $1::interval
				  )
				  AND EXISTS (
		          SELECT 1
		          FROM credential_model_bindings cmb
		          JOIN provider_models pm ON pm.id = cmb.provider_model_id
		          WHERE cmb.credential_id = e.credential_id
		            AND pm.raw_model_name = e.raw_model_name
		            AND cmb.available = TRUE
		      )
		    ORDER BY COALESCE(NULLIF(e.tenant_id, ''), 'default'), e.credential_id, e.raw_model_name, e.ts DESC
		)
		SELECT tenant_id, credential_id, raw_model_name, anomaly_type, provider_code
		FROM pick
		ORDER BY tenant_id, credential_id
			LIMIT $2`, dedupInterval, p.cfg.MaxPerTick)
	if err != nil {
		return fmt.Errorf("integrity_probe_planner: query events: %w", err)
	}
	defer rows.Close()
	type pending struct {
		tenant   string
		credID   int64
		rawModel string
		anomaly  string
		provider string
	}
	var batch []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.tenant, &p.credID, &p.rawModel, &p.anomaly, &p.provider); err != nil {
			slog.Warn("integrity_probe_planner: scan row failed", "error", err)
			continue
		}
		batch = append(batch, p)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()

	for _, item := range batch {
		task := ProbeQueueTask{
			CredentialID: item.credID,
			ProviderID:   0, // Resolved at consumer via LoadTarget, provider_id is denormalized in the queue column.
			TenantID:     item.tenant,
			RawModel:     item.rawModel,
			Command:      "integrity_verify",
			Mode:         "single",
			Priority:     70, // Higher than passive probes (50) so integrity verification runs promptly.
			ReasonCode:   item.anomaly,
			ReasonDetail: fmt.Sprintf("planner: unresolved %s event for (%d, %s)", item.anomaly, item.credID, item.rawModel),
			MaxAttempts:  2,
			Source:       "integrity_probe_planner",
			// source_event_id is intentionally empty: the anomaly type is
			// not globally unique and would collide across credentials.
			// The credential/model dedup key plus the SQL time window is
			// the durable idempotency contract for planner tasks.
			// Tenant is part of the durable idempotency key so identical
			// credentials/models in separate tenants do not suppress each other.
			DedupKey: fmt.Sprintf("integrity:%s:%d:%s", item.tenant, item.credID, item.rawModel),
		}
		_, inserted, err := p.queue.Enqueue(stepCtx, task)
		if err != nil {
			slog.Warn("integrity_probe_planner: enqueue failed",
				"credential_id", item.credID, "model", item.rawModel, "error", err)
			continue
		}
		if !inserted {
			p.skippedQueue.Add(1)
			continue
		}
		p.enqueued.Add(1)
		slog.Info("integrity_probe_planner: probe enqueued",
			"credential_id", item.credID,
			"model", item.rawModel,
			"anomaly", item.anomaly,
			"provider", item.provider,
		)
	}
	return nil
}
