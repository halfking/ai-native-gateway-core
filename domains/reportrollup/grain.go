// grain.go —— 对账快照的最细粒度（grain）scope（2026-09-29 对帐页多维筛选轮）。
//
// 为什么必须有 grain：既有六 scope（daily_total / daily_by_provider /
// daily_by_model / internal_tenant / internal_person / internal_model）是
// **边缘汇总**——每个 scope 只带 1~2 个维度。对帐页要求供应商 / 凭据 / 模型 /
// 租户 / 用户 / apikey **任意组合**过滤，而任意两个维度同时过滤都无法从
// 任何单个边缘 scope 折叠出来（读面 report.go 里 filter.empty() 的分支树
// 就是这个限制的化石：它只能正确处理单维过滤）。
//
// grain scope 把**完整维度元组**作为行落库：
//
//	daily_grain    = (provider, credential, api_key, tenant, person) × 出站模型
//	internal_grain = 同粒度，但仅 business 流量（内部计费口径）
//
// 于是读面可以从最细行折叠出任意维度组合的汇总，且「总计 = Σ 各分组」恒成立。
// 本地真库实测粒度：全部流量约 1005 行/日、business 约 272 行/日——对
// report_snapshots 这种日表完全可接受（30 天区间约 3 万行，聚合在 PG 侧做）。
//
// 口径与旧 scope 保持一致：
//   - daily_grain 含全部流量类（探针/自检同样烧供应商钱，对帐须全量）；
//   - internal_grain 仅 business 流量（内部计费口径）；
//   - 人员身份回落链与 internal_person 同款：end_user_id → 'person:'+person_hash
//     → 'unknown'。
package reportrollup

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Scope 枚举新增的两个最细粒度值（其余六值见 rollup.go）。
const (
	ScopeDailyGrain    Scope = "daily_grain"
	ScopeInternalGrain Scope = "internal_grain"
)

// grainScopeKey 把五维元组编码成 scope_key（UNIQUE 四键之一）。
//
// 编码必须**单射**：两个不同元组绝不能编出同一个串，否则 ON CONFLICT 会把
// 两天的用量互相覆盖（internal_person 踩过同款，见 internalPersonScopeKey
// 的租户编码注记）。文本维度用 `<字节长度>:<值>` 的长度前缀，数值维度用
// 纯十进制或 `-`（NULL），标签固定为 p/c/k/t/u 并以 `|` 分段——从左往右
// 逐段解析无歧义，含 `|`、`:` 的租户键/用户名也不会撞。
//
// 五维同时冗余进独立列（provider_id / credential_id / api_key_id /
// tenant_id / person），本编码只服务唯一键与人工排查，不承担过滤职责。
func grainScopeKey(providerID, credentialID, apiKeyID *int64, tenant, person string) string {
	var b strings.Builder
	writeNum := func(tag string, v *int64) {
		b.WriteString(tag)
		if v == nil {
			b.WriteByte('-')
		} else {
			b.WriteString(strconv.FormatInt(*v, 10))
		}
		b.WriteByte('|')
	}
	writeStr := func(tag, v string) {
		b.WriteString(tag)
		b.WriteString(strconv.Itoa(len(v)))
		b.WriteByte(':')
		b.WriteString(v)
		b.WriteByte('|')
	}
	writeNum("p", providerID)
	writeNum("c", credentialID)
	writeNum("k", apiKeyID)
	writeStr("t", tenant)
	writeStr("u", person)
	return b.String()
}

