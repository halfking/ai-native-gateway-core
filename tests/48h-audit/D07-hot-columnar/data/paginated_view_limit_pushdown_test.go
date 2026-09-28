// Package data - D07 数据测试（第五批）：**LIMIT 看起来在约束工作量，其实没有**。
//
// # 与批游标同型的一个缺陷，出现在查询面
//
// R79 在存储函数侧抓到过一个形状：一个 `ORDER BY <cols> LIMIT <n>` 的批游标，
// 如果游标列没有首列索引，成本是 O(rows²)。本门抓的是**同一个误解在查询面的形态**：
//
//	SELECT ... FROM <view> WHERE <ts 范围> ORDER BY ts DESC LIMIT <page_size>
//
// 读起来像「只要一页」，实测**页大小一点也没约束工作量**。
//
// # 真库实测（PostgreSQL 17.10，2026-09-29）——受控对照
//
// 同一时间窗（2026-09-26 一整天）、同一条 `ORDER BY ts DESC LIMIT 10`，
// 只改 FROM 来源，其余完全相同：
//
//	| FROM 来源                                        | 执行时间 | 计划形状                                    |
//	|--------------------------------------------------|---------|---------------------------------------------|
//	| 直查 request_logs                                 |  0.288ms| Index Scan，LIMIT 下推                        |
//	| 内层嵌套视图（含 LATERAL，**不含**两个反连接）    |  0.120ms| `Merge Append` + `Limit loops=10`，**下推**   |
//	| 完整视图 request_logs_with_current_month          | 12390ms | `Append (actual rows=404794)`，**下推失效**  |
//
// 第二次与第三次之间**只差两个 NOT EXISTS**：
//
//	WHERE NOT (EXISTS (SELECT 1 FROM session_turns_hot th WHERE th.request_id = rl.request_id))
//	  AND NOT (EXISTS (SELECT 1 FROM session_turns      tp WHERE tp.request_id = rl.request_id))
//
// 即：相关反连接让规划器无法对 UNION ALL 的各分支单独 top-N 取数再归并，
// 只能先把所有命中行物化出来、做两轮索引探测、再排序。
//
// # 「页大小约束不住工作量」的直接证据
//
//	LIMIT 10  → Append (actual rows=404794)
//	LIMIT 1000→ Append (actual rows=404794)      ← 一模一样
//
// 缓冲区读：**9,216,949 个 block**（hit 8,991,503 + read 225,446 ≈ 70 GB 逻辑读）换回 10 行。
// 这不是深翻页的问题——第 1 页和第 500 页一样贵，因为贵的部分在 LIMIT 之前就已经付完了。
//
// 接口侧 `admin/logs.go:470` 给的是 30s ctx 预算，`:482-488` 的 pageSize 上限 500、
// 但 `page` 本身**无上限**（`:478-481` 只夹下界）。窗口由 R37 的 366 天上限兜着，
// 所以不会无限放大，但一天窗口就已经是 12.4s。
//
// # 定级：P1，但不由本轮修
//
// 爆炸半径：视图被 `admin/logs.go`、`bg/stats_minute_rollup.go`、
// `domains/routeincident/store.go`、`db/probe_views_unified.go`、`maas/usage.go` 共用，
// 且有自愈重建链（`db/request_logs_view_schema.go`）+ 迁移 575/577/680/696/700/717
// 与一整套视图列数契约测试（113/115 列冻结契约）。
// **改视图是迁移 + schema 契约决策，本审计轮只定位与登记，不替 owner 改。**
//
// # 这道门为什么做成「登记制」而不是「判红制」
//
// 「视图里有相关子查询」本身**不是缺陷**——大量视图正确地使用它们。
// 真正该拦的是「**这个**形态出现在**被分页查询引用**的视图上」，而那正是本门的判据：
// 目录侧找带相关子查询的视图，仓内侧找对该视图做 `ORDER BY ... LIMIT` 的分页引用，
// 两侧求交集。命中必须有书面理由，否则判红；理由失效同样判红（白名单自收缩）。
//
// 跑测（无库自动 skip；只需能读 pg_views 的只读角色）：
//
//	D07_S01_PG_URL=postgres://reader@127.0.0.1:5432/llm_gateway?sslmode=disable \
//	  go test -timeout 180s ./tests/48h-audit/D07-hot-columnar/data/...
package data

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// correlatedSubqueryPattern matches a correlated EXISTS/NOT EXISTS in a view body.
// A bare `SELECT EXISTS (...)` over no outer reference is not the same thing, but
// distinguishing it textually is not worth the false-negative risk: this gate
// registers hits rather than asserting they are bugs, so the coarser match costs
// a justification line, not a red build.
var correlatedSubqueryPattern = regexp.MustCompile(`(?i)\bNOT\s+EXISTS\s*\(|\bEXISTS\s*\(`)

