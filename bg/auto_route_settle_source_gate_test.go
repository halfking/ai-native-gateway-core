package bg

import (
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// sqlLiteralsOfSettleFile 返回文件里所有字符串字面量的文本。
//
// 解析用**原始源码**：剥注释会吃掉字面量里的 "//"（URL、SQL `--` 注释），
// 把 Go 源码弄成无法解析；而字面量里本来就不含 Go 注释，不需要先剥。
// 解析失败按 t.Fatal 处理——这道门宁可红在「读不到」也不要静默放行。
func sqlLiteralsOfSettleFile(t *testing.T, path string) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("解析 %s 失败 %v —— 这道门不能在没有源码的情况下判定为通过", path, err)
	}
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		bl, ok := n.(*ast.BasicLit)
		if !ok || bl.Kind != token.STRING {
			return true
		}
		if s, err := strconv.Unquote(bl.Value); err == nil {
			out = append(out, s)
		}
		return true
	})
	return out
}

// auto_route_settle_worker 的数据源约束门（审计 §9.40）。
//
// # 这道门在防什么
//
// 停写后这个 worker 会全量 abandon（§9.35 已让它可见）。最直觉的修法是
// 「把三个读点从 request_logs_hot 换成会话族」或「换成 710 视图」——
// **两个都被实测否决了**，而否决的理由不会自己写在代码里：
//
//  1. **710 视图不是 drop-in**。它在真库上把 citus 父表 request_logs 展开成
//     7 个叶子分区做 Seq Scan（2026-10-02 实测：request_logs_2026_07 …
//     request_logs_default）。这个 worker 每 5 分钟跑一次、每次 500 行的
//     LEFT JOIN，扛不住。文件顶部那段「DO NOT add UNION ALL request_logs」
//     的注释在实质上是对的——只是它描述的报错（invalid perminfoindex）
//     在这条查询上**没有复现**，实测到的不是报错而是**更坏的东西：计划**。
//     「没报错但计划烂掉」的替代方案比报错那个更危险，因为更容易被接受。
//
//  2. **会话族不是全量可平移**。真库 2026-09 分区实测（1747 条 selection）：
//     99.3% 的 request_id 在会话臂有对应行；success / latency_ms / cost_usd
//     100% 有值（cost_usd 会话臂反而比 v1 好：100% vs 3.6%）；session 身份
//     100% 可从 session_id 取得（710 视图的会话臂就是这么投影 gw_session_id 的：
//     `CASE WHEN t.session_id ~~ 'sys:%' THEN NULL ELSE t.session_id END`）。
//     **但有两处真退化**：
//       - `canonical_id`：v1 32.3% 有值，会话臂 1.9%，且**会话族里根本没有这列**
//         （session_turns / session_turn_details 都没有）⇒ settleBatch 的
//         LATERAL retry_count 腿**无法平移**。这是架构缺口，不是工程问题。
//       - `is_auto_request`：v1 99.9% vs 会话臂 83.0% ⇒ loadTaskBaselines 的
//         cohort 会缩水约 17%。
//
// 顺带更正本文件顶部注释里一个可以验证的细节：`origin_actor` 在**两侧都是 0**
// （v1 0/1746、会话臂 0/1734），所以 `SQLExcludeSyntheticActors` 对这批行
// **早就空转**。这既不是移植引入的新问题，也不构成阻止移植的理由——
// 但它意味着「排除合成流量」这个假设在本 worker 上**已经不成立**，
// 应当被单独记账，而不是当成移植的障碍。
//
// # 断言方向
//
// 只断言「**没有**换成 710 视图 / citus 父表」与「实测结论在注释里」。
// 这两条可证。**不断言**「应该换成会话族」：那要先决定 canonical_id 缺口怎么办，
// 而那会改动 reward 语义，不该由一道门单方面定下来。

// settleSQLFiles 列出**承载 settleBatch / loadTaskBaselines SQL 的源文件**。
//
// §9.44 把这两条查询从 auto_route_settle_worker.go 搬进了
// auto_route_settle_sql.go（抽成 settleBaselinesSQL / settlePendingSQL 纯函数，
// 好让集成测试能指定源族而不必翻转没有 setter 的全局 S4 写门）。
//
// 搬动本身立刻制造了一个**新的假绿**：两道既有门
// （TestAutoRouteSettleWorkerDoesNotUseThe710View 与
// TestSettleLegsAllUseTheSameSource）都只扫这一个文件，于是开始对着一个不再含
// SQL 的文件做断言——「一道删掉它所守之物之后仍然通过的判据，就是装饰」。
// 其中一道当时确实是绿的（没有字符串里出现 710 视图），另一道是红的（数不到
// src.TurnsTable）。**只有红的那一道暴露了搬动，绿的那一道会一直绿下去。**
//
// 所以这个清单必须被所有扫 SQL 的门共用；新增搬动 SQL 的文件时必须同步登记——
// 漏登记的后果是判据静默失效，不是报错。
var settleSQLFiles = []string{
	"auto_route_settle_sql.go",
	"auto_route_settle_worker.go",
}

// settleSQLLiteralText 汇总 settleSQLFiles 里所有字符串字面量的文本。
func settleSQLLiteralText(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	for _, path := range settleSQLFiles {
		for _, l := range sqlLiteralsOfSettleFile(t, path) {
			b.WriteString(l)
			b.WriteString("\n")
		}
	}
	return b.String()
}

