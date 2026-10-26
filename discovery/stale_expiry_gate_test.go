// stale_expiry_gate_test.go — 2026-10-02 起，2026-10-21 改判据。
//
// 钉住 stale 下架的**宽限守卫**：`expireStaleModels` 的宽限守卫是**否定式**的
// （`NOT EXISTS (近 N 小时成功)`）。当**证据源停止写入**时该子查询结构性恒空，
// 守卫从「已验证近期无成功」退化成「根本没有证据」，而代码会照常下架 ——
// 主动禁用仍在工作的凭据模型，且无异常信号。
//
// 缺陷所在分支（证据源停止）在线上要等停写之后才出现，集成测试永远撞不到，
// 所以判定抽成纯函数 `staleExpiryMayRun` 无库可测；再用 AST 钉住
// `expireStaleModels` 真的调用了它——否则删掉调用、留下纯函数，门照样绿。
//
// ★ 2026-10-21（属主裁决「退役该护栏」）：**判据换源，护栏不撤**。
//
//	原判据：settings.RequestLogsWriteEnabled()（问「v1 能否写」）
//	新判据：会话族证据源近期是否有成功行（问「证据在不在」）
//
// 原判据在 890 退役 v1 之后恒为 false ⇒ 护栏恒 skip ⇒ 陈旧模型永不再下架。
// 新判据直接问要问的问题 ⇒ 护栏重新有判据，同时保留安全方向
// （证据缺失时不动）。
//
// ⇒ 本文件因此有**两组**断言：
//	A 组：纯函数语义（判据真 ⇒ 放行；判据假 ⇒ 阻断 + 给出可读原因）
//	B 组：AST 钉住「调用点存在 + 分支含 return + 位置在 UPDATE 前」
//	C 组：★ 钉住「判据不再问 v1」——这是本轮退役的**核心不变量**。
package discovery

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestStaleExpiryMayRun(t *testing.T) {
	tests := []struct {
		name                 string
		evidenceHasRecentHit bool
		wantMayRun           bool
	}{
		{"证据源近期有成功行 → 允许（保持原行为）", true, true},
		{"证据源近期无成功行 → 禁止（护栏生效）", false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mayRun, reason := staleExpiryMayRun(tc.evidenceHasRecentHit)
			if mayRun != tc.wantMayRun {
				t.Fatalf("staleExpiryMayRun(%v) = %v, want %v", tc.evidenceHasRecentHit, mayRun, tc.wantMayRun)
			}
			// 禁止时必须给出可读原因：静默阻断会让运维看不出「为什么模型不下架了」。
			if !mayRun && strings.TrimSpace(reason) == "" {
				t.Error("禁止下架时 blocked reason 为空——护栏会变成又一次静默行为改变")
			}
			if mayRun && reason != "" {
				t.Errorf("允许下架时不应带阻断原因，得到 %q", reason)
			}
		})
	}
}

// TestStaleExpiryMayRunReasonIsDistinctFromSilence 钉住「阻断 ≠ 无输出」：
// 阻断必须留痕，否则它只是把「误下架」换成了「静默不下架」，问题没解决，
// 只是换了个方向且更难查。
//
// ★ 2026-10-21：原因文案**不再点名 _request_logs 作为当前证据源**
// （它已退役），但**必须保留该词的来历**——运维要能查到这道护栏
// 当初依赖的是什么。⇒ 断言从「必须含 _request_logs」改为
// 「必须说明证据源是会话族，且必须留有历史来历说明」。
func TestStaleExpiryMayRunReasonIsDistinctFromSilence(t *testing.T) {
	_, reason := staleExpiryMayRun(false)
	if !strings.Contains(reason, "会话族") {
		t.Errorf("阻断原因必须点名当前的证据源（会话族），否则读日志的人无法定位；得到 %q", reason)
	}
	if !strings.Contains(reason, "_request_logs") {
		t.Errorf("阻断原因必须保留 v1 证据源的历史来历说明（运维要查它当初依赖什么）；得到 %q", reason)
	}
}