// goSQLLiteral matches a double-quoted Go string literal long enough to hold SQL.
var goSQLLiteral = regexp.MustCompile("(?s)\"([^\"]{20,})\"")

// sqlIdentifier tokenizes the inside of such a literal so quoted sources that
// carry aliases ("<view> AS r") still yield the bare view name.
var sqlIdentifier = regexp.MustCompile(`\b([a-z_][a-z0-9_]{3,})\b`)

// paginatedUsePattern is deliberately loose on purpose: within one SQL literal we
// only need to know the view is read in a way that orders and limits. Requiring
// the ORDER BY and LIMIT to sit in the same SELECT would drop real multi-statement
// literals, and this gate errs toward showing a hit for registration.
func literalIsPaginated(s string) bool {
	up := strings.ToUpper(s)
	return strings.Contains(up, "ORDER BY") && strings.Contains(up, "LIMIT")
}

// justifiedPaginatedAntiJoinViews is the allowlist. Each entry records the
// measured cost so the finding survives in code, not only in a report.
var justifiedPaginatedAntiJoinViews = map[string]string{
	// P1（2026-09-29 实测，未修）：两个相关 NOT EXISTS 反连接打掉 LIMIT 下推。
	// 实测（PG 17.10，2026-09-26 单日窗口，ORDER BY ts DESC LIMIT 10）：
	//   直查 request_logs                      0.288ms（Index Scan，LIMIT 下推）
	//   内层嵌套视图（含 LATERAL，无反连接）    0.120ms（Merge Append + Limit loops=10）
	//   完整视图                               12390ms（Append actual rows=404794，下推失效）
	// LIMIT 10 与 LIMIT 1000 的 Append 行数完全相同（均 404794）→ 页大小不约束工作量。
	// 缓冲区读 9,216,949 block（≈70GB 逻辑读）换回 10 行；接口 ctx 预算 30s
	// （admin/logs.go:470）。
	// 爆炸半径：admin/logs.go、bg/stats_minute_rollup.go、domains/routeincident/store.go、
	// db/probe_views_unified.go、maas/usage.go + 迁移 575/577/680/696/700/717 +
	// db/request_logs_view_schema.go 自愈重建链 + 视图 113/115 列冻结契约测试。
	// 改视图是迁移 + schema 契约决策，待 owner。
	"request_logs_with_current_month": "P1：两个相关 NOT EXISTS 使 1 天窗口 12.39s / 9.2M buffer；LIMIT 不约束工作量；改视图属迁移决策",

	// 下面三条是**逐个核实过的分诊结果**，不是「先登记再说」。本门的仓内扫描是
	// 文件级粒度（同文件里只要有一处分页就算命中），所以天然会有假阳性——
	// 处理办法是把假阳性也写进来并注明理由，而不是让它红着或悄悄删掉。
	// 每条的核对依据写在下面的注释里。
	//
	// 已核实为**假阳性**：全仓没有任何生产查询把它当 FROM 源读。命中的三处是
	// domains/streaming/model_alternatives_test.go:301 的断言、admin/diagnostics_routing.go:104
	// 的 fmt.Errorf 文案、provider/sql_dump_test.go:22 的清单——都不是查询。
	"v_routable_credential_models": "假阳性：仅出现于测试断言/错误文案，无生产查询读它",

	// 已核实为**真阳性候选（未测）**：admin/auto_route.go:1084 确认是
	// `FROM v_task_model_ranking WHERE ... ORDER BY affinity DESC, sample_count DESC LIMIT $4`。
	// 本轮未对它做 EXPLAIN 实测（下推是否真的失效未验证），故只登记为待测。
	"v_task_model_ranking": "真阳性候选（未测）：admin/auto_route.go:1084 ORDER BY+LIMIT 直查该视图",

	// 混合：`cmd/gateway/dual_read_validator.go:167` 与
	// `cmd/tools/validate_sessions_v2/loader.go:258` 两处读它但**没有** LIMIT（非分页）；
	// `domains/sessionsummary/message_source_v2.go:82` 是 CTE 里的 LEFT JOIN，
	// 外层 fetchTurns 施加 `ascending ts, LIMIT 20`（见该函数注释），构成一处间接分页读。
	// 本轮未实测其下推行为。
	"session_turns_with_current_month": "混合：两处读无 LIMIT；message_source_v2.go 的 CTE 经 fetchTurns 间接 LIMIT 20（未测）",
}

