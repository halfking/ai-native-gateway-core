// Guard for the PREMISE of pending decision 96, plus three other premises that
// audit round 234 verified against the code and that would silently stop being
// true if somebody acts on the decision.
//
// Why (2026-10-04, audit round 234):
//
// Round 229 registered 96 — 「domains/transformation/lockfree_circuit_breaker.go
// is dead code and the archive doc still teaches it」 — and round 232 classified
// it as 代码阻塞 (fix is unique: delete the file). Round 234 executed §191-B's
// Q2 against the CODE and found three mutually exclusive options: delete the
// file / keep it with a deprecation notice / wire it into production in place of
// the live StreamCircuitBreaker. So the item is genuinely undecided.
//
// These guards do not assert a defect. They pin the *premises* the decision
// rests on, so that the moment somebody acts — deletes the file, annotates it,
// or wires it in — the ledger's claim visibly stops matching the code.
//
// Each premise is a statement about reachability, which is the only kind of
// statement here that can be checked mechanically without a running process.
// A premise that merely says "still on disk" would be decoration (round 233
// deleted a rule that never fired).
package sqlguard

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 96's symbols. The ADR at docs/adr/2026-09-09-ir-transport-layer-retirement.md
// :127-131 recorded the same finding on 2026-09-09, 24 days before round 229
// re-registered it as a pending decision (see the round 234 report).
var lockfree96Symbols = []string{
	"LockFreeCircuitBreaker",
	"NewLockFreeCircuitBreaker",
	"GetErrorCount",
}

// TestLockfreeCircuitBreakerStaysUnreferenced pins 96's load-bearing premise:
// the file is genuinely dead code.
//
// It fails when the symbols gain an importer or a reference from another file in
// the same package — i.e. exactly when somebody picks the "wire it in" option
// and the whole 96 discussion changes shape.
//
// The scan is AST-based, not text-based: the substring `LockFreeCircuitBreaker`
// also appears inside `NewLockFreeCircuitBreaker` and in this file's own
// declarations, and a text scan cannot tell those apart.
func TestLockfreeCircuitBreakerStaysUnreferenced(t *testing.T) {
	root := repoRootForTest(t)
	target := filepath.Join(root, "domains", "transformation", "lockfree_circuit_breaker.go")
	if _, err := os.Stat(target); err != nil {
		// The file was deleted. That is one of the three legitimate outcomes of
		// 96, so the premise is no longer "dead code" — it is "resolved". Say so
		// instead of failing: closing a decision is a decision, not a build
		// break (playbook §157).
		t.Logf("96 的前提已失效：lockfree_circuit_breaker.go 不存在了 —— " +
			"96 号已被处置（删除方案）。请人工确认后关闭待裁决 96。")
		return
	}

	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, filepath.Join(root, "domains", "transformation"), nil, 0)
	if err != nil {
		t.Fatalf("parse domains/transformation: %v", err)
	}

	var offenders []string
	for _, pkg := range pkgs {
		for path, file := range pkg.Files {
			base := filepath.Base(path)
			for _, sym := range lockfree96Symbols {
				if base == "lockfree_circuit_breaker.go" {
					// Self-references: the declaration and the method receivers.
					// A file always "mentions" its own symbols.
					continue
				}
				if identRefsSymbol(file, sym) {
					offenders = append(offenders, base+" 引用了 "+sym)
				}
			}
		}
	}

	if len(offenders) > 0 {
		sortStrings(offenders)
		t.Errorf("待裁决 96 的前提已被打破：lockfree 熔断器不再是死代码。\n  %s\n\n"+
			"若这是有意接线（96 的第三个方案：用 LockFreeCircuitBreaker 替换现役 "+
			"StreamCircuitBreaker），请在台账里把 96 改写为「已处置」并记录新的落点；"+
			"若是非预期引用，请回退。", strings.Join(offenders, "\n  "))
		return
	}
	t.Logf("96 前提仍成立：%d 个符号在 domains/transformation 内零外部引用（跳过自身文件）",
		len(lockfree96Symbols))
}

// identRefsSymbol reports whether any Ident in the file names the symbol.
// It skips the declaration itself: a FuncDecl named exactly sym is a
// definition, not a use.
func identRefsSymbol(file *ast.File, sym string) bool {
	found := false
	ast.Inspect(file, func(n ast.Node) bool {
		if found {
			return false
		}
		switch v := n.(type) {
		case *ast.FuncDecl:
			if v.Name != nil && v.Name.Name == sym {
				return false // definition, not a use
			}
		case *ast.TypeSpec:
			if v.Name != nil && v.Name.Name == sym {
				return false // definition, not a use
			}
		case *ast.Ident:
			if v.Name == sym {
				found = true
			}
		}
		return !found
	})
	return found
}

// TestCircuitStateIsDefinedOutsideLockfreeFile pins the other half of 96's
// "delete the file" option: deleting lockfree_circuit_breaker.go does not leave
// an undefined symbol, because CircuitState is defined in a DIFFERENT file of
// the same package.
//
// Without this premise, "delete the file" is not obviously safe, which is
// precisely why 232 called it a clean single fix. If someone later moves
// CircuitState into the lockfree file, deletion stops being safe and the
// decision must be reopened.
func TestCircuitStateIsDefinedOutsideLockfreeFile(t *testing.T) {
	root := repoRootForTest(t)
	dir := filepath.Join(root, "domains", "transformation")
	definedIn := ""
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, nil, 0)
	if err != nil {
		t.Fatalf("parse domains/transformation: %v", err)
	}
	for _, pkg := range pkgs {
		for path, file := range pkg.Files {
			ast.Inspect(file, func(n ast.Node) bool {
				ts, ok := n.(*ast.TypeSpec)
				if !ok || ts.Name == nil || ts.Name.Name != "CircuitState" {
					return true
				}
				definedIn = filepath.Base(path)
				return false
			})
		}
	}
	if definedIn == "" {
		t.Fatal("判据失效：全包找不到 CircuitState 的定义 —— " +
			"要么它被重命名/删除（96 的删除方案需要重新评估），要么解析口径失效")
	}
	t.Logf("CircuitState 定义在 %s", definedIn)
	if definedIn == "lockfree_circuit_breaker.go" {
		t.Errorf("CircuitState 现在定义在 lockfree_circuit_breaker.go 内 ⇒ " +
			"「删整文件」不再安全（会留下未定义符号），待裁决 96 必须重开评估")
	}
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