// TestExpireStaleModelsConsultsTheGate 用 AST 钉住调用点**及其位置与终止性**。
//
// 三条断言缺一不可，每条都对应一个已实测能变绿的变异：
//
//	A. 调用存在        —— 变异「挪到 UPDATE 之后」被此条抓住
//	B. 分支里有 return —— 变异「保留调用与日志、只删 return」被此条抓住
//	C. 位置在 UPDATE 前 —— 变异「挪到循环之后」被此条抓住
//
// 只写 A 是不够的：2026-10-02 实测，删掉 return 的变体让本文件全部测试
// **依然全绿**，而护栏已形同虚设——门测的不是它声称测的那个东西，而且它绿着。
// B 这条断言就是为了钉住「计算了不等于生效」。
func TestExpireStaleModelsConsultsTheGate(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filepath.Join(filepath.Dir(file), "discovery.go"), nil, 0)
	if err != nil {
		t.Fatalf("parse discovery.go: %v", err)
	}

	var fn *ast.FuncDecl
	for _, decl := range f.Decls {
		if d, ok := decl.(*ast.FuncDecl); ok && d.Name.Name == "expireStaleModels" {
			fn = d
			break
		}
	}
	if fn == nil {
		t.Fatal("discovery.go 里找不到 expireStaleModels")
	}

	guardIdx, updateIdx := -1, -1
	for i, stmt := range fn.Body.List {
		// ★ 2026-10-22（§145 轮）：UPDATE 字面量搬进了纯函数
		// staleExpiryOffersSQL（因为它不能再过 fmt.Sprintf——投影里有字面 %，
		// 生产实证 2026-10-21 语法错事故）。这里改钉「调用了那个纯函数」，
		// 并在下方单独钉住 UPDATE 字面量仍在纯函数体内、没有漂走。
		if updateIdx < 0 && callsName(stmt, "staleExpiryOffersSQL") {
			updateIdx = i
		}
		if ifStmt, ok := stmt.(*ast.IfStmt); ok &&
			(callsName(ifStmt.Init, "staleExpiryMayRun") || callsName(ifStmt.Cond, "staleExpiryMayRun")) {
			guardIdx = i
			// 断言 B：分支体必须真的会终止。
			if !hasReturn(ifStmt.Body.List) {
				t.Error("护栏分支里没有 return——计算了阻断理由却不阻断，\n" +
					"护栏形同虚设而本文件其余测试仍会全绿。必须 return。")
			}
		}
	}

	if guardIdx < 0 {
		t.Fatal("expireStaleModels 没有调用 staleExpiryMayRun——\n" +
			"纯函数与它的表测试会照样全绿，而线上会在证据源停写后继续把\n" +
			"仍在工作的凭据模型判为 auto_discovery_expired 并下架。")
	}
	if updateIdx < 0 {
		t.Fatal("expireStaleModels 里找不到 staleExpiryOffersSQL 调用——本测试的前提变了，请重新核对")
	}
	// 断言 A′：UPDATE model_offers 字面量必须仍住在 staleExpiryOffersSQL 里。
	// （SQL 搬家时若无此钉，写下架行的那条语句可以整条消失而本门照绿。）
	var sqlFn *ast.FuncDecl
	for _, decl := range f.Decls {
		if d, ok := decl.(*ast.FuncDecl); ok && d.Name.Name == "staleExpiryOffersSQL" {
			sqlFn = d
			break
		}
	}
	if sqlFn == nil {
		t.Fatal("discovery.go 里找不到 staleExpiryOffersSQL——SQL 组装被再度搬家，请同步更新本门")
	}
	if !stmtMentionsFuncBody(sqlFn, "UPDATE model_offers") {
		t.Fatal("staleExpiryOffersSQL 体内不再含 UPDATE model_offers——下架 SQL 漂走了")
	}
	// 断言 C：护栏必须在下架写入**之前**。
	if guardIdx > updateIdx {
		t.Errorf("护栏在第 %d 条语句，staleExpiryOffersSQL（下架写入）在第 %d 条——护栏在下架之后才执行，等于没有",
			guardIdx, updateIdx)
	}
}

