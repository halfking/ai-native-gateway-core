//go:build !integration

package admin

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// 会话族「两个存储面」门（2026-10-02 建，§9.29）。
//
// # 为什么需要这道门
//
// 会话族的**写方只写 `_hot`**（`domains/session/v2/turn_writer.go:347` 插
// `session_turns_hot`），冷行由 `promote_session_turns_hot_to_partition` 搬到
// **分区父表**。因此 `session_turns`（父表）与 `session_turns_hot` 是**两个不同的
// 存储面**，边界随 promote 节奏移动。
//
// 本机实测（`llm_gateway`）：父表最新 ts = **2026-10-02 06:06:31**，
// `_hot` 从 06:07:14 接到**实时** ⇒ 父表落后约 8.7 小时。
//
// ⇒ **任何只读父表、不 UNION `_hot` 的查询都会漏掉最近数小时的数据。**
// 而且漏掉的恰恰是**最新的**那批——对「刚发生的事」类读点（刚失败、刚新建、
// 刚写入）伤害最大。
//
// # 三种正确写法，本门都接受
//
//  1. 走合并视图：`session_turns_with_current_month` / `session_bodies_unified`
//     （真库已核实两者都 `UNION ALL` 两面，1,684,512 行、最新到实时）；
//  2. 直读基表但 `UNION ALL` 两面（如 `bg/credential_selfcheck.go`）；
//  3. 只读 `_hot`（写方就是这个形状）。
//
// **只有「只读父表」是缺陷。**
//
// # 为什么是「默认拒绝 + 具名登记」而不是「禁止」
//
// `storage/sqlite/*` 与 `bg/lite_retention_worker.go` 里有同名的
// `session_turns` / `session_turn_details`，但那是 **SQLite 单文件库**，
// 与 PostgreSQL **不是同一个存储面**，不存在 hot/父表边界。
// 与 v1_direct_padded_column_reader_test.go 的登记表同一范式：
// 默认未登记 = 需要有人拍板，而不是默默放行。
//
// # 口径精度（必须随门一起读）
//
// 判据只扫**字符串字面量**（`go/ast` 解析，排除注释），且只认带 `public.`
// 前缀的**基表**形态。已知盲区，本门**看不到**：
//   - SQL 由字符串拼接（`"FROM public.session_" + "turns"`）在运行时拼出来的形状；
//   - 关系名不带 `public.` 前缀的裸读；
//   - SQLite 侧（有意排除，不是 PG 存储面）。
// 这三条盲区里，前两条靠**真库执行门**兜底（能真的跑出来的 SQL 一定会暴露）。

// sessionFamilyBareParentReaders 是「本门判不了、但已人工核实为正确」的具名登记表。
//
// **不是**「这些文件可以只读父表」，而是「**这些文件的正确形状是拼装出来的，
// 逐字面量判据看不见跨字面量的 UNION**」。每条都要写清两面在哪里。
//
// 判据的单位是**一条 SQL**，而这两处的两条面分处**不同的字符串常量**里
// （视图体由投影片段拼装；写路径由表名切片遍历），所以逐串判必然误报。
// 这与门头注释里写明的盲区是同一条：**逐字面量门看不见运行时拼起来的形状**。
var sessionFamilyBareParentReaders = map[string]string{
	"db/request_logs_view_schema.go": "710 视图体拼装器：session_turns_hot 与 session_turns " +
		"（以及 details 层两面的）投影片段是**不同的字符串常量**，两面在 9 处配对出现。" +
		"真库已核实：request_logs_with_current_month = hot∪parent，118 列、三列有值。",
	"domains/session/v2/session_aggregator.go": "UPDATE 只写 session_turns_hot（:198，写方形状正确）；" +
		":452 另有 `for _, table := range []string{\"public.session_turns_hot\", \"public.session_turns\"}` " +
		"——两面由表名切片遍历表达，字面量里只出现一次父表名，逐串判必然误报。",
	"domains/session/v2/session_request_status_backfill.go": "退役回填作业，**父表-only 是刻意的**，不是漏了 _hot。" +
		"理由三条，逐条可核：(1) 仍留在 session_turns_hot 的行是经 823 写路径落的" +
		"（internal/sessionv2mirror → TurnRecord.RequestStatus），标签本就带着，回填对它无增量；" +
		"(2) 少数确实缺标签的 hot 行不会丢——promote 把它转进父表后本作业就会扫到，" +
		"hot 保留窗自己会关；" +
		"(3) 要覆盖 _hot，批量的 UPDATE 就得知道每个 keyset 行落在哪个面上，" +
		"把单表 keyset 变成双表，只为覆盖一个会自行关闭的窗口。" +
		"**注意与同文件的 gauge 区分**：sessionRequestStatusRemainingSQL 刻意**两个面都查**，" +
		"因为它是 D9 第四条退役放行判据，问的是「退役后还会存在的每一行」而不是" +
		"「本作业还能修哪些」。两者范围不同不是不一致，是两个不同的问题；" +
		"把 gauge 也缩到父表才是缺陷（那会让 _hot 满是 NULL 时仍报 0，审计 §9.160）。",
	// 下面是本文件**登记后仍会命中**的逐个形状，三条各不相同，不是「整个文件
	// 可以单面」：
	//  1. DELETE FROM public.session_turns（父表腿）——紧邻其上一条独立语句就是
	//     DELETE FROM public.session_turns_hot，且 DeletedRows 是两面相加。
	//     判据单位是「一条 SQL」，跨语句的两面它看不见（同门头盲区）。
	//  2. INSERT INTO public.session_turns。
	//  3. INSERT INTO public.session_bodies。
	//     2/3 是**写**方选择只落父表，不是「漏读 _hot」：本工具按 v1 源全量重建，
	//     产出一份完整快照直接进分区父表，promote 只搬 hot→父表、不搬父表→hot，
	//     所以不构成漏数据。**bodies 的 DELETE 曾是真正的漏面并已修**（§9.183
	//     补了 session_bodies_hot 腿）；此处登记不得被当成那道修复的守卫 ——
	//     守卫是 repair_two_surface_test.go，它逐条断两条 DELETE 各自的面覆盖。
	"cmd/tools/validate_sessions_v2/repair.go": "修复工具：DELETE turns 父表腿的 hot 对偶在紧邻的上一条语句" +
		"（跨语句配对，逐串判看不见）；两处 INSERT 是重建快照直落分区父表的写方选择，非漏读。",
}

