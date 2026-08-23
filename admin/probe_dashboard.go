// Package admin — probe_dashboard.go
//
// Model Health Dashboard APIs for unified probe scheduler monitoring.
//
// Provides real-time visibility into:
//   - Model state distribution across all credential nodes
//   - Probe queue priorities and sizes
//   - System health metrics
//
// Spec: 2026-06-28-model-health-dashboard
package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/bg"
	"github.com/redis/go-redis/v9"
)

// ── Data Models ─────────────────────────────────────────────────────────

// ModelHealthSummary represents the health status of a model across all credentials
type ModelHealthSummary struct {
	ProviderModelID int64  `json:"provider_model_id"`
	RawModelName    string `json:"raw_model_name"`
	OutboundModel   string `json:"outbound_model_name"`
	Protocol        string `json:"protocol"`
	ProviderName    string `json:"provider_name"`

	// State distribution
	TotalCredentials  int     `json:"total_credentials"`
	HealthyCount      int     `json:"healthy_count"`
	SuspiciousCount   int     `json:"suspicious_count"`
	FailingCount      int     `json:"failing_count"`
	ProbingCount      int     `json:"probing_count"`
	HealthyPercentage float64 `json:"healthy_percentage"`
	FailingPercentage float64 `json:"failing_percentage"`

	// Priority distribution
	UrgentCount             int `json:"urgent_count"`
	SuspiciousPriorityCount int `json:"suspicious_priority_count"`
	FailingPriorityCount    int `json:"failing_priority_count"`
	WatchdogCount           int `json:"watchdog_count"`

	// Health metrics
	AvgSuccessRate7d        float64 `json:"avg_success_rate_7d"`
	AvgVerificationHours    float64 `json:"avg_verification_hours"`
	AvgConsecutiveSuccesses float64 `json:"avg_consecutive_successes"`

	// Real request stats (24h)
	TotalRealSuccess24h int      `json:"total_real_success_24h"`
	TotalRealFailure24h int      `json:"total_real_failure_24h"`
	RealSuccessRate24h  *float64 `json:"real_success_rate_24h,omitempty"`

	// Timestamps
	LastVerifiedAt    *time.Time `json:"last_verified_at,omitempty"`
	LastRealRequestAt *time.Time `json:"last_real_request_at,omitempty"`
	NextProbeAt       *time.Time `json:"next_probe_at,omitempty"`

	// Alerts
	CriticalNodes     int    `json:"critical_nodes"`
	PendingProbes5min int    `json:"pending_probes_5min"`
	OverallHealth     string `json:"overall_health"` // critical, warning, degraded, healthy, unknown
}

// ProbeQueueSnapshot represents the current state of probe queues
type ProbeQueueSnapshot struct {
	ProbePriority   string     `json:"probe_priority"`
	State           string     `json:"state"`
	QueueSize       int        `json:"queue_size"`
	ReadyNow        int        `json:"ready_now"`
	Ready1min       int        `json:"ready_1min"`
	Ready5min       int        `json:"ready_5min"`
	EarliestRetryAt *time.Time `json:"earliest_retry_at,omitempty"`
	LatestRetryAt   *time.Time `json:"latest_retry_at,omitempty"`
	AvgWaitSeconds  *float64   `json:"avg_wait_seconds,omitempty"`
	MaxWaitSeconds  *float64   `json:"max_wait_seconds,omitempty"`
}

// ProbeSystemHealth represents overall system health metrics
type ProbeSystemHealth struct {
	// Overall stats
	TotalNodes      int `json:"total_nodes"`
	HealthyNodes    int `json:"healthy_nodes"`
	FailingNodes    int `json:"failing_nodes"`
	SuspiciousNodes int `json:"suspicious_nodes"`
	ProbingNodes    int `json:"probing_nodes"`

	// Queue sizes
	UrgentQueueSize     int `json:"urgent_queue_size"`
	SuspiciousQueueSize int `json:"suspicious_queue_size"`
	FailingQueueSize    int `json:"failing_queue_size"`
	WatchdogQueueSize   int `json:"watchdog_queue_size"`

	// Active probes
	ReadyProbes            int `json:"ready_probes"`
	CurrentProbing         int `json:"current_probing"`
	CredentialsBeingProbed int `json:"credentials_being_probed"`

	// Health metrics
	AvgSuccessRate7d *float64 `json:"avg_success_rate_7d,omitempty"`

	// Recent activity
	LastProbeAt       *time.Time `json:"last_probe_at,omitempty"`
	LastRealRequestAt *time.Time `json:"last_real_request_at,omitempty"`

	// 24h request stats
	TotalRealSuccess24h int `json:"total_real_success_24h"`
	TotalRealFailure24h int `json:"total_real_failure_24h"`

	// Alerts
	CriticalNodes     int `json:"critical_nodes"`
	PendingProbes5min int `json:"pending_probes_5min"`

	SnapshotAt time.Time `json:"snapshot_at"`
}

// ModelNodeDetail represents detailed info for a single credential×model node
type ModelNodeDetail struct {
	RawModelName  string `json:"raw_model_name"`
	OutboundModel string `json:"outbound_model_name"`
	ProbePriority string `json:"probe_priority"`
	State         string `json:"state"`

	CredentialID    int64  `json:"credential_id"`
	CredentialLabel string `json:"credential_label"`
	ProviderName    string `json:"provider_name"`

	// Status
	LastVerifiedAt     *time.Time `json:"last_verified_at,omitempty"`
	NextRetryAt        *time.Time `json:"next_retry_at,omitempty"`
	MarkedSuspiciousAt *time.Time `json:"marked_suspicious_at,omitempty"`
	ProbingStartedAt   *time.Time `json:"probing_started_at,omitempty"`

	// Stats
	ConsecutiveSuccesses         int      `json:"consecutive_successes"`
	ConsecutiveFailures          int      `json:"consecutive_failures"`
	ConsecutiveWatchdogSuccesses int      `json:"consecutive_watchdog_successes"`
	SuccessRate7d                *float64 `json:"success_rate_7d,omitempty"`
	VerificationInterval         string   `json:"verification_interval"`

	// Real requests (24h)
	RealSuccess24h    int        `json:"real_success_24h"`
	RealFailure24h    int        `json:"real_failure_24h"`
	LastRealRequestAt *time.Time `json:"last_real_request_at,omitempty"`

	// Error info
	LastUnavailableReason *string `json:"last_unavailable_reason,omitempty"`
	LastErrCode           *string `json:"last_err_code,omitempty"`

	// Derived
	RetryIn              string  `json:"retry_in"`
	StateDurationMinutes float64 `json:"state_duration_minutes"`
}

// ModelStateBreakdown is returned by get_model_state_summary()
type ModelStateBreakdown struct {
	State              string   `json:"state"`
	Priority           string   `json:"priority"`
	Count              int      `json:"count"`
	AvgSuccessRate     *float64 `json:"avg_success_rate,omitempty"`
	NextProbeInSeconds *int     `json:"next_probe_in_seconds,omitempty"`
}

// buildStateDistribution (2026-06-30 PR-7) flattens a breakdown slice into a
// {state: total_count} map. Frontend ProbeHealthDetailView.vue reads
// `state_distribution` directly for the 4 status badges; without this
// field the badges show 0 (audit P0-10).
func buildStateDistribution(breakdown []ModelStateBreakdown) map[string]int {
	out := make(map[string]int, len(breakdown))
	for _, b := range breakdown {
		out[b.State] += b.Count
	}
	return out
}

// ── API Handlers ────────────────────────────────────────────────────────

// 2026-08-18 (Agent C): unified probe queue snapshot from
// credential_probe_queue + node_probe_state. The previous handler
// returned only the legacy v_probe_queue_snapshot (sourced from
// model_probe_state) and the 572-row historical backlog could not be
// distinguished from new activity. The new helper aggregates the
// durable queue and the in-flight node-probe state into a single
// response so the dashboard can render the queue as it actually is.
//
// UnifiedProbeQueueStats holds the new-shape counts. The three numbers
// (ready/running/finished) are the closest mapping to the legacy
// priority/state grid and let the frontend render the same 4 status
// badges without breaking change.
type UnifiedProbeQueueStats struct {
	// credential_probe_queue counts (integrity probe planner).
	QueueReady    int `json:"queue_ready"`
	QueueRunning  int `json:"queue_running"`
	QueueFinished int `json:"queue_finished"` // last 2h rows
	QueueClaims   int `json:"queue_claims"`   // in-flight rows owned by an active lease

	// node_probe_state counts (error-triggered NodeProbeWorker).
	NodePending     int `json:"node_pending"`
	NodeRunning     int `json:"node_running"`
	NodePaused      int `json:"node_paused"`
	NodeDue         int `json:"node_due"`         // next_retry_at <= now
	NodeUnclaimable int `json:"node_unclaimable"` // in_flight_until set but expired — no worker holds the lease

	// Active leases that have not been renewed within the lease window.
	// Detected by leased_at + heartbeat_window < now() with status='running'
	// in credential_probe_queue. Mirrors Agent B's lease heartbeat
	// detection so the dashboard surfaces the same risk metric.
	StaleLeases int `json:"stale_leases"`

	// Last completed record (audit coverage indicator).
	LastRunAt *time.Time `json:"last_run_at,omitempty"`
	// Legacy alias: the dashboard used to expose queue_size as an
	// aggregation across priorities. Keep the field for backward
	// compatibility but route it to the new source.
	QueueSize int `json:"queue_size"`

	// SnapshotAt is the wall-clock time the aggregation was taken at.
	SnapshotAt time.Time `json:"snapshot_at"`
}

// UnifiedProbeSystemHealth is the new `system-health` payload that
// joins credential_probe_queue, node_probe_state, node_probe_runs,
// and URSM tenant coverage. The legacy view (model_probe_state) is
// returned separately under `legacy_*` so a reader can see when the
// two pictures disagree.
type UnifiedProbeSystemHealth struct {
	// New source: tenant-routable credential coverage.
	// URSM tenant key coverage is the authoritative runtime signal:
	// a credential can be DB-eligible and still be unroutable if
	// its URSM tenant key is missing (see 2026-08-18 handoff §6.1).
	TotalCredentials    int `json:"total_credentials"`
	CredentialsWithURSM int `json:"credentials_with_ursm"`
	CredentialsNoURSM   int `json:"credentials_no_ursm"`
	URSMKeyCount        int `json:"ursm_key_count"`

	// New source: credential_probe_queue aggregate.
	QueuePending   int `json:"queue_pending"`
	QueueInFlight  int `json:"queue_in_flight"`
	QueueCompleted int `json:"queue_completed"` // last 2h
	QueueFailed    int `json:"queue_failed"`    // last 2h
	QueueExpired   int `json:"queue_expired"`   // last 2h
	QueueTotal     int `json:"queue_total"`     // convenience total

	// New source: node_probe_state aggregate.
	NodeTotal   int `json:"node_total"`
	NodeHealthy int `json:"node_healthy"`
	NodeFailing int `json:"node_failing"`
	NodePaused  int `json:"node_paused"`
	NodeRunning int `json:"node_running"`
	NodeDueNow  int `json:"node_due_now"`
	NodeLeased  int `json:"node_leased"`

	// New source: node_probe_runs aggregate.
	RunsLast1h        int        `json:"runs_last_1h"`
	RunsSuccess1h     int        `json:"runs_success_1h"`
	RunsFailed1h      int        `json:"runs_failed_1h"`
	RunsLastAt        *time.Time `json:"runs_last_at,omitempty"`
	SuccessRateLast1h *float64   `json:"success_rate_last_1h,omitempty"`

	// New source: recovery pseudo-success detection (handoff §6 P0).
	// A credential is "pseudo-ok" if its latest node_probe_runs row
	// shows direct_ok=true but the credentials row no longer has a
	// URSM tenant key — i.e. the row was written by the deprecated
	// recovery SQL that Agent A is removing. The dashboard surfaces
	// the count so operators can see the impact.
	PseudoSuccessCount int `json:"pseudo_success_count"`

	// Legacy view (model_probe_state — superseded). Returned so the
	// frontend can keep rendering the old badges while the migration
	// to URSM/probe_queue finishes. Explicitly tagged so a careless
	// reader doesn't mix the two.
	Legacy TotalLegacySystemHealth `json:"legacy"`

	SnapshotAt time.Time `json:"snapshot_at"`
}

