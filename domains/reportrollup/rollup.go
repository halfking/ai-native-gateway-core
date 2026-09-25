// Package reportrollup 实现对账报表的每日快照聚合与区间汇总读面。
//
// 数据流：usage_facts（请求级真相源，含 success/failure/rate_limited 终态、
// error_kind 分类、四类 token、供应商成本 cost_amount 与内部计费
// credits_charged）→ RollupDay 按天聚合成六个 scope 的快照行写入
// report_snapshots（迁移 745+746）→ RangeReport 只读快照行按任意时间
// 区间（日/周/月/自定义）二次汇总，导出 Excel 双 sheet（用量 / 模型质量
// 与错误分析）。日维度落地一次、区间汇总不回扫原始请求日志。
//
// scope 口径（SSOT = sql/objects/tables/report_snapshots.sql）：
//   - provider 面三 scope（daily_total / daily_by_provider /
//     daily_by_model）含全部流量类——探针/自检同样烧供应商钱，对帐须全量；
//   - internal 面三 scope（internal_tenant / internal_person /
//     internal_model）仅 business 流量——内部计费口径。
package reportrollup

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// internalPersonScopeKey 把租户编码进 internal_person 的 scope_key。
// 四键 UNIQUE (scope, scope_key, report_date, raw_model_name) 不含
// tenant_id 列——人员粒度若只以 person 作键，跨租户同名人员（含双双
// 归 'unknown'）的日桶会在 ON CONFLICT 中互相覆盖（R65 P1）。
//
// 编码用长度前缀（len(tenant):tenant:person）：R65 原稿的 \x00 分隔符
// 是 PG 非法编码——TEXT 列拒绝 NUL 字节，INSERT 全量 22021（本仓真库
// E2E 实证，pgxmock/纯单测探不到）。NUL 格式在真库零存活，无兼容负担。
func internalPersonScopeKey(tenant, person string) string {
	return strconv.Itoa(len(tenant)) + ":" + tenant + ":" + person
}

// splitInternalPersonScopeKey 读面还原 person 展示名；不符合长度前缀
// 格式的行（键格式变更前写入的历史快照，若有）原样返回（tenant=""）。
// 守卫必须校验到第三段冒号（i+1+n+1）：只校验到 tenant 尾（i+1+n）时，
// "2:ab" 这类恰好在前缀处耗尽的输入会通过守卫再在 k[i+1+n+1:] 越界
// panic；负数长度（"-1:xyz"）则会落到 k[i+1:i+1+n] 低>高 panic。
// （R67 24h 审计轮对账子代理实锤，可达面=读库中历史/外来 scope_key。）
func splitInternalPersonScopeKey(k string) (tenant, person string) {
	i := strings.IndexByte(k, ':')
	if i < 0 {
		return "", k
	}
	n, err := strconv.Atoi(k[:i])
	if err != nil || n < 0 || i+1+n+1 > len(k) {
		return "", k
	}
	return k[i+1 : i+1+n], k[i+1+n+1:]
}

// Scope 枚举 report_snapshots.scope 的六个合法值。
type Scope string

const (
	ScopeDailyTotal      Scope = "daily_total"
	ScopeDailyByProvider Scope = "daily_by_provider"
	ScopeDailyByModel    Scope = "daily_by_model"
	ScopeInternalTenant  Scope = "internal_tenant"
	ScopeInternalPerson  Scope = "internal_person"
	ScopeInternalModel   Scope = "internal_model"
)

// Querier 是聚合与写入所需的最小数据库面；*pgxpool.Pool 与 pgx.Tx 均满足，
// 测试可替换实现。
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// Snapshot 是 report_snapshots 的一行（读面投影；写面用 bucket）。
type Snapshot struct {
	Scope              Scope
	ScopeKey           string
	ReportDate         time.Time
	RawModelName       string
	RequestCount       int64
	SuccessCount       int64
	ErrorCount         int64
	InputTokens        int64
	OutputTokens       int64
	CacheReadTokens    int64
	CacheWriteTokens   int64
	ErrorKindBreakdown map[string]int64
	CacheHitRatio      *float64
	EstimatedCostCents int64
	Currency           string
	PriceSnapshot      map[string]any
	ProviderID         *int64
	CanonicalID        *int64
	TenantID           *string
	CreditsCharged     int64
	LatencyP50Ms       int64
	LatencyP95Ms       int64
	UpdatedAt          time.Time
}

