package reportrollup

// grouping_audit_test.go —— 分组集 ↔ 派发规则的回归门（2026-11-17 审计轮）。
//
// ★ 2026-11-17 审计轮修的是「每个维度都带上它的归属维度」：
//
//	旧分组集                    新分组集
//	(person)                   → (tenant_id, person)
//	(credential_id)             → (provider_id, credential_id)
//	(api_key_id)                → (tenant_id, api_key_id)
//	(report_date, person)       → (report_date, tenant_id, person)
//	(report_date, credential_id)→ (report_date, provider_id, credential_id)
//	(report_date, api_key_id)   → (report_date, tenant_id, api_key_id)
//
// 旧写法把归属维度 GROUPING 折叠成 NULL ⇒ 人员行的租户恒为空、凭据行丢掉
// 供应商归属、apikey 行丢掉租户归属，且跨租户同名人员会被合并成一行。
//
// ★ **这个门为什么必须存在**：分组集是「SQL 产出什么行」的声明，classify()
// 是「这一行归哪个分组」的判定，两者是**两份必须一一对应的清单**。改一边
// 忘了改另一边，后果不是报错而是**静默错账**：派发不中时兜底分支把行算进
// gkTotal ⇒ 总计把分组行重复累加（真库口径下总计会凭空翻倍）；派发串了
// 时分组行互相污染 ⇒ Σ各分组 ≠ 总计。
//
// 所以这道门不钉「分组集长什么样」（那是实现细节），而是钉**契约**：
//   ① 每个分组集恰好对应一个 groupKind，且互不重叠；
//   ② 15 个分组集与 15 个 groupKind 一一覆盖（不多不少）；
//   ③ 每个分组集派发到**预期**的那个 kind（不是"某个" kind）。

import (
	"strings"
	"testing"
)

// groupSetColumns 把分组集文本拆成被分组的列名集合。
// "(provider_id, credential_id)" → {provider_id, credential_id}。
func groupSetColumns(set string) map[string]bool {
	out := map[string]bool{}
	inner := strings.TrimSuffix(strings.TrimPrefix(set, "("), ")")
	for _, col := range strings.Split(inner, ",") {
		col = strings.TrimSpace(col)
		if col != "" {
			out[col] = true
		}
	}
	return out
}

// rowForGroupingSet 按分组集构造一行「带正确 GROUPING 标志」的扫描结果：
// 被分组的列 g=0（不是汇总值），被折叠的列 g=1。
//
// 扫描行里 gXxx 已经是 bool 语义（gXxx==1 ⇒ 该列被折叠掉）。
func rowForGroupingSet(set string) grainScanRow {
	cols := groupSetColumns(set)
	return grainScanRow{
		gDate:       !cols["report_date"],
		gProvider:   !cols["provider_id"],
		gCredential: !cols["credential_id"],
		gModel:      !cols["raw_model_name"],
		gTenant:     !cols["tenant_id"],
		gPerson:     !cols["person"],
		gKey:        !cols["api_key_id"],
	}
}

// expectedGroupKinds 分组集 → 预期 groupKind 的契约表。
//
// ★ 这张表是「读面的口径声明」：哪个分组集产出哪一族行。它同时是
// classify() 的真值表——两处不一致就是错账。
var expectedGroupKinds = map[string]groupKind{
	"()":                                        gkTotal,
	"(report_date)":                             gkDay,
	"(report_date, raw_model_name)":             gkDayModel,
	"(provider_id)":                             gkProvider,
	"(provider_id, raw_model_name)":             gkProviderModel,
	"(provider_id, credential_id)":              gkCredential,
	"(raw_model_name)":                          gkModel,
	"(tenant_id)":                               gkTenant,
	"(tenant_id, person)":                       gkPerson,
	"(tenant_id, api_key_id)":                   gkAPIKey,
	"(report_date, provider_id)":                gkDayProvider,
	"(report_date, provider_id, credential_id)": gkDayCredential,
	"(report_date, tenant_id)":                  gkDayTenant,
	"(report_date, tenant_id, person)":          gkDayPerson,
	"(report_date, tenant_id, api_key_id)":      gkDayAPIKey,
}

