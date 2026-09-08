package freediscovery

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"
)

// DiscoveryEngine 发现引擎: 加载模板 → 解析密钥 → 扫描上游 → ToS 初判 →
// 结果落库 (discovery_results, 状态 pending 待人工审查).
//
// 失败快路径: 任何一步失败, 任务标记 failed 并携带错误信息; 不做静默回退.
type DiscoveryEngine struct {
	db        *sql.DB
	templates *TemplateManager
	// providerScanners 按提供商装配 (预设钩子: FreeOf/PoolKey/Quota)
	providerScanners map[string]ProviderScanner
	// fallbackScanners 按协议兜底 (未注册预设的提供商)
	fallbackScanners map[APIType]ProviderScanner
	tos              *ToSChecker
}

// NewDiscoveryEngine 构造引擎.
func NewDiscoveryEngine(db *sql.DB, templates *TemplateManager) *DiscoveryEngine {
	e := &DiscoveryEngine{
		db:               db,
		templates:        templates,
		providerScanners: make(map[string]ProviderScanner),
		fallbackScanners: make(map[APIType]ProviderScanner),
		tos:              NewToSChecker(),
	}

	// 按预设装配提供商扫描器 (FreeOf/PoolKey/QuotaEstimator 钩子)
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

	// 协议兜底: google/anthropic 的模型发现 MVP 阶段复用 openai 形态扫描
	// (真协议适配后在此替换 scanner)
	e.fallbackScanners[APITypeOpenAICompletions] = NewHTTPScanner(nil)
	e.fallbackScanners[APITypeGoogleGenerativeAI] = NewHTTPScanner(nil)
	e.fallbackScanners[APITypeAnthropic] = NewHTTPScanner(nil)
	return e
}

// SetScanner 注入/覆盖指定协议的兜底扫描器 (测试与后续真协议适配入口).
func (e *DiscoveryEngine) SetScanner(t APIType, s ProviderScanner) {
	e.fallbackScanners[t] = s
}

// SetProviderScanner 注入/覆盖指定提供商的扫描器 (测试入口).
func (e *DiscoveryEngine) SetProviderScanner(code string, s ProviderScanner) {
	e.providerScanners[code] = s
}

// scannerFor 解析模板应使用的扫描器: 提供商装配优先, 协议兜底次之.
func (e *DiscoveryEngine) scannerFor(tpl *ProviderTemplate) ProviderScanner {
	if s, ok := e.providerScanners[tpl.ProviderCode]; ok && s != nil {
		return s
	}
	return e.fallbackScanners[tpl.APIType]
}

// Run 执行一次发现任务. 返回任务终态.
func (e *DiscoveryEngine) Run(ctx context.Context, req DiscoveryRequest) (*DiscoveryTask, error) {
	if req.TriggerType == "" {
		req.TriggerType = TriggerManual
	}

	// 1. 创建任务 (pending)
	task, err := e.createTask(ctx, req)
	if err != nil {
		return nil, err
	}

	// 2. 标记 running
	if err := e.updateTask(ctx, task, TaskStatusRunning, nil, nil); err != nil {
		return nil, err
	}

	// 3. 加载模板 + 解析密钥
	tpl, err := e.templates.Get(ctx, req.TenantID, req.TemplateID)
	if err != nil {
		return e.fail(ctx, task, fmt.Errorf("load template: %w", err))
	}
	apiKey, _, err := e.templates.ResolveAPIKey(ctx, tpl)
	if err != nil {
		return e.fail(ctx, task, fmt.Errorf("resolve api key: %w", err))
	}

	// 4. 选择扫描器 (提供商装配优先, 协议兜底)
	scanner := e.scannerFor(tpl)
	if scanner == nil {
		return e.fail(ctx, task, fmt.Errorf("no scanner for provider %q (api_type %q)", tpl.ProviderCode, tpl.APIType))
	}

	// 5. 扫描
	models, err := scanner.ScanModels(ctx, tpl, apiKey)
	if err != nil {
		return e.fail(ctx, task, err)
	}

	// 6. ToS 初判 (共享池/配额钩子已在 scanner 装配层生效)
	for i := range models {
		models[i].TosVerdict, models[i].TosNotes = e.tos.Check(tpl, models[i].ModelID)
	}

	// 7. 结果落库 (pending)
	if err := e.saveResults(ctx, task, models); err != nil {
		return e.fail(ctx, task, err)
	}

	// 8. 任务完成
	found := len(models)
	if err := e.updateTask(ctx, task, TaskStatusSuccess, &found, nil); err != nil {
		return nil, err
	}

	slog.Info("freediscovery: task completed",
		"tenant_id", req.TenantID, "task_id", task.ID,
		"provider_code", tpl.ProviderCode, "models_found", found)

	return e.GetTask(ctx, req.TenantID, task.ID)
}

