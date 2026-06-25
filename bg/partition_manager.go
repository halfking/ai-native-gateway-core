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
// from 2 months ago (e.g. on 2026-07-01, archives 2026-05).
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
func (pm *PartitionManager) ensureNextMonthPartitions(ctx context.Context) {
	for offset := 0; offset <= 1; offset++ {
		targetMonth := time.Now().AddDate(0, offset, 0)
		timeoutCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		_, err := pm.db.Exec(timeoutCtx,
			"SELECT ensure_request_logs_partition($1)", targetMonth)
		cancel()
		if err != nil {
			slog.Error("partition_manager: ensure_request_logs_partition failed",
				"month", targetMonth.Format("2006-01"),
				"error", err)
			continue
		}
		slog.Info("partition_manager: ensured partition",
			"month", targetMonth.Format("2006-01"))
	}
}

// archiveOldPartitionsIfNeeded archives 2-months-old partition to columnar
// Only runs on day 1-3 of the month to minimize disruption
func (pm *PartitionManager) archiveOldPartitionsIfNeeded(ctx context.Context) {
	now := time.Now()
	if now.Day() > 3 {
		return
	}

	twoMonthsAgo := now.AddDate(0, -2, 0)

	timeoutCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	var status string
	var rowsMigrated int64
	var partitionDropped bool

	err := pm.db.QueryRow(timeoutCtx,
		"SELECT status, rows_migrated, partition_dropped FROM archive_request_logs($1)",
		twoMonthsAgo,
	).Scan(&status, &rowsMigrated, &partitionDropped)

	if err != nil {
		slog.Error("partition_manager: archive_request_logs failed",
			"month", twoMonthsAgo.Format("2006-01"),
			"error", err)
		return
	}

	switch status {
	case "success":
		slog.Info("partition_manager: archive succeeded",
			"month", twoMonthsAgo.Format("2006-01"),
			"rows_migrated", rowsMigrated,
			"partition_dropped", partitionDropped)
	case "skipped":
		slog.Debug("partition_manager: archive skipped (partition not found)",
			"month", twoMonthsAgo.Format("2006-01"))
	default:
		slog.Warn("partition_manager: archive returned unknown status",
			"month", twoMonthsAgo.Format("2006-01"),
			"status", status)
	}
}