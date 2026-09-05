// bg/probe_rollback.go — delayed-rollback worker for tentative probe restores.
//
// 需求 6, bullet 3: 自检后对凭据节点的状态的修改、回退的延时处理.
//
// A probe that tentatively restores a node stamps credential_model_bindings
// .probe_revert_at = now()+T (via MarkTentativeRestore). A subsequent
// confirming probe success CLEARS probe_revert_at (see updateBindingAvailability
// success branch), so the revert is a one-shot guard against a probe that
// "passed" in isolation but whose restore should not persist without
// corroboration. This worker reverts bindings whose probe_revert_at has elapsed
// and were not confirmed.
//
// Mirrors the bg/credential_recovery.go ticker pattern (one-table-per-concern,
// no generic job framework). Respects admin_protected and manual% unavailable
// reasons (never fights an operator).
package bg

import (
	"context"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// llmgw_probe_rollback_* metrics (2026-08-24). Before this, the worker only
// logged via slog, so a dormant marking entry or a failing scan was invisible
// to /metrics dashboards (245 showed 8x "revert scan failed" with no signal).
//
//	llmgw_probe_rollback_tentative_marked_total — MarkTentativeRestore writes
//	    that stamped a revert deadline (the smart-fallback marking entry).
//	llmgw_probe_rollback_reverted_total — bindings actually reverted by the
//	    worker (deadline elapsed, never confirmed).
//	llmgw_probe_rollback_scan_failures_total — revert scans that errored
//	    (e.g. shared-PG context deadline exceeded; watch frequency).
//	llmgw_probe_rollback_scan_duration_seconds — wall clock per scan tick.
//	llmgw_probe_rollback_pending — gauge of bindings currently carrying a
//	    probe_revert_at (tentative restores awaiting confirmation).
var (
	probeRollbackMarkedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "llmgw_probe_rollback_tentative_marked_total",
		Help: "Tentative probe restores stamped with a revert deadline (smart-fallback marking entry).",
	})

	probeRollbackRevertedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "llmgw_probe_rollback_reverted_total",
		Help: "Tentative restores reverted by the delayed-rollback worker (elapsed without confirmation).",
	})

	probeRollbackScanFailures = promauto.NewCounter(prometheus.CounterOpts{
		Name: "llmgw_probe_rollback_scan_failures_total",
		Help: "Revert scan ticks that failed (watch for shared-PG timeouts).",
	})

	probeRollbackScanDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "llmgw_probe_rollback_scan_duration_seconds",
		Help:    "Wall-clock duration of each revert scan tick.",
		Buckets: []float64{0.005, 0.01, 0.05, 0.1, 0.25, 0.5, 1, 2, 5},
	})

	probeRollbackPending = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "llmgw_probe_rollback_pending",
		Help: "Bindings currently carrying probe_revert_at (tentative restores awaiting confirmation).",
	})
)

// ProbeRollbackConfig tunes the delayed-rollback scan.
type ProbeRollbackConfig struct {
	Interval time.Duration // scan cadence; defaults 10s
}

// ProbeRollback reverts tentative probe restores whose revert window elapsed.
type ProbeRollback struct {
	db     *pgxpool.Pool
	cfg    ProbeRollbackConfig
	cancel context.CancelFunc
	done   chan struct{}
}

// NewProbeRollback constructs the worker. Interval<=0 defaults to 10s.
func NewProbeRollback(db *pgxpool.Pool, cfg ProbeRollbackConfig) *ProbeRollback {
	if cfg.Interval <= 0 {
		cfg.Interval = 10 * time.Second
	}
	return &ProbeRollback{db: db, cfg: cfg, done: make(chan struct{})}
}

// Start launches the scan loop. No-op without a db.
func (r *ProbeRollback) Start(ctx context.Context) {
	if r == nil || r.db == nil {
		if r != nil {
			close(r.done)
		}
		return
	}
	ctx, r.cancel = context.WithCancel(ctx)
	go r.run(ctx)
}

// Stop cancels the loop and waits for it to exit.
func (r *ProbeRollback) Stop() {
	if r == nil {
		return
	}
	if r.cancel != nil {
		r.cancel()
	}
	<-r.done
}

func (r *ProbeRollback) run(ctx context.Context) {
	defer close(r.done)
	ticker := time.NewTicker(r.cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			start := time.Now()
			n, err := r.revertDue(ctx)
			probeRollbackScanDuration.Observe(time.Since(start).Seconds())
			if err != nil {
				probeRollbackScanFailures.Inc()
				slog.Warn("probe_rollback: revert scan failed", "error", err)
			} else if n > 0 {
				probeRollbackRevertedTotal.Add(float64(n))
				slog.Info("probe_rollback: reverted tentative restores", "count", n)
			}
			r.observePending(ctx)
		}
	}
}

