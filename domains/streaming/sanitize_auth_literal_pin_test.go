package streaming

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"testing"

	"github.com/kaixuan/llm-gateway-go/security/sanitize"
)

// Audit round 237 — AST-level pin of the no-verifier tenant constant.
//
// The behavioural tests in this file can prove the STATIC KEY GATE works, but
// they cannot observe which tenant string prepareSanitizeRequest binds: the
// accessor (security/sanitize/input_protocols.go:24 authenticatedTenant) is
// unexported, so no test outside that package can read it back. Round 237
// tried, produced a criterion that was true by construction, and deleted it.
//
// This file therefore pins the constant at the only place it is observable from
// here: the call expression itself, parsed as AST rather than matched as text.
//
// Why AST and not text: a text criterion is satisfied by a COMMENT, and
// "delete the wiring, leave a comment" is the single most common way a guard
// silently stops guarding (playbook §172 / the "删掉接线只留注释" negative-control
// shape). An AST criterion cannot see comments at all, and the mutation
// control below was run for exactly that reason.
func TestPrepareSanitizeRequestBindsDefaultTenantLiteral(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "sanitize_auth.go", nil, 0)
	if err != nil {
		t.Fatalf("parse sanitize_auth.go: %v", err)
	}

	// Locate prepareSanitizeRequest's body.
	var fn *ast.FuncDecl
	for _, decl := range file.Decls {
		if d, ok := decl.(*ast.FuncDecl); ok && d.Name.Name == "prepareSanitizeRequest" {
			fn = d
			break
		}
	}
	if fn == nil {
		t.Fatal("找不到 prepareSanitizeRequest —— 判据失效（函数被改名或删除）")
	}

	// Collect the tenant literals actually passed to WithAuthenticatedTenant.
	var literals []string
	var callCount int
	ast.Inspect(fn, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "WithAuthenticatedTenant" {
			return true
		}
		callCount++
		if len(call.Args) >= 2 {
			if lit, ok := call.Args[1].(*ast.BasicLit); ok && lit.Kind == token.STRING {
				if v, err := strconv.Unquote(lit.Value); err == nil {
					literals = append(literals, v)
				}
			}
		}
		return true
	})

	if callCount == 0 {
		t.Fatal("prepareSanitizeRequest 里没有 WithAuthenticatedTenant 调用 —— " +
			"脱敏将退化成 _unknown 桶（security/sanitize input_protocols.go:24）")
	}
	// There are two WithAuthenticatedTenant calls in this function on purpose:
	// the no-verifier fallback passes a literal, the verified path passes
	// tenant(info). Only the literal one is a constant this test can pin, so
	// the non-literal call is reported, never fatal.
	if len(literals) == 0 {
		t.Fatal("prepareSanitizeRequest 里没有任何一个 WithAuthenticatedTenant 调用传字面量租户 —— " +
			"脱敏将退化成 _unknown 桶（security/sanitize input_protocols.go:24）")
	}

	found := false
	for _, v := range literals {
		if v == "default" {
			found = true
		}
	}
	if !found {
		first := literals[0]
		if first == "" {
			first = `""`
		}
		written := sanitize.SanitizeRedisKey(sanitize.HashTenant(first), "sid")
		read := sanitize.SanitizeRedisKey(sanitize.HashTenant("_unknown"), "sid")
		t.Fatalf("prepareSanitizeRequest 的 WithAuthenticatedTenant 实参里没有 \"default\"，实际是 %v。\n\n"+
			"响应侧在 keyInfo==nil 时回退到 \"\"（handler.go:5604-5607）。\n"+
			"  当前写桶 = %s\n  响应读桶 = %s\n"+
			"改这一侧之前请先改另一侧，并同步更新：\n"+
			"  - security/sanitize/map_key_normalisation_test.go 的 tripwire\n"+
			"  - docs/全面审计v3/2026-10-04/237-* 报告\n"+
			"  - 待裁决 101", literals, written, read)
	}
	t.Logf("prepareSanitizeRequest：%d 个 WithAuthenticatedTenant 调用，其中字面量 %v（含 \"default\"）、"+
		"其余为表达式（如 tenant(info)，由 verified 路径使用）", callCount, literals)
}
