package bg

// ledger_reconciliation.go — Wave 3 B8 (2026-09-22): internal ledger
// reconciliation job (usage_ledger↔credit_ledger consistency).
//
// Design gap (§3 B8): until now only gateway↔provider-bill reconciliation
// existed (domains/providerprofile). The two internal ledgers were never
// cross-checked, so a crashed charge path or a mis-stamped credits_charged
// could silently diverge the wallet from what requests actually consumed.
// This job runs two checks on a fixed cadence and lands every difference in
// maas_reconciliation_findings (migration 737) plus a warning log + metric:
//
//  1. balance_chain — replay each tenant's credit_ledger rows in
//     (created_at, id) order: balance_after[n] must equal
//     balance_after[n-1] + amount[n]. A break means a ledger row was
//     written with a balance snapshot that doesn't chain (concurrent
//     wallet writers outside the serialised write path, manual edits,
//     partial replay).
//  2. usage_credit_mismatch — per (tenant, request_id): credits charged on
//     the request (request_logs_hot.credits_charged, the same value the
//     maas backfill treats as source-of-truth) must equal the summed
//     consume deductions in credit_ledger_hot with ref_type='request'.
//     Adjudication note: the audit named "usage_ledger", but that table
//     carries cost_usd only — the credit-side anchor of the usage family
//     lives on request_logs_hot.credits_charged, so this check reconciles
//     the two halves that actually both speak credits.
//
// Bounded noise: rows newer than settleLag are excluded (a request's ledger
// writes and its final log stamp can straddle the window edge); findings are
// capped per run; findings older than twice the window are deleted by the
// job itself.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

const (
	// DefaultLedgerReconciliationInterval is the job cadence.
	DefaultLedgerReconciliationInterval = time.Hour
	// DefaultLedgerReconciliationWindow is the lookback both checks scan.
	// Must stay within credit_ledger_hot / request_logs_hot retention.
	DefaultLedgerReconciliationWindow = 24 * time.Hour
	// ledgerSettleLag excludes the newest rows from both sides so a request
	// straddling the window boundary (ledger charged, log not yet stamped —
	// or vice versa) cannot false-positive.
	ledgerSettleLag = 10 * time.Minute
	// ledgerFindingsCap bounds one run's inserts; an incident floods
	// findings, not the table.
	ledgerFindingsCap = 200
)

var ledgerReconciliationFindings = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "llmgw_ledger_reconciliation_findings_total",
		Help: "Internal ledger reconciliation differences landed in maas_reconciliation_findings, by check kind.",
	},
	[]string{"check"},
)

// LedgerReconciler periodically cross-checks the internal ledgers.
type LedgerReconciler struct {
	pool              *pgxpool.Pool
	interval          time.Duration
	window            time.Duration
	maxFindingsPerRun int
	stopCh            chan struct{}
	stopOnce          sync.Once
	running           atomic.Bool
}

func NewLedgerReconciler(pool *pgxpool.Pool) *LedgerReconciler {
	return &LedgerReconciler{
		pool:              pool,
		interval:          DefaultLedgerReconciliationInterval,
		window:            DefaultLedgerReconciliationWindow,
		maxFindingsPerRun: ledgerFindingsCap,
		stopCh:            make(chan struct{}),
	}
}

// Start launches the ticker loop. Initial stagger mirrors the other bg
// workers so a cold boot does not probe the ledgers while migrations run.
func (r *LedgerReconciler) Start(ctx context.Context) {
	if r == nil || r.pool == nil {
		return
	}
	if !r.running.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer r.running.Store(false)
		select {
		case <-ctx.Done():
			return
		case <-r.stopCh:
			return
		case <-time.After(5 * time.Minute):
		}
		r.RunOnce(ctx)
		ticker := time.NewTicker(r.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-r.stopCh:
				return
			case <-ticker.C:
				r.RunOnce(ctx)
			}
		}
	}()
}

// Stop terminates the loop; safe to call multiple times.
func (r *LedgerReconciler) Stop() {
	r.stopOnce.Do(func() { close(r.stopCh) })
}

// RunOnce executes both checks, persists findings (capped) and returns the
// total number of differences observed.
func (r *LedgerReconciler) RunOnce(ctx context.Context) int {
	if r == nil || r.pool == nil {
		return 0
	}
	runCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	total := 0
	total += r.checkBalanceChain(runCtx)
	total += r.checkUsageCredit(runCtx)
	return total
}

// balanceChainSQL replays the per-tenant balance_after chain server-side
// and returns every break. Extracted for the SQL-shape regression test.
func balanceChainSQL() string {
	return `
		WITH chain AS (
		    SELECT tenant_id, id, amount, balance_after,
		           balance_after
		             - (lag(balance_after) OVER (PARTITION BY tenant_id ORDER BY created_at, id)
		                + amount) AS drift
		    FROM credit_ledger_hot
		    WHERE created_at > now() - $1::interval
		)
		SELECT tenant_id, id, amount, balance_after, drift
		FROM chain
		WHERE drift <> 0
		LIMIT $2
	`
}

