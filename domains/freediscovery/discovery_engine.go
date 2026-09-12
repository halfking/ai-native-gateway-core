package freediscovery

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/kaixuan/llm-gateway-go/metrics"
)

// DiscoveryEngine: loads template -> resolves API key -> scans upstream -> runs ToS
// initial check -> writes results (discovery_results, status pending for human review).
//
// Fail-fast: any step that fails marks the task failed with the error; no silent rollback.
type DiscoveryEngine struct {
	db        *sql.DB
	templates *TemplateManager
	// providerScanners are wired per provider (preset hooks: FreeOf/PoolKey/Quota).
	providerScanners map[string]ProviderScanner
	// fallbackScanners are wired per protocol (for providers without a preset).
	fallbackScanners map[APIType]ProviderScanner
	tos              *ToSChecker
}

// NewDiscoveryEngine constructs the engine.
func NewDiscoveryEngine(db *sql.DB, templates *TemplateManager) *DiscoveryEngine {
	e := &DiscoveryEngine{
		db:               db,
		templates:        templates,
		providerScanners: make(map[string]ProviderScanner),
		fallbackScanners: make(map[APIType]ProviderScanner),
		tos:              NewToSChecker(),
	}

	// Wire provider scanners from presets (FreeOf/PoolKey/QuotaEstimator hooks).
	for code, p := range builtinPresets {
		hs := NewHTTPScanner(nil)
		if p.FreeOf != nil {
			hs.freeOf = p.FreeOf
		}
		if p.PoolKeyOf != nil {
			hs.poolKeyOf = func(m modelEntry) string { return p.PoolKeyOf(m.ID) }
		}
		if p.QuotaEstimator != nil {
			hs.quotaEstimator = func(m modelEntry) (int64, int64) { return p.QuotaEstimator(m.ID) }
		}
		e.providerScanners[code] = hs
	}

	// Protocol fallbacks: google-ai-studio uses the real Gemini protocol (models[] shape);
	// the remaining protocols keep reusing the OpenAI shape (anthropic goes via the x-api-key adapter).
	e.fallbackScanners[APITypeOpenAICompletions] = NewHTTPScanner(nil)
	e.fallbackScanners[APITypeGoogleGenerativeAI] = NewGoogleGenerativeAIScanner(nil)
	e.fallbackScanners[APITypeAnthropic] = NewAnthropicScanner(nil)
	return e
}

// SetScanner injects/overrides the fallback scanner for a given protocol
// (entry point for tests and future real-protocol adapters).
func (e *DiscoveryEngine) SetScanner(t APIType, s ProviderScanner) {
	e.fallbackScanners[t] = s
}

// SetProviderScanner injects/overrides the scanner for a given provider (test entry point).
func (e *DiscoveryEngine) SetProviderScanner(code string, s ProviderScanner) {
	e.providerScanners[code] = s
}

// scannerFor resolves which scanner the template should use: provider wiring takes precedence,
// then the protocol fallback.
func (e *DiscoveryEngine) scannerFor(tpl *ProviderTemplate) ProviderScanner {
	if s, ok := e.providerScanners[tpl.ProviderCode]; ok && s != nil {
		return s
	}
	return e.fallbackScanners[tpl.APIType]
}

