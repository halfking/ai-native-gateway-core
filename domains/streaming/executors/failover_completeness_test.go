package executors

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// TestFailoverKindCoverage_EveryRetryableKindIsHandled documents which error
// kinds the candidate loop hands off on, and how.
//
// A failed candidate advances to the next one through one of three routes:
//
//	explicit continue @ IsCredentialFatal   — auth / quota
//	explicit continue @ isTransientFailoverKind — transient upstream
//	falling off the end of the loop body    — everything else
//
// All three reach the next candidate, which is why every IsRetryable kind fails
// over today even though two of them (KindNetwork, KindConcurrent) are in
// neither predicate. This test records that fact so the gap is visible rather
// than accidental, and so a future edit that makes the explicit branches
// load-bearing (see TestCandidateLoopFailsOverForEveryRetryableKind) has a list
// to work from.
func TestFailoverKindCoverage_EveryRetryableKindIsHandled(t *testing.T) {
	retryable := []errorsx.ErrorKind{
		errorsx.KindTransient,
		errorsx.KindTimeout,
		errorsx.KindNetwork,
		errorsx.KindUpstreamDown,
		errorsx.KindUpstreamOverloaded,
		errorsx.KindConcurrent,
		errorsx.KindStreamTimeout,
	}

	// Kinds that reach the next candidate only by falling off the end of the
	// loop body. Keeping them enumerated makes the implicit path auditable.
	implicitOnly := map[errorsx.ErrorKind]bool{
		errorsx.KindNetwork:    true,
		errorsx.KindConcurrent: true,
	}

	for _, kind := range retryable {
		t.Run(string(kind), func(t *testing.T) {
			if !errorsx.IsRetryable(kind) {
				t.Fatalf("test list is stale: %q is no longer IsRetryable", kind)
			}
			explicit := isTransientFailoverKind(kind) || errorsx.IsCredentialFatal(kind)
			switch {
			case explicit && implicitOnly[kind]:
				t.Errorf("%q is now handled explicitly — remove it from "+
					"implicitOnly so the list stays truthful", kind)
			case !explicit && !implicitOnly[kind]:
				t.Errorf("%q is IsRetryable but reaches the next candidate only "+
					"by falling off the end of the loop body. That works today, "+
					"but it is undocumented: either add it to "+
					"isTransientFailoverKind or to implicitOnly with a reason.",
					kind)
			}
		})
	}
}

// TestCandidateLoopFailsOverForEveryRetryableKind is a structural guard on the
// assumption that makes the above safe.
//
// Both `continue` statements near the end of Execute's candidate loop (the
// IsCredentialFatal one and the isTransientFailoverKind one) are currently
// no-ops: they are the final statements in the loop body, so control reaches
// the next candidate whether or not they run. Every error kind therefore fails
// over, including the ones in neither predicate.
//
// Append one statement after that last `continue` and the property inverts: the
// two branches become the only paths to the next candidate, and every kind
// outside them starts ending the walk at the first failure — a silent,
// production-only regression in cross-credential failover that no behavioural
// unit test would catch, because the kinds involved (KindNetwork,
// KindConcurrent) have no dedicated test of their own.
//
// So assert the shape directly: the candidate loop's body must END with an
// `if` whose block's last statement is `continue`.
func TestCandidateLoopFailsOverForEveryRetryableKind(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "executor.go", nil, 0)
	if err != nil {
		t.Fatalf("parse executor.go: %v", err)
	}

	loop := findCandidateLoop(t, file)
	if len(loop.Body.List) == 0 {
		t.Fatal("candidate loop body is empty")
	}

	last := loop.Body.List[len(loop.Body.List)-1]
	ifStmt, ok := last.(*ast.IfStmt)
	if !ok {
		t.Fatalf("candidate loop body now ends with %T, not an if statement.\n"+
			"The two trailing `continue`s (IsCredentialFatal, "+
			"isTransientFailoverKind) were no-ops precisely because nothing "+
			"followed them, which is what let every error kind — including "+
			"KindNetwork and KindConcurrent, which are in neither predicate — "+
			"fall through to the next candidate.\n"+
			"If this new trailing statement is intentional, every retryable "+
			"kind must now be covered by an explicit continue, or "+
			"cross-credential failover silently stops for the uncovered ones.",
			last)
	}

	if len(ifStmt.Body.List) == 0 {
		t.Fatal("trailing if block is empty")
	}
	if _, ok := ifStmt.Body.List[len(ifStmt.Body.List)-1].(*ast.BranchStmt); !ok {
		t.Errorf("trailing if block no longer ends in `continue` (got %T) — "+
			"see this test's doc comment for why that matters",
			ifStmt.Body.List[len(ifStmt.Body.List)-1])
	}
}

// findCandidateLoop locates Execute's `for candidateIndex, cand := range
// candidates` loop. Identified by its range expression rather than by position
// so ordinary edits to Execute do not break the guard.
func findCandidateLoop(t *testing.T, file *ast.File) *ast.RangeStmt {
	t.Helper()

	var found *ast.RangeStmt
	ast.Inspect(file, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "Execute" {
			return true
		}
		ast.Inspect(fn, func(inner ast.Node) bool {
			rng, ok := inner.(*ast.RangeStmt)
			if !ok {
				return true
			}
			ident, ok := rng.X.(*ast.Ident)
			if !ok || ident.Name != "candidates" {
				return true
			}
			// The value variable is the candidate itself; skip index-only
			// loops over the same slice.
			if val, ok := rng.Value.(*ast.Ident); !ok || val.Name != "cand" {
				return true
			}
			found = rng
			return false
		})
		return false
	})

	if found == nil {
		t.Fatal("could not find `for ... := range candidates` loop in Execute — " +
			"this guard needs updating to match the new loop shape")
	}
	return found
}
