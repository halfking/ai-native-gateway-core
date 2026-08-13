package bg

import (
	"context"
	"hash/fnv"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/settings"
)

// settingsGetPlatformInt is a thin wrapper around settings.GetPlatformInt
// so call sites stay short and consistent with the rest of this file.
func settingsGetPlatformInt(key string, fallback int) int {
	return settings.GetPlatformInt(key, fallback)
}

// DefaultPromoteInterval is how often the migrate-default-to-partition
// scheduler runs. 7-day *_default retention requires relatively
// frequent polling so writes don't accumulate beyond the configured
// window. 24h is too coarse — promote_xxx_default_batch must sweep at
// least every few hours.
const DefaultPromoteInterval = 1 * time.Hour

// DefaultRetentionWindow is the *_default "hot data" keep-window. Rows
// in *_default whose ts_col is older than (now - DefaultRetentionWindow)
// are eligible to be migrated to the matching monthly partition by the
// promote_*_default_batch functions installed in migration 336.
//
// 2026-07 hot-table architecture:
//   - most *_hot tables keep a short hot window, then promote into monthly
//     partitions on the promote scheduler;
//   - model_probe_runs_hot is an exception as of 2026-07-14: it no longer
//     promotes and is cleaned by direct TTL DELETE.
const DefaultRetentionWindow = 24 * time.Hour

// promoteBatchSize is the per-call LIMIT inside each promote_xxx_batch
// CTE. Keeps per-tx memory bounded so a backlog cannot OOM the gateway.
//
// Per-table promote batch sizes can be overridden via settings_kv:
//   - lifecycle.promote_batch_size (default 5000)
const promoteBatchSize = 5000

// requestLogsBodiesPromoteBatchSize is the default per-call LIMIT for the
// request_logs_bodies promote. Bodies rows are TOAST-heavy (~350 KB avg; the
// hot table was 13 GB / 37k rows), so the generic 5000-row batch moves
// ~1.7 GB per call and consistently exceeds PG statement_timeout=30s on the
// shared DB — promote then never makes progress and every hourly tick adds
// lock/I/O contention. 500 rows (~175 MB/batch) stays well under the cap.
// Override via lifecycle.request_logs_bodies_promote_batch_size.
const requestLogsBodiesPromoteBatchSize = 500

// PartitionManager automatically creates next month's partition,
// archives old partitions to columnar storage, continuously migrates
// cold rows from most *_hot tables into matching monthly partitions,
// and applies direct TTL cleanup for model_probe_runs_hot.
//
// Runs `interval` for ensure+archive (typically 24h). Runs
// `promoteInterval` for the promote cycle (typically 1h — see
// DefaultPromoteInterval). Three schedules are independent so a
// long-running promote cycle never delays ensure_next_month.
//
// On day 1-3 of each month, archives the partition from 2 months ago.
//
// Usage:
//
//	pm := bg.NewPartitionManager(dbConn.Pool(), 24*time.Hour)
//	pm.Start(context.Background())
//	defer pm.Stop()
type PartitionManager struct {
	db              *pgxpool.Pool
	interval        time.Duration
	promoteInterval time.Duration
	cancel          context.CancelFunc
	done            chan struct{}
	mu              sync.Mutex // 2026-07-20: protect lastAnalyzeAt
	lastAnalyzeAt   time.Time  // 2026-07-20: analyze cooldown 5min
}

// archiveSpec describes one archive_xxx call: which SQL function to
// invoke and which day-of-month it should run on. day 0 means "run
// every day in the window" (kept for backwards compatibility if we
// ever go back to that style).
type archiveSpec struct {
	day     int    // 1..7 — only run archiveOldPartitions on this day
	fnName  string // SQL function name, e.g. "archive_request_logs"
	label   string // human label for logs
	enabled bool   //nolint:unused

	// argExpr is the placeholder expression passed to fnName in
	// ensureNextMonthPartitions. Empty means "$1" (timestamptz, the
	// original convention). Functions declared with a `date` parameter
	// need "$1::date" — pgx sends a Go time.Time as timestamptz, and PG
	// will not implicitly down-cast timestamptz → date when resolving
	// the function, so the call fails with "function does not exist".
	argExpr string
}

func NewPartitionManager(db *pgxpool.Pool, interval time.Duration) *PartitionManager {
	if interval == 0 {
		interval = 24 * time.Hour
	}
	return &PartitionManager{
		db:              db,
		interval:        interval,
		promoteInterval: DefaultPromoteInterval,
		done:            make(chan struct{}),
	}
}

// SetPromoteInterval overrides the default promote cycle (1h). Tests
// use a shorter interval to drain a backlog quickly. Setting to 0
// disables the promote scheduler entirely.
func (pm *PartitionManager) SetPromoteInterval(d time.Duration) {
	pm.promoteInterval = d
}

func (pm *PartitionManager) Start(ctx context.Context) {
	ctx, pm.cancel = context.WithCancel(ctx)
	go pm.run(ctx)
	slog.Info("partition_manager started", "interval", pm.interval)
}