// checkBalanceChain replays the per-tenant balance_after chain server-side
// and lands every break.
func (r *LedgerReconciler) checkBalanceChain(ctx context.Context) int {
	rows, err := r.pool.Query(ctx, balanceChainSQL(), r.window, r.maxFindingsPerRun)
	if err != nil {
		slog.Warn("ledger reconciliation: balance chain scan failed", "error", err)
		return 0
	}
	defer rows.Close()
	count := 0
	type row struct {
		tenantID     string
		id           int64
		amount       int64
		balanceAfter int64
		drift        int64
	}
	var batch []row
	for rows.Next() {
		var v row
		if err := rows.Scan(&v.tenantID, &v.id, &v.amount, &v.balanceAfter, &v.drift); err != nil {
			slog.Warn("ledger reconciliation: balance chain scan row failed", "error", err)
			return count
		}
		batch = append(batch, v)
	}
	if err := rows.Err(); err != nil {
		slog.Warn("ledger reconciliation: balance chain scan failed", "error", err)
		return count
	}
	for _, v := range batch {
		r.insertFinding(ctx, "balance_chain", v.tenantID, fmt.Sprintf("ledger:%d", v.id),
			v.balanceAfter-v.drift, v.balanceAfter, map[string]any{
				"amount":        v.amount,
				"expected_from": "prev_balance_after + amount",
			})
		count++
	}
	return count
}

// usageCreditSQL compares per-request credit charges (request_logs_hot)
// against ledger consume deductions (credit_ledger_hot). Extracted for the
// SQL-shape regression test.
func usageCreditSQL() string {
	return `
		WITH usage AS (
		    SELECT tenant_id, request_id, SUM(credits_charged) AS charged
		    FROM request_logs_hot
		    WHERE ts > now() - $1::interval
		      AND ts < now() - $2::interval
		      AND credits_charged IS NOT NULL AND credits_charged <> 0
		    GROUP BY tenant_id, request_id
		),
		credit AS (
		    SELECT tenant_id, ref_id, SUM(-amount) AS debited
		    FROM credit_ledger_hot
		    WHERE created_at > now() - $1::interval
		      AND created_at < now() - $2::interval
		      AND entry_type = 'consume' AND ref_type = 'request'
		    GROUP BY tenant_id, ref_id
		)
		SELECT COALESCE(u.tenant_id, c.tenant_id),
		       COALESCE(u.request_id, c.ref_id),
		       COALESCE(u.charged, 0), COALESCE(c.debited, 0)
		FROM usage u
		FULL OUTER JOIN credit c
		  ON u.tenant_id = c.tenant_id AND u.request_id = c.ref_id
		WHERE COALESCE(u.charged, 0) <> COALESCE(c.debited, 0)
		LIMIT $3
	`
}

// checkUsageCredit compares per-request credit charges (request_logs_hot)
// against ledger consume deductions (credit_ledger_hot).
func (r *LedgerReconciler) checkUsageCredit(ctx context.Context) int {
	rows, err := r.pool.Query(ctx, usageCreditSQL(), r.window, ledgerSettleLag, r.maxFindingsPerRun)
	if err != nil {
		slog.Warn("ledger reconciliation: usage↔credit scan failed", "error", err)
		return 0
	}
	defer rows.Close()
	count := 0
	type pair struct {
		tenantID, requestID string
		charged, debited    int64
	}
	var batch []pair
	for rows.Next() {
		var v pair
		if err := rows.Scan(&v.tenantID, &v.requestID, &v.charged, &v.debited); err != nil {
			slog.Warn("ledger reconciliation: usage↔credit scan row failed", "error", err)
			return count
		}
		batch = append(batch, v)
	}
	if err := rows.Err(); err != nil {
		slog.Warn("ledger reconciliation: usage↔credit scan failed", "error", err)
		return count
	}
	for _, v := range batch {
		r.insertFinding(ctx, "usage_credit_mismatch", v.tenantID, v.requestID,
			v.charged, v.debited, map[string]any{
				"charged_on_request": v.charged,
				"debited_in_ledger":  v.debited,
			})
		count++
	}
	return count
}

func (r *LedgerReconciler) insertFinding(ctx context.Context, kind, tenantID, refID string, expected, actual int64, detail map[string]any) {
	ictx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	detailJSON, err := json.Marshal(detail)
	if err != nil {
		detailJSON = []byte("{}")
	}
	_, err = r.pool.Exec(ictx, `
		INSERT INTO maas_reconciliation_findings
		    (check_kind, tenant_id, ref_id, expected, actual, detail)
		VALUES ($1, $2, $3, $4, $5, $6::jsonb)
	`, kind, tenantID, refID, expected, actual, string(detailJSON))
	if err != nil {
		slog.Warn("ledger reconciliation: finding insert failed",
			"check", kind, "tenant", tenantID, "ref", refID, "error", err)
	}
	ledgerReconciliationFindings.WithLabelValues(kind).Inc()
	slog.Warn("ledger reconciliation: difference found",
		"check", kind, "tenant_id", tenantID, "ref_id", refID,
		"expected", expected, "actual", actual)
}
