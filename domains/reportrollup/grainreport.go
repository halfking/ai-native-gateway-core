// grainreport.go —— 对账报表的多维读面（2026-09-29 对帐页多维筛选轮）。
//
// 与旧读面（report.go，BuildRangeReport）的根本差异：
//
//	旧：读 6 个**边缘汇总** scope，在 Go 里按 filter.empty() 的分支树折叠。
//	    分支树只能正确处理**单维**过滤——两个维度同时过滤时无任何单 scope
//	    可折叠出正确结果，总计会与分组行对不上（报表自相矛盾）。
//	新：读最细粒度 grain scope，一条 GROUPING SETS SQL 在 PG 侧一次性算出
//	    总计 / 按天 / 按天×模型 / 六个维度各自的汇总；任意维度组合过滤都
//	    从最细行折叠，「总计 = Σ 各分组」恒成立。
//
// 回落语义：某日若只有旧 scope 快照（迁移落地前写的历史），该日单独走一次
// 旧口径查询，只贡献**总计与按天**两行（维度分解在旧快照里本就不可得，
// 硬凑会给出错误维度值）。响应回传 coverage.legacy_dates 供前端披露，
// 管理端可按日回填（/run 幂等）把该日补成 grain 口径。
package reportrollup

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// GrainFilter 多维过滤（零值 = 不过滤）。provider 视角与 internal 视角
// **共用同一组过滤条件**——旧读面会按视角拒绝交叉维度（provider_id 只在
// provider 视角可用），那是对边缘 scope 的能力限制，不是业务语义；对帐人
// 同样会问「内部流量里哪些 apikey 打到了失败率高的模型」。
type GrainFilter struct {
	ProviderID   *int64
	CredentialID *int64
	APIKeyID     *int64
	TenantID     string
	Person       string
	Model        string
}

func (f GrainFilter) empty() bool {
	return f.ProviderID == nil && f.CredentialID == nil && f.APIKeyID == nil &&
		f.TenantID == "" && f.Person == "" && f.Model == ""
}

// Names 维度 id → 展示名映射。缺表/查询失败时留空，读面退化为显示 id。
type Names struct {
	Providers   map[int64]string
	Credentials map[int64]string
	APIKeys     map[int64]string
}

// CredentialRow 凭据汇总行。
type CredentialRow struct {
	CredentialID   int64            `json:"credential_id"`
	CredentialName string           `json:"credential_name"`
	ProviderID     *int64           `json:"provider_id,omitempty"`
	ProviderName   string           `json:"provider_name,omitempty"`
	Totals         Totals           `json:"totals"`
	ErrorBreakdown map[string]int64 `json:"error_breakdown"`
	QualityScore   float64          `json:"quality_score,omitempty"`
}

// APIKeyRow apikey 汇总行。
type APIKeyRow struct {
	APIKeyID       int64            `json:"api_key_id"`
	APIKeyName     string           `json:"api_key_name"`
	TenantID       string           `json:"tenant_id,omitempty"`
	Totals         Totals           `json:"totals"`
	ErrorBreakdown map[string]int64 `json:"error_breakdown"`
	QualityScore   float64          `json:"quality_score,omitempty"`
}

// DailyModelRow 按天 × 模型行（图表数据源）。
type DailyModelRow struct {
	Date           string           `json:"date"`
	RawModelName   string           `json:"raw_model_name"`
	ProviderID     *int64           `json:"provider_id,omitempty"`
	ProviderName   string           `json:"provider_name,omitempty"`
	Totals         Totals           `json:"totals"`
	ErrorBreakdown map[string]int64 `json:"error_breakdown,omitempty"`
	// 明细模式下主表会显示质量评分；缺这一项时那一列在按天明细里整列是空的。
	// 公式与汇总行同一个（ProviderQualityScore），不在前端另写一份以免两边漂移。
	QualityScore float64 `json:"quality_score,omitempty"`
}

// DailyGroupRow 按天 × 单维度行（明细导出的数据面）。
type DailyGroupRow struct {
	Date           string           `json:"date"`
	Key            string           `json:"key"`
	Name           string           `json:"name,omitempty"`
	Totals         Totals           `json:"totals"`
	ErrorBreakdown map[string]int64 `json:"error_breakdown,omitempty"`
	// 同 DailyModelRow.QualityScore：明细模式下质量评分列不能是空的。
	QualityScore float64 `json:"quality_score,omitempty"`
}

// Coverage 快照口径覆盖披露。
type Coverage struct {
	// GrainDates 已有最细粒度快照的日期（升序）。
	GrainDates []string `json:"grain_dates"`
	// LegacyDates 区间内仅有旧口径快照的日期（升序）——这些日期只贡献总计
	// 与按天两行，维度分解需回填后才可见。
	LegacyDates []string `json:"legacy_dates"`
}