// observePending refreshes the pending gauge with a cheap COUNT against the
// partial index (migration 351). Best-effort: a failed count leaves the last
// gauge value in place rather than resetting to zero.
func (r *ProbeRollback) observePending(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var pending int
	if err := r.db.QueryRow(ctx, `
		SELECT COUNT(*) FROM credential_model_bindings
		WHERE probe_revert_at IS NOT NULL`).Scan(&pending); err != nil {
		return
	}
	probeRollbackPending.Set(float64(pending))
}

// revertDue flips available→FALSE for bindings whose probe_revert_at has
// elapsed and clears the timer (one-shot). Guards:
//   - admin_protected bindings are never touched;
//   - manual% unavailable_reason bindings are never touched;
//   - only available=TRUE rows are reverted (a row already marked unavailable
//     by another path just has its timer cleared).
//
// The race with a concurrent confirming probe is closed by the WHERE clause:
// updateBindingAvailability's success branch sets probe_revert_at=NULL in the
// SAME row, so once a confirm committed, this UPDATE matches zero rows.
func (r *ProbeRollback) revertDue(ctx context.Context) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tag, err := r.db.Exec(ctx, `
		UPDATE credential_model_bindings cmb
		SET available = FALSE,
		    unavailable_reason = COALESCE(cmb.unavailable_reason, 'probe_revert_timeout'),
		    unavailable_at = COALESCE(cmb.unavailable_at, now()),
		    unavailable_recover_at = COALESCE(cmb.unavailable_recover_at, now() + interval '5 minutes'),
		    probe_revert_at = NULL,
		    updated_at = now()
		WHERE cmb.probe_revert_at IS NOT NULL
		  AND cmb.probe_revert_at <= now()
		  AND cmb.available = TRUE
		  AND COALESCE(cmb.admin_protected, FALSE) = FALSE`)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// probeTentativeRevertAfterDefault is the smart-fallback revert window: how
// long a sync-probe (ProbeSync) restore stays visible without a confirming
// probe success. Must comfortably exceed the tick/queue probe cadence for
// cooling nodes (submit wake ≈ 5s; unified-queue pump 10s batches) so a
// healthy node is confirmed, not reverted, in steady state.
const probeTentativeRevertAfterDefault = 15 * time.Minute

// probeTentativeRevertAfter resolves LLM_GATEWAY_PROBE_TENTATIVE_REVERT_AFTER.
// Accepted forms: Go duration ("15m"), bare seconds ("900"), and the disabling
// literals "0"/"off"/"false"/"disabled" (→ 0, marking entry off). Unparsable
// or negative values fall back to the default (fail-safe: keep the guard).
func probeTentativeRevertAfter() time.Duration {
	v := strings.TrimSpace(os.Getenv("LLM_GATEWAY_PROBE_TENTATIVE_REVERT_AFTER"))
	if v == "" {
		return probeTentativeRevertAfterDefault
	}
	switch strings.ToLower(v) {
	case "0", "off", "false", "disabled":
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs < 0 {
			return probeTentativeRevertAfterDefault
		}
		return time.Duration(secs) * time.Second
	}
	if d, err := time.ParseDuration(v); err == nil {
		if d < 0 {
			return probeTentativeRevertAfterDefault
		}
		return d
	}
	return probeTentativeRevertAfterDefault
}

// MarkTentativeRestore stamps a (credential, model) binding as tentatively
// restored: available=TRUE now, but auto-reverted at revertAfter unless a
// confirming probe success clears probe_revert_at first. Used by "smart
// fallback" probe scenarios (需求 6 bullet 6: 需要智能回退的) — the one wired
// caller today is NodeProbeWorker.ProbeSync (request hot-path no_candidates
// recovery): a probe that "passed" in isolation restores the node tentatively,
// and the next confirming probe success (tick/queue/request_failure driven)
// clears the deadline. Best-effort.
func MarkTentativeRestore(ctx context.Context, db *pgxpool.Pool, credID int, model string, revertAfter time.Duration) error {
	if db == nil {
		return nil
	}
	tag, err := db.Exec(ctx, `
		UPDATE credential_model_bindings cmb
		SET available = TRUE,
		    unavailable_reason = NULL,
		    unavailable_at = NULL,
		    unavailable_recover_at = NULL,
		    probe_revert_at = now() + $3::interval,
		    updated_at = now()
		FROM provider_models pm
		WHERE pm.id = cmb.provider_model_id
		  AND cmb.credential_id = $1
		  AND pm.raw_model_name = $2
		  AND COALESCE(cmb.admin_protected, FALSE) = FALSE`,
		credID, model, int(revertAfter.Seconds()))
	if err != nil {
		return err
	}
	if n := tag.RowsAffected(); n > 0 {
		probeRollbackMarkedTotal.Add(float64(n))
	}
	return nil
}
