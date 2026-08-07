package autocombo

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

// Resolver Auto Combo 解析器
//
// tenantID 在数据层统一为 TEXT (free_resource_catalog.tenant_id,
// auto_combo_templates.tenant_id), 与 streaming handler / keyInfo.TenantID
// 的字符串语义保持一致; 旧实现使用 int64 会触发 PG 操作符不匹配, 已在
// 2026-08-07 修正.
type Resolver struct {
	db *sql.DB
}

// NewResolver 创建解析器
func NewResolver(db *sql.DB) *Resolver {
	return &Resolver{db: db}
}

// Resolve 解析 auto/* 模型 ID 到 AutoComboSpec。
//
// 查询顺序:
//  1. 若 !strings.HasPrefix(modelID, "auto/"), 返回 (nil, nil), 由调用方
//     视为非虚拟路由.
//  2. 在当前租户查找 auto_combo_templates 行; 命中即返回.
//  3. 命中不到时, 回退到内置 builtinMap, 该 map 不带租户信息但仅支持
//     OmniFree 已约定的稳定模型名.
func (r *Resolver) Resolve(ctx context.Context, modelID string, tenantID string) (*AutoComboSpec, error) {
	if !strings.HasPrefix(modelID, "auto/") {
		return nil, nil
	}

	spec, err := r.queryDB(ctx, modelID, tenantID)
	if err == nil && spec != nil {
		return spec, nil
	}
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("query auto combo template: %w", err)
	}

	return r.getBuiltinTemplate(modelID, tenantID)
}

// queryDB 在数据库中查找模板；空 tenant 时回退到 default 行, 兼容历史数据。
func (r *Resolver) queryDB(ctx context.Context, modelID, tenantID string) (*AutoComboSpec, error) {
	if r.db == nil {
		return nil, sql.ErrNoRows
	}
	tenantFilter := tenantID
	if tenantFilter == "" {
		tenantFilter = "default"
	}

	const q = `
        SELECT
            id, combo_name, variant, tier_filter, free_type_filter,
            tos_filter, provider_allowlist, provider_denylist,
            model_pattern, scoring_weights_json, max_candidates,
            exploration_rate, enabled, tenant_id
        FROM auto_combo_templates
        WHERE combo_name = $1
          AND enabled = TRUE
          AND tenant_id = $2
    `

	var spec AutoComboSpec
	err := r.db.QueryRowContext(ctx, q, modelID, tenantFilter).Scan(
		&spec.ID, &spec.ComboName, &spec.Variant, &spec.TierFilter,
		&spec.FreeTypeFilter, &spec.ToSFilter, &spec.ProviderAllowlist,
		&spec.ProviderDenylist, &spec.ModelPattern, &spec.ScoringWeightsJSON,
		&spec.MaxCandidates, &spec.ExplorationRate, &spec.Enabled, &spec.TenantID,
	)
	if err != nil {
		return nil, err
	}
	return &spec, nil
}

// getBuiltinTemplate 内置模板回退
func (r *Resolver) getBuiltinTemplate(modelID, tenantID string) (*AutoComboSpec, error) {
	builtinMap := map[string]Variant{
		"auto/free":           VariantCheap,
		"auto/best-free":      VariantCheap,
		"auto/coding:free":    VariantCoding,
		"auto/reasoning:free": VariantReasoning,
		"auto/fast:free":      VariantFast,
		"auto/creative:free":  VariantCreative,
	}

	variant, ok := builtinMap[modelID]
	if !ok {
		return nil, fmt.Errorf("unknown auto combo: %s", modelID)
	}

	// 默认权重（总和 = 1.0）
	weights := ScoringWeights{
		HealthScore:    0.3,
		LatencyP95:     0.2,
		QuotaRemaining: 0.25,
		Cost:           0.0, // 免费模式，成本权重为 0
		TaskFit:        0.15,
		TierAffinity:   0.1,
	}

	// 根据变体调整权重（保持总和 = 1.0，Cost 和 TierAffinity 已默认 0）
	switch variant {
	case VariantFast:
		// 强调低延迟: Latency 0.5 + Health 0.3 + Quota 0.2 = 1.0
		weights = ScoringWeights{
			HealthScore: 0.3, LatencyP95: 0.5, QuotaRemaining: 0.2,
		}
	case VariantCoding:
		// 强调任务适配: TaskFit 0.4 + Health 0.3 + Quota 0.3 = 1.0
		weights = ScoringWeights{
			HealthScore: 0.3, QuotaRemaining: 0.3, TaskFit: 0.4,
		}
	case VariantReasoning:
		// 强调推理质量: TaskFit 0.4 + Health 0.4 + Quota 0.2 = 1.0
		weights = ScoringWeights{
			HealthScore: 0.4, QuotaRemaining: 0.2, TaskFit: 0.4,
		}
	}

	weightsJSON, _ := json.Marshal(weights)

	return &AutoComboSpec{
		ComboName:          modelID,
		Variant:            variant,
		TierFilter:         []string{"free"},
		ToSFilter:          []string{"ok", "caution"},
		ProviderAllowlist:  []string{},
		ProviderDenylist:   []string{},
		ScoringWeightsJSON: weightsJSON,
		MaxCandidates:      50,
		ExplorationRate:    0.05,
		Enabled:            true,
		TenantID:           tenantID,
	}, nil
}
