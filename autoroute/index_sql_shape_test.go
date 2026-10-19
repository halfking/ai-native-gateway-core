package autoroute

// index_sql_shape_test.go —— auto-route **热路径**索引刷新 SQL 的形态门。
//
// 2026-10-20（R51 96h 轮）：admin 侧同款 SQL 上一轮已改写成「元组 IN 子查询」
// 并配了 5 条形态门（admin/auto_route_index_snapshot_sql_test.go），
// 但**热路径这一份（autoroute/index.go 的 refreshIndexSQL）没有任何门** ——
// 门禁缺口：同一个形态、同样的坑，只护住了一半。
//
// 本轮先在 252 生产库把热路径这一份量清楚（只读），再据此定门。
//
// ── 真库实测（2026-10-20，252 生产库，容器 pg-252-pg17）──────────────────
//
// 三个候选形态，只有「最新桶去重」那一段不同，其余 JOIN/WHERE/ORDER BY
// 完全一致：
//
//	A = CTE + JOIN（**现形态** refreshIndexSQL）
//	B = 元组 IN 子查询
//	C = DISTINCT ON
//
// **语义对账**（EXCEPT ALL 双向）：A/B/C 各 63 行，
// A_minus_B=0, B_minus_A=0, A_minus_C=0, C_minus_A=0, B_minus_C=0, C_minus_B=0
// ⇒ 三形态**行集完全等价**。
//
// **性能**（先预热，再交错 ABAB 交替 ×6 轮，报分布 ——
// 别拿先跑的那条当基线，先跑的是冷的）：
//
//	形态                     min      中位      max      均值    组内极差
//	A  CTE+JOIN（现形态）   297.9ms  339.9ms  439.9ms  355.0ms   142.0
//	B  元组 IN              239.3ms  428.2ms  568.4ms  420.0ms   329.1
//	C  DISTINCT ON          722.8ms  896.1ms 1013.6ms  885.2ms   290.8
//
// ⇒ **A vs B：中位差 −88.3ms，但组内极差（142.0 / 329.1）远大于组间差
//　⇒ 落在噪声带内 ⇒ 性能无实质差异**（与 admin 版同构的结论）。
// ⇒ **C 真慢 2~3 倍**：中位是 A 的 2.64×、B 的 2.09× ⇒ **远超噪声带，成立**。
//
// **EXPLAIN (ANALYZE, BUFFERS) 对 A**：Append → Index Scan（hot + 2026_09/
// 2026_10/2026_11 分区）→ Hash Join → Hash Left Join → Gather Merge → Sort；
// Execution Time 438.219 ms，Planning Time 149.129 ms。
//
// ⇒ **结论：现形态 A 没有性能问题可修，热路径不该动**。
// 　 这道门**不是**为了性能去改写 A，而是为了**防回退**：
// 　 防的是有人把 A 换成 C（真慢 2~3 倍），或换成第三种没量过的形态，
// 　 或把「去重」挪到「过滤」之后（语义变了），或把事实源换成冷父表
// 　 （最新 5 分钟的 rollup 看不见了）。
//
// ⚠ 本门**不**以性能为由钉形态：现形态与元组 IN 的差异落在噪声带内
// （admin 版上一轮正是把冷热缓存差异读成了 −59% 的真实收益，已撤回）。
// 这里钉的是四件**真实**的事：禁 DISTINCT ON、逐对 MAX(bucket)、
// 形态准入闸、去重位置、以及事实源。

import (
	"strings"
	"testing"
)

