package ursm_test

// stale-expiry 判据**换源**的守卫（2026-10-21，R72g 轮新增）
//
// # 背景：一道**判据已失效**的护栏
//
// discovery 的 stale 下架有一条宽限守卫，它是**否定式**的：
//
//	AND NOT EXISTS (SELECT 1 FROM <证据源> WHERE ... success = TRUE AND ts > 近 N 小时)
//
// 否定式守卫的危险在于：**当证据源停止写入时**，这个子查询不是「查不到」，
// 而是**结构性恒空** ⇒ 守卫从「已验证：近期无成功」退化成「根本没有证据」，
// 而代码会照常执行下架 ⇒ 无论模型是否真在用，都被判 auto_discovery_expired。
// 这不是降级，是**主动禁用仍在工作的凭据模型**，且写成功就是成功，
// 不会有任何异常信号。
//
// 2026-10-02 为此加了一道护栏，判据是
// `settings.RequestLogsWriteEnabled()`（问「v1 能否写」）。
//
// ★ 2026-10-21（迁移 890 真删 v1 族）：v1 已退役 ⇒ **该判据恒为 false**
// ⇒ 护栏恒 skip ⇒ **陈旧模型永不再自动下架**。
// 211 生产日志实证：
//
//	discovery: skip stale-model expiry reason="S4 停写中：宽限守卫依赖 v1 证据源…"
//
// ⇒ 属主裁决：**退役该护栏**（它已无判据，继续留着只会静默跳过）。
//
// # 这道门钉的是什么
//
// 「退役」的**正确形态是换源，不是删网**：
//
//	① 判据必须改成**直接问会话族证据源**（hot ∪ parent 近期有无成功行）——
//	   问对了问题，护栏才重新有判据；
//	② **否定式守卫必须保留** —— 会话族也可能停止写入（例：R62 修过的
//	   23514「no partition of relation session_turns found for row」，
//	   新装库缺当月分区 ⇒ 写路径直接失败）⇒ 那一刻守卫同样会结构性恒空
//	   ⇒ 在用模型会被全量下架 ⇒ 删掉判断就是把安全网一起删了。
//
// ★ 本门与 `discovery/stale_expiry_gate_test.go` 是**两个独立视角**：
// 那道门在包内（可白盒读包内函数），本门在仓外（只认**仓内源码文本**，
// 跨越包边界也能看见「判据问的是谁」）。
// 两道门测的不是同一件事：一个测行为契约，一个测**跨包的源码事实**。
//
// ⚠ 本门**只做静态判定**（读源码 + AST），不连库 ⇒ 不产生任何生产写入。

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const discoveryGoRel = "discovery/discovery.go"

func repoRootFromUrsm(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	// scripts/ursmcheck → 仓库根
	root := filepath.Dir(filepath.Dir(wd))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("找不到仓库根（%s 无 go.mod）：%v", root, err)
	}
	return root
}

func parseDiscovery(t *testing.T) *ast.File {
	t.Helper()
	path := filepath.Join(repoRootFromUrsm(t), discoveryGoRel)
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", discoveryGoRel, err)
	}
	return f
}

// TestStaleExpiryGuardStillExists 钉住「否定式守卫没有消失」。
//
// 退役的是**判据**，不是守卫本身。删掉守卫 = 在证据源停写时把在用模型
// 全量下架（无任何异常信号）。
func TestStaleExpiryGuardStillExists(t *testing.T) {
	f := parseDiscovery(t)

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

	found := false
	ast.Inspect(fn, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		ident, ok := call.Fun.(*ast.Ident)
		if ok && ident.Name == "staleExpiryMayRun" {
			found = true
		}
		return true
	})
	if !found {
		t.Fatal("expireStaleModels 不再调用 staleExpiryMayRun——\n" +
			"否定式守卫已被删除。退役的正确形态是**换判据**（问会话族证据源），\n" +
			"不是删掉判断：会话族一旦停止写入（例如 23514 写路径失败），\n" +
			"NOT EXISTS 守卫会结构性恒空 ⇒ 在用模型被全量下架且无异常信号。")
	}
}

