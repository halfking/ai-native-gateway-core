package bg

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ColumnarInvariantCheck runs once at gateway startup and emits a
// structured log line describing the current columnar AM compliance
// per parent table. Non-compliant partitions are logged at WARN
// level — they don't block startup (the event trigger from Phase 23
// / 02 catches new partitions at DDL time, and the daily cron
// heals existing drift) — but the log makes the state explicit so
// operators can react.
//
// Audit basis (2026-07-02):
//   - request_logs / request_wal / usage_ledger: UPDATE-heavy + (in
//     request_logs' case) large JSONB that overflows Citus columnar
//     1 GB serialization buffer. Stay heap until / unless the
//     body-table split (migration 328a/b) frees them up.
//   - routing_decision_log / credential_model_index: INSERT-only in
//     Go source. Must be columnar; event trigger enforces.
//
// Returns the count of non-compliant partitions for callers that
// want to surface it on a /healthz endpoint.
func ColumnarInvariantCheck(ctx context.Context, pool *pgxpool.Pool) (int, error) {
	if pool == nil {
		return 0, nil
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	rows, err := pool.Query(timeoutCtx, "SELECT * FROM columnar_drift_report()")
	if err != nil {
		// columnar_drift_report() may not exist on a fresh DB that
		// hasn't had phase 23 applied yet — treat that as "no
		// invariant in place" rather than a hard error.
		slog.Warn("columnar_invariant_check: drift_report query failed (phase 23 not applied?)",
			"error", err)
		return 0, nil
	}
	defer rows.Close()

	type row struct {
		ParentName        string
		Compliant         int
		Noncompliant      int
		TotalBytes        int64
		HeapBytes         int64
		ColumnarBytes     int64
	}
	var (
		total        int
		noncompliant int
	)
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.ParentName, &r.Compliant, &r.Noncompliant,
			&r.TotalBytes, &r.HeapBytes, &r.ColumnarBytes); err != nil {
			return noncompliant, fmt.Errorf("columnar_invariant_check scan: %w", err)
		}
		total += r.Compliant + r.Noncompliant
		noncompliant += r.Noncompliant

		level := slog.LevelInfo
		if r.Noncompliant > 0 {
			level = slog.LevelWarn
		}
		slog.Log(ctx, level, "columnar_invariant_check",
			"parent", r.ParentName,
			"compliant", r.Compliant,
			"noncompliant", r.Noncompliant,
			"total_bytes", r.TotalBytes,
			"heap_bytes", r.HeapBytes,
			"columnar_bytes", r.ColumnarBytes,
		)
	}
	if err := rows.Err(); err != nil {
		return noncompliant, fmt.Errorf("columnar_invariant_check rows: %w", err)
	}

	if noncompliant > 0 {
		slog.Warn("columnar_invariant_check: drift detected, daily cron will heal",
			"noncompliant", noncompliant, "total", total)
	} else {
		slog.Info("columnar_invariant_check: all reachable partitions compliant", "total", total)
	}
	return noncompliant, nil
}
