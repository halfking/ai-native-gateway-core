package bg

import (
	"context"
	"log/slog"
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
// Application-layer UPDATE/DELETE happens within seconds/minutes of the
// initial INSERT, so 7 days comfortably covers any in-flight mutation.
// If this constant is changed, also re-evaluate whether application-layer
// writes still fit inside the window.
//
// Per-table retention windows can be overridden via settings_kv:
//   - probe.hot_retention_hours (default 24)
//
// The PartitionManager reads these on every promote cycle so changes
// take effect on the next tick (no restart required, true hot reload).
const DefaultRetentionWindow = 7 * 24 * time.Hour

// defaultProbeHotRetention is the default retention for model_probe_runs_hot
// when settings.Global is nil or the key is missing. Mirrors the value in
// settings/spec_probe.go (probe.hot_retention_hours Default=24).
const defaultProbeHotRetention = 24 * time.Hour

// defaultProbeBatchSize mirrors settings/spec_probe.go (probe.promote_batch_size Default=5000).
const defaultProbeBatchSize = 5000

// defaultProbePartitionRetention mirrors settings/spec_probe.go (probe.partition_retention_days Default=90).
const defaultProbePartitionRetention = 90 * 24 * time.Hour

// promoteBatchSize is the per-call LIMIT inside each promote_xxx_batch
// CTE. Keeps per-tx memory bounded so a backlog cannot OOM the gateway.
//
// Per-table promote batch sizes can be overridden via settings_kv:
//   - probe.promote_batch_size (default 5000)
const promoteBatchSize = 5000

// PartitionManager automatically creates next month's partition,
// archives old partitions to columnar storage, and continuously
// migrates cold rows from *_default partitions into the matching
// monthly partition (so *_default only ever holds the recent 7-day
// window per the 2026-07 data-lifecycle architecture).
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
			timeoutCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			_, err := pm.db.Exec(timeoutCtx,
				"SELECT "+s.fnName+"($1)", targetMonth)
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
// Also drops old model_probe_runs partitions every tick (hot reload
// retention via probe.partition_retention_days). Separate from the
// archiveSpecs day-of-month gate because probe retention is typically
// much shorter (90 days) and dropping is O(1) (no row migration needed).
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

			pm.runArchive(ctx, s, twoMonthsAgo)
		}
	}

	// 2. model_probe_runs partition cleanup (every tick, hot-reloadable retention)
	pm.dropOldModelProbeRunsPartitions(ctx)
}

// dropOldModelProbeRunsPartitions drops monthly partitions of
// model_probe_runs older than the configured retention. Retention is
// read fresh from settings.Global on every call (probe.partition_retention_days,
// default 90), so changes take effect on the next partition_manager tick.
func (pm *PartitionManager) dropOldModelProbeRunsPartitions(ctx context.Context) {
	if !settings.GetPlatformBool("probe.partition_cleanup_enabled", true) {
		return
	}

	retentionDays := settings.GetPlatformInt("probe.partition_retention_days", int(defaultProbePartitionRetention.Hours()/24))
	if retentionDays < 7 {
		retentionDays = 7 // safety floor
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	// Use a simpler form: call the function and let it RAISE NOTICE on each drop.
	// The function returns TABLE(dropped_partition text, rows_dropped bigint).
	_, err := pm.db.Exec(timeoutCtx,
		"SELECT * FROM drop_old_model_probe_runs_partitions($1)",
		retentionDays,
	)
	if err != nil {
		slog.Error("partition_manager: model_probe_runs cleanup failed",
			"retention_days", retentionDays, "error", err)
		return
	}

	slog.Info("partition_manager: model_probe_runs cleanup ran",
		"retention_days", retentionDays)
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
		{fnName: "ensure_usage_ledger_partition", label: "usage_ledger"},         // Migration 330
		{fnName: "ensure_model_probe_runs_partition", label: "model_probe_runs"}, // Migration 385
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
func archiveSpecs() []archiveSpec {
	return []archiveSpec{
		{day: 1, fnName: "archive_routing_decision_log", label: "routing_decision_log"},
		{day: 3, fnName: "archive_credential_model_index", label: "credential_model_index"},
	}
}

// promoteSpecs lists every hot → monthly-partition migration function.
// Migrations 341, 343-347 (2026-07-05) unified all tables to hot table architecture.
// All promote functions now use *_hot_to_partition pattern.
//
// Migration 382/383 (2026-07-11) use archive_* functions instead of promote_*;
// they are invoked manually or via cron, not by this background loop.
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
		{fnName: "promote_model_probe_runs_hot_to_partition", label: "model_probe_runs_hot"},
	}
}

// promoteDefaultToPartitions iterates promoteSpecs() and, for each,
// drains all cold rows from *_hot tables into the matching monthly
// partition by repeatedly calling promote_<table>_hot_to_partition with
// DefaultRetentionWindow and promoteBatchSize. Each iteration is one
// single-statement CTE inside PostgreSQL — atomic per batch.
//
// Retention and batch size come from settings.Global (hot-reloadable):
//   - probe.hot_retention_hours (default 24h)
//   - probe.promote_batch_size (default 5000)
//
// All other tables use DefaultRetentionWindow (7d) and promoteBatchSize
// (5000) constants. Settings changes take effect on the next tick — no
// restart required.
//
// The function always terminates: each batch either moves
// `promoteBatchSize` rows (caller loops) or 0 rows (caller breaks
// out). On error we log and move to the next table so one broken
// function does not starve the others.
func (pm *PartitionManager) promoteDefaultToPartitions(ctx context.Context) {
	if pm.promoteInterval == 0 {
		// Disabled via SetPromoteInterval(0) — used by tests.
		return
	}
	for _, s := range promoteSpecs() {
		retention, batchSize := resolvePromoteConfig(s.label)
		for {
			if ctx.Err() != nil {
				return
			}
			timeoutCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
			var n int64
			err := pm.db.QueryRow(timeoutCtx,
				"SELECT "+s.fnName+"($1::interval, $2::int)",
				retention, batchSize,
			).Scan(&n)
			cancel()
			if err != nil {
				slog.Error("partition_manager: promote failed",
					"fn", s.fnName, "label", s.label,
					"retention", retention, "batch_size", batchSize,
					"error", err)
				break // move on to the next table
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
}

// resolvePromoteConfig returns the retention interval and batch size for
// a given hot table. Reads from settings.Global with hot-reload support
// (every call reads the latest value from settings_kv). Falls back to
// DefaultRetentionWindow / promoteBatchSize for tables without a
// per-table setting, and to type-specific defaults for model_probe_runs.
//
// This function is called on every promote tick — there is no caching
// layer to invalidate. Updated settings take effect within one tick
// (DefaultPromoteInterval = 1h).
func resolvePromoteConfig(label string) (time.Duration, int) {
	switch label {
	case "model_probe_runs_hot":
		// Per-table setting: probe.hot_retention_hours
		hours := settingsGetPlatformInt("probe.hot_retention_hours", int(defaultProbeHotRetention.Hours()))
		retention := time.Duration(hours) * time.Hour
		if retention < time.Hour {
			retention = time.Hour // safety floor
		}
		batchSize := settingsGetPlatformInt("probe.promote_batch_size", defaultProbeBatchSize)
		if batchSize < 100 {
			batchSize = 100
		}
		if batchSize > 50000 {
			batchSize = 50000
		}
		return retention, batchSize
	default:
		return DefaultRetentionWindow, promoteBatchSize
	}
}
