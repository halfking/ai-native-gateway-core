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
	"admin/turns_filter_options.go": "turnMergedSource composes parent UNION ALL hot across separate string literals, with the same tenant/time predicates; TestFilterOptionsArmsAreMerged requires both legs in the actual returned SQL.",
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
	// turn_writer 的 next-turn_no 是**两条独立 SQL 的热优先序贯**：先
	// `SELECT MAX(turn_no) FROM public.session_turns_hot`，仅当 hot 无行
	// （turnNo<=1，即该会话已整体晋升）才回退查 `public.session_turns`。
	// 两面分处两条语句（同「判据单位是一条 SQL」盲区），逐串判只见父表腿。
	// 正确性凭据（turn_writer.go :353-383 注释，runbook §10.106.25/26）：
	// promote 按 ts 序搬 hot→父表，活跃会话的最新 turn 恒在 hot；
	// 写路径持 per-session 顾问锁，turn_no 单调无并发推进，回退臂
	// 只服务「每行都已晋升」的会话。父表腿若被删，重放/摘要类全晋升
	// 会话的 turn_no 会从 1 重新计数——两面缺一不可。
	"domains/session/v2/turn_writer.go": "MAX(turn_no) 热优先序贯：hot 臂在前，" +
		"父表臂是「hot 无行才查」的晋升回退（两条独立 SQL，跨语句配对，逐串判看不见）；" +
		"promote 按 ts 序 + 顾问锁串行写保证最新 turn 恒在 hot，回退臂覆盖全晋升会话。",
	// R172：会话族轮次读点的**两臂直查** helper（冲突读回 / GetTurn / ListTurns）。
	// 父表名出现 **2 处**，但**两处都不是「只读父表」**：
	//  1. **父表腿** —— 与 `public.session_turns_hot` 腿同处一条 `UNION ALL`
	//     语句（两面齐全 ⇒ 不漏读 _hot；父表落后 hot 约 8.7 小时这条约束满足）；
	//  2. **反连接里的 `archived` 引用** —— 它只**过滤**（NOT EXISTS），
	//     `SELECT 1` 不投影任何列 ⇒ 不是数据来源。
	// ★ 逐字面量判据看不见第 1 条的两面配对：hot 腿、parent 腿、anti-join
	//   分处**三个不同的字符串常量**，而判定单位是「一条字面量」
	//   ⇒ 与 db/request_logs_view_schema.go、admin/turns_filter_options.go
	//   是同一条盲区（门头注释已写明）。
	// 正确性凭据：① admin/session_family_turns_reads_test.go 的
	// TestSessionFamilyTurnsReadsBothFaces 直接断言**产物 SQL**里两面同时出现、
	// 且反连接是「只过滤不投影」形状；
	// ② domains/session/v2/turn_writer_family_reads_test.go 把**产品自己发出的
	// 那条 SQL**与线上视图形态放在同一条语句、同一个快照里逐行对拍
	// （本地真库整族 2,758 行双向 EXCEPT ALL 差集 0/0；252 生产逐读点 0/0）。
	"internal/sessionfamily/turns_reads.go": "R172 两臂直查 helper：hot 腿与父表腿同处一条 " +
		"UNION ALL（两面齐全），反连接对父表的引用只过滤不投影；" +
		"三条腿分处不同字符串常量 ⇒ 逐字面量判据看不见两面配对。" +
		"正面证明见 admin/session_family_turns_reads_test.go 的 " +
		"TestSessionFamilyTurnsReadsBothFaces；等价性见 " +
		"domains/session/v2/turn_writer_family_reads_test.go（真库逐行对拍）。",
	// ★★ r1043 轮（2026-10-28）：★ 下面这两条**已撤**（★ 登记过期）。
	// ★ 起因：★ 上游在 `relationUsedAsSource` 里给「DELETE FROM <父表>」加了排除
	// ★ （★ DELETE 是写不是读 ⇒ 排除本身是对的）⇒ ⇒ ★ 但**同一次改动里**
	// ★ ★★ 又把 admin 的这两个文件登记进本表 ⇒ ⇒ ★★ 排除让它们不再命中本门
	// ★ ★★ ⇒ ⇒ 本表里这两条**当场就过期了**（★ 本门的「登记已失效」判定
	// ★ ★★ 如实报了出来 ⇒ ★★ 这是本门有牙的证据）。
	// ★ ★★ 撤登记后，★ 这两处的「删除必须覆盖两面」由**真实存在**的那道门管：
	// ★ ★★ admin/session_family_delete_two_surface_test.go
	// ★ ★★ （★ 逐条断「父表 DELETE ↔ 同 WHERE 的 hot DELETE」成对）。
	// ★ ★★ ★★ 此前这条注释引用的 `repair_two_surface_test.go` 指的是
	// ★ ★★ `cmd/tools/validate_sessions_v2/` 下的那一份 ⇒ ★★ 它**只覆盖那个工具**
	// ★ ★★ （★ 实测：★ 只有 ExecuteRepair 那两条测试）⇒ ★★ **完全没覆盖 admin 侧**
	// ★ ★★ ⇒ ⇒ ★★ 排除的理由当时是**假**的 ⇒ ⇒ ⇒ 本轮补上了真正的那道门。
	// ★★ 合并对拍（r1044）：★ 上游一侧同样撤了这两条登记（★ 结论一致），★ 但其依据
	// ★ ★ 是「由 internal/partguard 的 allowedParentDML 白名单管（r1041 版三条已登记）」
	// ★ ★ + 「repair_two_surface_test.go 的逐条断言」⇒ ⇒ ★★ **两处依据都不足以覆盖
	// ★ ★ admin 侧**：★ partguard 的白名单答的是「**允许**写吗」，★ **不答**「两面
	// ★ ★ **成对**吗」（★ 那正是 admin/session_family_delete_two_surface_test.go 的事）；
	// ★ ★ repair_two_surface_test.go 在 cmd/tools/validate_sessions_v2/ 下 ⇒ ★ 只覆盖
	// ★ ★ 那个修复工具 ⇒ ⇒ ★★ 上游的依据里**唯一真正覆盖 admin 侧的两面 DELETE**的，
	// ★ ★ 仍然只有本轮新立的那道门 ⇒ ⇒ ★★ 本条以**本轮的门**为准。
	// ★★ 另采上游一条**属实**的补充：★ 「DELETE FROM 不算读」这条排除本身是有
	// ★ ★ 正反对照的（★ TestBareParentReadIgnoresPairedDelete，★ 在**本文件**）
	// ★ ★ ⇒ ★★ 排除的**行为**是验过的 ⇒ ★★ 缺的只是**覆盖面**，不是排除本身。
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
				// ★ DELETE FROM parent 不是「读」（2026-10-28 分支审计轮）：
				//   wip/02 的 session_trash.go / recording_consent.go 用
				//   「DELETE FROM hot + DELETE FROM 父表」的双面删习语 ⇒ 被本门当裸读误伤。
				//   真读法必须走合并视图（trash 的 INSERT..SELECT 就走 session_bodies_unified）。
				// ★ ★★ r1043 轮修正：★ 这条排除的**依据原先是假的** ——
				// ★ ★★ 注释当时指的是 `repair_two_surface_test.go`，★ 而那一道
				// ★ ★★ （cmd/tools/validate_sessions_v2/ 下）**只覆盖那个修复工具**，
				// ★ ★★ **完全没覆盖 admin 侧** ⇒ ⇒ ★★ 排除一旦生效，admin 侧的
				// ★ ★★ 两面 DELETE 覆盖就**无人断言**。
				// ★ ⇒ ⇒ 现由**真实存在**的那道门管：
				// ★     admin/session_family_delete_two_surface_test.go
				// ★   （★ 逐条断「父表 DELETE ↔ 同 WHERE 的 hot DELETE」成对）。
				// ★ ★★ 本门只管读。
				if kw == "FROM" && endsWithWord(strings.TrimRight(pre[:len(pre)-len(kw)], " \t\n"), "DELETE") {
					break
				}
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