// GrainReport 多维区间报表。
type GrainReport struct {
	Start  time.Time   `json:"start"`
	End    time.Time   `json:"end"`
	View   View        `json:"view"`
	Filter GrainFilter `json:"-"`

	Totals         Totals           `json:"totals"`
	ErrorBreakdown map[string]int64 `json:"error_breakdown"`
	TopErrorKind   string           `json:"top_error_kind,omitempty"`
	TopErrorCount  int64            `json:"top_error_count,omitempty"`

	Days        []DayRow        `json:"days"`
	DailyModels []DailyModelRow `json:"daily_models,omitempty"`
	Providers   []ProviderRow   `json:"providers,omitempty"`
	Credentials []CredentialRow `json:"credentials,omitempty"`
	APIKeys     []APIKeyRow     `json:"api_keys,omitempty"`
	Models      []ModelRow      `json:"models"`
	// ModelTotals 纯模型口径（跨供应商合并、不拆行）——前端「模型统计」
	// 折叠菜单与图表图例的数据源。
	ModelTotals []ModelRow  `json:"model_totals,omitempty"`
	Tenants     []TenantRow `json:"tenants,omitempty"`
	Persons     []PersonRow `json:"persons,omitempty"`

	// Daily* 明细模式（daily=true）额外返回的按天 × 维度行，供「看到详细
	// 的每天的数据」与明细导出使用；汇总模式下为 nil。
	DailyProviders   []DailyGroupRow `json:"daily_providers,omitempty"`
	DailyCredentials []DailyGroupRow `json:"daily_credentials,omitempty"`
	DailyAPIKeys     []DailyGroupRow `json:"daily_api_keys,omitempty"`
	DailyTenants     []DailyGroupRow `json:"daily_tenants,omitempty"`
	DailyPersons     []DailyGroupRow `json:"daily_persons,omitempty"`

	SnapshotDates []string `json:"snapshot_dates"`
	Coverage      Coverage `json:"coverage"`
	Source        string   `json:"source"` // "grain" | "mixed" | "legacy"
}

// ────────────────────────────────────────────────────────────────────────────
// 聚合 SQL
// ────────────────────────────────────────────────────────────────────────────

// grainGroupSets 返回本次查询要算的分组集。daily=false 时只出汇总层级；
// daily=true 追加「按天 × 单维度」层级（明细表格与明细导出的数据面）。
//
// 分组集**不带**错误类型：错误类型走独立分支（见 grainErrGroupSets），
// 度量分支只在源行上聚合一次。早期版本把 kind 塞进**每一个**分组集，并用
// `(1 - is_err)` 让 explode 出来的错误行不贡献度量——语义正确但慢到不可用：
// explode 出的行会跟着走完 10~15 个分组集 × 20 个聚合表达式，真库实测
// （本地 7 天 6336 行 grain、12% 失败）整条查询 826ms，其中 explode 分支
// 占约 700ms，而不 explode 的同参数对照只要 11.8ms（真库 EXPLAIN ANALYZE
// 变体对照，见 docs 注释）。拆成两个分支后同一查询降到十几毫秒量级。
func grainGroupSets(daily bool) []string {
	sets := []string{
		"()",                            // 区间总计
		"(report_date)",                 // 按天
		"(report_date, raw_model_name)", // 按天 × 模型（图表）
		"(provider_id)",                 // 按供应商
		"(provider_id, raw_model_name)", // 按供应商 × 模型
		"(credential_id)",               // 按凭据
		"(raw_model_name)",              // 按模型
		"(tenant_id)",                   // 按租户
		"(person)",                      // 按用户
		"(api_key_id)",                  // 按 apikey
	}
	if daily {
		sets = append(sets,
			"(report_date, provider_id)",
			"(report_date, credential_id)",
			"(report_date, tenant_id)",
			"(report_date, person)",
			"(report_date, api_key_id)",
		)
	}
	return sets
}

// grainErrGroupSets 是错误类型分支的分组集：与度量分支**逐个对应**，只是
// 末尾多一个 kind。必须一一对应——classify() 靠 GROUPING() 标志把行派发到
// 对应的层级，两者层级集合不一致会让「按供应商的主要错误」少一层或多半层。
func grainErrGroupSets(daily bool) []string {
	base := grainGroupSets(daily)
	sets := make([]string, len(base))
	for i, s := range base {
		inner := strings.TrimSuffix(strings.TrimPrefix(s, "("), ")")
		if inner == "" {
			sets[i] = "(kind)"
			continue
		}
		sets[i] = "(" + inner + ", kind)"
	}
	return sets
}

