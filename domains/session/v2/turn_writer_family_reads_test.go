//go:build !integration

// ★ 这个 build tag 是**必需**的：本文件用到的 realDBPool 定义在
//   session_family_p0_realdb_test.go（`//go:build !integration`）⇒
//   没有 tag 时 `-tags=integration` 的整包编译会断链
//   （仓内 scripts/check-build-tags.sh 门查的就是这一条）。
package v2

// R172 · 会话族「轮次读点」去视图化守卫
//
// # 这道门守什么
//
// `domains/session/v2/turn_writer.go` 的三处轮次读点（冲突读回 / GetTurn /
// ListTurns）改读**两臂窄投影**（public.session_turns_hot ∪ public.session_turns）
// 直查，不再打 `public.session_turns_with_current_month` 视图。
//
// 等价性论证（四维：反连接逐字同源 / 投影裸列同源 / RLS 同底表同身份 /
// 谓词可交换）见 `internal/sessionfamily/turns_reads.go` 的头注。
//
// # 四条独立判据
//
//  1. **产物判据**（静态）：三个 helper 的产物必须恰好两臂、键谓词逐字相同、
//     口径钉（ORDER BY / LIMIT）逐字不变、投影自带过滤列与排序键、
//     且**反连接谓词三处逐字相同**。
//  2. **线上同源判据**（真库）：产物里的反连接谓词，归一化后必须**逐字出现在
//     线上视图的定义里** ⇒ 视图改了语义，这道门立刻红。
//  3. **调用点判据**（AST）：turn_writer.go 里三处读点必须调用对应 helper
//     （名字与参数逐字相符），且**任何** SQL 文本里都不得再出现视图名。
//  4. **真库等价判据**（真库）：把**产品自己发出的那条 SQL** 与视图形态放在
//     **同一条语句、同一个快照**里对拍（多重集 + 有序序列）。
//
// ★ 为什么第 4 条要「嵌入产品 SQL」而不是「另写一份等价 SQL」：
//   另写的那份是**手写复刻**，它对了不代表产品对了（仓内已有同族教训）。
//   嵌入产品 SQL ⇒ 对拍的就是线上真正发出去的那一条。

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/internal/sessionfamily"
)

// familyTurnsViewName 是本轮要淘汰的会话族视图名。
const familyTurnsViewName = "session_turns_with_current_month"

// turnsReadColumnNames 是 43 列契约的**期望名单**（与 Scan 顺序一致）。
//
// ★ 这份名单是「列不许增删、不许重排」的锚 —— 改列集必须先改这里，
//   且要意识到 Scan 的顺序跟着变（改了会静默错位读值）。
var turnsReadColumnNames = []string{
	"session_id", "turn_no", "tenant_id", "request_id", "ts",
	"project_id", "namespace", "parent_request_id", "task_type",
	"submit_mode",
	"compression_applied", "compression_strategy", "compression_meta",
	"compression_tokens_saved",
	"injection_verdict", "output_verdict",
	"model", "provider", "credential_id",
	"prompt_tokens", "completion_tokens",
	"cache_read_tokens", "cache_write_tokens",
	"cost_usd",
	"latency_ms", "status_code",
	"success", "error_kind",
	"source_kind", "quality",
	"attachment_count",
	"attachment_total_bytes",
	"multimodal_types",
	"t0_arrived_at", "t1_total_enqueued_at", "t2_total_dequeued_at",
	"t3_model_enqueued_at", "t4_model_dequeued_at", "t5_cred_enqueued_at",
	"t6_cred_dequeued_at", "t7_forward_start_at", "t8_response_start_at",
	"t9_response_end_at",
}

// splitTopLevelColumns 按**顶层逗号**切列清单。
//
// ★ 不能按 "," 切 —— COALESCE(project_id, '') 内部就有逗号。
func splitTopLevelColumns(s string) []string {
	var out []string
	depth := 0
	start := 0
	inStr := byte(0)
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inStr != 0 {
			if c == inStr {
				inStr = 0
			}
			continue
		}
		switch c {
		case '\'', '"':
			inStr = c
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, strings.TrimSpace(s[start:i]))
				start = i + 1
			}
		}
	}
	last := strings.TrimSpace(s[start:])
	if last != "" {
		out = append(out, last)
	}
	return out
}

// columnNameOf 从一个投影项里取出底层列名。
//
//	"session_id"                  → "session_id"
//	"COALESCE(project_id, '')"     → "project_id"
func columnNameOf(expr string) string {
	e := strings.TrimSpace(expr)
	if strings.HasPrefix(e, "COALESCE(") {
		inner := e[len("COALESCE(") : len(e)-1]
		if i := strings.Index(inner, ","); i > 0 {
			return strings.TrimSpace(inner[:i])
		}
		return strings.TrimSpace(inner)
	}
	return e
}

