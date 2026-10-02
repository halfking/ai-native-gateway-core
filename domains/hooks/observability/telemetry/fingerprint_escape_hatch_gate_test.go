package telemetry

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// 审计 §9.50.4：把「S4 停写之后指纹漂移检测器的逃生口**也是关着的**」
// 从散文变成一条可执行的断言。
//
// # 这条事实为什么重要
//
// `bg/integrity_fingerprint_drift.go` 的短路有两条腿：
//
//	腿 1（探针）：`probeFingerprintTraffic` 读 request_logs_hot / request_logs
//	              的 `system_fingerprint IS NOT NULL` ⇒ 停写后恒空
//	腿 2（进程内 arm）：`fingerprintScanDecision` 的 `inProcSeen` 分支，
//	              来自 `telemetry.SystemFingerprintObservedSince()`
//
// 我在 §9.50 第一版告警文案里把腿 2 写成「这条链**不读 v1**，所以恢复与否取决于
// 当前进程有没有真的处理过带指纹的请求」——**这是错的**，而且是本轮最贵的一个错：
// 它把「停写 + 重启 ⇒ 检测器永久关闭」写成了「有时会自愈」。据那条错误前提还发了
// 规则。订正后的事实更强，也更坏：
//
//	`markSystemFingerprintObserved()` 的**唯一**调用点在
//	`persistSystemFingerprint()` 内；而 `persistSystemFingerprint()` 的两个调用点
//	（client.go:1816 / :2468）**都在 `if logsWrite {` 块内**，
//	`logsWrite` 就是 `requestLogsWriteEnabled()`（= S4 停写键）。
//
// ⇒ 两条腿断在**同一个开关**上。停写之后它一次都不执行，重启即永久关闭。
//
// # 为什么用结构断言而不是注释
//
// 这不是「写法偏好」。若有人把 `persistSystemFingerprint` 挪到门控之外（这正是
// §9.50 给出的正确修法之一），他必须同时改掉
// `deploy/prometheus/rules/integrity-fingerprint-drift.yml` 里那段「逃生口也是
// 关着的」文案，否则运维会读到一条已经失效的说明。这道门就是那个强制点。
//
// 判据只断言**能被证明的那一侧**：AST 层面的词法包含关系 + `logsWrite` 的来源。
// 运行期由同一个变量控制，所以两者合起来构成完整论证。无法证明的（探针的运行期
// 取值）由 bg 侧的告警覆盖，不在这里假装。

const fingerprintObservedMarker = "markSystemFingerprintObserved"
const fingerprintPersistMarker = "persistSystemFingerprint"

type fingerprintCallSite struct {
	file   string
	line   int
	fn     string
	inGate bool
}

// telemetrySourceFiles 列出本包**所有**非测试源文件。
//
// 刻意不是只解析 client.go：逃生口随时可能被挪到同包的另一个文件里去「修好」，
// 而只盯一个文件的话，那次挪动会让这道门失去意义（它仍然绿，且什么都没守住）。
func telemetrySourceFiles(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(".")
	require.NoError(t, err)
	var out []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		out = append(out, name)
	}
	require.NotEmpty(t, out, "no telemetry source files found")
	return out
}

// findFingerprintCalls 收集本包里对 target 的所有调用点，并记录每处是否
// 词法上位于 `if logsWrite { … }` 体内。
func findFingerprintCalls(t *testing.T, target string) []fingerprintCallSite {
	t.Helper()
	var sites []fingerprintCallSite

	for _, name := range telemetrySourceFiles(t) {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, name, nil, 0)
		require.NoError(t, err, "parse %s", name)

		var walkFunc func(fn *ast.FuncDecl)
		walkFunc = func(fn *ast.FuncDecl) {
			var walkStmts func(stmts []ast.Stmt, inGate bool)
			walkStmts = func(stmts []ast.Stmt, inGate bool) {
				for _, stmt := range stmts {
					if isCallTo(stmt, target) {
						sites = append(sites, fingerprintCallSite{
							file:   name,
							line:   fset.Position(stmt.Pos()).Line,
							fn:     fn.Name.Name,
							inGate: inGate,
						})
					}
					walkStatement(stmt, inGate, walkStmts)
				}
			}
			walkStmts(fn.Body.List, false)
		}
		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Body != nil {
				walkFunc(fn)
			}
		}
	}
	return sites
}

