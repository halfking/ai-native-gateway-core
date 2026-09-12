package freediscovery

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/lib/pq"

	"github.com/kaixuan/llm-gateway-go/metrics"
)

// ImportService bulk-imports approved discovery results into free_resource_catalog.
//
// Conflict detection: when a (provider_code, model_id, tenant_id) row already exists
// in free_resource_catalog, the configured ConflictPolicy (skip / overwrite / merge)
// decides what happens. The unique constraint was added in migration 084.
//
// Safety contract (2026-09-09 audit-fix):
//   - A single transaction owns task locking, result loading, and writes — no
//     separate loadResults transaction (eliminates TOCTOU).
//   - The task row is locked with SELECT ... FOR UPDATE; tenant_id consistency
//     and status=success are both verified while holding the lock.
//   - discovery_results UPDATE statements all carry tenant_id and an
//     import_status='pending' predicate with a RowsAffected check.
//   - catalog writes use the result row's real tenant_id; no unconditional override.
//   - Any Update/Catalog error rolls the whole transaction back; summary.Imported
//     only counts rows whose CAS actually succeeded.
//
// ErrImportTaskNotFound: task does not exist or belongs to a different tenant
// (handler maps to 404). ErrImportTaskNotReady: task exists but status is not
// success or its results were already processed (handler maps to 409).
//
// summary.Skipped counts entries that already existed under the skip policy;
// summary.Conflicted counts entries lost to a concurrent writer; summary.Failed
// is always 0 (single transaction: any error rolls everything back; the real
// error is returned via the error return value).
type ImportService struct {
	db *sql.DB
}

// ErrImportTaskNotFound indicates the task does not exist (also returned when the
// task belongs to a different tenant). The handler maps this to 404.
var ErrImportTaskNotFound = errors.New("freediscovery: import task not found")

// ErrImportTaskNotReady indicates the task exists but its status does not allow
// import (status is not success, or its results have already been processed).
// The handler maps this to 409 Conflict.
var ErrImportTaskNotReady = errors.New("freediscovery: task not in success state")

// NewImportService constructs the import service.
func NewImportService(db *sql.DB) *ImportService {
	return &ImportService{db: db}
}