// Bucket 是一次聚合产出的待写快照行（RollupDay 内部产物）。
type Bucket struct {
	Scope              Scope
	ScopeKey           string
	RawModelName       string
	ProviderID         *int64
	TenantID           *string
	RequestCount       int64
	SuccessCount       int64
	InputTokens        int64
	OutputTokens       int64
	CacheReadTokens    int64
	CacheWriteTokens   int64
	ErrorKindBreakdown map[string]int64
	CostCents          int64
	Currency           string
	CreditsCharged     int64
	LatencyP50Ms       int64
	LatencyP95Ms       int64
	PriceSnapshot      map[string]any
}

// RollupStats 汇报单日聚合的写入规模。
type RollupStats struct {
	Date         time.Time `json:"date"`
	RowsWritten  int64     `json:"rows_written"`
	RequestsSeen int64     `json:"requests_seen"`
}

// personUnknown 是 end_user_id 与 person_hash 双缺时的兜底人员键。
const personUnknown = "unknown"

// defaultCentsPerCredit 与 maas_settings.cents_per_credit 的 DDL 默认值一致；
// maas_settings 缺表/缺行时按此冻结，避免快照 price_snapshot 落空。
const defaultCentsPerCredit = 0.1

// RollupDay 聚合 day（UTC 日，[00:00, +24h)）的 usage_facts 写入
// report_snapshots 六个 scope；可重入（ON CONFLICT 幂等回填）。
// 迟到数据次日重跑昨日即可修正——worker 启动时先补跑昨日即依赖此语义。
func RollupDay(ctx context.Context, q Querier, day time.Time) (RollupStats, error) {
	start := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	stats := RollupStats{Date: start}

	centsPerCredit, err := loadCentsPerCredit(ctx, q)
	if err != nil {
		// maas_settings 缺失不阻塞报表（内部金额列退化为 credits 口径）。
		slog.Warn("report rollup: load maas_settings failed, freeze empty cents_per_credit", "error", err)
	}

	// ① provider 面：provider × 出站模型（全部流量类）。
	providerModelRows, _, err := queryProviderModelDay(ctx, q, start, end)
	if err != nil {
		return stats, fmt.Errorf("aggregate provider×model: %w", err)
	}

	byProvider := foldProviderBuckets(providerModelRows)

	// ② daily_total 用全流量独立聚合（含未路由到 provider 的失败行——
	//    折叠自 provider 行会漏计这部分，R65 起不再产出折叠变体，
	//    daily_total 只此一份全流量口径）。
	// RequestsSeen 只取 daily_total 全流量单行口径（各分组查询互为包含
	// 关系，逐个累加会重复计数）。
	totalRow, seen, err := queryTotalDay(ctx, q, start, end)
	if err != nil {
		return stats, fmt.Errorf("aggregate daily total: %w", err)
	}
	stats.RequestsSeen = seen

	// ③ internal 面：租户 / 人员 / 租户×模型（仅 business 流量）。
	tenantRows, _, err := queryInternalTenantDay(ctx, q, start, end, centsPerCredit)
	if err != nil {
		return stats, fmt.Errorf("aggregate internal tenant: %w", err)
	}
	personRows, _, err := queryInternalPersonDay(ctx, q, start, end, centsPerCredit)
	if err != nil {
		return stats, fmt.Errorf("aggregate internal person: %w", err)
	}
	internalModelRows, _, err := queryInternalModelDay(ctx, q, start, end, centsPerCredit)
	if err != nil {
		return stats, fmt.Errorf("aggregate internal model: %w", err)
	}

	buckets := make([]Bucket, 0, len(providerModelRows)+len(tenantRows)+len(personRows)+len(internalModelRows)+1)
	buckets = append(buckets, providerModelRows...)
	buckets = append(buckets, byProvider...)
	if totalRow != nil {
		buckets = append(buckets, *totalRow)
	}
	buckets = append(buckets, tenantRows...)
	buckets = append(buckets, personRows...)
	buckets = append(buckets, internalModelRows...)

	for i := range buckets {
		if err := upsertBucket(ctx, q, start, &buckets[i]); err != nil {
			return stats, fmt.Errorf("upsert %s/%s/%s: %w", buckets[i].Scope, buckets[i].ScopeKey, buckets[i].RawModelName, err)
		}
		stats.RowsWritten++
	}
	return stats, nil
}

