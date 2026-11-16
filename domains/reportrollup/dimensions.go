// dimensions.go —— 筛选栏候选项（2026-09-29 对帐页多维筛选轮；2026-11-16 审计轮补全）。
//
// 候选来自快照本身（grain 口径 + 旧口径非 grain 日期的分区 scope），不回扫
// usage_facts——与主读面同一数据源，避免「报表能筛但下拉框没有」的错位。
//
// 与当前筛选条件**无关**是刻意选择：若候选按已选条件收窄，选中一个供应商后
// 其它供应商会从下拉里消失，用户无法横向切换（筛选栏最常见的自锁坑）。
// 候选只受日期区间与视角约束。
//
// ── 2026-11-16 审计轮补的三处「数据丢失」──
//
//  1. **未分配桶（provider_id IS NULL）曾经整桶消失**：报告读面把 NULL 映射成
//     哨兵 -1 并显示成一行（真库实测这一桶占全量流量 30.8%——没路由到
//     provider 的探针/限流/超时失败行），而候选查询用 `IS NOT NULL` 把它滤
//     掉了 ⇒ 页面能看见这一行却**筛不出来**。现在候选与读面用同一个键（-1）。
//
//  2. **零流量供应商曾经不可见**：候选只来自快照 ⇒ 只有区间内有流量的供应商
//     才进下拉框。一个「开着却一单没有」的供应商（配错/被限流/模型没接上）
//     是审计最该看见的信号，它恰恰被藏起来了。现在把 providers 表里
//     在册的供应商全部并入候选，请求数为 0，排在有请求的之后。
//
//  3. **静默截断**：旧实现按 500 项硬砍且不告诉任何人——真库 30 天就有 527
//     个模型名，被砍掉的正是「有请求但请求少」的那批。现在上限提高、并在
//     Truncated 字段里**显式回报**砍掉了几项，前端据此披露。
package reportrollup

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"time"
)

// DimensionOption 一个候选值 + 其在区间内的请求数（前端按请求数降序，
// 默认折叠长尾）。
type DimensionOption struct {
	Key      string `json:"key"`
	Name     string `json:"name,omitempty"`
	Requests int64  `json:"requests"`
}

// DimensionOptions 六维候选集合。
type DimensionOptions struct {
	Providers   []DimensionOption `json:"providers"`
	Credentials []DimensionOption `json:"credentials"`
	APIKeys     []DimensionOption `json:"api_keys"`
	Models      []DimensionOption `json:"models"`
	Tenants     []DimensionOption `json:"tenants"`
	Persons     []DimensionOption `json:"persons"`
	// Truncated 记录被硬上限砍掉的候选项数（维度名 → 砍掉几项）。
	// 没有维度被截断时整个字段省略——前端据此披露「还有 N 项未显示」，
	// 不再让「数据不见了」变成静默事实。
	Truncated map[string]int `json:"truncated,omitempty"`
}

// maxDimensionOptions 单维候选上限。
//
// 旧值 500 是按「30 天 527 个模型名」的量级随手定的，实测会真砍——砍掉的
// 正是请求数靠后的长尾，而长尾里**有请求**的项对审计是有意义的。现在提到
// 5000：366 天区间的模型名量级（目录里 raw 名共 1740 个）落在上限内，
// 真正超限时由 Truncated 显式回报，不静默。
const maxDimensionOptions = 5000

// dimensionColumns 候选维度 → 快照列的映射（六个分支共用同一份源查询）。
//
// textual 决定要不要再加 `AND col <> ”`：三个 id 列是 bigint，拿空串比较
// 会让 PG 尝试把 ” cast 成 bigint 直接 22P02（真库实测）。id 列不可能是
// 空串，文本列才需要这条。
//
// keyExpr 非空时用它替代 `s.<col>::text` 作为候选键；nullKey 非空时
// **不排除** NULL 行，而是把它们归到这个键下（provider 维度的「未分配」
// 桶用 '-1'，与报告读面的 UnassignedID 哨兵同键）。
type dimensionColumn struct {
	dim     string
	col     string
	textual bool
	keyExpr string
	nullKey string
}

