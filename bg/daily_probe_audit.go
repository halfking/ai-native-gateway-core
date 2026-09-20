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
	dailyProbeLookback = 72 * time.Hour
	dailyProbeInterval = 24 * time.Hour
	// dailyProbeAuditBatch caps one audit run. The pre-2026-09-20 query had
	// NO bound: a busy fleet's 3-day used∪failed pair set was submitted in
	// full every 24h. 100 pairs/day fleet-wide keeps the daily nudge cheap;
	// error pairs beyond it are still owned by their own failure ladders.
	dailyProbeAuditBatch = 100
	// dailyProbeUsagePerCredentialCap bounds the usage branch (INV-4 budget):
	// per error-evidence credential, verify at most the 2 most recently used
	// models per audit pass. Combined with the credential two-success gate a
	// recovering credential stops consuming audit slots after two probe
	// successes.
	dailyProbeUsagePerCredentialCap = 2
)

type dailyProbeAuditorDB interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// DailyProbeAudit submits credential/model pairs that need a daily nudge to
// NodeProbeWorker. 2026-09-20 probe-volume policy
// (docs/probe/2026-09-20-probe-volume-optimization.md) re-scoped it from
// "everything used or failed in 3 days" to an error-gated verification scan:
//
//   - usage branch: only credentials with failure evidence in the window
//     (candidate_failure_logs), only models with real (non-probe) traffic on
//     that credential in 3 days, at most 2 per credential;
//   - failure branch: pairs that actually failed in 3 days (the tracking
//     set), deduped against the usage branch;
//   - credentials that already have ≥2 distinct models whose latest probe
//     run succeeded within 24h are skipped entirely (两连成功早停);
//   - the whole run is bounded by dailyProbeAuditBatch.
//
// Healthy no-error credentials produce zero submissions — business success is
// the health evidence; continuous probing is the failure ladders' job.
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
		slog.Info("daily probe audit started (error-gated verification scan)",
			"lookback", dailyProbeLookback,
			"interval", dailyProbeInterval,
			"batch", dailyProbeAuditBatch,
			"usage_per_credential_cap", dailyProbeUsagePerCredentialCap)
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
// pumpDueStatesSQL). The UNION's usage branch is binding-anchored by its own
// cmb/pm JOINs and now excludes probe traffic ('probe' quality flag) so
// probes can no longer count as usage and keep the audit self-sustaining
// (INV-3). The failure branch is a raw (credential_id, raw_model_name)
// projection; the outer binding-chain EXISTS (existence only, both columns
// correlated, pm JOINed so a dangling cmb.provider_model_id is filtered)
// covers the UNION: redundant-but-true for branch 1, filtering for branch 2.
// The two-success gate applies to BOTH branches: a credential already
// re-verified by two probe successes leaves no scheduled work (INV-4).
func dailyProbeAuditSQL() string {
	return `
		WITH used AS (
			SELECT rl.credential_id AS id, pm.raw_model_name, MAX(rl.ts) AS last_used_at
			FROM request_logs_with_current_month rl
			JOIN credential_model_bindings cmb ON cmb.credential_id = rl.credential_id
			JOIN provider_models pm ON pm.id = cmb.provider_model_id
			WHERE rl.ts >= now() - ` + probeUsageWindowInterval + `
			  AND rl.credential_id IS NOT NULL
			  AND pm.raw_model_name <> ''
			  AND ` + fmt.Sprintf(probeTrafficExclusionPredicate, "rl", "rl") + `
			  AND (pm.raw_model_name = rl.client_model
			       OR pm.raw_model_name = rl.outbound_model
			       OR pm.outbound_model_name = rl.outbound_model)
			GROUP BY rl.credential_id, pm.raw_model_name
		), usage_ranked AS (
			SELECT u.id, u.raw_model_name, u.last_used_at,
			       row_number() OVER (
			           PARTITION BY u.id
			           ORDER BY u.last_used_at DESC, u.raw_model_name
			       ) AS rn
			FROM used u
			JOIN credentials c ON c.id = u.id
			WHERE ` + credentialFailureEvidenceSQL("c.id", probeUsageWindowInterval) + `
			  AND NOT ` + credentialTwoProbeSuccessGateSQL("u.id") + `
		), failing AS (
			SELECT f.credential_id AS id, f.raw_model_name, MAX(f.ts) AS last_used_at
			FROM candidate_failure_logs_with_current_month f
			WHERE f.ts >= now() - ` + probeUsageWindowInterval + `
			  AND f.credential_id IS NOT NULL
			  AND f.raw_model_name <> ''
			GROUP BY f.credential_id, f.raw_model_name
		), recent AS (
			SELECT id, raw_model_name FROM usage_ranked
			WHERE rn <= ` + fmt.Sprintf("%d", dailyProbeUsagePerCredentialCap) + `
			UNION
			SELECT f.id, f.raw_model_name
			FROM failing f
			WHERE NOT ` + credentialTwoProbeSuccessGateSQL("f.id") + `
		)
		SELECT DISTINCT recent.id, recent.raw_model_name
		FROM recent
		WHERE recent.id > 0 AND recent.raw_model_name <> ''
		  AND EXISTS (
			SELECT 1
			FROM credential_model_bindings cmb
			JOIN provider_models pm ON pm.id = cmb.provider_model_id
			WHERE cmb.credential_id = recent.id
			  AND pm.raw_model_name = recent.raw_model_name
		  )
		ORDER BY recent.id, recent.raw_model_name
		LIMIT $1`
}

func (a *DailyProbeAudit) run(ctx context.Context) {
	queryCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	rows, err := a.db.Query(queryCtx, dailyProbeAuditSQL(), dailyProbeAuditBatch)
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
		// source=selfcheck：这是计划性核查扫描，不是业务失败反应；走 Submit
		// 会把审计行静默归因为 request_failure（2026-09-08 审计：主动扫描器
		// 归因失真），看板与自检流 origin 徽章全部失真。
		a.worker.SubmitWithSource(credID, model, "default", "daily-probe-audit", "selfcheck")
		submitted++
	}
	if err := rows.Err(); err != nil {
		slog.Warn("daily probe audit rows failed", "error", err)
		return
	}
	slog.Info("daily probe audit queued", "pairs", submitted)
}