// loadCentsPerCredit 读取 maas_settings 的内部积分→货币系数（冻结进快照，
// 历史报表不随后续调价漂移）。
func loadCentsPerCredit(ctx context.Context, q Querier) (float64, error) {
	var cents float64
	err := q.QueryRow(ctx, `SELECT cents_per_credit::float8 FROM maas_settings WHERE id = 1`).Scan(&cents)
	if err != nil {
		return 0, err
	}
	return cents, nil
}

// providerModelDaySQL 聚合 provider × raw_model × day（全部流量类）。
// errb CTE 先按 (provider, model, error_kind) 细分失败行再透视成
// {kind: count}，主聚合按 group 键 + breakdown 分组（breakdown 函数依赖
// 于组键，同组内恒定）。token/成本对全部终态行求和——供应商账单按实际
// 处理量出账，失败重试中被上游处理过的 token 同样计费。
const providerModelDaySQL = `
WITH f AS (
    SELECT provider_id, COALESCE(raw_model_name, '') AS raw_model_name, status, error_kind,
           prompt_tokens, completion_tokens, cache_read_tokens, cache_write_tokens,
           credits_charged, cost_amount, cost_currency, latency_ms
    FROM usage_facts
    WHERE occurred_at >= $1 AND occurred_at < $2
      AND provider_id IS NOT NULL
),
errb AS (
    SELECT provider_id, raw_model_name, jsonb_object_agg(kind, n) AS breakdown
    FROM (
        SELECT provider_id, raw_model_name,
               COALESCE(NULLIF(error_kind, ''), 'unknown') AS kind,
               COUNT(*)::bigint AS n
        FROM f
        WHERE status <> 'success'
        GROUP BY 1, 2, 3
    ) e
    GROUP BY 1, 2
)
SELECT f.provider_id,
       COALESCE(f.raw_model_name, '') AS raw_model_name,
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
    ON errb.provider_id IS NOT DISTINCT FROM f.provider_id
   AND errb.raw_model_name IS NOT DISTINCT FROM f.raw_model_name
GROUP BY f.provider_id, f.raw_model_name, errb.breakdown`

// queryProviderModelDay 执行 provider×model 日聚合；调用方随后把结果折叠
// 成 by_provider 与 daily_total（provider 面）两级行。
func queryProviderModelDay(ctx context.Context, q Querier, start, end time.Time) ([]Bucket, int64, error) {
	rows, err := q.Query(ctx, providerModelDaySQL, start, end)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var seen int64
	buckets := make([]Bucket, 0, 32)
	for rows.Next() {
		var providerID int64
		b := Bucket{Scope: ScopeDailyByModel, PriceSnapshot: map[string]any{}}
		var breakdown []byte
		if err := rows.Scan(&providerID, &b.RawModelName, &b.RequestCount, &b.SuccessCount,
			&b.InputTokens, &b.OutputTokens, &b.CacheReadTokens, &b.CacheWriteTokens,
			&b.CreditsCharged, &b.CostCents, &b.Currency,
			&b.LatencyP50Ms, &b.LatencyP95Ms, &breakdown); err != nil {
			return nil, 0, err
		}
		pid := providerID
		b.ProviderID = &pid
		b.ScopeKey = fmt.Sprintf("%d", providerID)
		if err := decodeBreakdown(breakdown, &b.ErrorKindBreakdown); err != nil {
			return nil, 0, err
		}
		seen += b.RequestCount
		buckets = append(buckets, b)
	}
	return buckets, seen, rows.Err()
}

