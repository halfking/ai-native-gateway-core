package bg

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// 本文件是 2026-11-13 12h 修订审计轮的钉测：CredentialRollupRefresher.refreshOnce
// 的跨实例 advisory lock 形状门。
//
// 背景：245+154 两个网关实例共用 252 库，两者都会跑 refresher——没有跨实例锁时
// 每 30m 至少两次 17.3s 全表聚合（实证见 963 主档；双实例教训 R51 的
// TuningViewRefresher 24.4 次/h 同族）。修复在 refreshOnce 事务内取
// pg_advisory_xact_lock（与 auto_index_refresher 的
// credentialModelIndexRollupLockKey 同纪律）。
//
// 这道门钉三件事（AST 级，变异任何一件都应红）：
//  1. refreshOnce 里有 tx.Exec(pg_advisory_xact_lock)（锁在事务内——xact 作用域）；
//  2. 锁 Exec 引用 credRollupLockKey 常量（防内联字面量）；
//  3. 锁 Exec 先于 UPSERT（CredRollupUpsertSQL）——锁后取才有互斥意义。

// credentialRollupRefresherLockGuarded 是门本体。
func TestCredentialRollupRefresherLockGuarded(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "credential_rollup_refresher.go", nil, 0)
	if err != nil {
		t.Fatalf("parse credential_rollup_refresher.go: %v", err)
	}
	var fn *ast.FuncDecl
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if fd.Name.Name == "refreshOnce" {
			fn = fd
			break
		}
	}
	if fn == nil {
		t.Fatal("credential_rollup_refresher.go 里找不到 refreshOnce")
	}

	// Exec 既可能以 `if _, err := tx.Exec(...)`（IfStmt.Init）出现，
	// 也可能以 `ct, err := tx.Exec(...)`（裸 AssignStmt）出现——两种都收。
	var execCalls []struct {
		pos  int
		call *ast.CallExpr
	}
	for i, st := range fn.Body.List {
		var as *ast.AssignStmt
		if ifst, ok := st.(*ast.IfStmt); ok {
			as, _ = ifst.Init.(*ast.AssignStmt)
		} else {
			as, _ = st.(*ast.AssignStmt)
		}
		if as == nil || len(as.Rhs) != 1 {
			continue
		}
		call, ok := as.Rhs[0].(*ast.CallExpr)
		if !ok {
			continue
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Exec" {
			continue
		}
		execCalls = append(execCalls, struct {
			pos  int
			call *ast.CallExpr
		}{i, call})
	}
	execCount := len(execCalls)
	lockPos := -1
	upsertPos := -1
	for _, ec := range execCalls {
		i := ec.pos
		call := ec.call
		// tx.Exec(rctx, SQL, key...)——SQL 在 arg1 起（arg0 是 ctx），
		// 逐实参扫描：锁的 SQL 是含 pg_advisory_xact_lock 的字符串字面量；
		// UPSERT 的实参里有 CredRollupUpsertSQL() 调用。
		for _, a := range call.Args {
			if bl, ok := a.(*ast.BasicLit); ok && bl.Kind == token.STRING {
				if containsAll(bl.Value, "pg_advisory_xact_lock") {
					// 锁 Exec：必须引用常量 credRollupLockKey（防内联）。
					args := flattenIdents(call.Args)
					if !hasIdentNamed(args, "credRollupLockKey") {
						t.Fatalf("advisory lock Exec 未引用 credRollupLockKey 常量（内联字面量会绕开形状门）")
					}
					lockPos = i
				}
			}
			if callSel, ok := a.(*ast.CallExpr); ok {
				switch fn := callSel.Fun.(type) {
				case *ast.Ident:
					if fn.Name == "CredRollupUpsertSQL" {
						upsertPos = i
					}
				case *ast.SelectorExpr:
					if fn.Sel.Name == "CredRollupUpsertSQL" {
						upsertPos = i
					}
				}
			}
		}
	}

	if execCount == 0 {
		t.Fatal("refreshOnce 里没有任何 tx.Exec —— 事务形态被破坏")
	}
	if lockPos == -1 {
		t.Fatal("refreshOnce 里没有 pg_advisory_xact_lock —— 跨实例互斥被移除" +
			"（245+154 共用 252 ⇒ 双实例重复 17.3s 全表聚合，见 963 审计）")
	}
	if upsertPos == -1 {
		t.Fatal("refreshOnce 里找不到 CredRollupUpsertSQL 调用 —— UPSERT 被改名/移动")
	}
	if lockPos > upsertPos {
		t.Fatalf("advisory lock（语句 %d）出现在 UPSERT（语句 %d）之后 —— 锁后取没有互斥意义", lockPos, upsertPos)
	}
}

func containsAll(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}

func flattenIdents(exprs []ast.Expr) []string {
	var out []string
	for _, e := range exprs {
		if e == nil {
			continue
		}
		ast.Inspect(e, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok {
				out = append(out, id.Name)
			}
			return true
		})
	}
	return out
}

func hasIdentNamed(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
