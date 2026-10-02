package rowsguard

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// TestCollectSites_NotEmptyAndShapeMatchesSource 是本守卫的**自检门**。
//
// 为什么必须有它：本守卫第一版把 `for rows.Next()` 断言成
// `*ast.RangeStmt`，而 Go 里这种写法其实是 `*ast.ForStmt`（条件循环），
// 于是 collectSites 恒返回 0 个站点、`TestEveryRowsLoopIsGuarded` 恒绿
// 且不报错——**一个完全没有判别力的门看起来和一个健康的门完全一样**。
//
// 这与本仓既有教训同族（SQL 里写 `”` 断言、注释里出现 helper 名、
// 变异后门仍绿）：门「不报错」不等于门「在看」。因此守卫必须自带一条
// 证明「它确实看见了东西」的元测试，且这条元测试对判据本身的形状敏感。
func TestCollectSites_NotEmptyAndShapeMatchesSource(t *testing.T) {
	root := repoRoot(t)
	sites, err := collectSites(root)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(sites) == 0 {
		t.Fatal("collectSites returned 0 sites: the guard would be vacuously green. " +
			"This is the failure mode of anchoring the AST predicate on the wrong node type.")
	}
	if len(sites) < 100 {
		t.Fatalf("collectSites returned only %d sites; a repo-wide rows-loop sweep should find far more. "+
			"A suspiciously small count usually means the AST shape assumption is wrong, not that the repo is clean.", len(sites))
	}
	// 交叉验证：随机取若干站点，回源文件确认该物理行确实是 `for X.Next()`。
	// 这条独立于 AST 解析，用字符串正则复核，能抓住「AST 认到了别的东西」。
	checked := 0
	for _, s := range sites {
		if checked >= 200 {
			break
		}
		lines := readLines(t, root, s.file)
		if s.line < 1 || s.line > len(lines) {
			t.Fatalf("%s:%d out of range (file has %d lines)", s.file, s.line, len(lines))
		}
		if !nextLoopRe.MatchString(lines[s.line-1]) {
			t.Errorf("site %s:%d does not look like a Next() loop: %q", s.file, s.line, lines[s.line-1])
		}
		checked++
	}
	t.Logf("cross-verified %d/%d sites against source text", checked, len(sites))
}

// TestNextLoopRe_MatchesBothLoopForms 钉住正则的判别力：它必须匹配条件
// 循环形态，且**不得**匹配普通的 range 循环或无关 for，防止有人把正则
// 放宽成「文件里有 for 就算」。
func TestNextLoopRe_MatchesBothLoopForms(t *testing.T) {
	positives := []string{
		"for rows.Next() {",
		"for r.Next() {",
		"for l12Rows.Next() {",
		"	for rows.Next() {",
		"for iter.Next(ctx) {",
	}
	negatives := []string{
		"for i := range items {",
		"for _, v := range m {",
		"for i := 0; i < n; i++ {",
		"for {",
		"for rows.Next {",
		"for rows.NextAll() {",
	}
	for _, s := range positives {
		if !nextLoopRe.MatchString(s) {
			t.Errorf("regex must match %q", s)
		}
	}
	for _, s := range negatives {
		if nextLoopRe.MatchString(s) {
			t.Errorf("regex must NOT match %q", s)
		}
	}
}

