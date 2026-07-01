package bg

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PartitionManager automatically creates next month's partition
// and archives old partitions to columnar storage.
//
// Runs daily. On day 1-3 of each month, archives the partition
// from 2 months ago. Four tables are scheduled on different days to
// spread the load:
//   - day 1: request_logs, routing_decision_log
//   - day 2: request_wal
//   - day 3: credential_model_index (7-day cutoff, so 2-month-old data
//     is still in the main table; this only migrates the truly stale
//     6-7-week-old rows)
//
// Usage:
//
//	pm := bg.NewPartitionManager(dbConn.Pool(), 24*time.Hour)
//	pm.Start(context.Background())
//	defer pm.Stop()
type PartitionManager struct {
	db       *pgxpool.Pool
	interval time.Duration
	cancel   context.CancelFunc
	done     chan struct{}
}

// archiveSpec describes one archive_xxx call: which SQL function to
// invoke and which day-of-month it should run on. day 0 means "run
// every day in the window" (kept for backwards compatibility if we
// ever go back to that style).
type archiveSpec struct {
	day     int    // 1..7 — only run archiveOldPartitions on this day
	fnName  string // SQL function name, e.g. "archive_request_logs"
	label   string // human label for logs
	enabled bool //nolint:unused
}

func NewPartitionManager(db *pgxpool.Pool, interval time.Duration) *PartitionManager {
	if interval == 0 {
		interval = 24 * time.Hour
	}
	return &PartitionManager{
		db:       db,
		interval: interval,
		done:     make(chan struct{}),
	}
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

	// Run once on startup
	pm.ensureNextMonthPartitions(ctx)
	pm.archiveOldPartitionsIfNeeded(ctx)

	ticker := time.NewTicker(pm.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pm.ensureNextMonthPartitions(ctx)
			pm.archiveOldPartitionsIfNeeded(ctx)
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
func (pm *PartitionManager) archiveOldPartitionsIfNeeded(ctx context.Context) {
	now := time.Now()
	if now.Day() > 3 {
		return
	}

	twoMonthsAgo := now.AddDate(0, -2, 0)
	today := now.Day()

	for _, s := range archiveSpecs() {
		if s.day != today {
			continue
		}

		pm.runArchive(ctx, s, twoMonthsAgo)
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
func ensureSpecs() []archiveSpec {
	return []archiveSpec{
		{fnName: "ensure_request_logs_partition", label: "request_logs"},
		{fnName: "ensure_request_wal_partition", label: "request_wal"},
		{fnName: "ensure_routing_decision_log_partition", label: "routing_decision_log"},
		{fnName: "ensure_credential_model_index_partition", label: "credential_model_index"},
	}
}

// archiveSpecs returns the archive_<table>() functions we invoke on
// day-1..3 of each month, paired with the day-of-month they run on.
// Two tables share day 1 (request_logs + routing_decision_log) because
// they are similar in size and the schema-aware columnar inserts run
// quickly. credential_model_index gets its own day because it carries
// a 7-day cutoff (data younger than 7d is preserved in the main
// table, so it has the most rows to scan).
func archiveSpecs() []archiveSpec {
	return []archiveSpec{
		{day: 1, fnName: "archive_request_logs", label: "request_logs"},
		{day: 1, fnName: "archive_routing_decision_log", label: "routing_decision_log"},
		{day: 2, fnName: "archive_request_wal", label: "request_wal"},
		{day: 3, fnName: "archive_credential_model_index", label: "credential_model_index"},
	}
}