func (pm *PartitionManager) Stop() {
	if pm.cancel != nil {
		pm.cancel()
	}
	<-pm.done
}

func (pm *PartitionManager) run(ctx context.Context) {
	defer close(pm.done)

	// Run once on startup — the gateway has no insight into how long
	// it was down so a single pass drains whatever _default rows have
	// accumulated since the last run.
	pm.ensureNextMonthPartitions(ctx)
	pm.archiveOldPartitionsIfNeeded(ctx)
	pm.promoteDefaultToPartitions(ctx)

	mainTicker := time.NewTicker(pm.interval)
	defer mainTicker.Stop()

	// promoteTicker fires more frequently than mainTicker so the
	// 7-day retention window stays sharp even during heavy write load.
	promoteTicker := time.NewTicker(pm.promoteInterval)
	defer promoteTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-mainTicker.C:
			pm.ensureNextMonthPartitions(ctx)
			pm.archiveOldPartitionsIfNeeded(ctx)
		case <-promoteTicker.C:
			pm.promoteDefaultToPartitions(ctx)
		}
	}
}

// ensureNextMonthPartitions creates partitions for current and next month
// for every table we manage. Idempotent: each underlying
// ensure_<table>_partition() function checks for existence first.
func (pm *PartitionManager) ensureNextMonthPartitions(ctx context.Context) {
	specs := ensureSpecs()
	for offset := 0; offset <= 1; offset++ {
		targetMonth := time.Now().AddDate(0, offset, 0)
		for _, s := range specs {
			argExpr := s.argExpr
			if argExpr == "" {
				argExpr = "$1"
			}
			timeoutCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			_, err := pm.db.Exec(timeoutCtx,
				"SELECT "+s.fnName+"("+argExpr+")", targetMonth)
			cancel()
			if err != nil {
				slog.Error("partition_manager: ensure partition failed",
					"fn", s.fnName, "label", s.label,
					"month", targetMonth.Format("2006-01"),
					"error", err)
				continue
			}
			slog.Info("partition_manager: ensured partition",
				"fn", s.fnName, "label", s.label,
				"month", targetMonth.Format("2006-01"))
		}
	}
}

// archiveOldPartitionsIfNeeded archives 2-months-old partition to columnar
// for the table scheduled for today (day-of-month in 1..3).
// Outside that window this is a no-op.
//
// Also runs model_probe_runs_hot 14-day TTL DELETE cleanup
// (controlled by lifecycle.model_probe_runs_ttl_days, hot reloadable).
// 2026-07-14: switched from columnar-partition strategy to pure-hot-table,
// so no more monthly partition drops for model_probe_runs.
func (pm *PartitionManager) archiveOldPartitionsIfNeeded(ctx context.Context) {
	now := time.Now()

	// 1. Monthly archive (day 1..3 only)
	if now.Day() <= 3 {
		twoMonthsAgo := now.AddDate(0, -2, 0)
		today := now.Day()

		for _, s := range archiveSpecs() {
			if s.day != today {
				continue
			}

			// 2026-07-13: state table archive is per-table retention,
			// not twoMonthsAgo. We pass a sentinel and the dedicated
			// drop function reads per-table settings.
			if s.fnName == "drop_old_state_partitions" {
				pm.dropOldStatePartitions(ctx, s)
				continue
			}

			pm.runArchive(ctx, s, twoMonthsAgo)
		}
	}

	// 2. model_probe_runs hot table cleanup (every tick, 14-day TTL).
	// 2026-07-14: switched from columnar-partition strategy to pure-hot-table.
	pm.cleanupOldModelProbeRuns(ctx)

	// 3. 2026-07-13: credential_model_index 7-day TTL cleanup.
	// The SQL function `cleanup_old_credential_model_index()` deletes
	// `credential_model_index` rows older than 7 days. The dedup change
	// in auto_index_refresher.go (only-when-changed writes) keeps the
	// table from growing while traffic is stable; this cleanup handles
	// the long-tail case where a model stops being routed entirely
	// (e.g. credential disabled) so no more rows are inserted and the
	// historical ones can be reaped. Hot-reloadable via
	// lifecycle.credential_model_index_ttl_days (default 7).
	pm.cleanupOldCredentialModelIndex(ctx)

	// 4. 2026-07-13: credential_probe_model_log TTL cleanup.
	// This is a heap (columnar) table without partitions, so we
	// directly DELETE old rows. Runs every tick (1h); default 90d
	// retention via lifecycle.credential_probe_model_log_ttl_days.
	pm.cleanupOldCredentialProbeModelLog(ctx)

	// 5. 2026-07-14: request_logs_bodies partition cleanup (every tick).
	// Request/response bodies are only needed for debugging; older than
	// lifecycle.request_logs_bodies_ttl_days (default 7d) get DROP'd.
	pm.dropOldRequestLogsBodiesPartitions(ctx)

	// 6. 2026-07-15: node_probe_runs audit-table TTL cleanup (every tick).
	// Append-only audit table with no partition strategy; without this it
	// grows unboundedly. Default 14d via lifecycle.node_probe_runs_ttl_days.
	// Does NOT touch node_probe_state (the upsert state machine).
	pm.cleanupOldNodeProbeRuns(ctx)

	// 7. 2026-07-15: request_context_attrs side-table TTL cleanup (every
	// tick). One row per business request, no partition strategy; grows at
	// the same rate as request_logs. Default 7d via
	// lifecycle.request_context_attrs_ttl_days.
	pm.cleanupOldRequestContextAttrs(ctx)

	// 8. 2026-07-15: request_stats_minute family TTL cleanup (every tick).
	// Minute-level rollup tables upserted every minute; without this they
	// grow linearly with (minutes × dimension cardinality). Default 14d via
	// lifecycle.request_stats_minute_ttl_days. Covers all three rollup
	// tables; the single-row rollup cursor is not touched.
	pm.cleanupOldRequestStatsMinute(ctx)

	// 9. 2026-07-15: runtime_metrics TTL cleanup (every tick). Instances
	// push metrics periodically, no partition strategy. Default 30d via
	// lifecycle.runtime_metrics_ttl_days.
	pm.cleanupOldRuntimeMetrics(ctx)

	// 10. 2026-07-15: runtime_alert_events TTL cleanup (every tick).
	// Append-only alert events, no partition strategy. Default 30d via
	// lifecycle.runtime_alert_events_ttl_days.
	pm.cleanupOldRuntimeAlertEvents(ctx)
}

