package bg

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	dailyProbeLookback = 72 * time.Hour
	dailyProbeInterval = 24 * time.Hour
)

type dailyProbeAuditorDB interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// DailyProbeAudit submits every credential/model pair used or degraded in the
// last three days to NodeProbeWorker. NodeProbeWorker performs in-memory and
// database-level deduplication/backoff; Submit itself is not a durable queue.
type DailyProbeAudit struct {
	db     dailyProbeAuditorDB
	worker *NodeProbeWorker

	stopCh    chan struct{}
	stopOnce  sync.Once
	startOnce sync.Once
}

func NewDailyProbeAudit(db *pgxpool.Pool, worker *NodeProbeWorker) *DailyProbeAudit {
	return &DailyProbeAudit{
		db:     db,
		worker: worker,
		stopCh: make(chan struct{}),
	}
}

// Start runs one audit immediately, then repeats every 24 hours. It is safe to
// call repeatedly.
func (a *DailyProbeAudit) Start(ctx context.Context) {
	if a == nil || a.db == nil || a.worker == nil {
		return
	}
	a.startOnce.Do(func() {
		go func() {
			a.run(ctx)
			ticker := time.NewTicker(dailyProbeInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-a.stopCh:
					return
				case <-ticker.C:
					a.run(ctx)
				}
			}
		}()
		slog.Info("daily probe audit started", "lookback", dailyProbeLookback, "interval", dailyProbeInterval)
	})
}

// Stop requests termination. It is safe to call repeatedly, including before Start.
func (a *DailyProbeAudit) Stop() {
	if a == nil {
		return
	}
	a.stopOnce.Do(func() { close(a.stopCh) })
}

// dailyProbeAuditSQL is extracted for guard tests (same pattern as
// pumpDueStatesSQL). The UNION's request_logs branch is binding-anchored by
// its own cmb/pm JOINs, but the candidate_failure_logs branch is a raw
// (credential_id, raw_model_name) projection — historical pairs keep being
// submitted for the whole 72h lookback window even after their binding chain
// is broken, and every such submission is a doomed run (queue claim →
// in-flight tile → endpoint-build "no rows" → missing-binding drop +
// fake-success audit row). The outer binding-chain EXISTS (same predicate
// shape as pumpDueStatesSQL's — existence only, both columns correlated, pm
// JOINed so a dangling cmb.provider_model_id is filtered) covers the UNION:
// redundant-but-true for branch 1, filtering for branch 2.
func dailyProbeAuditSQL() string {
	return `
		SELECT DISTINCT credential_id, raw_model_name
		FROM (
			SELECT rl.credential_id, pm.raw_model_name
			FROM request_logs_with_current_month rl
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
		  AND EXISTS (
			SELECT 1
			FROM credential_model_bindings cmb
			JOIN provider_models pm ON pm.id = cmb.provider_model_id
			WHERE cmb.credential_id = recent.credential_id
			  AND pm.raw_model_name = recent.raw_model_name
		  )
		ORDER BY credential_id, raw_model_name`
}

func (a *DailyProbeAudit) run(ctx context.Context) {
	queryCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	rows, err := a.db.Query(queryCtx, dailyProbeAuditSQL())
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
