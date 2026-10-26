package admin

// §143.36 / P12 的门：**看板降级读面「活跃模型数 / 活跃供应商数」必须是单遍扫描**。
//
// 背景（runbook §143.36）：
// ① 生产 pg_stat_statements 里那条「外传守卫」语句（2 天 18,876.9 MB 累计物理读、
//    每次 7,065 块、均时 1,818 ms、max 29,524 ms）与仓内
//    `admin/dashboard_board_fallback.go` 的 `fillOverviewCountsFromLogs`
//    **逐字同构**（骨架序列 21,135 字符逐字相等，见 §143.36.4）；
// ② ★★ 既有判词「它在外传守卫/请求热路径上」**被证伪** ⇒ 它是**看板降级读面**
//    （`queryOverviewCounts` → `fillOverviewCountsFromLogs`），不是请求热路径；
// ③ 它的形态是**两个标量子查询** ⇒ 同一份派生表被**独立扫描两遍**
//    （每遍 = hot ∪ parent 两腿 UNION ALL + 118 列投影 + details LEFT JOIN）；
// ④ ★★★ 生产只读实测（EXPLAIN (ANALYZE, BUFFERS)）：
//      30 天窗·无租户：改前 2,827,731 buffers / 37,328 ms（撞满 30 s 超时）
//                      改后 1,426,254 buffers /  4,587 ms
//    ⇒ **恰好减半**（两次扫描 → 一次），执行时间 −87.7%；
// ⑤ ★★ 语义**逐值**不变：`COUNT(DISTINCT x)` 按定义忽略 NULL ⇒ 改前那两条
//    `... IS NOT NULL` 尾臂是 no-op ⇒ 实测两窗口结果值全等（731/15、60/6）。
//
// # 为什么必须钉门
//
// ① ★★★★★ **「退化回双子查询」是本条唯一的失败模式**，而且它**不会报错**
//    —— 结果值完全正确，只是物理读翻倍、30 天窗重新撞超时。
//    ⇒ 没有门的话，一次「顺手把两个子查询拆开」的改动就能无声地把成本放回去。
// ② ★★★★ **门要守的不是「文本里有 COUNT(DISTINCT)」**（改前改后都有）
//    而是「**派生表被引用了几次**」⇒ 判据必须数**引用次数**，不是数关键词。
// ③ ★★★ **不许把「裁剪投影」当修法**：实测裁剪 115 列 → 9 列**零收益**
//    （buffers 100.0% / 100.9%）⇒ planner 已裁列 ⇒ 根因是「扫两遍」。
//    ⇒ 门要显式登记这条阴性结论，防止后人再做一遍。
// ④ ★★ **IS NOT NULL 臂的免检许可不可外推**：`COUNT(DISTINCT)` 忽略 NULL；
//    若换成 SUM/AVG/COUNT(*) 就必须加回 ⇒ 门钉住「当前聚合是 DISTINCT 形态」。
// ⑤ ★★ **量具自身要有牙**（本轮实测踩到并修掉的三个量具 bug）：
//    (a) 用 `strings.Index(body, "fmt.Sprintf(`")` 找 SQL 模板会**先命中
//        `modelExpr := fmt.Sprintf(`COALESCE(...)`)`**（它在前面）⇒ 必须遍历
//        **所有**反引号模板并挑出含 "SELECT" 的那个；
//    (b) 「数 FROM %s 的出现次数」必须**限定在同一个 SQL 模板内** ——
//        按**文件**计数会把同文件的另外两个正常单扫读面也算进来 ⇒ 恒红；
//    (c) 门自己所在的 .go 文件含这些字面量 ⇒ 扫描时必须**排除自己**。

import (
	"os"
	"strings"
	"testing"
)

const fallbackSrc14336 = "dashboard_board_fallback.go"

