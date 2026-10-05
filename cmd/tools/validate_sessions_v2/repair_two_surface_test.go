package main

// 会话族「两个存储面」守卫（2026-10-05，§9.183）。
//
// # 为什么这道门存在
//
// admin/session_family_two_surface_test.go 是一条**逐条 SQL** 判的门，它的
// 登记表 `sessionFamilyBareParentReaders` 是**按文件**粒度的：
// `cmd/tools/validate_sessions_v2/repair.go` 为了放行「DELETE turns 父表腿的
// hot 对偶在紧邻的上一条语句」而登记了整个文件。
//
// ⇒ **登记的粒度比要守的性质粗**：文件里任何一个形状回退，登记都会替它挡下。
// 这不是假设——bodies 的 DELETE 原本就是真的漏面（只删 session_bodies、
// 漏 session_bodies_hot），修复它之后，**正是这条登记让那道修复处于无人看守
// 的状态**。删掉 hot 腿、把删除改回单面，这道门依然全绿。
//
// # 本门守的是什么
//
// 只守 §9.183 那一条修复，且逐条断、不断文件：
//
//	ExecuteRepair 里必须**同时**存在
//	    DELETE FROM public.session_bodies_hot
//	    DELETE FROM public.session_bodies
//	且 DeletedRows["session_bodies"] 是两面的 RowsAffected **相加**。
//
// 相加这一条不能省：只删两面却把计数写成单面，删除行为正确但
// RepairResult 的计数会低报，PlanRepair（走合并视图、数的是两面）与之对不上——
// 同一个函数里计数与动作再次用不同的面，正是 §9.183 认定的缺陷签名。
//
// # 为什么是静态判据而不是跑真库
//
// ExecuteRepair 的删除-重建是一个跨两张表、跨两个面的事务，构造它需要先在
// 真库里造出「同一会话在 hot 与父表两侧都有行」的夹具；本门守的是
// **源码形状**（这段 SQL 是否被写出来），真库夹具守的是**运行时行为**。
// 两者不互相替代，所以本门只声称前者，并在下面写明它声称不了什么。
//
// # 本门判不了的
//
//   - 拼装出来的表名（repair.go 目前的 SQL 都是字面量，没有运行时拼接；
//     若将来改成拼接，本门会因找不到字面量而报红——是**误报**，不是漏报，
//     因为拼装形态必须另行具名登记）；
//   - hot 腿那行 DELETE 的 WHERE 是否与父表腿同键。这条**没守**，是真盲区：
//     两面用不同谓词删同一会话，会退化成「只删了一部分」。本门只断两面都在。

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"
)

// execRepairSQLLiterals 返回 ExecuteRepair 函数体内、作为 tx.Exec 实参出现的
// 全部 SQL 字符串。选这个口径而不是「文件里所有字符串」：表名清单、注释里的
// 说明、错误文案都不是 SQL，照单全收会把判据变成 grep。
//
// **SQL 在 args[1]，不是 args[0]**：pgx 的 `Exec(ctx, sql, args...)` 第一位
// 是 context。写成 args[0] 时本门取到的全是 `fmt.Errorf` 的格式串，一条 SQL
// 都没有 —— 而「一条都没取到」是**判据失效**，不是通过，所以下面直接 Fatal。
// 这个错误是本门第一次运行时自己报出来的（不是事后想到的）。
func execRepairSQLLiterals(t *testing.T) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "repair.go", nil, 0) // 0 = 不带注释
	if err != nil {
		t.Fatalf("parse repair.go: %v", err)
	}
	var fn *ast.FuncDecl
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == "ExecuteRepair" {
			fn = fd
			break
		}
	}
	if fn == nil {
		t.Fatal("ExecuteRepair not found in repair.go — 门守的函数被改名/删除，必须同步更新本门")
	}
	var out []string
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) < 2 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Exec" {
			return true
		}
		lit, ok := call.Args[1].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		s, err := strconv.Unquote(lit.Value)
		if err != nil {
			return true
		}
		out = append(out, s)
		return true
	})
	if len(out) == 0 {
		t.Fatal("ExecuteRepair 内没有取到任何 tx.Exec 的 SQL 字面量 —— 判据失效（不是通过）")
	}
	return out
}

// mentionsRelationAsDeleteTarget 判 `DELETE FROM <rel>` 且 rel 完整匹配。
//
// 两个坑，都是本门第一次运行时自己撞出来的：
//   - 用后随字符检查而非 \b：rel 是另一个关系名的前缀时 \b 排除不掉
//     （`public.session_bodies` vs `public.session_bodies_hot`）。
//   - **两边必须同样大写**：SQL 为了匹配关键字被大写了，若拿大写串去
//     HasPrefix 一个小写的关系名，永远不成立 ⇒ 两个面都报「缺」，
//     一条完全正确的删除被报成两条缺失。误报方向是「全红」而不是「全绿」，
//     这一点是好的：门宁可吵也不能悄悄放过。
func mentionsRelationAsDeleteTarget(sql, rel string) bool {
	const marker = "DELETE FROM "
	up := strings.ToUpper(sql)
	want := strings.ToUpper(rel)
	from := 0
	for {
		i := strings.Index(up[from:], marker)
		if i < 0 {
			return false
		}
		i += from + len(marker)
		rest := up[i:]
		if !strings.HasPrefix(rest, want) {
			from = i
			continue
		}
		if tail := rest[len(want):]; tail == "" || tail[0] == ' ' || tail[0] == '\n' || tail[0] == '\t' || tail[0] == '\r' {
			return true
		}
		from = i
	}
}

