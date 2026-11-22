package apihub

import (
	"regexp"
	"strings"
	"testing"
)

// ── L4 守卫：MarkMissingDown 的 SQL 必须是「集合差」，不是逐行写 ────────
//
// 2026-11-23（211 配置优化遗留 L4）：探针以前把整张 assets 表分页拉进 Go 侧
// 再逐行 MarkHealth，带一个 20 页 × 500 行 = 10000 行的上界 ⇒ 超过上界的行
// 静默漏检；而且上界告警的判据是反的（正常跑完就喊）。
//
// 新口径：一条 UPDATE ... NOT (ref_id = ANY(...)) 在 DB 侧做差集。
//
// 这道门是**结构门**：它量的是 SQL 文本的形状，量不到「探针有没有真的调用它」
// —— 后者由 bg 侧的行为门（TestProbeUsesSetDifferenceInsteadOfListAggregation /
// TestProbeCoversAllAssetsBeyondOldPaginationCeiling）负责。两条门各管一半。
func TestMarkMissingDownSQLIsSetDifference(t *testing.T) {
	sql := markMissingDownSQL

	// ① 必须是一条 UPDATE，不是 SELECT/DELETE。
	if !strings.Contains(sql, "UPDATE public.assets") {
		t.Fatalf("markMissingDownSQL 不是对 public.assets 的 UPDATE。\n%s", sql)
	}

	// ② 必须有租户谓词（RLS 之外的第二道边界：跨租户误伤）。
	if !regexp.MustCompile(`tenant_id\s*=\s*\$1`).MatchString(sql) {
		t.Errorf("markMissingDownSQL 没有 tenant_id = $1 谓词。\n"+
			"  ★ 没有它，一条语句会把**所有租户**的同 kind 资产一起打成 down。\n\n%s", sql)
	}

	// ③ 必须有 kind 谓词（否则 mcp/llm 两类会互相误伤）。
	if !regexp.MustCompile(`kind\s*=\s*\$2`).MatchString(sql) {
		t.Errorf("markMissingDownSQL 没有 kind = $2 谓词。\n"+
			"  ★ 没有它，「活着的 mcp 集合」会被拿去判 llm_endpoint 行。\n\n%s", sql)
	}

	// ④ 必须真的用 ANY(...) 做反连（集合差），而不是别的形状。
	if !regexp.MustCompile(`NOT\s*\(\s*ref_id\s*=\s*ANY\(\s*\$3::bigint\[\]\s*\)\s*\)`).MatchString(sql) {
		t.Errorf("markMissingDownSQL 没有 `NOT (ref_id = ANY($3::bigint[]))` 反连。\n"+
			"  ★ 差集必须在 DB 侧做；拉回 Go 侧比对就是「统计面禁止列表聚合」。\n\n%s", sql)
	}

	// ⑤ 已经是 down 的行不得重复写（§10.26 的空写教训）。
	if !strings.Contains(sql, "health_state IS DISTINCT FROM 'down'") {
		t.Errorf("markMissingDownSQL 没有 `health_state IS DISTINCT FROM 'down'` 守卫。\n"+
			"  ★ 没有它，每轮都会把已经是 down 的行再写一遍（空写：n_tup_upd + 堆脏页）。\n\n%s", sql)
	}

	// ⑥ 目标值必须字面量 'down'（HealthState 的取值口径在 Go 侧是 "down"）。
	if !strings.Contains(sql, "SET health_state = 'down'") {
		t.Errorf("markMissingDownSQL 没有 `SET health_state = 'down'`。\n\n%s", sql)
	}
}
