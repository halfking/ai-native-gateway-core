package bg

// session_turn_details_expired.go — observability for rows stranded in
// session_turn_details_hot past the promote retention window (migration
// 759 follow-up). 759's drain is bounded and lossless by design: rows with
// a true key conflict stay in hot for explicit adjudication, so "expired
// but still in hot" is a steady-state signal, not a transient one. This
// file classifies those rows exclusively (every expired row counts in
// exactly one category) and publishes them as labeled gauges so operators
// can see whether the residual is benign duplicates or a stuck drain.
//
// Categories (exclusive; priority order mirrors 759's phases):
//   - request_duplicate: same (tenant_id, request_id, partition_date)
//     already in the parent AND every shared non-id column IS NOT DISTINCT
//     FROM the parent row → pure redundancy, the drain deletes it on the
//     next tick. Column equality uses the INTERSECTION of parent/hot
//     columns (759 demands exact shape identity because it writes; this
//     gauge only classifies, so a best-effort intersection is safe).
//   - session_turn_conflict: (session_id, turn_no, partition_date) held by
//     a parent row → real conflict, needs adjudication.
//   - id_date_conflict: (id, partition_date) held by a different parent
//     row → real conflict, needs adjudication.
//   - ready_unmoved: none of the above keys block the 759 phase-2 insert;
//     the drain should move it on the next tick. A persistently non-zero
//     value means the promote worker is stalled (the "hot 8h invariant
//     broken" signal). Includes the narrow case of a non-equal request-key
//     match with no other conflict (759 keeps it in hot; it shows up as
//     unmoved-but-not-draining and belongs to the same adjudication
//     backlog).
//
// total == request_duplicate + session_turn_conflict + id_date_conflict +
// ready_unmoved, always.

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// SessionTurnDetailsExpiredHotStats is the exclusive classification of
// session_turn_details_hot rows older than the promote retention window.
type SessionTurnDetailsExpiredHotStats struct {
	Total               int64
	RequestDuplicate    int64
	SessionTurnConflict int64
	IDDateConflict      int64
	ReadyUnmoved        int64
}

var (
	// sessionTurnDetailsExpiredHotRows carries one gauge per exclusive
	// classification (label "status": total / request_duplicate /
	// session_turn_conflict / id_date_conflict / ready_unmoved).
	sessionTurnDetailsExpiredHotRows = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "llm_gateway_session_turn_details_expired_hot_rows",
			Help: "session_turn_details_hot rows past the promote retention, classified exclusively. total should approach request_duplicate+ready_unmoved (the 759 drain deletes both on its next tick); a persistent session_turn_conflict/id_date_conflict needs adjudication, persistent ready_unmoved means the drain is stalled.",
		},
		[]string{"status"},
	)
	// sessionTurnDetailsExpiredDuplicateHotRows is the scalar view of the
	// benign-redundancy category (kept as a plain gauge so alert
	// expressions written against the pre-labeled metric keep working).
	sessionTurnDetailsExpiredDuplicateHotRows = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "llm_gateway_session_turn_details_expired_duplicate_hot_rows",
			Help: "session_turn_details_hot rows past retention whose (tenant_id, request_id, partition_date) key already exists in the parent with identical shared columns — pure redundancy the 759 drain deletes on its next tick.",
		},
	)
	// sessionTurnDetailsExpiredHotRowsVerified is 1 after a successful
	// classification refresh and 0 after a failed one, so a stale panel is
	// distinguishable from a genuinely empty hot table. On failure the
	// value gauges are set to NaN ("unknown"), matching the turn-logs
	// backlog gauges' failure semantics.
	sessionTurnDetailsExpiredHotRowsVerified = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "llm_gateway_session_turn_details_expired_hot_rows_verified",
			Help: "1 after a successful session_turn_details_expired_hot_rows refresh, 0 after a failed one (the value gauges are then NaN = unknown, not zero).",
		},
	)
)

// sessionTurnDetailsSharedColumns returns the non-id column names present
// in BOTH session_turn_details and session_turn_details_hot, in catalog
// order. Equality is only meaningful on shared columns; unlike migration
// 759 (which hard-fails on shape drift because it writes), this
// classification degrades to the intersection.
func sessionTurnDetailsSharedColumns(ctx context.Context, db *pgxpool.Pool) ([]string, error) {
	const q = `
		SELECT a.attname
		  FROM pg_attribute a
		  JOIN pg_attribute b
		    ON b.attname = a.attname
		   AND b.attrelid = 'public.session_turn_details'::regclass
		   AND b.attnum > 0 AND NOT b.attisdropped
		 WHERE a.attrelid = 'public.session_turn_details_hot'::regclass
		   AND a.attnum > 0 AND NOT a.attisdropped
		   AND a.attname <> 'id'
		 ORDER BY a.attnum
	`
	rows, err := db.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		cols = append(cols, c)
	}
	return cols, rows.Err()
}