// topLevelKeywordCount 数**顶层**（不在括号里、不在字符串里）关键字的出现次数。
//
// ★ 为什么不能直接 strings.Count：反连接里的 FROM 在括号里，
//   「数 FROM」会把反连接也算进去（仓内同族教训：判据不能靠猜空白/层级）。
func topLevelKeywordCount(sql, kw string) int {
	depth := 0
	inStr := byte(0)
	count := 0
	for i := 0; i < len(sql); i++ {
		c := sql[i]
		if inStr != 0 {
			if c == inStr {
				inStr = 0
			}
			continue
		}
		switch c {
		case '\'', '"':
			inStr = c
			continue
		case '(':
			depth++
			continue
		case ')':
			depth--
			continue
		}
		if depth != 0 {
			continue
		}
		if strings.HasPrefix(sql[i:], kw) {
			count++
			i += len(kw) - 1
		}
	}
	return count
}

// topLevelKeywordIndex 找出**顶层**关键字的位置（找不到返回 -1）。
func topLevelKeywordIndex(sql, kw string) int {
	depth := 0
	inStr := byte(0)
	for i := 0; i < len(sql); i++ {
		c := sql[i]
		if inStr != 0 {
			if c == inStr {
				inStr = 0
			}
			continue
		}
		switch c {
		case '\'', '"':
			inStr = c
			continue
		case '(':
			depth++
			continue
		case ')':
			depth--
			continue
		}
		if depth != 0 {
			continue
		}
		if strings.HasPrefix(sql[i:], kw) {
			return i
		}
	}
	return -1
}

// projectionOf 从一条「SELECT … FROM (…arms…) t …」里取出**外层**投影。
//
// ★ 取的是顶层 SELECT 到顶层 FROM 之间的部分（外层投影）；
//   两臂内部的投影在括号里，不在这一段。
func projectionOf(sql string) string {
	idx := topLevelKeywordIndex(sql, "FROM")
	if idx < 0 {
		return ""
	}
	sel := strings.TrimSpace(sql[:idx])
	return strings.TrimPrefix(sel, "SELECT ")
}

// projectionHas 判断某个列名是否出现在投影里。
func projectionHas(proj, col string) bool {
	for _, item := range splitTopLevelColumns(proj) {
		if columnNameOf(item) == col {
			return true
		}
	}
	return false
}

// 三条臂的正则（互不干扰，见判据 1 的说明）。
var (
	hotArmRe      = regexp.MustCompile(`FROM public\.session_turns_hot\b`)
	parentRefRe   = regexp.MustCompile(`FROM public\.session_turns\b`)
	archivedRefRe = regexp.MustCompile(`FROM public\.session_turns archived\b`)
)

// antiJoinRe 抽出产物里的反连接谓词。
// ★ (?s)：反连接谓词里有换行 ⇒ 不加这个 flag 的 "." 不匹配换行 ⇒ 抽取恒失败
//   （判据恒红比恒绿好，但那是白红）。
var antiJoinRe = regexp.MustCompile(`(?s)NOT EXISTS \(SELECT 1 FROM public\.session_turns archived.*?\)`)

// extractAntiJoin 抽出产物里的反连接谓词文本。
func extractAntiJoin(sql string) string {
	return antiJoinRe.FindString(sql)
}

// normSQL 归一化 SQL 片段，让「逐字同源」的比对不受排版影响。
//
// ★ 归一化只做**排版层**的事（空白、schema 前缀、括号内侧空格、NOT (EXISTS 写法），
//   **不碰任何标识符或运算符** ⇒ 语义差异仍然会被抓到。
func normSQL(s string) string {
	s = strings.ReplaceAll(s, "public.", "")
	s = strings.ReplaceAll(s, "( ", "(")
	s = strings.ReplaceAll(s, " )", ")")
	s = strings.Join(strings.Fields(s), " ")
	s = strings.ReplaceAll(s, "NOT (EXISTS", "NOT EXISTS")
	return s
}

// ── 判据 1：产物形状 ────────────────────────────────────────────────────────