// grainAggSQL 用给定 scope 集合做多维聚合。filter 拼成参数化 WHERE
// （占位符从 $4 起），返回 SQL 文本。
//
// 错误分布的处理值得单独说明：jsonb 没有 merge 聚合，所以先把每行的
// error_kind_breakdown 用 LATERAL jsonb_each_text 拆成 (行 × 错误类型)，
// 再按 (分组键, kind) 聚合——每个 (分组, kind) 行的度量是该分组内**带这个
// 错误类型的行**的度量之和，跨 kind 求和（Go 侧 add()）即得分组总度量。
// 这比「先在 SQL 里合并 jsonb 再聚合」多一层 explode，但避免了自造 jsonb
// 合并函数，且分组行数 × 错误类型数仍然很小（每行通常 1~3 种）。
//
// price_snapshot->>'cents_per_credit' 的 cast 用正则守卫：无守卫时一个
// 非数值键值会让整条查询 22P02 失败（快照的 price_snapshot 是自由 jsonb，
// 未来任何写入方都可能塞进别的形状）。
func grainAggSQL(scopes []string, filter GrainFilter, daily bool, excludeScope Scope) string {
	// 占位符必须**按实际启用的过滤条件顺序连号**：写死 $4..$9 会在条件不全
	// 时留下悬空占位符，PG 直接 42P18「could not determine data type of
	// parameter $N」（真库实测：只开 provider+credential+tenant 三个条件时
	// $6 悬空，整条查询 500）。顺序与 runGrainAgg 的 args 追加顺序严格一致。
	var where []string
	idx := 4
	for _, f := range []struct {
		col string
		on  bool
	}{
		{"provider_id", filter.ProviderID != nil},
		{"credential_id", filter.CredentialID != nil},
		{"api_key_id", filter.APIKeyID != nil},
		{"tenant_id", filter.TenantID != ""},
		{"person", filter.Person != ""},
		{"raw_model_name", filter.Model != ""},
	} {
		if !f.on {
			continue
		}
		where = append(where, fmt.Sprintf("AND %s = $%d", f.col, idx))
		idx++
	}
	if excludeScope != "" {
		// 旧口径回落专用：排除已由 grain 口径覆盖的日期，否则同一批请求
		// 会被 grain 行与旧 scope 行重复计入总计（真库实测踩过：dev 库带着
		// 整段历史 daily_total，回落查询把区间总计放大了五倍多）。
		where = append(where, fmt.Sprintf(
			"AND report_date NOT IN (SELECT gd.report_date FROM report_snapshots gd WHERE gd.scope = $%d)", idx))
		idx++
	}
	_ = idx
	filterClause := ""
	if len(where) > 0 {
		filterClause = "\n      " + strings.Join(where, "\n      ")
	}
	// 两个分支的分组集必须一一对应（grainErrGroupSets 由 grainGroupSets 派生），
	// 否则 classify() 派发不到对应层级，「按供应商的主要错误」会少一层。
	return `
WITH f AS (
    SELECT report_date, raw_model_name, provider_id, credential_id, api_key_id,
           tenant_id, person, request_count, success_count,
           input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
           estimated_cost_cents, currency, credits_charged, price_snapshot,
           error_kind_breakdown, latency_p50_ms, latency_p95_ms
    FROM report_snapshots
    WHERE scope = ANY($1)
      AND report_date >= $2 AND report_date <= $3` + filterClause + `
),
w0 AS (
    SELECT f.*,
           CASE WHEN f.latency_p50_ms > 0 OR f.latency_p95_ms > 0
                THEN GREATEST(f.success_count, 1) ELSE 0 END AS w,
           -- cents_per_credit 的正则守卫在这里做**一次**，而不是留在外层每个
           -- 分组集的每个 SUM 里逐行重算：留在外层时它被求值 (行数 × 分组集数)
           -- 次，真库实测占 ~110ms / 826ms。守卫本身必须留——price_snapshot 是
           -- 自由 jsonb，未来任何写入方都可能塞进非数值键值，一个坏值会让整条
           -- 查询 22P02。
           CASE WHEN f.price_snapshot->>'cents_per_credit' ~ '^-?[0-9]+(\.[0-9]+)?([eE][-+]?[0-9]+)?$'
                THEN (f.price_snapshot->>'cents_per_credit')::float8 ELSE 0 END AS cpc
    FROM f
)
SELECT GROUPING(report_date) AS g_date,
       GROUPING(provider_id) AS g_provider,
       GROUPING(credential_id) AS g_credential,
       GROUPING(raw_model_name) AS g_model,
       GROUPING(tenant_id) AS g_tenant,
       GROUPING(person) AS g_person,
       GROUPING(api_key_id) AS g_key,
       report_date, provider_id, credential_id, raw_model_name,
       tenant_id, person, api_key_id, NULL::text AS kind,
       COALESCE(SUM(request_count), 0)::bigint AS request_count,
       COALESCE(SUM(success_count), 0)::bigint AS success_count,
       COALESCE(SUM(input_tokens), 0)::bigint AS input_tokens,
       COALESCE(SUM(output_tokens), 0)::bigint AS output_tokens,
       COALESCE(SUM(cache_read_tokens), 0)::bigint AS cache_read_tokens,
       COALESCE(SUM(cache_write_tokens), 0)::bigint AS cache_write_tokens,
       COALESCE(SUM(estimated_cost_cents), 0)::bigint AS cost_cents,
       COALESCE(MAX(currency), 'USD')::text AS currency,
       COALESCE(SUM(credits_charged), 0)::bigint AS credits,
       COALESCE(SUM(credits_charged * cpc), 0)::float8 AS internal_cents,
       COALESCE(SUM(latency_p50_ms * w), 0)::float8 AS p50_wsum,
       COALESCE(SUM(latency_p95_ms * w), 0)::float8 AS p95_wsum,
       COALESCE(SUM(w), 0)::float8 AS wsum,
       0::bigint AS err_kind_count
-- COALESCE(...,0) 不能省：输入为空时 PG 的 GROUPING SETS 仍会返回
-- 一行「幽灵总行」（分组键全 NULL、GROUPING() 全 1、所有 SUM 为 NULL）。
-- 区间内无数据、或某个 scope 在该区间没有行（旧口径回落查询最常见：
-- grain 已覆盖的日期全被 NOT IN 排除）时就会命中，缺了 COALESCE 直接
-- 扫描报「cannot scan NULL into *int64」，整个端点 500（真库 E2E 实测）。
FROM w0
GROUP BY GROUPING SETS (` + strings.Join(grainGroupSets(daily), ", ") + `)
UNION ALL
-- 错误类型分支：只 explode 真正带错误的行，只产出 (层级, kind, 计数)，
-- 度量列全 0。
--
-- 为什么必须独立成分支，而不是让 explode 出来的行跟着走完全部分组集：
-- explode 行会为每个分组集重跑全部 20 个聚合表达式。真库实测（本地 7 天
-- 6336 行 grain、12% 失败）整条查询 826ms，其中约 700ms 花在这里；同参数
-- 下不 explode 的对照只要 11.8ms。独立分支的输入只有带错误的行（explode 后
-- 约 1k 行），代价回到可忽略。
--
-- 计数必须取 jsonb 里的值 e.n，不能拿 request_count 顶替：一行同时有两种
-- 错误时（如 timeout 3 + rate_limit 2，request_count=25），按行请求数累加会
-- 把每种错误都算成 25（真库实测：主要错误 202 → 225）。
SELECT GROUPING(report_date) AS g_date,
       GROUPING(provider_id) AS g_provider,
       GROUPING(credential_id) AS g_credential,
       GROUPING(raw_model_name) AS g_model,
       GROUPING(tenant_id) AS g_tenant,
       GROUPING(person) AS g_person,
       GROUPING(api_key_id) AS g_key,
       report_date, provider_id, credential_id, raw_model_name,
       tenant_id, person, api_key_id, kind,
       0::bigint AS request_count,
       0::bigint AS success_count,
       0::bigint AS input_tokens,
       0::bigint AS output_tokens,
       0::bigint AS cache_read_tokens,
       0::bigint AS cache_write_tokens,
       0::bigint AS cost_cents,
       'USD'::text AS currency,
       0::bigint AS credits,
       0::float8 AS internal_cents,
       0::float8 AS p50_wsum,
       0::float8 AS p95_wsum,
       0::float8 AS wsum,
       COALESCE(SUM(kind_n), 0)::bigint AS err_kind_count
FROM (
    SELECT report_date, provider_id, credential_id, raw_model_name,
           tenant_id, person, api_key_id, e.kind,
           CASE WHEN e.n ~ '^[0-9]+$' THEN e.n::bigint ELSE 0 END AS kind_n
    FROM w0
    CROSS JOIN LATERAL jsonb_each_text(w0.error_kind_breakdown) AS e(kind, n)
) errsrc
GROUP BY GROUPING SETS (` + strings.Join(grainErrGroupSets(daily), ", ") + `)
`
}

