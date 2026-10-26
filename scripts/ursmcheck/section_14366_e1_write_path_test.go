package ursm_test

// §143.66 的门：把「E1 门开 ⇒ 逐轮 outbound_body 停写」钉成**结构判据**，
// 并把「outbound_body **不可删列**」钉成**反向断言**。
//
// ★ 为什么是结构判据而不是字符串匹配（承 §143.65 的纪律）：
//   「门要查结构、不能只查字符串在不在」—— 字符串形态一变就红、或形态变了却仍绿，
//   两种都不合格 ⇒ 本门用 go/ast 解析真实语法树。
//
// ★ 三条判据：
//   ① 【写侧·停写】`session_writer_v2.go` 里，`storage.session_final_full_enabled`
//      的 if 分支体内必须存在 `bodiesRec.OutboundBody = nil`
//      ⇒ 门开时逐轮行不落 outbound_body（★ 这正是「月省 ≈1.8GB」的实现点）。
//   ② 【写侧·全量】同一开关的分支必须调用 `WriteFinalFullInTx`
//      ⇒ 停写的同时必须有人承载「每会话最后完整快照」⇒ **不能只停不写**。
//   ③ 【读侧·反向断言】`outbound_builder.go` 的 `BuildFromLatestOutbound`
//      必须仍然直读 `LoadLatestOutbound`（即直读 outbound_body）
//      ⇒ ★★ 因为 outbound_body 是「**压缩后实际转发给上游的内容**」的忠实记录，
//      而 `BuildFromDeltas` 只是**近似重建**（会按需过滤压缩 marker）
//      ⇒ ⇒ **删列式去重不成立** ⇒ 本门把它钉住，防止有人「顺手把列删了」。
//   ④ 【自检】判据 ① 必须**不是恒真**：用一个反例源码片段验证它会红。
//
// ★ 不依赖生产状态、不含任何生产读数。

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	writerPath14366     = "domains/session/v2/session_writer_v2.go"
	builderPath14366    = "domains/session/v2/outbound_builder.go"
	e1SwitchKey14366    = "storage.session_final_full_enabled"
	e1SwitchFn14366     = "GetPlatformBool"
	nilAssignField14366 = "OutboundBody"
	finalFullCall14366  = "WriteFinalFullInTx"
)

func repoRoot14366(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func parseFile14366(t *testing.T, rel string) (*token.FileSet, *ast.File) {
	t.Helper()
	path := filepath.Join(repoRoot14366(t), rel)
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读 %s 失败：%v", rel, err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		t.Fatalf("解析 %s 失败：%v", rel, err)
	}
	return fset, f
}

// containsSwitchCall14366 判定一个表达式里是否出现
// `settings.GetPlatformBool("<key>", …)` 形态的调用（★ 精确到 key，避免误判别的开关）。
func containsSwitchCall14366(e ast.Expr, key, fn string) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != fn {
			return true
		}
		for _, a := range call.Args {
			if lit, ok := a.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				if strings.Trim(lit.Value, `"`) == key {
					found = true
				}
			}
		}
		return true
	})
	return found
}

// ifBodiesWithSwitch14366 返回「条件里含该开关调用」的 if 语句列表。
func ifBodiesWithSwitch14366(f *ast.File, key, fn string) []*ast.IfStmt {
	var out []*ast.IfStmt
	ast.Inspect(f, func(n ast.Node) bool {
		ifs, ok := n.(*ast.IfStmt)
		if !ok {
			return true
		}
		if containsSwitchCall14366(ifs.Cond, key, fn) {
			out = append(out, ifs)
		}
		return true
	})
	return out
}

// hasNilAssignToField14366 在语句列表里找 `X.<Field> = nil` 形态的赋值。
func hasNilAssignToField14366(stmts []ast.Stmt, field string) bool {
	found := false
	for _, st := range stmts {
		as, ok := st.(*ast.AssignStmt)
		if !ok || len(as.Rhs) != 1 {
			continue
		}
		ident, ok := as.Rhs[0].(*ast.Ident)
		if !ok || ident.Name != "nil" {
			continue
		}
		for _, lhs := range as.Lhs {
			sel, ok := lhs.(*ast.SelectorExpr)
			if !ok {
				continue
			}
			if sel.Sel.Name == field {
				found = true
			}
		}
	}
	return found
}

// hasCall14366 在语句列表里找名字匹配的调用。
func hasCall14366(stmts []ast.Stmt, name string) bool {
	found := false
	for _, st := range stmts {
		ast.Inspect(st, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if sel.Sel.Name == name {
				found = true
			}
			return true
		})
	}
	return found
}

// ─────────────────────────────────────────────────────────────────────────────
// ① + ② 写侧结构门
// ─────────────────────────────────────────────────────────────────────────────