func TestTurnFamilyReadsSQLShape(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		// 口径钉：必须逐字出现的片段
		pin []string
		// 键谓词：两臂各一份 ⇒ 必须各出现两次
		keyPreds []string
		// 排序键：必须出现在**投影**里（否则外层 ORDER BY 会 42703）
		sortKeys []string
	}{
		{
			name: "冲突读回",
			sql:  sessionfamily.TurnConflictLookupSQL("$1", "$2"),
			pin:  []string{"ORDER BY partition_date ASC", "LIMIT 1"},
			keyPreds: []string{
				"tenant_id = $1",
				"request_id = $2",
			},
			sortKeys: []string{"partition_date"},
		},
		{
			name: "GetTurn",
			sql:  sessionfamily.TurnByRequestSQL("$1", "$2"),
			pin:  []string{"LIMIT 1"},
			keyPreds: []string{
				"tenant_id = $1",
				"request_id = $2",
			},
			sortKeys: nil,
		},
		{
			name: "ListTurns",
			sql:  sessionfamily.TurnsBySessionSQL("$1", "$2", "$3"),
			pin:  []string{"ORDER BY turn_no ASC", "LIMIT $3"},
			keyPreds: []string{
				"tenant_id = $1",
				"session_id = $2",
			},
			sortKeys: []string{"turn_no"},
		},
	}

	var predicates []string
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sql := tc.sql
			t.Logf("产物 SQL：\n%s", sql)

			if strings.Contains(sql, familyTurnsViewName) {
				t.Fatalf("产物里又出现会话族视图名 %q —— 本轮的目的就是绕开它的反连接臂：\n%s",
					familyTurnsViewName, sql)
			}
			// ★★ 两条臂各自恰好一次。
			//
			// ★ 为什么用正则而不是「数 FROM」：反连接里也有
			//   `FROM public.session_turns archived` ⇒ 裸数 FROM 会把反连接算进来。
			// ★ 为什么 hot 臂用 `_hot\b`：`session_turns_hot` 里 `_` 是词字符，
			//   `session_turns\b` **不匹配**它（\b 要求非词字符）⇒ 两类互不干扰。
			if n := len(hotArmRe.FindAllString(sql, -1)); n != 1 {
				t.Errorf("hot 臂出现 %d 次（期望恰好 1 次）：\n%s", n, sql)
			}
			// ★ 父表臂 = 「父表引用」−「反连接里的父表引用」。
			//   （Go 的 RE2 **不支持负向先行断言** ⇒ 不能写成 (?! archived)；
			//     仓内教训：判据用的正则必须先确认引擎支持。）
			archived := len(archivedRefRe.FindAllString(sql, -1))
			parent := len(parentRefRe.FindAllString(sql, -1)) - archived
			if archived != 1 {
				t.Errorf("反连接里的父表引用出现 %d 次（期望恰好 1 次）：\n%s", archived, sql)
			}
			if parent != 1 {
				t.Errorf("父表臂出现 %d 次（期望恰好 1 次）：\n%s", parent, sql)
			}
			if n := strings.Count(sql, "UNION ALL"); n != 1 {
				t.Errorf("UNION ALL 出现 %d 次（期望恰好 1 次 = 两臂）：\n%s", n, sql)
			}
			// ★★ 两条臂必须**被括号包住** ⇒ 外层的 ORDER BY / LIMIT 作用于
			//   「并集」而不是「最后一条臂」。
			//   （漏掉这对括号 ⇒ 口径直接变：LIMIT 会只裁父表腿。）
			if n := topLevelKeywordCount(sql, "UNION ALL"); n != 0 {
				t.Errorf("顶层 UNION ALL 出现 %d 次（期望 0 次 = 两臂必须在括号里）：\n%s", n, sql)
			}
			if n := topLevelKeywordCount(sql, "FROM"); n != 1 {
				t.Errorf("顶层 FROM 出现 %d 次（期望恰好 1 次 = 外层那条）：\n%s", n, sql)
			}
			// 键谓词两臂逐字相同 ⇒ 各出现两次。
			for _, p := range tc.keyPreds {
				if n := strings.Count(sql, p); n != 2 {
					t.Errorf("键谓词 %q 出现 %d 次（期望 2 次 = 两臂各一份）：\n%s", p, n, sql)
				}
			}
			// 口径钉。
			for _, p := range tc.pin {
				if !strings.Contains(sql, p) {
					t.Errorf("口径钉 %q 丢失 —— 口径变了，比对就既可能假红也可能假绿：\n%s", p, sql)
				}
			}
			// ★ 排序键必须在投影里（R172 实测踩到过 42703 的同族坑）。
			proj := projectionOf(sql)
			for _, k := range tc.sortKeys {
				if !projectionHas(proj, k) {
					t.Errorf("排序键 %q 不在投影里 ⇒ 外层 ORDER BY 会 42703：\n%s", k, sql)
				}
			}
			// ★★ 反连接必须**在**（承重件：它决定「同一 request_id 在两处时读几份」）。
			pred := extractAntiJoin(sql)
			if pred == "" {
				t.Fatalf("产物里没有反连接 —— 视图的 hot 臂带反连接，去掉它就换了一套语义：\n%s", sql)
			}
			for _, token := range []string{
				"archived.tenant_id::text = hot.tenant_id::text",
				"archived.request_id = hot.request_id",
			} {
				if !strings.Contains(pred, token) {
					t.Errorf("反连接谓词缺 %q：\n%s", token, pred)
				}
			}
			predicates = append(predicates, pred)
		})
	}

	// ★★ 三处读点的反连接谓词必须**逐字相同** —— 口径不一致时，
	//   同一个 request_id 在不同读点会读出不同份数。
	if len(predicates) == 3 {
		for i := 1; i < len(predicates); i++ {
			if predicates[i] != predicates[0] {
				t.Errorf("第 %d 处读点的反连接谓词与第 1 处不一致：\n  1: %s\n  %d: %s",
					i+1, predicates[0], i+1, predicates[i])
			}
		}
	}
}

