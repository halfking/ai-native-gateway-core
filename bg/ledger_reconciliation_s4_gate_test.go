package bg

import (
	"go/ast"
	"go/parser"
	"go/token"

	"strings"
	"testing"
)

// S4 停写门控范围（2026-10-02 审计）：usage_credit_mismatch 跨了门的影响半径。
//
// 结构事实（真库 + 源码双向核实）：
//
//	settings.KeyRequestLogsWriteEnabled 的声明范围 =
//	    request_logs 宽族（request_logs_hot 主行 + request_logs_bodies_hot 正文）
//	usageCreditSQL() 的两侧 =
//	    request_logs_hot.credits_charged   ← 族内，门管得到
//	    credit_ledger_hot（consume/ref_type='request'）  ← 族外，永不停写
//
// 而对账器此前**完全不咨询该门**。所以停写一旦生效，两侧就不再描述同一段时间：
// usage 臂在切换点冻结、credit 臂继续增长，于是切换点之后的每个请求都落进
// FULL OUTER JOIN 的「只有 credit」分支（charged=0 / debited>0），被当作差异写进
// maas_reconciliation_findings。**那些不是账务缺陷，就是停写本身**，而且数量无上界。
//
// 同一文件里的 balance_chain 不受影响：它整段只读 credit_ledger_hot（自洽回放），
// 不跨族。所以这道门只该挡 usage_credit_mismatch 一项。

func TestUsageCreditComparability(t *testing.T) {
	cases := []struct {
		name       string
		logsWrite  bool
		wantOK     bool
		wantReason string
	}{
		{"gate open — comparable", true, true, ""},
		{"gate closed — not comparable, and the reason is machine-readable", false, false, usageCreditSkipS4StopWrite},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ok, reason := usageCreditComparability(tc.logsWrite)
			if ok != tc.wantOK {
				t.Fatalf("comparable = %v, want %v", ok, tc.wantOK)
			}
			if reason != tc.wantReason {
				t.Fatalf("reason = %q, want %q", reason, tc.wantReason)
			}
			// The one thing this must never do is return a non-empty verdict
			// while claiming comparability, or an empty reason while refusing.
			// A blank reason is how "skipped" silently degrades into "no findings".
			if !ok && strings.TrimSpace(reason) == "" {
				t.Errorf("refused to compare but gave no machine-readable reason — " +
					"skip and 'no differences' must stay distinguishable")
			}
			if ok && reason != "" {
				t.Errorf("compared fine but still reported reason %q", reason)
			}
		})
	}
}

// TestUsageCreditSkipIsNotSilent pins the "swept 0" vs "swept nothing" separation
// at the struct level. A skipped check returns 0, exactly like a clean scan, so
// the only thing keeping the two apart is the per-run skip list — and a skip list
// that is never reset is worse than none, because it would keep reporting
// "skipped" for a run that really did execute and really did find nothing.
func TestUsageCreditSkipIsNotSilent(t *testing.T) {
	r := &LedgerReconciler{}
	if got := r.SkippedChecks(); len(got) != 0 {
		t.Fatalf("fresh reconciler already reports skips: %v", got)
	}
	r.markSkipped(usageCreditSkipS4StopWrite)
	if got := r.SkippedChecks(); len(got) != 1 || got[0] != usageCreditSkipS4StopWrite {
		t.Fatalf("skip not recorded: %v", got)
	}
	r.resetSkipped()
	if got := r.SkippedChecks(); len(got) != 0 {
		t.Fatalf("resetSkipped did not clear the list: %v", got)
	}
	// A nil reconciler must not panic; it is constructed in several places.
	var nilRec *LedgerReconciler
	if got := nilRec.SkippedChecks(); got != nil {
		t.Fatalf("nil reconciler returned %v", got)
	}
}

// TestRunOnceResetsSkipState pins the reset where it actually has to happen.
// resetSkipped() is trivially correct on its own; what matters is that RunOnce
// calls it. The first version of this test cleared the field by hand, so
// deleting the reset from RunOnce left it green — a test that performs the very
// step it claims to verify.
func TestRunOnceResetsSkipState(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "ledger_reconciliation.go", nil, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var runOnce *ast.FuncDecl
	for _, d := range file.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == "RunOnce" {
			runOnce = fd
		}
	}
	if runOnce == nil {
		t.Fatal("RunOnce not found")
	}
	reset := findCall(t, runOnce, "resetSkipped")
	if reset == nil {
		t.Fatal("RunOnce no longer calls resetSkipped() — SkippedChecks would keep reporting " +
			"the previous run's skips, so a run that really executed and really found " +
			"nothing would still read as 'skipped'")
	}
	// Ordering matters: reset must precede both checks, or the first check's own
	// skip can be wiped by a later reset.
	for _, check := range []string{"checkBalanceChain", "checkUsageCredit"} {
		c := findCall(t, runOnce, check)
		if c == nil {
			t.Fatalf("RunOnce no longer calls %s", check)
		}
		if reset.Pos() > c.Pos() {
			t.Errorf("resetSkipped() is called after %s — that check's skip would be wiped", check)
		}
	}
}

