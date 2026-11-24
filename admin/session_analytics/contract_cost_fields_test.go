package sessionanalytics

// contract_cost_fields_test.go —— L1 成本口径字段 JSON 形状钉测（2026-11-21 12h 修订审计轮 R-L1a）。
//
// ★ 背景：4e6e14667 给 9 个结构体补了 internal_cost/external_cost 分离字段（保留
//	total_cost 向后兼容），但**零钉测新增**——contract_test.go 无任何 cost 键断言。
//	按 AGENTS.md「端点形状钉测必须包含序列化输出断言」纪律，新增字段的 JSON 形状
//	必须被 CI 保护：后续重命名字段/漏 Scan/漏 tag 在 200 响应里不可见。
//
// ★ 钉法（两层）：
//	① 反射层：穷举 9 个结构体，断言三个 cost 字段的 json tag 逐一存在——
//	  改名 tag（如 internal_cost→internal_cost_usd）会在 CI 直接红，逼着同步前端消费面。
//	② 序列化层：真 json.Marshal 走 OverviewSummary 的自定义 MarshalJSON（Alias 内嵌），
//	  断言输出含全部三键且值语义正确（total = internal + external 的跨单位相加仍是其自身口径，
//	  分离字段独立序列化不被 Alias 丢弃）。

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// costFieldSpec 要求结构体必须同时暴露的三个成本键（json tag 名）。
var costFieldSpec = []string{"total_cost", "internal_cost", "external_cost"}

// costPinnedTypes 穷举 4e6e14667 补过分离字段的结构体（9 个）。
// 新增带三 cost 字段的类型时应加进本表（漏加=该类型字段形状无 CI 保护）。
var costPinnedTypes = []struct {
	name  string
	value interface{}
}{
	{"OverviewSummary", OverviewSummary{}},
	{"TopUser", TopUser{}},
	{"TopProject", TopProject{}},
	{"UserRanking", UserRanking{}},
	{"UserDetailSummary", UserDetailSummary{}},
	{"UserProject", UserProject{}},
	{"ProjectTask", ProjectTask{}},
	{"AgentRanking", AgentRanking{}},
	{"ModelRanking", ModelRanking{}},
}

// TestCostFieldJSONTagsPinned 钉测①：9 结构体 × 3 cost 字段 tag 逐一存在。
func TestCostFieldJSONTagsPinned(t *testing.T) {
	for _, tc := range costPinnedTypes {
		t.Run(tc.name, func(t *testing.T) {
			rt := reflect.TypeOf(tc.value)
			seen := map[string]bool{}
			for i := 0; i < rt.NumField(); i++ {
				tag := rt.Field(i).Tag.Get("json")
				if tag == "" || tag == "-" {
					continue
				}
				// tag 形如 "name,omitempty"；取首段。
				for j := 0; j < len(tag); j++ {
					if tag[j] == ',' {
						tag = tag[:j]
						break
					}
				}
				seen[tag] = true
			}
			for _, key := range costFieldSpec {
				assert.True(t, seen[key],
					"%s 缺 json tag %q —— L1 分离成本字段被改名/删除时必须同步前端消费面（SessionStatsRankings 等）",
					tc.name, key)
			}
		})
	}
}

// TestOverviewSummaryCostJSONShape 钉测②：真实序列化含三键，自定义 MarshalJSON 不丢新字段。
func TestOverviewSummaryCostJSONShape(t *testing.T) {
	s := OverviewSummary{
		TotalCost:    1.5,
		InternalCost: 1.0,
		ExternalCost: 0.5,
	}
	b, err := json.Marshal(&s) // 注意指针：MarshalJSON 定义在 *OverviewSummary 上
	require.NoError(t, err)

	var out map[string]json.Number
	require.NoError(t, json.Unmarshal(b, &out))
	for _, key := range costFieldSpec {
		require.Contains(t, out, key, "序列化输出缺 %q —— MarshalJSON Alias 内嵌必须带出新字段", key)
	}
	assert.Equal(t, "1", out["internal_cost"].String())
	assert.Equal(t, "0.5", out["external_cost"].String())
	assert.Equal(t, "1.5", out["total_cost"].String())
}

