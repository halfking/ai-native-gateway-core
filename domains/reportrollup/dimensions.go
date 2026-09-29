// dimensions.go —— 筛选栏候选项（2026-09-29 对帐页多维筛选轮）。
//
// 候选来自快照本身（grain 口径 + 旧口径非 grain 日期的分区 scope），不回扫
// usage_facts——与主读面同一数据源，避免「报表能筛但下拉框没有」的错位。
//
// 与当前筛选条件**无关**是刻意选择：若候选按已选条件收窄，选中一个供应商后
// 其它供应商会从下拉里消失，用户无法横向切换（筛选栏最常见的自锁坑）。
// 候选只受日期区间与视角约束。
package reportrollup

import (
	"context"
	"fmt"
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
}

// maxDimensionOptions 单维候选上限。本地真库实测 30 天内 527 个模型名、
// 171 个 apikey，全量下发只有几十 KB；但区间可到 366 天，硬上限避免
// 模型长尾把筛选栏撑成几百项。
const maxDimensionOptions = 500

// dimensionColumns 候选维度 → 快照列的映射（六个分支共用同一份源查询）。
//
// textual 决定要不要再加 `AND col <> ”`：三个 id 列是 bigint，拿空串比较
// 会让 PG 尝试把 ” cast 成 bigint 直接 22P02（真库实测）。id 列不可能是
// 空串，文本列才需要这条。
var dimensionColumns = []struct {
	dim     string
	col     string
	textual bool
}{
	{"provider", "provider_id", false},
	{"credential", "credential_id", false},
	{"api_key", "api_key_id", false},
	{"model", "raw_model_name", true},
	{"tenant", "tenant_id", true},
	{"person", "person", true},
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
	// 就是候选数本身（几十~几百行），比拉全量明细再在 Go 里聚合便宜一个
	// 数量级。
	branches := make([]string, 0, len(dimensionColumns))
	for _, d := range dimensionColumns {
		nonEmpty := ""
		if d.textual {
			nonEmpty = fmt.Sprintf(" AND s.%s <> ''", d.col)
		}
		// 四个动词全部写显式索引：Go 的 fmt 在用过 %[n]s 之后，后续隐式
		// %s 会**从 n+1 继续数**，于是这里第 4 个 %s 会拿到参数 3（src），
		// 把整段源查询再拼一遍——真库 42601 才暴露出来。
		branches = append(branches, fmt.Sprintf(
			`SELECT '%[1]s'::text AS dim, s.%[2]s::text AS key, SUM(s.request_count)::bigint AS requests
			   FROM (%[3]s) s
			  WHERE s.%[2]s IS NOT NULL%[4]s
			  GROUP BY 2`, d.dim, d.col, src, nonEmpty))
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
	assign := func(dst *[]DimensionOption, dim string) {
		list := buckets[dim]
		sort.Slice(list, func(i, j int) bool {
			if list[i].Requests != list[j].Requests {
				return list[i].Requests > list[j].Requests
			}
			return list[i].Key < list[j].Key
		})
		if len(list) > maxDimensionOptions {
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
	return out, nil
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