// settleSQLSourceText 汇总 settleSQLFiles 的原始源码（给需要匹配 Go 标识符的判据用）。
func settleSQLSourceText(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	for _, path := range settleSQLFiles {
		b.WriteString(readFileForSettleGate(t, path))
		b.WriteString("\n")
	}
	return b.String()
}

// TestSettleSQLFilesStillCarrySQL 挡住上面说的「扫空文件」退化。
//
// 判据钉在**真的会出现在 SQL 里的形状**上（`LEFT JOIN ` 与 selection 表名），
// 而不是「文件里有没有 SELECT」——后者对着一份只剩注释的文件也能通过。
func TestSettleSQLFilesStillCarrySQL(t *testing.T) {
	joined := strings.Join(sqlLiteralsOfSettleFile(t, "auto_route_settle_sql.go"), "\n")
	if !strings.Contains(joined, "LEFT JOIN ") {
		t.Fatalf("auto_route_settle_sql.go 的字面量里没有 LEFT JOIN —— SQL 搬走后又搬回来了？\n" +
			"  settleSQLFiles 清单必须跟着实际位置更新，否则所有扫 SQL 的门都在对着\n" +
			"  空文件断言，判据会静默失效（§9.44）。")
	}
	if !strings.Contains(joined, "auto_route_selections_hot") {
		t.Errorf("auto_route_settle_sql.go 的 SQL 里没有 auto_route_selections_hot —— " +
			"settleSQLFiles 的登记与实际内容不符")
	}
}

func TestAutoRouteSettleWorkerDoesNotUseThe710View(t *testing.T) {
	// 判据只跑在**字符串字面量**（即真正的 SQL）上，不跑整份源码。
	// 第一版对整份源码跑 `(?i)JOIN\s+request_logs\s`，结果命中第 15 行**注释里的
	// 散文** `//  3. Join request_logs for success / latency / cost.` —— 假阳性。
	// 「把在场代码报成不在场」比没有门更坏：它训练读者忽略自己。
	all := strings.ToLower(settleSQLLiteralText(t))

	if strings.Contains(all, "request_logs_with_current_month") {
		t.Error("auto_route_settle_worker.go 的 SQL 里引用了 request_logs_with_current_month。\n" +
			"  真库实测（2026-10-02）：该视图的 v1 臂是 citus 父表 request_logs，\n" +
			"  在 settleBatch 这条 LEFT JOIN 的计划里展开成 7 个叶子分区 Seq Scan\n" +
			"  （request_logs_2026_07 … request_logs_default）。这个 worker 每 5 分钟\n" +
			"  跑一次、每次 500 行，扛不住。\n" +
			"  改动前请先 EXPLAIN 实测，并把计划代价记进审计文档。")
	}
	citusParent := regexp.MustCompile(`(?i)join\s+request_logs\s`)
	if citusParent.MatchString(all) {
		t.Error("auto_routeSettle 的 SQL 出现了对 citus 父表 request_logs 的 JOIN。\n" +
			"  父表是列存分区，集合运算里带它会在计划期报\n" +
			"  `ERROR: invalid perminfoindex 0 in RTE with relid 0`（PG 17.10 / citus 13.3）。\n" +
			"  要看更早的数据请跑两条查询在 Go 里合并，或按分区显式 LATERAL。")
	}
}

// TestAutoRouteSettleWorkerSourceConstraintIsDocumented 让「为什么不换」有落点。
//
// 上面那道门只说「不许换成 710 视图」，不说为什么——半年后的人会以为那是随手写的
// 保守规则，直接删掉。这道门要求文件注释里必须同时记下：被否决的替代方案、
// 实测到的**计划**代价（不只是报错），以及会话侧的 canonical_id 缺口。
func TestAutoRouteSettleWorkerSourceConstraintIsDocumented(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "auto_route_settle_worker.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var doc strings.Builder
	for _, cg := range file.Comments {
		doc.WriteString(cg.Text())
		doc.WriteString("\n")
	}
	text := doc.String()

	// 判据钉在**特征句**上，不是关键词上。`canonical_id` 这个词在文件里出现十几次
	//（SQL 里就有 6 处），拿它当判据的结果是：把整段实质文档删光，门照样绿——
	// 一道删掉它所守之物之后仍然通过的判据，就是装饰。
	// 下面两句只有真正的实测结论文档里才有。
	mustMention := []struct{ needle, why string }{
		{"perminfoindex", "citus 父表那个报错名——它是「为什么不能 UNION 父表」的原始记录"},
		{"request_logs_with_current_month", "必须点名那个被实测否决的替代方案，否则下一个人还会想到它"},
		{"会话族里根本没有这一列", "canonical_id 缺口的原话表述。换成别的说法门就红，这是有意的：这段文档不该被随手改写"},
		{"叶子分区 Seq Scan", "实测到的**计划**代价。注意它不是报错——只写「会报错」的门会让人以为换个写法就绕过了"},
	}
	for _, m := range mustMention {
		if !strings.Contains(text, m.needle) {
			t.Errorf("文件注释里没有提到 %q —— %s。\n"+
				"  审计 §9.40 的实测结论必须落在代码里，不能只活在文档里：\n"+
				"  ① 710 视图会把 citus 父表展开成 7 个分区 Seq Scan（实测，非报错而是计划）；\n"+
				"  ② 会话族 99.3%% 可平移，但 canonical_id 在会话侧根本没有来源，\n"+
				"     settleBatch 的 LATERAL retry_count 腿因此无法平移。",
				m.needle, m.why)
		}
	}
}
