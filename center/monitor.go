package center

import (
	"context"
	"log/slog"
	"time"
)

// MonitorInstances monitors all instances and updates their status based on last_heartbeat.
// Status rules:
//   - now - last_heartbeat ≤ 120s → status = online
//   - 120s < now - last_heartbeat ≤ 360s → status = degraded
//   - > 360s → status = offline
//
// This function should be run in a goroutine with a ticker (e.g., every 30s).
func MonitorInstances(ctx context.Context, store Store, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	slog.Info("instance monitor started", "interval", interval)

	for {
		select {
		case <-ctx.Done():
			slog.Info("instance monitor stopped")
			return
		case <-ticker.C:
			if err := updateInstanceStatuses(ctx, store); err != nil {
				slog.Error("failed to update instance statuses", "error", err)
			}
		}
	}
}

// updateInstanceStatuses performs the actual status update logic
func updateInstanceStatuses(ctx context.Context, store Store) error {
	pgxStore, ok := store.(*PgxStore)
	if !ok {
		slog.Warn("store is not PgxStore, skipping batch status update")
		return nil
	}

	now := time.Now()
	onlineThreshold := now.Add(-120 * time.Second)
	degradedThreshold := now.Add(-360 * time.Second)

	query := `
		UPDATE gateway_instances
		SET status = CASE
			WHEN last_heartbeat IS NULL THEN status
			WHEN last_heartbeat >= $1 THEN $2
			WHEN last_heartbeat >= $3 THEN $4
			ELSE $5
		END
		WHERE last_heartbeat IS NOT NULL
	`

	result, err := pgxStore.db.Exec(ctx, query,
		onlineThreshold, StatusOnline,
		degradedThreshold, StatusDegraded,
		StatusOffline,
	)
	if err != nil {
		return err
	}

	rowsAffected := result.RowsAffected()
	if rowsAffected > 0 {
		slog.Debug("instance statuses updated", "rows_affected", rowsAffected)
	}

	return nil
}