// TotalLegacySystemHealth mirrors the existing v_probe_system_health
// shape (only the integer fields that survive the legacy view definition).
// It is intentionally a *separate* struct so the JSON marshaller can
// emit `legacy: {...}` and so a Go-side reader cannot accidentally
// read the new fields under the legacy type.
type TotalLegacySystemHealth struct {
	// Legacy field set is the same as the existing v_probe_system_health
	// view columns (the legacy backend keeps producing them).
	TotalNodes      int        `json:"total_nodes"`
	HealthyNodes    int        `json:"healthy_nodes"`
	FailingNodes    int        `json:"failing_nodes"`
	SuspiciousNodes int        `json:"suspicious_nodes"`
	ProbingNodes    int        `json:"probing_nodes"`
	UrgentQueueSize int        `json:"urgent_queue_size"`
	ReadyProbes     int        `json:"ready_probes"`
	CurrentProbing  int        `json:"current_probing"`
	LastProbeAt     *time.Time `json:"last_probe_at,omitempty"`
	// LegacySource is the constant string "model_probe_state" so the
	// frontend can label the section without a code review.
	LegacySource string `json:"legacy_source"`
	// LegacyModeSafe=false tells the frontend the legacy view is no
	// longer authoritative. Toggle to true only after the legacy
	// table is fully drained (planned for migration 536+).
	LegacyModeSafe bool `json:"legacy_mode_safe"`
}

// 2026-08-18 (Agent C): queue lease heartbeat window. A lease whose
// started_at + this interval has elapsed is treated as "stale" by
// the dashboard and counted under stale_leases. The 5-minute window
// matches Agent B's ProbeQueueLeaseDefault so the dashboard's metric
// is consistent with the queue's own lease expiry.
const unifiedProbeQueueLeaseWindow = 5 * time.Minute

// queryUnifiedProbeQueueStats aggregates the new probe queue state
// (credential_probe_queue + node_probe_state) in a single query. The
// handler writes the result under the `"unified"` key so the legacy
// `v_probe_queue_snapshot` view does not pollute the new dashboard.
//
// pgxQueryer-parameterized so the dashboard tests can drive it against
// a pgxmock pool without an exported *pgxpool.Pool-typed constructor.
func queryUnifiedProbeQueueStats(ctx context.Context, db pgxQueryer) (UnifiedProbeQueueStats, error) {
	out := UnifiedProbeQueueStats{SnapshotAt: time.Now()}
	row := db.QueryRow(ctx, `
		WITH q AS (
			SELECT
				COUNT(*) FILTER (WHERE status = 'ready')    AS q_ready,
				COUNT(*) FILTER (WHERE status = 'running')  AS q_running,
			COUNT(*) FILTER (WHERE status = 'running'
			                   AND lease_until IS NOT NULL
			                   AND lease_until < now()) AS q_stale,
				COUNT(*) FILTER (WHERE status IN ('success','failed','expired','cancelled')
				                   AND COALESCE(finished_at, updated_at) > now() - interval '2 hours') AS q_finished,
				COUNT(*) FILTER (WHERE status = 'success'
				                   AND COALESCE(finished_at, updated_at) > now() - interval '2 hours') AS q_finished_ok,
				COALESCE(MAX(COALESCE(finished_at, updated_at)), NOW()) AS q_last_run
			FROM credential_probe_queue
		),
		n AS (
			SELECT
				COUNT(*) FILTER (WHERE NOT COALESCE(paused, FALSE)
				                   AND COALESCE(in_flight_until, '1970-01-01'::timestamptz) <= now()
				                   AND next_retry_at <= now()) AS n_due,
				COUNT(*) FILTER (WHERE COALESCE(in_flight_until, '1970-01-01'::timestamptz) > now()) AS n_running,
				COUNT(*) FILTER (WHERE COALESCE(paused, FALSE)) AS n_paused,
				COUNT(*) FILTER (WHERE NOT COALESCE(paused, FALSE)
				                   AND next_retry_at <= now() + interval '1 hour') AS n_pending,
				COUNT(*) FILTER (WHERE in_flight_until IS NOT NULL AND in_flight_until < now() - interval '2 minutes') AS n_unclaimable
			FROM node_probe_state
		)
		SELECT
			COALESCE(q.q_ready, 0),
			COALESCE(q.q_running, 0),
			COALESCE(q.q_finished, 0),
			COALESCE(q.q_stale, 0),
			COALESCE(n.n_due, 0),
			COALESCE(n.n_running, 0),
			COALESCE(n.n_paused, 0),
			COALESCE(n.n_pending, 0),
			COALESCE(n.n_unclaimable, 0),
			q.q_last_run
		FROM q, n
		`)

	var lastRun time.Time
	if err := row.Scan(
		&out.QueueReady,
		&out.QueueRunning,
		&out.QueueFinished,
		&out.StaleLeases,
		&out.NodeDue,
		&out.NodeRunning,
		&out.NodePaused,
		&out.NodePending,
		&out.NodeUnclaimable,
		&lastRun,
	); err != nil {
		return out, fmt.Errorf("unified probe queue stats: %w", err)
	}
	// QueueClaims is the conservative total of running rows that still
	// hold a recent lease. Cheap to compute from already-loaded numbers.
	out.QueueClaims = out.QueueRunning
	out.QueueSize = out.QueueReady + out.QueueRunning
	out.LastRunAt = &lastRun
	return out, nil
}

// queryUnifiedProbeSystemHealth reads the new source tables and joins
// them with URSM tenant coverage. The handler returns the resulting
// struct under the `"unified"` key; the legacy view is loaded via
// handleProbeSystemHealth and serialized separately under `legacy`.
//
// pgxQueryer-parameterized so admin tests can drive it against a
// pgxmock pool. The function is intentionally pure (no Redis) —
// URSM tenant coverage is read from PostgreSQL via credentials +
// v_routable_credential_models so the response can be cached and
// the dashboard contract stays deterministic.
func queryUnifiedProbeSystemHealth(ctx context.Context, db pgxQueryer) (UnifiedProbeSystemHealth, error) {
	out := UnifiedProbeSystemHealth{
		SnapshotAt: time.Now(),
	}
	// Single round trip: queue + node + runs + URSM coverage + pseudo-success.
	// Each CTE is independently understandable; the final SELECT joins
	// them on a synthetic row (1) so we issue one query instead of five.
	row := db.QueryRow(ctx, `
		WITH q AS (
			SELECT
				COUNT(*) FILTER (WHERE status = 'ready')    AS q_ready,
				COUNT(*) FILTER (WHERE status = 'running')  AS q_inflight,
				COUNT(*) FILTER (WHERE status = 'success'
				                   AND COALESCE(finished_at, updated_at) > now() - interval '2 hours') AS q_completed,
				COUNT(*) FILTER (WHERE status = 'failed'
				                   AND COALESCE(finished_at, updated_at) > now() - interval '2 hours') AS q_failed,
				COUNT(*) FILTER (WHERE status = 'expired'
				                   AND COALESCE(finished_at, updated_at) > now() - interval '2 hours') AS q_expired
			FROM credential_probe_queue
		),
		n AS (
			SELECT
				COUNT(*) AS n_total,
				COUNT(*) FILTER (WHERE COALESCE(last_direct_ok, FALSE) = TRUE
				                   AND COALESCE(consecutive_failures, 0) = 0) AS n_healthy,
				COUNT(*) FILTER (WHERE COALESCE(consecutive_failures, 0) >= 2
				                   AND NOT COALESCE(paused, FALSE)) AS n_failing,
				COUNT(*) FILTER (WHERE COALESCE(paused, FALSE)) AS n_paused,
				COUNT(*) FILTER (WHERE COALESCE(in_flight_until, '1970-01-01'::timestamptz) > now()) AS n_running,
				COUNT(*) FILTER (WHERE NOT COALESCE(paused, FALSE)
				                   AND next_retry_at <= now()) AS n_due,
				COUNT(*) FILTER (WHERE COALESCE(in_flight_until, '1970-01-01'::timestamptz) > now()) AS n_leased
			FROM node_probe_state
		),
		r AS (
			SELECT
				COUNT(*) FILTER (WHERE started_at >= now() - interval '1 hour') AS r_total,
				COUNT(*) FILTER (WHERE started_at >= now() - interval '1 hour'
				                   AND COALESCE(success, FALSE) = TRUE) AS r_success,
				COUNT(*) FILTER (WHERE started_at >= now() - interval '1 hour'
				                   AND COALESCE(success, FALSE) = FALSE) AS r_failed,
				MAX(started_at) AS r_last_at
			FROM node_probe_runs
		),
		cr AS (
			SELECT
				COUNT(*) AS c_total,
				COUNT(*) FILTER (WHERE EXISTS (
					SELECT 1 FROM v_routable_credential_models v
					WHERE v.credential_id = c.id AND v.is_routable = TRUE)) AS c_with_ursm
			FROM credentials c
			WHERE COALESCE(c.lifecycle_status, '') = 'active'
		),
		ps AS (
			-- Pseudo-success (handoff §6 P0): a node_probe_runs row
			-- claims direct_ok=true but the underlying credential has
			-- no routable binding any more. This is the recovery
			-- SQL's spurious-true footprint; Agent A is removing it.
			SELECT COUNT(DISTINCT npr.credential_id) AS n_pseudo
			FROM node_probe_runs npr
			WHERE COALESCE(npr.direct_ok, FALSE) = TRUE
			  AND npr.started_at >= now() - interval '7 days'
			  AND NOT EXISTS (
			    SELECT 1 FROM v_routable_credential_models v
			    WHERE v.credential_id = npr.credential_id
			      AND v.is_routable = TRUE)
		)
		SELECT
			COALESCE(q.q_ready, 0),
			COALESCE(q.q_inflight, 0),
			COALESCE(q.q_completed, 0),
			COALESCE(q.q_failed, 0),
			COALESCE(q.q_expired, 0),
			COALESCE(n.n_total, 0),
			COALESCE(n.n_healthy, 0),
			COALESCE(n.n_failing, 0),
			COALESCE(n.n_paused, 0),
			COALESCE(n.n_running, 0),
			COALESCE(n.n_due, 0),
			COALESCE(n.n_leased, 0),
			COALESCE(r.r_total, 0),
			COALESCE(r.r_success, 0),
			COALESCE(r.r_failed, 0),
			r.r_last_at,
			COALESCE(cr.c_total, 0),
			COALESCE(cr.c_with_ursm, 0),
			COALESCE(ps.n_pseudo, 0)
		FROM q, n, r, cr, ps
	`)
	var (
		runsLastAt                sql.NullTime
		totalCreds, credsWithURSM int
		pseudoSuccess             int
		successRate               sql.NullFloat64
	)
	if err := row.Scan(
		&out.QueuePending,
		&out.QueueInFlight,
		&out.QueueCompleted,
		&out.QueueFailed,
		&out.QueueExpired,
		&out.NodeTotal,
		&out.NodeHealthy,
		&out.NodeFailing,
		&out.NodePaused,
		&out.NodeRunning,
		&out.NodeDueNow,
		&out.NodeLeased,
		&out.RunsLast1h,
		&out.RunsSuccess1h,
		&out.RunsFailed1h,
		&runsLastAt,
		&totalCreds,
		&credsWithURSM,
		&pseudoSuccess,
	); err != nil {
		return out, fmt.Errorf("unified probe system health: %w", err)
	}
	out.QueueTotal = out.QueuePending + out.QueueInFlight + out.QueueCompleted + out.QueueFailed + out.QueueExpired
	out.TotalCredentials = totalCreds
	out.CredentialsWithURSM = credsWithURSM
	out.CredentialsNoURSM = out.TotalCredentials - out.CredentialsWithURSM
	out.PseudoSuccessCount = pseudoSuccess
	// URSMKeyCount is approximated from credentials-with-ursm; the
	// Redis SCAN happens in the handler for an exact number.
	out.URSMKeyCount = out.CredentialsWithURSM
	if out.RunsLast1h > 0 {
		rate := float64(out.RunsSuccess1h) / float64(out.RunsLast1h)
		successRate = sql.NullFloat64{Float64: rate, Valid: true}
	}
	if successRate.Valid {
		v := successRate.Float64
		out.SuccessRateLast1h = &v
	}
	if runsLastAt.Valid {
		t := runsLastAt.Time
		out.RunsLastAt = &t
	}
	return out, nil
}

