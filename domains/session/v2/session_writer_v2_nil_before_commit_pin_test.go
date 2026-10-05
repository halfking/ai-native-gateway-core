package v2

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// 审计 §9.249.3 / §9.249.4：把「Write 在事务提交前不可能返回 nil」钉在源码上。
//
// 背景（生产 252，request a9e2dc006a62103b087999517bf765bd，2026-10-05 13:34）：
//   - session_dim 被写了（该表运行期唯一写方 = hook.go:234 的 UpsertSessionDim）
//     ⇒ 镜像 hook 进了 runShadowWrite；
//   - runShadowWrite 里维度 upsert 与 w.Write 之间**没有分支** ⇒ w.Write 被调用；
//   - 但两张 turn 面都没有该 request_id，session_aggregate_outbox 也没有该会话的行
//     （对照：同窗口 5 个成功请求每个 agg_outbox=1/sessions=1/turns=1，
//     且该表 49,406 行、最早 09-28 ⇒ **不是自消队列**，「0 行」是有效证据）
//     ⇒ 事务没有提交；
//   - 而 journalctl 全文 grep 该 request_id 为 0 命中，
//     llm_gateway_shadow_write_failed_total{kind="session_v2"}=2 与当前进程期内的
//     2 条 "V2 shadow write failed" 逐条吻合 ⇒ w.Write **也没有返回 error**。
//
// ⇒ 观测到的是「既没成功、也没报错」。按当时读到的源码这不可能：
// Write 的 11 个 return 全部返回 error，commit 之前没有任何 return nil。
// 而 go version -m 显示生产二进制是 vcs.modified=true 的脏树构建，
// **运行代码无法被验证等同于本文件**——所以这条判据的作用不是复现那个现象，
// 而是**在代码层先立一道防线**：将来任何人（或任何一次 rebase）引入一条
// 「提交前返回 nil」或「返回 (0, nil)」的路径，本门立刻转红，而不是等生产上
// 27 小时才 1 行的样本去发现。
//
// 为什么用 AST 而不是文本：文本判据会被**注释**满足，而「删掉接线只留注释」
// 是判据静默失效最常见的形态（见 domains/streaming/sanitize_auth_literal_pin_test.go
// 顶部的同一说明）。AST 看不见注释。
func TestWriteNeverReturnsNilBeforeCommit(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "session_writer_v2.go", nil, 0)
	if err != nil {
		t.Fatalf("parse session_writer_v2.go: %v", err)
	}

	fn := findFunc(t, file, "SessionWriterV2", "Write")

	// 结果类型必须只有一个返回值，否则「返回 nil」这件事本身不成立，
	// 判据的前提会悄悄消失。
	if fn.Type.Results == nil || len(fn.Type.Results.List) != 1 {
		t.Fatalf("Write 的签名不再是单返回值 error（Results=%v）—— "+
			"「提交前不得返回 nil」这条判据的前提已变，请连同本测试一起重新评估",
			fn.Type.Results)
	}

	// 定位本函数里**第一个** tx.Commit。它是「持久化已发生」的分界线。
	var commitPos token.Pos
	commits := 0
	ast.Inspect(fn, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Commit" {
			return true
		}
		if x, ok := sel.X.(*ast.Ident); !ok || x.Name != "tx" {
			return true
		}
		commits++
		if commitPos == token.NoPos || call.Pos() < commitPos {
			commitPos = call.Pos()
		}
		return true
	})
	if commits == 0 {
		t.Fatal("Write 里找不到 tx.Commit —— 判据失效（提交点被改名或移出本函数）。" +
			"在判据重新落地之前，「Write 提交前不返回 nil」这个不变量**无人看守**")
	}

	// 收集 commit 之前的所有 return。位置比较用 fset，否则跨文件 token.Pos 不可比。
	preCommitReturns := 0
	ast.Inspect(fn, func(n ast.Node) bool {
		ret, ok := n.(*ast.ReturnStmt)
		if !ok || ret.Pos() > commitPos {
			return true
		}
		preCommitReturns++
		if len(ret.Results) == 0 {
			t.Errorf("Write 在 tx.Commit 之前有一个**无值** return（第 %d 行）—— "+
				"提交前任何 return 都必须携带 error", fset.Position(ret.Pos()).Line)
			return true
		}
		if ident, ok := ret.Results[0].(*ast.Ident); ok && ident.Name == "nil" {
			t.Errorf("Write 在 tx.Commit 之前返回了 nil（第 %d 行）—— "+
				"这正是 §9.249.2 在生产上观测到的「既没成功也没报错」："+
				"调用方会认为写成功了，于是既不记日志也不登记 session_mirror_outbox，"+
				"该 request 永久没有 turn、没有任何可重放的痕迹",
				fset.Position(ret.Pos()).Line)
		}
		return true
	})

	// 反向守卫：commit 之前一个 return 都没有，说明函数被重构成了别的形状，
	// 此时「没找到违规 return」是**空洞通过**，不是通过。
	if preCommitReturns == 0 {
		t.Fatalf("Write 在 tx.Commit 之前没有任何 return 语句（%d 个 tx.Commit）—— "+
			"函数形状已变，本判据退化成恒真。请重新评估", commits)
	}

	t.Logf("Write：%d 个 tx.Commit，commit 之前 %d 个 return，全部携带 error —— "+
		"「提交前不返回 nil」成立", commits, preCommitReturns)
}

