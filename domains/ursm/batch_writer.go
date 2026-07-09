package ursm

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/internal/runctx"
	"github.com/redis/go-redis/v9"
)

// BatchWriter 批量状态更新器，保证原子性和级联传播
type BatchWriter struct {
	db       *pgxpool.Pool
	redis    *redis.Client
	memCache *sync.Map

	// 缓存失效回调
	invalidateCallback func(ctx context.Context, updates []StateUpdate)
}

// NewBatchWriter 创建BatchWriter实例
func NewBatchWriter(
	db *pgxpool.Pool,
	redis *redis.Client,
	invalidateCallback func(context.Context, []StateUpdate),
) *BatchWriter {
	return &BatchWriter{
		db:                 db,
		redis:              redis,
		memCache:           &sync.Map{},
		invalidateCallback: invalidateCallback,
	}
}

// ApplyUpdates 原子应用多个状态更新
// 1. 开启事务
// 2. 按层级排序（Provider → Credential → Model → Node）
// 3. 逐层应用更新
// 4. 构建级联更新
// 5. 提交事务
// 6. 异步失效缓存
func (w *BatchWriter) ApplyUpdates(ctx context.Context, updates []StateUpdate) error {
	if len(updates) == 0 {
		return nil
	}

	// 1. 开启事务
	tx, err := w.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	// 2. 按层级排序
	sorted := sortUpdatesByLayer(updates)

	// 3. 构建级联更新
	cascaded, err := w.buildCascadeUpdates(ctx, tx, sorted)
	if err != nil {
		return fmt.Errorf("build cascade updates: %w", err)
	}

	// 合并原始更新和级联更新
	allUpdates := append(sorted, cascaded...)
	allUpdates = sortUpdatesByLayer(allUpdates)

	// 4. 逐层应用
	for _, update := range allUpdates {
		switch update.Layer {
		case LayerProvider:
			if err := w.applyProviderUpdate(ctx, tx, update); err != nil {
				return fmt.Errorf("apply provider update: %w", err)
			}
		case LayerCredential:
			if err := w.applyCredentialUpdate(ctx, tx, update); err != nil {
				return fmt.Errorf("apply credential update: %w", err)
			}
		case LayerModel:
			if err := w.applyModelUpdate(ctx, tx, update); err != nil {
				return fmt.Errorf("apply model update: %w", err)
			}
		case LayerNode:
			if err := w.applyNodeUpdate(ctx, tx, update); err != nil {
				return fmt.Errorf("apply node update: %w", err)
			}
		}
	}

	// 5. 提交事务
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}

	// 6. 异步失效缓存
	if w.invalidateCallback != nil {
		go func() {
			invalidateCtx, cancel := runctx.DetachedTimeout(ctx, 5*time.Second)
			defer cancel()
			w.invalidateCallback(invalidateCtx, allUpdates)
		}()
	}

	return nil
}

// sortUpdatesByLayer 按层级排序（Provider < Credential < Model < Node）
func sortUpdatesByLayer(updates []StateUpdate) []StateUpdate {
	sorted := make([]StateUpdate, len(updates))
	copy(sorted, updates)

	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].Layer < sorted[j].Layer
	})

	return sorted
}

// applyProviderUpdate 应用Provider层更新
func (w *BatchWriter) applyProviderUpdate(ctx context.Context, tx pgx.Tx, update StateUpdate) error {
	now := time.Now()

	// 构建动态SQL
	query := `UPDATE providers SET updated_at = $1`
	args := []interface{}{now}
	argIdx := 2

	if update.Enabled != nil {
		query += fmt.Sprintf(", enabled = $%d", argIdx)
		args = append(args, *update.Enabled)
		argIdx++
	}

	if update.ManualDisabled != nil {
		query += fmt.Sprintf(", manual_disabled = $%d", argIdx)
		args = append(args, *update.ManualDisabled)
		argIdx++
	}

	query += fmt.Sprintf(" WHERE id = $%d", argIdx)
	args = append(args, update.ProviderID)

	_, err := tx.Exec(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("update provider %d: %w", update.ProviderID, err)
	}

	return nil
}