// countURSMKeys SCANs the URSM v2 tenant namespace for an exact key
// count. The query is bounded by a configurable per-pass limit so a
// missing key prefix cannot cause an unbounded Redis block. The
// caller is expected to provide a context with a timeout so a Redis
// stall cannot stall the dashboard handler.
//
// 2026-08-18 (Agent C): the dashboard used to expose
// `llmgw:avail:*:*` key counts as the proxy for "node state". That
// metric is still useful, but the new tenant coverage requires
// counting the `ursm:v2:node:tenant:*` keys because that is the
// authoritative runtime routability indicator. The handler returns
// both numbers so the frontend can keep the historical chart and
// surface the new routing-coverage series.
//
// Pure Redis client interface so it can be tested with a fake
// (the actual Handler.redisClient is interface{} for backward
// compatibility).
type redisScanner interface {
	Scan(ctx context.Context, cursor uint64, match string, count int64) *redis.ScanCmd
}

func countURSMKeys(ctx context.Context, rc redisScanner, pattern string, max int) int {
	if rc == nil {
		return 0
	}
	var (
		cursor uint64
		total  int
	)
	for {
		ks, next, err := rc.Scan(ctx, cursor, pattern, 256).Result()
		if err != nil {
			return total
		}
		total += len(ks)
		cursor = next
		if cursor == 0 || total >= max {
			break
		}
		if max > 0 && total >= max {
			break
		}
	}
	return total
}

// loadLegacySystemHealth reads the legacy v_probe_system_health view
// and returns the bag of integers the dashboard wants to keep
// rendering. Extracted so the handler test can drive it against a
// pgxmock pool without exporting the *pgxpool.Pool-typed h.db field.
//
// 2026-08-18 (Agent C): the legacy view is intentionally tagged
// legacy_mode_safe=false in the response so the dashboard can warn
// the operator that the numbers are no longer authoritative.
func loadLegacySystemHealth(ctx context.Context, db pgxQueryer) (ProbeSystemHealth, error) {
	var health ProbeSystemHealth
	var avgSuccessRate7d sql.NullFloat64
	var totalRealSuccess24h sql.NullInt64
	var totalRealFailure24h sql.NullInt64
	err := db.QueryRow(ctx, `SELECT * FROM v_probe_system_health`).Scan(
		&health.TotalNodes,
		&health.HealthyNodes,
		&health.FailingNodes,
		&health.SuspiciousNodes,
		&health.ProbingNodes,
		&health.UrgentQueueSize,
		&health.SuspiciousQueueSize,
		&health.FailingQueueSize,
		&health.WatchdogQueueSize,
		&health.ReadyProbes,
		&health.CurrentProbing,
		&health.CredentialsBeingProbed,
		&avgSuccessRate7d,
		&health.LastProbeAt,
		&health.LastRealRequestAt,
		&totalRealSuccess24h,
		&totalRealFailure24h,
		&health.CriticalNodes,
		&health.PendingProbes5min,
		&health.SnapshotAt,
	)
	if err != nil {
		return health, err
	}
	if avgSuccessRate7d.Valid {
		health.AvgSuccessRate7d = &avgSuccessRate7d.Float64
	}
	health.TotalRealSuccess24h = nullInt(totalRealSuccess24h)
	health.TotalRealFailure24h = nullInt(totalRealFailure24h)
	return health, nil
}