// TestUsageCreditCheckIsGatedBeforeQuerying is the structural pin: the gate must
// be consulted BEFORE the query is issued, and it must return early. Pinning the
// call alone would be the weaker "the guard exists" assertion — the failure mode
// this guards against is a guard that is called but whose result is ignored, or
// consulted after the rows are already in hand.
func TestUsageCreditCheckIsGatedBeforeQuerying(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "ledger_reconciliation.go", nil, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var fn *ast.FuncDecl
	for _, d := range file.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == "checkUsageCredit" {
			fn = fd
			break
		}
	}
	if fn == nil {
		t.Fatal("checkUsageCredit not found")
	}
	gateIf := findIfCalling(t, fn, "usageCreditComparability")
	if gateIf == nil {
		t.Fatal("checkUsageCredit no longer consults usageCreditComparability — " +
			"the cross-family comparison is ungated and will emit unbounded false findings " +
			"once S4 stop-write is active")
	}
	// The skip branch must actually terminate. Asserting "the function contains
	// some `return 0`" is a trap: an earlier version of this test searched the
	// whole body for the first `return 0`, so deleting the return from the skip
	// branch left it matching the `return 0` in the query-error path and the
	// guard stayed green on a gate that no longer short-circuits. Pin the return
	// to THIS if-statement's body.
	if !hasReturn(gateIf.Body.List) {
		t.Error("the gate's skip branch has no return — checkUsageCredit would go on to query " +
			"rows after deciding the comparison is not decidable")
	}
	if !callsWithin(gateIf.Body.List, "markSkipped") {
		t.Error("the gate result is not recorded via markSkipped — a skip is then " +
			"indistinguishable from a clean run that found nothing")
	}
	// Ordering: the decision must precede the query, not merely coexist with it.
	if gateIf.Pos() > fn.Body.List[len(fn.Body.List)-1].Pos() {
		t.Error("gate statement sits after the last statement of the function body")
	}
	q := findCall(t, fn, "Query")
	if q == nil {
		t.Fatal("checkUsageCredit no longer issues a query — this test needs updating")
	}
	if gateIf.Pos() > q.Pos() {
		t.Error("the gate is consulted AFTER the query is issued; it must short-circuit before any rows are read")
	}

	// The sibling check must stay ungated: balance_chain reads credit_ledger_hot
	// only, so gating it would silently disable a check that is still decidable.
	for _, d := range file.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Name.Name != "checkBalanceChain" {
			continue
		}
		if findIfCalling(t, fd, "usageCreditComparability") != nil {
			t.Error("checkBalanceChain is gated by the usage↔credit predicate, but it replays " +
				"credit_ledger_hot against itself and stays fully decidable under stop-write — " +
				"gating it would trade a false-positive machine for a silent hole")
		}
	}
}

// findIfCalling returns the first IfStmt in fn that calls name — checking the
// Init clause as well as Cond, because the gate is written as
// `if ok, reason := usageCreditComparability(...); !ok {`, which puts the call in
// Init, not Cond. An earlier version inspected Cond only and reported the gate
// missing on correct code.
func findIfCalling(t *testing.T, fn *ast.FuncDecl, name string) *ast.IfStmt {
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
		if findCallExpr(ifs.Init, name) != nil || findCallExpr(ifs.Cond, name) != nil {
			out = ifs
			return false
		}
		return true
	})
	return out
}

// findCall returns the first call expression of the named selector/method in fn.
func findCall(t *testing.T, fn *ast.FuncDecl, method string) *ast.CallExpr {
	t.Helper()
	var out *ast.CallExpr
	ast.Inspect(fn, func(n ast.Node) bool {
		if out == nil {
			out = findCallExpr(n, method)
		}
		return out == nil
	})
	return out
}

func findCallExpr(n ast.Node, name string) *ast.CallExpr {
	// ast.Inspect panics on a nil node, and IfStmt.Init is nil whenever the
	// gate is written without an init clause. Without this guard the whole
	// guard dies with "ast.Walk: unexpected node type <nil>" — which reads as
	// a failure but is a crash, and would make a mutation look "caught" for the
	// wrong reason.
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

func callsWithin(stmts []ast.Stmt, name string) bool {
	for _, s := range stmts {
		if findCallExpr(s, name) != nil {
			return true
		}
	}
	return false
}

func hasReturn(stmts []ast.Stmt) bool {
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