// ★★★★ 剥注释层（2026-10-21 补）—— **本门的第一版踩了仓内第 5 处同款坑**。
//
// 仓内既有纪律（见 cmd/gateway/dual_read_gate_test.go 的 stripGoComments、
// internal/sessionv2mirror/abandoned_turn_gate_test.go 等 10+ 处实现）：
// ★ **「判据一旦能在注释里被满足，就不是判据」**。
//
// ★★ **本轮实测抓到自己的两个 needle 可被注释满足**（剥注释后消失）：
//   - `(SELECT`   —— 改前形态的「顶层标量子查询」标记；
//   - `AVG(`      —— 「聚合必须保持 DISTINCT 形态」这条禁项。
//     ⇒ **改前形态恰好写在【注释】里**（本函数上方的长注释描述了旧形态）
//     ⇒ ⇒ **不改剥注释层 ⇒ 改前的双扫形态会被判绿** ⇒ 门没有牙。
//
// ⇒ ★★ **纪律要靠「检索既有实现」来继承，不能靠记忆**（远端 LESSONS 2026-10-21 第一条）。
//
// ★★ **剥注释器必须是【状态机】而不是正则** —— 本轮第一版用了正则
//
//	（`(?m)//[^\n]*` / `(?s)/\*.*?\*/`）⇒ **自证门当场抓到它把字符串字面量里的
//	`//` 与 `/* */` 也吃掉了**（仓内 `admin/request_logs_retirement_tiering_test.go`
//	的 `stripGoCommentsKeepingStrings` 就是状态机实现 ⇒ **同包直接复用**）。
//	⇒ 正则实现会在遇到 URL 字面量（"http://…"）时把后面整行吃掉。
func stripGoComments14336(src string) string {
	return stripGoCommentsKeepingStrings(src)
}

// sqlTemplates14336 返回一个 .go 文件里所有反引号 SQL 模板（fmt.Sprintf 的实参）。
func sqlTemplates14336(src string) []string {
	var out []string
	rest := src
	for {
		i := strings.Index(rest, "fmt.Sprintf(`")
		if i < 0 {
			return out
		}
		rest = rest[i+len("fmt.Sprintf(`"):]
		j := strings.Index(rest, "`")
		if j < 0 {
			return out
		}
		out = append(out, rest[:j])
		rest = rest[j+1:]
	}
}

// boardFallbackBody14336 返回 fillOverviewCountsFromLogs 的函数体。
func boardFallbackBody14336(t *testing.T) string {
	t.Helper()
	src, err := os.ReadFile(fallbackSrc14336)
	if err != nil {
		t.Fatalf("read %s: %v", fallbackSrc14336, err)
	}
	// ★★ 剥注释后再取函数体（判据不得被注释满足）
	body := goFuncBodyStrict(stripGoComments14336(string(src)),
		"func (h *Handler) fillOverviewCountsFromLogs(")
	if body == "" {
		t.Fatalf("fillOverviewCountsFromLogs not found in %s", fallbackSrc14336)
	}
	return body
}

// boardFallbackSQL14336 返回 fillOverviewCountsFromLogs 的 SQL 模板。
//
// ★ 必须挑「含 SELECT 的那个模板」：函数体里第一个反引号模板是 modelExpr，
//
//	不是 SQL（第一版门在这里读空 ⇒ 门红但产品是对的 ⇒ 量具 bug）。
func boardFallbackSQL14336(t *testing.T) string {
	t.Helper()
	src, err := os.ReadFile(fallbackSrc14336)
	if err != nil {
		t.Fatalf("read %s: %v", fallbackSrc14336, err)
	}
	body := goFuncBodyStrict(stripGoComments14336(string(src)),
		"func (h *Handler) fillOverviewCountsFromLogs(")
	if body == "" {
		t.Fatalf("fillOverviewCountsFromLogs not found in %s", fallbackSrc14336)
	}
	for _, tmpl := range sqlTemplates14336(body) {
		if strings.Contains(tmpl, "SELECT") {
			return tmpl
		}
	}
	t.Fatalf("fillOverviewCountsFromLogs has no SELECT-bearing SQL template; templates=%v",
		sqlTemplates14336(body))
	return "" // unreachable（t.Fatalf 已终止），但 Go 编译器要求
}

// TestSection14336OverviewCountsIsSingleScan 是本条门的核心判据：
// 派生表（logsTable 形如 "(SELECT … FROM public.session_turns_hot t … UNION ALL …)"）
// 在这条语句里必须**只被引用一次**。
//
// ★ 判据口径（★ 别数错东西）：
//
//	改前形态里派生表占位符出现 **2 次**（两个标量子查询各一次）⇒ 门红；
//	改后形态里出现 **1 次** ⇒ 门绿。
//	⚠ 不能用「数 FROM」：顶层是 SELECT，FROM 在子查询里 ⇒ 对改前形态
//	  「顶层 FROM」与「子查询 FROM」的区分不成立。
//	⚠ 不能用「数 SELECT」：改前 3 个 / 改后 3 个 —— 数字巧合接近，不可靠。
//	⇒ 唯一稳的口径是「**派生表占位符在同一个 SQL 模板里被引用了几次**」。
func TestSection14336OverviewCountsIsSingleScan(t *testing.T) {
	sql := boardFallbackSQL14336(t)

	// ★ 派生表占位符 = "FROM %s"（logsTable 的占位形态）。
	fromCount := strings.Count(sql, "FROM %s")
	if fromCount != 1 {
		t.Errorf("派生表被引用 %d 次（FROM %%s）⇒ 期望恰好 1 次（单遍扫描）。\n"+
			"  两个标量子查询 ⇒ 同一份派生表被独立扫描两遍 ⇒ 物理读翻倍、\n"+
			"  30 天窗重新撞满 30 s statement_timeout（§143.26.3 的「2 天 69 次被杀」）。\n"+
			"  实际 SQL 模板: %q", fromCount, sql)
	}
}