// grainDaySQL 最细粒度日聚合。两个口径共用一段 SQL：businessOnly 只切换
// traffic_class 过滤（provider 面全流量 / internal 面业务流量）。
//
// 维度列的 NULL 用 IS NOT DISTINCT FROM 连接 errb（等价于 NULL 安全等值）——
// 未落到 provider/credential/api_key 的失败行在 provider 面上真实存在，
// 用普通等值 JOIN 会把它们与 errb 断开，错误分布整格丢失（provider 面
// 的既有 SQL 已用同款写法）。
func grainDaySQL(businessOnly bool) string {
	classFilter := ""
	if businessOnly {
		classFilter = "\n      AND traffic_class = 'business'"
	}
	return `
WITH f AS (
    SELECT provider_id, credential_id, api_key_id, tenant_id,
           COALESCE(NULLIF(end_user_id, ''),
                    NULLIF('person:' || COALESCE(person_hash, ''), 'person:'),
                    '` + personUnknown + `') AS person,
           COALESCE(raw_model_name, '') AS raw_model_name,
           status, error_kind,
           prompt_tokens, completion_tokens, cache_read_tokens, cache_write_tokens,
           credits_charged, cost_amount, cost_currency, latency_ms
    FROM usage_facts
    WHERE occurred_at >= $1 AND occurred_at < $2` + classFilter + `
),
errb AS (
    SELECT provider_id, credential_id, api_key_id, tenant_id, person, raw_model_name,
           jsonb_object_agg(kind, n) AS breakdown
    FROM (
        SELECT provider_id, credential_id, api_key_id, tenant_id, person, raw_model_name,
               COALESCE(NULLIF(error_kind, ''), 'unknown') AS kind,
               COUNT(*)::bigint AS n
        FROM f
        WHERE status <> 'success'
        GROUP BY 1, 2, 3, 4, 5, 6, 7
    ) e
    GROUP BY 1, 2, 3, 4, 5, 6
)
SELECT f.provider_id, f.credential_id, f.api_key_id, f.tenant_id, f.person, f.raw_model_name,
       COUNT(*)::bigint,
       COUNT(*) FILTER (WHERE f.status = 'success')::bigint,
       COALESCE(SUM(f.prompt_tokens), 0)::bigint,
       COALESCE(SUM(f.completion_tokens), 0)::bigint,
       COALESCE(SUM(f.cache_read_tokens), 0)::bigint,
       COALESCE(SUM(f.cache_write_tokens), 0)::bigint,
       COALESCE(SUM(f.credits_charged), 0)::bigint,
       COALESCE(ROUND(SUM(f.cost_amount) * 100), 0)::bigint AS cost_cents,
       COALESCE(MAX(f.cost_currency), 'USD') AS currency,
       COALESCE(percentile_cont(0.5) WITHIN GROUP (ORDER BY f.latency_ms)
                FILTER (WHERE f.status = 'success'), 0)::bigint AS p50_ms,
       COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY f.latency_ms)
                FILTER (WHERE f.status = 'success'), 0)::bigint AS p95_ms,
       COALESCE(errb.breakdown, '{}'::jsonb) AS error_breakdown
FROM f
LEFT JOIN errb
    ON errb.provider_id   IS NOT DISTINCT FROM f.provider_id
   AND errb.credential_id IS NOT DISTINCT FROM f.credential_id
   AND errb.api_key_id    IS NOT DISTINCT FROM f.api_key_id
   AND errb.tenant_id     IS NOT DISTINCT FROM f.tenant_id
   AND errb.person        = f.person
   AND errb.raw_model_name = f.raw_model_name
GROUP BY f.provider_id, f.credential_id, f.api_key_id, f.tenant_id, f.person,
         f.raw_model_name, errb.breakdown`
}

// queryGrainDay 执行最细粒度日聚合。scope 决定口径（daily_grain 全流量 /
// internal_grain 业务流量）；centsPerCredit 仅 internal_grain 冻结进
// price_snapshot（内部金额 = credits × 冻结系数，与三个 internal scope 同源）。
func queryGrainDay(ctx context.Context, q Querier, scope Scope, start, end time.Time, centsPerCredit float64) ([]Bucket, error) {
	rows, err := q.Query(ctx, grainDaySQL(scope == ScopeInternalGrain), start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	price := map[string]any{}
	if scope == ScopeInternalGrain {
		price = centsSnapshot(centsPerCredit)
	}
	buckets := make([]Bucket, 0, 1024)
	for rows.Next() {
		var providerID, credentialID, apiKeyID sql.NullInt64
		var tenant, person string
		b := Bucket{Scope: scope, PriceSnapshot: price}
		var breakdown []byte
		if err := rows.Scan(&providerID, &credentialID, &apiKeyID, &tenant, &person, &b.RawModelName,
			&b.RequestCount, &b.SuccessCount,
			&b.InputTokens, &b.OutputTokens, &b.CacheReadTokens, &b.CacheWriteTokens,
			&b.CreditsCharged, &b.CostCents, &b.Currency,
			&b.LatencyP50Ms, &b.LatencyP95Ms, &breakdown); err != nil {
			return nil, err
		}
		if providerID.Valid {
			v := providerID.Int64
			b.ProviderID = &v
		}
		if credentialID.Valid {
			v := credentialID.Int64
			b.CredentialID = &v
		}
		if apiKeyID.Valid {
			v := apiKeyID.Int64
			b.APIKeyID = &v
		}
		tenantCopy := tenant
		b.TenantID = &tenantCopy
		personCopy := person
		b.Person = &personCopy
		b.ScopeKey = grainScopeKey(b.ProviderID, b.CredentialID, b.APIKeyID, tenant, person)
		if err := decodeBreakdown(breakdown, &b.ErrorKindBreakdown); err != nil {
			return nil, err
		}
		buckets = append(buckets, b)
	}
	return buckets, rows.Err()
}

// GrainScopes 返回视角对应的最细粒度 scope（读面回落判定与写面共用同一份
// 枚举，避免两处各写一个字面量后漂移）。
func GrainScopes(view View) (Scope, []Scope, error) {
	switch view {
	case ViewProvider, ViewCredential:
		// credential 视角与 provider 视角同面（全流量）——凭据不是另一种
		// 流量口径，只是同一份流量换一种切分维度。
		return ScopeDailyGrain, []Scope{ScopeDailyTotal, ScopeDailyByProvider, ScopeDailyByModel}, nil
	case ViewInternal, ViewKey:
		return ScopeInternalGrain, []Scope{ScopeInternalTenant, ScopeInternalPerson, ScopeInternalModel}, nil
	default:
		return "", nil, fmt.Errorf("unknown view %q", view)
	}
}