// totalDaySQL 全流量日总计（含 provider 未落定的失败行）。单一桶，无模型
// 维度（raw_model_name = ”）。
const totalDaySQL = `
SELECT COUNT(*)::bigint,
       COUNT(*) FILTER (WHERE status = 'success')::bigint,
       COALESCE(SUM(prompt_tokens), 0)::bigint,
       COALESCE(SUM(completion_tokens), 0)::bigint,
       COALESCE(SUM(cache_read_tokens), 0)::bigint,
       COALESCE(SUM(cache_write_tokens), 0)::bigint,
       COALESCE(SUM(credits_charged), 0)::bigint,
       COALESCE(ROUND(SUM(cost_amount) * 100), 0)::bigint,
       COALESCE(MAX(cost_currency), 'USD'),
       COALESCE((SELECT jsonb_object_agg(kind, n) FROM (
            SELECT COALESCE(NULLIF(error_kind, ''), 'unknown') AS kind, COUNT(*)::bigint AS n
            FROM usage_facts
            WHERE occurred_at >= $1 AND occurred_at < $2 AND status <> 'success'
            GROUP BY 1
       ) e), '{}'::jsonb)
FROM usage_facts
WHERE occurred_at >= $1 AND occurred_at < $2`

func queryTotalDay(ctx context.Context, q Querier, start, end time.Time) (*Bucket, int64, error) {
	var b Bucket
	var breakdown []byte
	b.Scope = ScopeDailyTotal
	b.ScopeKey = "all"
	b.PriceSnapshot = map[string]any{}
	err := q.QueryRow(ctx, totalDaySQL, start, end).Scan(
		&b.RequestCount, &b.SuccessCount,
		&b.InputTokens, &b.OutputTokens, &b.CacheReadTokens, &b.CacheWriteTokens,
		&b.CreditsCharged, &b.CostCents, &b.Currency, &breakdown)
	if err != nil {
		return nil, 0, err
	}
	if err := decodeBreakdown(breakdown, &b.ErrorKindBreakdown); err != nil {
		return nil, 0, err
	}
	return &b, b.RequestCount, nil
}

// internalTenantDaySQL 租户日聚合（仅 business 流量，内部计费口径）。
const internalTenantDaySQL = `
WITH f AS (
    SELECT tenant_id, status, error_kind,
           prompt_tokens, completion_tokens, cache_read_tokens, cache_write_tokens,
           credits_charged, cost_amount, cost_currency
    FROM usage_facts
    WHERE occurred_at >= $1 AND occurred_at < $2
      AND traffic_class = 'business'
),
errb AS (
    SELECT tenant_id, jsonb_object_agg(kind, n) AS breakdown
    FROM (
        SELECT tenant_id, COALESCE(NULLIF(error_kind, ''), 'unknown') AS kind, COUNT(*)::bigint AS n
        FROM f WHERE status <> 'success' GROUP BY 1, 2
    ) e
    GROUP BY 1
)
SELECT f.tenant_id,
       COUNT(*)::bigint,
       COUNT(*) FILTER (WHERE f.status = 'success')::bigint,
       COALESCE(SUM(f.prompt_tokens), 0)::bigint,
       COALESCE(SUM(f.completion_tokens), 0)::bigint,
       COALESCE(SUM(f.cache_read_tokens), 0)::bigint,
       COALESCE(SUM(f.cache_write_tokens), 0)::bigint,
       COALESCE(SUM(f.credits_charged), 0)::bigint,
       COALESCE(ROUND(SUM(f.cost_amount) * 100), 0)::bigint,
       COALESCE(MAX(f.cost_currency), 'USD'),
       COALESCE(errb.breakdown, '{}'::jsonb)
FROM f
LEFT JOIN errb ON errb.tenant_id = f.tenant_id
GROUP BY f.tenant_id, errb.breakdown`

