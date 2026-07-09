package credentialstate

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/kaixuan/llm-gateway-go/internal/runctx"
)

// 节点生命周期管理

// RegisterNode 注册状态监控节点（含去重）
func (m *Manager) RegisterNode(ctx context.Context, req RegisterNodeRequest) error {
	// 1. 去重检查
	var exists bool
	err := m.db.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM credential_state_nodes 
			WHERE credential_id = $1 AND raw_model_name = $2
		)
	`, req.CredentialID, req.RawModelName).Scan(&exists)

	if err != nil {
		return fmt.Errorf("check existence: %w", err)
	}

	if exists {
		slog.Info("credstate: node already exists (idempotent)",
			"credential_id", req.CredentialID,
			"model", req.RawModelName)
		return nil
	}

	// 2. 插入节点
	_, err = m.db.Exec(ctx, `
		INSERT INTO credential_state_nodes 
			(credential_id, raw_model_name, node_status, probe_enabled, 
			 probe_interval_seconds, created_by)
		VALUES ($1, $2, 'active', $3, $4, $5)
		ON CONFLICT (credential_id, raw_model_name) DO UPDATE
		SET node_status = 'active',
			probe_enabled = EXCLUDED.probe_enabled,
			probe_interval_seconds = EXCLUDED.probe_interval_seconds,
			updated_at = NOW()
	`, req.CredentialID, req.RawModelName, req.ProbeEnabled,
		int(req.ProbeInterval.Seconds()), req.CreatedBy)

	if err != nil {
		return fmt.Errorf("insert node: %w", err)
	}

	// 3. 清除缓存（等待探测后自动填充）
	key := m.cacheKey(req.CredentialID, req.RawModelName)
	m.memCache.Delete(key)

	// 4. 触发立即探测（方案 A：立即探测）
	if req.TriggerProbe && m.modelProbeSubmitter != nil {
		go func() {
			probeCtx, cancel := runctx.DetachedTimeout(ctx, 10*time.Second)
			defer cancel()
			_ = m.modelProbeSubmitter(probeCtx, req.CredentialID, req.RawModelName)
		}() //nolint:errcheck // fire-and-forget probe submit
	}

	slog.Info("credstate: node registered",
		"credential_id", req.CredentialID,
		"model", req.RawModelName,
		"trigger_probe", req.TriggerProbe)

	return nil
}

// UnregisterNode 注销节点（硬删除）
func (m *Manager) UnregisterNode(ctx context.Context, credID int, model string) error {
	result, err := m.db.Exec(ctx, `
		DELETE FROM credential_state_nodes
		WHERE credential_id = $1 AND raw_model_name = $2
	`, credID, model)

	if err != nil {
		return fmt.Errorf("delete node: %w", err)
	}

	// 清除缓存
	key := m.cacheKey(credID, model)
	m.memCache.Delete(key)
	if m.redisClient != nil {
		redisKey := "llmgw:credstate:" + key
		m.redisClient.Del(ctx, redisKey)
	}

	if result.RowsAffected() > 0 {
		slog.Info("credstate: node unregistered",
			"credential_id", credID,
			"model", model)
	}

	return nil
}

// EnableNode 启用节点
func (m *Manager) EnableNode(ctx context.Context, credID int, model string) error {
	result, err := m.db.Exec(ctx, `
		UPDATE credential_state_nodes
		SET node_status = 'active',
			probe_enabled = TRUE,
			disabled_at = NULL,
			disabled_by = NULL,
			updated_at = NOW()
		WHERE credential_id = $1
		  AND raw_model_name = $2
		  AND node_status = 'disabled'
	`, credID, model)

	if err != nil {
		return fmt.Errorf("enable node: %w", err)
	}

	if result.RowsAffected() > 0 {
		slog.Info("credstate: node enabled",
			"credential_id", credID,
			"model", model)

		// 触发立即探测验证可用性
		if m.modelProbeSubmitter != nil {
			go func() {
				probeCtx, cancel := runctx.DetachedTimeout(ctx, 10*time.Second)
				defer cancel()
				_ = m.modelProbeSubmitter(probeCtx, credID, model)
			}() //nolint:errcheck // fire-and-forget probe submit
		}

	}

	return nil
}

// DisableNode 禁用节点（方案 A：清除缓存）
func (m *Manager) DisableNode(ctx context.Context, credID int, model string, reason string) error {
	result, err := m.db.Exec(ctx, `
		UPDATE credential_state_nodes
		SET node_status = 'disabled',
			probe_enabled = FALSE,
			disabled_at = NOW(),
			disabled_by = $3,
			updated_at = NOW()
		WHERE credential_id = $1
		  AND raw_model_name = $2
		  AND node_status = 'active'
	`, credID, model, reason)

	if err != nil {
		return fmt.Errorf("disable node: %w", err)
	}

	// 方案 A：清除缓存
	key := m.cacheKey(credID, model)
	m.memCache.Delete(key)
	if m.redisClient != nil {
		redisKey := "llmgw:credstate:" + key
		m.redisClient.Del(ctx, redisKey)
	}

	if result.RowsAffected() > 0 {
		slog.Info("credstate: node disabled and cache cleared",
			"credential_id", credID,
			"model", model,
			"reason", reason)
	}

	return nil
}

// RegisterNodesForCredential 批量注册凭据的所有模型节点
func (m *Manager) RegisterNodesForCredential(ctx context.Context, credID int, models []string) error {
	// 查询凭据关联的所有模型
	rows, err := m.db.Query(ctx, `
		SELECT DISTINCT pm.raw_model_name
		FROM provider_models pm
		JOIN credential_model_bindings cmb ON cmb.provider_model_id = pm.id
		WHERE cmb.credential_id = $1
		  AND cmb.available = TRUE
	`, credID)

	if err != nil {
		return fmt.Errorf("query models: %w", err)
	}
	defer rows.Close()

	var dbModels []string
	for rows.Next() {
		var model string
		if err := rows.Scan(&model); err != nil {
			continue
		}
		dbModels = append(dbModels, model)
	}

	// 合并用户提供的模型列表（去重）
	modelSet := make(map[string]bool)
	for _, mdl := range dbModels {
		modelSet[mdl] = true
	}
	for _, mdl := range models {
		if mdl != "" {
			modelSet[mdl] = true
		}
	}

	// 批量注册
	var errs []error
	registered := 0
	for mdl := range modelSet {
		req := RegisterNodeRequest{
			CredentialID:  credID,
			RawModelName:  mdl,
			ProbeEnabled:  true,
			ProbeInterval: time.Hour,
			CreatedBy:     "system",
			TriggerProbe:  false, // 批量注册时不立即触发探测
		}

		if err := m.RegisterNode(ctx, req); err != nil {
			errs = append(errs, fmt.Errorf("register %s: %w", mdl, err))
		} else {
			registered++
		}
	}

	slog.Info("credstate: batch registration completed",
		"credential_id", credID,
		"total_models", len(modelSet),
		"registered", registered,
		"errors", len(errs))

	if len(errs) > 0 {
		return fmt.Errorf("batch registration partial failure: %d errors", len(errs))
	}

	return nil
}

// UnregisterNodesForCredential 批量注销凭据的所有节点
func (m *Manager) UnregisterNodesForCredential(ctx context.Context, credID int) error {
	result, err := m.db.Exec(ctx, `
		DELETE FROM credential_state_nodes
		WHERE credential_id = $1
	`, credID)

	if err != nil {
		return fmt.Errorf("batch delete: %w", err)
	}

	// 清理缓存（通配符删除）
	m.memCache.Range(func(k, v interface{}) bool {
		key := k.(string)
		if len(key) > 0 {
			// 解析 key: "credID:model"
			var keyCredID int
			_, _ = fmt.Sscanf(key, "%d:", &keyCredID) //nolint:errcheck // best-effort parse, keyCredID may remain 0
			if keyCredID == credID {
				m.memCache.Delete(key)
			}
		}
		return true
	})

	slog.Info("credstate: batch deletion completed",
		"credential_id", credID,
		"nodes_deleted", result.RowsAffected())

	return nil
}

// NodeExists 检查节点是否存在
func (m *Manager) NodeExists(ctx context.Context, credID int, model string) (bool, error) {
	var exists bool
	err := m.db.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM credential_state_nodes
			WHERE credential_id = $1 AND raw_model_name = $2
		)
	`, credID, model).Scan(&exists)

	return exists, err
}

// GetNode 获取节点信息
func (m *Manager) GetNode(ctx context.Context, credID int, model string) (*StateNode, error) {
	var node StateNode
	err := m.db.QueryRow(ctx, `
		SELECT id, credential_id, raw_model_name, node_status, probe_enabled,
		       probe_interval_seconds, last_probe_at, next_probe_at,
		       created_at, created_by, updated_at, disabled_at, disabled_by
		FROM credential_state_nodes
		WHERE credential_id = $1 AND raw_model_name = $2
	`, credID, model).Scan(
		&node.ID,
		&node.CredentialID,
		&node.RawModelName,
		&node.NodeStatus,
		&node.ProbeEnabled,
		&node.ProbeIntervalSeconds,
		&node.LastProbeAt,
		&node.NextProbeAt,
		&node.CreatedAt,
		&node.CreatedBy,
		&node.UpdatedAt,
		&node.DisabledAt,
		&node.DisabledBy,
	)

	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	return &node, nil
}