// stmtMentionsFuncBody 判定函数体源码里是否含 needle（原文形态，剥不剥空白均可）。
func stmtMentionsFuncBody(fn *ast.FuncDecl, needle string) bool {
	var body string
	ast.Inspect(fn, func(n ast.Node) bool {
		if bl, ok := n.(*ast.BasicLit); ok {
			body += bl.Value
		}
		return true
	})
	return strings.Contains(body, needle)
}

// TestStaleExpiryCriterionIsTheEvidenceProbe 钉住**调用点的实参**必须是
// 证据探针的结果，而不是某个常量。
//
// 为什么这条不能省：上一条（DoesNotAskV1）只保证「不再问 v1」。
// 而「传一个常量」同样能通过它——实测变异 M1 把
//
//	staleExpiryMayRun(s.sessionFamilyHasRecentSuccess(ctx))
//	staleExpiryMayRun(false)
//
// 改掉后，前一条**照样绿**：既没问 v1，也不是常量以外的任何东西。
// ⇒ 门必须直接钉住「实参 = 探针调用」这一条不变量。
//
// ⚠ 这条断言也钉住了「护栏的判据是**实测事实**」这个设计意图：
// 一旦有人把判据改回某个开关（哪怕是新开关），这里就会红。
func TestStaleExpiryCriterionIsTheEvidenceProbe(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filepath.Join(filepath.Dir(file), "discovery.go"), nil, 0)
	if err != nil {
		t.Fatalf("parse discovery.go: %v", err)
	}

	var fn *ast.FuncDecl
	for _, decl := range f.Decls {
		if d, ok := decl.(*ast.FuncDecl); ok && d.Name.Name == "expireStaleModels" {
			fn = d
			break
		}
	}
	if fn == nil {
		t.Fatal("discovery.go 里找不到 expireStaleModels")
	}

	// 找出护栏调用点，并把它的**实参**取出来。
	var arg ast.Node
	found := false
	ast.Inspect(fn, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if !callsNameOnCallee(call, "staleExpiryMayRun") {
			return true
		}
		if len(call.Args) != 1 {
			t.Errorf("staleExpiryMayRun 的实参个数 = %d（应为 1）", len(call.Args))
			return true
		}
		arg = call.Args[0]
		found = true
		return false
	})
	if !found {
		t.Fatal("expireStaleModels 里找不到 staleExpiryMayRun 的调用")
	}
	if arg == nil {
		t.Fatal("没能取到 staleExpiryMayRun 的实参")
	}

	// 实参必须是「探针方法的调用」，不能是常量 / 字面量 / 别的开关。
	switch v := arg.(type) {
	case *ast.CallExpr:
		sel, ok := v.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "sessionFamilyHasRecentSuccess" {
			t.Errorf("staleExpiryMayRun 的实参是调用 %T，但不是 sessionFamilyHasRecentSuccess 的调用——\n"+
				"判据必须是**证据探针的实测结果**。实测变异 M1（传常量 false）\n"+
				"会让「不再问 v1」那条断言照样绿 ⇒ 必须直接钉住实参。", arg)
		}
	case *ast.Ident:
		t.Errorf("staleExpiryMayRun 的实参是标识符 %q（常量/变量）——\n"+
			"判据必须是证据探针的实测结果，而不是常量。", v.Name)
	case *ast.BasicLit:
		t.Errorf("staleExpiryMayRun 的实参是字面量 %s——\n"+
			"判据必须是证据探针的实测结果，而不是常量（恒 false = 护栏恒 skip，\n"+
			"恒 true = 无保护），这两者都不是「问证据源在不在」。", v.Value)
	default:
		t.Errorf("staleExpiryMayRun 的实参类型是 %T，期望是 sessionFamilyHasRecentSuccess 的调用", arg)
	}
}