// Run executes one discovery task. Returns the terminal task state.
//
// State machine (2026-09-09 audit-fix):
//   - Run entry first validates the template exists and is enabled; otherwise
//     ErrTemplateNotFound / ErrTemplateDisabled.
//   - createTask writes pending (started_at NULL).
//   - updateTask(running) also sets started_at; must transition from pending and check RowsAffected.
//   - After scan success/failure, updateTask(success/failed); must transition from running and check RowsAffected.
//   - Any UPDATE mismatch returns ErrTaskStateConflict to prevent out-of-order updates.
func (e *DiscoveryEngine) Run(ctx context.Context, req DiscoveryRequest) (*DiscoveryTask, error) {
	if req.TriggerType == "" {
		req.TriggerType = TriggerManual
	}

	// 0. Pre-flight template validation: reject disabled templates (a disabled template may
	//    carry stale credentials, and triggering a scan would consume upstream API quota
	//    while writing a failed task; consistent with List(enabledOnly)).
	tpl, err := e.templates.Get(ctx, req.TenantID, req.TemplateID)
	if err != nil {
		if errors.Is(err, ErrTemplateNotFound) {
			metrics.FreeDiscoveryScansTotal.WithLabelValues("", "template_not_found").Inc()
		}
		return nil, err
	}
	if !tpl.Enabled {
		metrics.FreeDiscoveryScansTotal.WithLabelValues(tpl.ProviderCode, "template_disabled").Inc()
		return nil, ErrTemplateDisabled
	}

	// 1. Create the task (pending).
	task, err := e.createTask(ctx, req)
	if err != nil {
		return nil, err
	}

	// 2. Mark running (start time is written here; also check RowsAffected to prevent concurrent overwrites).
	if err := e.updateTask(ctx, task, TaskStatusRunning, TaskStatusPending, nil, nil); err != nil {
		return nil, err
	}
	now := timeNow().UTC()
	task.StartedAt = &now
	task.Status = TaskStatusRunning
	scanStart := timeNow()
	metrics.FreeDiscoveryActiveScans.Inc()

	// 3. Resolve the API key.
	apiKey, _, err := e.templates.ResolveAPIKey(ctx, tpl)
	if err != nil {
		return e.fail(ctx, task, TaskStatusRunning, fmt.Errorf("resolve api key: %w", err), scanStart)
	}

	// 4. Pick the scanner (provider wiring first, protocol fallback second).
	scanner := e.scannerFor(tpl)
	if scanner == nil {
		return e.fail(ctx, task, TaskStatusRunning, fmt.Errorf("no scanner for provider %q (api_type %q)", tpl.ProviderCode, tpl.APIType), scanStart)
	}

	// 5. Scan.
	models, err := scanner.ScanModels(ctx, tpl, apiKey)
	if err != nil {
		return e.fail(ctx, task, TaskStatusRunning, err, scanStart)
	}

	// 6. ToS initial check (shared-pool/quota hooks already applied at scanner wiring).
	for i := range models {
		models[i].TosVerdict, models[i].TosNotes = e.tos.Check(tpl, models[i].ModelID)
		if models[i].TosVerdict == "avoid" || models[i].TosVerdict == "caution" {
			metrics.FreeDiscoveryTosViolationsTotal.WithLabelValues(tpl.ProviderCode, models[i].TosVerdict).Inc()
		}
	}

	// 7. Persist results (pending).
	if err := e.saveResults(ctx, task, models); err != nil {
		return e.fail(ctx, task, TaskStatusRunning, err, scanStart)
	}

	// 8. Complete the task.
	found := len(models)
	if err := e.updateTask(ctx, task, TaskStatusSuccess, TaskStatusRunning, &found, nil); err != nil {
		metrics.FreeDiscoveryActiveScans.Dec()
		return nil, err
	}

	metrics.FreeDiscoveryActiveScans.Dec()
	metrics.FreeDiscoveryScansTotal.WithLabelValues(tpl.ProviderCode, "success").Inc()
	metrics.FreeDiscoveryScanDurationSeconds.Observe(timeSince(scanStart).Seconds())
	metrics.FreeDiscoveryModelsDiscoveredTotal.Observe(float64(found))
	metrics.FreeDiscoveryResourcesDiscoveredTotal.Add(float64(found))

	slog.Info("freediscovery: task completed",
		"tenant_id", req.TenantID, "task_id", task.ID,
		"provider_code", tpl.ProviderCode, "models_found", found)

	return e.GetTask(ctx, req.TenantID, task.ID)
}

// failScanMetrics records observability metrics for the scan-failure path (terminal status + duration).
func failScanMetrics(provider string, cause error, scanStart time.Time) {
	metrics.FreeDiscoveryActiveScans.Dec()
	metrics.FreeDiscoveryScansTotal.WithLabelValues(provider, "failed").Inc()
	if !scanStart.IsZero() {
		metrics.FreeDiscoveryScanDurationSeconds.Observe(timeSince(scanStart).Seconds())
	}
}

