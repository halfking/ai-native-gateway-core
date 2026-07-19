package bg

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	dailyProbeLookback = 72 * time.Hour
	dailyProbeInterval = 24 * time.Hour
)

// DailyProbeAudit submits every credential/model pair used or degraded in the
// last three days to NodeProbeWorker. It uses the same durable queue and
// worker-level dedup/backoff, so a daily sweep cannot create an unbounded burst.
type DailyProbeAudit struct {
	db     *pgxpool.Pool
	worker *NodeProbeWorker
}

func NewDailyProbeAudit(db *pgxpool.Pool, worker *NodeProbeWorker) *DailyProbeAudit {
	return &DailyProbeAudit{db: db, worker: worker}
}

// Start runs one audit immediately, then repeats every 24 hours.
func (a *DailyProbeAudit) Start(ctx context.Context) {
	if a == nil || a.db == nil || a.worker == nil {
		return
	}
	go func() {
		a.run(ctx)
		ticker := time.NewTicker(dailyProbeInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				a.run(ctx)
			}
		}
	}()
	slog.Info("daily probe audit started", "lookback", dailyProbeLookback, "interval", dailyProbeInterval)
}

func (a *DailyProbeAudit) run(ctx context.Context) {
	queryCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	rows, err := a.db.Query(queryCtx, `
		SELECT DISTINCT credential_id, raw_model_name
		FROM (
			SELECT rl.credential_id, pm.raw_model_name
			FROM request_logs rl
			JOIN credential_model_bindings cmb ON cmb.credential_id = rl.credential_id
			JOIN provider_models pm ON pm.id = cmb.provider_model_id
			WHERE rl.ts >= now() - interval '3 days'
			  AND rl.credential_id IS NOT NULL
			  AND pm.raw_model_name <> ''
			  AND (pm.raw_model_name = rl.client_model
			       OR pm.raw_model_name = rl.outbound_model
			       OR pm.outbound_model_name = rl.outbound_model)
			UNION ALL
			SELECT credential_id, raw_model_name
			FROM candidate_failure_logs
			WHERE ts >= now() - interval '3 days'
			  AND credential_id IS NOT NULL
			  AND raw_model_name <> ''
		) recent
		WHERE credential_id > 0 AND raw_model_name <> ''
		ORDER BY credential_id, raw_model_name`)
	if err != nil {
		slog.Warn("daily probe audit query failed", "error", err)
		return
	}
	defer rows.Close()

	submitted := 0
	for rows.Next() {
		var credID int
		var model string
		if err := rows.Scan(&credID, &model); err != nil {
			slog.Warn("daily probe audit scan failed", "error", err)
			continue
		}
		a.worker.Submit(credID, model, "default", "daily-probe-audit")
		submitted++
	}
	if err := rows.Err(); err != nil {
		slog.Warn("daily probe audit rows failed", "error", err)
		return
	}
	slog.Info("daily probe audit queued", "pairs", submitted)
}