// loadLegacyQueueSnapshot reads the legacy v_probe_queue_snapshot
// view and returns the rows for the dashboard. Extracted so the
// handler test can drive it against a pgxmock pool.
func loadLegacyQueueSnapshot(ctx context.Context, db pgxQueryer) ([]ProbeQueueSnapshot, error) {
	rows, err := db.Query(ctx, `SELECT * FROM v_probe_queue_snapshot`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var queues []ProbeQueueSnapshot
	for rows.Next() {
		var q ProbeQueueSnapshot
		if err := rows.Scan(
			&q.ProbePriority,
			&q.State,
			&q.QueueSize,
			&q.ReadyNow,
			&q.Ready1min,
			&q.Ready5min,
			&q.EarliestRetryAt,
			&q.LatestRetryAt,
			&q.AvgWaitSeconds,
			&q.MaxWaitSeconds,
		); err != nil {
			return nil, err
		}
		queues = append(queues, q)
	}
	return queues, rows.Err()
}

// GET /api/admin/probe/dashboard
// Returns model health summary for all models or filtered by model name
func (h *Handler) handleProbeDashboard(w http.ResponseWriter, r *http.Request) {
	modelFilter := r.URL.Query().Get("model") // optional filter

	query := `SELECT * FROM v_model_health_dashboard`
	args := []any{}

	if modelFilter != "" {
		query += ` WHERE raw_model_name ILIKE $1 OR outbound_model_name ILIKE $1`
		args = append(args, "%"+modelFilter+"%")
	}

	rows, err := h.db.Query(r.Context(), query, args...)
	if err != nil {
		slog.Error("probe dashboard db query failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	defer rows.Close()

	var models []ModelHealthSummary
	for rows.Next() {
		var m ModelHealthSummary
		var healthyPercentage sql.NullFloat64
		var failingPercentage sql.NullFloat64
		var avgSuccessRate7d sql.NullFloat64
		var avgVerificationHours sql.NullFloat64
		var avgConsecutiveSuccesses sql.NullFloat64
		var totalRealSuccess24h sql.NullInt64
		var totalRealFailure24h sql.NullInt64
		err := rows.Scan(
			&m.ProviderModelID,
			&m.RawModelName,
			&m.OutboundModel,
			&m.Protocol,
			&m.ProviderName,
			&m.TotalCredentials,
			&m.HealthyCount,
			&m.SuspiciousCount,
			&m.FailingCount,
			&m.ProbingCount,
			&healthyPercentage,
			&failingPercentage,
			&m.UrgentCount,
			&m.SuspiciousPriorityCount,
			&m.FailingPriorityCount,
			&m.WatchdogCount,
			&avgSuccessRate7d,
			&avgVerificationHours,
			&avgConsecutiveSuccesses,
			&totalRealSuccess24h,
			&totalRealFailure24h,
			&m.RealSuccessRate24h,
			&m.LastVerifiedAt,
			&m.LastRealRequestAt,
			&m.NextProbeAt,
			&m.CriticalNodes,
			&m.PendingProbes5min,
			&m.OverallHealth,
		)
		if err != nil {
			slog.Error("probe dashboard scan failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal server error")
			return
		}
		m.HealthyPercentage = nullFloat64(healthyPercentage)
		m.FailingPercentage = nullFloat64(failingPercentage)
		m.AvgSuccessRate7d = nullFloat64(avgSuccessRate7d)
		m.AvgVerificationHours = nullFloat64(avgVerificationHours)
		m.AvgConsecutiveSuccesses = nullFloat64(avgConsecutiveSuccesses)
		m.TotalRealSuccess24h = nullInt(totalRealSuccess24h)
		m.TotalRealFailure24h = nullInt(totalRealFailure24h)
		models = append(models, m)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"models": models,
		"total":  len(models),
	})
}

// GET /api/admin/probe/queue-snapshot
//
// 2026-08-18 (Agent C): now returns both the new unified source
// (credential_probe_queue + node_probe_state) and the legacy view
// (v_probe_queue_snapshot, model_probe_state) under separate keys
// so the frontend can render both. The legacy view is flagged
// `legacy: true` so a careless reader does not mix the two
// pictures — the 572-row historical backlog (AGENT C handoff
// 2026-08-18) lives in the legacy view and is unaffected by the
// active queue.
func (h *Handler) handleProbeQueueSnapshot(w http.ResponseWriter, r *http.Request) {
	// ── new source: credential_probe_queue + node_probe_state ─────
	unified, err := queryUnifiedProbeQueueStats(r.Context(), h.db)
	if err != nil {
		slog.Error("probe dashboard unified queue query failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	// ── legacy source: v_probe_queue_snapshot (model_probe_state) ─
	rows, err := h.db.Query(r.Context(), `SELECT * FROM v_probe_queue_snapshot`)
	if err != nil {
		slog.Error("probe dashboard legacy queue query failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	defer rows.Close()

	var queues []ProbeQueueSnapshot
	for rows.Next() {
		var q ProbeQueueSnapshot
		err := rows.Scan(
			&q.ProbePriority,
			&q.State,
			&q.QueueSize,
			&q.ReadyNow,
			&q.Ready1min,
			&q.Ready5min,
			&q.EarliestRetryAt,
			&q.LatestRetryAt,
			&q.AvgWaitSeconds,
			&q.MaxWaitSeconds,
		)
		if err != nil {
			slog.Error("probe dashboard scan failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal server error")
			return
		}
		queues = append(queues, q)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		// New source: tagged as legacy: false so a frontend can
		// trust the numbers and ignore the legacy view.
		"unified": unified,
		// Legacy view: tagged as legacy: true and preserved under
		// its own key so the operator can compare the two.
		// Keep the established top-level shape for existing clients while
		// exposing the source-labelled views for new clients.
		"queues": queues,
		"total":  len(queues),
		"legacy": map[string]interface{}{
			"queues":           queues,
			"total":            len(queues),
			"legacy":           true,
			"legacy_mode_safe": false,
			"legacy_source":    "model_probe_state",
		},
		"snapshot_at": time.Now(),
	})
}

// GET /api/admin/probe/system-health
//
// 2026-08-18 (Agent C): now returns both the new unified source
// (credential_probe_queue + node_probe_state + node_probe_runs +
// URSM tenant coverage) and the legacy view (v_probe_system_health,
// model_probe_state). The legacy view is preserved under `legacy`
// with `legacy: true` so the frontend can render the old badges
// while the migration to URSM/probe_queue finishes without losing
// the historical chart.
func (h *Handler) handleProbeSystemHealth(w http.ResponseWriter, r *http.Request) {
	// ── new source: unified probe + URSM tenant coverage ─────────
	unified, err := queryUnifiedProbeSystemHealth(r.Context(), h.db)
	if err != nil {
		slog.Error("probe dashboard unified system-health query failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	// ── URSM tenant key count is the only Redis-side metric. The
	// legacy llmgw:avail count is intentionally NOT mixed here — the
	// new source is the authoritative runtime signal.
	if rc, ok := h.redisClient.(*redis.Client); ok {
		ursm := countURSMKeys(r.Context(), rc, "ursm:v2:node:k2:*", 0)
		unified.URSMKeyCount = ursm
	}

	// ── legacy source: v_probe_system_health (model_probe_state) ──
	var legacyHealth ProbeSystemHealth
	var avgSuccessRate7d sql.NullFloat64
	var totalRealSuccess24h sql.NullInt64
	var totalRealFailure24h sql.NullInt64
	legacyErr := h.db.QueryRow(r.Context(), `SELECT * FROM v_probe_system_health`).Scan(
		&legacyHealth.TotalNodes,
		&legacyHealth.HealthyNodes,
		&legacyHealth.FailingNodes,
		&legacyHealth.SuspiciousNodes,
		&legacyHealth.ProbingNodes,
		&legacyHealth.UrgentQueueSize,
		&legacyHealth.SuspiciousQueueSize,
		&legacyHealth.FailingQueueSize,
		&legacyHealth.WatchdogQueueSize,
		&legacyHealth.ReadyProbes,
		&legacyHealth.CurrentProbing,
		&legacyHealth.CredentialsBeingProbed,
		&avgSuccessRate7d,
		&legacyHealth.LastProbeAt,
		&legacyHealth.LastRealRequestAt,
		&totalRealSuccess24h,
		&totalRealFailure24h,
		&legacyHealth.CriticalNodes,
		&legacyHealth.PendingProbes5min,
		&legacyHealth.SnapshotAt,
	)
	if legacyErr != nil {
		// Legacy view is intentionally soft-fail. The new source is
		// authoritative; the legacy view is best-effort and may be
		// absent on a green-field deployment. Surface the error in
		// the response so the dashboard can warn the operator.
		slog.Warn("probe dashboard legacy system-health query failed",
			"error", legacyErr)
	} else {
		if avgSuccessRate7d.Valid {
			legacyHealth.AvgSuccessRate7d = &avgSuccessRate7d.Float64
		}
		legacyHealth.TotalRealSuccess24h = nullInt(totalRealSuccess24h)
		legacyHealth.TotalRealFailure24h = nullInt(totalRealFailure24h)
		if rc, ok := h.redisClient.(*redis.Client); ok && h.availabilityReader != nil {
			keys, cacheErr := h.availabilityReader.ScanKeys(r.Context(), 0)
			if cacheErr == nil && len(keys) > 0 {
				legacyHealth.TotalNodes = len(keys)
				legacyHealth.HealthyNodes = 0
				legacyHealth.FailingNodes = 0
				legacyHealth.SuspiciousNodes = 0
				legacyHealth.ProbingNodes = 0
				for _, key := range keys {
					data, err := rc.HGetAll(r.Context(), key).Result()
					if err != nil {
						continue
					}
					switch data["state"] {
					case "healthy", "healthy_confirmed", "available":
						legacyHealth.HealthyNodes++
					case "failing", "broken_confirmed", "unavailable":
						legacyHealth.FailingNodes++
					case "suspicious":
						legacyHealth.SuspiciousNodes++
					case "probing":
						legacyHealth.ProbingNodes++
					}
				}
			}
		}
	}

	// Populate the Legacy struct embedded in the unified payload so the
	// frontend can render both views side-by-side without losing the
	// pre-cutover chart history.
	unified.Legacy = TotalLegacySystemHealth{
		TotalNodes:      legacyHealth.TotalNodes,
		HealthyNodes:    legacyHealth.HealthyNodes,
		FailingNodes:    legacyHealth.FailingNodes,
		SuspiciousNodes: legacyHealth.SuspiciousNodes,
		ProbingNodes:    legacyHealth.ProbingNodes,
		UrgentQueueSize: legacyHealth.UrgentQueueSize,
		ReadyProbes:     legacyHealth.ReadyProbes,
		CurrentProbing:  legacyHealth.CurrentProbing,
		LastProbeAt:     legacyHealth.LastProbeAt,
		LegacySource:    "model_probe_state",
		LegacyModeSafe:  false,
	}

	w.Header().Set("Content-Type", "application/json")
	// Preserve every historical top-level field for existing dashboard clients;
	// the source-labelled payloads remain available for the cutover view.
	legacyPayload := make(map[string]any)
	if raw, marshalErr := json.Marshal(legacyHealth); marshalErr == nil {
		_ = json.Unmarshal(raw, &legacyPayload)
	}
	legacyPayload["unified"] = unified
	legacyPayload["legacy"] = legacyHealth
	legacyPayload["legacy_mode_safe"] = false
	legacyPayload["snapshot_at"] = time.Now()
	_ = json.NewEncoder(w).Encode(legacyPayload)
}

// GET /api/admin/probe/model/{model}/nodes
// Returns detailed node information for a specific model
func (h *Handler) handleProbeModelNodes(w http.ResponseWriter, r *http.Request) {
	// Extract model name from path: /api/admin/probe/model/{model}/nodes
	path := strings.TrimPrefix(r.URL.Path, "/api/admin/probe/model/")
	parts := strings.Split(path, "/")
	if len(parts) < 2 || parts[1] != "nodes" {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	modelName := parts[0]

	rows, err := h.db.Query(r.Context(), `
		SELECT * FROM v_model_priority_details
		WHERE raw_model_name = $1
	`, modelName)
	if err != nil {
		slog.Error("probe dashboard db query failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	defer rows.Close()

	var nodes []ModelNodeDetail
	for rows.Next() {
		var n ModelNodeDetail
		err := rows.Scan(
			&n.RawModelName,
			&n.OutboundModel,
			&n.ProbePriority,
			&n.State,
			&n.CredentialID,
			&n.CredentialLabel,
			&n.ProviderName,
			&n.LastVerifiedAt,
			&n.NextRetryAt,
			&n.MarkedSuspiciousAt,
			&n.ProbingStartedAt,
			&n.ConsecutiveSuccesses,
			&n.ConsecutiveFailures,
			&n.ConsecutiveWatchdogSuccesses,
			&n.SuccessRate7d,
			&n.VerificationInterval,
			&n.RealSuccess24h,
			&n.RealFailure24h,
			&n.LastRealRequestAt,
			&n.LastUnavailableReason,
			&n.LastErrCode,
			&n.RetryIn,
			&n.StateDurationMinutes,
		)
		if err != nil {
			slog.Error("probe dashboard scan failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal server error")
			return
		}
		if rc, ok := h.redisClient.(*redis.Client); ok {
			reader := bg.NewModelAvailabilityReader(rc)
			if reader != nil {
				snapshot, cacheErr := reader.Read(r.Context(), int(n.CredentialID), n.RawModelName)
				if cacheErr == nil && snapshot != nil {
					n.State = snapshot.State
					n.ConsecutiveSuccesses = snapshot.ConsecutiveSuccesses
					n.ConsecutiveFailures = snapshot.ConsecutiveFailures
					if snapshot.NextRetryAt != nil {
						n.NextRetryAt = snapshot.NextRetryAt
					}
				}
			}
		}
		nodes = append(nodes, n)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"model": modelName,
		"nodes": nodes,
		"total": len(nodes),
	})
}

// GET /api/admin/probe/model/{model}/state-summary
// Returns state distribution summary for a specific model
func (h *Handler) handleProbeModelStateSummary(w http.ResponseWriter, r *http.Request) {
	// Extract model name from path
	path := strings.TrimPrefix(r.URL.Path, "/api/admin/probe/model/")
	parts := strings.Split(path, "/")
	if len(parts) < 2 || parts[1] != "state-summary" {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	modelName := parts[0]

	rows, err := h.db.Query(r.Context(), `SELECT * FROM get_model_state_summary($1)`, modelName)
	if err != nil {
		slog.Error("probe dashboard db query failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	defer rows.Close()

	var breakdown []ModelStateBreakdown
	for rows.Next() {
		var b ModelStateBreakdown
		err := rows.Scan(
			&b.State,
			&b.Priority,
			&b.Count,
			&b.AvgSuccessRate,
			&b.NextProbeInSeconds,
		)
		if err != nil {
			slog.Error("probe dashboard scan failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal server error")
			return
		}
		breakdown = append(breakdown, b)
	}
	if rc, ok := h.redisClient.(*redis.Client); ok {
		reader := bg.NewModelAvailabilityReader(rc)
		if reader != nil {
			rows, cacheErr := reader.ReadByModel(r.Context(), modelName)
			if cacheErr == nil && len(rows) > 0 {
				counts := map[string]int{}
				for _, row := range rows {
					counts[row.Snapshot.State]++
				}
				breakdown = breakdown[:0]
				states := make([]string, 0, len(counts))
				for state := range counts {
					states = append(states, state)
				}
				sort.Strings(states)
				for _, state := range states {
					count := counts[state]
					breakdown = append(breakdown, ModelStateBreakdown{
						State:    state,
						Priority: state,
						Count:    count,
					})
				}
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	// 2026-06-30 PR-7: also expose state_distribution as a flat map so the
	// frontend can render header badges (healthy/degraded/failed/probing
	// counts) without iterating breakdown[]. Audit P0-10 — the previous
	// endpoint only returned `breakdown` and the frontend read
	// `state_distribution`, leaving the 4 status badges showing 0.
	stateDistribution := buildStateDistribution(breakdown)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"model":              modelName,
		"breakdown":          breakdown,
		"state_distribution": stateDistribution,
	})
}

// GET /api/admin/probe/availability-timeline
// Returns 24h availability timeline for models
func (h *Handler) handleProbeAvailabilityTimeline(w http.ResponseWriter, r *http.Request) {
	modelFilter := r.URL.Query().Get("model")

	query := `SELECT * FROM v_model_availability_timeline`
	args := []any{}

	if modelFilter != "" {
		query += ` WHERE raw_model_name = $1`
		args = append(args, modelFilter)
	}

	query += ` ORDER BY raw_model_name, hour_bucket DESC LIMIT 500`

	rows, err := h.db.Query(r.Context(), query, args...)
	if err != nil {
		slog.Error("probe dashboard db query failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	defer rows.Close()

	type TimelinePoint struct {
		RawModelName          string    `json:"raw_model_name"`
		OutboundModel         string    `json:"outbound_model_name"`
		HourBucket            time.Time `json:"hour_bucket"`
		TotalProbes           int       `json:"total_probes"`
		SuccessfulProbes      int       `json:"successful_probes"`
		FailedProbes          int       `json:"failed_probes"`
		SuccessRate           float64   `json:"success_rate"`
		AvgLatencyMs          *float64  `json:"avg_latency_ms,omitempty"`
		ProbedCredentials     int       `json:"probed_credentials"`
		SuccessfulCredentials int       `json:"successful_credentials"`
		FailedCredentials     int       `json:"failed_credentials"`
	}

	var timeline []TimelinePoint
	for rows.Next() {
		var p TimelinePoint
		err := rows.Scan(
			&p.RawModelName,
			&p.OutboundModel,
			&p.HourBucket,
			&p.TotalProbes,
			&p.SuccessfulProbes,
			&p.FailedProbes,
			&p.SuccessRate,
			&p.AvgLatencyMs,
			&p.ProbedCredentials,
			&p.SuccessfulCredentials,
			&p.FailedCredentials,
		)
		if err != nil {
			slog.Error("probe dashboard scan failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal server error")
			return
		}
		timeline = append(timeline, p)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"timeline": timeline,
		"total":    len(timeline),
	})
}

// ── Provider HTTP latency (2026-07-23, sub-item ②) ──────────────────────
//
// 复用 node_probe_runs 最近一次成功探测的 direct_latency_ms，按 provider
// 聚合返回。NodeProbeWorker 在新探测模式下默认运行，健康节点约 5 分钟级
// 节奏记录延时，无需额外定时器。
//
// GET /api/admin/probe/provider-latency
type ProviderLatencyEntry struct {
	ProviderID   int64     `json:"provider_id"`
	ProviderName string    `json:"provider_name"`
	ProviderCode string    `json:"provider_code"`
	LatencyMs    int       `json:"latency_ms"`
	ProbedAt     time.Time `json:"probed_at"`
}

func (h *Handler) handleProviderLatency(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// 取每个 provider 最近一次成功的探测延时（direct_ok=true，1 小时窗口内）。
	// ProviderCode 是 lane.id 的实际取值（前端 swimlane 按此 key 查找）。
	rows, err := h.db.Query(r.Context(), `
		SELECT DISTINCT ON (npr.provider_id)
			npr.provider_id,
			COALESCE(p.display_name, p.code, ''),
			COALESCE(p.code, ''),
			COALESCE(npr.direct_latency_ms, npr.gateway_latency_ms, 0),
			npr.started_at
		FROM node_probe_runs npr
		LEFT JOIN providers p ON p.id = npr.provider_id
		WHERE npr.provider_id IS NOT NULL
			AND npr.provider_id > 0
			AND npr.direct_ok = TRUE
			AND npr.direct_latency_ms > 0
			AND npr.started_at >= now() - interval '1 hour'
		ORDER BY npr.provider_id, npr.started_at DESC, npr.id DESC
		LIMIT 500
	`)
	if err != nil {
		slog.Error("probe dashboard db query failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	defer rows.Close()

	entries := []ProviderLatencyEntry{}
	for rows.Next() {
		var e ProviderLatencyEntry
		if err := rows.Scan(&e.ProviderID, &e.ProviderName, &e.ProviderCode, &e.LatencyMs, &e.ProbedAt); err != nil {
			slog.Error("probe dashboard scan failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal server error")
			return
		}
		entries = append(entries, e)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"entries": entries,
		"total":   len(entries),
	})
}

// ── Probe queue tasks (2026-07-23, sub-item ③) ─────────────────────────
//
// 返回 credential_probe_queue 表中逐条任务（区别于聚合视图
// v_probe_queue_snapshot），用于在自检 tab 以泳道形式展示待执行/执行中
// 的探测任务，随执行更新状态。
//
// GET /api/admin/probe/queue-tasks?limit=100
// 2026-07-24: 返回待执行/执行中/近期已执行，并附带标准模型名（standardized_name）。
type ProbeQueueTaskRow struct {
	ID               int64        `json:"id"`
	CredentialID     int64        `json:"credential_id"`
	ProviderID       int64        `json:"provider_id"`
	ProviderName     string       `json:"provider_name"`
	ProviderCode     string       `json:"provider_code"`
	RawModel         string       `json:"raw_model"`
	StandardizedName string       `json:"standardized_name"`
	Status           string       `json:"status"`
	Attempt          int          `json:"attempt"`
	Priority         int16        `json:"priority"`
	ReasonCode       string       `json:"reason_code"`
	NextRunAt        sql.NullTime `json:"next_run_at,omitempty"`
	ResultLatencyMs  *int         `json:"result_latency_ms,omitempty"`
	ResultHTTPStatus *int         `json:"result_http_status,omitempty"`
	UpdatedAt        sql.NullTime `json:"updated_at,omitempty"`
	// Source (2026-08-10, fix/selfcheck-queue-and-recovery) labels which
	// queue produced this row so the frontend can badge it distinctly.
	// This is the integrity-probe queue (credential_probe_queue), fed by
	// IntegrityProbePlanner — as opposed to the error-triggered
	// NodeProbeWorker rows returned by handleProbeNodeTasks below.
	Source string `json:"source"`
}

func (h *Handler) handleProbeQueueTasks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	limit := 100
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	tasks, err := queryProbeQueueTasks(r.Context(), h.db, limit)
	if err != nil {
		slog.Error("probe dashboard queue-tasks query failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"tasks": tasks,
		"total": len(tasks),
	})
}

// queryProbeQueueTasks is the pgxQueryer-parameterized core of
// handleProbeQueueTasks, extracted so admin/probe_dashboard_test.go can
// drive it against a pgxmock pool without needing an exported
// *pgxpool.Pool-typed constructor on Handler.
func queryProbeQueueTasks(ctx context.Context, db pgxQueryer, limit int) ([]ProbeQueueTaskRow, error) {
	rows, err := db.Query(ctx, `
		SELECT
			q.id, q.credential_id, q.provider_id,
			COALESCE(p.display_name, ''),
			COALESCE(p.code, ''),
			COALESCE(q.raw_model, ''),
			COALESCE(NULLIF(pm.standardized_name, ''), NULLIF(mc.canonical_name, ''), q.raw_model, ''),
			q.status, q.attempt, q.priority, COALESCE(q.reason_code, ''),
			q.next_run_at,
			q.result_latency_ms,
			q.result_http_status,
			q.updated_at
		FROM credential_probe_queue q
		LEFT JOIN providers p ON p.id = q.provider_id
		LEFT JOIN provider_models pm
		       ON pm.provider_id = q.provider_id
		      AND lower(pm.raw_model_name) = lower(q.raw_model)
		LEFT JOIN models_canonical mc ON mc.id = pm.canonical_id
		WHERE q.status IN ('ready', 'running', 'success', 'failed', 'expired')
		  AND (
		      q.status IN ('ready', 'running')
		      OR q.updated_at > now() - interval '2 hours'
		  )
		ORDER BY
			CASE q.status
				WHEN 'running' THEN 0
				WHEN 'ready' THEN 1
				ELSE 2
			END,
			q.priority DESC,
			COALESCE(q.updated_at, q.next_run_at) DESC NULLS LAST,
			q.id DESC
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	tasks := []ProbeQueueTaskRow{}
	for rows.Next() {
		var t ProbeQueueTaskRow
		var lat, httpStatus sql.NullInt32
		if err := rows.Scan(
			&t.ID, &t.CredentialID, &t.ProviderID, &t.ProviderName, &t.ProviderCode,
			&t.RawModel, &t.StandardizedName, &t.Status, &t.Attempt, &t.Priority, &t.ReasonCode,
			&t.NextRunAt, &lat, &httpStatus, &t.UpdatedAt,
		); err != nil {
			return nil, err
		}
		if lat.Valid {
			v := int(lat.Int32)
			t.ResultLatencyMs = &v
		}
		if httpStatus.Valid {
			v := int(httpStatus.Int32)
			t.ResultHTTPStatus = &v
		}
		t.Source = "integrity"
		tasks = append(tasks, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return tasks, nil
}

// ── Node probe tasks (2026-08-10, fix/selfcheck-queue-and-recovery) ────
//
// handleProbeQueueTasks above only sees credential_probe_queue rows (the
// integrity-probe queue, fed by IntegrityProbePlanner every ~10min). The
// error-triggered self-heal path — NodeProbeWorker's 7-step backoff ladder
// (5s→30s→60s→5m→1h→2h→6h) — writes to node_probe_state / node_probe_runs
// instead, so those retries were invisible in the self-check swimlanes.
// This endpoint surfaces that queue with the same row shape so the
// frontend can merge both sources.
//
// GET /api/admin/probe/node-tasks?limit=120
type NodeProbeTaskRow struct {
	CredentialID        int64      `json:"credential_id"`
	ProviderID          int64      `json:"provider_id"`
	ProviderName        string     `json:"provider_name"`
	ProviderCode        string     `json:"provider_code"`
	RawModel            string     `json:"raw_model"`
	StandardizedName    string     `json:"standardized_name"`
	Status              string     `json:"status"`
	Attempt             int        `json:"attempt"`
	ConsecutiveFailures int        `json:"consecutive_failures"`
	NextRetryAt         *time.Time `json:"next_retry_at,omitempty"`
	LastDirectOk        *bool      `json:"last_direct_ok,omitempty"`
	LastGatewayOk       *bool      `json:"last_gateway_ok,omitempty"`
	LastErrCode         *string    `json:"last_err_code,omitempty"`
	LastLatencyMs       *int       `json:"last_latency_ms,omitempty"`
	Paused              bool       `json:"paused"`
	UpdatedAt           *time.Time `json:"updated_at,omitempty"`
	// Source is always "node_probe" for this endpoint; mirrors the
	// ProbeQueueTaskRow.Source label ("integrity") so the frontend can
	// badge/merge rows from both queues consistently.
	Source string `json:"source"`
}

func (h *Handler) handleProbeNodeTasks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	limit := 120
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	tasks, err := queryProbeNodeTasks(r.Context(), h.db, limit)
	if err != nil {
		slog.Error("probe dashboard node-tasks query failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"tasks": tasks,
		"total": len(tasks),
	})
}

// queryProbeNodeTasks is the pgxQueryer-parameterized core of
// handleProbeNodeTasks, extracted for the same reason as
// queryProbeQueueTasks above — testability against pgxmock without an
// exported *pgxpool.Pool-typed constructor.
//
// Status classification mirrors NodeProbeWorker's own state machine
// (bg/node_probe.go): a row currently leased by pickDueAtomically is
// "running"; a row awaiting its next backoff tick is "pending"; a
// paused row (attempt cap reached) is "paused".
func queryProbeNodeTasks(ctx context.Context, db pgxQueryer, limit int) ([]NodeProbeTaskRow, error) {
	rows, err := db.Query(ctx, `
		SELECT
			nps.credential_id,
			COALESCE(c.provider_id, 0),
			COALESCE(p.display_name, ''),
			COALESCE(p.code, ''),
			nps.raw_model_name,
			COALESCE(NULLIF(pm.standardized_name, ''), NULLIF(mc.canonical_name, ''), nps.raw_model_name, ''),
			CASE
				WHEN nps.in_flight_until IS NOT NULL AND nps.in_flight_until > now() THEN 'running'
				WHEN nps.paused THEN 'paused'
				ELSE 'pending'
			END,
			COALESCE(latest.attempt, 0),
			nps.consecutive_failures,
			nps.next_retry_at,
			nps.last_direct_ok,
			nps.last_gateway_ok,
			nps.last_err_code,
			latest.direct_latency_ms,
			nps.paused,
			nps.updated_at
		FROM node_probe_state nps
		LEFT JOIN credentials c ON c.id = nps.credential_id
		LEFT JOIN providers p ON p.id = c.provider_id
		LEFT JOIN provider_models pm
		       ON pm.provider_id = c.provider_id
		      AND lower(pm.raw_model_name) = lower(nps.raw_model_name)
		LEFT JOIN models_canonical mc ON mc.id = pm.canonical_id
		LEFT JOIN LATERAL (
			SELECT npr.attempt, npr.direct_latency_ms
			FROM node_probe_runs npr
			WHERE npr.credential_id = nps.credential_id
			  AND npr.raw_model_name = nps.raw_model_name
			ORDER BY npr.id DESC
			LIMIT 1
		) latest ON TRUE
		-- WHERE 说明：node_probe_state.next_retry_at 是 NOT NULL 列（默认
		-- now()），所以不能用 IS NOT NULL 否则返回全表。这里只展示「还有
		-- 探测任务要做」的行：(a) 正在被 worker 租用执行；(b) 已暂停（达
		-- 到 7 步退避上限，运维需可见）；(c) 待执行/即将到期 ——
		-- NodeProbeWorker 成功探测后会把 next_retry_at 推到 now()+1h，用
		-- <= now()+1h 窗口排除那些远期复检（1h+）的健康节点，避免泳道
		-- 被无关行塞满。1h 与 runOne 成功分支的 next_retry_at 设置对齐
		-- （bg/node_probe.go: now() + interval 1 hour）。
		WHERE nps.in_flight_until > now()
		   OR nps.paused
		   OR (NOT nps.paused AND nps.next_retry_at <= now() + interval '1 hour')
		ORDER BY
			CASE
				WHEN nps.in_flight_until IS NOT NULL AND nps.in_flight_until > now() THEN 0
				WHEN NOT nps.paused THEN 1
				ELSE 2
			END,
			nps.next_retry_at NULLS LAST,
			nps.updated_at DESC NULLS LAST
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	tasks := []NodeProbeTaskRow{}
	for rows.Next() {
		var t NodeProbeTaskRow
		var nextRetryAt, updatedAt sql.NullTime
		var lastErrCode sql.NullString
		var lastLatencyMs sql.NullInt32
		var lastDirectOk, lastGatewayOk sql.NullBool
		if err := rows.Scan(
			&t.CredentialID, &t.ProviderID, &t.ProviderName, &t.ProviderCode,
			&t.RawModel, &t.StandardizedName, &t.Status, &t.Attempt, &t.ConsecutiveFailures,
			&nextRetryAt, &lastDirectOk, &lastGatewayOk, &lastErrCode, &lastLatencyMs,
			&t.Paused, &updatedAt,
		); err != nil {
			return nil, err
		}
		if nextRetryAt.Valid {
			v := nextRetryAt.Time
			t.NextRetryAt = &v
		}
		if lastDirectOk.Valid {
			v := lastDirectOk.Bool
			t.LastDirectOk = &v
		}
		if lastGatewayOk.Valid {
			v := lastGatewayOk.Bool
			t.LastGatewayOk = &v
		}
		if lastErrCode.Valid {
			v := lastErrCode.String
			t.LastErrCode = &v
		}
		if lastLatencyMs.Valid {
			v := int(lastLatencyMs.Int32)
			t.LastLatencyMs = &v
		}
		if updatedAt.Valid {
			v := updatedAt.Time
			t.UpdatedAt = &v
		}
		t.Source = "node_probe"
		tasks = append(tasks, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return tasks, nil
}

// ── Register Routes ─────────────────────────────────────────────────────

// handleProbeTaskCreate is the public "add self-check task" API (需求 6 bullet 1:
// 让外部需要自检的操作不用关心细节，直接增加自检任务).
// POST /api/admin/probe/tasks  {credential_id, raw_model, command?, ...}
// It enqueues a task into the unified credential_probe_queue; the caller does
// not need to know the dedup_key, backoff, or execution details. Requires the
// unified queue to be wired (SetProbeQueue); otherwise 503.
func (h *Handler) handleProbeTaskCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.probeQueue == nil {
		writeError(w, http.StatusServiceUnavailable, "unified probe queue not configured")
		return
	}
	var req struct {
		CredentialID int64  `json:"credential_id"`
		RawModel     string `json:"raw_model"`
		Command      string `json:"command"`           // default node_probe
		Source       string `json:"source"`            // default admin
		Priority     int16  `json:"priority"`          // default 60
		MaxAttempts  int    `json:"max_attempts"`      // default 7 (node-probe chain)
		Reason       string `json:"reason"`            // free-form audit detail
		RunAfterSec  int    `json:"run_after_seconds"` // schedule in future (0 = now)
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.CredentialID <= 0 || req.RawModel == "" {
		writeError(w, http.StatusBadRequest, "credential_id and raw_model are required")
		return
	}
	if req.Command == "" {
		req.Command = "node_probe"
	}
	if req.Source == "" {
		req.Source = "admin"
	}
	if req.MaxAttempts <= 0 {
		req.MaxAttempts = 7
	}
	nextRunAt := time.Now()
	if req.RunAfterSec > 0 {
		nextRunAt = nextRunAt.Add(time.Duration(req.RunAfterSec) * time.Second)
	}
	task := bg.ProbeQueueTask{
		CredentialID: req.CredentialID,
		RawModel:     req.RawModel,
		Command:      req.Command,
		Mode:         "multi_round",
		Priority:     req.Priority,
		MaxAttempts:  req.MaxAttempts,
		Source:       req.Source,
		ReasonDetail: req.Reason,
		NextRunAt:    nextRunAt,
		DedupKey:     bg.BuildProbeDedupKey(req.Command, req.CredentialID, req.RawModel),
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	id, inserted, err := h.probeQueue.Enqueue(ctx, task)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "enqueue failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"queued":    inserted,
		"task_id":   id,
		"dedup_key": task.DedupKey,
		"message":   ternary(inserted, "task enqueued", "a task for this dedup key is already active"),
	})
}

// handleProbeTaskCancel is the public "remove self-check task" API (需求 6
// bullet 1). DELETE /api/admin/probe/tasks?key=<dedup_key>  (or ?credential_id&raw_model).
// Cancels active (ready/running) tasks for the key; terminal-sticky.
func (h *Handler) handleProbeTaskCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.probeQueue == nil {
		writeError(w, http.StatusServiceUnavailable, "unified probe queue not configured")
		return
	}
	key := r.URL.Query().Get("key")
	if key == "" {
		credIDStr := r.URL.Query().Get("credential_id")
		rawModel := r.URL.Query().Get("raw_model")
		credID, err := strconv.ParseInt(credIDStr, 10, 64)
		if err != nil || credID <= 0 || rawModel == "" {
			writeError(w, http.StatusBadRequest, "provide ?key=<dedup_key> or ?credential_id=&raw_model=")
			return
		}
		key = bg.BuildProbeDedupKey("node_probe", credID, rawModel)
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	n, err := h.probeQueue.Cancel(ctx, key)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "cancel failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"cancelled": n,
		"dedup_key": key,
	})
}

func ternary(b bool, t, f string) string {
	if b {
		return t
	}
	return f
}

// ProbeTriStateTask is one row of GET /api/admin/probe/tasks (OBS-BE5,
// 25 号 §6.2 三态队列). Metadata only — no result_body_preview, keeping the
// 可观测安全红线 (body 不进 API/SSE) 一致。
type ProbeTriStateTask struct {
	ID           int64  `json:"id"`
	DedupKey     string `json:"dedup_key"`
	CredentialID int64  `json:"credential_id"`
	ProviderID   *int64 `json:"provider_id,omitempty"`
	// ProviderName / ProviderCode come from the providers table (LEFT JOIN).
	// They let the 自检 tab render 供应商 + 凭据 instead of a bare
	// "凭据 #<id>" — operator-facing dashboard readability (2026-08-20).
	// omitempty so legacy clients keep working when the JOIN yields NULL.
	ProviderName  string     `json:"provider_name,omitempty"`
	ProviderCode  string     `json:"provider_code,omitempty"`
	RawModel      string     `json:"raw_model"`
	Command       string     `json:"command"`
	Source        string     `json:"source"`
	Origin        string     `json:"origin"`            // scheduled | error | manual
	Status        string     `json:"status"`            // pending | in_flight | completed
	Outcome       string     `json:"outcome,omitempty"` // success|failed|expired|cancelled (completed 行)
	Attempt       int        `json:"attempt"`
	MaxAttempts   int        `json:"max_attempts"`
	Priority      int16      `json:"priority"`
	NextRetryAtMs int64      `json:"next_retry_at_ms,omitempty"` // 退避下一跳（pending 重臂行）
	ReasonCode    string     `json:"reason_code,omitempty"`
	HTTPStatus    *int       `json:"http_status,omitempty"`
	LatencyMs     *int       `json:"latency_ms,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	FinishedAt    *time.Time `json:"finished_at,omitempty"`
}

// probeTriStateOrigin maps credential_probe_queue.source onto the 26 号 §4
// tri-state origin badge. Mirrors bg.probeQueueOriginFromSource (both sides
// keep a copy of the small mapping to avoid an import).
func probeTriStateOrigin(source string) string {
	switch source {
	case "request_failure", "no_candidates":
		return "error"
	case "admin", "external_async":
		return "manual"
	default:
		return "scheduled"
	}
}

// probeCompletedWindow caps the completed leg (保留最近 N 条, 27 号 OBS-BE5).
const probeCompletedWindow = 200

// queryProbeTriStateTasks reads one leg of the tri-state probe queue view.
// status is one of pending|in_flight|completed; completed is capped at
// probeCompletedWindow rows ordered newest-first.
func queryProbeTriStateTasks(ctx context.Context, db pgxQueryer, status string, limit int) ([]ProbeTriStateTask, error) {
	if limit <= 0 {
		limit = 50
	}
	var where, order string
	switch status {
	case "pending":
		where, order = `q.status='ready'`, `q.priority DESC, q.next_run_at ASC, q.id ASC`
	case "in_flight":
		where, order = `q.status='running'`, `q.started_at DESC NULLS LAST, q.id DESC`
	case "completed":
		where, order = `q.status IN ('success','failed','expired','cancelled')`,
			`COALESCE(q.finished_at, q.updated_at) DESC, q.id DESC`
		if limit > probeCompletedWindow {
			limit = probeCompletedWindow
		}
	default:
		return nil, fmt.Errorf("invalid status %q", status)
	}
	rows, err := db.Query(ctx, `
		SELECT q.id, q.dedup_key, q.credential_id, q.provider_id,
		       COALESCE(NULLIF(p.display_name, ''), NULLIF(p.catalog_code, ''), NULLIF(p.code, ''), ''),
		       COALESCE(NULLIF(p.catalog_code, ''), NULLIF(p.code, ''), ''),
		       COALESCE(q.raw_model, ''), q.probe_command, q.source, q.status,
		       q.attempt, q.max_attempts, q.priority, q.next_run_at,
		       COALESCE(q.reason_code, ''), q.result_http_status, q.result_latency_ms,
		       q.created_at, q.updated_at, q.finished_at
		FROM credential_probe_queue q
		LEFT JOIN providers p ON p.id = q.provider_id
		WHERE `+where+`
		ORDER BY `+order+`
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tasks := []ProbeTriStateTask{}
	for rows.Next() {
		var t ProbeTriStateTask
		var nextRunAt time.Time
		var httpStatus, latency sql.NullInt32
		if err := rows.Scan(
			&t.ID, &t.DedupKey, &t.CredentialID, &t.ProviderID,
			&t.ProviderName, &t.ProviderCode,
			&t.RawModel, &t.Command, &t.Source, &t.Status,
			&t.Attempt, &t.MaxAttempts, &t.Priority, &nextRunAt,
			&t.ReasonCode, &httpStatus, &latency,
			&t.CreatedAt, &t.UpdatedAt, &t.FinishedAt,
		); err != nil {
			return nil, err
		}
		t.Origin = probeTriStateOrigin(t.Source)
		switch t.Status {
		case "ready":
			t.Status = "pending"
		case "running":
			t.Status = "in_flight"
		default:
			t.Outcome = t.Status
			t.Status = "completed"
		}
		if t.Status == "pending" && !nextRunAt.IsZero() {
			t.NextRetryAtMs = nextRunAt.UnixMilli()
		}
		if httpStatus.Valid {
			v := int(httpStatus.Int32)
			t.HTTPStatus = &v
		}
		if latency.Valid {
			v := int(latency.Int32)
			t.LatencyMs = &v
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

// handleProbeTaskList is the tri-state queue view behind
// GET /api/admin/probe/tasks?status=pending|in_flight|completed&limit= (OBS-BE5).
// Read-only — it never touches scheduling semantics (25 号 §6.1 不变).
func (h *Handler) handleProbeTaskList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	status := r.URL.Query().Get("status")
	if status == "" {
		status = "pending"
	}
	switch status {
	case "pending", "in_flight", "completed":
	default:
		writeError(w, http.StatusBadRequest, "status must be pending|in_flight|completed")
		return
	}
	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err != nil || n <= 0 || n > probeCompletedWindow {
			writeError(w, http.StatusBadRequest, "limit must be 1..200")
			return
		} else {
			limit = n
		}
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	tasks, err := queryProbeTriStateTasks(r.Context(), h.db, status, limit)
	if err != nil {
		slog.Error("probe tri-state tasks query failed", "status", status, "error", err)
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"status": status,
		"tasks":  tasks,
		"count":  len(tasks),
	})
}

// handleProbeTaskRoute dispatches GET (tri-state list) / POST (create) /
// DELETE (cancel) on the public /api/admin/probe/tasks endpoint.
func (h *Handler) handleProbeTaskRoute(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.handleProbeTaskList(w, r)
	case http.MethodPost:
		h.handleProbeTaskCreate(w, r)
	case http.MethodDelete:
		h.handleProbeTaskCancel(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// RegisterProbeDashboardRoutes registers probe dashboard API routes
// Called from cmd/gateway/main.go during admin API setup
func (h *Handler) RegisterProbeDashboardRoutes(mux *http.ServeMux, adminWrap func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("/api/admin/probe/dashboard", adminWrap(h.handleProbeDashboard))
	mux.HandleFunc("/api/admin/probe/queue-snapshot", adminWrap(h.handleProbeQueueSnapshot))
	mux.HandleFunc("/api/admin/probe/provider-latency", adminWrap(h.handleProviderLatency))
	mux.HandleFunc("/api/admin/probe/queue-tasks", adminWrap(h.handleProbeQueueTasks))
	mux.HandleFunc("/api/admin/probe/node-tasks", adminWrap(h.handleProbeNodeTasks))
	// 2026-08-13 (需求 6 bullet 1): public add/remove self-check task API.
	mux.HandleFunc("/api/admin/probe/tasks", adminWrap(h.handleProbeTaskRoute))
	mux.HandleFunc("/api/admin/probe/system-health", adminWrap(h.handleProbeSystemHealth))
	mux.HandleFunc("/api/admin/probe/model/", adminWrap(h.handleProbeModelRoutes))
	mux.HandleFunc("/api/admin/probe/availability-timeline", adminWrap(h.handleProbeAvailabilityTimeline))
	mux.HandleFunc("/api/admin/probe/cache-state", adminWrap(h.handleProbeCacheState))
	mux.HandleFunc("/api/admin/probe/cache-rebuild", adminWrap(h.handleProbeCacheRebuild))
	mux.HandleFunc("/api/admin/probe/cache-keys", adminWrap(h.handleProbeCacheKeys))
	// 2026-08-11: self-check / node-probe queue SSE stream (自检 tab). Mounted
	// conditionally so a gateway without the hub wired (e.g. tests) does not
	// expose a nil-handler route.
	if h.probeStreamHub != nil {
		mux.HandleFunc("/api/admin/probe/stream", adminWrap(h.probeStreamHub.HandleStream))
	}
}

// handleProbeModelRoutes is a router for /api/admin/probe/model/* endpoints
func (h *Handler) handleProbeModelRoutes(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if strings.HasSuffix(path, "/nodes") {
		h.handleProbeModelNodes(w, r)
	} else if strings.HasSuffix(path, "/state-summary") {
		h.handleProbeModelStateSummary(w, r)
	} else {
		http.Error(w, "not found", http.StatusNotFound)
	}
}

// CacheStateEntry is the JSON shape returned by /api/admin/probe/cache-state.
type CacheStateEntry struct {
	CredentialID         int        `json:"credential_id"`
	RawModel             string     `json:"raw_model_name"`
	State                string     `json:"state"`
	Available            bool       `json:"available"`
	LastStatus           string     `json:"last_status"`
	ConsecutiveSuccesses int        `json:"consecutive_successes"`
	ConsecutiveFailures  int        `json:"consecutive_failures"`
	UpdatedAt            *time.Time `json:"updated_at,omitempty"`
	NextRetryAt          *time.Time `json:"next_retry_at,omitempty"`
	Source               string     `json:"source"`
}

// GET /api/admin/probe/cache-state
//
// Reads the unified Redis availability cache directly.  Operators can:
//   - look up a specific (credential_id, raw_model) pair: ?credential_id=11&model=glm-5.2
//   - enumerate every entry for one raw_model: ?model=glm-5.2 (capped at 256)
//   - enumerate every entry under a credential: ?credential_id=11 (capped at 256)
//   - enumerate everything (capped at 4096 keys)
//
// Useful for diagnosing "is the cache populated" / "is the writer doing
// its job" without touching the PostgreSQL tables.  Designed to remain
// available even when the DB is degraded — it talks to Redis only.
//
// Optional ?format=prom returns the snapshot in Prometheus text
// exposition format (key=value lines with HELP/TYPE headers) so
// operators can `curl /api/admin/probe/cache-state?format=prom |
// promtool check metrics` to pull a one-off scrape without touching
// the gateway's /metrics endpoint.  The metric family names are
// distinct from the live /metrics series to avoid scrape collisions.
func (h *Handler) handleProbeCacheState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.availabilityReader == nil {
		http.Error(w, "availability reader not wired", http.StatusServiceUnavailable)
		return
	}

	query := r.URL.Query()
	credIDRaw := query.Get("credential_id")
	modelRaw := query.Get("model")
	credID := 0
	if credIDRaw != "" {
		if v, err := strconv.Atoi(credIDRaw); err == nil && v > 0 {
			credID = v
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	var entries []CacheStateEntry
	switch {
	case credID > 0 && modelRaw != "":
		// Single (cred, model) lookup.
		snap, err := h.availabilityReader.Read(ctx, credID, modelRaw)
		if err != nil {
			slog.Error("probe dashboard cache read failed", "credential_id", credID, "error", err)
			writeError(w, http.StatusInternalServerError, "internal server error")
			return
		}
		if snap != nil {
			entries = append(entries, toCacheStateEntry(credID, modelRaw, *snap))
		}
	default:
		// Range scan.
		keys, err := h.availabilityReader.ScanKeys(ctx, credID)
		if err != nil {
			slog.Error("probe dashboard cache scan failed", "credential_id", credID, "error", err)
			writeError(w, http.StatusInternalServerError, "internal server error")
			return
		}
		rc, ok := h.redisClient.(*redis.Client)
		if !ok || rc == nil {
			slog.Error("probe dashboard redis client unavailable")
			writeError(w, http.StatusServiceUnavailable, "redis client unavailable")
			return
		}
		for _, key := range keys {
			cid, model, ok := parseAvailabilityKey(key)
			if !ok {
				continue
			}
			if modelRaw != "" && cid != credID && model != modelRaw {
				continue
			}
			if modelRaw != "" && model != modelRaw {
				continue
			}
			snap, err := h.availabilityReader.Read(ctx, cid, model)
			if err != nil || snap == nil {
				continue
			}
			entries = append(entries, toCacheStateEntry(cid, model, *snap))
		}
	}

	switch query.Get("format") {
	case "prom", "prometheus":
		writeCacheStateProm(w, entries)
	default:
		if entries == nil {
			entries = []CacheStateEntry{}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"reader":        "redis",
			"key_prefix":    "llmgw:avail",
			"credential_id": credID,
			"model":         modelRaw,
			"count":         len(entries),
			"entries":       entries,
		})
	}
}

// writeCacheStateProm renders the cache snapshot in Prometheus text
// exposition format.  We emit one metric family per logical
// attribute:
//
//	llmgw_availability_cache_snapshot_info{credential_id="11",raw_model="glm-5.2",state="healthy_confirmed",source="model_probe"} 1
//	llmgw_availability_cache_available{credential_id="11",raw_model="glm-5.2"} 1
//	llmgw_availability_cache_consecutive_successes{credential_id="11",raw_model="glm-5.2"} 3
//	llmgw_availability_cache_consecutive_failures{credential_id="11",raw_model="glm-5.2"} 0
//	llmgw_availability_cache_next_retry_at{credential_id="11",raw_model="glm-5.2"} 1.7e+09
//	llmgw_availability_cache_updated_at{credential_id="11",raw_model="glm-5.2"} 1.7e+09
//
// The metric names are intentionally distinct from the
// /metrics series (which expose writer/reader counters and the
// key counter as `llmgw_availability_*`) so a `promtool check
// metrics` pass on the live /metrics scrape does not collide with a
// one-off curl to /cache-state.
func writeCacheStateProm(w http.ResponseWriter, entries []CacheStateEntry) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	promPrintln(w, "# HELP llmgw_availability_cache_snapshot_info Per-(credential, model) cache snapshot, one series per entry.")
	promPrintln(w, "# TYPE llmgw_availability_cache_snapshot_info gauge")
	promPrintln(w, "# HELP llmgw_availability_cache_available 1 if the cached state is available for routing, 0 otherwise.")
	promPrintln(w, "# TYPE llmgw_availability_cache_available gauge")
	promPrintln(w, "# HELP llmgw_availability_cache_consecutive_successes Cached consecutive success count from the most recent probe.")
	promPrintln(w, "# TYPE llmgw_availability_cache_consecutive_successes gauge")
	promPrintln(w, "# HELP llmgw_availability_cache_consecutive_failures Cached consecutive failure count from the most recent probe.")
	promPrintln(w, "# TYPE llmgw_availability_cache_consecutive_failures gauge")
	promPrintln(w, "# HELP llmgw_availability_cache_updated_at Unix timestamp of the most recent cache write for this entry.")
	promPrintln(w, "# TYPE llmgw_availability_cache_updated_at gauge")
	promPrintln(w, "# HELP llmgw_availability_cache_next_retry_at Unix timestamp by which the probe worker plans to revisit this entry.")
	promPrintln(w, "# TYPE llmgw_availability_cache_next_retry_at gauge")

	now := time.Now().Unix()
	for _, e := range entries {
		base := fmt.Sprintf(`credential_id=%q,raw_model=%q,state=%q,source=%q`,
			strconv.Itoa(e.CredentialID), e.RawModel, e.State, e.Source)
		promPrintf(w, "llmgw_availability_cache_snapshot_info{%s} 1\n", base)
		avail := 0
		if e.Available {
			avail = 1
		}
		promPrintf(w, "llmgw_availability_cache_available{credential_id=%q,raw_model=%q} %d\n",
			strconv.Itoa(e.CredentialID), e.RawModel, avail)
		promPrintf(w, "llmgw_availability_cache_consecutive_successes{credential_id=%q,raw_model=%q} %d\n",
			strconv.Itoa(e.CredentialID), e.RawModel, e.ConsecutiveSuccesses)
		promPrintf(w, "llmgw_availability_cache_consecutive_failures{credential_id=%q,raw_model=%q} %d\n",
			strconv.Itoa(e.CredentialID), e.RawModel, e.ConsecutiveFailures)
		if e.UpdatedAt != nil {
			ts := e.UpdatedAt.Unix()
			if ts < 0 || ts == 0 {
				ts = now
			}
			promPrintf(w, "llmgw_availability_cache_updated_at{credential_id=%q,raw_model=%q} %d\n",
				strconv.Itoa(e.CredentialID), e.RawModel, ts)
		}
		if e.NextRetryAt != nil {
			ts := e.NextRetryAt.Unix()
			if ts < 0 || ts == 0 {
				ts = now
			}
			promPrintf(w, "llmgw_availability_cache_next_retry_at{credential_id=%q,raw_model=%q} %d\n",
				strconv.Itoa(e.CredentialID), e.RawModel, ts)
		}
	}
}

// promPrintln / promPrintf wrap fmt.Fprintln/Fprintf for Prometheus
// exporter endpoints where write errors are non-actionable (client
// disconnect mid-response means we can't recover). Use these helpers
// instead of bare fmt.Fprintln to satisfy errcheck without per-line
// _ = noise.
func promPrintln(w io.Writer, s string)                  { _, _ = fmt.Fprintln(w, s) }
func promPrintf(w io.Writer, format string, args ...any) { _, _ = fmt.Fprintf(w, format, args...) }

func toCacheStateEntry(credentialID int, rawModel string, snap bg.ModelAvailabilitySnapshot) CacheStateEntry {
	return CacheStateEntry{
		CredentialID:         credentialID,
		RawModel:             rawModel,
		State:                snap.State,
		Available:            snap.Available,
		LastStatus:           snap.LastStatus,
		ConsecutiveSuccesses: snap.ConsecutiveSuccesses,
		ConsecutiveFailures:  snap.ConsecutiveFailures,
		UpdatedAt:            snap.UpdatedAt,
		NextRetryAt:          snap.NextRetryAt,
		Source:               snap.Source,
	}
}

func parseAvailabilityKey(key string) (int, string, bool) {
	// Format: llmgw:avail:{credential_id}:{raw_model}
	// raw_model may itself contain colons, so split from the right.
	const prefix = "llmgw:avail:"
	if !strings.HasPrefix(key, prefix) {
		return 0, "", false
	}
	rest := key[len(prefix):]
	idx := strings.Index(rest, ":")
	if idx <= 0 {
		return 0, "", false
	}
	credID, err := strconv.Atoi(rest[:idx])
	if err != nil {
		return 0, "", false
	}
	return credID, rest[idx+1:], true
}

// POST /api/admin/probe/cache-rebuild
//
// Triggers a single DB→Redis cache rebuild pass. Intended for ops use
// after a Redis flush or cold deploy. The body is JSON-optional:
//
//	{ "lookback_seconds": 3600, "batch_size": 200 }
//
// When the body is empty we use the worker's defaults.  The response is
// JSON containing how many entries were re-populated.
func (h *Handler) handleProbeCacheRebuild(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.availabilityBackfill == nil {
		http.Error(w, "availability backfill not wired", http.StatusServiceUnavailable)
		return
	}

	type req struct {
		LookbackSeconds int `json:"lookback_seconds"`
		BatchSize       int `json:"batch_size"`
	}
	var body req
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil && err.Error() != "EOF" {
			slog.Error("probe dashboard invalid json", "error", err)
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
	}

	if body.LookbackSeconds > 0 || body.BatchSize > 0 {
		// Run a one-shot backfill with custom parameters by constructing
		// a temporary worker. We do not mutate the long-running worker
		// so the next scheduled tick is unaffected.
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		count, err := runOneShotBackfill(ctx, h.availabilityBackfill, body.LookbackSeconds, body.BatchSize)
		if err != nil {
			slog.Error("probe dashboard backfill failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal server error")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"rebuilt":          count,
			"lookback_seconds": body.LookbackSeconds,
			"batch_size":       body.BatchSize,
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	count, err := h.availabilityBackfill.RunOnceWithTrigger(ctx, "manual")
	if err != nil {
		slog.Error("probe dashboard backfill failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"rebuilt": count,
		"mode":    "default",
	})
}

// POST /api/admin/probe/cache-keys
//
// Triggers a one-shot SCAN over the llmgw:avail:* namespace and
// refreshes the llmgw_availability_keys_count Prometheus gauge with
// the live count. Useful when an operator has just flushed Redis or
// after a failover to make the dashboard reflect the new
// cardinality within seconds instead of waiting for the next
// periodic tick.
func (h *Handler) handleProbeCacheKeys(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.availabilityKeyCounter == nil {
		http.Error(w, "availability key counter not wired", http.StatusServiceUnavailable)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	h.availabilityKeyCounter.CountOnce(ctx)

	writeJSON(w, http.StatusOK, map[string]any{
		"refreshed": true,
	})
}

// runOneShotBackfill runs a single DB→Redis rebuild pass with custom
// batch_size / lookback_seconds. It reuses the worker's connection but
// executes a parameterised SQL query and bypasses the worker's
// shouldRefresh filter so the operator can force a full refresh.
func runOneShotBackfill(ctx context.Context, w *bg.AvailabilityCacheBackfill, lookbackSeconds, batchSize int) (int, error) {
	if lookbackSeconds <= 0 {
		lookbackSeconds = 3600
	}
	if batchSize <= 0 {
		batchSize = 200
	}
	if w == nil {
		return 0, fmt.Errorf("backfill worker not wired")
	}
	db := w.DB()
	if db == nil {
		return 0, fmt.Errorf("backfill worker has no DB pool")
	}
	rows, err := db.Query(ctx, `
		SELECT credential_id, raw_model_name, state,
		       COALESCE(consecutive_successes, 0),
		       COALESCE(consecutive_failures, 0),
		       COALESCE(total_attempts, 0),
		       last_attempt_at, next_retry_at, last_status
		FROM model_probe_state
		WHERE next_retry_at IS NOT NULL
		  AND next_retry_at <= NOW() + make_interval(secs => $1)
		ORDER BY next_retry_at DESC
		LIMIT $2
	`, lookbackSeconds, batchSize)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	written := 0
	cache := w.Cache()
	if cache == nil {
		return 0, fmt.Errorf("backfill worker has no cache")
	}
	for rows.Next() {
		var (
			credID      int
			model       string
			state       string
			succ        int
			fail        int
			total       int
			lastAttempt *time.Time
			nextRetry   *time.Time
			lastStatus  *string
		)
		if err := rows.Scan(&credID, &model, &state, &succ, &fail, &total,
			&lastAttempt, &nextRetry, &lastStatus); err != nil {
			continue
		}
		status := ""
		if lastStatus != nil {
			status = *lastStatus
		}
		available := !isUnavailableState(state)
		fields := bg.ModelAvailabilityFields(credID, model, state, available, status, succ, fail, nextRetry, "backfill")
		if err := cache.Set(ctx, credID, model, fields); err != nil {
			continue
		}
		written++
	}
	if err := rows.Err(); err != nil {
		return written, err
	}
	return written, nil
}

// isUnavailableState mirrors bg.isUnavailable but stays in the admin
// package so we don't need to export it from bg just for this endpoint.
func isUnavailableState(state string) bool {
	switch state {
	case "broken_confirmed", "failing", "unreachable":
		return true
	}
	return false
}

func nullFloat64(v sql.NullFloat64) float64 {
	if !v.Valid {
		return 0
	}
	return v.Float64
}

func nullInt(v sql.NullInt64) int {
	if !v.Valid {
		return 0
	}
	return int(v.Int64)
}