// TestTurnFamilyReadsProjectionIsFrozen 钉住 43 列契约。
func TestTurnFamilyReadsProjectionIsFrozen(t *testing.T) {
	raw := splitTopLevelColumns(sessionfamily.TurnsReadColumnsRaw)
	outer := splitTopLevelColumns(sessionfamily.TurnsReadColumns)

	if len(raw) != len(turnsReadColumnNames) {
		t.Fatalf("两臂裸列是 %d 列，契约是 %d 列", len(raw), len(turnsReadColumnNames))
	}
	if len(outer) != len(turnsReadColumnNames) {
		t.Fatalf("外层投影是 %d 列，契约是 %d 列", len(outer), len(turnsReadColumnNames))
	}
	for i, want := range turnsReadColumnNames {
		if got := columnNameOf(raw[i]); got != want {
			t.Errorf("第 %d 列：裸列是 %q，契约是 %q（列序不许改 —— Scan 跟着它走）", i+1, got, want)
		}
		if got := columnNameOf(outer[i]); got != want {
			t.Errorf("第 %d 列：外层投影是 %q，契约是 %q", i+1, got, want)
		}
	}
	// ★ 外层投影必须逐项包住 COALESCE（改前就是这么读的），
	//   且**不得**在两臂内部包（视图那一层是裸列 ⇒ 两臂也必须是裸列）。
	coalesced := map[string]bool{
		"project_id": true, "namespace": true, "parent_request_id": true,
		"task_type": true, "compression_tokens_saved": true,
		"injection_verdict": true, "output_verdict": true,
		"prompt_tokens": true, "completion_tokens": true,
		"cache_read_tokens": true, "cache_write_tokens": true,
		"cost_usd": true, "latency_ms": true, "status_code": true,
		"success": true, "attachment_count": true,
		"attachment_total_bytes": true, "multimodal_types": true,
	}
	for i, item := range outer {
		want := columnNameOf(item)
		if coalesced[want] && !strings.HasPrefix(strings.TrimSpace(item), "COALESCE(") {
			t.Errorf("第 %d 列 %q 在外层丢了 COALESCE ⇒ 读到的值与改前不同（NULL vs 空串）", i+1, want)
		}
	}
	for i, item := range raw {
		if strings.Contains(strings.ToUpper(item), "COALESCE") {
			t.Errorf("第 %d 列（%q）在两臂内部带了 COALESCE —— 视图那一层是**裸列**，带了就不逐字等价",
				i+1, columnNameOf(item))
		}
	}
}

// 两条臂的**键谓词**提取正则。
//
// ★ hot 臂的键谓词到「AND NOT EXISTS」为止（反连接是视图带的承重件，不算键谓词）；
// ★ 父表臂的键谓词到该臂的右括号为止。
//
// ★ 已知限制（写明以免后人误读）：两条正则都用**非贪婪**匹配 ⇒ 若将来键谓词里
//   出现带括号的函数调用，提取会被提前截断 ⇒ 表现为**假红**（安全方向：宁可白红，
//   不可假绿）。
var (
	hotArmKeyRe    = regexp.MustCompile(`(?s)FROM public\.session_turns_hot hot\s+WHERE\s+(.*?)\s+AND NOT EXISTS`)
	parentArmKeyRe = regexp.MustCompile(`(?s)FROM public\.session_turns\s+WHERE\s+(.*?)\)`)
)

// armKeyPredicates 拆出两条臂各自的键谓词。
func armKeyPredicates(sql string) (hot, parent string, ok bool) {
	h := hotArmKeyRe.FindStringSubmatch(sql)
	p := parentArmKeyRe.FindStringSubmatch(sql)
	if h == nil || p == nil {
		return "", "", false
	}
	return strings.TrimSpace(h[1]), strings.TrimSpace(p[1]), true
}