// endsWithWord reports whether s ends with `word` as a complete identifier
// (preceded by a non-identifier char or at string start).
func endsWithWord(s, word string) bool {
	if len(s) < len(word) || !strings.EqualFold(s[len(s)-len(word):], word) {
		return false
	}
	boundary := len(s) == len(word) || !isIdentChar(s[len(s)-len(word)-1])
	return boundary
}

// TestBareParentReadIgnoresPairedDelete 2026-10-28 分支审计轮的反向+正向对照：
// 双面 DELETE 的父表腿不算裸读；真读法（SELECT / INSERT..SELECT）仍要红。
func TestBareParentReadIgnoresPairedDelete(t *testing.T) {
	pairedDelete := `DELETE FROM public.session_bodies_hot WHERE tenant_id = $1;
	                 DELETE FROM public.session_bodies WHERE tenant_id = $1`
	if hits := bareParentReadsIn(pairedDelete); len(hits) != 0 {
		t.Fatalf("双面 DELETE 的父表腿被当裸读误伤：%v", hits)
	}
	bareSelect := `SELECT body FROM public.session_bodies WHERE tenant_id = $1`
	if hits := bareParentReadsIn(bareSelect); len(hits) != 1 {
		t.Fatalf("裸父表 SELECT 应被本门判红，实测 %d 处", len(hits))
	}
	insertSelect := `INSERT INTO public.session_bodies_trash SELECT * FROM public.session_bodies WHERE tenant_id = $1`
	if hits := bareParentReadsIn(insertSelect); len(hits) != 1 {
		t.Fatalf("INSERT..SELECT 裸父表读应被本门判红，实测 %d 处", len(hits))
	}
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
