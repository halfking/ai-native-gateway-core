// 判据纪律：本文件是 cache-economics 覆盖索引修复的**真库层**。
//
// 缺陷：usage_ledger 上的 (ts, tenant_id) 覆盖索引（856 建的）INCLUDE 列表
// **不含 cache_read_tokens**。cache-economics 要的四列聚合里只要出现
// SUM(cache_read_tokens)，planner 就放弃 index-only scan，退回
// Parallel Index Scan 逐行回堆 —— 30 天 1,478k 行实测
// buffers 209,361 / 3.2s，handler 的 10s 预算被挤爆，端点稳定 500。
//
// 为什么真库层必要：源码层（internal/sqlguard）只能证明**迁移文件写了什么**，
// 只有这条能证明生产上那条聚合**真的**不再回堆。
//
// ★ 三条纪律在本文件里的具体落法：
//  1. SQL 逐字取自 usage_enhanced.go（extractCacheEconomicsAggregateSQL）。
//     最初版本抄了一份 SQL，结果线上真语法错误时判据是绿的 —— 抄的判据
//     测的是抄的东西，不是被测的代码。
//  2. 前提必须先判：提取到的字面量若不含那四个聚合列，测试**立刻失败**，
//     而不是安静地跑一条空查询然后「通过」。
//  3. 阻断侧不能因「被别的东西挡下」而假绿：断言的不是「没有 Seq Scan」
//     （10 月分区只有 79MB，planner 选 Seq Scan 完全正当），而是
//     **真正吃时间的那个分区**是否免于回堆。
package admin

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// cacheEconomicsSourceFile 是被测源码的落点。
const cacheEconomicsSourceFile = "usage_enhanced.go"

func cacheEconomicsSourceText(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(cacheEconomicsSourceFile)
	if err != nil {
		t.Fatalf("read %s: %v", cacheEconomicsSourceFile, err)
	}
	return string(b)
}

// extractCacheEconomicsAggregateSQL 逐字取出 usageCacheEconomics 的主聚合 SQL。
//
// 只替换 %s 占位符（handler 拼的是 whereClause），其余一字不动 ——
// handler 拼进来的 `WHERE ul.ts >= $1 AND ul.ts < $2` 与测试里写的
// now()-interval 形态语义等价（默认 30 天窗口），而参数化形式在 EXPLAIN
// 里拿不到实际剪枝结果。
func extractCacheEconomicsAggregateSQL(t *testing.T) string {
	t.Helper()
	body := cacheEconomicsSourceText(t)

	// ★ 必须先切到 usageCacheEconomics 的**函数体**里再找字面量。
	//   usage_enhanced.go 里有三处 `query := fmt.Sprintf(`（本轮合并后
	//   新增了两处），按整文件取第一个会抓到别的 handler 的 SQL ——
	//   前提断言当场红，判据直接失效。
	//   这是同一族教训的第四次现身：**取子串必须有两端界定**。
	const fnOpen = "func (h *Handler) usageCacheEconomics("
	fnStart := strings.Index(body, fnOpen)
	if fnStart < 0 {
		t.Fatal("cannot locate usageCacheEconomics in " + cacheEconomicsSourceFile)
	}
	// usageCacheEconomics 可能是文件最后一个函数，此时没有后继 `\nfunc `。
	// 那就取到文件末尾 —— 上界缺失不等于无界：**内层的「唯一性」断言
	// 仍然兜得住**，而下面的前提断言会兜住提取到错误 SQL 的情况。
	fnEnd := strings.Index(body[fnStart+len(fnOpen):], "\nfunc ")
	fnBody := body[fnStart:]
	if fnEnd >= 0 {
		fnBody = body[fnStart : fnStart+len(fnOpen)+fnEnd]
	}

	const open = "query := fmt.Sprintf(`"
	if n := strings.Count(fnBody, open); n != 1 {
		t.Fatalf("usageCacheEconomics 里应有且仅有 1 处 %q，实为 %d 处："+
			"提取点已漂移，本判据正在测一条无关的查询", open, n)
	}
	i := strings.Index(fnBody, open)
	rest := fnBody[i+len(open):]
	j := strings.Index(rest, "`, whereClause)")
	if j < 0 {
		t.Fatal("cannot locate the end of the usageCacheEconomics aggregate SQL literal")
	}

	sql := rest[:j]
	if !strings.Contains(sql, "%s") {
		t.Fatal("提取到的 SQL 没有占位符（handler 拼的 whereClause），提取点已失效")
	}

	// 前提断言：这条判据盯的就是这四个聚合列。提取点漂移时必须当场红，
	// 否则后面所有断言都在测一条无关的查询。
	for _, frag := range []string{
		"COUNT(*)",
		"SUM(ul.cache_read_tokens)",
		"SUM(ul.prompt_tokens)",
		"SUM(ul.cost_usd)",
		"FROM usage_ledger_with_current_month ul",
	} {
		if !strings.Contains(sql, frag) {
			t.Fatalf("提取到的 cache-economics 聚合 SQL 不含 %q：提取点已漂移，"+
				"本判据正在测一条无关的查询", frag)
		}
	}

	return strings.Replace(sql, "%s",
		"WHERE ul.ts >= (now() - interval '30 days') AND ul.ts < now()", 1)
}

// ── 真库层 ────────────────────────────────────────────────────────────────

// explainPlanNode 是 EXPLAIN (FORMAT JSON) 的一个节点。
type explainPlanNode struct {
	NodeType    string            `json:"Node Type"`
	Relation    string            `json:"Relation Name"`
	IndexName   string            `json:"Index Name"`
	ActualRows  int64             `json:"Actual Rows"`
	ActualLoops int64             `json:"Actual Loops"`
	Plans       []explainPlanNode `json:"Plans"`
}

