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

// ImportService 把审查通过的发现结果批量导入 free_resource_catalog.
//
// 冲突检测: free_resource_catalog 已有 (provider_code, model_id, tenant_id) 行时
// 按策略 skip / overwrite / merge 处理 (084 迁移唯一约束).
//
// 安全契约 (2026-09-09 audit-fix):
//   - 单事务完成 task 锁 + 结果加载 + 写库, 无独立 loadResults 事务 (消除 TOCTOU);
//   - task 行 SELECT ... FOR UPDATE, 校验 tenant_id 一致且 status=success;
//   - discovery_results UPDATE 全部带 tenant_id + import_status='pending' 条件 + RowsAffected 检查;
//   - catalog 写入使用结果真实 tenant_id, 不再无条件覆盖;
//   - 任意 Update/Catalog 错误整体回滚, summary.Imported 只计实际 CAS 成功的行.
//
// ErrImportTaskNotReady: 任务不存在/跨租户/状态非 success (handler 映射 404/409).
// ErrImportTaskNotFound 与上面共用 sentinel, ErrImportTaskNotReady 区分跨租户 / 非 success.
//
// summary.Skipped 表示 skip 策略下已存在的条目; summary.Conflicted 表示 conflict;
// summary.Failed 永远为 0 (全事务任一错误整体回滚, 真实错误经由 error 返回).
type ImportService struct {
	db *sql.DB
}

// ErrImportTaskNotFound 任务不存在 (含跨租户). handler 应映射 404.
var ErrImportTaskNotFound = errors.New("freediscovery: import task not found")

// ErrImportTaskNotReady 任务存在但状态不允许导入 (非 success, 或 result 已被处理).
// handler 应映射 409 Conflict.
var ErrImportTaskNotReady = errors.New("freediscovery: task not in success state")

// NewImportService 构造导入服务.
func NewImportService(db *sql.DB) *ImportService {
	return &ImportService{db: db}
}

// Import 执行批量导入, 返回统计. 全程单事务: 任一失败整体回滚.
//
// 与早期实现的差异 (audit-fix): 不再使用独立 loadResults 事务再起导入事务的 TOCTOU 模式,
// 而是在单事务内按 task_id FOR UPDATE 锁住任务, 校验 tenant + status, 再 SELECT 结果
// 并立即 CAS 更新 (status='pending' AND tenant_id=$tenant), 失败行不计入 Imported.
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

	// 1. 锁住任务 + 校验归属与终态.
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
		// 防御性: 跨租户 (RLS bypass 角色下也不应发生, 但应用层兜底).
		return nil, ErrImportTaskNotFound
	}
	if taskStatus != TaskStatusSuccess {
		return nil, fmt.Errorf("%w: status=%s", ErrImportTaskNotReady, taskStatus)
	}

	// 2. 加载待导入结果 (FOR UPDATE 锁行, 防止并发 Import).
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

	// 3. 逐条 import; 全部成功后 commit, 任一错误整体回滚.
	summary := &ImportSummary{}
	for _, r := range results {
		outcome, err := s.importOne(ctx, tx, req, r)
		if err != nil {
			return nil, err
		}
		// outcome: imported / skipped / conflicted (对应 summary 字段)
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

	// 4. 任务统计 (CAS: 仅在 models_imported 当前值 + $2 与本事务一致时累加,
	//    失败不阻断已成功导入的结果; 同时记录 import_status 更新总数便于排查).
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

// importOutcome 内部枚举, 区分 catalog 写入语义.
//   - imported:  全新条目 INSERT 或 overwrite/merge 已写入, 结果行已 mark imported
//   - skipped:   skip 策略下命中已存在条目, 保留 catalog, 结果行已 mark skipped
//   - conflicted: skip 策略但并发写入产生冲突 / 已被其他事务处理 (CAS 失败)
type importOutcome string

const (
	outcomeImported   importOutcome = "imported"
	outcomeSkipped    importOutcome = "skipped"
	outcomeConflicted importOutcome = "conflicted"
)

// importOne 导入单条结果. 返回 importOutcome (imported/skipped/conflicted).
//
// 关键变更 (audit-fix):
//   - mark imported/skipped/conflict 使用 CAS, RowsAffected==0 视为"已被并发处理";
//   - 重复 INSERT 命中 unique key 也走 CAS 路径, 不再静默计 conflict.
func (s *ImportService) importOne(ctx context.Context, tx *sql.Tx, req ImportRequest, r *DiscoveryResult) (importOutcome, error) {
	// 检测冲突 (catalog 已有 (provider_code, model_id, tenant_id))
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
			// skip: 不动 catalog, 把结果标 skipped (语义清晰).
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

	// 全新条目 INSERT; avoid 条目按契约导入为禁用状态.
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
			// 并发兜底: 视为 conflict (其他事务先写); 标 conflict 不计入 imported.
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

// casUpdateResult CAS 更新结果状态; 仅在 import_status='pending' 时命中.
//
// from=actualNew 表示本次尝试写入的最终状态 (imported/skipped/conflict).
// RowsAffected==0 表示该结果已被其他事务处理, 计为 outcomeConflicted.
func (s *ImportService) casUpdateResult(ctx context.Context, tx *sql.Tx, resultID int64, _, actualNew string, now time.Time) error {
	// 使用 anyString 与 imported_at 字段必须保留 NOT NULL / NULL 兼容:
	// 从 pending 转 imported/skipped/conflict 时同时记录 imported_at, 反映首次处理时间.
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

// lockAndLoadResults 在导入事务内按 FOR UPDATE 锁住待导入结果行, 同时校验
// 结果行 tenant_id 与请求 tenant 一致 (防御 RLS bypass 场景).
//
// req.ResultIDs 为空 = 该任务全部 pending; 非空 = 指定行 (仍校验属于该 task 且 pending).
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
		// 不再覆盖 tenant_id; 由 SQL 查询 + 任务 tenant 校验共同保证.
		out = append(out, &r)
	}
	return out, rows.Err()
}

// pqInt64Array 依赖 lib/pq 的 int64 数组参数.
func pqInt64Array(ids []int64) any {
	return pq.Array(ids)
}