// createTask writes the pending task row. started_at stays NULL and is written by the running transition.
func (e *DiscoveryEngine) createTask(ctx context.Context, req DiscoveryRequest) (*DiscoveryTask, error) {
	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("freediscovery: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := setTenantTx(ctx, tx, req.TenantID); err != nil {
		return nil, err
	}

	// provider_code is read from the template and stored on the task for traceability
	// (RLS visibility within the same transaction).
	var providerCode string
	if err := tx.QueryRowContext(ctx,
		`SELECT provider_code FROM provider_templates WHERE id=$1`, req.TemplateID,
	).Scan(&providerCode); err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("freediscovery: template %d not found (tenant %q)", req.TemplateID, req.TenantID)
		}
		return nil, fmt.Errorf("freediscovery: lookup template: %w", err)
	}

	var id int64
	err = tx.QueryRowContext(ctx, `
		INSERT INTO discovery_tasks (
			tenant_id, template_id, provider_code, status, trigger_type, triggered_by
		) VALUES ($1,$2,$3,$4,$5,$6)
		RETURNING id`,
		req.TenantID, req.TemplateID, providerCode, string(TaskStatusPending),
		string(req.TriggerType), nullableStr(req.TriggeredBy),
	).Scan(&id)
	if err != nil {
		return nil, fmt.Errorf("freediscovery: insert task: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("freediscovery: commit task: %w", err)
	}

	return &DiscoveryTask{
		ID: id, TenantID: req.TenantID, TemplateID: &req.TemplateID,
		ProviderCode: providerCode, Status: TaskStatusPending,
		TriggerType: req.TriggerType, TriggeredBy: req.TriggeredBy,
	}, nil
}

// updateTask updates the task status and counters; expectedFrom is the state-machine precondition,
// and 0 rows returns ErrTaskStateConflict. The running state also writes started_at.
func (e *DiscoveryEngine) updateTask(
	ctx context.Context, task *DiscoveryTask, status TaskStatus,
	expectedFrom TaskStatus, modelsFound, modelsImported *int,
) error {
	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("freediscovery: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := setTenantTx(ctx, tx, task.TenantID); err != nil {
		return err
	}

	var completedAt, startedAt any
	if status == TaskStatusSuccess || status == TaskStatusFailed {
		completedAt = timeNow().UTC()
	}
	if status == TaskStatusRunning {
		startedAt = completedAt
		// Do not force a completed_at write while running (avoids overwriting).
		completedAt = nil
	}

	res, err := tx.ExecContext(ctx, `
		UPDATE discovery_tasks SET status=$2,
			models_found=COALESCE($3, models_found),
			models_imported=COALESCE($4, models_imported),
			started_at=COALESCE($5, started_at),
			completed_at=COALESCE($6, completed_at)
		WHERE id=$1 AND tenant_id=$7 AND status=$8`,
		task.ID, string(status), modelsFound, modelsImported, startedAt, completedAt,
		task.TenantID, string(expectedFrom))
	if err != nil {
		return fmt.Errorf("freediscovery: update task %d: %w", task.ID, err)
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("freediscovery: update task %d: %w", task.ID, ErrTaskStateConflict)
	}
	return tx.Commit()
}

// fail marks the task as failed and returns (task body, original error). The task body lets the
// handler still return 200 + failed task details on scan failure (docs section 3.4 contract); the
// error is not swallowed.
//
// Only the expectedFrom state (typically running) may transition; 0 rows indicates a broken state
// machine. The returned error still wraps cause so the handler can pass it through; the task body
// remains in the failed state for the UI. When scanStart is non-zero, scan duration and
// terminal-status metrics are recorded at the same time.
func (e *DiscoveryEngine) fail(ctx context.Context, task *DiscoveryTask, expectedFrom TaskStatus, cause error, scanStart ...time.Time) (*DiscoveryTask, error) {
	failScanMetrics(task.ProviderCode, cause, firstTime(scanStart))
	task.Status = TaskStatusFailed
	task.ErrorMessage = cause.Error()

	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return task, cause // Surface the original error when the DB is unavailable.
	}
	defer func() { _ = tx.Rollback() }()

	if err := setTenantTx(ctx, tx, task.TenantID); err != nil {
		return task, cause
	}
	res, err := tx.ExecContext(ctx, `
		UPDATE discovery_tasks SET status=$2, error_message=$3, completed_at=$4
		WHERE id=$1 AND tenant_id=$5 AND status=$6`,
		task.ID, string(TaskStatusFailed), task.ErrorMessage, timeNow().UTC(),
		task.TenantID, string(expectedFrom))
	if err != nil {
		slog.Error("freediscovery: failed to mark task failed",
			"task_id", task.ID, "error", err)
		return task, cause
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		// State-machine conflict: surface the concurrency issue to the caller, but keep the task body
		// marked failed (handler's decision).
		slog.Warn("freediscovery: fail transition rejected by state guard",
			"task_id", task.ID, "expected_from", string(expectedFrom))
		return task, fmt.Errorf("%w: %v", ErrTaskStateConflict, cause)
	}
	if err := tx.Commit(); err != nil {
		slog.Error("freediscovery: failed to commit failure mark", "task_id", task.ID, "error", err)
		return task, cause
	}
	slog.Warn("freediscovery: task failed",
		"tenant_id", task.TenantID, "task_id", task.ID, "error", cause.Error())
	return task, cause
}