// TestTurnFamilyReadsArmsKeyPredicatesMatch 判据：两条臂的**键谓词必须逐字相同**。
//
// ★★ 这条判据是被**变异臂逼出来的**（R172 的 M8 臂）：
//   把父表臂的谓词改成 `… AND tenant_id IS NOT NULL` ⇒ 在**当前数据**上结果不变
//   （tenant_id 恒非空）⇒ 真库等价判据抓不到 ⇒ 形状判据也抓不到（因为
//   「tenant_id = $1」这个**子串**仍然出现两次）⇒ ⇒ **门漏了**。
//   ⇒ 唯一的可靠判据是**逐字比对两条臂的键谓词**。
// ⇒ ★ 这也是「真库判据只能证明【当前数据】」的典型：语义差异可能在**别的数据**上显形。
func TestTurnFamilyReadsArmsKeyPredicatesMatch(t *testing.T) {
	cases := []struct {
		name string
		sql  string
	}{
		{"冲突读回", sessionfamily.TurnConflictLookupSQL("$1", "$2")},
		{"GetTurn", sessionfamily.TurnByRequestSQL("$1", "$2")},
		{"ListTurns", sessionfamily.TurnsBySessionSQL("$1", "$2", "$3")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hot, parent, ok := armKeyPredicates(tc.sql)
			if !ok {
				t.Fatalf("拆不出两条臂的键谓词 —— 判据的提取规则失效（白红，不是通过）：\n%s", tc.sql)
			}
			if hot == "" || parent == "" {
				t.Fatalf("键谓词为空（hot=%q parent=%q）：\n%s", hot, parent, tc.sql)
			}
			if hot != parent {
				t.Errorf("两条臂的键谓词不一致：\n  hot 臂：%q\n  父表臂：%q\n"+
					"⇒ 同一把键在两条臂上口径不同 ⇒ 一条臂会多读/少读（"+
					"在当前数据上可能看不出来，换一批数据就会显形）", hot, parent)
			}
		})
	}
}

// ── 判据 2：调用点（AST）────────────────────────────────────────────────────

type sqlCallSite struct {
	Func   string
	Method string
	// Kind: "literal" | "ident" | "helper"
	Kind string
	// Text: literal 时的 SQL 文本（含拼接片段）
	Text string
	// Helper: helper 调用的名字
	Helper string
	// HelperArgs: helper 调用的实参（字面量原样）
	HelperArgs []string
}

// collectSQLCalls 抽出 turn_writer.go 里每一处 SQL 调用（Query / QueryRow / Exec）。
//
// ★ pgx 的签名统一是 (ctx, sql, args...) ⇒ SQL 是**第二个**实参。
func collectSQLCalls(t *testing.T) []sqlCallSite {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "turn_writer.go", nil, 0)
	if err != nil {
		t.Fatalf("解析 turn_writer.go 失败：%v", err)
	}
	var out []sqlCallSite
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		cur := fd.Name.Name
		// ★ 先收集「局部变量 := sessionfamily.Xxx(...)」的映射：
		//   读点把 SQL 存进局部变量再传给 Query/QueryRow ⇒ SQL 实参是
		//   一个 Ident，不解析这层映射就永远看不到 helper（判据恒红）。
		type helperBinding struct {
			name string
			args []string
		}
		var helperVars = map[string]helperBinding{}
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			as, ok := n.(*ast.AssignStmt)
			if !ok || len(as.Rhs) != 1 || len(as.Lhs) != 1 {
				return true
			}
			id, ok := as.Lhs[0].(*ast.Ident)
			if !ok {
				return true
			}
			if name, args, ok := helperCall(as.Rhs[0]); ok {
				helperVars[id.Name] = helperBinding{name, args}
			}
			return true
		})
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			switch sel.Sel.Name {
			case "Query", "QueryRow", "Exec":
			default:
				return true
			}
			if len(call.Args) < 2 {
				return true
			}
			site := sqlCallSite{Func: cur, Method: sel.Sel.Name}
			describeSQLArg(call.Args[1], &site)
			// ★ SQL 实参是局部变量 ⇒ 解析到它绑定的那次 helper 调用。
			if site.Kind == "ident" {
				if hv, ok := helperVars[site.Text]; ok {
					site.Kind = "helper"
					site.Helper = hv.name
					site.HelperArgs = hv.args
				}
			}
			out = append(out, site)
			return true
		})
	}
	return out
}