// walkLedgerScans 收集计划里所有落在 usage_ledger* 上的扫描节点。
func walkLedgerScans(n explainPlanNode, out *[]explainPlanNode) {
	if n.Relation != "" && strings.HasPrefix(n.Relation, "usage_ledger") {
		*out = append(*out, n)
	}
	for _, c := range n.Plans {
		walkLedgerScans(c, out)
	}
}

// ledgerCoverIndexHasCacheTokens 报告某分区上是否**存在且有效**一条
// (ts, tenant_id) 前缀、INCLUDE 携带缓存 token 列的覆盖索引。
//
// ★ 必须按**定义**匹配而不是按索引名匹配：分区上的索引由 PG 按列名自动
//
//	改名，名字里根本不含 cache，按名字查会得出「没建」的假结论
//	（870 实测踩过）。这条同时是上面那条判据的**前提断言** ——
//	「索引不存在」与「索引存在但没被用上」是两种完全不同的故障，
//	混在一起报就会把方向修反。
func ledgerCoverIndexHasCacheTokens(ctx context.Context, pool *pgxpool.Pool, partition string) (bool, error) {
	var ok bool
	err := pool.QueryRow(ctx, `
		SELECT EXISTS (
		  SELECT 1
		    FROM pg_index i
		   WHERE i.indrelid = to_regclass($1)
		     AND i.indisvalid AND i.indisready
		     AND pg_get_indexdef(i.indexrelid, 1, true) = 'ts'
		     AND pg_get_indexdef(i.indexrelid, 2, true) = 'tenant_id'
		     AND pg_get_indexdef(i.indexrelid) LIKE '%INCLUDE (prompt_tokens, completion_tokens, cache_read_tokens%'
		)`, partition).Scan(&ok)
	return ok, err
}

// TestCacheEconomicsAggregateIsServedWithoutHeapFetch 真库判据：
//
// ① 前提：每个 usage_ledger 分区上都有一条携带缓存 token 列的覆盖索引。
//
//	（873 迁移之前必然红 —— 这就是它的作用）
//
// ② 结果：30 天窗口那条聚合的 EXPLAIN 里，真正吃时间的分区
//
//	（9 月，1,039,470 行）必须走 **Index Only Scan**。
//
// 故意**不**断言「没有 Seq Scan」：10 月分区只有 79MB / 44 万行，
// planner 选 Parallel Seq Scan 是完全正当的优化，用它当判据会把
// 「索引建对了但没被选中」和「压根没有覆盖索引」混为一谈。
func TestCacheEconomicsAggregateIsServedWithoutHeapFetch(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	// ① 前提：逐分区确认覆盖索引已就位。
	var partitions []string
	rows, err := pool.Query(ctx, `
		SELECT c.relname
		  FROM pg_class c
		  JOIN pg_inherits i ON i.inhrelid = c.oid
		 WHERE i.inhparent = 'public.usage_ledger'::regclass
		 ORDER BY 1`)
	if err != nil {
		t.Fatalf("list partitions: %v", err)
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			t.Fatalf("scan partition: %v", err)
		}
		partitions = append(partitions, name)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate partitions: %v", err)
	}
	if len(partitions) == 0 {
		t.Fatal("没有查到任何 usage_ledger 分区，判据前提不成立")
	}

	var missing []string
	for _, part := range partitions {
		ok, err := ledgerCoverIndexHasCacheTokens(ctx, pool, "public."+part)
		if err != nil {
			t.Fatalf("check covering index on %s: %v", part, err)
		}
		if !ok {
			missing = append(missing, part)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("分区 %v 上没有携带 cache_read_tokens 的 (ts, tenant_id) 覆盖索引："+
			"带 SUM(cache_read_tokens) 的窗口聚合会退回逐行回堆。请应用迁移 873"+
			"（注意：分区索引按列名自动改名，不要按名字 grep）", missing)
	}

	// ② 结果：用被测源码逐字取出的 SQL 跑真实计划。
	sql := extractCacheEconomicsAggregateSQL(t)
	var raw []byte
	if err := pool.QueryRow(ctx, "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) "+sql).Scan(&raw); err != nil {
		t.Fatalf("explain: %v", err)
	}

	var plan []struct {
		Plan explainPlanNode `json:"Plan"`
	}
	if err := json.Unmarshal(raw, &plan); err != nil {
		t.Fatalf("unmarshal plan: %v", err)
	}
	if len(plan) == 0 {
		t.Fatal("EXPLAIN 返回空计划，判据前提不成立")
	}

	var scans []explainPlanNode
	walkLedgerScans(plan[0].Plan, &scans)
	if len(scans) == 0 {
		t.Fatalf("计划里没有落在 usage_ledger 上的扫描节点：\n%s", raw)
	}

	// 只看真正吃时间的分区。小分区走 Seq Scan 不构成缺陷。
	const heavyRows = 100_000
	for _, s := range scans {
		rowsRead := s.ActualRows
		if s.ActualLoops > 1 {
			rowsRead = s.ActualRows * s.ActualLoops / 3 // 并行下每 worker 约 1/3
		}
		if rowsRead < heavyRows {
			continue
		}
		if s.NodeType != "Index Only Scan" {
			t.Errorf("分区 %s 在 30 天窗口里读到约 %d 行却走的是 %q"+
				"（索引 %q）：这几万行的指标列要逐行回堆取，"+
				"覆盖索引没被用上就是这条缺陷还在", s.Relation, rowsRead, s.NodeType, s.IndexName)
		}
	}
	t.Logf("ledger scans: %d", len(scans))
	for _, s := range scans {
		t.Logf("  %s: %s rows=%d loops=%d index=%q",
			s.Relation, s.NodeType, s.ActualRows, s.ActualLoops, s.IndexName)
	}
}