// TestSection14336NoScalarSubqueryInOverviewCounts 钉住「顶层没有标量子查询」：
// 改前形态顶层是 SELECT (SELECT …), (SELECT …)。
func TestSection14336NoScalarSubqueryInOverviewCounts(t *testing.T) {
	sql := boardFallbackSQL14336(t)

	if strings.Contains(sql, "(SELECT") {
		t.Errorf("顶层仍有标量子查询：(SELECT …) ⇒ 派生表会被独立扫描两遍。\n  实际: %q", sql)
	}
	if n := strings.Count(sql, "COUNT(DISTINCT"); n != 2 {
		t.Errorf("COUNT(DISTINCT 出现 %d 次 ⇒ 期望 2（模型数 + 供应商数）", n)
	}
}

// TestSection14336DistinctAggregatesIgnoreNulls 钉住「聚合是 DISTINCT 形态」：
// ★ 这是「可以省掉 IS NOT NULL 臂」的**唯一**依据
//
//	（COUNT(DISTINCT x) 按定义忽略 NULL；SUM/AVG/COUNT(*) 不行）。
//
// ⇒ 若将来把聚合换成非 DISTINCT 形态，必须把 IS NOT NULL 臂加回去。
func TestSection14336DistinctAggregatesIgnoreNulls(t *testing.T) {
	body := boardFallbackBody14336(t)
	if !strings.Contains(body, "COUNT(DISTINCT") {
		t.Errorf("两个聚合都必须保持 COUNT(DISTINCT …) 形态")
	}
	// 禁掉非 DISTINCT 聚合（它们对 NULL 敏感 ⇒ IS NOT NULL 臂不能省）
	for _, forbidden := range []string{"SUM(", "AVG(", "COUNT(*)", "COUNT( r.", "COUNT(r."} {
		if strings.Contains(body, forbidden) {
			t.Errorf("出现了 %q：非 DISTINCT 聚合对 NULL 敏感 ⇒ 不能省 IS NOT NULL 臂", forbidden)
		}
	}
}

// TestSection14336NarrowProjectionIsNotTheFix 登记阴性结论：
// ★ 实测：把 115 列投影裁成 9 列【零收益】（buffers 100.0% / 100.9%）
//
//	⇒ planner 已把未引用的投影列裁掉（plan 里 width 700 → 40）
//	⇒ 根因是「扫两遍」，不是「列太宽」。
//
// 这条门的作用是**防复发**：防止有人（哪怕是为了「优化」）把这条读面
// 改成窄投影，然后误以为问题已解决、却留下 2 倍物理读。
func TestSection14336NarrowProjectionIsNotTheFix(t *testing.T) {
	body := boardFallbackBody14336(t)
	// 裁剪投影会引入「按列名挑选」的代码路径（仓内 helper 的形状）。
	// 本门只登记结论 + 断言当前实现走的是【完整投影】helper。
	if !strings.Contains(body, "boardRequestLogsFromClause()") {
		t.Errorf("必须继续用 boardRequestLogsFromClause()（完整投影 helper）；\n" +
			"  裁剪投影的收益已实测为 0（§143.36.6），不要走那条路。")
	}
	// 该 helper 必须仍在用完整投影（SessionFamilyTurnsSourceSQL）
	src, err := os.ReadFile("board_time_range.go")
	if err != nil {
		t.Fatalf("read board_time_range.go: %v", err)
	}
	fb := goFuncBodyStrict(stripGoComments14336(string(src)), "func boardRequestLogsFromClause(")
	if fb == "" {
		t.Fatalf("boardRequestLogsFromClause not found")
	}
	if !strings.Contains(fb, "SessionFamilyTurnsSourceSQL()") {
		t.Errorf("boardRequestLogsFromClause 必须用 SessionFamilyTurnsSourceSQL()（完整投影）")
	}
}

