// Package bg — metrics.go
//
// Prometheus counters and histograms for the synchronous no-candidate
// probe path added in 2026-07-17. The metrics are intentionally distinct
// from the background NodeProbeWorker counters so dashboards can
// separately attribute "probe runs triggered by an inbound request that
// hit no_candidates" vs "probe runs scheduled by the 30s tick /
// request_failure threshold".
//
// Counters
//
//	llmgw_node_probe_sync_total{outcome="recovered"|"exhausted"|"timeout"|"client_cancel"|"skipped"}
//
//	  recovered  — at least one (cred,model) pair passed both rounds,
//	               the executor retried Execute() and the upstream call
//	               returned 200. The user-facing request succeeded.
//	  exhausted  — every (cred,model) probe failed; the executor returned
//	               503 model_not_found.
//	  timeout    — ProbeSync ctx fired before any candidate recovered.
//	  client_cancel — the client's r.Context() was cancelled (client
//	               disconnected) while we were holding.
//	  skipped    — ProbeSync was a no-op (kill-switch off, candidates
//	               empty, etc.) — kept for trace completeness.
//
// Histogram
//
//	llmgw_node_probe_sync_duration_seconds
//
//	  Wall-clock time spent inside ProbeSync, including the parallel
//	  fan-out + gateway round. Observed at every outcome.
//
// Counter
//
//	llmgw_node_probe_sync_inflight_waiters
//
//	  Gauge of how many request goroutines are currently waiting for an
//	  in-flight background probe to finish (dedup reuse path).
package bg

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	nodeProbeSyncTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "llmgw_node_probe_sync_total",
			Help: "Outcomes of synchronous no-candidate probes triggered from the request hot path.",
		},
		[]string{"outcome"},
	)

	nodeProbeSyncDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "llmgw_node_probe_sync_duration_seconds",
		Help:    "Wall-clock duration of ProbeSync invocations from the request hot path.",
		Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1, 2, 3, 5, 8, 15},
	})

	nodeProbeSyncInflightWaiters = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "llmgw_node_probe_sync_inflight_waiters",
		Help: "Number of request goroutines currently blocked on an in-flight background probe (dedup reuse).",
	})

	// 2026-09-03 P1.3: ProbeQueue submission observability.
	//
	// The node_probe submission path (bg/node_probe.go::submitViaQueueSource)
	// now retries bounded (default 3 attempts) with backoff and persists
	// the final error into node_probe_state. These metrics let operators
	// tell apart:
	//   - outcome=success   — task inserted into credential_probe_queue
	//   - outcome=duplicate — peer or earlier attempt already enqueued
	//                          the same dedup_key (normal, not a failure)
	//   - outcome=skipped_ineligible / skipped_out_of_scope — deterministic
	//                          gate rejection (2026-09-05 noise-reduction):
	//                          logged once at Info, never retried,
	//                          node_probe_state left untouched (disabled
	//                          credential/provider is config state, not a
	//                          queue_submit_failed infrastructure failure)
	//   - outcome=failed    — all retries exhausted on a transient error;
	//                          row of node_probe_state now carries
	//                          last_err_code=queue_submit_failed
	//
	// Operators should alert on sustained rate(failed) > 0 because that
	// means the durable probe queue is not draining requests that came
	// from real failures (credential_recovery cannot recover what never
	// reached the queue).
	//
	// source values: request_failure (Submit path) | periodic (pump path).
	nodeProbeQueueSubmissionTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "llmgw_node_probe_queue_submission_total",
			Help: "Outcomes of ProbeQueue.Enqueue from the node_probe submission path, by source and outcome.",
		},
		[]string{"source", "outcome"},
	)

	nodeProbeQueueSubmissionRetriesTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "llmgw_node_probe_queue_submission_retries_total",
			Help: "Retry attempts (attempts > 1) issued by submitViaQueueSource.",
		},
		[]string{"source"},
	)

	nodeProbeQueueSubmissionDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "llmgw_node_probe_queue_submission_duration_seconds",
			Help:    "Wall-clock duration of submitViaQueueSource including all retry attempts.",
			Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1, 2, 5},
		},
		[]string{"source", "outcome"},
	)

	// 2026-08-29 P2: Hot table promote metrics for monitoring partition migration health.
	//
	// hotTablePromoteFailuresTotal counts promote failures by table label.
	// A rate() > 0 means hot table data is accumulating and not being
	// drained into monthly partitions, which will degrade query performance.
	// Operators should alert on sustained failures (e.g., rate(5m) > 0).
	//
	// Label "table" is the human-readable hot table name from promoteSpecs(),
	// e.g. "request_logs_hot", "session_bodies_hot", etc.
	hotTablePromoteFailuresTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "llm_gateway_hot_table_promote_failures_total",
			Help: "Hot table promote failures by table (label = hot table name from promoteSpecs)",
		},
		[]string{"table"},
	)

	// hotTablePromoteBatchesTotal counts successful promote batches by table.
	// Useful for monitoring promote throughput and comparing against failures.
	hotTablePromoteBatchesTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "llm_gateway_hot_table_promote_batches_total",
			Help: "Successful hot table promote batches by table",
		},
		[]string{"table"},
	)

	// hotTablePromoteRowsTotal counts total rows promoted by table.
	// Tracks the volume of data being moved from hot to partition tables.
	hotTablePromoteRowsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "llm_gateway_hot_table_promote_rows_total",
			Help: "Total rows promoted from hot to partition tables",
		},
		[]string{"table"},
	)

	// hotTablePromoteDurationSeconds observes promote batch duration by table.
	// High durations may indicate database load or large backlog.
	hotTablePromoteDurationSeconds = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "llm_gateway_hot_table_promote_duration_seconds",
			Help:    "Hot table promote batch duration by table",
			Buckets: []float64{0.1, 0.5, 1, 2, 5, 10, 30, 60},
		},
		[]string{"table"},
	)

	// hotTablePromoteSkippedTotal counts promote attempts skipped due to
	// advisory lock contention (peer gateway holding the lock).
	hotTablePromoteSkippedTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "llm_gateway_hot_table_promote_skipped_total",
			Help: "Hot table promote attempts skipped due to advisory lock contention",
		},
		[]string{"table"},
	)

	// 2026-08-31 (P2-8 audit-data-closure): per-table gauge tracking how
	// many consecutive promote cycles have been skipped because the
	// advisory lock is held. A persistently positive value indicates a
	// zombie lock — the peer gateway that held the lock has crashed
	// without releasing it. Operators should alert when this gauge stays
	// > 0 for longer than the promoteInterval itself.
	hotTablePromoteZombieLockStreak = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "llm_gateway_hot_table_promote_zombie_lock_streak",
			Help: "Consecutive promote cycles skipped because the advisory lock was held. Resets to 0 when a cycle successfully acquires the lock.",
		},
		[]string{"table"},
	)
	// R47（存储演进批，R46 §五#8）：per-table hot 剩余行数 gauge——promote
	// 排水结束后的水位。持续高于「批量×周期」即排水速度跟不上写入，
	// 是 hot 表 8h 不变式承压的前瞻信号。
	hotTableBacklogRows = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "llm_gateway_hot_table_backlog_rows",
			Help: "Rows remaining in the hot table after each promote drain cycle. Persistently high values mean promote throughput lags write throughput.",
		},
		[]string{"table"},
	)
	// R48（§五#3 存储演进二批）：oldest-row-age gauge——每张 hot 表内最旧一行
	// 距今的秒数（EXTRACT(EPOCH FROM (now() - MIN(<ts_col>)))）。0 = 表为空。
	// 与 backlog_rows 互补：backlog 看"剩多少"，oldest 看"最旧多久"——
	// 两者一起定位 promote 是否滞后（高 backlog + 大 age = 写入速度跑赢排水）。
	// 观测窗口足够长（sessions 族 TTL 一周）即可裁决 sessions/session_turns/
	// session_bodies 是否纳入 stateTableTTLSpecs 的 enabled 开关。
	hotTableOldestRowAgeSeconds = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "llm_gateway_hot_table_oldest_row_age_seconds",
			Help: "Seconds since the oldest row currently in the hot table. 0 when the table is empty. Persistently large values mean write throughput outpaces promote drain — pair with backlog_rows to confirm.",
		},
		[]string{"table"},
	)

	// metricWorkerRestarts (R53, R51-F13) — self-healing restarts of bg
	// workers after a runFn panic (BaseWorker supervise loop). Label
	// "worker" is the compile-time literal worker name (closed set, ~25
	// values). A sustained rate > 0 on one worker means that worker hits a
	// recurring panic and needs a real fix, not just the backoff.
	metricWorkerRestarts = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "llm_gateway_bg_worker_restarts_total",
			Help: "Self-healing restarts of a bg worker after a runFn panic (BaseWorker supervise loop).",
		},
		[]string{"worker"},
	)
)

