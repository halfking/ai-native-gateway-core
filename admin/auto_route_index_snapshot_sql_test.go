package admin

// auto_route_index_snapshot_sql_test.go —— auto-route 索引端点 SQL 形态门。
//
// 2026-10-19（96h 审计轮）：本端点的 SQL 原先是
//
//	WITH latest_bucket AS (SELECT credential_id, raw_model, MAX(bucket) ...
//	                       FROM credential_model_index_with_current_month
//	                       GROUP BY credential_id, raw_model)
//	SELECT ... FROM credential_model_index_with_current_month cmi
//	JOIN latest_bucket lb ON lb.credential_id = cmi.credential_id
//	                    AND lb.raw_model     = cmi.raw_model
//	                    AND lb.bucket        = cmi.bucket
//
// 这个形态在 PG 上**把同一批行扫了两遍**（252 生产库 EXPLAIN ANALYZE 实测：
// CTE 431,820 行 × 3 loops + 外层 143,940 行 × 3 loops，Execution 1789 ms）。
// 改成元组 IN 子查询后只扫一遍，Execution 739 ms（-59%）。
//
// 门要钉住的是**形态**，因为形态一改回去，代价立刻回去，而功能测试是绿的
// （返回的行完全一样）——没有这道门，性能回退不可见。
//
// 三条腿都要有牙：
//   · 反向：不得出现「CTE + JOIN 回同一视图」的双扫形态；
//   · 正向：必须走元组 IN 子查询，且是**逐对** MAX(bucket)（不是全局 MAX）；
//   · 位置：canonical_id 过滤必须落在 IN 子查询**之外**（语义等价性第 2 条）——
//     这条只有拼装后的 SQL 才看得出来，故门走 autoRouteIndexSnapshotQuery。

import (
	"strings"
	"testing"
)

// TestAutoRouteIndexSnapshotSQLDoesNotDoubleScan —— 反向腿。
func TestAutoRouteIndexSnapshotSQLDoesNotDoubleScan(t *testing.T) {
	query, _ := autoRouteIndexSnapshotQuery("", 100)
	for _, banned := range []string{
		"latest_bucket",
		"JOIN latest_bucket",
		"WITH latest_bucket",
	} {
		if strings.Contains(query, banned) {
			t.Errorf("auto-route 索引 SQL 出现 %q —— 这是「CTE 取 MAX(bucket) 再 JOIN 回同一视图」的双扫形态，"+
				"PG 会把同一批行扫两遍（252 真库实测 1789 ms vs 单扫 739 ms）。"+
				"请用元组 IN 子查询：WHERE (cmi.credential_id, cmi.raw_model, cmi.bucket) IN (SELECT ... MAX(bucket) ... GROUP BY credential_id, raw_model)",
				banned)
		}
	}
}

// TestAutoRouteIndexSnapshotSQLUsesTupleInSubquery —— 正向腿。
func TestAutoRouteIndexSnapshotSQLUsesTupleInSubquery(t *testing.T) {
	query, _ := autoRouteIndexSnapshotQuery("", 100)
	if !strings.Contains(query, "(cmi.credential_id, cmi.raw_model, cmi.bucket) IN (") {
		t.Errorf("auto-route 索引 SQL 未使用元组 IN 子查询形态：\n%s", query)
	}
	// 逐对取 MAX：必须带 GROUP BY credential_id, raw_model。
	if !strings.Contains(query, "MAX(bucket)") {
		t.Errorf("auto-route 索引 SQL 缺 MAX(bucket)：\n%s", query)
	}
	if !strings.Contains(query, "GROUP BY credential_id, raw_model") {
		t.Errorf("auto-route 索引 SQL 的 MAX(bucket) 没有按 (credential_id, raw_model) 分组 —— "+
			"全局 MAX(bucket) 只会落在最后一次有指标变化的桶上（典型 1~2 行），会把整个索引藏起来：\n%s", query)
	}
	// 显式否掉"全局 MAX"这种形态：MAX(bucket) 后面不能紧跟 FROM 就结束分组。
	if strings.Contains(query, "SELECT MAX(bucket) FROM") {
		t.Errorf("auto-route 索引 SQL 疑似退化成全局 MAX(bucket)：\n%s", query)
	}
}

