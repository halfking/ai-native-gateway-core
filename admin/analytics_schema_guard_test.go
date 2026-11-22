package admin

import (
	"os"
	"strings"
	"testing"
)

// TestAnalyticsQueries_AlignRealSchema —— analytics 详情查询列名对齐真实 schema 的源码守卫。
//
// 背景（2026-11-22/23 三批同族事故）：analytics_queries.go 的 SQL 按「想象中的表
// 结构」书写，与迁移真实 schema 不符，全部在运行时报 column does not exist 并被
// 静默降级：
//
//	① final_status → usage_facts 真列是 status（537）
//	② cost_usd（usage_facts 上下文）→ 真列是 cost_amount（537）
//	③ request_stats_minute 上查 model_name/provider_name/client_id/SUM(success)/
//	   SUM(tokens)/SUM(credits)/latency_sum → 全不存在（394 真列 success_count/
//	   total_tokens/credits_charged/latency_ms_sum；维度列只有 tenant_id/
//	   provider_id/canonical_id）⇒ 趋势/交叉/降级路径从未工作过
//	④ usage_facts 上查 error_type → 不存在（真列 error_kind）⇒ 错误分布恒空
//
// 修法：维度类查询统一走 request_stats_dim_minute（dim_type/dim_key，与看板
// queryDimPie 同真源）+ request_stats_error_drill_minute（error_kind）。
// 本守卫把「已证伪的写法」钉死在源码层——单测的假 store 不会告诉你列不存在。
func TestAnalyticsQueries_AlignRealSchema(t *testing.T) {
	src, err := os.ReadFile("analytics_queries.go")
	if err != nil {
		t.Fatalf("read analytics_queries.go: %v", err)
	}
	body := string(src)

	// 剥掉 // 注释行再查——历史事故在注释里留有修订记录，属文档不是 SQL。
	var codeLines []string
	for _, line := range strings.Split(body, "\n") {
		if !strings.Contains(line, "//") {
			codeLines = append(codeLines, line)
		}
	}
	code := strings.Join(codeLines, "\n")

	// ①② 已证伪列名（在任何上下文都不得再出现）
	for _, marker := range []string{
		"final_status",
		"error_type",
		"COALESCE(error_type",
	} {
		if strings.Contains(code, marker) {
			t.Fatalf("analytics queries must not reference column %q (does not exist in usage_facts; real columns: status/error_kind per migrations 537)", marker)
		}
	}

	// ③ 维度查询不得查 request_stats_minute（该表无维度名列，维度真源是
	//    request_stats_dim_minute；本文件全部查询应走 dim_minute 或 drill 表）
	if strings.Contains(code, "FROM request_stats_minute") {
		t.Fatalf("dimension queries must read request_stats_dim_minute (dim_type/dim_key), not request_stats_minute — the latter has no model/client name columns and its count columns are success_count/total_tokens/credits_charged")
	}

	// ③-补 已证伪的列名拼法。注：model_name 在 request_stats_error_drill_minute
	//（394）是真实列，drill 表上下文合法 ⇒ 不在此列；provider_name/client_id
	// 在 usage_facts / dim_minute / drill 三表都不存在。
	for _, marker := range []string{
		"SUM(success)", "SUM(tokens)", "SUM(credits)",
		"latency_sum", "provider_name", "client_id =",
	} {
		if strings.Contains(code, marker) {
			t.Fatalf("analytics queries must not reference non-existent column pattern %q", marker)
		}
	}

	// ④ 正面钉：必须使用 dim_minute 真源与真实列名
	for _, marker := range []string{
		"FROM request_stats_dim_minute",
		"request_stats_error_drill_minute",
		"success_count", "failure_count", "total_tokens", "credits_charged",
	} {
		if !strings.Contains(code, marker) {
			t.Fatalf("analytics queries must use real schema artifacts; missing %q", marker)
		}
	}

	// 维度映射必须与 rollup 写入侧（bg/stats_minute_rollup.go rollupDimQueries）一致：
	// client 维度 = agent_name（看板 clients 饼图同源），provider dim_key = provider_id 文本。
	if !strings.Contains(code, `"agent_name"`) {
		t.Fatalf("dimTypeFor must map client dimension to agent_name (dashboard clients pie source dim)")
	}
}