func TestExecuteRepairDeletesBodiesFromBothSurfaces(t *testing.T) {
	stmts := execRepairSQLLiterals(t)

	// R42 扩展（代理复审 P3）：turns 腿与 bodies 腿同形（session_turns 与
	// session_turns_hot 互为孪生面），此前只靠 admin 门的按文件登记遮着——
	// 登记粒度比性质粗，正是本提交自己论证过的形态。
	for _, tc := range []struct{ surface string }{
		{"public.session_bodies_hot"},
		{"public.session_bodies"},
		{"public.session_turns_hot"},
		{"public.session_turns"},
		// §R43/L4：details 族同样是 hot/父表双面，且重建不写 details ——
		// 只删一面会留下键指向已删 turns 的孤儿行。
		{"public.session_turn_details_hot"},
		{"public.session_turn_details"},
	} {
		found := false
		for _, s := range stmts {
			if mentionsRelationAsDeleteTarget(s, tc.surface) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("ExecuteRepair 缺少 DELETE FROM %s —— session_bodies/session_turns 各有 "+
				"_hot 孪生面，只删一个面会让另一面的行活过这次修复（§9.183）", tc.surface)
		}
	}
}

// TestExecuteRepairCountsBothSurfaces 断计数侧：只删两面却单面计数，会让
// RepairResult.DeletedRows 与 PlanRepair（走合并视图、数两面）口径不一致。
// 这里断的是「求和表达式存在」，不是它的具体写法。
// containsBinaryExpr 判断 expr 内部是否有二元运算（`a + b`）。
//
// **只穿透类型转换，不穿透方法调用**。实际写法是
// `int(a.RowsAffected() + b.RowsAffected())`，顶层是 `int(...)` 这个 CallExpr。
// 但无脑穿透所有 CallExpr 会在 `int(tag.RowsAffected())` 这种**单面**写法上
// 走进 `RowsAffected()`（零参 CallExpr）并 panic —— 第一版就是这么写的，
// 于是「删除退回单面」这个恰恰要抓的变异，是**崩溃**而不是一条红因。
// 崩溃也是红，但红不出「哪条性质被破坏」，会把人引去修判据而不是修代码。
func containsBinaryExpr(expr ast.Expr) bool {
	switch e := expr.(type) {
	case *ast.BinaryExpr:
		return true
	case *ast.ParenExpr:
		return containsBinaryExpr(e.X)
	case *ast.CallExpr:
		// 类型转换形如 int(x) / uint64(x)：Fun 是标识符且恰有一个实参。
		// 方法调用 tag.RowsAffected() 的 Fun 是 SelectorExpr，天然被排除。
		if _, isIdent := e.Fun.(*ast.Ident); isIdent && len(e.Args) == 1 {
			return containsBinaryExpr(e.Args[0])
		}
		return false
	}
	return false
}

func TestExecuteRepairCountsBothSurfaces(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "repair.go", nil, 0)
	if err != nil {
		t.Fatalf("parse repair.go: %v", err)
	}
	var fn *ast.FuncDecl
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == "ExecuteRepair" {
			fn = fd
			break
		}
	}
	if fn == nil {
		t.Fatal("ExecuteRepair not found in repair.go")
	}
	// 找 `result.DeletedRows["session_bodies"] = <expr>`，要求 expr 是
	// 形如 a + b 的 BinaryExpr（两面 RowsAffected 相加）。
	// R42 扩展：bodies 与 turns 两个键都要求两面相加（同上 P3）。
	for _, key := range []string{"session_bodies", "session_turns", "session_turn_details"} {
		found, isSum := false, false
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			as, ok := n.(*ast.AssignStmt)
			if !ok || len(as.Lhs) != 1 || len(as.Rhs) != 1 {
				return true
			}
			idx, ok := as.Lhs[0].(*ast.IndexExpr)
			if !ok {
				return true
			}
			lit, ok := idx.Index.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			k, err := strconv.Unquote(lit.Value)
			if err != nil || k != key {
				return true
			}
			found = true
			if containsBinaryExpr(as.Rhs[0]) {
				isSum = true
			}
			return true
		})
		if !found {
			t.Errorf("ExecuteRepair 里找不到 result.DeletedRows[%q] = ... 赋值 —— 门守的代码被改写", key)
			continue
		}
		if !isSum {
			t.Errorf("result.DeletedRows[%q] 不是两面相加表达式 —— "+
				"删除行为可能正确但计数单面，与 PlanRepair（走合并视图数两面）口径不一致（§9.183）", key)
		}
	}
}
