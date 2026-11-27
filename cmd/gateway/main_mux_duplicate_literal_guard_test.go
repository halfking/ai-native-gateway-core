package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"testing"
)

// TestMainMuxNoDuplicateLiteralRegistration 钉「同一个 mux 上字面量路由不得重复注册」。
//
// 2026-11-28 211 部署实拍：merge 把
//
//	mux.HandleFunc("GET /api/admin/session-analytics/v2/filter-options", ...)
//
// 复制成了相邻两行 ⇒ Go 1.22 ServeMux 启动即 panic("conflicts with pattern") ⇒
// 蓝绿候选槽重启循环、部署回退。编译、vet、既有路由守卫全绿——它们都不在
// 「同一个 main.go 里字面量重复」这个形态上。
//
// 判据：在**同一个语句块的直接子语句**里，对同一个接收者（mux 变量名）重复出现的
// 字面量 pattern 即红。嵌套块/互斥的 if-else 分支不互相比较（main.go 里 /v1/chat/completions
// 等在不同分支各注册一次是合法的）。
func TestMainMuxNoDuplicateLiteralRegistration(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse main.go: %v", err)
	}
	type key struct{ recv, pattern string }
	ast.Inspect(f, func(n ast.Node) bool {
		blk, ok := n.(*ast.BlockStmt)
		if !ok {
			return true
		}
		seen := map[key]token.Position{}
		for _, st := range blk.List {
			es, ok := st.(*ast.ExprStmt)
			if !ok {
				continue
			}
			call, ok := es.X.(*ast.CallExpr)
			if !ok || len(call.Args) < 1 {
				continue
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || (sel.Sel.Name != "HandleFunc" && sel.Sel.Name != "Handle") {
				continue
			}
			recv, ok := sel.X.(*ast.Ident)
			if !ok {
				continue
			}
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				continue
			}
			pat, err := strconv.Unquote(lit.Value)
			if err != nil {
				continue
			}
			k := key{recv.Name, pat}
			pos := fset.Position(call.Pos())
			if prev, dup := seen[k]; dup {
				t.Errorf("main.go:%d 与 :%d 在同一语句块内对 %s 重复注册字面量路由 %q（ServeMux 启动 panic）",
					pos.Line, prev.Line, recv.Name, pat)
				continue
			}
			seen[k] = pos
		}
		return true
	})
}
