package bg

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// ★ 2026-10-20（迁移 890 + 并行审计轮 D0）：本文件的门控类判据**退役换题**。
//
// # 退役了什么
//
// 原先本文件守的是「36h lookback 扫描的**可比性门**」：
//
//   - 常量 `lookbackSkipS4StopWrite`（"s4_stop_write"）与纯函数
//     `lookbackComparability(logsWriteEnabled)`；
//   - 三道门：
//     ① `TestLookbackComparability`（判定函数的正反两腿）；
//     ② `TestLookbackSkipIsNotSeparableFromNoCandidates`（跳过 ≠ 没有候选）；
//     ③ `TestLookbackScanIsGatedBeforeQuerying`（结构钉子：门在查询之前、
//        门分支有 return、门结果经 markSkipped 记录、reset 在门之前）。
//
// # 为什么退役（两个前提同时消失，而门仍然闭合）
//
// ① **证据源换了**：890 把 v1 族整体 DROP，36h 回看的证据源随之迁到
//    **会话族**（hot ∪ parent，写入路径**无条件**、不读任何停写键）⇒
//    「证据源会因某个开关被关掉而冻结 ⇒ 问题不可判定」这个前提**不再成立**。
// ② **门的输入恒为假**：`lookbackComparability` 读的是
//    `settings.RequestLogsWriteEnabled()`，而该键的**代码默认**已被并行审计轮
//    改成 false（origin/main 的 D0）⇒ 在任何**未显式配置**该键的环境
//    （新装 / 克隆 / 灾备）上，这条扫描**永久停摆** ⇒
//    「不再具备判定能力」被读成「没有需要恢复的绑定」（静默洞）。
//
// ⇒ 门关着的理由已过期、后果却不可逆 ⇒ 门必须退役，而不是留着当摆设。
//
// # 现在由什么承担
//
// 新门 `TestLookbackScanIsNotGatedByRetiredV1Key` 守的是**退役后的不变式**：
// 这条扫描**不得**再被那个已退役的键门控 ⇒ 有人把门加回来时立刻变红。
//
// ⚠ 退役**不等于**「这条扫描现在开始跑」：本扫描有**自己独立**的启用开关
//   （`lookbackScanIntervalLocked()`，hotconfig / env；interval=0 是禁用哨兵）
//   ⇒ 是否真的扫描仍由那个开关决定 ⇒ 退役**不会**自行产生探测流量。
//
// # 「跳过 ≠ 没有候选」这条不变式去哪了
//
// 它没有消失，只是**搬走了**：跳过机制（`markSkipped` / `resetSkipped` /
// `SkippedChecks`）仍在使用，只是**使用者换成了**账务对账那条扫描
// （reason = `usage_arm_empty`）。那边的等价判据见
// `bg/ledger_reconciliation_s4_gate_test.go`。
//
// # 为什么保留 AST 辅助函数
//
// 下面的 findIfCallingCred / findCallCred / findCallCredExpr /
// callsWithinCred / hasReturnCred 是本文件自己的 AST 辅助（未导出、包内专用）。
// 新门仍然要用它们（判据要打在**去掉注释后**的源码上，而不是在源码里搜字符串），
// 所以它们随新门一起留在这里。