func queryInternalTenantDay(ctx context.Context, q Querier, start, end time.Time, centsPerCredit float64) ([]Bucket, int64, error) {
	rows, err := q.Query(ctx, internalTenantDaySQL, start, end)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var seen int64
	buckets := make([]Bucket, 0, 8)
	for rows.Next() {
		var tenant string
		b := Bucket{Scope: ScopeInternalTenant, PriceSnapshot: centsSnapshot(centsPerCredit)}
		var breakdown []byte
		if err := rows.Scan(&tenant, &b.RequestCount, &b.SuccessCount,
			&b.InputTokens, &b.OutputTokens, &b.CacheReadTokens, &b.CacheWriteTokens,
			&b.CreditsCharged, &b.CostCents, &b.Currency, &breakdown); err != nil {
			return nil, 0, err
		}
		b.ScopeKey = tenant
		b.TenantID = &tenant
		if err := decodeBreakdown(breakdown, &b.ErrorKindBreakdown); err != nil {
			return nil, 0, err
		}
		seen += b.RequestCount
		buckets = append(buckets, b)
	}
	return buckets, seen, rows.Err()
}

// internalPersonDaySQL 人员日聚合（仅 business）：身份 = end_user_id，
// 缺失回落 'person:'+person_hash（不可逆散列仍保稳定维度），双缺归
// unknown 桶。
const internalPersonDaySQL = `
WITH f AS (
    SELECT tenant_id,
           COALESCE(NULLIF(end_user_id, ''),
                    NULLIF('person:' || COALESCE(person_hash, ''), 'person:'),
                    '` + personUnknown + `') AS person,
           status, error_kind,
           prompt_tokens, completion_tokens, cache_read_tokens, cache_write_tokens,
           credits_charged, cost_amount, cost_currency
    FROM usage_facts
    WHERE occurred_at >= $1 AND occurred_at < $2
      AND traffic_class = 'business'
),
errb AS (
    SELECT tenant_id, person, jsonb_object_agg(kind, n) AS breakdown
    FROM (
        SELECT tenant_id, person, COALESCE(NULLIF(error_kind, ''), 'unknown') AS kind, COUNT(*)::bigint AS n
        FROM f WHERE status <> 'success' GROUP BY 1, 2, 3
    ) e
    GROUP BY 1, 2
)
SELECT f.tenant_id, f.person,
       COUNT(*)::bigint,
       COUNT(*) FILTER (WHERE f.status = 'success')::bigint,
       COALESCE(SUM(f.prompt_tokens), 0)::bigint,
       COALESCE(SUM(f.completion_tokens), 0)::bigint,
       COALESCE(SUM(f.cache_read_tokens), 0)::bigint,
       COALESCE(SUM(f.cache_write_tokens), 0)::bigint,
       COALESCE(SUM(f.credits_charged), 0)::bigint,
       COALESCE(ROUND(SUM(f.cost_amount) * 100), 0)::bigint,
       COALESCE(MAX(f.cost_currency), 'USD'),
       COALESCE(errb.breakdown, '{}'::jsonb)
FROM f
LEFT JOIN errb ON errb.tenant_id = f.tenant_id AND errb.person = f.person
GROUP BY f.tenant_id, f.person, errb.breakdown`