// dropOldStatePartitions calls the SQL helper
// `drop_old_state_partitions(retention_days)` which drops monthly
// partitions older than the retention window for state tables
// (routing_decision_log, candidate_failure_logs, handoff_logs,
// credential_model_call_history, model_probe_runs, etc.).
//
// 2026-07-13 状态表精简：
//   - 状态/路由类表默认 30 天 DROP PARTITION
//   - 请求记录类表（usage_ledger、request_wal、credit_ledger、tool_usage_stats）
//     默认 1 天（hot 表，依赖月度分区长期保留但不 DROP）
//   - credential_probe_model_log 是列存储堆表，需通过单独清理任务
//
// Designed to run on day-2 of the month (matches archiveSpecs()).
// Per-table retention is read fresh from settings.Global on every call
// (lifecycle.*_ttl_days) so changes take effect on the next
// partition_manager tick (1h by default). Returns the number of
// partitions dropped; logs a single summary line.
func (pm *PartitionManager) dropOldStatePartitions(ctx context.Context, s archiveSpec) {
	timeoutCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	// Pass the smallest TTL as the global retention — individual
	// partitions older than ANY of the per-table TTLs are eligible to
	// drop. The SQL function does per-table policy enforcement.
	// We use the maximum across all state tables so we don't
	// accidentally drop partitions still needed.
	maxDays := settingsGetPlatformInt("lifecycle.routing_decision_log_ttl_days", 30)
	if d := settingsGetPlatformInt("lifecycle.candidate_failure_logs_ttl_days", 30); d > maxDays {
		maxDays = d
	}
	if d := settingsGetPlatformInt("lifecycle.handoff_logs_ttl_days", 30); d > maxDays {
		maxDays = d
	}
	if d := settingsGetPlatformInt("lifecycle.credential_model_call_history_ttl_days", 30); d > maxDays {
		maxDays = d
	}
	if d := settingsGetPlatformInt("lifecycle.model_probe_runs_ttl_days", 90); d > maxDays {
		maxDays = d
	}
	if maxDays < 30 {
		maxDays = 30 // safety floor — never set to less than 30d
	}

	var dropped int64
	err := pm.db.QueryRow(timeoutCtx,
		"SELECT drop_old_state_partitions($1)", maxDays,
	).Scan(&dropped)
	if err != nil {
		slog.Error("partition_manager: state table drop failed",
			"label", s.label, "ttl_days", maxDays, "error", err)
		return
	}
	if dropped > 0 {
		slog.Info("partition_manager: state table drop ran",
			"label", s.label, "ttl_days", maxDays, "partitions_dropped", dropped)
	}
}

