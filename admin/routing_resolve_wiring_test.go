package admin

// 2026-10-02 批判式审计第二轮：给「resolve 污染治理」补**接线门**。
//
// 为什么需要这一层（两个已实证的漏检，均为变异验证得出，非推测）：
//
//  缺陷 A —— 门不覆盖接线。TestResolveCandidatesInvariant_Live 直接调用纯函数
//    filterResolveCandidatesByCid，走的是**门自己手写的 SQL**，从不经过
//    handleRoutingResolve。实测：把 handler 里的
//        candidates = filterResolveCandidatesByCid(candidates, expectedCid)
//    整行删掉（保留 resolveInputCanonicalID 的调用与 expectedCid 赋值，
//    以便编译通过），`go test ./admin/` **全包 76.5s 全绿**。
//    ⇒ 真库门度量的是「纯函数 + 门手写 SQL」这条自洽闭环，
//      **不是**「handler 会过滤」。整条治理被摘掉时门毫无反应。
//
//  缺陷 B —— 手写 SQL 会与真 SQL 漂移。门内复刻了 handler 的三条 WHERE 分支
//    （raw_model_name / standardized_name / canonical_name）。实测：把
//    **handler 里**的 `OR lower(mc.canonical_name) = ANY($1)` 删掉，
//    门依然全绿 —— 因为门跑的是它自己那份副本。
//    ⇒ 两份 SQL 文本相同这件事没有任何东西在守，
//      且「真 SQL 少一个匹配面」会静默降低 resolve 的召回而门察觉不到。
//
// 本文件的门用 AST 判定，判据钉在**节点身份**（哪个 CallExpr / 哪个
// AssignStmt），不钉「函数体内第一个 X」—— 后者会在被测代码重构后静默改判。
//
// 已知边界（写明本门**看不到**什么，避免误以为它替代了别的东西）：
//   * 它判「调用存在且形态正确」，不判运行时 SQL 的**结果**；结果仍由
//     TestResolveCandidatesInvariant_Live 负责，而那一条在 DSN 缺失时会 skip。
//   * 它看不见运行时 `strings.Join` / fmt 拼出来的 SQL 形状。
//   * 两道门互不替代：这道门证明接线存在，那道门证明数据干净。

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const routingSourceFile = "routing.go"

func parseRoutingSource(t *testing.T) (*token.FileSet, *ast.File) {
	t.Helper()
	fset := token.NewFileSet()
	path, err := filepath.Abs(routingSourceFile)
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", routingSourceFile, err)
	}
	return fset, f
}

// findFuncDecl returns the top-level FuncDecl with the given name. A missing
// function is a t.Fatal, NOT a skip and NOT a nil return: a gate that cannot
// find what it is gating has no verdict, and returning nil would let every
// downstream walk turn "the fix is gone" into a vacuous pass.
func findFuncDecl(t *testing.T, f *ast.File, name string) *ast.FuncDecl {
	t.Helper()
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if ok && fd.Name.Name == name {
			return fd
		}
	}
	t.Fatalf("%s: func %s not found — the gate cannot prove an invariant about "+
		"a function it cannot find; treat this as a FAIL, not a skip", routingSourceFile, name)
	return nil
}

// callName renders a CallExpr's callee as "pkg.Fn" or "Fn".
func callName(call *ast.CallExpr) string {
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		return fn.Name
	case *ast.SelectorExpr:
		if pkg, ok := fn.X.(*ast.Ident); ok {
			return pkg.Name + "." + fn.Sel.Name
		}
		return fn.Sel.Name
	}
	return ""
}

