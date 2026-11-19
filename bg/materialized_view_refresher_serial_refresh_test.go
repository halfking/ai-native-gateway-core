package bg

import (
	"os"
	"strings"
	"testing"
)

// ── 981 族刷新必须**串行**（2026-11-20 211 实测）────────────────────────────
//
// 缺陷形态：刷新器对会话族统一设 work_mem=256MB ⇒ PG 为最重的
// model_usage_aggregates（请求级 / 40 万行）选**并行计划** ⇒ 需要动态共享内存
// （DSM）。211 的 PG 容器 /dev/shm 只有 64MB（Docker 默认 ShmSize=67108864）
// ⇒ 分配 16MB 段必然失败：
//
//	ERROR: could not resize shared memory segment "/PostgreSQL.4193532418"
//	       to 16777216 bytes: No space left on device (SQLSTATE 53100)
//
// ⇒ 该视图**永远刷不上** ⇒ 刷新戳永远不落
// ⇒ admin/session_analytics 的新鲜度门（90min 预算）永远判陈旧
// ⇒ 端点恒 503 analytics_view_stale。
//
// 实测对照（211 acc-pg17，同参数）：
//	并行开启  → 0.5s 内失败（53100）
//	关并行    → 2.6s 刷新成功
//
// ★ 为什么不用「失败后重试串行」兜底：刷新器对连续失败有**告警**
//   （failureCount ≥ 2 ⇒ alertCallback）⇒ 每轮先失败一次 ⇒ 每轮踩告警线
//   ⇒ 假告警比假数据更坏。⇒ 走确定性路线：一开始就串行。

// TestSerialRefreshViews_Covers981Family 钉测：981 族 4 个视图必须在串行集合里。
func TestSerialRefreshViews_Covers981Family(t *testing.T) {
	for _, v := range []string{
		"user_session_aggregates",
		"project_task_aggregates",
		"agent_usage_aggregates",
		"model_usage_aggregates",
	} {
		if !serialRefreshViews[v] {
			t.Errorf("视图 %q 不在串行刷新集合里 ⇒ 它会选并行计划 ⇒ "+
				"64MB /dev/shm 的 PG 容器上必然 53100 ⇒ 刷新戳不落 ⇒ 新鲜度门恒 503", v)
		}
	}
}

// TestSerialRefreshViews_DoesNotTouchLegacyFamily 钉测：旧族（357）一行不改。
//
// ★ 零爆炸半径：252 生产上旧族最慢的那条实测 ≥180s（被 kill），
//
//	给旧族关并行可能把 180s 顶到 600s 语句上限之外 ⇒ 只对新增的 981 族关并行。
func TestSerialRefreshViews_DoesNotTouchLegacyFamily(t *testing.T) {
	for _, v := range []string{
		"session_client_stats",
		"session_task_stats",
		"session_client_task_matrix",
	} {
		if serialRefreshViews[v] {
			t.Errorf("视图 %q 被加进了串行集合 —— 旧族不在本次改动范围内，"+
				"且 252 上最慢的一条实测 ≥180s，关并行有顶穿语句上限的风险", v)
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