var sessionFamilyParents = []string{"session_turns", "session_bodies", "session_turn_details"}

// sessionFamilySQLiteDirs / Files：与 PG 不是同一个存储面，有意排除。
var sessionFamilySQLiteDirs = []string{"storage/sqlite", "tests", "installer"}
var sessionFamilySQLiteFiles = []string{"bg/lite_retention_worker.go"}

func TestNoBareParentSessionFamilyRead(t *testing.T) {
	files, err := goFilesUnder("..")
	if err != nil {
		t.Fatalf("walk repo root: %v", err)
	}
	fset := token.NewFileSet()
	var hits []string
	for _, f := range files {
		rel := relToRepoRoot(f)
		if isSessionFamilyNonPG(rel) {
			continue
		}
		src, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			continue // 解析不了的由编译门拦，这里不制造假警报
		}
		for _, lit := range sqlStringLiterals(src) {
			for _, hit := range bareParentReadsIn(lit) {
				hits = append(hits, rel+" :: "+hit)
			}
		}
	}
	sort.Strings(hits)

	var unregistered []string
	for _, h := range hits {
		file := h[:strings.Index(h, " :: ")]
		if _, ok := sessionFamilyBareParentReaders[file]; !ok {
			unregistered = append(unregistered, h)
		}
	}
	if len(unregistered) == 0 {
		if len(hits) == 0 {
			return
		}
		// 全部已登记：核对登记理由非空，并反向报告仍命中的具体形状
		for file, why := range sessionFamilyBareParentReaders {
			if strings.TrimSpace(why) == "" {
				t.Errorf("会话族裸父表读方 %q 已登记但理由为空——空理由等于没有理由，\n"+
					"三个月后没人能判断它是仍然成立还是早已过期", file)
			}
		}
		return
	}
	t.Fatalf("会话族出现未登记的裸父表读法（%d 处）：\n  %s\n\n"+
		"会话族的写方只写 `session_turns_hot`，冷行由 promote 搬到分区父表，\n"+
		"两个面之间**没有固定边界**（本机实测父表落后 hot 约 8.7 小时）⇒\n"+
		"只读父表会漏掉最近数小时的数据，且漏掉的正是最新那批。\n"+
		"三种正确写法（选一）：\n"+
		"  1) 走合并视图 session_turns_with_current_month / session_bodies_unified；\n"+
		"  2) 直读基表但 UNION ALL 两面；\n"+
		"  3) 确认只读 _hot 即可。\n"+
		"若本文件确实可以只读父表，请在 sessionFamilyBareParentReaders 里具名登记并写明理由。\n"+
		"（storage/sqlite/* 与 bg/lite_retention_worker.go 是 SQLite 单文件库，\n"+
		"  与 PG 不是同一个存储面，已有意排除。）",
		len(unregistered), strings.Join(unregistered, "\n  "))
}

// TestSessionFamilyBareParentRegistryIsHonest 反向自检：登记了却已经不命中的，
// 说明理由已过期（代码改好了但登记没删），必须报红而不是默默留着。
func TestSessionFamilyBareParentRegistryIsHonest(t *testing.T) {
	if len(sessionFamilyBareParentReaders) == 0 {
		return
	}
	fset := token.NewFileSet()
	hitFiles := map[string]bool{}
	files, err := goFilesUnder("..")
	if err != nil {
		t.Fatalf("walk repo root: %v", err)
	}
	for _, f := range files {
		rel := relToRepoRoot(f)
		if isSessionFamilyNonPG(rel) {
			continue
		}
		src, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			continue
		}
		for _, lit := range sqlStringLiterals(src) {
			if len(bareParentReadsIn(lit)) > 0 {
				hitFiles[rel] = true
			}
		}
	}
	for file := range sessionFamilyBareParentReaders {
		if !hitFiles[file] {
			t.Errorf("会话族裸父表读方 %q 已登记但当前不再命中——登记已失效。\n"+
				"要么代码已改好（那就删掉这条登记），要么读法换了形态而门没看见\n"+
				"（那就核一遍门为什么没报）。**登记表会过期，过期的登记表比没有更坏**", file)
		}
	}
}