// QuerySessionTurnDetailsExpiredHotRows classifies every
// session_turn_details_hot row older than retention into exactly one
// category (see the file comment for the definitions). The equality probe
// mirrors 759's v_equal shape: hot NULL matches anything, otherwise a
// typed IS NOT DISTINCT FROM per shared column.
func QuerySessionTurnDetailsExpiredHotRows(ctx context.Context, db *pgxpool.Pool, retention time.Duration) (SessionTurnDetailsExpiredHotStats, error) {
	var s SessionTurnDetailsExpiredHotStats
	cols, err := sessionTurnDetailsSharedColumns(ctx, db)
	if err != nil {
		return s, fmt.Errorf("session_turn_details shared columns: %w", err)
	}
	equalParts := make([]string, 0, len(cols))
	for _, c := range cols {
		equalParts = append(equalParts,
			fmt.Sprintf("(h.%[1]s IS NULL OR h.%[1]s IS NOT DISTINCT FROM p_req.%[1]s)", c))
	}
	// duplicate = request key present AND all shared non-id columns equal.
	dupExpr := "p_req.id IS NOT NULL"
	if len(equalParts) > 0 {
		dupExpr += " AND " + strings.Join(equalParts, " AND ")
	}
	q := `
		SELECT
			count(*),
			count(*) FILTER (WHERE ` + dupExpr + `),
			count(*) FILTER (WHERE NOT (` + dupExpr + `) AND p_turn.id IS NOT NULL),
			count(*) FILTER (WHERE NOT (` + dupExpr + `) AND p_turn.id IS NULL AND p_id.id IS NOT NULL),
			count(*) FILTER (WHERE NOT (` + dupExpr + `) AND p_turn.id IS NULL AND p_id.id IS NULL)
		FROM public.session_turn_details_hot h
		LEFT JOIN public.session_turn_details p_req
		  ON p_req.tenant_id = h.tenant_id
		 AND p_req.request_id = h.request_id
		 AND p_req.partition_date = h.partition_date
		LEFT JOIN public.session_turn_details p_turn
		  ON p_turn.session_id = h.session_id
		 AND p_turn.turn_no = h.turn_no
		 AND p_turn.partition_date = h.partition_date
		LEFT JOIN public.session_turn_details p_id
		  ON p_id.id = h.id AND p_id.partition_date = h.partition_date
		WHERE h.ts < statement_timestamp() - $1::interval
	`
	err = db.QueryRow(ctx, q, retention).Scan(
		&s.Total, &s.RequestDuplicate, &s.SessionTurnConflict,
		&s.IDDateConflict, &s.ReadyUnmoved,
	)
	return s, err
}

// recordSessionTurnDetailsExpiredHotRows publishes one classification
// result. The verified gauge stays 1 until a failure resets it.
func recordSessionTurnDetailsExpiredHotRows(s SessionTurnDetailsExpiredHotStats) {
	sessionTurnDetailsExpiredHotRows.WithLabelValues("total").Set(float64(s.Total))
	sessionTurnDetailsExpiredHotRows.WithLabelValues("request_duplicate").Set(float64(s.RequestDuplicate))
	sessionTurnDetailsExpiredHotRows.WithLabelValues("session_turn_conflict").Set(float64(s.SessionTurnConflict))
	sessionTurnDetailsExpiredHotRows.WithLabelValues("id_date_conflict").Set(float64(s.IDDateConflict))
	sessionTurnDetailsExpiredHotRows.WithLabelValues("ready_unmoved").Set(float64(s.ReadyUnmoved))
	sessionTurnDetailsExpiredDuplicateHotRows.Set(float64(s.RequestDuplicate))
	sessionTurnDetailsExpiredHotRowsVerified.Set(1)
}

// recordSessionTurnDetailsExpiredHotRowsFailure marks the gauges unknown:
// verified drops to 0 and every value gauge is set to NaN (a failed
// refresh reports unknown, not zero — zero would read as "hot table
// clean" on a dashboard).
func recordSessionTurnDetailsExpiredHotRowsFailure() {
	nan := math.NaN()
	for _, status := range []string{"total", "request_duplicate", "session_turn_conflict", "id_date_conflict", "ready_unmoved"} {
		sessionTurnDetailsExpiredHotRows.WithLabelValues(status).Set(nan)
	}
	sessionTurnDetailsExpiredDuplicateHotRows.Set(nan)
	sessionTurnDetailsExpiredHotRowsVerified.Set(0)
}

// refreshSessionTurnDetailsExpiredHotGauges is the 1h-tick entry point,
// wired next to refreshTurnLogsBacklogGauge in runCleanup. Retention comes
// from the same resolvePromoteConfig the promote worker uses for this
// table, so the gauge window and the drain window cannot drift apart.
func (pm *PartitionManager) refreshSessionTurnDetailsExpiredHotGauges(ctx context.Context) {
	if pm.db == nil {
		return
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	retention, _ := resolvePromoteConfig("session_turn_details_hot")
	stats, err := QuerySessionTurnDetailsExpiredHotRows(timeoutCtx, pm.db, retention)
	if err != nil {
		slog.Warn("partition_manager: session_turn_details expired-hot gauge refresh failed", "error", err)
		recordSessionTurnDetailsExpiredHotRowsFailure()
		return
	}
	recordSessionTurnDetailsExpiredHotRows(stats)
}