// TestStaleExpiryCriterionDoesNotAskV1 钉住本轮退役的**核心不变量**：
// 判据**不再问 v1**。
//
// 2026-10-21 之前的判据是 `settings.RequestLogsWriteEnabled()`。v1 随 890
// 退役之后，它恒为 false ⇒ 护栏恒 skip ⇒ 陈旧模型永不再自动下架
// （211 生产日志实证）。退役的正确形态是**换源**（改问会话族证据源），
// 而不是删掉判断。
//
// ⚠ 这条断言**必须剥注释**后再看：注释里出现 `settings.RequestLogsWriteEnabled()`
// 是**正常的**（那是在解释「当初的问法错在哪」），而代码里再出现就是回归。
// ⇒ 判据用 AST 找**调用表达式**（CallExpr），而不是文本匹配。
func TestStaleExpiryCriterionDoesNotAskV1(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filepath.Join(filepath.Dir(file), "discovery.go"), nil, 0)
	if err != nil {
		t.Fatalf("parse discovery.go: %v", err)
	}

	// 找出所有对 settings.RequestLogsWriteEnabled 的**调用**（AST 层面，
	// 注释里的同名字符串不会被误判为调用）。
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		ast.Inspect(fn, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if sel.Sel.Name != "RequestLogsWriteEnabled" {
				return true
			}
			t.Errorf("discovery.go:%d 的 %s 仍在调用 settings.RequestLogsWriteEnabled()——\n"+
				"v1 已随 890 退役，这个判据恒为 false ⇒ 护栏恒 skip ⇒\n"+
				"陈旧模型永不再自动下架（211 生产日志实证）。\n"+
				"退役的正确形态是**换源**（问会话族证据源近期有无成功行），\n"+
				"不是删掉判断，也不是换个源继续问。",
				fset.Position(call.Pos()).Line, fn.Name.Name)
			return true
		})
	}
}

// TestStaleExpiryCriterionQueriesSessionFamily 钉住新判据**真的查了会话族**。
//
// 只断言「不再问 v1」是不够的：那可以靠「什么都不查」来满足。
// ⇒ 必须钉住：判据的实测实现里出现会话族两张表的成功行计数。
//
// ⚠ 同样是 AST 层面：查的是 `session_turns_hot` / `session_turns` 这两个
// **标识符**出现在 SQL 字面量里。
func TestStaleExpiryCriterionQueriesSessionFamily(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filepath.Join(filepath.Dir(file), "discovery.go"), nil, 0)
	if err != nil {
		t.Fatalf("parse discovery.go: %v", err)
	}

	var fn *ast.FuncDecl
	for _, decl := range f.Decls {
		if d, ok := decl.(*ast.FuncDecl); ok && d.Name.Name == "sessionFamilyHasRecentSuccess" {
			fn = d
			break
		}
	}
	if fn == nil {
		t.Fatal("discovery.go 里找不到 sessionFamilyHasRecentSuccess——\n" +
			"护栏的新判据必须有实测实现（查会话族近期有无成功行），\n" +
			"不能只把旧判据删掉了事。")
	}

	// 收集该函数里所有字符串字面量（SQL 在 raw string 里）。
	var lits []string
	ast.Inspect(fn, func(n ast.Node) bool {
		if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
			lits = append(lits, lit.Value)
		}
		return true
	})
	joined := strings.Join(lits, "\n")

	for _, needle := range []string{"session_turns_hot", "session_turns", "success = TRUE"} {
		if !strings.Contains(joined, needle) {
			t.Errorf("sessionFamilyHasRecentSuccess 的 SQL 里没有 %q——\n"+
				"新判据必须直接查会话族证据源（hot ∪ parent）的成功行，\n"+
				"否则「证据在不在」这个问题根本没有被问。", needle)
		}
	}
}