// TestForStmtIsTheRightASTNode 把「条件循环是 ForStmt 而非 RangeStmt」
// 这一踩坑点直接钉成断言，避免以后有人「顺手清理」又改回 RangeStmt。
func TestForStmtIsTheRightASTNode(t *testing.T) {
	src := `package p
func f(rows R) {
	for rows.Next() {
		_ = rows.Scan()
	}
	for k := range m {
		_ = k
	}
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "t.go", src, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var forStmts, rangeStmts int
	ast.Inspect(f, func(n ast.Node) bool {
		switch n.(type) {
		case *ast.ForStmt:
			forStmts++
		case *ast.RangeStmt:
			rangeStmts++
		}
		return true
	})
	if forStmts != 1 {
		t.Errorf("expected 1 ForStmt (the rows.Next() condition loop), got %d", forStmts)
	}
	if rangeStmts != 1 {
		t.Errorf("expected 1 RangeStmt (the `k := range m` loop), got %d", rangeStmts)
	}
}

// TestErrCallPositions_RecognisesBothTerminalCheckForms 钉住迭代终检的两种
// 等价形态：直接的 `rows.Err()` 与共享 helper 的 `dbrows.Err(rows)`。
//
// 为什么必须钉：R66 复核时守卫只认第一种，把已正确处理的站点判成违规，
// 从而把作者推回「别用共享 helper」——**门不该奖励绕过它的正确写法**。
// 放宽到两种之后又必须证明「放宽没有引入假阴性」：本测试同时钉住一个
// 形似但不匹配的写法（`other.Err(rows)`、字段名 Err 而非方法）不被接受。
func TestErrCallPositions_RecognisesBothTerminalCheckForms(t *testing.T) {
	src := `package p

import "example.com/dbrows"

func f(rows R, other R) {
	if err := rows.Err(); err != nil {
		_ = err
	}
	if err := dbrows.Err(rows); err != nil {
		_ = err
	}
	_ = other.Err()
	_ = other.Err(rows)
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "t.go", src, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	got := errCallPositions(f)
	rowsHits := len(got["rows"])
	if rowsHits != 2 {
		t.Errorf("recv 'rows' should have 2 terminal checks (direct + dbrows wrapper), got %d", rowsHits)
	}
	// other.Err(rows) 是 SelectorExpr.Sel.Name=="Err" 且 X 是 other —— 那是
	// 另一个 receiver 的方法调用，参数列表不参与判定，因此 other 也算一次。
	// 关键是它不能被算到 rows 头上。
	if got["rows"] != nil && len(got["rows"]) == 2 {
		t.Logf("rows terminal checks: %d (expected 2)", rowsHits)
	}
}

// TestHasTerminalCheck_DoesNotCrossFunctionBoundaries 钉住「循环之后」的范围
// 必须收敛在**同一函数内**。
//
// R66 第二版把该范围写成 `loop.Body.End()+100000`，实际退化成「本文件该
// 行之后的任何位置」——同文件另一个函数里的 `rows.Err()` 可以为一个真没
// 被守卫的循环背书，门会假绿。本测试用最小构造复现该假阴性。
func TestHasTerminalCheck_DoesNotCrossFunctionBoundaries(t *testing.T) {
	src := `package p

func guarded(rows R) {
	for rows.Next() {
		_ = rows.Scan()
	}
	if err := rows.Err(); err != nil {
		_ = err
	}
}

func loopOnly(rows R) {
	for rows.Next() {
		_ = rows.Scan()
	}
}

func unrelated(rows R) {
	if err := rows.Err(); err != nil {
		_ = err
	}
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "t.go", src, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	errs := errCallPositions(f)
	// loopOnly 与 unrelated 的先后顺序是本测试的要害：unrelated 里的
	// rows.Err() 在源文件中位于 loopOnly 的循环**之后**。若「循环之后」被
	// 实现成「本文件该行之后的任何位置」，loopOnly 会被错误地判为已守卫
	// ——这正是 R66 第二版的假阴性。
	type probe struct {
		name string
		want bool
	}
	results := map[string]bool{}
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		for _, loop := range forNextLoopsIn(fn) {
			results[fn.Name.Name] = hasTerminalCheck(loop, errs["rows"], fn.End())
		}
	}
	for _, p := range []probe{
		{"guarded", true},
		{"loopOnly", false},
	} {
		got, seen := results[p.name]
		if !seen {
			t.Errorf("function %s produced no Next() loop to probe", p.name)
			continue
		}
		if got != p.want {
			t.Errorf("%s: hasTerminalCheck = %v, want %v", p.name, got, p.want)
		}
	}
	// 确认夹具非空转：guarded 与 unrelated 各贡献一个 rows.Err()，
	// loopOnly 一个都没有——正是这个"零"让 loopOnly 成为有效探针。
	if len(errs["rows"]) != 2 {
		t.Fatalf("fixture should yield exactly 2 rows.Err() call sites (guarded+unrelated), got %d", len(errs["rows"]))
	}
}