func queryInternalPersonDay(ctx context.Context, q Querier, start, end time.Time, centsPerCredit float64) ([]Bucket, int64, error) {
	rows, err := q.Query(ctx, internalPersonDaySQL, start, end)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var seen int64
	buckets := make([]Bucket, 0, 16)
	for rows.Next() {
		var tenant, person string
		b := Bucket{Scope: ScopeInternalPerson, PriceSnapshot: centsSnapshot(centsPerCredit)}
		var breakdown []byte
		if err := rows.Scan(&tenant, &person, &b.RequestCount, &b.SuccessCount,
			&b.InputTokens, &b.OutputTokens, &b.CacheReadTokens, &b.CacheWriteTokens,
			&b.CreditsCharged, &b.CostCents, &b.Currency, &breakdown); err != nil {
			return nil, 0, err
		}
		b.ScopeKey = internalPersonScopeKey(tenant, person)
		b.TenantID = &tenant
		if err := decodeBreakdown(breakdown, &b.ErrorKindBreakdown); err != nil {
			return nil, 0, err
		}
		seen += b.RequestCount
		buckets = append(buckets, b)
	}
	return buckets, seen, rows.Err()
}

// internalModelDaySQL 租户 × 出站模型日聚合（仅 business）：内部报表
// sheet2（模型质量与错误分析）的数据面。
const internalModelDaySQL = `
WITH f AS (
    SELECT tenant_id, COALESCE(raw_model_name, '') AS raw_model_name, status, error_kind,
           prompt_tokens, completion_tokens, cache_read_tokens, cache_write_tokens,
           credits_charged, cost_amount, cost_currency, latency_ms
    FROM usage_facts
    WHERE occurred_at >= $1 AND occurred_at < $2
      AND traffic_class = 'business'
),
errb AS (
    SELECT tenant_id, raw_model_name, jsonb_object_agg(kind, n) AS breakdown
    FROM (
        SELECT tenant_id, raw_model_name, COALESCE(NULLIF(error_kind, ''), 'unknown') AS kind, COUNT(*)::bigint AS n
        FROM f WHERE status <> 'success' GROUP BY 1, 2, 3
    ) e
    GROUP BY 1, 2
)
SELECT f.tenant_id, f.raw_model_name,
       COUNT(*)::bigint,
       COUNT(*) FILTER (WHERE f.status = 'success')::bigint,
       COALESCE(SUM(f.prompt_tokens), 0)::bigint,
       COALESCE(SUM(f.completion_tokens), 0)::bigint,
       COALESCE(SUM(f.cache_read_tokens), 0)::bigint,
       COALESCE(SUM(f.cache_write_tokens), 0)::bigint,
       COALESCE(SUM(f.credits_charged), 0)::bigint,
       COALESCE(ROUND(SUM(f.cost_amount) * 100), 0)::bigint,
       COALESCE(MAX(f.cost_currency), 'USD'),
       COALESCE(percentile_cont(0.5) WITHIN GROUP (ORDER BY f.latency_ms)
                FILTER (WHERE f.status = 'success'), 0)::bigint,
       COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY f.latency_ms)
                FILTER (WHERE f.status = 'success'), 0)::bigint,
       COALESCE(errb.breakdown, '{}'::jsonb)
FROM f
LEFT JOIN errb ON errb.tenant_id = f.tenant_id AND errb.raw_model_name = f.raw_model_name
GROUP BY f.tenant_id, f.raw_model_name, errb.breakdown`

func queryInternalModelDay(ctx context.Context, q Querier, start, end time.Time, centsPerCredit float64) ([]Bucket, int64, error) {
	rows, err := q.Query(ctx, internalModelDaySQL, start, end)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var seen int64
	buckets := make([]Bucket, 0, 32)
	for rows.Next() {
		var tenant string
		b := Bucket{Scope: ScopeInternalModel, PriceSnapshot: centsSnapshot(centsPerCredit)}
		var breakdown []byte
		if err := rows.Scan(&tenant, &b.RawModelName, &b.RequestCount, &b.SuccessCount,
			&b.InputTokens, &b.OutputTokens, &b.CacheReadTokens, &b.CacheWriteTokens,
			&b.CreditsCharged, &b.CostCents, &b.Currency,
			&b.LatencyP50Ms, &b.LatencyP95Ms, &breakdown); err != nil {
			return nil, 0, err
		}
		b.ScopeKey = tenant
		b.TenantID = &tenant
		if err := decodeBreakdown(breakdown, &b.ErrorKindBreakdown); err != nil {
			return nil, 0, err
		}
		seen += b.RequestCount
		buckets = append(buckets, b)
	}
	return buckets, seen, rows.Err()
}