// TestHandleRoutingResolveWiresCanonicalFilter is the wiring gate for defect A.
//
// It asserts two independent facts about handleRoutingResolve:
//  1. resolveInputCanonicalID is still called (the lookup that makes filtering
//     possible cannot be silently dropped), and
//  2. `candidates` is reassigned from filterResolveCandidatesByCid somewhere in
//     the function body.
//
// Deliberately NOT "the filter call must sit inside the lookup's if-guard":
// that shape is behaviour-preserving either way —
// filterResolveCandidatesByCid is a no-op when expectedCid <= 0 — so pinning
// the position would be a false positive on a legal refactor. Measured: the
// position-pinned first draft turned red when the call was hoisted out of the
// guard, which changes nothing observable. A gate that cries wolf on correct
// code is worse than no gate, because the next real finding gets waved through.
//
// Deleting the reassignment — the exact mutation verified to leave the whole
// package green — turns this red.
func TestHandleRoutingResolveWiresCanonicalFilter(t *testing.T) {
	_, f := parseRoutingSource(t)
	fd := findFuncDecl(t, f, "handleRoutingResolve")

	var sawLookup, sawReassign bool

	ast.Inspect(fd.Body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.CallExpr:
			if callName(node) == "resolveInputCanonicalID" {
				sawLookup = true
			}
		case *ast.AssignStmt:
			for i, lhs := range node.Lhs {
				id, ok := lhs.(*ast.Ident)
				if !ok || id.Name != "candidates" || i >= len(node.Rhs) {
					continue
				}
				if call, ok := node.Rhs[i].(*ast.CallExpr); ok &&
					callName(call) == "filterResolveCandidatesByCid" {
					sawReassign = true
				}
			}
		}
		return true
	})

	if !sawLookup {
		t.Error("handleRoutingResolve no longer calls resolveInputCanonicalID — " +
			"the input-canonical lookup that makes filtering possible is gone")
	}
	if !sawReassign {
		t.Error("handleRoutingResolve never reassigns `candidates` from " +
			"filterResolveCandidatesByCid — the whole 2026-09-29 pollution fix is " +
			"inert while the live gate stays green. (The gate must stay green on a " +
			"hoist out of the lookup guard; only dropping the call is a defect.)")
	}
}

// TestHandleRoutingResolveSQLMatchBranchesMatchTheLiveGate is the drift gate for
// defect B.
//
// The live invariant gate hand-writes the same three WHERE branches. This
// asserts the production query in routing.go still carries all three, so the
// gate's copy cannot silently diverge from the query it claims to reproduce.
// Removing `OR lower(mc.canonical_name) = ANY($1)` from routing.go — verified to
// leave the invariant gate green — turns this red.
func TestHandleRoutingResolveSQLMatchBranchesMatchTheLiveGate(t *testing.T) {
	_, f := parseRoutingSource(t)

	// The three match arms the live gate replicates. Keyed on the SQL text,
	// not on a regex over the whole file, so an unrelated string literal
	// elsewhere in routing.go cannot satisfy (or break) this gate.
	wantArms := []struct {
		label string
		text  string
	}{
		{"raw_model_name", "lower(v.raw_model_name) = ANY($1)"},
		{"standardized_name", "lower(COALESCE(mo.standardized_name, v.raw_model_name)) = ANY($1)"},
		{"canonical_name", "lower(mc.canonical_name) = ANY($1)"},
	}

	// Collect SQL string literals only, and normalise whitespace so the
	// handler's tab-indented formatting cannot make this gate flaky.
	var sqlLits []string
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		s, err := strconv.Unquote(lit.Value)
		if err != nil || !strings.Contains(s, "ANY($1)") {
			return true
		}
		sqlLits = append(sqlLits, strings.Join(strings.Fields(s), " "))
		return true
	})
	if len(sqlLits) == 0 {
		t.Fatal("no SQL literal containing ANY($1) found in routing.go — " +
			"the query was restructured; re-derive the live gate's copy")
	}

	var missing []string
	for _, arm := range wantArms {
		found := false
		for _, lit := range sqlLits {
			if strings.Contains(lit, arm.text) {
				found = true
				break
			}
		}
		if !found {
			missing = append(missing, arm.label+" → "+arm.text)
		}
	}
	if len(missing) > 0 {
		t.Errorf("resolve query lost match arm(s) that TestResolveCandidatesInvariant_Live "+
			"still assumes:\n  %s\n"+
			"The live gate replicates these branches by hand; if production drops one, "+
			"the gate keeps measuring its own stale copy and stays green.",
			strings.Join(missing, "\n  "))
	}
}
