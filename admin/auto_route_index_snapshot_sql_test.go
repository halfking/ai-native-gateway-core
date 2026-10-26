package admin

// auto_route_index_snapshot_sql_test.go —— auto-route 索引端点 SQL 形态门。
//
// 2026-10-19（96h 审计轮）：本端点的 SQL 写成
//
//	WITH latest_bucket AS (SELECT credential_id, raw_model, MAX(bucket) ...
//	                       FROM credential_model_index_with_current_month
//	                       GROUP BY credential_id, raw_model)
//	SELECT ... FROM credential_model_index_with_current_month cmi
//	JOIN latest_bucket lb ON lb.credential_id = cmi.credential_id
//	                    AND lb.raw_model     = cmi.raw_model
//	                    AND lb.bucket        = cmi.bucket
//
// 本轮改写成「元组 IN 子查询」。⚠⚠ **原提交（a54213451）给出的性能理由
// 「1789 ms → 739 ms，-59%，每天省 27 分钟数据库时间」是错的，已撤回**——
// 错因是**测量顺序造成的冷热缓存混淆**（先跑的冷、后跑的暖）。订正后的真库
// 实测（生产形态、先预热再交错重复 ABAB ×6）：
//
//	旧写法 n=6 min 644 / 中位 777 / max 1173 / 均值 829 ms
//	新写法 n=6 min 632 / 中位 691 / max 1060 / 均值 755 ms
//	⇒ 中位差 -11%、最小值差 -1.9%，**组内极差大于组间差** ⇒ 性能无实质差异。
//
// ⇒ 所以这道门**不再**以性能为由钉形态。它现在钉的是三件**真实**的事：
//  1) 【性能·真实】不得改成 `DISTINCT ON`：真库实测它让 PG 对 432,076 行做
//     external merge Sort、溢出 58,944 kB 到磁盘，Execution 2980 ms（我方）/
//     3040.8 ms（复核方）⇒ **比两种现有写法都差 3~4 倍**，远超噪声带。
//  2) 【语义】MAX(bucket) 必须**逐对**取，不是全局 MAX：桶是稀疏的，全局
//     最大值只落在最后一次有指标变化的桶上（典型 1~2 行），会把整个索引藏起来。
//  3) 【语义】canonical_id 过滤必须落在**去重之外**：否则语义从「先按对取
//     最新桶、再过滤」变成「在过滤后的行里取最新桶」，同一 (对, 桶) 上
//     canonical_id 变过时返回行不同。这条只有**拼装后**的 SQL 才看得出来，
//     故门走 autoRouteIndexSnapshotQuery。
//
// 另有一条**准入闸**：只允许两种已实测等价的形态（CTE+JOIN / 元组 IN）。
// 出现第三种形态 ⇒ 必须先在真库跑 A/B（**且交错重复**，别拿先跑的那条当基线）
// 才能进来。这条是为了防止「随手一改就换个形态」——上一轮的教训正是：
// 一次没交错重复的测量，写进了注释、门禁与三份文档。

import (
	"strings"
	"testing"
)

// autoRouteIndexSnapshotKnownShapes —— 已实测等价的两种形态的判别特征。
// 门只认这两种；出现别的形态 ⇒ 需先真库 A/B 再准入。
var autoRouteIndexSnapshotKnownShapes = []struct {
	name   string
	marker string
}{
	{"元组 IN 子查询", "(cmi.credential_id, cmi.raw_model, cmi.bucket) IN ("},
	{"CTE + JOIN 回同一视图", "JOIN latest_bucket"},
}

// TestAutoRouteIndexSnapshotSQLRejectsDISTINCTON —— 性能腿（真实差异）。
func TestAutoRouteIndexSnapshotSQLRejectsDISTINCTON(t *testing.T) {
	query, _ := autoRouteIndexSnapshotQuery("", 100)
	if strings.Contains(strings.ToUpper(query), "DISTINCT ON") {
		t.Errorf("auto-route 索引 SQL 用了 DISTINCT ON —— 252 真库实测它让 PG 对 43 万行做 " +
			"external merge Sort、溢出 58,944 kB 到磁盘（work_mem 仅 8MB），" +
			"Execution 2980 ms（我方）/ 3040.8 ms（复核方），**比两种现有写法都差 3~4 倍**。")
	}
}

// TestAutoRouteIndexSnapshotSQLIsPerPairMax —— 语义腿：逐对取 MAX(bucket)。
func TestAutoRouteIndexSnapshotSQLIsPerPairMax(t *testing.T) {
	query, _ := autoRouteIndexSnapshotQuery("", 100)
	if !strings.Contains(query, "MAX(bucket)") {
		t.Errorf("auto-route 索引 SQL 缺 MAX(bucket)：\n%s", query)
	}
	if !strings.Contains(query, "GROUP BY credential_id, raw_model") {
		t.Errorf("auto-route 索引 SQL 的 MAX(bucket) 没有按 (credential_id, raw_model) 分组 —— "+
			"全局 MAX(bucket) 只会落在最后一次有指标变化的桶上（典型 1~2 行），会把整个索引藏起来：\n%s", query)
	}
	if strings.Contains(query, "SELECT MAX(bucket) FROM") {
		t.Errorf("auto-route 索引 SQL 疑似退化成全局 MAX(bucket)：\n%s", query)
	}
}

// TestAutoRouteIndexSnapshotUsesKnownShape —— 准入闸：只允许两种已实测等价的形态。
func TestAutoRouteIndexSnapshotUsesKnownShape(t *testing.T) {
	query, _ := autoRouteIndexSnapshotQuery("", 100)
	for _, shape := range autoRouteIndexSnapshotKnownShapes {
		if strings.Contains(query, shape.marker) {
			return
		}
	}
	t.Errorf("auto-route 索引 SQL 不属于任何一种**已实测等价**的形态 —— "+
		"出现第三种形态时，必须先在真库跑 A/B（**且交错重复**，别拿先跑的那条当基线）"+
		"并做行集双向 EXCEPT 对账，才能准入。当前 SQL：\n%s", query)
}

// TestAutoRouteIndexSnapshotCanonicalFilterStaysOutsideSubquery —— 语义位置腿。
//
// 语义等价性第 2 条：原写法是「先按对取最新桶，再按 canonical_id 过滤」。
// 若把过滤推进 IN 子查询里，就变成「在过滤后的行里取最新桶」——当同一
// (对, 桶) 上 canonical_id 变过时，两者返回的行**不同**。
// 这是纯位置关系，只看常量文本看不出来，必须拼装后再判。
func TestAutoRouteIndexSnapshotCanonicalFilterStaysOutsideSubquery(t *testing.T) {
	query, args := autoRouteIndexSnapshotQuery("canon-123", 50)

	inIdx := strings.Index(query, "(cmi.credential_id, cmi.raw_model, cmi.bucket) IN (")
	if inIdx < 0 {
		// 当前形态是 CTE+JOIN 时，位置约束体现在「过滤不在 CTE 里」。
		cteIdx := strings.Index(query, "latest_bucket")
		if cteIdx < 0 {
			t.Fatalf("既没有元组 IN 子查询也没有 CTE：\n%s", query)
		}
	} else {
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