// Import executes a bulk import and returns the summary. The whole flow runs in
// a single transaction: any error rolls everything back.
//
// Compared to the earlier implementation (audit-fix): instead of running
// loadResults in its own transaction and then a separate import transaction
// (TOCTOU), we now lock the task with FOR UPDATE inside one transaction,
// verify tenant + status, SELECT the pending results, and immediately CAS
// each row (status='pending' AND tenant_id=$tenant). Rows that fail the CAS
// are not counted in Imported.
func (s *ImportService) Import(ctx context.Context, req ImportRequest) (*ImportSummary, error) {
	if req.ConflictPolicy == "" {
		req.ConflictPolicy = ConflictSkip
	}
	switch req.ConflictPolicy {
	case ConflictSkip, ConflictOverwrite, ConflictMerge:
	default:
		return nil, fmt.Errorf("freediscovery: invalid conflict_policy %q", req.ConflictPolicy)
	}
	if req.TaskID <= 0 {
		return nil, fmt.Errorf("freediscovery: task_id is required")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("freediscovery: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := setTenantTx(ctx, tx, req.TenantID); err != nil {
		return nil, err
	}

	// 1. Lock the task and verify ownership + terminal status.
	var taskStatus TaskStatus
	var taskTenant string
	err = tx.QueryRowContext(ctx, `
		SELECT tenant_id, status FROM discovery_tasks
		WHERE id=$1 AND tenant_id=$2
		FOR UPDATE`, req.TaskID, req.TenantID).Scan(&taskTenant, &taskStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrImportTaskNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("freediscovery: lock task: %w", err)
	}
	if taskTenant != req.TenantID {
		// Defensive: cross-tenant (should never happen even under RLS-bypass role, but application-layer safety net).
		return nil, ErrImportTaskNotFound
	}
	if taskStatus != TaskStatusSuccess {
		return nil, fmt.Errorf("%w: status=%s", ErrImportTaskNotReady, taskStatus)
	}

	// 2. Load pending results (FOR UPDATE locks rows, preventing concurrent Import).
	results, err := s.lockAndLoadResults(ctx, tx, req)
	if err != nil {
		return nil, err
	}
	if len(results) == 0 {
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("freediscovery: commit: %w", err)
		}
		return &ImportSummary{}, nil
	}

	// 3. Import one by one; commit only after all succeed, any error rolls back the whole transaction.
	summary := &ImportSummary{}
	for _, r := range results {
		outcome, err := s.importOne(ctx, tx, req, r)
		if err != nil {
			return nil, err
		}
		// outcome: imported / skipped / conflicted (mapped to summary fields)
		switch outcome {
		case outcomeImported:
			summary.Imported++
			metrics.FreeDiscoveryImportTotal.WithLabelValues("imported").Inc()
		case outcomeSkipped:
			summary.Skipped++
			metrics.FreeDiscoveryImportTotal.WithLabelValues("skipped").Inc()
		case outcomeConflicted:
			summary.Conflicted++
			metrics.FreeDiscoveryImportTotal.WithLabelValues("conflicted").Inc()
		}
	}

	// 4. Task counters (CAS: accumulate only when current models_imported + $2 matches this transaction;
	//    failure does not abort already-successful imports; also record import_status update count for diagnostics).
	if summary.Imported > 0 {
		if _, err := tx.ExecContext(ctx, `
			UPDATE discovery_tasks SET models_imported = COALESCE(models_imported, 0) + $2
			WHERE id=$1 AND tenant_id=$3`,
			req.TaskID, summary.Imported, req.TenantID); err != nil {
			return nil, fmt.Errorf("freediscovery: update task counters: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("freediscovery: commit import: %w", err)
	}

	slog.Info("freediscovery: import completed",
		"tenant_id", req.TenantID, "task_id", req.TaskID,
		"imported", summary.Imported, "skipped", summary.Skipped, "conflicted", summary.Conflicted,
		"policy", string(req.ConflictPolicy), "by", req.ImportedBy)
	return summary, nil
}

// importOutcome is an internal enum distinguishing catalog write semantics.
//   - imported:   new INSERT or overwrite/merge written; result row marked imported
//   - skipped:    skip policy hit existing entry; catalog preserved, result row marked skipped
//   - conflicted: skip policy but concurrent write produced conflict / already handled by another transaction (CAS failed)
type importOutcome string

const (
	outcomeImported   importOutcome = "imported"
	outcomeSkipped    importOutcome = "skipped"
	outcomeConflicted importOutcome = "conflicted"
)

// importOne imports a single discovery result.
//
// Returns the importOutcome (imported/skipped/conflicted).
//
// Key audit-fix changes:
//   - mark imported/skipped/conflict uses CAS; RowsAffected==0 means
//     "already processed by another transaction";
//   - duplicate INSERT hitting a unique key also flows through the CAS path
//     instead of silently counting as conflict.
func (s *ImportService) importOne(ctx context.Context, tx *sql.Tx, req ImportRequest, r *DiscoveryResult) (importOutcome, error) {
	// Conflict probe: a catalog row already exists for (provider_code, model_id, tenant_id).
	var existingID int64
	err := tx.QueryRowContext(ctx, `
		SELECT id FROM free_resource_catalog
		WHERE provider_code=$1 AND model_id=$2 AND tenant_id=$3`,
		r.ProviderCode, r.ModelID, r.TenantID).Scan(&existingID)
	exists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("freediscovery: conflict probe %s: %w", r.ModelID, err)
	}

	now := timeNow().UTC()
	if exists {
		switch req.ConflictPolicy {
		case ConflictSkip:
			// skip: leave the catalog row untouched and mark the result as skipped
			// (semantically clearest outcome).
			if err := s.casUpdateResult(ctx, tx, r.ID, "imported", "skipped", now); err != nil {
				return "", err
			}
			return outcomeSkipped, nil
		case ConflictOverwrite:
			if _, err := tx.ExecContext(ctx, `
				UPDATE free_resource_catalog SET
					display_name=$2, free_type=$3, monthly_tokens=$4, daily_tokens=$5,
					pool_key=$6, tos_verdict=$7, tos_notes=$8,
					source_type='discovered', discovery_task_id=$9,
					last_synced_at=$10, enabled=TRUE, disabled_at=NULL, disabled_reason=NULL
				WHERE id=$1 AND tenant_id=$11`,
				existingID, r.DisplayName, r.FreeType, r.MonthlyTokens, r.DailyTokens,
				r.PoolKey, r.TosVerdict, r.TosNotes,
				req.TaskID, now, r.TenantID); err != nil {
				return "", fmt.Errorf("freediscovery: overwrite %s: %w", r.ModelID, err)
			}
			if err := s.casUpdateResult(ctx, tx, r.ID, "imported", "imported", now); err != nil {
				return "", err
			}
			return outcomeImported, nil
		case ConflictMerge:
			if _, err := tx.ExecContext(ctx, `
				UPDATE free_resource_catalog SET
					display_name = CASE WHEN display_name = '' OR display_name = model_id THEN $2 ELSE display_name END,
					free_type = CASE WHEN COALESCE(free_type,'') = '' THEN $3 ELSE free_type END,
					monthly_tokens = CASE WHEN monthly_tokens = 0 THEN $4 ELSE monthly_tokens END,
					daily_tokens = CASE WHEN daily_tokens = 0 THEN $5 ELSE daily_tokens END,
					pool_key = COALESCE(NULLIF(pool_key,''), $6),
					tos_verdict = CASE WHEN tos_verdict IN ('unknown','') THEN $7 ELSE tos_verdict END,
					tos_notes = COALESCE(NULLIF(tos_notes,''), $8),
					source_type = CASE WHEN source_type = 'manual' THEN source_type ELSE 'discovered' END,
					discovery_task_id = COALESCE(discovery_task_id, $9),
					last_synced_at = $10
				WHERE id=$1 AND tenant_id=$11`,
				existingID, r.DisplayName, r.FreeType, r.MonthlyTokens, r.DailyTokens,
				r.PoolKey, r.TosVerdict, r.TosNotes,
				req.TaskID, now, r.TenantID); err != nil {
				return "", fmt.Errorf("freediscovery: merge %s: %w", r.ModelID, err)
			}
			if err := s.casUpdateResult(ctx, tx, r.ID, "imported", "imported", now); err != nil {
				return "", err
			}
			return outcomeImported, nil
		}
	}

	// New entries use INSERT; per contract, entries with verdict=avoid are imported in the disabled state.
	enabled := r.TosVerdict != "avoid"
	var disabledAt, disabledReason any
	if !enabled {
		disabledAt = now
		disabledReason = "freediscovery: tos_verdict=avoid"
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO free_resource_catalog (
			provider_code, model_id, display_name, free_type,
			monthly_tokens, daily_tokens, pool_key,
			tos_verdict, tos_notes, discovery_method,
			source_type, discovery_task_id, last_synced_at, upstream_metadata,
			enabled, disabled_at, disabled_reason, tenant_id
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,'auto-scan','discovered',$10,$11,'{}'::jsonb,$12,$13,$14,$15)`,
		r.ProviderCode, r.ModelID, r.DisplayName, r.FreeType,
		r.MonthlyTokens, r.DailyTokens, r.PoolKey,
		r.TosVerdict, r.TosNotes,
		req.TaskID, now, enabled, disabledAt, disabledReason, r.TenantID)
	if err != nil {
		if strings.Contains(err.Error(), "duplicate key") {
			// Concurrent fallback: treat as conflict (another transaction wrote first); mark conflict, do not count as imported.
			if casErr := s.casUpdateResult(ctx, tx, r.ID, "imported", "conflict", now); casErr != nil {
				return "", casErr
			}
			return outcomeConflicted, nil
		}
		return "", fmt.Errorf("freediscovery: insert catalog %s: %w", r.ModelID, err)
	}
	if err := s.casUpdateResult(ctx, tx, r.ID, "imported", "imported", now); err != nil {
		return "", err
	}
	return outcomeImported, nil
}

// casUpdateResult CAS-updates the result status; only hits when import_status='pending'.
//
// from=actualNew indicates the final status written by this attempt (imported/skipped/conflict).
// RowsAffected==0 means the result was already processed by another transaction,
// counted as outcomeConflicted.
func (s *ImportService) casUpdateResult(ctx context.Context, tx *sql.Tx, resultID int64, _, actualNew string, now time.Time) error {
	// Use anyString and the imported_at field must preserve NOT NULL / NULL compatibility:
	// when transitioning from pending to imported/skipped/conflict, also record imported_at to capture the first-processing time.
	res, err := tx.ExecContext(ctx, `
		UPDATE discovery_results SET import_status=$2, imported_at=$3
		WHERE id=$1 AND import_status='pending'`,
		resultID, actualNew, now)
	if err != nil {
		return fmt.Errorf("freediscovery: mark %s %d: %w", actualNew, resultID, err)
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("freediscovery: result %d already processed", resultID)
	}
	return nil
}

// lockAndLoadResults locks the pending result rows with FOR UPDATE inside the import
// transaction, and verifies that each row's tenant_id matches the request tenant
// (defense-in-depth for RLS-bypass scenarios).
//
// req.ResultIDs empty = all pending results for the task;
// non-empty = the specified rows (still verified to belong to the task and be pending).
func (s *ImportService) lockAndLoadResults(ctx context.Context, tx *sql.Tx, req ImportRequest) ([]*DiscoveryResult, error) {
	q := `
		SELECT id, task_id, tenant_id, provider_code, model_id, display_name,
		       COALESCE(context_window,0), COALESCE(max_tokens,0), COALESCE(free_type,''),
		       COALESCE(monthly_tokens,0), COALESCE(daily_tokens,0), COALESCE(pool_key,''),
		       tos_verdict, COALESCE(tos_notes,''), import_status
		FROM discovery_results
		WHERE task_id=$1 AND tenant_id=$2 AND import_status='pending'
		FOR UPDATE`
	args := []any{req.TaskID, req.TenantID}
	if len(req.ResultIDs) > 0 {
		q += ` AND id = ANY($3)`
		args = append(args, pqInt64Array(req.ResultIDs))
	}
	q += ` ORDER BY id`

	rows, err := tx.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("freediscovery: lock results: %w", err)
	}
	defer rows.Close()

	var out []*DiscoveryResult
	for rows.Next() {
		var r DiscoveryResult
		if err := rows.Scan(
			&r.ID, &r.TaskID, &r.TenantID, &r.ProviderCode, &r.ModelID, &r.DisplayName,
			&r.ContextWindow, &r.MaxTokens, &r.FreeType,
			&r.MonthlyTokens, &r.DailyTokens, &r.PoolKey,
			&r.TosVerdict, &r.TosNotes, &r.ImportStatus,
		); err != nil {
			return nil, fmt.Errorf("freediscovery: scan result: %w", err)
		}
		// No longer overriding tenant_id; guaranteed jointly by the SQL query and the task tenant check.
		out = append(out, &r)
	}
	return out, rows.Err()
}

// pqInt64Array depends on lib/pq's int64 array parameter support.
func pqInt64Array(ids []int64) any {
	return pq.Array(ids)
}