// cleanupOldCredentialModelIndex calls the SQL helper
// `cleanup_old_credential_model_index()` which deletes rows older than
// the configured TTL. Designed to run every partition_manager tick
// (1h by default); the SQL DELETE is index-backed on `bucket` so the
// overhead is a few hundred milliseconds even on million-row tables.
func (pm *PartitionManager) cleanupOldCredentialModelIndex(ctx context.Context) {
	ttlDays := settings.GetPlatformInt("lifecycle.credential_model_index_ttl_days", 7)
	if ttlDays < 1 {
		ttlDays = 7 // safety floor — never set to 0 (would wipe the table)
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	var deleted int64
	err := pm.db.QueryRow(timeoutCtx,
		"SELECT cleanup_old_credential_model_index()",
	).Scan(&deleted)
	if err != nil {
		slog.Error("partition_manager: credential_model_index cleanup failed",
			"ttl_days", ttlDays, "error", err)
		return
	}

	if deleted > 0 {
		slog.Info("partition_manager: credential_model_index cleanup ran",
			"ttl_days", ttlDays, "rows_deleted", deleted)
	}
}

// cleanupOldModelProbeRuns deletes old rows from model_probe_runs_hot.
// 2026-07-14: pure-hot-table strategy — no more columnar partitions.
// Retention is controlled by lifecycle.model_probe_runs_ttl_days
// (default 14, hot-reloadable).
func (pm *PartitionManager) cleanupOldModelProbeRuns(ctx context.Context) {
	retentionDays := settings.GetPlatformInt("lifecycle.model_probe_runs_ttl_days", 14)
	if retentionDays < 1 {
		retentionDays = 14
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	tag, err := pm.db.Exec(timeoutCtx,
		"DELETE FROM model_probe_runs_hot WHERE created_at < now() - ($1 || ' days')::interval",
		retentionDays)
	if err != nil {
		slog.Error("partition_manager: model_probe_runs hot cleanup failed",
			"retention_days", retentionDays, "error", err)
		return
	}
	n := tag.RowsAffected()
	if n > 0 {
		slog.Info("partition_manager: cleaned model_probe_runs_hot",
			"deleted_rows", n, "retention_days", retentionDays)
	}
}

// cleanupOldNodeProbeRuns deletes audit rows from node_probe_runs older
// than the configured TTL. node_probe_runs is an append-only audit table
// (one row per probe attempt) and has no partition strategy, so without
// this cleanup it grows unboundedly. The sibling node_probe_state table
// is the upsert state machine and is intentionally NOT touched here.
// Retention: lifecycle.node_probe_runs_ttl_days (default 14, hot-reloadable).
// Uses the existing idx_node_probe_runs_started index on started_at.
func (pm *PartitionManager) cleanupOldNodeProbeRuns(ctx context.Context) {
	retentionDays := settings.GetPlatformInt("lifecycle.node_probe_runs_ttl_days", 14)
	if retentionDays < 1 {
		retentionDays = 14
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	tag, err := pm.db.Exec(timeoutCtx,
		"DELETE FROM node_probe_runs WHERE started_at < now() - ($1 || ' days')::interval",
		retentionDays)
	if err != nil {
		slog.Error("partition_manager: node_probe_runs cleanup failed",
			"retention_days", retentionDays, "error", err)
		return
	}
	n := tag.RowsAffected()
	if n > 0 {
		slog.Info("partition_manager: cleaned node_probe_runs",
			"deleted_rows", n, "retention_days", retentionDays)
	}
}

// cleanupOldRequestContextAttrs deletes rows from request_context_attrs
// older than the configured TTL. This side table stores per-request
// observability attributes (one row per business request) and has no
// partition strategy, so it grows at the same rate as request_logs.
// Retention: lifecycle.request_context_attrs_ttl_days (default 7,
// hot-reloadable). Backed by idx_rca_ts (added in migration 412).
func (pm *PartitionManager) cleanupOldRequestContextAttrs(ctx context.Context) {
	retentionDays := settings.GetPlatformInt("lifecycle.request_context_attrs_ttl_days", 7)
	if retentionDays < 1 {
		retentionDays = 7
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	tag, err := pm.db.Exec(timeoutCtx,
		"DELETE FROM request_context_attrs WHERE ts < now() - ($1 || ' days')::interval",
		retentionDays)
	if err != nil {
		slog.Error("partition_manager: request_context_attrs cleanup failed",
			"retention_days", retentionDays, "error", err)
		return
	}
	n := tag.RowsAffected()
	if n > 0 {
		slog.Info("partition_manager: cleaned request_context_attrs",
			"deleted_rows", n, "retention_days", retentionDays)
	}
}

// cleanupOldRequestStatsMinute deletes rows older than the configured TTL
// from the three minute-level rollup tables. They are upserted every minute
// (bg/stats_minute_rollup.go) with no partition strategy, so without this
// they grow linearly with (minutes × dimension cardinality). All three share
// the `bucket` timestamp column and the lifecycle.request_stats_minute_ttl_days
// setting (default 14). request_stats_rollup_cursor (single-row state table)
// is intentionally NOT touched. Each table has a bucket-leading index so the
// DELETE is an index descent.
func (pm *PartitionManager) cleanupOldRequestStatsMinute(ctx context.Context) {
	retentionDays := settings.GetPlatformInt("lifecycle.request_stats_minute_ttl_days", 14)
	if retentionDays < 1 {
		retentionDays = 14
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	for _, table := range []string{
		"request_stats_minute",
		"request_stats_dim_minute",
		"request_stats_error_drill_minute",
	} {
		tag, err := pm.db.Exec(timeoutCtx,
			"DELETE FROM "+table+" WHERE bucket < now() - ($1 || ' days')::interval",
			retentionDays)
		if err != nil {
			slog.Error("partition_manager: request_stats cleanup failed",
				"table", table, "retention_days", retentionDays, "error", err)
			continue
		}
		if n := tag.RowsAffected(); n > 0 {
			slog.Info("partition_manager: cleaned request_stats table",
				"table", table, "deleted_rows", n, "retention_days", retentionDays)
		}
	}
}

// cleanupOldRuntimeMetrics deletes rows from runtime_metrics older than the
// configured TTL. Instances push metrics periodically (center/runtime_metrics)
// and the table has no partition strategy. Retention:
// lifecycle.runtime_metrics_ttl_days (default 30, hot-reloadable). Backed by
// idx_rt_time (timestamp DESC).
func (pm *PartitionManager) cleanupOldRuntimeMetrics(ctx context.Context) {
	retentionDays := settings.GetPlatformInt("lifecycle.runtime_metrics_ttl_days", 30)
	if retentionDays < 1 {
		retentionDays = 30
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	tag, err := pm.db.Exec(timeoutCtx,
		"DELETE FROM runtime_metrics WHERE timestamp < now() - ($1 || ' days')::interval",
		retentionDays)
	if err != nil {
		slog.Error("partition_manager: runtime_metrics cleanup failed",
			"retention_days", retentionDays, "error", err)
		return
	}
	if n := tag.RowsAffected(); n > 0 {
		slog.Info("partition_manager: cleaned runtime_metrics",
			"deleted_rows", n, "retention_days", retentionDays)
	}
}

// cleanupOldRuntimeAlertEvents deletes rows from runtime_alert_events older
// than the configured TTL. Alert events are append-only (status can be
// triggered/resolved but rows are never deleted). Retention:
// lifecycle.runtime_alert_events_ttl_days (default 30, hot-reloadable).
// Backed by idx_rae_detected_at (added in migration 413).
func (pm *PartitionManager) cleanupOldRuntimeAlertEvents(ctx context.Context) {
	retentionDays := settings.GetPlatformInt("lifecycle.runtime_alert_events_ttl_days", 30)
	if retentionDays < 1 {
		retentionDays = 30
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	tag, err := pm.db.Exec(timeoutCtx,
		"DELETE FROM runtime_alert_events WHERE detected_at < now() - ($1 || ' days')::interval",
		retentionDays)
	if err != nil {
		slog.Error("partition_manager: runtime_alert_events cleanup failed",
			"retention_days", retentionDays, "error", err)
		return
	}
	if n := tag.RowsAffected(); n > 0 {
		slog.Info("partition_manager: cleaned runtime_alert_events",
			"deleted_rows", n, "retention_days", retentionDays)
	}
}

// dropOldRequestLogsBodiesPartitions drops monthly partitions of
// request_logs_bodies older than the configured TTL. Retention is
// read fresh from settings.Global on every call
// (lifecycle.request_logs_bodies_ttl_days, default 7), so changes
// take effect on the next partition_manager tick.
func (pm *PartitionManager) dropOldRequestLogsBodiesPartitions(ctx context.Context) {
	retentionDays := settings.GetPlatformInt("lifecycle.request_logs_bodies_ttl_days", 7)
	if retentionDays < 1 {
		retentionDays = 7
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	var dropped int64
	err := pm.db.QueryRow(timeoutCtx,
		"SELECT COUNT(*) FROM drop_old_request_logs_bodies_partitions($1)",
		retentionDays,
	).Scan(&dropped)
	if err != nil {
		slog.Error("partition_manager: request_logs_bodies cleanup failed",
			"ttl_days", retentionDays, "error", err)
		return
	}

	if dropped > 0 {
		slog.Info("partition_manager: request_logs_bodies cleanup dropped partitions",
			"ttl_days", retentionDays, "count", dropped)
	}
}

// cleanupOldCredentialProbeModelLog deletes rows from
// credential_probe_model_log older than the configured TTL.
//
// 2026-07-13: this is a heap (columnar) table without monthly
// partitions, so we use direct DELETE. Designed to run every tick
// (1h by default); the SQL DELETE is index-backed on created_at
// (we add an index if missing). Hot-reloadable via
// lifecycle.credential_probe_model_log_ttl_days (default 90).
func (pm *PartitionManager) cleanupOldCredentialProbeModelLog(ctx context.Context) {
	ttlDays := settingsGetPlatformInt("lifecycle.credential_probe_model_log_ttl_days", 90)
	if ttlDays < 7 {
		ttlDays = 7 // safety floor
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	var deleted int64
	err := pm.db.QueryRow(timeoutCtx,
		"SELECT cleanup_old_credential_probe_model_log($1)", ttlDays,
	).Scan(&deleted)
	if err != nil {
		slog.Error("partition_manager: credential_probe_model_log cleanup failed",
			"ttl_days", ttlDays, "error", err)
		return
	}

	if deleted > 0 {
		slog.Info("partition_manager: credential_probe_model_log cleanup ran",
			"ttl_days", ttlDays, "rows_deleted", deleted)
	}
}

func (pm *PartitionManager) runArchive(ctx context.Context, s archiveSpec, twoMonthsAgo time.Time) {
	timeoutCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	// We use the lowest common shape: every archive_<table>() returns
	// either a 3-tuple (status, rows_archived, rows_deleted) — CMI
	// style — or a 3-tuple (status, rows_migrated, partition_dropped)
	// — the other three. We scan into string + int64 + bool, which
	// accepts both shapes regardless of which column name is used.
	var status string
	var rowsMigrated int64
	var partitionDropped bool

	err := pm.db.QueryRow(timeoutCtx,
		"SELECT status, rows_migrated, partition_dropped FROM "+s.fnName+"($1)",
		twoMonthsAgo,
	).Scan(&status, &rowsMigrated, &partitionDropped)

	if err != nil {
		slog.Error("partition_manager: archive failed",
			"fn", s.fnName, "label", s.label,
			"month", twoMonthsAgo.Format("2006-01"),
			"error", err)
		return
	}

	switch status {
	case "success":
		slog.Info("partition_manager: archive succeeded",
			"fn", s.fnName, "label", s.label,
			"month", twoMonthsAgo.Format("2006-01"),
			"rows_migrated", rowsMigrated,
			"partition_dropped", partitionDropped)
	case "skipped":
		slog.Debug("partition_manager: archive skipped (partition not found)",
			"fn", s.fnName, "label", s.label,
			"month", twoMonthsAgo.Format("2006-01"))
	default:
		slog.Warn("partition_manager: archive returned unknown status",
			"fn", s.fnName, "label", s.label,
			"month", twoMonthsAgo.Format("2006-01"),
			"status", status)
	}
}

// ensureSpecs returns the ensure_<table>_partition() functions we
// invoke on every tick. Add a row here when a new table is onboarded.
//
// Migration 328a (2026-07-02) split the body columns out of
// request_logs into request_logs_bodies. The bodies partition is
// paired with the metadata partition by (target_ts → month), so we
// ensure both at the same time. Without this entry, request_logs
// would have a partition that request_logs_bodies lacks, and the
// admin/body JOIN would silently return no rows for that month.
func ensureSpecs() []archiveSpec {
	return []archiveSpec{
		{fnName: "ensure_request_logs_partition", label: "request_logs"},
		{fnName: "ensure_request_logs_bodies_partition", label: "request_logs_bodies"},
		{fnName: "ensure_request_wal_partition", label: "request_wal"},
		{fnName: "ensure_routing_decision_log_partition", label: "routing_decision_log"},
		{fnName: "ensure_credential_model_index_partition", label: "credential_model_index"},
		{fnName: "ensure_usage_ledger_partition", label: "usage_ledger"}, // Migration 330

		// ── 2026-08-07 审计补齐 ────────────────────────────────────────
		// 迁移 473 一次性补了 2026_09 + 2026_10 分区，但这些表从未接入
		// ensureSpecs()，到 2026-11-01 会再次因「no partition of relation
		// found for row」全面写入失败。函数本身早已随各自迁移安装，
		// 这里只是把它们接入 24h 定时轮转。
		//
		// timestamptz 签名（默认 argExpr="$1"）
		{fnName: "ensure_credit_ledger_partition", label: "credit_ledger"},      // Migration 334
		{fnName: "ensure_tool_usage_stats_partition", label: "tool_usage_stats"}, // Migration 335

		// date 签名 —— pgx 传 time.Time 为 timestamptz，需显式 ::date 转换，
		// 否则 PG 报 "function does not exist"。
		//
		// ensure_sessions_v2_partitions 一次调用同时覆盖 public.sessions /
		// session_turns / session_bodies 三张表（见 Migration 430）。这三张
		// 表是 V2 会话主链路的写入目标，缺分区等于聊天全挂。
		{fnName: "ensure_sessions_v2_partitions", label: "sessions_v2 (sessions/session_turns/session_bodies)", argExpr: "$1::date"}, // Migration 430
		{fnName: "ensure_session_module_executions_partition", label: "session_module_executions", argExpr: "$1::date"},              // Migration 382
		{fnName: "ensure_dashboard_events_partition", label: "dashboard_access_events", argExpr: "$1::date"},                         // Migration 383
		{fnName: "ensure_cache_metrics_partition", label: "cache_metrics", argExpr: "$1::date"},                                      // Migration 475

		// model_probe_runs 已切换为纯 hot 表策略（2026-07-14），
		// 不再 promote 到 columnar 分区，所以也不需要 ensure。
		// {fnName: "ensure_model_probe_runs_partition", label: "model_probe_runs"}, // Migration 385 (retired)
		//
		// 以下两张表有意不接入：
		//   routing_decision_log_archive —— 仅 archive job（每月 1-3 日）写入，
		//     archive_routing_decision_log() 自建目标分区。
		//   candidate_failure_logs —— ensure 函数是空 body（见 sql/objects/
		//     functions/），实际走 hot 表 + promote 架构。
	}
}

// archiveSpecs returns the archive_<table>() functions we invoke on
// day-1..3 of each month, paired with the day-of-month they run on.
//
// Migration 331 (2026-07-04): Removed archive_request_logs and archive_request_wal
// to simplify architecture. Archive tables had minimal storage footprint (~30 MB/partition)
// and no application code queried them. Old partitions are now dropped directly when
// no longer needed, or retained longer in main table if required.
//
// Remaining archive functions:
//   - routing_decision_log: lightweight archive
//   - credential_model_index: 7-day cutoff archive
//   - state tables (routing_decision_log, candidate_failure_logs, handoff_logs):
//     day-2 monthly DROP via drop_old_state_partitions (per-table retention)
func archiveSpecs() []archiveSpec {
	return []archiveSpec{
		{day: 1, fnName: "archive_routing_decision_log", label: "routing_decision_log"},
		{day: 2, fnName: "drop_old_state_partitions", label: "state_tables"},
		{day: 3, fnName: "archive_credential_model_index", label: "credential_model_index"},
	}
}

// promoteSpecs lists every hot → monthly-partition migration function.
// Migrations 341, 343-347 (2026-07-05) unified all tables to hot table architecture.
// All promote functions now use *_hot_to_partition pattern.
//
// 2026-07-13: added candidate_failure_logs_hot (Migration 392).
//
// Each function signature is promote_<table>_hot_to_partition(p_retention interval,
// p_batch_size int) RETURNS bigint; the caller loops until the function
// returns 0 (no more eligible cold rows for this table).
func promoteSpecs() []archiveSpec {
	return []archiveSpec{
		{fnName: "promote_request_logs_hot_to_partition", label: "request_logs_hot"},
		{fnName: "promote_usage_ledger_hot_to_partition", label: "usage_ledger_hot"},
		{fnName: "promote_request_wal_hot_to_partition", label: "request_wal_hot"},
		{fnName: "promote_routing_decision_log_hot_to_partition", label: "routing_decision_log_hot"},
		{fnName: "promote_credential_model_index_hot_to_partition", label: "credential_model_index_hot"},
		{fnName: "promote_request_logs_bodies_hot_to_partition", label: "request_logs_bodies"},
		{fnName: "promote_credit_ledger_hot_to_partition", label: "credit_ledger"},
		{fnName: "promote_tool_usage_stats_hot_to_partition", label: "tool_usage_stats"},
		// model_probe_runs_hot 已切换为纯 hot 表策略（2026-07-14），
		// 不再 promote 到 columnar 分区。hot 表数据通过 cleanupOldModelProbeRuns()
		// 按 lifecycle.model_probe_runs_ttl_days 直接 DELETE 清理。
		// {fnName: "promote_model_probe_runs_hot_to_partition", label: "model_probe_runs_hot"},
		{fnName: "promote_candidate_failure_logs_hot_to_partition", label: "candidate_failure_logs_hot"}, // Migration 392
	}
}

// promoteDefaultToPartitions iterates promoteSpecs() and, for each,
// drains all cold rows from *_hot tables into the matching monthly
// partition by repeatedly calling promote_<table>_hot_to_partition with
// DefaultRetentionWindow and promoteBatchSize. Each iteration is one
// single-statement CTE inside PostgreSQL — atomic per batch.
//
// Retention and batch size come from settings.Global (hot-reloadable).
// model_probe_runs_hot no longer participates in this flow.
//
// The function always terminates: each batch either moves
// `promoteBatchSize` rows (caller loops) or 0 rows (caller breaks
// out). On error we log and move to the next table so one broken
// function does not starve the others.
// promoteLockKey returns a stable advisory-lock key for a hot-table label.
// Both gateway instances (e.g. 245 + 154) share the same PostgreSQL, so two
// concurrent hourly promote cycles can race the same *_hot table and one
// ends up blocked into PG statement_timeout. pg_try_advisory_xact_lock on
// this key makes the loser skip the table for that tick instead of waiting.
// The key is derived with FNV-1a so it is identical across instances.
func promoteLockKey(label string) int64 {
	const prefix = "llm-gateway:promote:"
	h := fnv.New64a()
	h.Write([]byte(prefix + label))
	return int64(h.Sum64())
}

func (pm *PartitionManager) promoteDefaultToPartitions(ctx context.Context) {
	if pm.promoteInterval == 0 {
		// Disabled via SetPromoteInterval(0) — used by tests.
		return
	}
	for _, s := range promoteSpecs() {
		retention, batchSize := resolvePromoteConfig(s.label)
		lockKey := promoteLockKey(s.label)
		for {
			if ctx.Err() != nil {
				return
			}
			timeoutCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
			tx, err := pm.db.Begin(timeoutCtx)
			if err != nil {
				cancel()
				slog.Error("partition_manager: promote begin failed",
					"label", s.label, "error", err)
				break
			}
			// Serialize against the peer gateway's promote on the same
			// shared table. pg_try_advisory_xact_lock never blocks: if the
			// other instance holds the key, skip this table for this tick
			// instead of waiting into PG statement_timeout=30s.
			var locked bool
			if err := tx.QueryRow(timeoutCtx,
				"SELECT pg_try_advisory_xact_lock($1)", lockKey,
			).Scan(&locked); err != nil {
				tx.Rollback(timeoutCtx)
				cancel()
				slog.Error("partition_manager: promote lock failed",
					"label", s.label, "error", err)
				break
			}
			if !locked {
				tx.Rollback(timeoutCtx)
				cancel()
				slog.Debug("partition_manager: promote skipped (peer holds lock)",
					"label", s.label)
				break
			}
			var n int64
			err = tx.QueryRow(timeoutCtx,
				"SELECT "+s.fnName+"($1::interval, $2::int)",
				retention, batchSize,
			).Scan(&n)
			commitErr := tx.Commit(timeoutCtx) // releases the xact lock
			cancel()
			if err != nil {
				slog.Error("partition_manager: promote failed",
					"fn", s.fnName, "label", s.label,
					"retention", retention, "batch_size", batchSize,
					"error", err)
				break // move on to the next table
			}
			if commitErr != nil {
				slog.Error("partition_manager: promote commit failed",
					"label", s.label, "error", commitErr)
				break
			}
			if n == 0 {
				slog.Debug("partition_manager: promote done",
					"label", s.label,
					"retention", retention)
				break
			}
			slog.Info("partition_manager: promote batch",
				"label", s.label, "rows", n)
		}
	}
	pm.analyzePartitionStats(ctx)
}

// analyzePartitionStats refreshes planner stats on hot heap tables and recent
// monthly partitions. Columnar partitions after bulk promote often keep
// n_mod_since_analyze=0 so autovacuum analyze never fires.
func (pm *PartitionManager) analyzePartitionStats(ctx context.Context) {
	// 2026-07-20: 5 min cooldown. analyze 一次 3.3s 频繁跑会拖慢 commit.
	pm.mu.Lock()
	if !pm.lastAnalyzeAt.IsZero() && time.Since(pm.lastAnalyzeAt) < 5*time.Minute {
		pm.mu.Unlock()
		return
	}
	pm.lastAnalyzeAt = time.Now()
	pm.mu.Unlock()

	timeoutCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	var n int64
	err := pm.db.QueryRow(timeoutCtx,
		"SELECT analyze_llm_gateway_table_stats($1)", 2,
	).Scan(&n)
	if err != nil {
		slog.Warn("partition_manager: analyze stats failed", "error", err)
		return
	}
	if n > 0 {
		slog.Info("partition_manager: analyze stats", "tables", n)
	}
}

// resolvePromoteConfig returns the retention interval and batch size for
// a given hot table. Reads from settings.Global with hot-reload support
// (every call reads the latest value from settings_kv). Falls back to
// DefaultRetentionWindow / promoteBatchSize for tables without a
// per-table setting.
//
// This function is called on every promote tick — there is no caching
// layer to invalidate. Updated settings take effect within one tick
// (DefaultPromoteInterval = 1h).
//
// Per-table retention settings (all hot-reloadable, all default 24h except
// request_logs_bodies which is 1d due to size):
//   - lifecycle.hot_retention_hours            — request_logs_hot, usage_ledger_hot, ...
//   - lifecycle.request_logs_bodies_retention_hours — request_logs_bodies (1d default)
//   - lifecycle.credential_model_index_ttl_days  — credential_model_index (reaped by
//     cleanup_old_credential_model_index())
func resolvePromoteConfig(label string) (time.Duration, int) {
	switch label {
	case "request_logs_bodies":
		// 2026-07-13: request_logs_bodies stores full request/response
		// payloads (TOAST). It grew to 3.4 GB / 24k rows in one month
		// because the default retention was 7d. Bodies are only needed
		// for forensic / export use cases — operators rarely look at
		// them more than a day after the fact. Default drops to 24h;
		// operators can override via
		// lifecycle.request_logs_bodies_retention_hours.
		hours := settingsGetPlatformInt("lifecycle.request_logs_bodies_retention_hours", 24)
		retention := time.Duration(hours) * time.Hour
		if retention < time.Hour {
			retention = time.Hour // safety floor — at least 1h
		}
		batchSize := settingsGetPlatformInt(
			"lifecycle.request_logs_bodies_promote_batch_size",
			requestLogsBodiesPromoteBatchSize,
		)
		if batchSize < 100 {
			batchSize = 100 // safety floor — avoid pathological micro-batches
		}
		return retention, batchSize
	default:
		hours := settingsGetPlatformInt("lifecycle.hot_retention_hours", int(DefaultRetentionWindow.Hours()))
		retention := time.Duration(hours) * time.Hour
		if retention < time.Hour {
			retention = time.Hour
		}
		batchSize := settingsGetPlatformInt("lifecycle.promote_batch_size", promoteBatchSize)
		if batchSize < 100 {
			batchSize = 100
		}
		if batchSize > 50_000 {
			batchSize = 50_000
		}
		return retention, batchSize
	}
}