// saveResults batch-writes discovery results (single transaction; ON CONFLICT skips duplicate model_id).
func (e *DiscoveryEngine) saveResults(ctx context.Context, task *DiscoveryTask, models []DiscoveredModel) error {
	if len(models) == 0 {
		return nil
	}
	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("freediscovery: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := setTenantTx(ctx, tx, task.TenantID); err != nil {
		return err
	}

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO discovery_results (
			tenant_id, task_id, provider_code, model_id, display_name,
			context_window, max_tokens, free_type, monthly_tokens, daily_tokens, pool_key,
			tos_verdict, tos_notes, import_status, raw_metadata
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,'pending',$14)
		ON CONFLICT (task_id, model_id) DO UPDATE SET
			display_name=EXCLUDED.display_name,
			context_window=EXCLUDED.context_window,
			max_tokens=EXCLUDED.max_tokens,
			free_type=EXCLUDED.free_type,
			monthly_tokens=EXCLUDED.monthly_tokens,
			daily_tokens=EXCLUDED.daily_tokens,
			pool_key=EXCLUDED.pool_key,
			tos_verdict=EXCLUDED.tos_verdict,
			tos_notes=EXCLUDED.tos_notes,
			raw_metadata=EXCLUDED.raw_metadata
		-- import_status / imported_at preserve their existing values: repeated scans do not reset the reviewed status.`)
	if err != nil {
		return fmt.Errorf("freediscovery: prepare result insert: %w", err)
	}
	defer stmt.Close()

	for _, m := range models {
		raw, err := json.Marshal(m.RawMetadata)
		if err != nil {
			raw = []byte("{}")
		}
		if _, err := stmt.ExecContext(ctx,
			task.TenantID, task.ID, m.ProviderCode, m.ModelID, m.DisplayName,
			m.ContextWindow, m.MaxTokens, m.FreeType, m.MonthlyTokens, m.DailyTokens, m.PoolKey,
			m.TosVerdict, m.TosNotes, raw,
		); err != nil {
			return fmt.Errorf("freediscovery: insert result %s: %w", m.ModelID, err)
		}
	}
	return tx.Commit()
}

// GetTask reads a task (RLS-filtered).
func (e *DiscoveryEngine) GetTask(ctx context.Context, tenantID string, id int64) (*DiscoveryTask, error) {
	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("freediscovery: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := setTenantTx(ctx, tx, tenantID); err != nil {
		return nil, err
	}

	row := tx.QueryRowContext(ctx, `
		SELECT id, tenant_id, template_id, provider_code, status, trigger_type,
		       COALESCE(triggered_by,''), started_at, completed_at, COALESCE(error_message,''),
		       models_found, models_imported, created_at, updated_at
		FROM discovery_tasks WHERE id=$1`, id)

	var t DiscoveryTask
	var status, trigger string
	var templateID sql.NullInt64
	var startedAt, completedAt, createdAt, updatedAt sql.NullTime
	if err := row.Scan(
		&t.ID, &t.TenantID, &templateID, &t.ProviderCode, &status, &trigger,
		&t.TriggeredBy, &startedAt, &completedAt, &t.ErrorMessage,
		&t.ModelsFound, &t.ModelsImported, &createdAt, &updatedAt,
	); err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("freediscovery: task %d not found", id)
		}
		return nil, fmt.Errorf("freediscovery: scan task: %w", err)
	}
	t.Status = TaskStatus(status)
	t.TriggerType = TriggerType(trigger)
	if templateID.Valid {
		v := templateID.Int64
		t.TemplateID = &v
	}
	nullableTime(&t.StartedAt, startedAt)
	nullableTime(&t.CompletedAt, completedAt)
	if createdAt.Valid {
		t.CreatedAt = createdAt.Time
	}
	if updatedAt.Valid {
		t.UpdatedAt = updatedAt.Time
	}
	return &t, nil
}

// ListTasks lists the tenant's tasks (newest first; limit capped at 200).
func (e *DiscoveryEngine) ListTasks(ctx context.Context, tenantID string, limit int) ([]*DiscoveryTask, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("freediscovery: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := setTenantTx(ctx, tx, tenantID); err != nil {
		return nil, err
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT id, tenant_id, template_id, provider_code, status, trigger_type,
		       COALESCE(triggered_by,''), started_at, completed_at, COALESCE(error_message,''),
		       models_found, models_imported, created_at
		FROM discovery_tasks ORDER BY created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("freediscovery: list tasks: %w", err)
	}
	defer rows.Close()

	var out []*DiscoveryTask
	for rows.Next() {
		var t DiscoveryTask
		var status, trigger string
		var templateID sql.NullInt64
		var startedAt, completedAt, createdAt sql.NullTime
		if err := rows.Scan(
			&t.ID, &t.TenantID, &templateID, &t.ProviderCode, &status, &trigger,
			&t.TriggeredBy, &startedAt, &completedAt, &t.ErrorMessage,
			&t.ModelsFound, &t.ModelsImported, &createdAt,
		); err != nil {
			return nil, fmt.Errorf("freediscovery: scan task row: %w", err)
		}
		t.Status = TaskStatus(status)
		t.TriggerType = TriggerType(trigger)
		if templateID.Valid {
			v := templateID.Int64
			t.TemplateID = &v
		}
		nullableTime(&t.StartedAt, startedAt)
		nullableTime(&t.CompletedAt, completedAt)
		if createdAt.Valid {
			t.CreatedAt = createdAt.Time
		}
		out = append(out, &t)
	}
	return out, rows.Err()
}

// ListResults lists the results of a task, optionally filtered by import_status
// ("all" means no filter).
func (e *DiscoveryEngine) ListResults(ctx context.Context, tenantID string, taskID int64, importStatus string) ([]*DiscoveryResult, error) {
	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("freediscovery: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := setTenantTx(ctx, tx, tenantID); err != nil {
		return nil, err
	}

	q := `
		SELECT id, task_id, provider_code, model_id, display_name,
		       COALESCE(context_window,0), COALESCE(max_tokens,0), COALESCE(free_type,''),
		       COALESCE(monthly_tokens,0), COALESCE(daily_tokens,0), COALESCE(pool_key,''),
		       tos_verdict, COALESCE(tos_notes,''), import_status, imported_at, created_at
		FROM discovery_results WHERE task_id=$1`
	args := []any{taskID}
	if importStatus != "" && importStatus != "all" {
		q += ` AND import_status=$2`
		args = append(args, importStatus)
	}
	q += ` ORDER BY model_id`

	rows, err := tx.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("freediscovery: list results: %w", err)
	}
	defer rows.Close()

	var out []*DiscoveryResult
	for rows.Next() {
		var r DiscoveryResult
		var importedAt sql.NullTime
		if err := rows.Scan(
			&r.ID, &r.TaskID, &r.ProviderCode, &r.ModelID, &r.DisplayName,
			&r.ContextWindow, &r.MaxTokens, &r.FreeType,
			&r.MonthlyTokens, &r.DailyTokens, &r.PoolKey,
			&r.TosVerdict, &r.TosNotes, &r.ImportStatus, &importedAt, &r.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("freediscovery: scan result row: %w", err)
		}
		nullableTime(&r.ImportedAt, importedAt)
		r.TenantID = tenantID
		out = append(out, &r)
	}
	return out, rows.Err()
}

func nullableTime(dst **time.Time, v sql.NullTime) {
	if v.Valid {
		t := v.Time
		*dst = &t
	}
}

// timeNow/timeSince are independent clock entry points that tests can override.
var timeSince = time.Since

// firstTime returns the first time.Time in a variadic argument list; an empty call returns the zero value.
func firstTime(ts []time.Time) time.Time {
	if len(ts) == 0 {
		return time.Time{}
	}
	return ts[0]
}
