package bg

import (
	"os"
	"strings"
	"testing"
)

// ── 会话族刷新必须**串行**（2026-11-20 981 族 / 2026-11-22 全族）────────────
//
// 缺陷形态：刷新器对会话族统一设 work_mem=256MB ⇒ PG 为重视图选**并行计划**
// ⇒ 需要动态共享内存（DSM）。211 的 PG 容器 /dev/shm 只有 64MB
// （Docker 默认 ShmSize=67108864）⇒ 段分配必然失败：
//
//	ERROR: could not resize shared memory segment "/PostgreSQL.xxx"
//	       to 8388608 bytes: No space left on device (SQLSTATE 53100)
//
// ⇒ 该视图**永远刷不上** ⇒ 刷新戳永远不落
// ⇒ admin/session_analytics 的新鲜度门（90min 预算）永远判陈旧
// ⇒ 端点恒 503 analytics_view_stale。
//
// 两次发病记录：
//   · 2026-11-20：981 族的 model_usage_aggregates（要 16MB 段）。
//   · 2026-11-22：357 族的 session_client_task_matrix（要 8MB 段）——
//     owner 报障页面 /admin/session-analytics/clients/default 的直接根因；
//     当天 11:05Z 还成功、12:05Z 起连续三轮 53100 ⇒ **并行阈值会随数据量/
//     统计信息演化自己走过来**，不能按「今天还没发病」排除。
//
// 实测对照（211 acc-pg17 / 252 pg-252-pg17，同参数）：
//	211: matrix 并行 → 0.5s 内失败（53100）；串行 → 0.596s 成功
//	252: matrix 串行 117.6s（并行 mean 102.6s，+15%）
//	     task_stats 串行 12.1s（并行 mean 9.3s）；client_stats 串行 5.2s（并行 3.4s）
//
// ★ 为什么不用「失败后重试串行」兜底：刷新器对连续失败有**告警**
//   （failureCount ≥ 2 ⇒ alertCallback）⇒ 每轮先失败一次 ⇒ 每轮踩告警线
//   ⇒ 假告警比假数据更坏。⇒ 走确定性路线：一开始就串行。
//
// ★ 为什么 2026-11-22 起 357 族三张一起进集合（订正此前「旧族不动」）：
//   同上——阈值会自己走过来；两台真库实测串行代价与并行同量级；
//   「实测未发病」不构成豁免（matrix 发病前一天也「没发病」）。

// TestSerialRefreshViews_CoversSessionFamily 钉测：**清单里的每个视图**都必须
// 在串行集合里。
//
// ★ 2026-11-22 从「钉 981 族 4 个」扩成「钉清单全集」：两个族先后在同一台
// 211 上发病（相隔两天），说明这是**族级风险**而不是视图级个案——
// 只要清单里有视图不在串行集合，它就是下一个等发病的。
// 这条不变量同时防住「清单加视图忘登记串行集合」的漏配。
func TestSerialRefreshViews_CoversSessionFamily(t *testing.T) {
	for _, v := range SessionAnalyticsViews {
		if !serialRefreshViews[v] {
			t.Errorf("视图 %q 不在串行刷新集合里 ⇒ 它会选并行计划 ⇒ "+
				"64MB /dev/shm 的 PG 容器上一旦越过并行阈值就 53100 ⇒ "+
				"刷新戳不落 ⇒ 新鲜度门恒 503（981 族 2026-11-20、357 族 2026-11-22 两次发病）", v)
		}
	}
}

// TestRefreshView_IssuesParallelDisableForSerialViews 钉测：接线点真的存在。
//
// ★ 只断言「有个 serialRefreshViews 变量」不够 —— 它可能是死代码
//
//	（本仓有 coordination 指标族零调用死代码的先例）。
//	⇒ 这里断言 refreshView 的方法体内**真的**按视图查表并 SET。
func TestRefreshView_IssuesParallelDisableForSerialViews(t *testing.T) {
	b, err := os.ReadFile("materialized_view_refresher.go")
	if err != nil {
		t.Fatalf("读源文件：%v", err)
	}
	src := string(b)
	at := strings.Index(src, "func (r *MaterializedViewRefresher) refreshView(")
	if at < 0 {
		t.Fatal("没找到 refreshView")
	}
	end := strings.Index(src[at:], "\n}\n")
	if end < 0 {
		t.Fatal("refreshView 抽取失败")
	}
	body := src[at : at+end]

	if !strings.Contains(body, "serialRefreshViews[viewName]") {
		t.Fatal("refreshView 里没有按视图查 serialRefreshViews —— 串行集合是死代码")
	}
	if !strings.Contains(body, "SET max_parallel_workers_per_gather = 0") {
		t.Fatal("refreshView 里没有关并行查询 —— DSM 失败（53100）照旧发生")
	}
	// 必须有复位：连接要归还池子，下一条语句不该继承被改过的会话参数。
	if !strings.Contains(body, "RESET max_parallel_workers_per_gather") {
		t.Fatal("没有 RESET max_parallel_workers_per_gather —— 连接归还池后仍带着串行设置")
	}
}