// describeSQLArg 把 SQL 实参归类。
func describeSQLArg(e ast.Expr, site *sqlCallSite) {
	switch v := e.(type) {
	case *ast.BasicLit:
		if v.Kind != token.STRING {
			site.Kind = "ident"
			return
		}
		s, err := strconv.Unquote(v.Value)
		if err != nil {
			site.Kind = "ident"
			return
		}
		site.Kind = "literal"
		site.Text = s
	case *ast.Ident:
		site.Kind = "ident"
		site.Text = v.Name
	case *ast.CallExpr:
		if name, args, ok := helperCall(v); ok {
			site.Kind = "helper"
			site.Helper = name
			site.HelperArgs = args
			return
		}
		site.Kind = "ident"
	case *ast.BinaryExpr:
		// 拼接：把字面量片段拼起来，helper 调用记成 <splice:Name>。
		var b strings.Builder
		kind := "literal"
		helper := ""
		var hargs []string
		for _, part := range splitBinary(v) {
			if name, args, ok := helperCall(part); ok {
				b.WriteString("<splice:" + name + ">")
				kind = "helper"
				helper = name
				hargs = args
				continue
			}
			if lit, ok := part.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				if s, err := strconv.Unquote(lit.Value); err == nil {
					b.WriteString(s)
					continue
				}
			}
			b.WriteString("<splice>")
			if kind == "literal" {
				kind = "ident"
			}
		}
		site.Kind = kind
		site.Text = b.String()
		site.Helper = helper
		site.HelperArgs = hargs
	default:
		site.Kind = "ident"
	}
}

// splitBinary 把 a+b+c 拆成叶子实参。
func splitBinary(e ast.Expr) []ast.Expr {
	if b, ok := e.(*ast.BinaryExpr); ok {
		if b.Op != token.ADD {
			return []ast.Expr{e}
		}
		return append(splitBinary(b.X), splitBinary(b.Y)...)
	}
	return []ast.Expr{e}
}

// helperCall 识别 `sessionfamily.XXX(...)` 形态的调用。
func helperCall(e ast.Expr) (string, []string, bool) {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return "", nil, false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return "", nil, false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || pkg.Name != "sessionfamily" {
		return "", nil, false
	}
	args := make([]string, 0, len(call.Args))
	for _, a := range call.Args {
		lit, ok := a.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return "", nil, false
		}
		s, err := strconv.Unquote(lit.Value)
		if err != nil {
			return "", nil, false
		}
		args = append(args, s)
	}
	return sel.Sel.Name, args, true
}

// TestTurnFamilyReadsCallSites pins the three read points to the helpers.
//
// ★ 判据落在「调用点」而不是「源码里有没有这个词」——
//   注释里提到视图名不算违规，SQL 里提到才算。
func TestTurnFamilyReadsCallSites(t *testing.T) {
	sites := collectSQLCalls(t)
	if len(sites) == 0 {
		t.Fatal("没抽出任何 SQL 调用 —— 判据的扫描范围失效（会让「0 命中」变成恒绿）")
	}

	// ★ 任何 SQL 文本里都不得再出现视图名。
	for _, s := range sites {
		if strings.Contains(s.Text, familyTurnsViewName) {
			t.Errorf("%s 的 %s 里又出现会话族视图名 %q：\n%s",
				s.Func, s.Method, familyTurnsViewName, s.Text)
		}
	}

	want := []struct {
		fn     string
		helper string
		args   []string
	}{
		{"appendTurnInLockedTx", "TurnConflictLookupSQL", []string{"$1", "$2"}},
		{"GetTurn", "TurnByRequestSQL", []string{"$1", "$2"}},
		{"ListTurns", "TurnsBySessionSQL", []string{"$1", "$2", "$3"}},
	}
	for _, w := range want {
		found := false
		for _, s := range sites {
			if s.Func != w.fn || s.Helper != w.helper {
				continue
			}
			found = true
			if len(s.HelperArgs) != len(w.args) {
				t.Errorf("%s 的 %s 实参是 %v，期望 %v", w.fn, w.helper, s.HelperArgs, w.args)
				continue
			}
			for i := range w.args {
				if s.HelperArgs[i] != w.args[i] {
					t.Errorf("%s 的 %s 第 %d 个实参是 %q，期望 %q",
						w.fn, w.helper, i+1, s.HelperArgs[i], w.args[i])
				}
			}
		}
		if !found {
			t.Errorf("%s 里没有调用 %s —— 三处读点的去视图改动被回退了", w.fn, w.helper)
		}
	}
}

// ── 判据 3：真库 —— 线上同源 + 逐值等价 ────────────────────────────────────