// TestData_PaginatedViewSource_CorrelatedAntiJoinIsRegistered finds views that
// combine two things that individually are fine and jointly defeat LIMIT
// pushdown: a correlated subquery in the body, and a paginated read somewhere in
// the Go sources.
func TestData_PaginatedViewSource_CorrelatedAntiJoinIsRegistered(t *testing.T) {
	conn, ctx := connectAuditDB(t)

	rows, err := conn.Query(ctx, `
		SELECT c.relname, pg_get_viewdef(c.oid, true)
		  FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE n.nspname = 'public' AND c.relkind = 'v'
		 ORDER BY c.relname`)
	if err != nil {
		t.Fatalf("enumerate public views: %v", err)
	}
	defer rows.Close()

	antiJoinViews := map[string]string{} // view -> offending snippet
	viewCount := 0
	for rows.Next() {
		var name, def string
		if err := rows.Scan(&name, &def); err != nil {
			t.Fatalf("scan view: %v", err)
		}
		viewCount++
		if loc := correlatedSubqueryPattern.FindStringIndex(def); loc != nil {
			lo := loc[0] - 60
			if lo < 0 {
				lo = 0
			}
			hi := loc[1] + 40
			if hi > len(def) {
				hi = len(def)
			}
			antiJoinViews[name] = strings.Join(strings.Fields(def[lo:hi]), " ")
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read view rows: %v", err)
	}
	if viewCount == 0 {
		t.Fatal("no public views found — refusing to pass vacuously")
	}
	if len(antiJoinViews) == 0 {
		t.Fatalf("no public view body matched %s across %d views; the pattern is what "+
			"this gate is built on, so an empty match means the gate stopped seeing", "NOT EXISTS / EXISTS", viewCount)
	}

	identifiers := paginatedIdentifiers(t)
	paginated := map[string]bool{}
	for name := range antiJoinViews {
		paginated[name] = identifiers[name]
	}

	var flagged, shrunk []string
	overlap := 0
	for name, snippet := range antiJoinViews {
		_, justified := justifiedPaginatedAntiJoinViews[name]
		if !paginated[name] {
			continue // not read with ORDER BY+LIMIT anywhere: not this gate's business
		}
		overlap++
		if justified {
			t.Logf("已登记：%s — %s", name, justifiedPaginatedAntiJoinViews[name])
			continue
		}
		flagged = append(flagged, fmt.Sprintf(
			"%s：视图体含相关子查询（%s…），且仓内有 ORDER BY+LIMIT 的分页引用 —— "+
				"这类组合会让 LIMIT 失去约束力，请先 EXPLAIN 确认下推是否真的失效，"+
				"再把测量数字写进 justifiedPaginatedAntiJoinViews", name, snippet))
	}
	// The intersection must be non-empty, or one of the two halves has gone
	// blind: either the catalog scan stopped seeing correlated subqueries, or
	// the repo scan stopped seeing ORDER BY+LIMIT. An empty overlap would make
	// every check below pass for the wrong reason.
	if overlap == 0 {
		t.Fatalf("no view is both correlated-subquery-bearing and paginated (%d candidate views, "+
			"%d repo identifiers). One of the two scans is blind — this gate would otherwise pass "+
			"vacuously", len(antiJoinViews), len(paginated))
	}
	for name := range justifiedPaginatedAntiJoinViews {
		if _, stillThere := antiJoinViews[name]; !stillThere {
			shrunk = append(shrunk, fmt.Sprintf("%s（视图体已不再含相关子查询）", name))
			continue
		}
		if !paginated[name] {
			shrunk = append(shrunk, fmt.Sprintf("%s（仓内已无 ORDER BY+LIMIT 的分页引用）", name))
		}
	}

	t.Logf("视图普查：public 共 %d 个视图，其中 %d 个视图体含相关子查询；"+
		"其中被仓内分页查询引用且已登记的有 %d 个",
		viewCount, len(antiJoinViews), len(justifiedPaginatedAntiJoinViews))

	for _, f := range flagged {
		t.Errorf("带相关子查询的视图被分页查询引用且未登记：\n\t%s", f)
	}
	for _, s := range shrunk {
		t.Errorf("白名单条目已失效 —— 请删除或更新理由：\n\t%s", s)
	}
}

// paginatedIdentifiers returns every identifier token that appears inside a
// quoted string literal in a .go file which also paginates something
// (ORDER BY + LIMIT in the same literal).
//
// Why tokens and not whole literals: real sources are quoted *with their
// aliases* — `"request_logs_with_current_month AS r"`, `"... rl"` — so a
// bare-identifier pattern matched almost none of them and the gate reported
// "no overlap" for the single most paginated view in the tree. Collecting
// tokens and intersecting them against the catalog's view names keeps this
// precise without guessing which spelling a file uses.
//
// Granularity is the FILE, not the literal: admin/logs.go assembles its query
// with fmt.Sprintf, so the FROM source (from logsSourceFromSQL()) and the
// `ORDER BY ... LIMIT` live in different literals and only meet after
// assembly. Judging per literal loses those real hits.
//
// This gate must not observe itself. Its own source names every allowlisted
// view as a string literal and its comments contain "ORDER BY ... LIMIT", so
// an earlier version counted ITSELF as the consumer — and deleting an
// allowlist entry then dropped the overlap to zero, tripping the vacuity guard
// instead of the real assertion. That is the worst shape of gate bug: it stays
// green for a reason unrelated to the code under audit.
func paginatedIdentifiers(t *testing.T) map[string]bool {
	t.Helper()
	root := repoRoot(t)
	refs := map[string]bool{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor", ".build-local", "dist", "web", "installer":
				return fs.SkipDir
			}
			if d.Name() == "48h-audit" && filepath.Base(filepath.Dir(path)) == "tests" {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		src := string(b)
		paginates := false
		for _, lit := range goSQLLiteral.FindAllStringSubmatch(src, -1) {
			if literalIsPaginated(lit[1]) {
				paginates = true
				break
			}
		}
		if !paginates {
			return nil
		}
		for _, lit := range goSQLLiteral.FindAllStringSubmatch(src, -1) {
			for _, tok := range sqlIdentifier.FindAllStringSubmatch(lit[1], -1) {
				refs[tok[1]] = true
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk repo: %v", err)
	}
	return refs
}