// recordPromoteFailure increments the failure counter for a table.
func recordPromoteFailure(table string) {
	hotTablePromoteFailuresTotal.WithLabelValues(table).Inc()
}

// recordPromoteBatch increments the batch counter and records row count.
func recordPromoteBatch(table string, rows int64) {
	hotTablePromoteBatchesTotal.WithLabelValues(table).Inc()
	hotTablePromoteRowsTotal.WithLabelValues(table).Add(float64(rows))
}

// recordPromoteDuration observes the duration of a promote batch.
func recordPromoteDuration(table string, seconds float64) {
	hotTablePromoteDurationSeconds.WithLabelValues(table).Observe(seconds)
}

// recordPromoteSkipped increments the skipped counter.
func recordPromoteSkipped(table string) {
	hotTablePromoteSkippedTotal.WithLabelValues(table).Inc()
}

// recordHotTableBacklog sets the post-drain hot table row count (R47).
func recordHotTableBacklog(table string, rows int64) {
	hotTableBacklogRows.WithLabelValues(table).Set(float64(rows))
}

// recordHotTableOldestRowAge sets the oldest-row age in seconds (R48 §五#3).
// ageSeconds < 0 表示表为空或查询失败（保持 last value + Warn）。
func recordHotTableOldestRowAge(table string, ageSeconds float64) {
	hotTableOldestRowAgeSeconds.WithLabelValues(table).Set(ageSeconds)
}

