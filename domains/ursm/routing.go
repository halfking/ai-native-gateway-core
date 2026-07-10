package ursm

import (
	"context"
	"fmt"
	"log/slog"
)

// IsAvailable 检查节点可用性（四层级联）
// 返回: (available, reason)
// 策略: fail-open（缓存miss时返回true）
func (m *Manager) IsAvailable(ctx context.Context, credentialID int, model string) (bool, string) {
	// Layer 1: Provider
	cred, err := m.credentialCache.Get(ctx, credentialKey(credentialID))
	if err != nil {
		// fail-open: 缓存miss时允许通过
		slog.Debug("credential cache miss, fail-open",
			"credential_id", credentialID,
			"error", err)
		return true, ""
	}

	provider, err := m.providerCache.Get(ctx, providerKey(cred.ProviderID))
	if err != nil {
		slog.Debug("provider cache miss, fail-open",
			"provider_id", cred.ProviderID,
			"error", err)
		return true, ""
	}

	if !provider.IsAvailable() {
		return false, provider.UnavailableReason()
	}

	// Layer 2: Credential
	if !cred.IsAvailable() {
		return false, cred.UnavailableReason()
	}

	// Layer 3: Model
	modelState, err := m.modelCache.Get(ctx, modelKey(credentialID, model))
	if err != nil {
		slog.Debug("model cache miss, fail-open",
			"credential_id", credentialID,
			"model", model,
			"error", err)
		return true, ""
	}

	if !modelState.IsAvailable() {
		return false, modelState.UnavailableReason()
	}

	// Layer 4: Node
	nodeState, err := m.nodeCache.Get(ctx, nodeKey(credentialID, model))
	if err != nil {
		slog.Debug("node cache miss, fail-open",
			"credential_id", credentialID,
			"model", model,
			"error", err)
		return true, ""
	}

	if !nodeState.IsAvailable() {
		return false, nodeState.UnavailableReason()
	}

	return true, ""
}

// GetAvailableNodes 获取可用节点列表
// 流程：
// 1. 从DB加载所有候选节点
// 2. 四层级联可用性检查
// 3. 检查并获取指纹槽
// 4. 检查并获取并发槽
// 5. 成本排序
// 6. 应用策略
func (m *Manager) GetAvailableNodes(
	ctx context.Context,
	model string,
	sessionID string,
) ([]RouteNode, error) {
	// 1. 从DB加载所有候选节点
	allNodes, err := m.loadNodesByModel(ctx, model)
	if err != nil {
		return nil, fmt.Errorf("failed to load nodes: %w", err)
	}

	if len(allNodes) == 0 {
		return nil, ErrNoAvailableNodes
	}

	slog.Debug("loaded candidate nodes",
		"model", model,
		"session_id", sessionID,
		"count", len(allNodes))

	var available []RouteNode

	// 2. 逐个检查可用性
	for _, node := range allNodes {
		// 2.1 四层级联可用性检查
		isAvail, reason := m.IsAvailable(ctx, node.CredentialID, node.RawModel)
		if !isAvail {
			slog.Debug("node filtered by availability",
				"credential_id", node.CredentialID,
				"model", node.RawModel,
				"reason", reason)
			continue
		}

		// 2026-07-09 audit follow-up:
		// URSM previously did a real FP/concurrency Acquire during route planning.
		// That made every candidate selection hold resources before the executor
		// even chose a final credential, and there was no full release lifecycle
		// for the unchosen nodes. For now URSM stays responsible for state/filter
		// + ranking, while the actual resource gating remains in the mature
		// executor path (FpSlots + Limiter), which already has explicit request
		// end cleanup. This avoids double-accounting and removes the structural
		// leak where planning itself consumed slots.
		available = append(available, node)
	}

	if len(available) == 0 {
		return nil, ErrNoAvailableNodes
	}

	slog.Debug("after availability filtering",
		"model", model,
		"session_id", sessionID,
		"available_count", len(available))

	// 3. 成本排序
	var sorted []RouteNode
	if m.costScorer != nil {
		sorted = m.costScorer.SortByCompositeScore(available)
	} else {
		// 降级：保持原顺序
		sorted = available
	}

	// 4. 应用策略（Tier/Billing/Sticky）
	final := applyRoutingPolicy(sorted, sessionID)

	slog.Info("routing decision completed",
		"model", model,
		"session_id", sessionID,
		"final_count", len(final))

	return final, nil
}

// loadNodesByModel 从DB加载候选节点
func (m *Manager) loadNodesByModel(ctx context.Context, model string) ([]RouteNode, error) {
	query := `
		SELECT 
			c.id AS credential_id,
			c.provider_id,
			p.display_name AS provider_name,
			pm.raw_model_name,
			COALESCE(mo.price_in_per_1m, 0) AS price_in,
			COALESCE(mo.price_out_per_1m, 0) AS price_out,
			COALESCE(mo.currency, 'USD') AS currency,
			COALESCE(c.fp_slot_limit, 20) AS fp_slot_limit,
			COALESCE(c.concurrency_limit, 50) AS concurrency_limit,
			COALESCE(ns.success_rate, 0.95) AS success_rate,
			COALESCE(ns.p95_latency_ms, 1000) AS p95_latency_ms,
			COALESCE(c.health_status, 'healthy') AS health_status
		FROM credentials c
		JOIN providers p ON p.id = c.provider_id
		JOIN provider_models pm ON pm.provider_id = c.provider_id
		LEFT JOIN model_offers mo ON mo.credential_id = c.id AND mo.raw_model_name = pm.raw_model_name
		LEFT JOIN node_stats ns ON ns.credential_id = c.id AND ns.raw_model_name = pm.raw_model_name
		WHERE (LOWER(pm.raw_model_name) = LOWER($1) OR LOWER(pm.standardized_name) = LOWER($1))
		  AND c.lifecycle_status = 'active'
		  AND c.status = 'active'
		ORDER BY c.id
	`

	rows, err := m.db.Query(ctx, query, model)
	if err != nil {
		return nil, fmt.Errorf("query failed: %w", err)
	}
	defer rows.Close()

	var nodes []RouteNode
	for rows.Next() {
		var node RouteNode
		if err := rows.Scan(
			&node.CredentialID,
			&node.ProviderID,
			&node.ProviderName,
			&node.RawModel,
			&node.PriceInPer1M,
			&node.PriceOutPer1M,
			&node.Currency,
			&node.FpSlotLimit,
			&node.ConcurrencyLimit,
			&node.SuccessRate,
			&node.P95LatencyMs,
			&node.HealthStatus,
		); err != nil {
			slog.Warn("failed to scan node row",
				"error", err)
			continue
		}

		// 初始化状态
		node.Available = false
		node.FpSlotIndex = -1
		node.ConcurrencyHeld = false

		nodes = append(nodes, node)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows iteration failed: %w", err)
	}

	return nodes, nil
}