// TestAutoRouteIndexSnapshotCanonicalFilterStaysOutsideSubquery —— 位置腿。
//
// 语义等价性第 2 条：原写法是「先按对取最新桶，再按 canonical_id 过滤」。
// 若把过滤推进 IN 子查询里，就变成「在过滤后的行里取最新桶」——当同一
// (对, 桶) 上 canonical_id 变过时，两者返回的行**不同**。
// 这是纯位置关系，只看常量文本看不出来，必须拼装后再判。
func TestAutoRouteIndexSnapshotCanonicalFilterStaysOutsideSubquery(t *testing.T) {
	query, args := autoRouteIndexSnapshotQuery("canon-123", 50)

	inIdx := strings.Index(query, "(cmi.credential_id, cmi.raw_model, cmi.bucket) IN (")
	if inIdx < 0 {
		t.Fatalf("未找到元组 IN 子查询：\n%s", query)
	}
	// 子查询的闭合括号：IN ( ... ) 里 SELECT...GROUP BY 之后的那个 ')'
	groupIdx := strings.Index(query, "GROUP BY credential_id, raw_model")
	if groupIdx < 0 {
		t.Fatalf("未找到 GROUP BY：\n%s", query)
	}
	closeIdx := strings.Index(query[groupIdx:], ")")
	if closeIdx < 0 {
		t.Fatalf("未找到子查询闭合括号：\n%s", query)
	}
	subqueryEnd := groupIdx + closeIdx

	filterIdx := strings.Index(query, "AND cmi.canonical_id = $1")
	if filterIdx < 0 {
		t.Fatalf("canonical_id 过滤未拼进查询：\n%s", query)
	}
	if filterIdx < subqueryEnd {
		t.Errorf("canonical_id 过滤落在 IN 子查询**内部**（位置 %d < 子查询结束 %d）—— "+
			"语义从「先按对取最新桶、再过滤」变成了「在过滤后的行里取最新桶」，"+
			"同一 (对, 桶) 上 canonical_id 变过时返回行不同：\n%s", filterIdx, subqueryEnd, query)
	}
	if len(args) != 2 {
		t.Errorf("参数个数 = %d，期望 2（canonical_id + top）：%v", len(args), args)
	}
}

// TestAutoRouteIndexSnapshotKeepsOrderAndLimit —— 参数编号与收尾形态。
func TestAutoRouteIndexSnapshotKeepsOrderAndLimit(t *testing.T) {
	query, args := autoRouteIndexSnapshotQuery("canon-123", 50)
	if !strings.Contains(query, "ORDER BY cmi.score_smart DESC LIMIT $2") {
		t.Errorf("auto-route 索引 SQL 丢了 ORDER BY/LIMIT 或参数编号不对：\n%s", query)
	}
	if len(args) != 2 || args[0] != "canon-123" || args[1] != 50 {
		t.Errorf("参数内容不对：%v", args)
	}
	// 无 canonical_id 时不应出现悬空的 AND。
	q2, a2 := autoRouteIndexSnapshotQuery("", 100)
	if strings.Contains(q2, "AND cmi.canonical_id") {
		t.Errorf("canonicalID 为空时不该拼 canonical_id 过滤：\n%s", q2)
	}
	if !strings.Contains(q2, "LIMIT $1") {
		t.Errorf("无 canonical_id 时 top 应占 $1：\n%s", q2)
	}
	if len(a2) != 1 || a2[0] != 100 {
		t.Errorf("无 canonical_id 时参数不对：%v", a2)
	}
}