// applyCredentialUpdate 应用Credential层更新
func (w *BatchWriter) applyCredentialUpdate(ctx context.Context, tx pgx.Tx, update StateUpdate) error {
	now := time.Now()

	// 构建动态SQL
	query := `UPDATE credentials SET state_updated_at = $1`
	args := []interface{}{now}
	argIdx := 2

	if update.AvailabilityState != nil {
		query += fmt.Sprintf(", availability_state = $%d", argIdx)
		args = append(args, *update.AvailabilityState)
		argIdx++
	}

	if update.HealthStatus != nil {
		query += fmt.Sprintf(", health_status = $%d", argIdx)
		args = append(args, *update.HealthStatus)
		argIdx++
	}

	if update.QuotaState != nil {
		query += fmt.Sprintf(", quota_state = $%d", argIdx)
		args = append(args, *update.QuotaState)
		argIdx++
	}

	if update.ConsecutiveFailures != nil {
		query += fmt.Sprintf(", consecutive_failures = $%d", argIdx)
		args = append(args, *update.ConsecutiveFailures)
		argIdx++
	}

	query += fmt.Sprintf(" WHERE id = $%d AND lifecycle_status = 'active'", argIdx)
	args = append(args, update.CredentialID)

	_, err := tx.Exec(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("update credential %d: %w", update.CredentialID, err)
	}

	return nil
}

// applyModelUpdate 应用Model层更新
func (w *BatchWriter) applyModelUpdate(ctx context.Context, tx pgx.Tx, update StateUpdate) error {
	now := time.Now()

	if update.ProbeState == nil {
		return nil
	}

	// 更新model_probe_state表
	query := `
		INSERT INTO model_probe_state (credential_id, raw_model_name, state, last_attempt_at)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (credential_id, raw_model_name)
		DO UPDATE SET
			state = EXCLUDED.state,
			last_attempt_at = EXCLUDED.last_attempt_at
	`

	_, err := tx.Exec(ctx, query, update.CredentialID, update.Model, *update.ProbeState, now)
	if err != nil {
		return fmt.Errorf("update model probe state (%d, %s): %w", update.CredentialID, update.Model, err)
	}

	return nil
}

// applyNodeUpdate 应用Node层更新（写入内存/Redis，不写DB）
func (w *BatchWriter) applyNodeUpdate(ctx context.Context, tx pgx.Tx, update StateUpdate) error {
	// Node层状态暂存于credential_state_log表（轻量级）
	now := time.Now()

	query := `
		INSERT INTO credential_state_log (credential_id, raw_model_name, updated_at, last_error)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (credential_id, raw_model_name)
		DO UPDATE SET
			updated_at = EXCLUDED.updated_at,
			last_error = EXCLUDED.last_error
	`

	lastError := update.ErrorKind
	if lastError == "" {
		lastError = "unknown"
	}

	_, err := tx.Exec(ctx, query, update.CredentialID, update.Model, now, lastError)
	if err != nil {
		return fmt.Errorf("update node state log (%d, %s): %w", update.CredentialID, update.Model, err)
	}

	// 同时写入Redis（异步，允许失败）
	go func() {
		key := fmt.Sprintf("ursm:node:%d:%s", update.CredentialID, update.Model)
		data := map[string]interface{}{
			"updated_at": now.Unix(),
		}
		if update.ConsecutiveFailures != nil {
			data["consecutive_failures"] = *update.ConsecutiveFailures
		}
		if !update.Success {
			data["last_error"] = lastError
		}
		w.redis.HMSet(context.Background(), key, data)
		w.redis.Expire(context.Background(), key, 10*time.Minute)
	}()

	return nil
}

// buildCascadeUpdates 构建级联更新
func (w *BatchWriter) buildCascadeUpdates(ctx context.Context, tx pgx.Tx, updates []StateUpdate) ([]StateUpdate, error) {
	var cascades []StateUpdate

	for _, update := range updates {
		switch update.Layer {
		case LayerProvider:
			// Provider禁用 → 级联所有Credential
			if (update.Enabled != nil && !*update.Enabled) ||
				(update.ManualDisabled != nil && *update.ManualDisabled) {
				credIDs, err := getCredentialsByProvider(ctx, tx, update.ProviderID)
				if err != nil {
					return nil, fmt.Errorf("get credentials by provider %d: %w", update.ProviderID, err)
				}

				providerDisabledState := "provider_disabled"
				for _, credID := range credIDs {
					cascades = append(cascades, StateUpdate{
						Layer:             LayerCredential,
						CredentialID:      credID,
						AvailabilityState: &providerDisabledState,
						Timestamp:         update.Timestamp,
					})
				}
			}

		case LayerCredential:
			// Credential禁用 → 释放资源（在Manager层处理，这里仅记录）
			// 无需级联更新
		}
	}

	return cascades, nil
}

// getCredentialsByProvider 查询某Provider下所有active凭据
func getCredentialsByProvider(ctx context.Context, tx pgx.Tx, providerID int) ([]int, error) {
	query := `
		SELECT id FROM credentials
		WHERE provider_id = $1 AND lifecycle_status = 'active'
	`

	rows, err := tx.Query(ctx, query, providerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var credIDs []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		credIDs = append(credIDs, id)
	}

	return credIDs, rows.Err()
}