func isSessionFamilyNonPG(rel string) bool {
	for _, d := range sessionFamilySQLiteDirs {
		if rel == d || strings.HasPrefix(rel, d+"/") {
			return true
		}
	}
	for _, f := range sessionFamilySQLiteFiles {
		if rel == f {
			return true
		}
	}
	return false
}

// sqlStringLiterals returns every string literal in the file, i.e. the only
// places a SQL statement can live. Comments are structurally excluded by the
// AST, which is what makes this zero-false-positive for "is it in a comment".
func sqlStringLiterals(f *ast.File) []string {
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		bl, ok := n.(*ast.BasicLit)
		if !ok || bl.Kind != token.STRING {
			return true
		}
		v, err := strconv.Unquote(bl.Value)
		if err != nil {
			return true
		}
		out = append(out, v)
		return true
	})
	return out
}

// bareParentReadsIn finds SQL statements that read a session-family **base
// table** without reading its `_hot` twin anywhere in the same statement.
//
// # 判据的单位是「这条 SQL」，不是「这个文件」
//
// 第一版把判据钉在「文件里出现过裸父表读」上，**门宽了**：同一条 SQL 里
// `FROM public.session_turns UNION ALL FROM public.session_turns_hot` 的
// **父表那一支本来就是合法的**，而 12 个文件全部被判红——
// 每次假阳性都是「门宽了」而不是「代码错了」。
// 一个文件里完全可能同时有一条正确的 union 查询和一条漏读的单面查询，
// 所以必须**逐条 SQL 判**。
//
// 排除形态：`session_turns` 是 `session_turns_hot` 与
// `session_turns_with_current_month` 的**前缀**，而 `\b` 排除不了它们
// （后一个字符是 `_`，属单词字符）——所以这里做的是**显式的后随字符检查**。
func bareParentReadsIn(sql string) []string {
	flat := strings.Join(strings.Fields(sql), " ")
	var out []string
	for _, p := range sessionFamilyParents {
		hot := "public." + p + "_hot"
		if mentionsRelation(flat, hot) {
			// 同一条 SQL 已经读了两面：父表那一支是 UNION 的合法分支。
			continue
		}
		needle := "public." + p
		if relationUsedAsSource(flat, needle) {
			out = append(out, needle)
		}
	}
	return out
}

// mentionsRelation reports whether `rel` appears in `flat` at all as a complete
// relation name (not followed by an identifier character).
func mentionsRelation(flat, rel string) bool {
	for _, abs := range occurrencesOf(flat, rel) {
		after := abs + len(rel)
		if after >= len(flat) || !isIdentChar(flat[after]) {
			return true
		}
	}
	return false
}

// relationUsedAsSource requires the relation to appear as a **statement's
// source**, i.e. preceded by FROM / JOIN / INTO / UPDATE — not merely mentioned.
//
// 这一条不是洁癖：踩到过两个真实假阳性。
//   - cmd/gateway/main.go:2659 是 slog.Info 的**日志文案**，
//     里面列出「public.sessions public.session_turns public.session_bodies」当描述；
//   - domains/session/v2/test_helpers.go:48 是一份**表名清单**（清理助手用）。
//
// 两者都含表名、都不是 SQL。第一版重写时把这条要求连同 hasSQLKeywordBefore
// 一起删掉，于是它们又变成了假阳性——**门的每一次返工都是被跑出来的，不是想出来的**。
func relationUsedAsSource(flat, rel string) bool {
	for _, abs := range occurrencesOf(flat, rel) {
		after := abs + len(rel)
		if after < len(flat) && isIdentChar(flat[after]) {
			continue // 前缀命中：session_turns 之于 session_turns_hot
		}
		pre := strings.TrimRight(flat[:abs], " \t\n")
		for _, kw := range []string{"FROM", "JOIN", "INTO", "UPDATE"} {
			if len(pre) >= len(kw) && strings.EqualFold(pre[len(pre)-len(kw):], kw) {
				return true
			}
		}
	}
	return false
}

func occurrencesOf(flat, sub string) []int {
	var out []int
	for i := 0; ; {
		j := strings.Index(flat[i:], sub)
		if j < 0 {
			return out
		}
		out = append(out, i+j)
		i += j + len(sub)
	}
}

func isIdentChar(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

func hasSQLKeywordBefore(pre string) bool {
	for _, kw := range []string{"FROM", "JOIN", "INTO", "UPDATE"} {
		trimmed := strings.TrimRight(pre, " \t\n")
		if len(trimmed) >= len(kw) && strings.EqualFold(trimmed[len(trimmed)-len(kw):], kw) {
			return true
		}
	}
	return false
}
