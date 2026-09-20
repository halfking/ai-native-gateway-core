package bg

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	todaySuccessProbeInterval = 15 * time.Minute
	todaySuccessProbeLookback = 24 * time.Hour
	todaySuccessProbeBatch    = 40
	// todaySuccessPerCredentialCap bounds how many pairs ONE credential may
	// contribute per tick (2026-09-20 probe-volume policy): the scan is a
	// recovery re-verification, not a fleet census. Two is the policy's
	// consecutive-success budget — with the credential-level two-success
	// gate, a credential that recovers is verified twice and then skipped
	// until a new failure re-arms it.
	todaySuccessPerCredentialCap = 2
)

type todaySuccessProbeDB interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// TodaySuccessProbe submits a bounded set of (credential, model) pairs that
// had a successful business request today but are currently judged unhealthy
// (binding unavailable or last probe failed), so recovery is re-verified.
//
// 2026-09-20 probe-volume policy (docs/probe/2026-09-20-probe-volume-optimization.md):
// previously this scanner re-probed every recently-used pair whose
// last_attempt_at was older than 60 minutes — an hourly re-probe of the
// healthy fleet by design. Now:
//   - only pairs with a current unhealthy signal are submitted (available=FALSE
//     or last_direct_ok=FALSE); healthy pairs are never re-probed here —
//     business success is the health evidence;
//   - credentials that already have ≥2 distinct models whose latest probe run
//     succeeded within 24h are skipped entirely (两连成功早停);
//   - each credential contributes at most todaySuccessPerCredentialCap pairs
//     per tick, most-recently-used first.
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
		slog.Info("today success probe started (recovery re-verify only)",
			"interval", todaySuccessProbeInterval,
			"lookback", todaySuccessProbeLookback,
			"batch", todaySuccessProbeBatch,
			"per_credential_cap", todaySuccessPerCredentialCap)
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
		// source=selfcheck（FR-selfcheck-timely-recovery）：这是主动恢复
		// 扫描，不是业务失败反应；走 Submit 会把审计行静默归因为
		// request_failure，看板与自检流 origin 徽章全部失真。
		p.worker.SubmitWithSource(credID, model, "default", "today-success-probe", "selfcheck")
		submitted++
	}
	if err := rows.Err(); err != nil {
		slog.Warn("today success probe rows failed", "error", err)
		return
	}
	slog.Info("today success probe queued", "pairs", submitted)
}

// todaySuccessProbeSQL is extracted for guard tests (same pattern as
// pumpDueStatesSQL). It returns pairs that (a) carried real (non-probe)
// successful traffic in the last 24h, (b) are currently judged unhealthy —
// binding unavailable OR last probe round failed — and (c) belong to a
// credential that has NOT already been re-verified by two distinct models
// probing successfully within 24h (两连成功早停). Each credential contributes
// at most todaySuccessPerCredentialCap pairs, most-recently-used first.
func todaySuccessProbeSQL() string {
	return `
		WITH used AS (
			SELECT rl.credential_id AS id, pm.raw_model_name, MAX(rl.ts) AS last_used_at
			FROM request_logs_hot rl
			JOIN credential_model_bindings cmb ON cmb.credential_id = rl.credential_id
			JOIN provider_models pm ON pm.id = cmb.provider_model_id
			WHERE rl.ts >= now() - interval '24 hours'
			  AND rl.success = TRUE
			  AND ` + fmt.Sprintf(probeTrafficExclusionPredicate, "rl") + `
			  AND rl.credential_id IS NOT NULL
			  AND pm.raw_model_name <> ''
			  AND (pm.raw_model_name = rl.client_model
			       OR pm.raw_model_name = rl.outbound_model
			       OR pm.outbound_model_name = rl.outbound_model)
			GROUP BY rl.credential_id, pm.raw_model_name
		), unhealthy AS (
			SELECT used.id, used.raw_model_name, used.last_used_at
			FROM used
			JOIN credential_model_bindings cmb ON cmb.credential_id = used.id
			JOIN provider_models pm ON pm.id = cmb.provider_model_id
			  AND pm.raw_model_name = used.raw_model_name
			LEFT JOIN node_probe_state nps
			  ON nps.credential_id = used.id
			 AND nps.raw_model_name = used.raw_model_name
			WHERE COALESCE(cmb.available, TRUE) = FALSE
			   OR COALESCE(nps.last_direct_ok, TRUE) = FALSE
		), ranked AS (
			SELECT unhealthy.*,
			       row_number() OVER (
			           PARTITION BY unhealthy.id
			           ORDER BY unhealthy.last_used_at DESC, unhealthy.raw_model_name
			       ) AS rn
			FROM unhealthy
			WHERE NOT ` + credentialTwoProbeSuccessGateSQL("unhealthy.id") + `
		)
		SELECT id, raw_model_name
		FROM ranked
		WHERE rn <= ` + fmt.Sprintf("%d", todaySuccessPerCredentialCap) + `
		ORDER BY last_used_at DESC, id, raw_model_name
		LIMIT $1`
}