// ────────────────────────────────────────────────────────────────────────────
// 查询与折叠
// ────────────────────────────────────────────────────────────────────────────

// grainScanRow 一行聚合结果（GROUPING 标志 + 度量）。
type grainScanRow struct {
	date         time.Time
	providerID   *int64
	credentialID *int64
	model        string
	tenant       string
	person       string
	apiKeyID     *int64
	kind         string

	requestCount  int64
	successCount  int64
	inputTokens   int64
	outputTokens  int64
	cacheRead     int64
	cacheWrite    int64
	costCents     int64
	currency      string
	credits       int64
	internalCent  float64
	p50w, p95w, w float64
	// errKindCount 是本行（分组 × kind）里**带该错误类型**的请求数。
	// 度量走 (1-is_err) 只计一次，错误分布走这一列——两者分开才不会因
	// jsonb explode 把请求数重复计（真库实测踩过：一度放大约 3 倍）。
	errKindCount int64

	gDate, gProvider, gCredential, gModel, gTenant, gPerson, gKey bool
}

// groupKind 标识一行属于哪个分组集。
type groupKind int

const (
	gkTotal groupKind = iota
	gkDay
	gkDayModel
	gkProvider
	gkProviderModel
	gkCredential
	gkModel
	gkTenant
	gkPerson
	gkAPIKey
	gkDayProvider
	gkDayCredential
	gkDayTenant
	gkDayPerson
	gkDayAPIKey
)

func classify(r grainScanRow) groupKind {
	d, p, c := !r.gDate, !r.gProvider, !r.gCredential
	m, t, u, k := !r.gModel, !r.gTenant, !r.gPerson, !r.gKey
	switch {
	case d && !p && !c && m && !t && !u && !k:
		return gkDayModel
	case d && !p && !c && !m && !t && !u && !k:
		return gkDay
	case !d && !p && !c && !m && !t && !u && !k:
		return gkTotal
	case !d && p && !c && !m && !t && !u && !k:
		return gkProvider
	case !d && p && !c && m && !t && !u && !k:
		return gkProviderModel
	case !d && !p && c && !m && !t && !u && !k:
		return gkCredential
	case !d && !p && !c && m && !t && !u && !k:
		return gkModel
	case !d && !p && !c && !m && t && !u && !k:
		return gkTenant
	case !d && !p && !c && !m && !t && u && !k:
		return gkPerson
	case !d && !p && !c && !m && !t && !u && k:
		return gkAPIKey
	case d && p && !c && !m && !t && !u && !k:
		return gkDayProvider
	case d && !p && c && !m && !t && !u && !k:
		return gkDayCredential
	case d && !p && !c && !m && t && !u && !k:
		return gkDayTenant
	case d && !p && !c && !m && !t && u && !k:
		return gkDayPerson
	case d && !p && !c && !m && !t && !u && k:
		return gkDayAPIKey
	}
	return gkTotal // 不可达：分组集枚举封闭；兜底避免空结构体静默丢行
}

// grainAccumulator 单个分组的度量累加器（跨 kind 累加）。
type grainAccumulator struct {
	requestCount, successCount int64
	inputTokens, outputTokens  int64
	cacheRead, cacheWrite      int64
	costCents, credits         int64
	internalCents              float64
	currency                   string
	p50w, p95w, w              float64
	br                         map[string]int64
}

func (a *grainAccumulator) add(r grainScanRow) {
	a.requestCount += r.requestCount
	a.successCount += r.successCount
	a.inputTokens += r.inputTokens
	a.outputTokens += r.outputTokens
	a.cacheRead += r.cacheRead
	a.cacheWrite += r.cacheWrite
	a.costCents += r.costCents
	a.credits += r.credits
	a.internalCents += r.internalCent
	if r.currency != "" {
		a.currency = r.currency
	}
	a.p50w += r.p50w
	a.p95w += r.p95w
	a.w += r.w
	if r.kind != "" && r.errKindCount > 0 {
		a.br = mergeBreakdown(a.br, map[string]int64{r.kind: r.errKindCount})
	}
}