// foldProviderBuckets 把 provider×model 行折叠成 daily_by_provider 行。
// latency 分位不可跨桶折叠，by_provider 行置 0（报表只在模型粒度展示
// 延迟，文档见设计 §3）。
func foldProviderBuckets(modelRows []Bucket) []Bucket {
	byProvider := make(map[int64]*Bucket)
	for i := range modelRows {
		m := &modelRows[i]
		if m.ProviderID == nil {
			continue
		}
		p, ok := byProvider[*m.ProviderID]
		if !ok {
			p = &Bucket{
				Scope:         ScopeDailyByProvider,
				ScopeKey:      m.ScopeKey,
				ProviderID:    m.ProviderID,
				Currency:      m.Currency,
				PriceSnapshot: map[string]any{},
			}
			byProvider[*m.ProviderID] = p
		}
		p.RequestCount += m.RequestCount
		p.SuccessCount += m.SuccessCount
		p.InputTokens += m.InputTokens
		p.OutputTokens += m.OutputTokens
		p.CacheReadTokens += m.CacheReadTokens
		p.CacheWriteTokens += m.CacheWriteTokens
		p.CreditsCharged += m.CreditsCharged
		p.CostCents += m.CostCents
		p.ErrorKindBreakdown = mergeBreakdown(p.ErrorKindBreakdown, m.ErrorKindBreakdown)
	}
	out := make([]Bucket, 0, len(byProvider))
	for _, p := range byProvider {
		out = append(out, *p)
	}
	return out
}

// upsertBucket 写一行快照，ON CONFLICT 四键幂等回填。jsonb 参数以
// string + ::text::jsonb 绑定——pgx SimpleProtocol 会把 []byte 绑成
// bytea hex，直接 cast jsonb 必撞 22P02（feedback_analyzer 同坑同修法）。
func upsertBucket(ctx context.Context, q Querier, day time.Time, b *Bucket) error {
	breakdownJSON, err := json.Marshal(orEmptyMap(b.ErrorKindBreakdown))
	if err != nil {
		return fmt.Errorf("marshal error_kind_breakdown: %w", err)
	}
	priceJSON, err := json.Marshal(orEmptyPrice(b.PriceSnapshot))
	if err != nil {
		return fmt.Errorf("marshal price_snapshot: %w", err)
	}
	successRatio := cacheHitRatio(b.CacheReadTokens, b.InputTokens)
	_, err = q.Exec(ctx, `
		INSERT INTO report_snapshots (
		    scope, scope_key, report_date, raw_model_name,
		    request_count, success_count, error_count,
		    input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
		    error_kind_breakdown, cache_hit_ratio,
		    estimated_cost_cents, currency, price_snapshot,
		    provider_id, canonical_id, tenant_id,
		    credits_charged, latency_p50_ms, latency_p95_ms, updated_at
		) VALUES (
		    $1, $2, $3, $4,
		    $5, $6, $7,
		    $8, $9, $10, $11,
		    $12::text::jsonb, $13,
		    $14, $15, $16::text::jsonb,
		    $17, $18, $19,
		    $20, $21, $22, now()
		)
		ON CONFLICT (scope, scope_key, report_date, raw_model_name) DO UPDATE SET
		    request_count = EXCLUDED.request_count,
		    success_count = EXCLUDED.success_count,
		    error_count = EXCLUDED.error_count,
		    input_tokens = EXCLUDED.input_tokens,
		    output_tokens = EXCLUDED.output_tokens,
		    cache_read_tokens = EXCLUDED.cache_read_tokens,
		    cache_write_tokens = EXCLUDED.cache_write_tokens,
		    error_kind_breakdown = EXCLUDED.error_kind_breakdown,
		    cache_hit_ratio = EXCLUDED.cache_hit_ratio,
		    estimated_cost_cents = EXCLUDED.estimated_cost_cents,
		    currency = EXCLUDED.currency,
		    price_snapshot = EXCLUDED.price_snapshot,
		    provider_id = EXCLUDED.provider_id,
		    canonical_id = EXCLUDED.canonical_id,
		    tenant_id = EXCLUDED.tenant_id,
		    credits_charged = EXCLUDED.credits_charged,
		    latency_p50_ms = EXCLUDED.latency_p50_ms,
		    latency_p95_ms = EXCLUDED.latency_p95_ms,
		    updated_at = now()
	`,
		string(b.Scope), b.ScopeKey, day, b.RawModelName,
		b.RequestCount, b.SuccessCount, b.RequestCount-b.SuccessCount,
		b.InputTokens, b.OutputTokens, b.CacheReadTokens, b.CacheWriteTokens,
		string(breakdownJSON), successRatio,
		b.CostCents, currencyOr(b.Currency), string(priceJSON),
		b.ProviderID, nil, b.TenantID,
		b.CreditsCharged, b.LatencyP50Ms, b.LatencyP95Ms,
	)
	return err
}