// TestSection14336OnlyOneDoubleScanSiteInAdmin 全包扫描：
// 「同一个 SQL 模板里出现 >= 2 个 FROM %s（派生表占位）+ (SELECT COUNT(DISTINCT」这个
// 形态在 admin 包里**不应存在**（已修）⇒ 若将来出现，说明又埋了一个双扫读面。
//
// ★ 量具纪律：必须【按 SQL 模板】计数，不能按文件计数
//
//	（dashboard_board_fallback.go 里还有 fallbackBoardTrends / fallbackDimPie
//	 两个**正常**的单扫读面，各带 1 个 FROM %s ⇒ 按文件计数会恒红）；
//	且必须排除门自己所在的文件（它含这些字面量）。
func TestSection14336OnlyOneDoubleScanSiteInAdmin(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read admin dir: %v", err)
	}
	self := "section_14336_board_single_scan_test.go"
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || e.Name() == self {
			continue
		}
		b, err := os.ReadFile(e.Name())
		if err != nil {
			continue
		}
		for _, tmpl := range sqlTemplates14336(stripGoComments14336(string(b))) {
			if strings.Count(tmpl, "FROM %s") >= 2 &&
				strings.Contains(tmpl, "(SELECT COUNT(DISTINCT") {
				t.Errorf("admin/%s 的某个 SQL 模板里出现 %d 个 FROM %%s + (SELECT COUNT(DISTINCT ⇒ 疑似双扫读面\n  模板: %q",
					e.Name(), strings.Count(tmpl, "FROM %s"), tmpl)
			}
		}
	}
}

// TestSection14336StripCommentsHasTeeth 是【剥注释器自身】的两腿自证：
//
//	腿① 该剥的剥掉了：注释里的 SQL 片段不能被源码判据命中；
//	腿② 不该剥的没剥：字符串字面量里的 "//" 与 "/* */" 不能被吃掉
//	     （否则会把真实 SQL 判成「不存在」）。
//
// ★ 这条门存在的理由：本门的第一版有两个 needle 可被注释满足
//
//	（`(SELECT` / `AVG(`）⇒ **改前形态恰好写在注释里** ⇒ 门会判绿。
func TestSection14336StripCommentsHasTeeth(t *testing.T) {
	// 腿①：该剥的剥掉了
	withComment := "x := 1 // (SELECT COUNT(DISTINCT y)\n" +
		"/* AVG( */\n" +
		"z := 2\n"
	stripped := stripGoComments14336(withComment)
	if strings.Contains(stripped, "(SELECT") {
		t.Errorf("行注释里的 (SELECT 未被剥掉 ⇒ 源码判据可被注释满足")
	}
	if strings.Contains(stripped, "AVG(") {
		t.Errorf("块注释里的 AVG( 未被剥掉 ⇒ 源码判据可被注释满足")
	}
	if !strings.Contains(stripped, "z := 2") {
		t.Errorf("剥注释把真实代码也吃掉了")
	}

	// 腿②：不该剥的没剥
	withLiterals := "u := " + "`http://example.com/a`" + "\n" +
		"v := " + "`/* not a comment */`" + "\n" +
		"w := 3\n"
	stripped2 := stripGoComments14336(withLiterals)
	if !strings.Contains(stripped2, "http://example.com/a") {
		t.Errorf("字符串字面量里的 // 被误剥 ⇒ 会把真实 SQL 判成不存在")
	}
	if !strings.Contains(stripped2, "/* not a comment */") {
		t.Errorf("字符串字面量里的 /* */ 被误剥")
	}
	if !strings.Contains(stripped2, "w := 3") {
		t.Errorf("剥注释把真实代码也吃掉了")
	}
}

// TestSection14336RunbookHasSection 钉住 runbook 的 §143.36 在场。
func TestSection14336RunbookHasSection(t *testing.T) {
	runbook := "../docs/06-deployment/04-runbooks/runbooks/2026-10-04-ursm-write-amplification-measurement.md"
	b, err := os.ReadFile(runbook)
	if err != nil {
		t.Fatalf("read runbook: %v", err)
	}
	body := string(b)
	for _, needle := range []string{
		"§143.36",
		"fillOverviewCountsFromLogs",
		"2,827,731",
		"1,426,254",
		"50.4%",
		"逐值",
		"看板降级读面",
	} {
		if !strings.Contains(body, needle) {
			t.Errorf("runbook 缺 %q", needle)
		}
	}
}