// finalize 补齐派生列。分位用成功请求数加权还原（分位数不可跨行折叠，
// 与旧读面 acc.finalize 同款近似）。
func (a *grainAccumulator) finalize() Totals {
	t := Totals{
		RequestCount:       a.requestCount,
		SuccessCount:       a.successCount,
		InputTokens:        a.inputTokens,
		OutputTokens:       a.outputTokens,
		CacheReadTokens:    a.cacheRead,
		CacheWriteTokens:   a.cacheWrite,
		EstimatedCostCents: a.costCents,
		CreditsCharged:     a.credits,
		InternalCostCents:  a.internalCents,
		Currency:           currencyOr(a.currency),
	}
	t.ErrorCount = t.RequestCount - t.SuccessCount
	if t.RequestCount > 0 {
		t.ErrorRate = float64(t.ErrorCount) / float64(t.RequestCount)
	}
	t.TotalTokens = t.InputTokens + t.OutputTokens + t.CacheReadTokens + t.CacheWriteTokens
	if denom := t.InputTokens + t.CacheReadTokens; denom > 0 {
		r := float64(t.CacheReadTokens) / float64(denom)
		t.CacheHitRatio = &r
	}
	if a.internalCents > 0 {
		t.InternalCurrency = "CNY"
	}
	if a.w > 0 {
		t.LatencyP50Ms = a.p50w / a.w
		t.LatencyP95Ms = a.p95w / a.w
	}
	return t
}

// breakdown 返回累加出的错误分布（永不为 nil）。
func (a *grainAccumulator) breakdown() map[string]int64 {
	if a.br == nil {
		return map[string]int64{}
	}
	return a.br
}

// runGrainAgg 执行一次多维聚合并把每一行折叠进对应分组。
// filterArgCount 返回过滤条件占用的参数个数（占位符 $4 起）。
func filterArgCount(f GrainFilter) int {
	n := 0
	for _, on := range []bool{f.ProviderID != nil, f.CredentialID != nil, f.APIKeyID != nil,
		f.TenantID != "", f.Person != "", f.Model != ""} {
		if on {
			n++
		}
	}
	return n
}

