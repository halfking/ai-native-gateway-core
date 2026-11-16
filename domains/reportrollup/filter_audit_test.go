package reportrollup

// filter_audit_test.go —— 过滤语义的回归门（2026-11-16 审计轮）。
//
// 两处语义改动，各自对应审计发现：
//
//  1. **provider_id = -1 落成 IS NULL**：未落定桶在快照里是 NULL（不是 -1），
//     读面映射成 -1 显示成一行（真库实测占全量流量 30.8%）。等值匹配
//     `provider_id = -1` 永远 0 行 ⇒ 这一桶看得见却筛不出来。
//  2. **模型过滤走 raw 名集合（= ANY）**：路由页给的是 canonical 名，展开后
//     是一组 raw 名（见 modelnames.go）。
//
// 占位符纪律不变：$4 起必须连号无空洞（PG 42P18）。

import (
	"strings"
	"testing"
)

func TestGrainAggSQL_UnassignedProviderIsNull(t *testing.T) {
	// provider_id = -1（未落定哨兵）必须落成 IS NULL，且**不占占位符**
	//（IS NULL 不带参数 ⇒ 参数序号不能跳）。
	f := GrainFilter{ProviderID: i64(UnassignedID)}
	sql := grainAggSQL([]string{"daily_grain"}, f, false, "")
	if !strings.Contains(sql, "provider_id IS NULL") {
		t.Errorf("未落定供应商没落成 IS NULL：\n%s", sql)
	}
	if strings.Contains(sql, "provider_id = $") {
		t.Errorf("未落定供应商仍在做等值匹配（永远 0 行）：\n%s", sql)
	}
	used := placeholderSet(sql)
	for i := 1; i <= 3; i++ {
		if !used[i] {
			t.Errorf("$%d 未被使用", i)
		}
	}
	if used[4] {
		t.Errorf("未落定供应商多占了 $4：\n%s", sql)
	}
	if got := filterArgCount(f); got != 0 {
		t.Errorf("filterArgCount = %d，期望 0（IS NULL 不占参数）", got)
	}
}

// TestGrainAggSQL_UnassignedIdDimsAreNull 三个 id 维度共用同一条约定：
// -1 = 未落定 ⇒ 落成 IS NULL，且**不占占位符**（IS NULL 不带参数 ⇒ 参数
// 序号不能跳）。
//
// ★ 2026-11-17 审计轮：旧实现只覆盖 provider_id，于是凭据/apikey 的
// 未落定桶（真库实测 38,035 笔 / 16 笔）在读面显示成一行、点进去却
// **永远 0 行**（`credential_id = -1` 在真库里没有这号）⇒ 看得见筛不出。
func TestGrainAggSQL_UnassignedIdDimsAreNull(t *testing.T) {
	for _, tc := range []struct {
		name string
		col  string
		f    GrainFilter
	}{
		{"provider", "provider_id", GrainFilter{ProviderID: i64(UnassignedID)}},
		{"credential", "credential_id", GrainFilter{CredentialID: i64(UnassignedID)}},
		{"api_key", "api_key_id", GrainFilter{APIKeyID: i64(UnassignedID)}},
	} {
		sql := grainAggSQL([]string{"daily_grain"}, tc.f, false, "")
		if !strings.Contains(sql, tc.col+" IS NULL") {
			t.Errorf("%s 未落定没落成 IS NULL：\n%s", tc.name, sql)
		}
		if strings.Contains(sql, tc.col+" = $") {
			t.Errorf("%s 未落定仍在做等值匹配（永远 0 行）：\n%s", tc.name, sql)
		}
		used := placeholderSet(sql)
		for i := 1; i <= 3; i++ {
			if !used[i] {
				t.Errorf("%s：$%d 未被使用", tc.name, i)
			}
		}
		if used[4] {
			t.Errorf("%s 未落定多占了 $4：\n%s", tc.name, sql)
		}
		if got := filterArgCount(tc.f); got != 0 {
			t.Errorf("%s filterArgCount = %d，期望 0（IS NULL 不占参数）", tc.name, got)
		}
	}
}