// TestAppendTurnInLockedTxNeverReturnsZeroNil 钉住更深一层的不变量。
//
// §9.249.3 的排除依赖这一点：appendTurnInLockedTx 的 8 个 return 里，
// `RowsAffected()==0` 分支**必须**在 session_turns_with_current_month 查到行
// （查不到会 `return 0, fmt.Errorf("read existing turn_no: %w", err)`），
// 而该视图 = session_turns_hot ∪ session_turns，正好覆盖 S4 门检查的两面
// ⇒ 「返回 (0, nil)」就等于「告诉调用方写成功了，但库里没有 turn」。
func TestAppendTurnInLockedTxNeverReturnsZeroNil(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "turn_writer.go", nil, 0)
	if err != nil {
		t.Fatalf("parse turn_writer.go: %v", err)
	}

	fn := findFunc(t, file, "TurnWriter", "appendTurnInLockedTx")

	returns := 0
	ast.Inspect(fn, func(n ast.Node) bool {
		ret, ok := n.(*ast.ReturnStmt)
		if !ok {
			return true
		}
		returns++
		if len(ret.Results) != 2 {
			// 该函数返回 (int, error)，非二元返回不是本判据关心的形态。
			return true
		}
		lit, ok := ret.Results[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.INT || lit.Value != "0" {
			return true
		}
		if ident, ok := ret.Results[1].(*ast.Ident); ok && ident.Name == "nil" {
			t.Errorf("appendTurnInLockedTx 返回了 (0, nil)（第 %d 行）—— "+
				"turn_no=0 且无错误会让 Write 继续往下写并最终提交，"+
				"但库里没有这一轮 turn：对账侧看到的是「有 turn_no、无 turn 行」",
				fset.Position(ret.Pos()).Line)
		}
		return true
	})

	if returns == 0 {
		t.Fatal("appendTurnInLockedTx 里没有找到任何 return —— 判据失效（函数被改名或删除）")
	}

	t.Logf("appendTurnInLockedTx：%d 个 return，无 (0, nil) —— 成立", returns)
}