func runGrainAgg(ctx context.Context, q Querier, scopes []Scope, start, end time.Time, filter GrainFilter, daily bool, excludeScope Scope, sink func(groupKind, grainScanRow)) error {
	names := make([]string, len(scopes))
	for i, s := range scopes {
		names[i] = string(s)
	}
	args := []any{names, start, end}
	if filter.ProviderID != nil {
		args = append(args, *filter.ProviderID)
	}
	if filter.CredentialID != nil {
		args = append(args, *filter.CredentialID)
	}
	if filter.APIKeyID != nil {
		args = append(args, *filter.APIKeyID)
	}
	if filter.TenantID != "" {
		args = append(args, filter.TenantID)
	}
	if filter.Person != "" {
		args = append(args, filter.Person)
	}
	if filter.Model != "" {
		args = append(args, filter.Model)
	}
	if excludeScope != "" {
		args = append(args, string(excludeScope))
	}
	rows, err := q.Query(ctx, grainAggSQL(names, filter, daily, excludeScope), args...)
	if err != nil {
		return fmt.Errorf("aggregate report_snapshots: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		r, err := scanGrainRow(rows)
		if err != nil {
			return err
		}
		sink(classify(r), r)
	}
	return rows.Err()
}

func scanGrainRow(rows pgx.Rows) (grainScanRow, error) {
	var (
		r                                     grainScanRow
		date                                  sql.NullTime
		providerID, credentialID, apiKeyID    sql.NullInt64
		model, tenant, person, kind, currency sql.NullString
		gDate, gProv, gCred, gModel           int
		gTenant, gPerson, gKey                int
		p50w, p95w, w, internalCents          float64
		errKindCount                          int64
	)
	if err := rows.Scan(
		&gDate, &gProv, &gCred, &gModel, &gTenant, &gPerson, &gKey,
		&date, &providerID, &credentialID, &model, &tenant, &person, &apiKeyID, &kind,
		&r.requestCount, &r.successCount,
		&r.inputTokens, &r.outputTokens, &r.cacheRead, &r.cacheWrite,
		&r.costCents, &currency, &r.credits, &internalCents,
		&p50w, &p95w, &w, &errKindCount,
	); err != nil {
		return r, fmt.Errorf("scan grain aggregate: %w", err)
	}
	r.gDate, r.gProvider, r.gCredential = gDate == 1, gProv == 1, gCred == 1
	r.gModel, r.gTenant, r.gPerson, r.gKey = gModel == 1, gTenant == 1, gPerson == 1, gKey == 1
	if date.Valid {
		r.date = date.Time
	}
	r.providerID = nullInt(providerID)
	r.credentialID = nullInt(credentialID)
	r.apiKeyID = nullInt(apiKeyID)
	r.model, r.tenant, r.person = model.String, tenant.String, person.String
	r.kind = kind.String
	r.currency = currency.String
	r.internalCent = internalCents
	r.p50w, r.p95w, r.w = p50w, p95w, w
	r.errKindCount = errKindCount
	return r, nil
}

// UnassignedID 是「该维度未落定」的行在分组表里的哨兵 id。usage_facts 里
// 未路由到 provider 的失败行（限流、无候选节点等）没有 provider_id /
// credential_id——本地真库实测这批占了单日请求的大头（09-23：1665 行 grain
// 里 754 行未落定，11 万/12.1 万请求）。若让它们在分组里直接消失，
// 「Σ 各分组 = 总计」这条对帐人不校验就会自证的等式就断了，所以显式成档。
const UnassignedID int64 = -1

// dimKey 把可空维度列折成分组键（NULL → 哨兵）。
func dimKey(v *int64) int64 {
	if v == nil {
		return UnassignedID
	}
	return *v
}

func nullInt(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	out := v.Int64
	return &out
}

// legacyTotalScope 旧口径回落的**单一** scope：六个旧 scope 是互相包含的
// 边缘汇总，一起加会重复计数。provider 面取 daily_total（唯一覆盖全流量
// 含未落定 provider 的失败行的口径），internal 面取 internal_tenant
// （business 流量的完整分区）。
func legacyTotalScope(view View) Scope {
	if view == ViewInternal {
		return ScopeInternalTenant
	}
	return ScopeDailyTotal
}

// BuildGrainReport 组装多维区间报表。
//
// daily=false 只出汇总层级（总计 / 按天 / 按天×模型 / 各维度汇总），前端
// 默认走这条——响应行数与区间天数基本无关。daily=true 追加「按天 × 维度」
// 明细行，供明细表格与明细导出。
func BuildGrainReport(ctx context.Context, q Querier, start, end time.Time, view View, filter GrainFilter, names Names, daily bool) (*GrainReport, error) {
	if !view.Valid() {
		return nil, fmt.Errorf("unknown view %q", view)
	}
	day0 := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.UTC)
	end0 := time.Date(end.Year(), end.Month(), end.Day(), 0, 0, 0, 0, time.UTC)
	if end0.Before(day0) {
		return nil, fmt.Errorf("end date before start date")
	}
	grainScope, _, err := GrainScopes(view)
	if err != nil {
		return nil, err
	}

	total := &grainAccumulator{}
	days := map[string]*grainAccumulator{}
	dayModels := map[string]*grainAccumulator{}
	providers := map[int64]*grainAccumulator{}
	providerModels := map[string]*grainAccumulator{}
	credentials := map[int64]*grainAccumulator{}
	models := map[string]*grainAccumulator{}
	tenants := map[string]*grainAccumulator{}
	persons := map[string]*grainAccumulator{}
	apiKeys := map[int64]*grainAccumulator{}
	dayProviders := map[string]*grainAccumulator{}
	dayCredentials := map[string]*grainAccumulator{}
	dayTenants := map[string]*grainAccumulator{}
	dayPersons := map[string]*grainAccumulator{}
	dayAPIKeys := map[string]*grainAccumulator{}

	// meta 记每组的第一行维度值（度量累加器本身不含维度字段）。
	metaProviderModel := map[string]grainScanRow{}
	metaCredential := map[int64]grainScanRow{}
	metaAPIKey := map[int64]grainScanRow{}
	metaDayModel := map[string]grainScanRow{}

	legacyDates := map[string]bool{}
	grainDates := map[string]bool{}

	// sink 把一行折叠进它所属的分组。grainOnly=true 时（第一次查询）额外
	// 记录覆盖日期。
	sink := func(grainOnly bool) func(groupKind, grainScanRow) {
		return func(k groupKind, r grainScanRow) {
			date := ""
			if !r.gDate {
				date = r.date.Format("2006-01-02")
			}
			switch k {
			case gkTotal:
				total.add(r)
			case gkDay:
				// 覆盖日期只能从按天行读——(kind) 总计行的 report_date 已被
				// GROUPING 折叠成 NULL（回落查询同理）。
				grainDates[date] = true
				getAcc(days, date).add(r)
			case gkDayModel:
				key := date + "\x00" + r.model
				getAccStr(dayModels, key).add(r)
				if _, ok := metaDayModel[key]; !ok {
					metaDayModel[key] = r
				}
			case gkProvider:
				getAccInt(providers, dimKey(r.providerID)).add(r)
			case gkProviderModel:
				key := strconv.FormatInt(int64(derefI(r.providerID)), 10) + "\x00" + r.model
				getAccStr(providerModels, key).add(r)
				if _, ok := metaProviderModel[key]; !ok {
					metaProviderModel[key] = r
				}
			case gkCredential:
				ck := dimKey(r.credentialID)
				getAccInt(credentials, ck).add(r)
				if _, ok := metaCredential[ck]; !ok {
					metaCredential[ck] = r
				}
			case gkModel:
				getAccStr(models, r.model).add(r)
			case gkTenant:
				getAccStr(tenants, r.tenant).add(r)
			case gkPerson:
				getAccStr(persons, personKey(r.tenant, r.person)).add(r)
			case gkAPIKey:
				ak := dimKey(r.apiKeyID)
				getAccInt(apiKeys, ak).add(r)
				if _, ok := metaAPIKey[ak]; !ok {
					metaAPIKey[ak] = r
				}
			case gkDayProvider:
				getAccStr(dayProviders, date+"\x00"+strconv.FormatInt(dimKey(r.providerID), 10)).add(r)
			case gkDayCredential:
				getAccStr(dayCredentials, date+"\x00"+strconv.FormatInt(dimKey(r.credentialID), 10)).add(r)
			case gkDayTenant:
				getAccStr(dayTenants, date+"\x00"+r.tenant).add(r)
			case gkDayPerson:
				getAccStr(dayPersons, date+"\x00"+personKey(r.tenant, r.person)).add(r)
			case gkDayAPIKey:
				getAccStr(dayAPIKeys, date+"\x00"+strconv.FormatInt(dimKey(r.apiKeyID), 10)).add(r)
			}
			if !grainOnly {
				if date != "" {
					legacyDates[date] = true
				}
			}
		}
	}

	// ① 最细粒度口径（迁移后的每日聚合）。
	if err := runGrainAgg(ctx, q, []Scope{grainScope}, day0, end0, filter, daily, "", sink(true)); err != nil {
		return nil, err
	}
	// ② 旧口径回落：区间内没有任何 grain 快照的日期，只贡献总计与按天。
	if err := runGrainAgg(ctx, q, []Scope{legacyTotalScope(view)}, day0, end0, filter, false, grainScope,
		legacySink(total, days, legacyDates)); err != nil {
		return nil, err
	}

	rep := &GrainReport{
		Start:          day0,
		End:            end0,
		View:           view,
		Filter:         filter,
		Totals:         total.finalize(),
		ErrorBreakdown: total.breakdown(),
		SnapshotDates:  []string{},
		Coverage:       Coverage{GrainDates: []string{}, LegacyDates: []string{}},
	}
	rep.TopErrorKind, rep.TopErrorCount = topErrorKind(rep.ErrorBreakdown)

	// 按天：补齐区间内无快照的日期（图表 x 轴需要连续轴；0 行也是有效信息）。
	for d := day0; !d.After(end0); d = d.AddDate(0, 0, 1) {
		key := d.Format("2006-01-02")
		a := days[key]
		if a == nil {
			a = &grainAccumulator{}
		}
		rep.Days = append(rep.Days, DayRow{Date: key, Totals: a.finalize(), ErrorBreakdown: a.breakdown()})
		if grainDates[key] || legacyDates[key] {
			rep.SnapshotDates = append(rep.SnapshotDates, key)
		}
	}
	sort.Slice(rep.Days, func(i, j int) bool { return rep.Days[i].Date < rep.Days[j].Date })

	// 按天 × 模型（图表数据源）。
	dayModelKeys := sortedKeysStr(dayModels)
	for _, key := range dayModelKeys {
		a := dayModels[key]
		meta := metaDayModel[key]
		date, model := splitKey(key)
		tot := a.finalize()
		row := DailyModelRow{Date: date, RawModelName: model, ProviderID: meta.providerID,
			Totals: tot, ErrorBreakdown: a.breakdown(), QualityScore: ProviderQualityScore(tot)}
		if meta.providerID != nil {
			row.ProviderName = names.Providers[*meta.providerID]
		}
		rep.DailyModels = append(rep.DailyModels, row)
	}

	// 各维度汇总。
	for _, pid := range sortedKeysInt(providers) {
		t := providers[pid].finalize()
		rep.Providers = append(rep.Providers, ProviderRow{
			ProviderID:     pid,
			ProviderName:   names.Providers[pid],
			Totals:         t,
			ErrorBreakdown: providers[pid].breakdown(),
			QualityScore:   ProviderQualityScore(t),
		})
	}
	sort.Slice(rep.Providers, func(i, j int) bool {
		return rep.Providers[i].Totals.RequestCount > rep.Providers[j].Totals.RequestCount
	})

	for _, key := range sortedKeysStr(providerModels) {
		a := providerModels[key]
		meta := metaProviderModel[key]
		_, model := splitKey(key)
		tot := a.finalize()
		row := ModelRow{RawModelName: model, Totals: tot, ErrorBreakdown: a.breakdown(),
			QualityScore: ProviderQualityScore(tot)}
		if meta.providerID != nil {
			pid := *meta.providerID
			row.ProviderID = &pid
			row.ProviderName = names.Providers[pid]
		}
		rep.Models = append(rep.Models, row)
	}
	sort.Slice(rep.Models, func(i, j int) bool {
		return rep.Models[i].Totals.RequestCount > rep.Models[j].Totals.RequestCount
	})

	// 纯模型口径（跨供应商合并）单独成表 ModelTotals：它才是「整个模型的
	// 统计列表」的唯一口径（同一模型名可能被多个供应商承载，Models 的
	// 供应商×模型行会把它拆成多行，前端模型清单菜单必须用不拆分的这份）。
	for _, model := range sortedKeysStr(models) {
		tot := models[model].finalize()
		rep.ModelTotals = append(rep.ModelTotals, ModelRow{
			RawModelName:   model,
			Totals:         tot,
			ErrorBreakdown: models[model].breakdown(),
			QualityScore:   ProviderQualityScore(tot),
		})
	}
	sort.Slice(rep.ModelTotals, func(i, j int) bool {
		return rep.ModelTotals[i].Totals.RequestCount > rep.ModelTotals[j].Totals.RequestCount
	})

	for _, cid := range sortedKeysInt(credentials) {
		a := credentials[cid]
		tot := a.finalize()
		row := CredentialRow{
			CredentialID:   cid,
			CredentialName: names.Credentials[cid],
			Totals:         tot,
			ErrorBreakdown: a.breakdown(),
			QualityScore:   ProviderQualityScore(tot),
		}
		if meta := metaCredential[cid]; meta.providerID != nil {
			pid := *meta.providerID
			row.ProviderID = &pid
			row.ProviderName = names.Providers[pid]
		}
		rep.Credentials = append(rep.Credentials, row)
	}
	sort.Slice(rep.Credentials, func(i, j int) bool {
		return rep.Credentials[i].Totals.RequestCount > rep.Credentials[j].Totals.RequestCount
	})

	for _, kid := range sortedKeysInt(apiKeys) {
		a := apiKeys[kid]
		tot := a.finalize()
		row := APIKeyRow{
			APIKeyID:       kid,
			APIKeyName:     names.APIKeys[kid],
			Totals:         tot,
			ErrorBreakdown: a.breakdown(),
			QualityScore:   ProviderQualityScore(tot),
		}
		row.TenantID = metaAPIKey[kid].tenant
		rep.APIKeys = append(rep.APIKeys, row)
	}
	sort.Slice(rep.APIKeys, func(i, j int) bool {
		return rep.APIKeys[i].Totals.RequestCount > rep.APIKeys[j].Totals.RequestCount
	})

	for _, tenant := range sortedKeysStr(tenants) {
		tot := tenants[tenant].finalize()
		rep.Tenants = append(rep.Tenants, TenantRow{TenantID: tenant, Totals: tot,
			ErrorBreakdown: tenants[tenant].breakdown(), QualityScore: ProviderQualityScore(tot)})
	}
	sort.Slice(rep.Tenants, func(i, j int) bool {
		return rep.Tenants[i].Totals.RequestCount > rep.Tenants[j].Totals.RequestCount
	})

	personKeys := sortedKeysStr(persons)
	for _, key := range personKeys {
		tenant, person := splitKey(key)
		tot := persons[key].finalize()
		rep.Persons = append(rep.Persons, PersonRow{
			TenantID:       tenant,
			Person:         person,
			Totals:         tot,
			ErrorBreakdown: persons[key].breakdown(),
			QualityScore:   ProviderQualityScore(tot),
		})
	}
	sort.Slice(rep.Persons, func(i, j int) bool {
		return rep.Persons[i].Totals.RequestCount > rep.Persons[j].Totals.RequestCount
	})

	if daily {
		rep.DailyProviders = dailyGroups(dayProviders, names.Providers, func(key string) (string, string) {
			d, k := splitKey(key)
			return d, dailyDimName(names.Providers, k)
		})
		rep.DailyCredentials = dailyGroups(dayCredentials, names.Credentials, func(key string) (string, string) {
			d, k := splitKey(key)
			return d, dailyDimName(names.Credentials, k)
		})
		rep.DailyTenants = dailyGroups(dayTenants, nil, func(key string) (string, string) {
			d, k := splitKey(key)
			return d, k
		})
		rep.DailyPersons = dailyGroups(dayPersons, nil, func(key string) (string, string) {
			d, k := splitKey(key)
			return d, k
		})
		rep.DailyAPIKeys = dailyGroups(dayAPIKeys, names.APIKeys, func(key string) (string, string) {
			d, k := splitKey(key)
			return d, dailyDimName(names.APIKeys, k)
		})
	}

	for d := range grainDates {
		rep.Coverage.GrainDates = append(rep.Coverage.GrainDates, d)
	}
	for d := range legacyDates {
		// 已在 grain 口径覆盖的日期不是「回落日期」。
		if grainDates[d] {
			continue
		}
		rep.Coverage.LegacyDates = append(rep.Coverage.LegacyDates, d)
	}
	sort.Strings(rep.Coverage.GrainDates)
	sort.Strings(rep.Coverage.LegacyDates)
	switch {
	case len(rep.Coverage.LegacyDates) > 0 && len(rep.Coverage.GrainDates) > 0:
		rep.Source = "mixed"
	case len(rep.Coverage.LegacyDates) > 0:
		rep.Source = "legacy"
	default:
		rep.Source = "grain"
	}
	return rep, nil
}