// cacheHitRatio = cache_read / (input + cache_read)，分母 0 返回 nil
// （表列 nullable，设计 §2.1）。cache_read_tokens 为 NULL 时按 0。
func cacheHitRatio(cacheRead, input int64) any {
	denom := input + cacheRead
	if denom <= 0 {
		return nil
	}
	return float64(cacheRead) / float64(denom)
}

func orEmptyMap(m map[string]int64) map[string]int64 {
	if m == nil {
		return map[string]int64{}
	}
	return m
}

// orEmptyPrice 同上，针对 price_snapshot（any 值类型）。
func orEmptyPrice(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

func currencyOr(c string) string {
	if c == "" {
		return "USD"
	}
	return c
}

// centsSnapshot 冻结内部价系数进 price_snapshot。
func centsSnapshot(cents float64) map[string]any {
	if cents <= 0 {
		return map[string]any{}
	}
	return map[string]any{"cents_per_credit": cents, "currency": "CNY"}
}

// decodeBreakdown 解 jsonb_object_agg 的字节流为 map；空值返回空 map。
func decodeBreakdown(raw []byte, out *map[string]int64) error {
	m := map[string]int64{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &m); err != nil {
			return fmt.Errorf("decode error_kind_breakdown %q: %w", string(raw), err)
		}
	}
	*out = m
	return nil
}

// mergeBreakdown 把 src 累加进 dst 并返回结果 map（nil 安全——map 传参
// 重绑不影响调用方，必须回传）。
func mergeBreakdown(dst, src map[string]int64) map[string]int64 {
	if dst == nil {
		dst = map[string]int64{}
	}
	for k, v := range src {
		dst[k] += v
	}
	return dst
}

// MissingRollupDates 返回 (now-lookback, now) 开区间内没有 daily_total
// 快照行的日期（UTC，升序）。daily_total 在 RollupDay 里无条件写一行
// （零流量日也是一行 COUNT=0），因此「无行」=「该日聚合从未发生」——
// worker 停机跨过钟点、单轮失败后靠本函数做有界追赶；早于部署日的
// 历史不在追赶范围（超出 lookback），需要时走管理端手动 /run。
func MissingRollupDates(ctx context.Context, q Querier, lookbackDays int, now time.Time) ([]time.Time, error) {
	if lookbackDays <= 0 {
		lookbackDays = 1
	}
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	from := today.AddDate(0, 0, -lookbackDays)
	rows, err := q.Query(ctx, `
		SELECT generate_series($1::date, $2::date, '1 day')::date AS d
		EXCEPT
		SELECT report_date FROM report_snapshots WHERE scope = 'daily_total'
	`, from, today.AddDate(0, 0, -1))
	if err != nil {
		return nil, fmt.Errorf("query missing rollup dates: %w", err)
	}
	defer rows.Close()
	var out []time.Time
	for rows.Next() {
		var d time.Time
		if err := rows.Scan(&d); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