func walkStatement(stmt ast.Stmt, inGate bool, walkStmts func([]ast.Stmt, bool)) {
	isGate := func(cond ast.Expr) bool {
		id, ok := cond.(*ast.Ident)
		return ok && id.Name == "logsWrite"
	}
	switch s := stmt.(type) {
	case *ast.BlockStmt:
		walkStmts(s.List, inGate)
	case *ast.IfStmt:
		if s.Init != nil {
			walkStatement(s.Init, inGate, walkStmts)
		}
		if s.Else != nil {
			walkStatement(s.Else, inGate, walkStmts)
		}
		walkStmts(s.Body.List, inGate || isGate(s.Cond))
	case *ast.ForStmt:
		walkStmts(s.Body.List, inGate)
	case *ast.RangeStmt:
		walkStmts(s.Body.List, inGate)
	case *ast.SwitchStmt:
		for _, c := range s.Body.List {
			walkStatement(c, inGate, walkStmts)
		}
	case *ast.TypeSwitchStmt:
		for _, c := range s.Body.List {
			walkStatement(c, inGate, walkStmts)
		}
	case *ast.SelectStmt:
		for _, c := range s.Body.List {
			walkStatement(c, inGate, walkStmts)
		}
	case *ast.CaseClause:
		walkStmts(s.Body, inGate)
	case *ast.CommClause:
		walkStmts(s.Body, inGate)
	}
}

// isCallTo 浅匹配：只看这条语句本身，不进嵌套块（理由同 bg 侧那道门）。
func isCallTo(stmt ast.Stmt, target string) bool {
	found := false
	ast.Inspect(stmt, func(n ast.Node) bool {
		if _, ok := n.(*ast.BlockStmt); ok {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if id, ok := call.Fun.(*ast.Ident); ok && id.Name == target {
			found = true
		}
		return true
	})
	return found
}

func TestFingerprintArmIsNotGatedByStopWrite(t *testing.T) {
	// §9.51 这道门被**反转过一次**。第一版断言的是「arm 在 `if logsWrite`
	// 内」——那正是缺陷本身。修好之后同一段判据会红，于是必须把它换成
	// **可持久的不变式**：
	//
	//	进程内指纹 arm 的整条调用链上，**没有任何一环**在 `if logsWrite {}` 体内。
	//
	// 这才是「修好了」的定义。钉死具体行号或具体函数名会在下一次重构时变成噪音，
	// 而这条不变式在重构后依然可判定。
	//
	// 断言顺序**不是随意的**：环 3 排在最前，因为它是「别把修复撤销掉」的守卫。
	// 撤销修复会同时触发环 1/环 2，若它们在前，环 3 就永远轮不到——而一条
	// 排在更严格断言之后、自己不可达的断言就是装饰（本门第一版正是如此）。

	// ── 环 3：落列那个函数的函数体里不得再 arm ──────────────────────────────
	//
	// 第一版这里写的是「persistSystemFingerprint 的调用点不能自我递归」。
	// 它读起来像是这条，但它量的是完全不同的东西，而且几乎恒真。已订正。
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "client.go", nil, 0)
	require.NoError(t, err)
	var persist *ast.FuncDecl
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == fingerprintPersistMarker {
			persist = fn
		}
	}
	require.NotNil(t, persist, "client.go no longer defines %s()", fingerprintPersistMarker)

	// 用深匹配而不是 isCallTo：后者为「调用归属哪个基本块」设计，是浅匹配，
	// 用在这里会恒假（详见 blockCallsDeep 的注释）。
	for _, arm := range []string{"observeSystemFingerprint", fingerprintObservedMarker} {
		require.False(t, blockCallsDeep(persist.Body, arm),
			"%s() calls %s() again — that is exactly the coupling audit §9.51 removed. "+
				"%s is only ever called from `if logsWrite {}`, so arming there makes the "+
				"in-process re-arm die at S4 stop-write again.",
			fingerprintPersistMarker, arm, fingerprintPersistMarker)
	}

	// ── 环 1：arm 的唯一底层写入方，且它自己不在门内 ─────────────────────────
	observed := findFingerprintCalls(t, fingerprintObservedMarker)
	require.NotEmpty(t, observed,
		"%s() has no call site — the in-process re-arm would be permanently dead code",
		fingerprintObservedMarker)
	for _, o := range observed {
		require.False(t, o.inGate,
			"%s() at %s:%d is inside `if logsWrite {}`. The in-process re-arm records that "+
				"THIS PROCESS saw fingerprint traffic; it has nothing to do with whether that "+
				"traffic was written to the wide table. §9.51 moved it out for exactly this reason.",
			fingerprintObservedMarker, o.file, o.line)
		require.Equal(t, "observeSystemFingerprint", o.fn,
			"%s() must be reached only through observeSystemFingerprint() — that wrapper "+
				"holds the nil/empty guard, and a bare call site would arm on an empty value",
			fingerprintObservedMarker)
	}

	// ── 环 2：arm 的入口必须在两个写路径里，且都在门控之外 ───────────────────
	arms := findFingerprintCalls(t, "observeSystemFingerprint")
	require.Len(t, arms, 2,
		"the arm must be wired into both insertRequestLog and updateRequestLog; got %d "+
			"call sites — a request that only ever reaches one of them would never re-arm "+
			"the detector on that path", len(arms))
	for _, a := range arms {
		require.False(t, a.inGate,
			"observeSystemFingerprint() at %s:%d is inside `if logsWrite {}` — that is the "+
				"exact defect audit §9.51 fixed; do not move it back", a.file, a.line)
		require.Contains(t, []string{"insertRequestLog", "updateRequestLog"}, a.fn,
			"observeSystemFingerprint() at %s:%d is called from %s; re-derive the stop-write "+
				"story for a new call site", a.file, a.line, a.fn)
	}
}

