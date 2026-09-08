package freediscovery

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"

	"github.com/lib/pq"
)

// ImportService 把审查通过的发现结果批量导入 free_resource_catalog.
//
// 冲突检测: free_resource_catalog 已有 (provider_code, model_id, tenant_id) 行时
// 按策略 skip / overwrite / merge 处理 (084 迁移唯一约束).
type ImportService struct {
	db *sql.DB
}

// NewImportService 构造导入服务.
func NewImportService(db *sql.DB) *ImportService {
	return &ImportService{db: db}
}

// Import 执行批量导入, 返回统计. 全程单事务: 任一失败整体回滚.
func (s *ImportService) Import(ctx context.Context, req ImportRequest) (*ImportSummary, error) {
	if req.ConflictPolicy == "" {
		req.ConflictPolicy = ConflictSkip
	}
	switch req.ConflictPolicy {
	case ConflictSkip, ConflictOverwrite, ConflictMerge:
	default:
		return nil, fmt.Errorf("freediscovery: invalid conflict_policy %q", req.ConflictPolicy)
	}

	// 1. 加载目标结果行 (RLS 事务内)
	results, err := s.loadResults(ctx, req)
	if err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return &ImportSummary{}, nil
	}

	// 2. 单事务导入
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("freediscovery: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := setTenantTx(ctx, tx, req.TenantID); err != nil {
		return nil, err
	}

	summary := &ImportSummary{}
	for _, r := range results {
		conflict, err := s.importOne(ctx, tx, req, r)
		if err != nil {
			return nil, err
		}
		switch {
		case conflict:
			summary.Conflicted++
			if _, err := tx.ExecContext(ctx,
				`UPDATE discovery_results SET import_status='conflict' WHERE id=$1`, r.ID); err != nil {
				return nil, fmt.Errorf("freediscovery: mark conflict %d: %w", r.ID, err)
			}
		default:
			summary.Imported++
		}
	}

	// 3. 更新任务统计
	if _, err := tx.ExecContext(ctx, `
		UPDATE discovery_tasks SET models_imported = models_imported + $2
		WHERE id=$1`, req.TaskID, summary.Imported); err != nil {
		return nil, fmt.Errorf("freediscovery: update task counters: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("freediscovery: commit import: %w", err)
	}

	slog.Info("freediscovery: import completed",
		"tenant_id", req.TenantID, "task_id", req.TaskID,
		"imported", summary.Imported, "conflicted", summary.Conflicted,
		"policy", string(req.ConflictPolicy), "by", req.ImportedBy)
	return summary, nil
}

// importOne 导入单条结果. 返回 conflict=true 表示命中已有条目且策略不覆盖.
func (s *ImportService) importOne(ctx context.Context, tx *sql.Tx, req ImportRequest, r *DiscoveryResult) (bool, error) {
	// 检测冲突
	var existingID int64
	var existingEnabled bool
	err := tx.QueryRowContext(ctx, `
		SELECT id, enabled FROM free_resource_catalog
		WHERE provider_code=$1 AND model_id=$2 AND tenant_id=$3`,
		r.ProviderCode, r.ModelID, req.TenantID).Scan(&existingID, &existingEnabled)
	exists := err == nil
	if err != nil && err != sql.ErrNoRows {
		return false, fmt.Errorf("freediscovery: conflict probe %s: %w", r.ModelID, err)
	}

	now := timeNow().UTC()
	markImported := func() error {
		if _, err := tx.ExecContext(ctx,
			`UPDATE discovery_results SET import_status='imported', imported_at=$2 WHERE id=$1`,
			r.ID, now); err != nil {
			return fmt.Errorf("freediscovery: mark imported %d: %w", r.ID, err)
		}
		return nil
	}
	if exists {
		switch req.ConflictPolicy {
		case ConflictSkip:
			return true, nil // 保留现有条目
		case ConflictOverwrite:
			_, err = tx.ExecContext(ctx, `
				UPDATE free_resource_catalog SET
					display_name=$2, free_type=$3, monthly_tokens=$4, daily_tokens=$5,
					pool_key=$6, tos_verdict=$7, tos_notes=$8,
					source_type='discovered', discovery_task_id=$9,
					last_synced_at=$10, enabled=TRUE, disabled_at=NULL, disabled_reason=NULL
				WHERE id=$1`,
				existingID, r.DisplayName, r.FreeType, r.MonthlyTokens, r.DailyTokens,
				r.PoolKey, r.TosVerdict, r.TosNotes,
				req.TaskID, now)
			if err != nil {
				return false, fmt.Errorf("freediscovery: overwrite %s: %w", r.ModelID, err)
			}
			return false, markImported()
		case ConflictMerge:
			// 仅补充空字段: 现有非空值优先
			_, err = tx.ExecContext(ctx, `
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
				WHERE id=$1`,
				existingID, r.DisplayName, r.FreeType, r.MonthlyTokens, r.DailyTokens,
				r.PoolKey, r.TosVerdict, r.TosNotes,
				req.TaskID, now)
			if err != nil {
				return false, fmt.Errorf("freediscovery: merge %s: %w", r.ModelID, err)
			}
			return false, markImported()
		}
	}

	// 全新条目 INSERT; avoid 条目按现有契约导入为禁用状态
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
		req.TaskID, now, enabled, disabledAt, disabledReason, req.TenantID)
	if err != nil {
		if strings.Contains(err.Error(), "duplicate key") {
			// 并发导入竞态兜底
			return true, nil
		}
		return false, fmt.Errorf("freediscovery: insert catalog %s: %w", r.ModelID, err)
	}
	return false, markImported()
}

// loadResults 在 RLS 事务内加载待导入结果.
// req.ResultIDs 为空 = 该任务全部 pending; 非空 = 指定行 (仍校验 pending).
func (s *ImportService) loadResults(ctx context.Context, req ImportRequest) ([]*DiscoveryResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("freediscovery: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := setTenantTx(ctx, tx, req.TenantID); err != nil {
		return nil, err
	}

	q := `
		SELECT id, task_id, provider_code, model_id, display_name,
		       COALESCE(context_window,0), COALESCE(max_tokens,0), COALESCE(free_type,''),
		       COALESCE(monthly_tokens,0), COALESCE(daily_tokens,0), COALESCE(pool_key,''),
		       tos_verdict, COALESCE(tos_notes,''), import_status
		FROM discovery_results WHERE task_id=$1 AND import_status='pending'`
	args := []any{req.TaskID}
	if len(req.ResultIDs) > 0 {
		q += ` AND id = ANY($2)`
		args = append(args, pqInt64Array(req.ResultIDs))
	}
	q += ` ORDER BY id`

	rows, err := tx.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("freediscovery: load results: %w", err)
	}
	defer rows.Close()

	var out []*DiscoveryResult
	for rows.Next() {
		var r DiscoveryResult
		if err := rows.Scan(
			&r.ID, &r.TaskID, &r.ProviderCode, &r.ModelID, &r.DisplayName,
			&r.ContextWindow, &r.MaxTokens, &r.FreeType,
			&r.MonthlyTokens, &r.DailyTokens, &r.PoolKey,
			&r.TosVerdict, &r.TosNotes, &r.ImportStatus,
		); err != nil {
			return nil, fmt.Errorf("freediscovery: scan result: %w", err)
		}
		r.TenantID = req.TenantID
		out = append(out, &r)
	}
	return out, rows.Err()
}

// pqInt64Array 依赖 lib/pq 的 int64 数组参数.
func pqInt64Array(ids []int64) any {
	return pq.Array(ids)
}