// TestStaleExpiryGraceWindowIsShared 钉住「探针窗口」与「守卫窗口」同源。
//
// 两者若漂移，会出现：探针说「有证据」（因为它在 48h 窗口里看到了行），
// 而守卫自己在 24h 窗口里看不到任何行 ⇒ 否定式守卫退化成「没有证据」
// ⇒ 正是这道门要防的那种退化。
//
// ⇒ 断言 staleExpiryGraceHours 只有**一个**实现点，且 expireStaleModels
// 用的就是它（不是内联的 24）。
func TestStaleExpiryGraceWindowIsShared(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filepath.Join(filepath.Dir(file), "discovery.go"), nil, 0)
	if err != nil {
		t.Fatalf("parse discovery.go: %v", err)
	}

	// 统计读取 DISCOVERY_GRACE_HOURS 的位置数（必须恰好 1）。
	envReads := 0
	for _, decl := range f.Decls {
		ast.Inspect(decl, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			if strings.Contains(lit.Value, "DISCOVERY_GRACE_HOURS") {
				envReads++
			}
			return true
		})
	}
	if envReads != 1 {
		t.Errorf("DISCOVERY_GRACE_HOURS 在 discovery.go 里被读了 %d 次（应为 1）——\n"+
			"窗口有两个实现点就可能漂移，而漂移会让证据探针与守卫对\n"+
			"「哪些行算近期成功」给出不同答案。", envReads)
	}

	// expireStaleModels 必须调用 staleExpiryGraceHours()。
	var fn *ast.FuncDecl
	for _, decl := range f.Decls {
		if d, ok := decl.(*ast.FuncDecl); ok && d.Name.Name == "expireStaleModels" {
			fn = d
			break
		}
	}
	if fn == nil {
		t.Fatal("discovery.go 里找不到 expireStaleModels")
	}
	if !callsName(fn.Body, "staleExpiryGraceHours") {
		t.Error("expireStaleModels 没有调用 staleExpiryGraceHours()——\n" +
			"它必须与证据探针共用同一个窗口实现点。")
	}
}

// callsName 报告表达式里是否有对 name 的调用。
//
// Init 与 Cond 两处都要看：护栏写成 `if may, why := f(); !may {` 时，调用在
// **Init** 里而 Cond 只有 `!may`。只扫 Cond 会漏掉真实存在的护栏——
// 这个坑本测试自己踩过一次。
//
// 刻意区分「调用」与「引用」：变异里出现过 `_ = staleExpiryMayRun` 这种
// 只引用不调用的写法，按名字匹配会误判为有护栏。
//
// ★ 2026-10-21 修一处量具缺陷：原来只认 `*ast.Ident`（裸函数名），
// 于是 `s.staleExpiryGraceHours()` 这种**方法调用**（SelectorExpr）
// 会被判成「没调用」⇒ 断言恒红。
// ⇒ 现在同时认 Ident 与 SelectorExpr 的 Sel 名字。
func callsName(node ast.Node, name string) bool {
	if node == nil {
		return false
	}
	found := false
	ast.Inspect(node, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fun := call.Fun.(type) {
		case *ast.Ident:
			if fun.Name == name {
				found = true
			}
		case *ast.SelectorExpr:
			if fun.Sel.Name == name {
				found = true
			}
		}
		return true
	})
	return found
}

// callsNameOnCallee 只看**被调用者**的名字（不看参数里的嵌套调用）。
//
// 与 callsName 的区别：后者会 Inspect 整棵子树，所以
// `f(g())` 里 callsName(x, "g") 也为 true。
// 而「这条语句的**被调用者**是不是 X」这个问题，只能看 Fun 本身。
//
// ★ 2026-10-21 补：加这条是因为实测变异 M1 之后，我在查「护栏调用的
// 实参」时误用了 callsName(call, ...) —— 它会 Inspect 到**自身**的 Fun，
// 于是「staleExpiryMayRun 的调用里是否含 staleExpiryMayRun 调用」恒为真。
func callsNameOnCallee(call *ast.CallExpr, name string) bool {
	if call == nil {
		return false
	}
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		return fun.Name == name
	case *ast.SelectorExpr:
		return fun.Sel.Name == name
	}
	return false
}

func hasReturn(stmts []ast.Stmt) bool {
	for _, s := range stmts {
		switch v := s.(type) {
		case *ast.ReturnStmt:
			return true
		case *ast.BlockStmt:
			if hasReturn(v.List) {
				return true
			}
		case *ast.IfStmt:
			if hasReturn(v.Body.List) {
				return true
			}
		}
	}
	return false
}

// stmtMentions 报告语句（含其子树）里是否出现过给定的 SQL 文本。
func stmtMentions(stmt ast.Stmt, needle string) bool {
	found := false
	ast.Inspect(stmt, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		if strings.Contains(lit.Value, needle) {
			found = true
		}
		return true
	})
	return found
}