// TestLogsWriteSnapshotReadsTheStopWriteKey 让上一道门的论证闭合。
//
// `inGate` 只有在 `logsWrite` 确实**就是 S4 停写键**时才有意义。这条链有三跳，
// 每一跳都要钉住，否则中间任一环被换掉、而门仍然绿：
//
//	logsWrite := requestLogsWriteEnabled()                  （client.go，两处）
//	  └─ requestLogsWriteEnabled() { return settings.RequestLogsWriteEnabled() }
//	       └─ settings.RequestLogsWriteEnabled() { return GetPlatformBool(KeyRequestLogsWriteEnabled, true) }
//	            └─ KeyRequestLogsWriteEnabled = "storage.request_logs_write_enabled"
func TestLogsWriteSnapshotReadsTheStopWriteKey(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "client.go", nil, 0)
	require.NoError(t, err)

	funcs := map[string]*ast.FuncDecl{}
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Body != nil {
			funcs[fn.Name.Name] = fn
		}
	}

	// 跳 1：两个写路径各取一次快照。
	assignedIn := map[string]int{}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			assign, ok := n.(*ast.AssignStmt)
			if !ok {
				return true
			}
			for i, lhs := range assign.Lhs {
				id, ok := lhs.(*ast.Ident)
				if !ok || id.Name != "logsWrite" || i >= len(assign.Rhs) {
					continue
				}
				if call, ok := assign.Rhs[i].(*ast.CallExpr); ok {
					if rid, ok := call.Fun.(*ast.Ident); ok && rid.Name == "requestLogsWriteEnabled" {
						assignedIn[fn.Name.Name]++
					}
				}
			}
			return true
		})
	}
	for _, fn := range []string{"insertRequestLog", "updateRequestLog"} {
		require.Equal(t, 1, assignedIn[fn],
			"%s must take exactly one `logsWrite := requestLogsWriteEnabled()` snapshot", fn)
	}
	require.Len(t, assignedIn, 2,
		"only insertRequestLog and updateRequestLog may snapshot logsWrite; got %d snapshots "+
			"in %v — a third gate site is a place this argument has not been checked",
		len(assignedIn), assignedIn)

	// 跳 2：本地 wrapper 必须**原样**转发到 settings 包。
	//
	// 第一版只数了 `settings.RequestLogsWriteEnabled` 的引用次数（== 1），
	// 变异 M5（把 wrapper 改成 `if <key> { return true }; return true`）**通过了**
	// ——引用还是一次，值却已经被偷换。⇒ 这里断言的是**函数体形状**：
	// 恰好一条 `return settings.RequestLogsWriteEnabled()`，没有第二条语句。
	wrapper := funcs["requestLogsWriteEnabled"]
	require.NotNil(t, wrapper, "client.go no longer defines requestLogsWriteEnabled()")
	require.Len(t, wrapper.Body.List, 1,
		"requestLogsWriteEnabled() must be a pure pass-through: exactly one statement. "+
			"Any extra statement can change its value while still referencing the S4 key once, "+
			"which would silently invalidate every `if logsWrite {}` gate in this package "+
			"(mutation M5 in audit §9.50.5 slipped through the earlier reference-count check)")

	ret, ok := wrapper.Body.List[0].(*ast.ReturnStmt)
	require.True(t, ok, "requestLogsWriteEnabled() must be `return settings.RequestLogsWriteEnabled()`")
	require.Len(t, ret.Results, 1, "requestLogsWriteEnabled() must return exactly one value")

	call, ok := ret.Results[0].(*ast.CallExpr)
	require.True(t, ok, "requestLogsWriteEnabled() must return the settings call directly, "+
		"not a local variable or an expression around it")
	sel, ok := call.Fun.(*ast.SelectorExpr)
	require.True(t, ok, "unexpected callee shape in requestLogsWriteEnabled()")
	pkg, ok := sel.X.(*ast.Ident)
	require.True(t, ok, "unexpected callee shape in requestLogsWriteEnabled()")
	require.Equal(t, "settings", pkg.Name, "the wrapper must forward to the settings package")
	require.Equal(t, "RequestLogsWriteEnabled", sel.Sel.Name,
		"the wrapper must forward to settings.RequestLogsWriteEnabled()")

	// 跳 3：键名本身就是 S4 停写键。直接读常量声明的**值**，不在注释里搜字符串
	//（注释会被重写，值不会）。
	sf, err := parser.ParseFile(token.NewFileSet(), filepath.Join("..", "..", "..", "..", "settings",
		"key_request_logs_write_enabled.go"), nil, 0)
	require.NoError(t, err)

	const keyName = "KeyRequestLogsWriteEnabled"
	var key string
	for _, decl := range sf.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok || len(vs.Names) != 1 || vs.Names[0].Name != keyName || len(vs.Values) != 1 {
				continue
			}
			if lit, ok := vs.Values[0].(*ast.BasicLit); ok {
				key, _ = strconv.Unquote(lit.Value)
			}
		}
	}
	require.Equal(t, "storage.request_logs_write_enabled", key,
		"settings.%s must remain the S4 stop-write key; if it was renamed, the alert text in "+
			"deploy/prometheus/rules/integrity-fingerprint-drift.yml and §9.50 must be updated "+
			"with it (got %q)", keyName, key)
}

// blockCallsDeep 判断一个函数体里（**任意深度**）是否出现目标调用。
//
// 与 isCallTo 的区别是本门最容易踩的坑，代价是三轮：
// isCallTo 是**浅**匹配，遇到 *ast.BlockStmt 就停止下探——那是为「把调用
// 归属到正确的基本块」设计的（findFingerprintCalls 靠它定位）。但「这个
// 函数到底调没调 X」是另一个问题，答案需要**穿透**所有嵌套块。
//
// 把 isCallTo 用在函数体上会得到恒假的结论：把 Body 本身传进去，它在根
// 节点就返回 false；改传 Body.List 的元素，调用在 `if err == nil { … }`
// 里面，同样返回 false。⇒ 写出一条看起来在守、实际永远为真的装饰断言。
//
// **门里凡是「某函数是否调用了 X」都要走这里**；凡是「调用归属于哪个基本块」
// 才走 isCallTo。两种问题不要共用一个匹配器。
func blockCallsDeep(body *ast.BlockStmt, target string) bool {
	if body == nil {
		return false
	}
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if id, ok := call.Fun.(*ast.Ident); ok && id.Name == target {
			found = true
		}
		return true
	})
	return found
}
