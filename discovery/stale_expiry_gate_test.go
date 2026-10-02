// stale_expiry_gate_test.go — 2026-10-02。
//
// 钉住 S4 停写护栏：`expireStaleModels` 的宽限守卫是**否定式**的
// （`NOT EXISTS (request_logs 近 N 小时成功)`）。停写后该子查询结构性恒空，
// 守卫从「已验证近期无成功」退化成「没有证据」，而代码会照常下架 ——
// 主动禁用仍在工作的凭据模型，且无异常信号。
//
// 缺陷所在分支（证据源停止）在线上要等停写之后才出现，集成测试永远撞不到，
// 所以判定抽成纯函数 `staleExpiryMayRun` 无库可测；再用 AST 钉住
// `expireStaleModels` 真的调用了它——否则删掉调用、留下纯函数，门照样绿。
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
		name       string
		writable   bool
		wantMayRun bool
	}{
		{"证据源在写 → 允许（保持原行为）", true, true},
		{"证据源停写 → 禁止（护栏生效）", false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mayRun, reason := staleExpiryMayRun(tc.writable)
			if mayRun != tc.wantMayRun {
				t.Fatalf("staleExpiryMayRun(%v) = %v, want %v", tc.writable, mayRun, tc.wantMayRun)
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
func TestStaleExpiryMayRunReasonIsDistinctFromSilence(t *testing.T) {
	_, reason := staleExpiryMayRun(false)
	if !strings.Contains(reason, "request_logs") {
		t.Errorf("阻断原因必须点名证据源（request_logs），否则读日志的人无法定位；得到 %q", reason)
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
		if updateIdx < 0 && stmtMentions(stmt, "UPDATE model_offers") {
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
			"纯函数与它的表测试会照样全绿，而线上会在 S4 停写后继续把\n" +
			"仍在工作的凭据模型判为 auto_discovery_expired 并下架。")
	}
	if updateIdx < 0 {
		t.Fatal("expireStaleModels 里找不到 UPDATE model_offers——本测试的前提变了，请重新核对")
	}
	// 断言 C：护栏必须在下架写入**之前**。
	if guardIdx > updateIdx {
		t.Errorf("护栏在第 %d 条语句，UPDATE model_offers 在第 %d 条——护栏在下架之后才执行，等于没有",
			guardIdx, updateIdx)
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
		if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == name {
			found = true
		}
		return true
	})
	return found
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