// TestStaleExpiryGuardReturnsOnBlock 钉住「算出来就真的会阻断」。
//
// 2026-10-02 已实测过这个坑：保留调用与日志、只删 return ⇒ 门全绿而护栏
// 形同虚设。⇒ 必须钉住 return 存在。
func TestStaleExpiryGuardReturnsOnBlock(t *testing.T) {
	f := parseDiscovery(t)

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

	for _, stmt := range fn.Body.List {
		ifStmt, ok := stmt.(*ast.IfStmt)
		if !ok {
			continue
		}
		if !ifCalls(ifStmt, "staleExpiryMayRun") {
			continue
		}
		if !hasReturnStmt(ifStmt.Body.List) {
			t.Fatal("护栏分支里没有 return——计算了阻断理由却不阻断。\n" +
				"必须 return，否则护栏形同虚设而门全绿。")
		}
		return
	}
	t.Fatal("expireStaleModels 里找不到以 staleExpiryMayRun 为条件的 if 分支")
}

// TestStaleExpiryCriterionIsSessionFamilyEvidence 钉住本轮退役的**核心**：
// 判据问的是**会话族证据源**，不是 v1，也不是别的开关。
//
// ⚠ 用 AST 判定**调用**（不是文本匹配）：注释里出现
// `settings.RequestLogsWriteEnabled()` 是正常的（那是在解释旧判据错在哪），
// 而代码里再出现才是回归。
func TestStaleExpiryCriterionIsSessionFamilyEvidence(t *testing.T) {
	f := parseDiscovery(t)

	// ① 代码里不得再调用 v1 开关。
	for _, decl := range f.Decls {
		ast.Inspect(decl, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if sel.Sel.Name == "RequestLogsWriteEnabled" {
				t.Errorf("discovery.go 仍在调用 settings.RequestLogsWriteEnabled()——\n"+
					"v1 已随 890 退役，该判据恒为 false ⇒ 护栏恒 skip ⇒\n"+
					"陈旧模型永不再自动下架（211 生产日志实证）。",)
			}
			return true
		})
	}

	// ② 护栏调用的实参必须是「会话族证据探针」的调用。
	var arg ast.Node
	found := false
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "expireStaleModels" {
			continue
		}
		ast.Inspect(fn, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			ident, ok := call.Fun.(*ast.Ident)
			if !ok || ident.Name != "staleExpiryMayRun" || len(call.Args) != 1 {
				return true
			}
			arg = call.Args[0]
			found = true
			return false
		})
	}
	if !found {
		t.Fatal("expireStaleModels 里找不到 staleExpiryMayRun 的调用")
	}
	switch v := arg.(type) {
	case *ast.CallExpr:
		sel, ok := v.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "sessionFamilyHasRecentSuccess" {
			t.Errorf("护栏判据的实参不是会话族证据探针的调用（得到 %T）——\n"+
				"判据必须是**证据源实测结果**。传常量（恒 false = 恒 skip，\n"+
				"恒 true = 无保护）都不是「问证据源在不在」。", arg)
		}
	default:
		t.Errorf("护栏判据的实参是 %T（期望是 sessionFamilyHasRecentSuccess 的调用）", arg)
	}
}