// legacySink 旧口径回落行的落点：只有总计与按天有值，维度分组不接受
// （旧快照里凭据/apikey/用户维度本就不可得，硬凑等于给错误维度值）。
func legacySink(total *grainAccumulator, days map[string]*grainAccumulator, legacyDates map[string]bool) func(groupKind, grainScanRow) {
	return func(k groupKind, r grainScanRow) {
		switch k {
		case gkTotal:
			total.add(r)
		case gkDay:
			// 覆盖日期只能从按天行读——(kind) 总计行的 report_date 已被
			// GROUPING 折叠成 NULL（grain 查询的覆盖日期同理）。
			legacyDates[r.date.Format("2006-01-02")] = true
			getAccStr(days, r.date.Format("2006-01-02")).add(r)
		}
	}
}

// dailyDimName 解析 id 维度（供应商/凭据/apikey）在**日行**里的取值。
//
// 为什么不是直接取 names[id]：未落定哨兵（UnassignedID）在名字表里查不到，
// 取出来是空串。前端 dayRows 拿这个值去 nameOf 里查汇总行，汇总行的 key 是
// "-1"，空串永远查不到 → 回落成空 → **按天明细里近两成的行名称列是空的**
// （真库实测 36 行里 7 行）。所以哨兵这里返回 "-1"，让前端能命中汇总那条
// 哨兵行、由 idText 显示「未落定」。
func dailyDimName(names map[int64]string, k string) string {
	id := parseInt64(k)
	if name := names[id]; name != "" {
		return name
	}
	if id == UnassignedID {
		return strconv.FormatInt(UnassignedID, 10)
	}
	return ""
}