// hotTableTSColumn returns the timestamp column name for a hot-table label.
// Defaults to "ts" (canonical for most hot tables in this repo); overrides
// only for tables whose schema is known to use a different column.
//
// R48 §五#3：观测 sessions 族 TTL 裁决前置——需要知道每张表的最旧
// 行落在哪一列。本映射必须与 sql/migrations 中对应表的 CREATE 保持一致，
// 添加新 hot 表时必须同步更新此处。
// R51 (2026-09-21)：移除 harness_hot / session_audit_logs_hot /
// probe_feedback_hot 三条映射——仓库中无这三张表的建表/写入路径（幽灵表），
// 留在白名单里只会让本映射与实际 CREATE 的"必须一致"契约失真。
func hotTableTSColumn(label string) string {
	switch label {
	case "session_turns_hot", "session_memora_hot", "session_censors_hot",
		"session_tools_hot", "session_bodies_hot", "session_module_executions_hot",
		"auto_route_selections_hot", "candidate_failure_logs_hot",
		"dashboard_access_events_hot", "session_last_requests":
		return "created_at"
	default:
		return "ts"
	}
}

// incPromoteZombieLockStreak (2026-08-31, P2-8) bumps the per-table
// zombie-lock streak counter. Called from partition_manager whenever a
// promote cycle failed to acquire the advisory lock.
func incPromoteZombieLockStreak(table string) {
	hotTablePromoteZombieLockStreak.WithLabelValues(table).Inc()
}