// TestSessionFamilyEvidenceProbeQueriesSessionFamily 钉住探针**真的查会话族**。
//
// 只断言「不再问 v1」是不够的：什么都不查也能满足它。
func TestSessionFamilyEvidenceProbeQueriesSessionFamily(t *testing.T) {
	f := parseDiscovery(t)

	var fn *ast.FuncDecl
	for _, decl := range f.Decls {
		if d, ok := decl.(*ast.FuncDecl); ok && d.Name.Name == "sessionFamilyHasRecentSuccess" {
			fn = d
			break
		}
	}
	if fn == nil {
		t.Fatal("discovery.go 里找不到 sessionFamilyHasRecentSuccess——\n" +
			"新判据必须有实测实现，不能只把旧判据删掉了事。")
	}

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
			t.Errorf("证据探针的 SQL 里没有 %q——它必须直接查会话族（hot ∪ parent）的成功行", needle)
		}
	}
	// 两条臂都要在：只看 hot 会在 hot 轮转后误判「无证据」⇒ 恒 skip。
	//
	// ⚠ 判定用**标识符边界**而不是「`public.session_turns` 后面跟一个空格」：
	// 真实 SQL 里 `FROM public.session_turns` 后面是**换行**（WHERE 另起一行），
	// 写死空格会漏判（实测：这正是本门第一版的量具缺陷 ⇒ 恒红）。
	if !hasIdentifier(joined, "public.session_turns_hot") {
		t.Error("证据探针缺少 public.session_turns_hot 这一臂")
	}
	if !hasIdentifier(joined, "public.session_turns") {
		t.Error("证据探针缺少 public.session_turns（parent 臂）这一臂——\n" +
			"hot ∪ parent 两条臂都必须查：只看 hot 会在 hot 轮转/清空后\n" +
			"把「无证据」误判出来 ⇒ 护栏恒 skip。")
	}
}

// hasIdentifier 报告文本里是否出现了**作为完整标识符**的 name。
//
// 判定用「前后不是标识符字符」，而不是朴素子串匹配 ——
// `public.session_turns` 是 `public.session_turns_hot` 的**前缀**，
// 朴素匹配会把它算成出现了（假阳性）。
func hasIdentifier(text, name string) bool {
	for i := 0; i+len(name) <= len(text); i++ {
		if text[i:i+len(name)] != name {
			continue
		}
		if i > 0 && isIdentByteForGate(text[i-1]) {
			continue
		}
		if i+len(name) < len(text) && isIdentByteForGate(text[i+len(name)]) {
			continue
		}
		return true
	}
	return false
}

func isIdentByteForGate(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '.'
}

// TestStaleExpiryGraceWindowHasSingleImplementation 钉住窗口**只有一个实现点**。
//
// 探针窗口与守卫窗口若漂移，会出现：探针说「有证据」（48h 窗口里看到了行），
// 而守卫自己在 24h 窗口里看不到任何行 ⇒ 否定式守卫退化成「没有证据」。
//
// ⚠ 判定必须**剥注释**（用 AST 只看字符串字面量与 env 调用）：
// 文本计数会把注释里的 "DISCOVERY_GRACE_HOURS" 也算进去
// （实测：本门第一版就是这么写的 ⇒ 恒红，因为注释里本来就该提到它）。
func TestStaleExpiryGraceWindowHasSingleImplementation(t *testing.T) {
	f := parseDiscovery(t)

	envReads := 0
	for _, decl := range f.Decls {
		ast.Inspect(decl, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Getenv" {
				return true
			}
			if len(call.Args) != 1 {
				return true
			}
			lit, ok := call.Args[0].(*ast.BasicLit)
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
		t.Errorf("DISCOVERY_GRACE_HOURS 的 os.Getenv 调用在 %s 里出现 %d 次（应为 1）——\n"+
			"窗口有两个实现点就可能漂移，而漂移会让证据探针与守卫对\n"+
			"「哪些行算近期成功」给出不同答案。", discoveryGoRel, envReads)
	}
}

// ── AST 工具 ────────────────────────────────────────────────────────────

func ifCalls(ifStmt *ast.IfStmt, name string) bool {
	found := false
	check := func(node ast.Node) {
		if node == nil {
			return
		}
		ast.Inspect(node, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			ident, ok := call.Fun.(*ast.Ident)
			if ok && ident.Name == name {
				found = true
			}
			return true
		})
	}
	check(ifStmt.Init)
	check(ifStmt.Cond)
	return found
}

func hasReturnStmt(stmts []ast.Stmt) bool {
	for _, s := range stmts {
		switch v := s.(type) {
		case *ast.ReturnStmt:
			return true
		case *ast.BlockStmt:
			if hasReturnStmt(v.List) {
				return true
			}
		case *ast.IfStmt:
			if hasReturnStmt(v.Body.List) {
				return true
			}
		}
	}
	return false
}
