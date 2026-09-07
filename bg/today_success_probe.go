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
	todaySuccessProbeInterval    = 15 * time.Minute
	todaySuccessProbeLookback    = 24 * time.Hour
	todaySuccessProbeBatch       = 40
	// Healthy pairs are re-probed at most hourly (same cadence as the
	// node_probe_state success re-arm). Business success already stamps
	// last_attempt_at, so busy healthy nodes are never probed at all.
	todaySuccessHealthySkipAfter = time.Hour
)

type todaySuccessProbeDB interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// TodaySuccessProbe submits a bounded set of (credential, model) pairs that
// had a successful business request today. Failed or never-probed pairs are
// always included; recently healthy pairs are skipped so the scan stays cheap.
type TodaySuccessProbe struct {
	db     todaySuccessProbeDB
	worker *NodeProbeWorker

	stopCh    chan struct{}
	stopOnce  sync.Once
	startOnce sync.Once
}

func NewTodaySuccessProbe(db *pgxpool.Pool, worker *NodeProbeWorker) *TodaySuccessProbe {
	return &TodaySuccessProbe{db: db, worker: worker, stopCh: make(chan struct{})}
}

func (p *TodaySuccessProbe) Start(ctx context.Context) {
	if p == nil || p.db == nil || p.worker == nil {
		return
	}
	p.startOnce.Do(func() {
		go func() {
			p.run(ctx)
			ticker := time.NewTicker(todaySuccessProbeInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-p.stopCh:
					return
				case <-ticker.C:
					p.run(ctx)
				}
			}
		}()
		slog.Info("today success probe started",
			"interval", todaySuccessProbeInterval,
			"lookback", todaySuccessProbeLookback,
			"batch", todaySuccessProbeBatch)
	})
}

func (p *TodaySuccessProbe) Stop() {
	if p == nil {
		return
	}
	p.stopOnce.Do(func() { close(p.stopCh) })
}

func (p *TodaySuccessProbe) run(ctx context.Context) {
	queryCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	rows, err := p.db.Query(queryCtx, todaySuccessProbeSQL(), todaySuccessProbeBatch)
	if err != nil {
		slog.Warn("today success probe query failed", "error", err)
		return
	}
	defer rows.Close()
	submitted := 0
	for rows.Next() {
		var credID int
		var model string
		if err := rows.Scan(&credID, &model); err != nil {
			slog.Warn("today success probe scan failed", "error", err)
			continue
		}
		p.worker.Submit(credID, model, "default", "today-success-probe")
		submitted++
	}
	if err := rows.Err(); err != nil {
		slog.Warn("today success probe rows failed", "error", err)
		return
	}
	slog.Info("today success probe queued", "pairs", submitted)
}

func todaySuccessProbeSQL() string {
	return `
		SELECT rl.credential_id, pm.raw_model_name
		FROM request_logs_hot rl
		JOIN credential_model_bindings cmb ON cmb.credential_id = rl.credential_id
		JOIN provider_models pm ON pm.id = cmb.provider_model_id
		LEFT JOIN node_probe_state nps
		  ON nps.credential_id = rl.credential_id
		 AND nps.raw_model_name = pm.raw_model_name
		WHERE rl.ts >= now() - interval '24 hours'
		  AND rl.success = TRUE
		  AND NOT COALESCE('probe' = ANY(rl.quality_flags), FALSE)
		  AND rl.credential_id IS NOT NULL
		  AND pm.raw_model_name <> ''
		  AND (pm.raw_model_name = rl.client_model
		       OR pm.raw_model_name = rl.outbound_model
		       OR pm.outbound_model_name = rl.outbound_model)
		  AND (
		      nps.credential_id IS NULL
		      OR COALESCE(cmb.available, FALSE) = FALSE
		      OR COALESCE(nps.last_direct_ok, FALSE) = FALSE
		      OR nps.last_attempt_at IS NULL
		      OR nps.last_attempt_at < now() - interval '60 minutes'
		  )
		GROUP BY rl.credential_id, pm.raw_model_name
		ORDER BY MAX(rl.ts) DESC, rl.credential_id, pm.raw_model_name
		LIMIT $1`
}