var dimensionColumns = []dimensionColumn{
	{"provider", "provider_id", false, "COALESCE(s.provider_id::text, '-1')", "-1"},
	{"credential", "credential_id", false, "", ""},
	{"api_key", "api_key_id", false, "", ""},
	{"model", "raw_model_name", true, "", ""},
	{"tenant", "tenant_id", true, "", ""},
	{"person", "person", true, "", ""},
}

// dimensionBranchSQL 拼单个维度的 UNION ALL 分支。
//
// 单独拆出来是为了让「分支形态」可被测试直接断言（而不是靠整条 SQL 的
// 字符串里有没有某段字面量）——2026-11-16 审计轮里，未分配桶消失正是
// 「provider 分支多了个 IS NOT NULL」这一处改动造成的。
func dimensionBranchSQL(d dimensionColumn, src string) string {
	keyExpr := d.keyExpr
	if keyExpr == "" {
		keyExpr = fmt.Sprintf("s.%s::text", d.col)
	}
	// WHERE 子句：nullKey 非空 ⇒ NULL 行也要出候选（未分配桶）；
	// 否则排除 NULL（id 列永远有值，留着只会多一行空桶）。
	where := ""
	if d.nullKey == "" {
		where = fmt.Sprintf(" WHERE s.%s IS NOT NULL", d.col)
		if d.textual {
			where += fmt.Sprintf(" AND s.%s <> ''", d.col)
		}
	}
	// 四个动词全部写显式索引：Go 的 fmt 在用过 %[n]s 之后，后续隐式
	// %s 会**从 n+1 继续数**，于是这里第 4 个 %s 会拿到参数 3（src），
	// 把整段源查询再拼一遍——真库 42601 才暴露出来。
	return fmt.Sprintf(
		`SELECT '%[1]s'::text AS dim, %[2]s AS key, SUM(s.request_count)::bigint AS requests
		   FROM (%[3]s) s%[4]s
		  GROUP BY 2`, d.dim, keyExpr, src, where)
}