// createTask 写入 pending 任务行.
func (e *DiscoveryEngine) createTask(ctx context.Context, req DiscoveryRequest) (*DiscoveryTask, error) {
	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("freediscovery: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := setTenantTx(ctx, tx, req.TenantID); err != nil {
		return nil, err
	}

	// 冗余 provider_code: 从模板读取, 任务留痕
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
	now := timeNow().UTC()
	err = tx.QueryRowContext(ctx, `
		INSERT INTO discovery_tasks (
			tenant_id, template_id, provider_code, status, trigger_type, triggered_by, started_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7)
		RETURNING id`,
		req.TenantID, req.TemplateID, providerCode, string(TaskStatusPending),
		string(req.TriggerType), nullableStr(req.TriggeredBy), now,
	).Scan(&id)
	if err != nil {
		return nil, fmt.Errorf("freediscovery: insert task: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("freediscovery: commit task: %w", err)
	}

	started := now
	return &DiscoveryTask{
		ID: id, TenantID: req.TenantID, TemplateID: &req.TemplateID,
		ProviderCode: providerCode, Status: TaskStatusPending,
		TriggerType: req.TriggerType, TriggeredBy: req.TriggeredBy,
		StartedAt: &started,
	}, nil
}

// updateTask 更新任务状态与统计.
func (e *DiscoveryEngine) updateTask(ctx context.Context, task *DiscoveryTask, status TaskStatus, modelsFound, modelsImported *int) error {
	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("freediscovery: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := setTenantTx(ctx, tx, task.TenantID); err != nil {
		return err
	}

	var completedAt any
	if status == TaskStatusSuccess || status == TaskStatusFailed {
		completedAt = timeNow().UTC()
	}

	_, err = tx.ExecContext(ctx, `
		UPDATE discovery_tasks SET status=$2,
			models_found=COALESCE($3, models_found),
			models_imported=COALESCE($4, models_imported),
			completed_at=$5
		WHERE id=$1`,
		task.ID, string(status), modelsFound, modelsImported, completedAt)
	if err != nil {
		return fmt.Errorf("freediscovery: update task %d: %w", task.ID, err)
	}
	return tx.Commit()
}

// fail 标记任务失败并返回 (任务体, 原错误). 任务体供 handler 在
// 扫描失败时仍返回 200 + failed 任务详情 (docs §3.4 契约); 不吞错.
func (e *DiscoveryEngine) fail(ctx context.Context, task *DiscoveryTask, cause error) (*DiscoveryTask, error) {
	task.Status = TaskStatusFailed
	task.ErrorMessage = cause.Error()

	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return task, cause // DB 不可用时优先暴露原始错误
	}
	defer func() { _ = tx.Rollback() }()

	if err := setTenantTx(ctx, tx, task.TenantID); err != nil {
		return task, cause
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE discovery_tasks SET status=$2, error_message=$3, completed_at=$4 WHERE id=$1`,
		task.ID, string(TaskStatusFailed), task.ErrorMessage, timeNow().UTC()); err != nil {
		slog.Error("freediscovery: failed to mark task failed",
			"task_id", task.ID, "error", err)
		return task, cause
	}
	if err := tx.Commit(); err != nil {
		slog.Error("freediscovery: failed to commit failure mark", "task_id", task.ID, "error", err)
		return task, cause
	}
	slog.Warn("freediscovery: task failed",
		"tenant_id", task.TenantID, "task_id", task.ID, "error", cause.Error())
	return task, cause
}

// saveResults 批量写入发现结果 (单事务; ON CONFLICT 跳过重复 model_id).
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
		-- import_status / imported_at 保持原值: 重复扫描不重置已审查状态`)
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

// GetTask 读取任务 (RLS 过滤).
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

// ListTasks 列出租户任务 (按创建时间倒序, limit 上限 200).
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

// ListResults 列出任务的发现结果, 可按 import_status 过滤 ("all" = 不过滤).
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
