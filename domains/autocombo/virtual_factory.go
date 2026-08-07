package autocombo

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/freeresource"
)

// VirtualFactory 虚拟 Combo 工厂
type VirtualFactory struct {
	db           *sql.DB
	quotaTracker *freeresource.QuotaTracker
}

// NewVirtualFactory 创建虚拟工厂
func NewVirtualFactory(db *sql.DB, quotaTracker *freeresource.QuotaTracker) *VirtualFactory {
	return &VirtualFactory{
		db:           db,
		quotaTracker: quotaTracker,
	}
}

// Build 动态构建虚拟 combo 的候选池
func (vf *VirtualFactory) Build(ctx context.Context, spec *AutoComboSpec, tenantID int64) (*VirtualCombo, error) {
	var candidates []Candidate

	// 1. 加载已连接的免费凭据
	credCandidates, err := vf.loadCredentialCandidates(ctx, spec, tenantID)
	if err != nil {
		return nil, fmt.Errorf("load credential candidates: %w", err)
	}
	candidates = append(candidates, credCandidates...)

	// 2. 加载 keyless 提供商
	keylessCandidates, err := vf.loadKeylessCandidates(ctx, spec, tenantID)
	if err != nil {
		return nil, fmt.Errorf("load keyless candidates: %w", err)
	}
	candidates = append(candidates, keylessCandidates...)

	// 3. 配额预检过滤
	filtered := vf.filterByQuota(ctx, candidates, tenantID)

	// 4. 限制候选数量
	if len(filtered) > spec.MaxCandidates {
		filtered = filtered[:spec.MaxCandidates]
	}

	return &VirtualCombo{
		Name:            spec.ComboName,
		Variant:         spec.Variant,
		CandidatePool:   filtered,
		ExplorationRate: spec.ExplorationRate,
		CreatedAt:       time.Now(),
	}, nil
}

// loadCredentialCandidates 加载凭据候选
func (vf *VirtualFactory) loadCredentialCandidates(ctx context.Context, spec *AutoComboSpec, tenantID int64) ([]Candidate, error) {
	query := `
        SELECT 
            c.id AS credential_id,
            c.provider_code,
            frc.model_id,
            frc.display_name,
            COALESCE(c.health_score, 1.0) AS health_score,
            COALESCE(c.p95_latency_ms, 1000) AS p95_latency_ms,
            0 AS cost_per_1m
        FROM credentials c
        JOIN free_resource_catalog frc ON c.provider_code = frc.provider_code
        WHERE c.tenant_id = $1
          AND c.enabled = TRUE
          AND c.is_free_tier = TRUE
          AND frc.enabled = TRUE
          AND frc.tos_verdict = ANY($2)
    `

	args := []interface{}{tenantID, spec.ToSFilter}

	// 添加 allowlist 过滤
	if len(spec.ProviderAllowlist) > 0 {
		query += " AND c.provider_code = ANY($3)"
		args = append(args, spec.ProviderAllowlist)
	}

	// 添加 denylist 过滤
	if len(spec.ProviderDenylist) > 0 {
		idx := len(args) + 1
		query += fmt.Sprintf(" AND c.provider_code != ALL($%d)", idx)
		args = append(args, spec.ProviderDenylist)
	}

	rows, err := vf.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var candidates []Candidate
	for rows.Next() {
		var c Candidate
		if err := rows.Scan(&c.CredentialID, &c.ProviderCode, &c.ModelID,
			&c.DisplayName, &c.HealthScore, &c.LatencyP95, &c.CostPer1M); err != nil {
			return nil, err
		}
		c.IsFree = true
		c.QuotaRemain = 1.0 // 默认满配额，后续通过 filterByQuota 更新
		candidates = append(candidates, c)
	}

	return candidates, rows.Err()
}

// loadKeylessCandidates 加载 keyless 候选
func (vf *VirtualFactory) loadKeylessCandidates(ctx context.Context, spec *AutoComboSpec, tenantID int64) ([]Candidate, error) {
	query := `
        SELECT 
            kp.provider_code,
            pc.display_name,
            COALESCE(kp.reliability_score, 1.0) AS reliability_score,
            100 AS est_latency_ms
        FROM keyless_providers kp
        JOIN provider_catalog pc ON kp.provider_code = pc.code
        WHERE kp.tenant_id = $1
          AND kp.enabled = TRUE
          AND kp.allowlist_in_auto_combo = TRUE
    `

	args := []interface{}{tenantID}

	// 添加 allowlist 过滤
	if len(spec.ProviderAllowlist) > 0 {
		query += " AND kp.provider_code = ANY($2)"
		args = append(args, spec.ProviderAllowlist)
	}

	rows, err := vf.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var candidates []Candidate
	for rows.Next() {
		var c Candidate
		c.CredentialID = -1 // SYNTHETIC_KEYLESS_CREDENTIAL_ID
		c.IsKeyless = true
		c.IsFree = true
		c.CostPer1M = 0
		c.QuotaRemain = 1.0 // Keyless 无配额限制

		if err := rows.Scan(&c.ProviderCode, &c.DisplayName, &c.HealthScore, &c.LatencyP95); err != nil {
			return nil, err
		}
		candidates = append(candidates, c)
	}

	return candidates, rows.Err()
}

// filterByQuota 配额预检过滤
func (vf *VirtualFactory) filterByQuota(ctx context.Context, candidates []Candidate, tenantID int64) []Candidate {
	filtered := make([]Candidate, 0, len(candidates))

	for _, c := range candidates {
		if c.IsKeyless {
			// Keyless 无配额限制
			filtered = append(filtered, c)
			continue
		}

		// 配额预检
		ok, err := vf.quotaTracker.Preflight(ctx, freeresource.PreflightRequest{
			CredentialID:    c.CredentialID,
			ProviderCode:    c.ProviderCode,
			ModelID:         c.ModelID,
			WindowType:      freeresource.WindowTypeDay1,
			DefaultLimit:    1000,
			MinRemainingPct: 0.1, // 最少剩余 10%
			TenantID:        tenantID,
		})

		if err != nil {
			// 预检错误，跳过
			continue
		}

		if ok {
			filtered = append(filtered, c)
		}
	}

	return filtered
}