// LoadDimensionOptions 拉区间内各维度实际出现过的候选值。
//
// 数据面 = grain scope ∪（无 grain 行的日期上的旧口径分区 scope）。旧口径
// 分区 scope 由 grainLegacyScopeForView 给出（provider 面 daily_total /
// internal 面 internal_tenant）——六个旧 scope 互相包含，一起加会让候选
// 请求数翻倍，所以只取单一分区。两者按日期互斥，不重复。
func LoadDimensionOptions(ctx context.Context, q Querier, view View, start, end time.Time) (*DimensionOptions, error) {
	grainScope, _, err := GrainScopes(view)
	if err != nil {
		return nil, err
	}
	legacyScope := grainLegacyScopeForView(view)

	// 两个来源按日期互斥：grain 行全取；旧口径行只取「该日没有 grain 行」的
	// 日期。排除条件必须**只加在旧口径分支上**——加在整个源上会把 grain 行
	// 一起删掉，候选直接全空（真库实测踩过）。
	src := `
    SELECT raw_model_name, provider_id, credential_id, api_key_id,
           tenant_id, person, request_count
      FROM report_snapshots
     WHERE scope = $1 AND report_date >= $3 AND report_date <= $4
    UNION ALL
    SELECT raw_model_name, provider_id, credential_id, api_key_id,
           tenant_id, person, request_count
      FROM report_snapshots
     WHERE scope = $2 AND report_date >= $3 AND report_date <= $4
       AND report_date NOT IN (SELECT gd.report_date FROM report_snapshots gd WHERE gd.scope = $5)`
	args := []any{string(grainScope), string(legacyScope), start, end, string(grainScope)}

	// 每个维度一条 UNION ALL 分支：PG 各分支独立 GROUP BY，每维返回的行数
	// 就是候选数本身（几十~几千行），比拉全量明细再在 Go 里聚合便宜一个
	// 数量级。
	branches := make([]string, 0, len(dimensionColumns))
	for _, d := range dimensionColumns {
		branches = append(branches, dimensionBranchSQL(d, src))
	}

	full := "SELECT dim, key, requests FROM (" + unionAll(branches) + ") u"
	if path := os.Getenv("REPORT_DUMP_SQL"); path != "" {
		_ = os.WriteFile(path, []byte(full), 0o600)
	}
	rows, err := q.Query(ctx, full, args...)
	if err != nil {
		return nil, fmt.Errorf("load dimension options: %w", err)
	}
	defer rows.Close()

	buckets := map[string][]DimensionOption{}
	for rows.Next() {
		var dim, key string
		var requests int64
		if err := rows.Scan(&dim, &key, &requests); err != nil {
			return nil, fmt.Errorf("scan dimension option: %w", err)
		}
		buckets[dim] = append(buckets[dim], DimensionOption{Key: key, Requests: requests})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := &DimensionOptions{}
	truncated := map[string]int{}
	assign := func(dst *[]DimensionOption, dim string) {
		list := buckets[dim]
		sortDimensionOptions(list)
		if len(list) > maxDimensionOptions {
			truncated[dim] = len(list) - maxDimensionOptions
			list = list[:maxDimensionOptions]
		}
		*dst = list
	}
	assign(&out.Providers, "provider")
	assign(&out.Credentials, "credential")
	assign(&out.APIKeys, "api_key")
	assign(&out.Models, "model")
	assign(&out.Tenants, "tenant")
	assign(&out.Persons, "person")

	// 供应商候选补全：把「在册但区间内零流量」的供应商并进来（requests=0）。
	//
	// 为什么必须补：候选只来自快照 ⇒ 只有区间内有流量的供应商才进下拉框。
	// 一个「开着却一单没有」的供应商（配错 / 被限流 / 模型没接上）是审计最该
	// 看见的信号，而它恰恰被「有流量才有候选」这条规则藏了起来。
	//
	// providers 表缺失或查询失败**不致命**：候选退化成「只有有流量的」，
	// 但必须留痕——静默吞掉会让「列表变短」无法与「查询失败」区分。
	if err := mergeIdleProviders(ctx, q, out); err != nil {
		slog.Warn("report rollup: idle providers not merged into dimension options",
			"error", err)
	}
	if len(truncated) > 0 {
		out.Truncated = truncated
	}
	return out, nil
}

// sortDimensionOptions 候选排序：**有请求的排前面**，请求数降序；同分按键
// 升序（稳定、可预期）。零请求的候选排在后面，但**不隐藏**——它们是
// 「配了却没流量」的审计线索。
func sortDimensionOptions(list []DimensionOption) {
	sort.Slice(list, func(i, j int) bool {
		if list[i].Requests != list[j].Requests {
			return list[i].Requests > list[j].Requests
		}
		return list[i].Key < list[j].Key
	})
}

// mergeIdleProviders 把 providers 表里在册、但区间内没有流量的供应商补进
// 候选列表（requests=0）。
//
// 软删除（deleted_at 非空）的供应商不补：业务上它已经下线，塞进候选只会
// 让审计人以为「它该有流量」。
func mergeIdleProviders(ctx context.Context, q Querier, out *DimensionOptions) error {
	rows, err := q.Query(ctx, `
		SELECT id::text
		  FROM providers
		 WHERE deleted_at IS NULL`)
	if err != nil {
		return fmt.Errorf("merge idle providers: %w", err)
	}
	defer rows.Close()
	known := make(map[string]bool, len(out.Providers))
	for _, item := range out.Providers {
		known[item.Key] = true
	}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return fmt.Errorf("scan idle provider: %w", err)
		}
		if key == "" || known[key] {
			continue
		}
		known[key] = true
		out.Providers = append(out.Providers, DimensionOption{Key: key, Requests: 0})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	sortDimensionOptions(out.Providers)
	return nil
}

// unionAll 拼接 UNION ALL 分支（空列表返回恒假选择，保证 SQL 永远合法）。
func unionAll(branches []string) string {
	if len(branches) == 0 {
		return "SELECT ''::text AS dim, ''::text AS key, 0::bigint AS requests WHERE false"
	}
	out := branches[0]
	for _, b := range branches[1:] {
		out += " UNION ALL " + b
	}
	return out
}

// grainLegacyScopeForView 返回旧口径候选所用的分区 scope。
func grainLegacyScopeForView(view View) Scope {
	if view == ViewInternal {
		return ScopeInternalTenant
	}
	return ScopeDailyTotal
}