// stripSQLComments 去掉 SQL 里的行注释与块注释。
//
// ⚠ 为什么判据必须先剥注释：refreshIndexSQL **满是有注释的 SQL**
// （"-- CHANNEL_QUALITY_ROUTING: ..." 之类）。直接在原始文本里搜
// "DISTINCT ON"，只要有人写一句解释"这里没用 DISTINCT ON"的注释就会误报
// —— 门于是把**注释**当成违规，或者反过来：写注释的人为了让门绿去改注释，
// 下一个人写注释就重新踩。⇒ 模式类判据一律扫**剥掉注释后的代码位**。
//
// 单引号字符串内的内容原样保留（SQL 里的 'manual%' 之类不能被当成注释）。
func stripSQLComments(sql string) string {
	var b strings.Builder
	b.Grow(len(sql))

	inString := false
	for i := 0; i < len(sql); {
		c := sql[i]
		switch {
		case inString:
			b.WriteByte(c)
			if c == '\'' {
				// '' 是转义的单引号，不算结束。
				if i+1 < len(sql) && sql[i+1] == '\'' {
					b.WriteByte('\'')
					i += 2
					continue
				}
				inString = false
			}
			i++
		case c == '\'':
			inString = true
			b.WriteByte(c)
			i++
		case c == '-' && i+1 < len(sql) && sql[i+1] == '-':
			// 行注释：跳到行尾（保留换行，让行号/排版不变）。
			for i < len(sql) && sql[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(sql) && sql[i+1] == '*':
			i += 2
			for i+1 < len(sql) && !(sql[i] == '*' && sql[i+1] == '/') {
				if sql[i] == '\n' {
					b.WriteByte('\n')
				}
				i++
			}
			i += 2
			if i > len(sql) {
				i = len(sql)
			}
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

// autoRouteRefreshIndexKnownShapes —— 已实测等价的两种形态的判别特征。
// 门只认这两种；出现别的形态 ⇒ 需先真库 A/B（**且交错重复**，别拿先跑
// 的那条当基线）才能进来。
var autoRouteRefreshIndexKnownShapes = []struct {
	name   string
	marker string
}{
	{"CTE + JOIN 回同一视图", "JOIN latest_bucket"},
	{"元组 IN 子查询", "(cmi.credential_id, cmi.raw_model, cmi.bucket) IN ("},
}

// autoRouteRefreshIndexSourceView 是热路径必须读的事实源。
//
// 读冷父表（credential_model_index）会**看不见最新 5 分钟的 rollup** ——
// bg.AutoIndexRefresher 写的是 hot 表，promote 函数只迁移超过 24h 的行，
// 所以最新的数据只存在于 union 视图里。
const autoRouteRefreshIndexSourceView = "credential_model_index_with_current_month"

// TestAutoRouteRefreshIndexRejectsDISTINCTON —— 性能腿（真实差异）。
//
// 变异：把去重段改成 `SELECT DISTINCT ON (credential_id, raw_model) ...`
// ⇒ 本门红。
func TestAutoRouteRefreshIndexRejectsDISTINCTON(t *testing.T) {
	sqlText := stripSQLComments(refreshIndexSQL)
	if strings.Contains(strings.ToUpper(sqlText), "DISTINCT ON") {
		t.Errorf("auto-route 热路径索引 SQL 用了 DISTINCT ON —— 252 真库实测（2026-10-20）："+
			"三形态行集完全等价（EXCEPT ALL 双向差集全 0，各 63 行），但 DISTINCT ON 的中位耗时 "+
			"896.1ms，是现形态（CTE+JOIN，339.9ms）的 2.64×、元组 IN（428.2ms）的 2.09×，"+
			"**远超噪声带**（组内极差 142~329ms）⇒ 这是本轮唯一站得住的性能差异。\n%s", refreshIndexSQL)
	}
}

// TestAutoRouteRefreshIndexIsPerPairMax —— 语义腿：逐对取 MAX(bucket)。
//
// 变异：把 `GROUP BY credential_id, raw_model` 去掉，或把 MAX(bucket)
// 改成全局 MAX ⇒ 本门红。
func TestAutoRouteRefreshIndexIsPerPairMax(t *testing.T) {
	sqlText := stripSQLComments(refreshIndexSQL)
	if !strings.Contains(sqlText, "MAX(bucket)") {
		t.Errorf("auto-route 热路径索引 SQL 缺 MAX(bucket)：\n%s", refreshIndexSQL)
	}
	if !strings.Contains(sqlText, "GROUP BY credential_id, raw_model") {
		t.Errorf("auto-route 热路径索引 SQL 的 MAX(bucket) 没有按 (credential_id, raw_model) 分组 —— "+
			"全局 MAX(bucket) 只会落在最后一次有指标变化的桶上（典型 1~2 行），会把整个索引藏起来：\n%s", refreshIndexSQL)
	}
	if strings.Contains(sqlText, "SELECT MAX(bucket) FROM") {
		t.Errorf("auto-route 热路径索引 SQL 疑似退化成全局 MAX(bucket)：\n%s", refreshIndexSQL)
	}
}

// TestAutoRouteRefreshIndexUsesKnownShape —— 准入闸：只允许两种已实测等价的形态。
//
// 变异：把去重段换成第三种形态（例如 `WHERE bucket = (SELECT MAX(bucket) ...)`）
// ⇒ 本门红。
func TestAutoRouteRefreshIndexUsesKnownShape(t *testing.T) {
	sqlText := stripSQLComments(refreshIndexSQL)
	for _, shape := range autoRouteRefreshIndexKnownShapes {
		if strings.Contains(sqlText, shape.marker) {
			return
		}
	}
	t.Errorf("auto-route 热路径索引 SQL 不属于任何一种**已实测等价**的形态 —— "+
		"出现第三种形态时，必须先在真库跑 A/B（**且交错重复**，别拿先跑的那条当基线）"+
		"并做行集双向 EXCEPT 对账，才能准入。\n"+
		"已实测等价的两种：CTE+JOIN（现形态）、元组 IN；DISTINCT ON 实测真慢 2~3 倍，不在准入名单里。\n%s",
		refreshIndexSQL)
}

// TestAutoRouteRefreshIndexDedupStaysBeforeFilters —— 位置腿：
// **去重必须发生在过滤之前**。
//
// 语义是「先按 (credential_id, raw_model) 取最新桶，再按可用性过滤」。
// 若把过滤推进去重段，就变成「在过滤后的行里取最新桶」——当某个桶上
// 该对的所有行都被过滤掉时，两者返回的行**不同**。
//
// 这条对两种准入形态都要成立：
//   · CTE+JOIN 形态：去重段（latest_bucket CTE）**不得**引用任何被过滤的表，
//     也不得自带 WHERE —— 它只能读事实源视图本身。
//   · 元组 IN 形态：IN 子查询内部不得带过滤条件。
//
// 变异：把 WHERE 里的可用性过滤搬进 latest_bucket CTE ⇒ 本门红。
func TestAutoRouteRefreshIndexDedupStaysBeforeFilters(t *testing.T) {
	dedupBody := autoRouteRefreshIndexDedupBody(t)

	// 去重段只允许读事实源视图；一旦引用了被过滤的表，
	// 就意味着过滤被推进了去重之内。
	forbidden := []string{
		"credentials",
		"credential_model_bindings",
		"provider_models",
		"providers",
		"models_canonical",
		"node_probe_state",
	}
	for _, f := range forbidden {
		if strings.Contains(dedupBody, f) {
			t.Errorf("auto-route 热路径索引 SQL 的去重段引用了被过滤的表 %q —— "+
				"过滤被推进了去重之内，语义从「先按对取最新桶、再过滤」变成了"+
				"「在过滤后的行里取最新桶」，两者返回行可能不同。\n去重段：\n%s", f, dedupBody)
		}
	}
	if strings.Contains(strings.ToUpper(dedupBody), "WHERE") {
		t.Errorf("auto-route 热路径索引 SQL 的去重段自带 WHERE —— "+
			"过滤必须在去重之外（见 refreshIndexSQL 的注释与本仓的语义等价约定）：\n%s", dedupBody)
	}
}

// TestAutoRouteRefreshIndexReadsUnionView —— 事实源腿。
//
// 读冷父表 credential_model_index 会**看不见最新 5 分钟的 rollup**：
// bg.AutoIndexRefresher 写的是 hot 表，promote 函数只迁移超过 24h 的行，
// 所以最新数据只存在于 union 视图 credential_model_index_with_current_month。
//
// 变异：把事实源换成冷父表 ⇒ 本门红。
func TestAutoRouteRefreshIndexReadsUnionView(t *testing.T) {
	sqlText := stripSQLComments(refreshIndexSQL)
	if !strings.Contains(sqlText, autoRouteRefreshIndexSourceView) {
		t.Errorf("auto-route 热路径索引 SQL 没有读事实源视图 %q —— "+
			"读冷父表会看不见最新 5 分钟的 rollup（hot 表里才有最新数据，"+
			"promote 只迁移超过 24h 的行）⇒ 索引会系统性滞后一个 promote 周期。\n%s",
			autoRouteRefreshIndexSourceView, refreshIndexSQL)
	}
	// 直接读冷父表是另一条独立路径，必须不出现。
	if strings.Contains(sqlText, "FROM credential_model_index ") ||
		strings.Contains(sqlText, "FROM credential_model_index\n") {
		t.Errorf("auto-route 热路径索引 SQL 直接读了冷父表 credential_model_index —— "+
			"最新 5 分钟的 rollup 只在 union 视图里。\n%s", refreshIndexSQL)
	}
}

// TestAutoRouteRefreshIndexKeepsOrderAndFilters —— 收尾形态腿。
//
// ORDER BY 与关键可用性过滤是这个热路径的**行为契约**：一个是排序结果，
// 一个是「只有可路由的候选才进索引」。丢任何一条都会静默改变路由行为。
//
// 变异：删掉 ORDER BY 或删掉 `cmb.available = TRUE` ⇒ 本门红。
func TestAutoRouteRefreshIndexKeepsOrderAndFilters(t *testing.T) {
	sqlText := stripSQLComments(refreshIndexSQL)
	if !strings.Contains(sqlText, "ORDER BY cmi.canonical_id, cmi.score_smart DESC") {
		t.Errorf("auto-route 热路径索引 SQL 丢了 ORDER BY cmi.canonical_id, cmi.score_smart DESC —— "+
			"排序是热路径的行为契约。\n%s", refreshIndexSQL)
	}
	required := []string{
		"cmb.available = TRUE",
		"pm.available = TRUE",
		"COALESCE(cr.lifecycle_status, 'active') = 'active'",
		"COALESCE(cr.status, 'active') = 'active'",
		"COALESCE(cr.manual_disabled, false) = false",
	}
	for _, frag := range required {
		if !strings.Contains(sqlText, frag) {
			t.Errorf("auto-route 热路径索引 SQL 丢了关键过滤 %q —— "+
				"删掉它会让不可路由的候选进索引 ⇒ 路由行为静默改变。\n%s", frag, refreshIndexSQL)
		}
	}
}

// TestAutoRouteRefreshIndexGateIgnoresComments —— 防假阳性自证。
//
// 上一条纪律（判据在原始文本里搜模式 ⇒ 注释里的同名字样也算违规）的
// 正反两面都要有：
//   · **注释里的 "DISTINCT ON" 不得触发性能腿**（否则写一句解释就会误报，
//     下一个人为了让门绿去改注释 —— 门把注释当成了契约）；
//   · **真代码位里的 "DISTINCT ON" 仍必须被抓**（只补前者会退化成
//     「什么都抓不到」的恒绿门）。
func TestAutoRouteRefreshIndexGateIgnoresComments(t *testing.T) {
	got := stripSQLComments("SELECT 1 -- DISTINCT ON (x)\nFROM t")
	if strings.Contains(strings.ToUpper(got), "DISTINCT ON") {
		t.Errorf("stripSQLComments 没有剥掉行注释：%q", got)
	}
	got2 := stripSQLComments("SELECT /* DISTINCT ON */ 1 FROM t")
	if strings.Contains(strings.ToUpper(got2), "DISTINCT ON") {
		t.Errorf("stripSQLComments 没有剥掉块注释：%q", got2)
	}
	// 单引号字符串里的内容不能被当成注释。
	got3 := stripSQLComments("SELECT 'a -- b' FROM t")
	if !strings.Contains(got3, "'a -- b'") {
		t.Errorf("stripSQLComments 误伤了单引号字符串：%q", got3)
	}
	// 真代码位仍要原样保留。
	got4 := stripSQLComments("SELECT DISTINCT ON (x) a FROM t")
	if !strings.Contains(strings.ToUpper(got4), "DISTINCT ON") {
		t.Errorf("stripSQLComments 误伤了真代码位：%q", got4)
	}
}

// autoRouteRefreshIndexDedupBody 取出「去重段」的文本。
//
// CTE+JOIN 形态：latest_bucket CTE 的函数体。
// 元组 IN 形态：IN 子查询的文本。
// 两种形态都取不到时返回空串（由调用方的其它门兜底）。
func autoRouteRefreshIndexDedupBody(t *testing.T) string {
	t.Helper()

	sqlText := stripSQLComments(refreshIndexSQL)

	if idx := strings.Index(sqlText, "WITH latest_bucket AS ("); idx >= 0 {
		rest := sqlText[idx+len("WITH latest_bucket AS ("):]
		if end := strings.Index(rest, ")\n"); end >= 0 {
			return rest[:end]
		}
		return rest
	}

	if idx := strings.Index(sqlText, "(cmi.credential_id, cmi.raw_model, cmi.bucket) IN ("); idx >= 0 {
		rest := sqlText[idx+len("(cmi.credential_id, cmi.raw_model, cmi.bucket) IN ("):]
		if end := strings.Index(rest, ")\n"); end >= 0 {
			return rest[:end]
		}
		return rest
	}

	t.Fatalf("既没有 CTE 也没有元组 IN —— 去重段无法定位：\n%s", refreshIndexSQL)
	return ""
}