// TestLookbackScanIsNotGatedByRetiredV1Key（★ 退役换题后的新门）：
// 36h 回看扫描**不得**再被已退役的 v1 停写键门控。
//
// 守的不变量：scanLookbackRecoveries 的函数体里**不得**出现
//   - `settings.RequestLogsWriteEnabled` 的调用（那个键的 subject 已被 890
//     删除，且代码默认已被翻成 false），也**不得**出现
//   - `lookbackComparability`（已随门退役删除；若有人把它加回来，这条同时红）。
//
// ⚠ 判据必须**有牙**：检查器若根本认不出「读了这个键」，上面那条断言就是恒绿。
//
//	⇒ 正向对照：一个确实读了该键的合成片段必须被判成「读到了」。
func TestLookbackScanIsNotGatedByRetiredV1Key(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "credential_recovery.go", nil, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var scan *ast.FuncDecl
	for _, d := range file.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == "scanLookbackRecoveries" {
			scan = fd
			break
		}
	}
	if scan == nil {
		t.Fatal("scanLookbackRecoveries not found")
	}

	// ★ 正向对照：检查器必须看得见「读了这个键」，否则下面的否定断言恒绿。
	if !mentionsRequestLogsWriteEnabledInSource(t, "package p\nfunc f() { if settings.RequestLogsWriteEnabled() { return } }") {
		t.Fatal("判据没有牙：一个确实读了 settings.RequestLogsWriteEnabled 的片段被判成「没读」")
	}
	if mentionsRequestLogsWriteEnabledInSource(t, "package p\nfunc f() { if false { return } }") {
		t.Fatal("判据误伤：一个没读该键的片段被判成「读了」")
	}
	// ★ 第二条正对照：**注释里**提到这个键不算读点（退役记录就写在注释里）。
	if mentionsRequestLogsWriteEnabledInSource(t, "package p\n// 旧门读 settings.RequestLogsWriteEnabled()\nfunc f() {}") {
		t.Fatal("判据误伤：注释里提到该键被判成「读了」——判据必须打在去掉注释后的源码上")
	}

	if mentionsRequestLogsWriteEnabledExpr(scan) {
		t.Error("scanLookbackRecoveries 又读起了 settings.RequestLogsWriteEnabled —— " +
			"那道门的前两个前提都已消失（证据源随 890 迁到会话族、该键的代码默认被翻成 false），" +
			"门若还挂着，这条扫描会在任何未显式配置该键的环境上永久停摆，" +
			"把「不再具备判定能力」读成「没有需要恢复的绑定」")
	}
	if findIfCallingCred(t, scan, "lookbackComparability") != nil {
		t.Error("scanLookbackRecoveries 又调用了 lookbackComparability —— " +
			"那道门已随 890 + D0 退役（连同它的常量与纯函数一并删除）")
	}

	// 扫描**必须**仍然发查询 —— 否则「不门控」就变成了「不扫描」，
	// 而那比门控更糟（静默洞，且连 skip 痕迹都没有）。
	if findCallCred(t, scan, "Query") == nil {
		t.Error("scanLookbackRecoveries 不发查询了 —— 「退役这道门」不等于「不扫描」；" +
			"不发查询的扫描既没有候选，也没有 skip 痕迹 ⇒ 与「没有需要恢复的绑定」不可区分")
	}
}

// mentionsRequestLogsWriteEnabledInSource 报「这段 Go **源码**里读没读
// settings.RequestLogsWriteEnabled」（注释不算）。
//
// 解析成 AST 再判，而不是 strings.Contains：退役记录就写在注释里，
// 用文本匹配会把注释判成读点 ⇒ 恒红。
func mentionsRequestLogsWriteEnabledInSource(t *testing.T, src string) bool {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "snippet.go", src, 0)
	if err != nil {
		t.Fatalf("parse snippet: %v", err)
	}
	return mentionsRequestLogsWriteEnabledExpr(file)
}

// mentionsRequestLogsWriteEnabledExpr 报「这段 Go 代码里读没读
// settings.RequestLogsWriteEnabled」。
func mentionsRequestLogsWriteEnabledExpr(node ast.Node) bool {
	if node == nil {
		return false
	}
	found := false
	ast.Inspect(node, func(n ast.Node) bool {
		if found {
			return false
		}
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
		// 形如 settings.RequestLogsWriteEnabled()：包名必须是 settings。
		if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "settings" {
			found = true
		}
		return !found
	})
	return found
}

func findIfCallingCred(t *testing.T, fn *ast.FuncDecl, name string) *ast.IfStmt {
	t.Helper()
	var out *ast.IfStmt
	ast.Inspect(fn, func(n ast.Node) bool {
		if out != nil {
			return false
		}
		ifs, ok := n.(*ast.IfStmt)
		if !ok {
			return true
		}
		if findCallCredExpr(ifs.Init, name) != nil || findCallCredExpr(ifs.Cond, name) != nil {
			out = ifs
			return false
		}
		return true
	})
	return out
}

func findCallCred(t *testing.T, fn *ast.FuncDecl, name string) *ast.CallExpr {
	t.Helper()
	var out *ast.CallExpr
	ast.Inspect(fn, func(n ast.Node) bool {
		if out == nil {
			out = findCallCredExpr(n, name)
		}
		return out == nil
	})
	return out
}

func findCallCredExpr(n ast.Node, name string) *ast.CallExpr {
	// ast.Inspect panics on a nil node, and IfStmt.Init is nil whenever the
	// gate is written without an init clause.
	if n == nil {
		return nil
	}
	var out *ast.CallExpr
	ast.Inspect(n, func(x ast.Node) bool {
		if out != nil {
			return false
		}
		call, ok := x.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fn := call.Fun.(type) {
		case *ast.SelectorExpr:
			if fn.Sel.Name == name {
				out = call
			}
		case *ast.Ident:
			if fn.Name == name {
				out = call
			}
		}
		return out == nil
	})
	return out
}

func callsWithinCred(stmts []ast.Stmt, name string) bool {
	for _, s := range stmts {
		if findCallCredExpr(s, name) != nil {
			return true
		}
	}
	return false
}

func hasReturnCred(stmts []ast.Stmt) bool {
	for _, s := range stmts {
		found := false
		ast.Inspect(s, func(n ast.Node) bool {
			if _, ok := n.(*ast.ReturnStmt); ok {
				found = true
			}
			return !found
		})
		if found {
			return true
		}
	}
	return false
}
