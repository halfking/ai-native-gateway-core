package admin

// R76 轮（2026-10-24）：`/api/admin/compression/sessions` 的 SQL 形状守卫。
//
// ★ 为什么要守形状：生产 252 共享库实测，旧形状（CTE 把 bodies 的 jsonb 载荷
// 带过整个 24h 窗口 + 对**每个会话**算 jsonb_array_length）要 **105,934 ms**
// ⇒ 必然撞 handler 的 10s ctx ⇒ 端点**恒 500**。
// 新形状（载荷只在 LIMIT 之后回表）实测 **359 ms** ⇒ 295×。
//
// ★ 为什么断言「**产物**」而不是源码字面量：形状必须钉在**真正发给数据库的
// 那个 SQL** 上；否则「把同样的 SQL 换个地方写」就会让门失去牙。

import (
	"strings"
	"testing"
)

// compressionSessionsBodiesMarker bodies 源的起始片段（用来在 SQL 里定位它）。
// ★ 不能用整段 bodies 源做比较：它太长且含换行，断言会变成脆的字符串比对。
const compressionSessionsBodiesMarker = "(SELECT sb.request_id"

func compressionSessionsProducedSQL(t *testing.T) (string, string) {
	t.Helper()
	where := `rl.ts >= $1 AND rl.ts <= $2 AND rl.gw_session_id IS NOT NULL AND ($3 OR rl.success)`
	countSQL, mainSQL := compressionSessionsSQL(where, 5, 6)
	return countSQL, mainSQL
}

// TestR76CompressionSessionsCTEMustNotCarryBodyPayload（★ 判据①）
//
// ★ 载荷列（rb.outbound_body / rb.request_body）一旦进了 window_turns 的投影，
// 就会被物化到**整个窗口**的每一行 ⇒ 生产 17,219 行 × ~110KB ⇒ 106 秒。
func TestR76CompressionSessionsCTEMustNotCarryBodyPayload(t *testing.T) {
	_, mainSQL := compressionSessionsProducedSQL(t)

	start := strings.Index(mainSQL, "window_turns AS (")
	if start < 0 {
		t.Fatal("★ 主查询里找不到 window_turns CTE")
	}
	projStart := start + len("window_turns AS (")
	projEnd := strings.Index(mainSQL[projStart:], "FROM ")
	if projEnd < 0 {
		t.Fatal("★ window_turns CTE 里找不到 FROM ⇒ 形状已变")
	}
	projection := mainSQL[projStart : projStart+projEnd]

	for _, forbidden := range []string{"rb.outbound_body", "rb.request_body", "rb.response_body", "rb."} {
		if strings.Contains(projection, forbidden) {
			t.Errorf("★ window_turns 的投影列里出现了 bodies 载荷列 %q ⇒ 载荷会被物化到整个窗口", forbidden)
		}
	}
}

// TestR76CompressionSessionsPayloadMustBeFetchedAfterLimit（★ 判据②）
//
// ★★ 这是本轮最关键的一条形状约束：**载荷回表必须发生在 LIMIT 之后**。
// 若载荷在分页之前求值，它会对**每个会话**执行一次
// ⇒ 生产 14,434 个会话 ⇒ 14,434 次 jsonb 载荷解冻 ⇒ 实测 6,090 ms（仍贴着 10s 预算）。
// 挪到 LIMIT 之后只剩 50 次 ⇒ 实测 359 ms。
//
// ★ 载荷取值形态：**标量子查询**（带 LIMIT 1 ⇒ 不可被 pull-up 展开），
//
//	而不是 LEFT JOIN LATERAL —— 后者被既有门
//	admin/s3_wave4_bodies_sources_test.go 明令禁止（2612 生产缺陷的守卫）。
func TestR76CompressionSessionsPayloadMustBeFetchedAfterLimit(t *testing.T) {
	_, mainSQL := compressionSessionsProducedSQL(t)

	limitIdx := strings.Index(mainSQL, "LIMIT $")
	payloadIdx := strings.Index(mainSQL, "jsonb_typeof(request_body)")
	if limitIdx < 0 {
		t.Fatal("★ 主查询里找不到分页 LIMIT")
	}
	if payloadIdx < 0 {
		t.Fatal("★ 主查询里找不到载荷取值（jsonb_typeof(request_body)）")
	}
	if payloadIdx < limitIdx {
		t.Errorf("★ 载荷取值出现在 LIMIT 之前 ⇒ 它会对每个会话求值一次" +
			"（生产实测 6,090ms vs 322ms）")
	}
	// 载荷取值必须只出现在**标量子查询**里（不得回退成 LEFT JOIN LATERAL）
	if strings.Contains(mainSQL, "LEFT JOIN LATERAL") {
		t.Error("★ 载荷回表不得用 LEFT JOIN LATERAL（既有门 s3_wave4 禁止；" +
			"LATERAL 体里重跑 canonical 全源是 2612 生产实测的性能缺陷）")
	}
}

// TestR76CompressionSessionsBodiesJoinMustBeExistsSemantics（★ 判据③）
//
// ★ bodies 侧必须走 EXISTS 半连接：LEFT JOIN + `rb.outbound_body IS NOT NULL`
// 会把 bodies 的载荷列带进结果集；EXISTS 只问「有没有」。
func TestR76CompressionSessionsBodiesJoinMustBeExistsSemantics(t *testing.T) {
	countSQL, mainSQL := compressionSessionsProducedSQL(t)

	for name, sql := range map[string]string{"count": countSQL, "main": mainSQL} {
		if !strings.Contains(sql, "EXISTS (SELECT 1 FROM "+compressionSessionsBodiesMarker) {
			t.Errorf("★ %s 查询里找不到 bodies 的 EXISTS 半连接", name)
		}
		if strings.Contains(sql, "LEFT JOIN "+compressionSessionsBodiesMarker) {
			t.Errorf("★ %s 查询里仍有 bodies 的 LEFT JOIN ⇒ 载荷会被带进结果集", name)
		}
		// 半连接里的判定谓词必须保留（去掉它口径就变了：会把没有 outbound body 的轮次算进来）
		if !strings.Contains(sql, "rb.outbound_body IS NOT NULL") {
			t.Errorf("★ %s 查询的半连接缺 `rb.outbound_body IS NOT NULL` ⇒ 口径被改了", name)
		}
	}
	// count 的聚合口径不能变
	if !strings.Contains(countSQL, "COUNT(DISTINCT rl.gw_session_id)") {
		t.Error("★ count 查询的聚合口径被改了（应仍是 COUNT(DISTINCT rl.gw_session_id)）")
	}
}

// TestR76CompressionSessionsMustKeepJsonbTypeofGuard（★ 判据④）
//
// ★ 2605 部署实测抓的缺陷守卫（保留）：request_delta 有 array / SQL NULL /
// **JSON 字面量 null** / 其它标量四种形态 ⇒ 必须 jsonb_typeof 判形，
// 否则 jsonb_array_length 直接 22023。
func TestR76CompressionSessionsMustKeepJsonbTypeofGuard(t *testing.T) {
	_, mainSQL := compressionSessionsProducedSQL(t)
	if !strings.Contains(mainSQL, "jsonb_typeof(request_body) = 'array'") {
		t.Error("★ 载荷回表里丢了 jsonb_typeof 判形 ⇒ JSON null 形态会 22023")
	}
	if !strings.Contains(mainSQL, "jsonb_array_length(request_body)") {
		t.Error("★ 载荷回表里丢了 jsonb_array_length ⇒ estimated_original_msgs 会算错")
	}
}