// resetPromoteZombieLockStreak (2026-08-31, P2-8) zeroes the per-table
// streak counter. Called from partition_manager whenever a promote cycle
// successfully acquires the advisory lock, so the gauge reflects
// consecutive-skipped cycles (not lifetime skipped).
func resetPromoteZombieLockStreak(table string) {
	hotTablePromoteZombieLockStreak.WithLabelValues(table).Set(0)
}

// 2026-09-01 P1-B: Materialized view refresh metrics for observability
// of the routing analytics MV refresh cycle (bg/materialized_view_refresher.go).
//
// Label "view" is a closed enum: routing_analytics_7d | routing_audit_summary_7d
// (the two views created by migration 632). No high-cardinality dimensions.
var (
	// mvRefreshTotal counts refresh attempts by view and outcome.
	// outcome values: success | failed | skipped_no_lock | skipped_follower | skipped_missing_view
	mvRefreshTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "gateway_mv_refresh_total",
			Help: "Materialized view refresh attempts by view and outcome.",
		},
		[]string{"view", "outcome"},
	)

	// mvRefreshDurationSeconds observes the wall-clock time per refresh.
	// Only observed when outcome=success (failed/skipped refreshes either
	// error out early or skip the DB entirely, so their duration is not
	// meaningful for capacity planning).
	mvRefreshDurationSeconds = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "gateway_mv_refresh_duration_seconds",
			Help:    "Materialized view refresh duration (REFRESH CONCURRENTLY wall time).",
			Buckets: []float64{0.5, 1, 2, 5, 10, 20, 30, 60, 120, 300},
		},
		[]string{"view"},
	)

	// mvRefreshLastSuccessUnix is the unix timestamp of the last successful
	// refresh per view. Paired with time() in Prometheus rules to alert on
	// staleness (e.g., no refresh in >20 minutes = 2 missed cycles).
	mvRefreshLastSuccessUnix = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gateway_mv_refresh_last_success_unix",
			Help: "Unix timestamp of the last successful materialized view refresh.",
		},
		[]string{"view"},
	)

	// mvRefreshCoordinationResult counts how the cross-instance coordination
	// resolved for each refresh cycle: redis_leader (won Redis token),
	// redis_follower (lost Redis race), redis_fallback (Redis unavailable,
	// used Postgres advisory lock), postgres_lock_held (Postgres advisory
	// lock was already taken by peer), postgres_lock_acquired (Postgres
	// advisory lock successfully acquired).
	mvRefreshCoordinationResult = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "gateway_mv_refresh_coordination_result_total",
			Help: "Cross-instance coordination result for MV refresh cycles.",
		},
		[]string{"result"},
	)
)

// recordMVRefreshSuccess records a successful MV refresh with its duration.
func recordMVRefreshSuccess(view string, durationSeconds float64) {
	mvRefreshTotal.WithLabelValues(view, "success").Inc()
	mvRefreshDurationSeconds.WithLabelValues(view).Observe(durationSeconds)
	mvRefreshLastSuccessUnix.WithLabelValues(view).Set(float64(time.Now().Unix()))
}

// recordMVRefreshFailure records a failed MV refresh attempt.
func recordMVRefreshFailure(view string) {
	mvRefreshTotal.WithLabelValues(view, "failed").Inc()
}

// recordMVRefreshSkipped records a skipped refresh with a reason.
// reason should be one of: skipped_no_lock | skipped_follower | skipped_missing_view
func recordMVRefreshSkipped(view, reason string) {
	mvRefreshTotal.WithLabelValues(view, reason).Inc()
}

// recordMVCoordination records the coordination outcome for a refresh cycle.
func recordMVCoordination(result string) {
	mvRefreshCoordinationResult.WithLabelValues(result).Inc()
}