// TestSection14366E1StopsPerTurnOutboundWrite 是写侧结构门：
// E1 门开 ⇒ 逐轮行停写 outbound_body，且必须同时写 final_full。
func TestSection14366E1StopsPerTurnOutboundWrite(t *testing.T) {
	_, f := parseFile14366(t, writerPath14366)

	ifs := ifBodiesWithSwitch14366(f, e1SwitchKey14366, e1SwitchFn14366)
	if len(ifs) == 0 {
		t.Fatalf("%s：找不到以 `settings.%s(%q, …)` 为条件的 if 分支 ⇒ "+
			"★ E1（逐轮停写 outbound_body）的写侧结构已变 ⇒ 本门的判据对象漂移了 ⇒ "+
			"请同步本门（不要直接删判据）", writerPath14366, e1SwitchFn14366, e1SwitchKey14366)
	}

	// ① 停写：至少一个开关分支的体内必须有 `X.OutboundBody = nil`
	nilAssigned := false
	for _, ifs := range ifs {
		if hasNilAssignToField14366(ifs.Body.List, nilAssignField14366) {
			nilAssigned = true
			break
		}
	}
	if !nilAssigned {
		t.Errorf("%s：E1 开关分支里没有 `%s = nil` 的赋值 ⇒ ★ 门开时逐轮行仍会落 outbound_body "+
			"⇒ 「月省 ≈1.8GB」的机制【已失效】⇒ 本轮 §143.66 的存量读数（1,819 MB）会重新增长",
			writerPath14366, nilAssignField14366)
	}

	// ② 承载：至少一个开关分支必须调用 WriteFinalFullInTx（★ 不能只停不写）
	finalFullCalled := false
	for _, ifs := range ifs {
		if hasCall14366(ifs.Body.List, finalFullCall14366) {
			finalFullCalled = true
			break
		}
	}
	if !finalFullCalled {
		t.Errorf("%s：E1 开关分支里没有 `%s` 调用 ⇒ ★ 停写 outbound_body 之后，"+
			"「每会话最后完整快照」就没人承载了 ⇒ 读侧会拿到空的正文", writerPath14366, finalFullCall14366)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ③ 读侧反向断言：outbound_body 不可删列
// ─────────────────────────────────────────────────────────────────────────────

// TestSection14366OutboundBodyIsNotRedundant 是**反向断言**门：
// `BuildFromLatestOutbound` 必须仍然直读 outbound_body。
//
// ★ 依据（§143.66 实测 + 代码级判定）：
//
//	· outbound_body 是「**压缩后实际转发给上游的内容**」的忠实记录；
//	· `BuildFromDeltas` 只是**近似重建**（会按需过滤压缩 marker）；
//	· 压缩层需要「上一轮实际发送的内容（含压缩）」⇒ 直读 outbound_body。
//
// ⇒ ⇒ **删列式去重不成立** ⇒ 本门把这条结论钉住。
func TestSection14366OutboundBodyIsNotRedundant(t *testing.T) {
	_, f := parseFile14366(t, builderPath14366)

	var target *ast.FuncDecl
	for _, d := range f.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Name.Name == "BuildFromLatestOutbound" {
			target = fn
			break
		}
	}
	if target == nil {
		t.Fatalf("%s：找不到 `BuildFromLatestOutbound` ⇒ ★ 压缩层取「上一轮实际转发内容」的入口没了 "+
			"⇒ 请确认这是否是有意删除（若是，请同步本门并更新 §143.66 的结论）", builderPath14366)
	}
	if !hasCall14366(target.Body.List, "LoadLatestOutbound") {
		t.Errorf("%s：`BuildFromLatestOutbound` 不再调用 `LoadLatestOutbound` ⇒ "+
			"★ 它已改为「重建」而非「直读」⇒ ★★ 则 outbound_body 的留存前提已经变了 "+
			"⇒ ★ 本门必须同步更新（且 §143.66 的「不可删列」结论需重新评估）", builderPath14366)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ④ 自检：判据 ① 不是恒真
// ─────────────────────────────────────────────────────────────────────────────

// TestSection14366WriteGateIsNotConstant 用**反例源码**验证判据 ① 会红。
//
// ★ 纪律：一道只断言异常形态的门，必须配一条「正常形态必须通过 + 反例必须失败」的对照，
//
//	否则它可能恒绿（§143.65 已登记）。
func TestSection14366WriteGateIsNotConstant(t *testing.T) {
	const counterExample = `package v2

import "github.com/kaixuan/llm-gateway-go/settings"

func bad(rec BodiesRecord, req Req) {
	// 反例：开关分支里【没有】把 OutboundBody 置 nil ⇒ 判据 ① 必须失败
	if settings.GetPlatformBool("storage.session_final_full_enabled", false) {
		_ = rec
	}
}
`
	const goodExample = `package v2

import "github.com/kaixuan/llm-gateway-go/settings"

func good(rec BodiesRecord, req Req) {
	if settings.GetPlatformBool("storage.session_final_full_enabled", false) {
		rec.OutboundBody = nil
	}
}
`
	judge := func(src string) (found bool) {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, "counter_example.go", src, parser.ParseComments)
		if err != nil {
			t.Fatalf("解析反例失败：%v", err)
		}
		for _, ifs := range ifBodiesWithSwitch14366(f, e1SwitchKey14366, e1SwitchFn14366) {
			if hasNilAssignToField14366(ifs.Body.List, nilAssignField14366) {
				found = true
			}
		}
		return found
	}

	if judge(counterExample) {
		t.Errorf("反例（开关分支里没有 `%s = nil`）竟被判为通过 ⇒ ★ 判据 ① 是恒真的 ⇒ 门没有守卫力", nilAssignField14366)
	}
	if !judge(goodExample) {
		t.Errorf("正例（开关分支里有 `%s = nil`）竟被判为失败 ⇒ ★ 判据 ① 判反了", nilAssignField14366)
	}
}