// TestWriteActuallyInsertsTheTurn 堵住前两条判据的**实测盲区**。
//
// 变异 M6 实测发现：把
//
//	turnNo, err := w.turnWriter.appendTurnInLockedTx(lockCtx, tx, turnRec)
//
// 整段替换成 `turnNo, err := 1, error(nil)`（即「跳过 turn 插入，但不报错」），
// 前两条判据**都判绿**。而那恰恰是 §9.249.2 在生产上观测到的症状形状：
// 事务提交、调用方拿到 nil、库里没有这一轮 turn。
//
// ⇒ 前两条是**必要条件**，不是充分条件。本条补上充分性的一半：
// `Write` 必须真的调用 turn 插入，并且**检查它的错误**。
func TestWriteActuallyInsertsTheTurn(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "session_writer_v2.go", nil, 0)
	if err != nil {
		t.Fatalf("parse session_writer_v2.go: %v", err)
	}

	fn := findFunc(t, file, "SessionWriterV2", "Write")

	// 1) 必须存在 turn 插入调用。
	var callPos token.Pos
	calls := 0
	ast.Inspect(fn, func(n ast.Node) bool {
		ce, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := ce.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "appendTurnInLockedTx" {
			return true
		}
		calls++
		callPos = ce.Pos()
		return true
	})
	if calls == 0 {
		t.Fatalf("Write 里没有调用 appendTurnInLockedTx（第 %d 行起）—— "+
			"turn 插入被跳过了，调用方却会拿到 nil，"+
			"这与 §9.249.2 生产上那 1 行「既没成功也没报错」完全同形", calls)
	}

	// 2) 该调用必须把 error 绑到一个具名变量上。
	var errName string
	ast.Inspect(fn, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok || len(as.Rhs) != 1 || len(as.Lhs) != 2 {
			return true
		}
		if !containsCall(as.Rhs[0], "appendTurnInLockedTx") {
			return true
		}
		if id, ok := as.Lhs[1].(*ast.Ident); ok {
			errName = id.Name
		}
		return true
	})
	if errName == "" {
		t.Fatalf("appendTurnInLockedTx 的返回值没有被赋给 (turnNo, err) 形式 —— "+
			"判据失效：无法确认错误被检查（调用点第 %d 行）", fset.Position(callPos).Line)
	}

	// 3) 之后必须存在针对该 error 的 != nil 检查，且检查体内有 return。
	checked := false
	ast.Inspect(fn, func(n ast.Node) bool {
		ifs, ok := n.(*ast.IfStmt)
		if !ok || ifs.Pos() < callPos {
			return true
		}
		if !isNilCheckOn(ifs.Cond, errName) {
			return true
		}
		ast.Inspect(ifs.Body, func(m ast.Node) bool {
			if _, ok := m.(*ast.ReturnStmt); ok {
				checked = true
			}
			return true
		})
		return true
	})
	if !checked {
		t.Errorf("appendTurnInLockedTx 的 %s 没有被 `if %s != nil { … return … }` 检查（调用点第 %d 行）—— "+
			"turn 插入失败会一路走到 tx.Commit，提交一个没有这一轮的事务",
			errName, errName, fset.Position(callPos).Line)
	}

	t.Logf("Write：调用 appendTurnInLockedTx %d 次，错误绑定到 %q 且被 != nil 检查并 return —— 成立",
		calls, errName)
}

func containsCall(e ast.Expr, selName string) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		ce, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if sel, ok := ce.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == selName {
			found = true
		}
		return true
	})
	return found
}

func isNilCheckOn(cond ast.Expr, name string) bool {
	found := false
	ast.Inspect(cond, func(n ast.Node) bool {
		be, ok := n.(*ast.BinaryExpr)
		if !ok || be.Op != token.NEQ {
			return true
		}
		if id, ok := be.Y.(*ast.Ident); ok && id.Name == "nil" {
			if lhs, ok := be.X.(*ast.Ident); ok && lhs.Name == name {
				found = true
			}
		}
		return true
	})
	return found
}

func findFunc(t *testing.T, file *ast.File, recvType, name string) *ast.FuncDecl {
	t.Helper()
	for _, decl := range file.Decls {
		d, ok := decl.(*ast.FuncDecl)
		if !ok || d.Name.Name != name || d.Recv == nil || len(d.Recv.List) == 0 {
			continue
		}
		if recvTypeOf(d.Recv.List[0].Type) == recvType {
			return d
		}
	}
	t.Fatalf("%s 里找不到方法 (*%s).%s —— 判据失效（函数被改名或删除）", file.Name.Name, recvType, name)
	return nil
}

func recvTypeOf(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return recvTypeOf(t.X)
	case *ast.Ident:
		return t.Name
	}
	return ""
}
