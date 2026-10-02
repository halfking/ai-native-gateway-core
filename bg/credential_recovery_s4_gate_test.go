package bg

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// S4 停写门控范围（2026-10-02 审计，第三处跨门边界的检查）。
//
// lookbackCandidateSQL 拿「36h 窗口内是否有成功」当候选资格判据，证据源是
// request_logs_hot ∪ request_logs —— **整体在 S4 门内**。停写生效后证据源冻结，
// EXISTS(...) 恒空，于是**没有任何绑定再进 lookback 候选集**，降级/离线绑定
// 在这条路径上永久失去恢复机会，而扫描照常返回空、无任何痕迹。
//
// 方向与 ledger_reconciliation 的 usage_credit_mismatch 相反：那边跨门产生
// **假报机**（无上界的假差异），这边跨门产生**静默洞**（无痕的漏恢复）。
// 同一个结构缺陷的两个方向，所以判据与修法也同形。
//
// 逐项核实过同文件另外两条恢复路径**不需要**门控（不是「同一个文件就一起挡」）：
//   - expiredCmbRecoverySQL（:1132 的 NOT EXISTS）读 node_probe_state；
//   - recoverFreshDegradedSQL（:1261 的 NOT EXISTS）同样读 node_probe_state。
// 两者都不碰 request_logs、不受该门影响，停写后仍完全可判定。
// 真正被这条扫描漏掉的，正是 unavailable_reason **不**属于
// continuous_failure / probe!_% / auto!_% 的那批绑定——因为那正是另外两条
// 路径接不住的部分。

func TestLookbackComparability(t *testing.T) {
	cases := []struct {
		name       string
		logsWrite  bool
		wantOK     bool
		wantReason string
	}{
		{"gate open — the 36h question is answerable", true, true, ""},
		{"gate closed — the question stops being answerable", false, false, lookbackSkipS4StopWrite},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ok, reason := lookbackComparability(tc.logsWrite)
			if ok != tc.wantOK {
				t.Fatalf("comparable = %v, want %v", ok, tc.wantOK)
			}
			if reason != tc.wantReason {
				t.Fatalf("reason = %q, want %q", reason, tc.wantReason)
			}
			// A refusal must carry a machine-readable reason, otherwise "skipped"
			// degrades silently into "no candidates" — which for this scan reads
			// as "nothing to recover" and is the exact failure we're preventing.
			if !ok && strings.TrimSpace(reason) == "" {
				t.Error("refused to scan but gave no machine-readable reason")
			}
			if ok && reason != "" {
				t.Errorf("scanned fine but still reported reason %q", reason)
			}
		})
	}
}

// TestLookbackSkipIsNotSeparableFromNoCandidates pins the channel that keeps
// "we stopped being able to tell" apart from "there is nothing to tell".
func TestLookbackSkipIsNotSeparableFromNoCandidates(t *testing.T) {
	r := &CredentialRecovery{}
	if got := r.SkippedChecks(); len(got) != 0 {
		t.Fatalf("fresh recovery already reports skips: %v", got)
	}
	r.markSkipped(lookbackSkipS4StopWrite)
	if got := r.SkippedChecks(); len(got) != 1 || got[0] != lookbackSkipS4StopWrite {
		t.Fatalf("skip not recorded: %v", got)
	}
	r.resetSkipped()
	if got := r.SkippedChecks(); len(got) != 0 {
		t.Fatalf("resetSkipped did not clear the list: %v", got)
	}
	var nilRec *CredentialRecovery
	if got := nilRec.SkippedChecks(); got != nil {
		t.Fatalf("nil recovery returned %v", got)
	}
}

// TestLookbackScanIsGatedBeforeQuerying is the structural pin. Three separate
// things must hold, and each has a way to silently break:
//
//  1. the scan consults the predicate at all (drop it → cross-boundary again);
//  2. the gate precedes the query (move it after → rows already in hand);
//  3. the gate's branch terminates (drop the return → it queries anyway).
//
// A weaker version of (3) searched the function body for the first `return` and
// matched an unrelated one — the same bug shape fixed in ledger_reconciliation.go.
func TestLookbackScanIsGatedBeforeQuerying(t *testing.T) {
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

	gate := findIfCallingCred(t, scan, "lookbackComparability")
	if gate == nil {
		t.Fatal("scanLookbackRecoveries no longer consults lookbackComparability — the 36h " +
			"evidence source (request_logs_hot/request_logs) is inside the S4 blast radius, " +
			"and after stop-write the scan would report 'no candidates' forever")
	}
	if !hasReturnCred(gate.Body.List) {
		t.Error("the gate's skip branch has no return — the scan would go on to query rows " +
			"after deciding the question is unanswerable")
	}
	if !callsWithinCred(gate.Body.List, "markSkipped") {
		t.Error("the gate result is not recorded via markSkipped — a skip is then " +
			"indistinguishable from a scan that legitimately found no candidates")
	}
	// The reset must run BEFORE the gate, as a sibling — not inside the skip
	// branch. Inside would mean only skipped runs ever clear the list, so a run
	// that really executed and really found nothing would keep inheriting the
	// previous run's "skipped" verdict. (The guard's first version looked for
	// it inside gate.Body and correctly failed on this file — the placement was
	// right and the assertion was wrong.)
	reset := findCallCred(t, scan, "resetSkipped")
	if reset == nil {
		t.Error("the scan never calls resetSkipped — SkippedChecks would keep reporting the " +
			"previous run's skips, so a run that really executed and really found nothing " +
			"would still read as 'skipped'")
	} else {
		if reset.Pos() > gate.Pos() {
			t.Error("resetSkipped() is called AFTER the gate — a run that skipped would keep " +
				"the previous run's verdict, and a run that executed would clear it late")
		}
		if callsWithinCred(gate.Body.List, "resetSkipped") {
			t.Error("resetSkipped() sits inside the skip branch — only skipped runs would clear " +
				"the list, so an executed run that found nothing inherits the old verdict")
		}
	}
	q := findCallCred(t, scan, "Query")
	if q == nil {
		t.Fatal("scan no longer issues a query — this test needs updating")
	}
	if gate.Pos() > q.Pos() {
		t.Error("the gate is consulted AFTER the query is issued; it must short-circuit first")
	}

	// The two ungated sibling scans must stay ungated: they read
	// node_probe_state, not request_logs, so they remain fully decidable under
	// stop-write. Gating them would trade a silent hole for a different one.
	for _, d := range file.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok {
			continue
		}
		switch fd.Name.Name {
		case "recoverExpiredBindings", "recoverFreshDegradedBindings":
			if findIfCallingCred(t, fd, "lookbackComparability") != nil {
				t.Errorf("%s is gated by the lookback predicate, but it keys off "+
					"credential_model_bindings / node_probe_state, which the S4 gate does not "+
					"touch — it stays decidable, and gating it would silently disable recovery "+
					"on the two paths that ARE still valid", fd.Name.Name)
			}
		}
	}
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