// TestGrainAggSQL_UnassignedIdDimsWithOtherFilters 多个未落定维度与其它
// 过滤条件并存时，占位符必须仍然连号（$4 起）。
func TestGrainAggSQL_UnassignedIdDimsWithOtherFilters(t *testing.T) {
	f := GrainFilter{
		ProviderID:   i64(UnassignedID),
		CredentialID: i64(UnassignedID),
		APIKeyID:     i64(UnassignedID),
		Person:       "p",
	}
	sql := grainAggSQL([]string{"daily_grain"}, f, false, "")
	for _, col := range []string{"provider_id", "credential_id", "api_key_id"} {
		if !strings.Contains(sql, col+" IS NULL") {
			t.Errorf("%s 未落定没落成 IS NULL：\n%s", col, sql)
		}
	}
	if !strings.Contains(sql, "person = $4") {
		t.Errorf("person 的占位符没接上 $4：\n%s", sql)
	}
	used := placeholderSet(sql)
	for i := 1; i <= 4; i++ {
		if !used[i] {
			t.Errorf("$%d 未被使用（占位符有空洞 = PG 42P18）", i)
		}
	}
	if used[5] {
		t.Errorf("出现了超出 $4 的占位符：\n%s", sql)
	}
	if got := filterArgCount(f); got != 1 {
		t.Errorf("filterArgCount = %d，期望 1（三个 IS NULL 不占参数，只有 person 占）", got)
	}
}

// TestGrainAggSQL_UnassignedProviderWithOtherFilters 未落定供应商与其它
// 过滤条件并存时，占位符必须仍然连号（$4 起）。
func TestGrainAggSQL_UnassignedProviderWithOtherFilters(t *testing.T) {
	f := GrainFilter{ProviderID: i64(UnassignedID), CredentialID: i64(2), Person: "p"}
	sql := grainAggSQL([]string{"daily_grain"}, f, false, "")
	if !strings.Contains(sql, "provider_id IS NULL") {
		t.Errorf("未落定供应商没落成 IS NULL：\n%s", sql)
	}
	if !strings.Contains(sql, "credential_id = $4") {
		t.Errorf("后续过滤条件的占位符没接上 $4：\n%s", sql)
	}
	if !strings.Contains(sql, "person = $5") {
		t.Errorf("person 的占位符没接上 $5：\n%s", sql)
	}
	used := placeholderSet(sql)
	for i := 1; i <= 5; i++ {
		if !used[i] {
			t.Errorf("$%d 未被使用（占位符有空洞 = PG 42P18）", i)
		}
	}
	if used[6] {
		t.Errorf("出现了超出 $5 的占位符：\n%s", sql)
	}
}

// TestGrainAggSQL_ModelNamesUseAny 展开后的模型过滤走 = ANY（集合），
// 而不是单值等值。
func TestGrainAggSQL_ModelNamesUseAny(t *testing.T) {
	f := GrainFilter{Model: "minimax-m2.5", ModelNames: []string{"MiniMax-M2.5", "minimax-m2.5"}}
	sql := grainAggSQL([]string{"daily_grain"}, f, false, "")
	if !strings.Contains(sql, "raw_model_name = ANY($4)") {
		t.Errorf("模型集合过滤没走 = ANY：\n%s", sql)
	}
	if strings.Contains(sql, "raw_model_name = $4") {
		t.Errorf("模型集合过滤退化成了单值等值：\n%s", sql)
	}
	if got := filterArgCount(f); got != 1 {
		t.Errorf("filterArgCount = %d，期望 1（集合占 1 个参数）", got)
	}
}

// TestGrainAggSQL_ModelExactWhenNotExpanded 未展开时保持单值精确匹配
// （旧行为不能变）。
func TestGrainAggSQL_ModelExactWhenNotExpanded(t *testing.T) {
	f := GrainFilter{Model: "glm-5.2"}
	sql := grainAggSQL([]string{"daily_grain"}, f, false, "")
	if !strings.Contains(sql, "raw_model_name = $4") {
		t.Errorf("未展开的模型过滤没保持单值等值：\n%s", sql)
	}
}

// TestFilterArgCount_EmptyFilter 空过滤不占参数。
func TestFilterArgCount_EmptyFilter(t *testing.T) {
	if got := filterArgCount(GrainFilter{}); got != 0 {
		t.Errorf("filterArgCount(空) = %d，期望 0", got)
	}
	if (GrainFilter{ModelNames: []string{"a"}}).empty() != false {
		t.Error("带 ModelNames 的过滤不该是 empty")
	}
}
