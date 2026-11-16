package admin

// execute 路径的「每类别删除行数」归属守卫。
//
// # 这条腿守的是哪块缺口
//
// 2026-11-16 补「execute 真删行」证据时读到一处错位：
//
//	Apply 返回的是**按位置对齐**的 []int64，且只含「表确实存在」的类别；
//	handler 却按 planCategories(plan)（**全部**类别）按位置取值。
//
// ⇒ 只要某个类别的表在本环境**一张都不存在**（代码**明确支持**的场景：
// 「老部署没迁移到位 ⇒ 不该整单失败」⇒ 跳过不存在的表），
// 该类别**之后**的每个类别都会拿到别人的数字：
//
//	面板上就会显示「探测记录 删了 3 行」，而那 3 行其实是**凭据**的。
//
// ⇒ 修法：Apply 按**类别 key** 返回，handler 按 key 取值 ⇒ 缺席的类别就是 0。
//
// # ★ 为什么本文件不用真库
//
// 归属逻辑是纯函数 ⇒ 无库可跑 ⇒ 任何一次 `go test ./admin/` 都会执行它
// （真库腿会因为没设 DSN 而跳过 ⇒ **这条腿才是那个永远在的守卫**）。
// 真库那一侧（真的把行删掉）在 admin/data_lifecycle_data_init_isolated_db_test.go。

import "testing"

// TestDataInitPerCategoryCounts_MissingCategoryDoesNotShiftOthers 钉住
// 「某个类别缺席 ⇒ 后续类别不位移」。
//
// ★ 这里的输入形状就是**线上会遇到的形状**：probe_records 一张表都没有
//
//	（老部署没迁移到位），而 request_records / credentials / api_keys 有表。
//	⇒ 若实现退回「按位置取值」，probe_records 会拿到 credentials 的 3 行，
//	  credentials 会拿到 api_keys 的 1 行，api_keys 会拿到 0 —— 全部错位。
func TestDataInitPerCategoryCounts_MissingCategoryDoesNotShiftOthers(t *testing.T) {
	order := []string{"request_records", "probe_records", "credentials", "api_keys"}
	// Apply 的真实返回形状：只含真正执行过的类别。
	deleted := map[string]int64{
		"request_records": 7,
		"credentials":     3,
		"api_keys":        1,
	}

	got := dataInitPerCategoryCounts(order, deleted)

	want := map[string]int64{
		"request_records": 7,
		"probe_records":   0, // 缺席 ⇒ 0，不是别人的数字
		"credentials":     3,
		"api_keys":        1,
	}
	for key, w := range want {
		if got[key] != w {
			t.Errorf("category %s: got %d rows, want %d (full map=%v)", key, got[key], w, got)
		}
	}
}

// TestDataInitPerCategoryCounts_KeepsEveryPlannedCategory 钉住
// 「计划里的每个类别都要出现在结果里」——
// 前端按 key 渲染表格（DataInitialization.vue 的 v-for="r in lastResult.results"），
// 类别缺席 = 面板上少一行。
func TestDataInitPerCategoryCounts_KeepsEveryPlannedCategory(t *testing.T) {
	order := []string{"request_records", "probe_records", "credentials", "api_keys", "users", "tenants"}
	deleted := map[string]int64{"request_records": 2}

	got := dataInitPerCategoryCounts(order, deleted)

	if len(got) != len(order) {
		t.Fatalf("got %d categories, want %d: %v", len(got), len(order), got)
	}
	for _, key := range order {
		if _, ok := got[key]; !ok {
			t.Errorf("category %s missing from result map: %v", key, got)
		}
	}
}

// TestDataInitPerCategoryCounts_TotalEqualsSumOfCategories 钉住
// 「总数 = 各类别之和」——响应里的 total_rows_deleted 是面板摘要行的数。
func TestDataInitPerCategoryCounts_TotalEqualsSumOfCategories(t *testing.T) {
	order := []string{"request_records", "probe_records", "credentials"}
	deleted := map[string]int64{"request_records": 2, "credentials": 3}

	got := dataInitPerCategoryCounts(order, deleted)

	var total int64
	for _, v := range got {
		total += v
	}
	if total != 5 {
		t.Errorf("total = %d, want 5 (map=%v)", total, got)
	}
}