// TestTurnFamilyReadsEquivalenceRealDB 在真库上把**产品自己发出的那条 SQL**
// 与视图形态放在同一条语句、同一个快照里对拍。
//
// ★ 需要 TEST_DATABASE_URL（没设就跳过，不假装通过）。
func TestTurnFamilyReadsEquivalenceRealDB(t *testing.T) {
	pool := realDBPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	// 前提 0：视图还在，且还是「反连接」形态 —— 等价性论证的前提本身。
	var viewDef string
	if err := pool.QueryRow(ctx,
		"SELECT pg_get_viewdef('public."+familyTurnsViewName+"'::regclass, true)").Scan(&viewDef); err != nil {
		t.Fatalf("取视图定义失败（视图没了 ⇒ 本门的前提不成立）：%v", err)
	}
	if !strings.Contains(viewDef, "EXISTS") {
		t.Fatalf("视图 %s 不再是反连接形态 ⇒ 等价性论证的前提变了，请重新评估：\n%s",
			familyTurnsViewName, viewDef)
	}

	// ★★ 判据 2：产物里的反连接谓词必须与**线上视图定义**里的那条同源。
	//
	// ★ 这是本轮最承重的一条判据：它把「等价性」从「我们以为视图长这样」
	//   变成「视图此刻就是这么长这样」⇒ 视图语义一改，这道门立刻红。
	prodPred := normSQL(extractAntiJoin(sessionfamily.TurnByRequestSQL("$1", "$2")))
	if prodPred == "" {
		t.Fatal("产物里抽不出反连接谓词 —— 判据的提取规则失效")
	}
	normView := normSQL(viewDef)
	if !strings.Contains(normView, prodPred) {
		t.Errorf("产物里的反连接谓词与线上视图定义**不同源**：\n  产物：%s\n  视图：%s",
			prodPred, normView)
	} else {
		t.Logf("判据 2 通过：产物的反连接谓词逐字出现在线上视图定义里\n  %s", prodPred)
	}

	// 观察项（不再是硬前提）：hot ∩ 父表的重叠对数。
	// ★ 因为反连接被保留，等价性**不依赖**它为 0 —— 这里只做记录，
	//   用来观察「视图的反连接此刻剔了几行」。
	var overlap int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)::int FROM (
			SELECT DISTINCT h.tenant_id, h.request_id
			  FROM public.session_turns_hot h
			  JOIN public.session_turns a
			    ON a.tenant_id = h.tenant_id AND a.request_id = h.request_id
		) z`).Scan(&overlap); err != nil {
		t.Fatalf("查 hot ∩ 父表 失败：%v", err)
	}
	t.Logf("观察项：hot ∩ 父表 = %d 对（反连接被保留 ⇒ 等价性不依赖它为 0）", overlap)

	proj := sessionfamily.TurnsReadColumns
	arms := sessionfamily.TurnsArmsSQL(sessionfamily.TurnsReadColumnsRaw, "TRUE")

	// 判据 3a：整族逐行多重集等价（产品 arms vs 视图）。
	var vRows, aRows, vOnly, aOnly int
	if err := pool.QueryRow(ctx, `
		WITH v AS (SELECT `+proj+` FROM public.`+familyTurnsViewName+`),
		     a AS (SELECT `+proj+` FROM `+arms+` t)
		SELECT (SELECT count(*)::int FROM v),
		       (SELECT count(*)::int FROM a),
		       (SELECT count(*)::int FROM (SELECT * FROM v EXCEPT ALL SELECT * FROM a) x),
		       (SELECT count(*)::int FROM (SELECT * FROM a EXCEPT ALL SELECT * FROM v) y)`).
		Scan(&vRows, &aRows, &vOnly, &aOnly); err != nil {
		t.Fatalf("整族对拍失败：%v", err)
	}
	t.Logf("整族：视图 %d 行 · 产品 %d 行 · 只在视图 %d · 只在产品 %d", vRows, aRows, vOnly, aOnly)
	if vRows == 0 {
		t.Fatal("视图侧 0 行 ⇒ 样本是空的，对拍没有意义")
	}
	if vOnly != 0 || aOnly != 0 {
		t.Errorf("整族逐行不等价：只在视图 %d 行 · 只在产品 %d 行", vOnly, aOnly)
	}

	// 判据 3b~3d：三处读点各自的形态（**嵌入产品 SQL**）。
	keys := sampleTurnKeys(t, pool, ctx)
	if len(keys) == 0 {
		t.Skip("真库里没有可用的样本键（既没有 hot 行也没有父表行）")
	}

	for _, k := range keys {
		// ① 冲突读回（3 列，ORDER BY partition_date ASC LIMIT 1）
		product := sessionfamily.TurnConflictLookupSQL("$1", "$2")
		viewForm := `SELECT session_id, turn_no, partition_date
			FROM public.` + familyTurnsViewName + `
			WHERE tenant_id = $1 AND request_id = $2
			ORDER BY partition_date ASC
			LIMIT 1`
		assertSameRows(t, ctx, pool, "冲突读回", viewForm, product,
			[]any{k.tenant, k.request}, "")

		// ② GetTurn（43 列，LIMIT 1）
		product = sessionfamily.TurnByRequestSQL("$1", "$2")
		viewForm = `SELECT ` + proj + `
			FROM public.` + familyTurnsViewName + `
			WHERE tenant_id = $1 AND request_id = $2
			LIMIT 1`
		assertSameRows(t, ctx, pool, "GetTurn", viewForm, product,
			[]any{k.tenant, k.request}, "")

		// ③ ListTurns（43 列，ORDER BY turn_no ASC LIMIT 100）
		product = sessionfamily.TurnsBySessionSQL("$1", "$2", "$3")
		viewForm = `SELECT ` + proj + `
			FROM public.` + familyTurnsViewName + `
			WHERE tenant_id = $1 AND session_id = $2
			ORDER BY turn_no ASC
			LIMIT $3`
		assertSameRows(t, ctx, pool, "ListTurns", viewForm, product,
			[]any{k.tenant, k.session, 100}, "turn_no")
	}
}

// turnKey 是一个真实键样本。
type turnKey struct {
	tenant  string
	request string
	session string
}

// sampleTurnKeys 取真实样本键：**两腿都取**（hot 里一条、父表里一条）。
//
// ★ 只取 hot 的键会漏掉「反连接生效」的那一半（R169 的教训）。
func sampleTurnKeys(t *testing.T, pool *pgxpool.Pool, ctx context.Context) []turnKey {
	t.Helper()
	var out []turnKey

	collect := func(sql string) {
		rows, err := pool.Query(ctx, sql)
		if err != nil {
			t.Fatalf("抽样失败：%v", err)
		}
		defer rows.Close()
		for rows.Next() {
			var k turnKey
			if err := rows.Scan(&k.tenant, &k.request, &k.session); err != nil {
				t.Fatalf("扫描样本失败：%v", err)
			}
			out = append(out, k)
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("样本遍历失败：%v", err)
		}
	}

	// hot 腿（近窗）
	collect(`SELECT tenant_id::text, request_id::text, session_id::text
		FROM public.session_turns_hot
		WHERE ts > now() - interval '30 days'
		ORDER BY ts DESC LIMIT 5`)
	// 父表腿（近窗）
	collect(`SELECT tenant_id::text, request_id::text, session_id::text
		FROM public.session_turns
		WHERE ts > now() - interval '30 days'
		ORDER BY ts DESC LIMIT 5`)

	return out
}

// assertSameRows 把「视图形态」与「产品形态」放进同一条语句对拍。
func assertSameRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool,
	label, viewForm, product string, args []any, orderCol string) {
	t.Helper()
	var vCount, aCount, vOnly, aOnly int
	var vSeq, aSeq []string
	// orderCol 为空 ⇒ 结果至多 1 行（无序聚合即可）；
	// 否则按该列排序后逐行比对（口径里的排序键）。
	orderV, orderA := "", ""
	if orderCol != "" {
		orderV = " ORDER BY v." + orderCol
		orderA = " ORDER BY a." + orderCol
	}
	q := `
		WITH v AS (` + viewForm + `),
		     a AS (` + product + `)
		SELECT (SELECT count(*)::int FROM v),
		       (SELECT count(*)::int FROM a),
		       (SELECT count(*)::int FROM (SELECT * FROM v EXCEPT ALL SELECT * FROM a) x),
		       (SELECT count(*)::int FROM (SELECT * FROM a EXCEPT ALL SELECT * FROM v) y),
		       (SELECT coalesce(array_agg(v::text` + orderV + `), '{}'::text[]) FROM v),
		       (SELECT coalesce(array_agg(a::text` + orderA + `), '{}'::text[]) FROM a)`
	if err := pool.QueryRow(ctx, q, args...).Scan(&vCount, &aCount, &vOnly, &aOnly, &vSeq, &aSeq); err != nil {
		t.Fatalf("%s 对拍失败：%v", label, err)
	}
	if vOnly != 0 || aOnly != 0 {
		t.Errorf("%s：视图 %d 行 / 产品 %d 行，但只在视图 %d 行 / 只在产品 %d 行 ⇒ 两者不等价",
			label, vCount, aCount, vOnly, aOnly)
	}
	if len(vSeq) != len(aSeq) {
		t.Errorf("%s：结果序列长度不同（视图 %d / 产品 %d）", label, len(vSeq), len(aSeq))
		return
	}
	for i := range vSeq {
		if vSeq[i] != aSeq[i] {
			t.Errorf("%s：第 %d 行的值不同\n  视图：%s\n  产品：%s", label, i+1, vSeq[i], aSeq[i])
			return
		}
	}
}
