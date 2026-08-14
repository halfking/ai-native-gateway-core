package autoroute

// standard_iq_gate.go — RT-1（doc 19 轨道 ROUTE）：MinStandardIQ 硬门禁。
//
// 在 ComplexityMatch 之外增加第二道能力过滤：当租户/平台开启了
// MinStandardIQ 门禁时，标准智商低于阈值的候选模型被直接排除，
// 不进入评分排序。
//
// 口径说明（勿误导）：这里的"标准智商"是 Artificial Analysis
// Intelligence Index（0-100 的准确率式复合分，Agents 34% / Coding 24%
// / Scientific 24% / General 18%），不是人类 IQ-100 量表。数值与
// models_canonical.standard_iq 及 modeliqdata 内嵌参考表同源。
//
// 失败语义：fail-open。参考表中查不到的模型一律放行——参考表是
// 策划快照（104 个模型），不允许它把候选池清空；查得到且低于阈值
// 才排除。minIQ <= 0 视为门禁关闭。

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/modeliqdata"
)

// StandardIQMatch reports whether the model passes the MinStandardIQ gate.
//
// 返回值：
//   - passes：是否放行（低于阈值 → false；查不到或 minIQ<=0 → true）
//   - iq：命中的标准智商值（查不到时为 0）
//   - found：参考表中是否命中（四级匹配：精确 → 点转横杠 →
//     CanonicalizeClientModel → NormalizeRouteKey，见 modeliqdata）
func StandardIQMatch(modelName string, minIQ float64) (passes bool, iq float64, found bool) {
	if minIQ <= 0 || modelName == "" {
		// 门禁关闭或无模型名：不查表，直接放行（iq/found 仅在查表时有意义，
		// 但 zero-threshold 分支保持签名统一，返回 0/false）。
		if minIQ <= 0 && modelName != "" {
			// threshold 关闭时仍返回真实值，方便调用方在 metadata 里记录
			iq, found, _ = modeliqdata.LookupStandardIQ(modelName)
			return true, iq, found
		}
		return true, 0, false
	}
	iq, found, _ = modeliqdata.LookupStandardIQ(modelName)
	if !found {
		return true, 0, false
	}
	return iq >= minIQ, iq, found
}

// StandardIQFilterReason renders the decision-metadata note for a candidate
// excluded by the gate. Kept low-cardinality: model name + threshold only.
func StandardIQFilterReason(modelName string, iq, minIQ float64) string {
	return fmt.Sprintf("standard_iq_below_min: model=%s iq=%.1f min=%.1f", modelName, iq, minIQ)
}

// resolveStandardIQThreshold resolves the effective MinStandardIQ threshold
// for one request. Per-tenant policy (routing_policy.weights_json 的
// min_standard_iq 键，经 Index 刷新加载) 优先于全局 FeatureFlags：
//   - 租户键存在且 > 0 → 该租户以该值开启门禁
//   - 租户键存在且 = 0 → 该租户显式关闭门禁
//   - 无租户键 / 无租户解析 → 回落全局 flag（默认关闭）
func resolveStandardIQThreshold(tenantPolicy map[string]float64, tenantID string, flags *FeatureFlags) float64 {
	if tenantPolicy != nil && tenantID != "" {
		if v, ok := tenantPolicy[tenantID]; ok {
			return v
		}
	}
	if flags != nil && flags.UseStandardIQGate {
		return flags.MinStandardIQ
	}
	return 0
}

// resolveTenantCode resolves the tenant code for an API key, tolerating a nil
// resolver (platform-level only) or a zero key id.
func resolveTenantCode(resolver func(int) string, apiKeyID int) string {
	if resolver == nil || apiKeyID <= 0 {
		return ""
	}
	return resolver(apiKeyID)
}

// loadTenantIQPolicies reads the per-tenant MinStandardIQ thresholds from
// routing_policy.weights_json -> min_standard_iq. Only rows that carry the
// key are returned; a missing key means "fall back to the global flag".
// Called best-effort from Index.Refresh — errors are logged and the previous
// policy is kept.
func loadTenantIQPolicies(ctx context.Context, pool *pgxpool.Pool) (map[string]float64, error) {
	if pool == nil {
		return nil, nil
	}
	rows, err := pool.Query(ctx, `
		SELECT tenant_id, (weights_json->>'min_standard_iq')::float8
		FROM routing_policy
		WHERE weights_json ? 'min_standard_iq'
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]float64)
	for rows.Next() {
		var tenantID string
		var v float64
		if err := rows.Scan(&tenantID, &v); err != nil {
			return nil, err
		}
		out[tenantID] = v
	}
	return out, rows.Err()
}
