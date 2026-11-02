package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// TestProbeLaneBootCheckNotGatedOnDataPlaneMode guards the 2026-11-02 fix.
//
// The probe-lane physical isolation boot check (probelane.AssertWithQueryRow)
// validates that the two probe tables exist when LLM_GATEWAY_PROBE_LANE_PHYSICAL
// is on. Its whole purpose is to turn a SILENT failure into a loud one:
//
//	lane on + migration 928 missing ⇒ probelane.PersistTurn fails for EVERY
//	probe turn ⇒ one error log ⇒ downstream hooks (stats / live / mirror) go
//	quiet ⇒ probe traffic silently stops being recorded.
//
// The check originally sat inside `if !bgDataPlaneOnly { ... }`. But the probe
// WRITE path is reachable in data-plane-only mode: telemetryClient.SetDB is
// gated on dbConn only (never on bgDataPlaneOnly), so a data-plane instance
// (LLM_GATEWAY_BG_MODE=data-plane — the 245 canary) DOES call
// probelane.PersistTurn. With the check behind the full-only gate, enabling
// the lane on 245 without migration 928 reproduces exactly the silent failure
// the check was added to prevent.
//
// This is the SAME defect class as the 2026-09-09→09-14 autoroute blackout
// (audit §9.70), where request-path wiring was gated behind !bgDataPlaneOnly
// and data-plane instances served from a dead fallback.
//
// The assertion is on the AST (enclosing condition chain), NOT on the
// spelling `if !bgDataPlaneOnly`: the natural re-introduction is the inverted
// `if bgDataPlaneOnly { } else { <check> }`, which reintroduces the identical
// outage while a text search stays green. See
// TestAutoRouteWiringNotGatedOnDataPlaneMode for the same reasoning applied
// to the autoroute engine.
func TestProbeLaneBootCheckNotGatedOnDataPlaneMode(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse main.go: %v", err)
	}

	parents := buildASTParentMap(file)

	// ── ① the probe-lane boot check ─────────────────────────────────────
	checks := findSelectorCalls(file, "probelane", "AssertWithQueryRow")
	if len(checks) == 0 {
		t.Fatal("main.go: probelane.AssertWithQueryRow call not found — the probe-lane boot " +
			"check was removed or moved; re-evaluate this guard")
	}
	for _, call := range checks {
		assertNotGatedOnDataPlaneMode(t, parents, call,
			"the probe-lane boot check (probelane.AssertWithQueryRow)")
	}

	// ── ② the probe-lane write path's DB wiring ──────────────────────────
	// The check only matters where the write path is live. SetDB is what makes
	// probelane.PersistTurn reachable (telemetry persists the request log).
	// If SetDB ever becomes data-plane-only, the write path is dead in
	// data-plane mode and assertion ① becomes harmless-but-pointless — so the
	// two must stay consistent, and this assertion is what forces a human to
	// notice the change instead of silently letting the coupling rot.
	setDBs := findSelectorCalls(file, "telemetryClient", "SetDB")
	if len(setDBs) == 0 {
		t.Fatal("main.go: telemetryClient.SetDB call not found — the probe-lane write path's " +
			"DB wiring moved; re-evaluate this guard")
	}
	for _, call := range setDBs {
		assertNotGatedOnDataPlaneMode(t, parents, call,
			"the probe-lane write path's DB wiring (telemetryClient.SetDB)")
	}
}

// assertNotGatedOnDataPlaneMode fails when the given call site sits under any
// enclosing condition that mentions the bgDataPlaneOnly identifier.
func assertNotGatedOnDataPlaneMode(t *testing.T, parents map[ast.Node]ast.Node, call ast.Node, what string) {
	t.Helper()
	for node := call; node != nil; node = parents[node] {
		var cond ast.Expr
		switch stmt := node.(type) {
		case *ast.IfStmt:
			cond = stmt.Cond
		case *ast.ForStmt:
			cond = stmt.Cond
		case *ast.SwitchStmt:
			cond = stmt.Tag
		case *ast.TypeSwitchStmt:
			continue
		default:
			continue
		}
		if cond == nil {
			continue
		}
		if id := findIdent(cond, "bgDataPlaneOnly"); id != nil {
			t.Fatalf("%s is gated behind bgDataPlaneOnly (%s) — data-plane-only instances "+
				"(LLM_GATEWAY_BG_MODE=data-plane, e.g. the 245 canary) reach the probe write "+
				"path anyway, so the lane would fail silently there (probe turns dropped, one "+
				"error log, downstream hooks quiet). Same defect class as the 2026-09 autoroute "+
				"blackout (audit §9.70).", what, id.Name)
		}
	}
}

// findSelectorCalls returns every call of pkg.Sel(...) in the file.
func findSelectorCalls(file *ast.File, pkg, sel string) []ast.Node {
	var out []ast.Node
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		se, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		ident, ok := se.X.(*ast.Ident)
		if !ok {
			return true
		}
		if ident.Name == pkg && se.Sel.Name == sel {
			out = append(out, call)
		}
		return true
	})
	return out
}

// findIdent returns the first identifier with the given name inside expr.
func findIdent(expr ast.Expr, name string) *ast.Ident {
	var found *ast.Ident
	ast.Inspect(expr, func(n ast.Node) bool {
		if found != nil {
			return false
		}
		if id, ok := n.(*ast.Ident); ok && id.Name == name {
			found = id
			return false
		}
		return true
	})
	return found
}

// buildASTParentMap maps every node to its AST parent.
//
// Built with a stack instead of ast.Inspect's nil-callback trick so that
// early-stop returns cannot desynchronise the stack: ast.Inspect only calls
// the visitor with nil for nodes whose visitor returned true, so a `return
// false` anywhere would leave a stale entry on the stack.
func buildASTParentMap(file *ast.File) map[ast.Node]ast.Node {
	parents := make(map[ast.Node]ast.Node)
	var stack []ast.Node

	ast.Inspect(file, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		if len(stack) > 0 {
			parents[n] = stack[len(stack)-1]
		}
		stack = append(stack, n)
		return true
	})
	return parents
}