func dailyGroups(m map[string]*grainAccumulator, _ map[int64]string, name func(string) (string, string)) []DailyGroupRow {
	keys := sortedKeysStr(m)
	out := make([]DailyGroupRow, 0, len(keys))
	for _, k := range keys {
		date, key := name(k)
		tot := m[k].finalize()
		out = append(out, DailyGroupRow{Date: date, Key: key, Totals: tot,
			ErrorBreakdown: m[k].breakdown(), QualityScore: ProviderQualityScore(tot)})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Date != out[j].Date {
			return out[i].Date < out[j].Date
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// topErrorKind 返回次数最多的错误类型（并列时取字典序，保证同一份数据
// 多次导出列出的「主要错误类型」稳定）。
func topErrorKind(br map[string]int64) (string, int64) {
	best, bestN := "", int64(0)
	for k, v := range br {
		if v > bestN || (v == bestN && best != "" && k < best) {
			best, bestN = k, v
		}
	}
	return best, bestN
}

func getAcc(m map[string]*grainAccumulator, key string) *grainAccumulator {
	a := m[key]
	if a == nil {
		a = &grainAccumulator{}
		m[key] = a
	}
	return a
}

func getAccStr(m map[string]*grainAccumulator, key string) *grainAccumulator { return getAcc(m, key) }
func getAccInt(m map[int64]*grainAccumulator, key int64) *grainAccumulator {
	a := m[key]
	if a == nil {
		a = &grainAccumulator{}
		m[key] = a
	}
	return a
}

func sortedKeysStr(m map[string]*grainAccumulator) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedKeysInt(m map[int64]*grainAccumulator) []int64 {
	keys := make([]int64, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	return keys
}

// splitKey 拆 "<date>\x00<key>" 复合键（\x00 是 PG 非法字节，只存在于内存
// map 键，不会进 TEXT 列）。
func splitKey(k string) (string, string) {
	if i := strings.IndexByte(k, 0); i >= 0 {
		return k[:i], k[i+1:]
	}
	return k, ""
}

// personKey 人员分组键：跨租户同名人员必须可区分（internal_person 同款
// 教训——四键 UNIQUE 不含 tenant_id，裸 person 键会互相覆盖）。
func personKey(tenant, person string) string {
	return tenant + "\x00" + person
}

func parseInt64(s string) int64 {
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}
	return v
}