// TestGrainGroupSets_Contract 每个分组集都派发到契约表指定的 groupKind。
func TestGrainGroupSets_Contract(t *testing.T) {
	for _, daily := range []bool{false, true} {
		sets := grainGroupSets(daily)
		for _, set := range sets {
			want, ok := expectedGroupKinds[set]
			if !ok {
				t.Errorf("daily=%v 分组集 %q 不在契约表里（改了分组集必须同步契约表与 classify）", daily, set)
				continue
			}
			got := classify(rowForGroupingSet(set))
			if got != want {
				t.Errorf("daily=%v 分组集 %q 派发到 %v，期望 %v", daily, set, got, want)
			}
		}
	}
}

// TestGrainGroupSets_CoverAllKinds 分组集与 groupKind 必须一一覆盖：
// 少一个 ⇒ 那一族行永远不产出（报表里整族消失）；多一个 ⇒ 两族撞同一个
// 派发结果（其中一族静默丢行）。
func TestGrainGroupSets_CoverAllKinds(t *testing.T) {
	all := []groupKind{
		gkTotal, gkDay, gkDayModel, gkProvider, gkProviderModel, gkCredential,
		gkModel, gkTenant, gkPerson, gkAPIKey,
		gkDayProvider, gkDayCredential, gkDayTenant, gkDayPerson, gkDayAPIKey,
	}
	for _, daily := range []bool{false, true} {
		sets := grainGroupSets(daily)
		seen := map[groupKind]string{}
		for _, set := range sets {
			got := classify(rowForGroupingSet(set))
			if prev, dup := seen[got]; dup {
				t.Errorf("daily=%v 分组集 %q 与 %q 派发到同一个 kind(%v)", daily, set, prev, got)
			}
			seen[got] = set
		}
		// daily=false 不产出按天×维度族；daily=true 必须全族覆盖。
		want := len(all)
		if !daily {
			want = 10 // 总计/按天/按天×模型/六个维度汇总
		}
		if len(sets) != want {
			t.Errorf("daily=%v 分组集数 = %d，期望 %d", daily, len(sets), want)
		}
		if daily {
			for _, k := range all {
				if _, ok := seen[k]; !ok {
					t.Errorf("daily=true 没有分组集派发到 kind(%v)", k)
				}
			}
		}
	}
}

// TestGrainGroupSets_AttributionPresent 每个「带归属」的维度必须真的把
// 归属维度放进分组集 —— 旧写法（person / credential_id / api_key_id 单列）
// 会把归属 GROUPING 折叠成 NULL，人员行租户恒空、凭据行丢供应商归属。
func TestGrainGroupSets_AttributionPresent(t *testing.T) {
	need := map[string][]string{
		"gkPerson":     {"tenant_id", "person"},
		"gkCredential": {"provider_id", "credential_id"},
		"gkAPIKey":     {"tenant_id", "api_key_id"},
	}
	for set, kind := range map[string]groupKind{
		"(tenant_id, person)":          gkPerson,
		"(provider_id, credential_id)": gkCredential,
		"(tenant_id, api_key_id)":      gkAPIKey,
	} {
		cols := groupSetColumns(set)
		for _, want := range need[groupKindName(kind)] {
			if !cols[want] {
				t.Errorf("分组集 %q 少了归属维度 %q（人员行租户恒空 / 凭据行丢供应商归属 / apikey 行丢租户）", set, want)
			}
		}
	}
}

func groupKindName(k groupKind) string {
	switch k {
	case gkPerson:
		return "gkPerson"
	case gkCredential:
		return "gkCredential"
	case gkAPIKey:
		return "gkAPIKey"
	}
	return ""
}

// TestGrainErrGroupSets_MirrorMetrics 错误分支的分组集必须与度量分支
// **逐个对应**（只是末尾多一个 kind）——错位会让「按供应商的主要错误」
// 少一层或多半层。
func TestGrainErrGroupSets_MirrorMetrics(t *testing.T) {
	for _, daily := range []bool{false, true} {
		base := grainGroupSets(daily)
		errSets := grainErrGroupSets(daily)
		if len(base) != len(errSets) {
			t.Fatalf("daily=%v 度量分支 %d 个分组集 vs 错误分支 %d 个", daily, len(base), len(errSets))
		}
		for i := range base {
			inner := strings.TrimSuffix(strings.TrimPrefix(base[i], "("), ")")
			want := "(" + inner + ", kind)"
			if inner == "" {
				want = "(kind)"
			}
			if errSets[i] != want {
				t.Errorf("daily=%v 第 %d 个错误分组集 = %q，期望 %q（必须与度量分支逐个对应）", daily, i, errSets[i], want)
			}
		}
	}
}