// TestCostKeysNotNestedUnderAlias 钉测②补：TopUser 无自定义 MarshalJSON，走默认序列化，
// 断言分离字段值独立（不被 total 覆盖/吞并）。
func TestCostKeysNotNestedUnderAlias(t *testing.T) {
	u := TopUser{
		Owner:        "u1",
		TotalCost:    2.0,
		InternalCost: 1.25,
		ExternalCost: 0.75,
	}
	b, err := json.Marshal(u)
	require.NoError(t, err)

	var out map[string]interface{}
	require.NoError(t, json.Unmarshal(b, &out))
	assert.Equal(t, 1.25, out["internal_cost"], "internal_cost 序列化值漂移")
	assert.Equal(t, 0.75, out["external_cost"], "external_cost 序列化值漂移")
	assert.Equal(t, 2.0, out["total_cost"])
}

// TestCostSplitColumnsAreSelectedAndScanned 钉测③：SQL 真的 SELECT 了
// internal_cost / external_cost 两列，且 SELECT 列数 == rows.Scan 目标数。
//
// ★ 为什么这条不能省（它就是本文件原先的**假绿门**）：
//
//	4e6e14667 给 9 个结构体补了 internal_cost/external_cost 分离字段，但
//	会话分析族的几条 SELECT 只出了混合值 total_cost（= internal_cost +
//	external_cost，**跨单位相加**）⇒ 结构体里字段在、JSON 键在、**值恒为 0**。
//	而本文件原先只断言"json tag 存在"与"结构体直接序列化的值"⇒ **没有
//	一条断言 SQL 真的 Scan 了这两列** ⇒ 这个缺陷可以长期门绿：
//	反射与序列化都看不出"这个字段永远是 0"。
//
//	⇒ 这里补**形状**断言（两层）：
//	① 两个别名必须出现在 SELECT 列表里（不在 ⇒ 结构体字段恒 0）；
//	② SELECT 列数必须 == 该函数 rows.Scan 的实参个数（漏扫 ⇒ 恒 0；
//	   多扫 ⇒ 运行期类型错或静默串列）。
//
// ★ 为什么断言"列数"而不是"第几列"：行号是脆的（改一次列序就红，而列序
//
//	本身不是缺陷）；列数 + 别名是**语义**的 —— 只要漏扫一列，列数立刻不等；
//	只要没 SELECT 那一列，别名立刻不在。两者都与行号无关。
//
// ★ 为什么三条 SQL 分开列：userRankingSQL 与 userRankingByProjectSQL 共用
// 同一个 rows.Scan（GetUserRanking）⇒ 两条 SQL 的**列序必须一致**；分开
// 断言才能在"其中一条改了列序"时报出来，而不是让另一条替它背锅。
func TestCostSplitColumnsAreSelectedAndScanned(t *testing.T) {
	cases := []struct {
		name     string
		sql      string
		scanFunc string
	}{
		{"topUsersSQL", topUsersSQL, "func getTopUsers("},
		{"userRankingSQL", userRankingSQL, "func GetUserRanking("},
		{"userRankingByProjectSQL", userRankingByProjectSQL, "func GetUserRanking("},
	}

	src := readPackageSource(t, "queries.go")
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cols := sqlSelectColumns(t, tc.sql)
			for _, alias := range []string{"internal_cost", "external_cost"} {
				require.True(t, hasSelectAlias(cols, alias),
					"%s 的 SELECT 里缺别名 %q —— 结构体里的分离字段会恒为 0（实际列清单：%v）",
					tc.name, alias, cols)
			}
			want := len(cols)
			got := scanDestinationsOf(t, src, tc.scanFunc)
			assert.Equal(t, want, got,
				"%s 的 SELECT 列数(%d) 与 %s 的 rows.Scan 目标数(%d) 不一致 ⇒ 漏扫/多扫，"+
					"分列成本会恒为 0 或读错列",
				tc.name, want, tc.scanFunc, got)
		})
	}
}
