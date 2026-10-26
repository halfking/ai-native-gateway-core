package ursm_test

// §143.68 的门：把「turn 与 bodies 必须同事务、且 bodies 写入不得被条件化」
// 钉成**结构判据**，并把「分区键的日历口径错位」钉成**必须被登记**的事实。
//
// ★ 为什么这道门是本轮的第一产物（§143.68 实测推导）：
//
//	§143.67 判定「2026-09 的 159,831 个会话正文已被删除」；§143.68 复核后
//	**把这个归因推翻了**（正文是「**从未被持久保存**」，不是「被删」），
//	但**留下一个更要紧的问题**：
//
//	★ 9 月的 turn 表有 794,529 行 / 159,834 个会话，而 9 月的 bodies 表**一行都没有**
//	  ⇒ ⇒ **「turn 有、bodies 无」= 孤儿 turn**；
//	★ 而当前代码里 turn 与 bodies 写在**同一个 tx**（`turnWriter.BeginTx` →
//	  `AppendTurnInTx` + `WriteBodiesInTx(tx, …)`）⇒ **当前代码不可能产生孤儿 turn**；
//	★ ⇒ ⇒ 孤儿 turn 的成因只能是「**当时那条 bodies 写入路径不存在或被条件跳过**」。
//
//	⇒ 所以本门钉的正是**那件事**：bodies 写入一旦被塞进任何条件分支，
//	  「同事务」这条不变式就会被静默打破（写成孤儿 turn），而且**不会报错**。
//
// ★ 两条纪律的落点：
//   ① 「门要查结构、不能只查字符串在不在」⇒ 本门用 go/ast 解析真实语法树；
//   ② 「一道只断言异常形态的门，必须配一条『正常形态必须通过 + 反例必须失败』的对照」
//      ⇒ 判据 ③ 用**合成源码**自证判据 ② 不是恒真。
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
	writerPath14368        = "domains/session/v2/session_writer_v2.go"
	partitionDatePath14368 = "domains/session/v2/partition_date.go"
	turnAppendCall14368    = "appendTurnInLockedTx"
	bodiesWriteCall14368   = "WriteBodiesInTx"
	fixmeMarker14368       = "FIXME-14368"
)

func repoRoot14368(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func parseFile14368(t *testing.T, rel string) (*token.FileSet, *ast.File) {
	t.Helper()
	path := filepath.Join(repoRoot14368(t), rel)
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

// findSessionWrite14368 返回 `SessionWriterV2.Write` 方法声明。
func findSessionWrite14368(f *ast.File) *ast.FuncDecl {
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || len(fn.Recv.List) == 0 {
			continue
		}
		if fn.Name.Name == "Write" {
			return fn
		}
	}
	return nil
}

// txVarName14368 在函数体里找「由 BeginTx 赋出的那个 tx 变量名」。
//
// ★ 为什么不用「按参数位置取第 2 个实参」：调用形态一变（换参数顺序、换 tx 载体）
//
//	位置假设就静默错位 ⇒ 那种情况下门会「红得不对」或「绿得不对」。
//	⇒ 改为**先找到 tx 变量本身**，再断言两个调用都把它当实参传下去。
func txVarName14368(fn *ast.FuncDecl) string {
	name := ""
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for _, lhs := range as.Lhs {
			id, ok := lhs.(*ast.Ident)
			if !ok {
				continue
			}
			// tx, err := ... / var tx ... = ...
			if id.Name == "tx" {
				name = id.Name
				return false
			}
		}
		return true
	})
	return name
}

// callDepth14368 找出名为 name 的调用，返回它的**最小条件深度**、
// 是否找到、以及它的实参里是否出现了 tx 变量。
//
// ★★★ 判别的精确定义（本门最关键的一处）：
//
//	「条件深度」= 该调用**出现在某个 if / for / range / switch 的 Body 内**的层数。
//	而 `if err := w.bodiesWriter.WriteBodiesInTx(ctx, tx, rec); err != nil { return err }`
//	里的调用位于 **IfStmt.Init**（不在 Body 内）⇒ **深度 = 0**。
//
//	⇒ 这样区分的正是两种形态：
//	  · 错误检查（Init）⇒ 深度 0 ⇒ **不是条件化** ✔
//	  · 业务条件（Body）⇒ 深度 ≥1 ⇒ **是条件化** ✘（孤儿 turn 的成因形态）
//
//	⇒ ★ 若把 Init 也算进条件深度，本门就会对**每一个正常调用**判红
//	  ⇒ 那种门等于没有门 ⇒ 故必须精确区分。
func callDepth14368(stmts []ast.Stmt, name, txVar string) (minDepth int, found bool, hasTx bool) {
	minDepth = -1

	inspectCall := func(call *ast.CallExpr, depth int) {
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != name {
			return
		}
		if minDepth < 0 || depth < minDepth {
			minDepth = depth
		}
		found = true
		for _, a := range call.Args {
			if id, ok := a.(*ast.Ident); ok && id.Name == txVar {
				hasTx = true
			}
		}
	}

	var walk func(list []ast.Stmt, depth int)
	walk = func(list []ast.Stmt, depth int) {
		for _, st := range list {
			switch s := st.(type) {
			case *ast.IfStmt:
				// Init（例如 `if err := f(...); err != nil {`）属于当前层 ⇒ 深度不加。
				// ★ Init 的语法有两种形态：`if err := f(...); …` 是 AssignStmt，
				//   `if f(...)` 是 ExprStmt ⇒ 两种都要收，否则**正常的错误检查会被漏掉**
				//   ⇒ 门会对每个正常调用判「找不到」（量具故障，不是被测物失败）。
				if s.Init != nil {
					ast.Inspect(s.Init, func(n ast.Node) bool {
						if call, ok := n.(*ast.CallExpr); ok {
							inspectCall(call, depth)
						}
						return true
					})
				}
				walk(s.Body.List, depth+1)
				switch e := s.Else.(type) {
				case *ast.BlockStmt:
					walk(e.List, depth+1)
				case *ast.IfStmt:
					walk([]ast.Stmt{e}, depth+1)
				}
			case *ast.ForStmt:
				walk(s.Body.List, depth+1)
			case *ast.RangeStmt:
				walk(s.Body.List, depth+1)
			case *ast.SwitchStmt:
				for _, c := range s.Body.List {
					if cc, ok := c.(*ast.CaseClause); ok {
						walk(cc.Body, depth+1)
					}
				}
			case *ast.TypeSwitchStmt:
				for _, c := range s.Body.List {
					if cc, ok := c.(*ast.CaseClause); ok {
						walk(cc.Body, depth+1)
					}
				}
			case *ast.ExprStmt:
				if call, ok := s.X.(*ast.CallExpr); ok {
					inspectCall(call, depth)
				}
			default:
				// ★ 其它语句形态（AssignStmt / ReturnStmt / DeferStmt …）统一用
				//   ast.Inspect 收调用。
				//   ★ 为什么必须收 AssignStmt：生产代码里的 turn 写入是
				//   `turnNo, err := w.turnWriter.appendTurnInLockedTx(lockCtx, tx, turnRec)`
				//   ⇒ 若只收 ExprStmt，**每个正常调用都会被漏掉** ⇒ 门会误报
				//   「找不到调用」（= 量具故障，不是被测物失败）。
				ast.Inspect(st, func(n ast.Node) bool {
					if call, ok := n.(*ast.CallExpr); ok {
						inspectCall(call, depth)
					}
					return true
				})
			}
		}
	}
	walk(stmts, 0)
	return minDepth, found, hasTx
}

// ─────────────────────────────────────────────────────────────────────────────
// ① 同事务：turn 写入与 bodies 写入必须共用同一个 tx
// ─────────────────────────────────────────────────────────────────────────────

// TestSection14368TurnAndBodiesShareOneTransaction 是同事务门。
//
// ★ 依据（§143.68 实测）：当前代码里 turn 的 INSERT 与 bodies 的 INSERT 在
//
//	**同一个事务**里（`w.turnWriter.BeginTx` ⇒ `AppendTurnInTx(tx, …)` +
//	`WriteBodiesInTx(tx, …)`）⇒ 任一失败整体回滚 ⇒ **不可能产生孤儿 turn**。
//
// ⇒ ⇒ 而 §143.67 实测到 9 月存在 **159,830 个「turn 有、bodies 无」的会话**
//
//	⇒ ⇒ 孤儿 turn 只能来自「那条 bodies 写入路径当时不存在/被条件跳过」。
//
// ⇒ ⇒ 本门把「同事务」钉住，防止将来有人把 bodies 写入挪出事务或换成别的连接。
func TestSection14368TurnAndBodiesShareOneTransaction(t *testing.T) {
	_, f := parseFile14368(t, writerPath14368)
	fn := findSessionWrite14368(f)
	if fn == nil {
		t.Fatalf("%s：找不到 `SessionWriterV2.Write` ⇒ ★ 本门的判据对象漂移了 ⇒ "+
			"请同步本门（不要直接删判据）", writerPath14368)
	}
	txVar := txVarName14368(fn)
	if txVar == "" {
		t.Fatalf("%s：`Write` 里找不到 tx 变量 ⇒ ★ 本门的判据对象漂移了 ⇒ 请同步本门", writerPath14368)
	}

	_, turnFound, turnHasTx := callDepth14368(fn.Body.List, turnAppendCall14368, txVar)
	if !turnFound {
		t.Fatalf("%s：`Write` 里找不到 `%s` 调用 ⇒ ★ turn 写入路径变了 ⇒ 请同步本门",
			writerPath14368, turnAppendCall14368)
	}
	if !turnHasTx {
		t.Errorf("%s：`%s` 调用没有把 tx 变量（%q）当实参 ⇒ ★ turn 写入不在那个共享事务里",
			writerPath14368, turnAppendCall14368, txVar)
	}

	_, bodiesFound, bodiesHasTx := callDepth14368(fn.Body.List, bodiesWriteCall14368, txVar)
	if !bodiesFound {
		t.Fatalf("%s：`Write` 里找不到 `%s` 调用 ⇒ ★ bodies 写入路径没了 ⇒ "+
			"★ 请确认这是有意删除（若是，请同步本门并更新 §143.68 的结论）",
			writerPath14368, bodiesWriteCall14368)
	}
	if !bodiesHasTx {
		t.Errorf("%s：`%s` 调用没有把 tx 变量（%q）当实参 ⇒ ★★ bodies 写入**不在** turn 的事务里 "+
			"⇒ ⇒ 「turn 与 bodies 同事务 ⇒ 无孤儿 turn」这条不变式已被打破 "+
			"⇒ ⇒ 会静默产生孤儿 turn（正是 §143.67 观测到的 9 月形态）",
			writerPath14368, bodiesWriteCall14368, txVar)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ② 无条件：bodies 写入不得位于任何条件分支的 Body 内
// ─────────────────────────────────────────────────────────────────────────────

// TestSection14368BodiesWriteIsUnconditional 是「不得条件化」门。
//
// ★ 依据：孤儿 turn（turn 有、bodies 无）只有在「bodies 写入可能被跳过」时才会出现。
//
//	而 §143.68 实测到 9 月有 159,830 个这样的会话 ⇒ ⇒ **条件化 bodies 写入**
//	就是孤儿 turn 的结构性成因形态 ⇒ 本门把它钉住。
//
// ★ ★ 注意判别精度（见 callDepth14368 的注释）：错误检查
//
//	`if err := w.bodiesWriter.WriteBodiesInTx(...); err != nil { … }`
//	里的调用在 **IfStmt.Init**，**深度 = 0** ⇒ **不算条件化** ✔
//	⇒ 若把 Init 也算成条件深度，本门会对每一个正常调用判红 ⇒ 那种门等于没有门。
func TestSection14368BodiesWriteIsUnconditional(t *testing.T) {
	_, f := parseFile14368(t, writerPath14368)
	fn := findSessionWrite14368(f)
	if fn == nil {
		t.Fatalf("%s：找不到 `SessionWriterV2.Write` ⇒ 请同步本门", writerPath14368)
	}
	txVar := txVarName14368(fn)

	depth, found, _ := callDepth14368(fn.Body.List, bodiesWriteCall14368, txVar)
	if !found {
		t.Fatalf("%s：`Write` 里找不到 `%s` 调用 ⇒ 请同步本门", writerPath14368, bodiesWriteCall14368)
	}
	if depth > 0 {
		t.Errorf("%s：`%s` 调用出现在条件分支内（条件深度 %d）⇒ ★★ bodies 写入**可能被跳过** "+
			"⇒ ⇒ 会静默产生「turn 有、bodies 无」的孤儿 turn "+
			"⇒ ⇒ §143.67 观测到的 9 月形态（159,830 个会话）就会复发 "+
			"⇒ ⇒ 若这是有意为之（例如按灰度开关只写 turn），请在本行写明理由并同步 §143.68 的结论",
			writerPath14368, bodiesWriteCall14368, depth)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ③ 自检：判据 ② 不是恒真（合成源码：条件化 ⇒ 判红；正常 ⇒ 判绿）
// ─────────────────────────────────────────────────────────────────────────────

// TestSection14368ConditionalDepthJudgeIsNotConstant 用**合成源码**自证判据 ② 有牙。
func TestSection14368ConditionalDepthJudgeIsNotConstant(t *testing.T) {
	const counterExample = `package v2

func bad(w BodiesWriter, ctx context.Context, tx pgx.Tx, rec BodiesRecord) {
	if someCondition {
		_ = w.WriteBodiesInTx(ctx, tx, rec)
	}
}
`
	const goodExample = `package v2

func good(w BodiesWriter, ctx context.Context, tx pgx.Tx, rec BodiesRecord) {
	// 正常形态：错误检查在 IfStmt.Init ⇒ 条件深度 0
	if err := w.WriteBodiesInTx(ctx, tx, rec); err != nil {
		return err
	}
}
`
	judge := func(src string) int {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, "synthetic.go", src, parser.ParseComments)
		if err != nil {
			t.Fatalf("解析合成源码失败：%v", err)
		}
		depth, found, _ := callDepth14368(f.Decls[0].(*ast.FuncDecl).Body.List, bodiesWriteCall14368, "tx")
		if !found {
			return -1
		}
		return depth
	}

	if d := judge(counterExample); d <= 0 {
		t.Errorf("反例（bodies 写入在 if Body 内）竟被判为深度 %d ⇒ ★ 判据 ② 是恒真的 ⇒ 门没有守卫力", d)
	}
	if d := judge(goodExample); d != 0 {
		t.Errorf("正例（bodies 写入在 IfStmt.Init 的错误检查里）被判为深度 %d ⇒ ★ 判别精度有误 "+
			"⇒ 判据把「错误检查」误判成「条件化」⇒ 本门会对正常代码判红", d)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ④ 分区键的日历口径错位：必须被登记，不许无声存在
// ─────────────────────────────────────────────────────────────────────────────

// TestSection14368PartitionDateCalendarOffsetIsRegistered 把「partition_date 的
// 日历口径与分区正典错位 8 小时」钉成**必须被登记**的事实。
//
// ★ 依据（§143.68 实测，252）：
//
//	· `domains/session/v2/partition_date.go` 的 `calendarDate()` 取的是 **UTC 日历日**；
//	· 而分区正典是 **Asia/Shanghai 零点**（811 / 694 头注「生产分区边界 = Asia/Shanghai 零点」）；
//	· ⇒ **每天 00:00~08:00 (+08) 的数据被划到「前一天」** ⇒
//	  **每月 1 日 00:00~08:00 (+08) 的数据落进上月分区**
//	  （实测：session_bodies_2026_09 里正是 +08 的 2026-10-01 00:00~07:59 那 3,749 行，
//	  它们的 partition_date 全是 2026-09-30）。
//
// ⇒ ⇒ 三条后果：① 「某月」的语义错位 8 小时；② 保留期判定（841 retain_months）
//
//	把边界数据算到上月；③ 按 +08 日历日的查询会漏掉边界那 8 小时（它们在上月分区里）。
//
// ★ 处置口径（承「正确实现但是死代码 / 有缺陷的实现 ⇒ 钉住现状 + FIXME + 反向断言」范式）：
//
//	**本轮只登记、不改行为** —— 改口径不是一行改动：存量行的 partition_date 需要搬家，
//	否则同一日历月会分裂在两个分区里。⇒ ⇒ 本门允许两种状态通过：
//	  (a) 已修（实现不再是 UTC 日历日）；(b) 仍是 UTC 日历日 **且** 源文件里有登记该错位的 FIXME。
//	⇒ ⇒ 「不许无声存在」：既不许无声改口径，也不许无声忽略它。
func TestSection14368PartitionDateCalendarOffsetIsRegistered(t *testing.T) {
	path := filepath.Join(repoRoot14368(t), partitionDatePath14368)
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读 %s 失败：%v", partitionDatePath14368, err)
	}
	text := string(src)

	if !strings.Contains(text, "func calendarDate") {
		t.Fatalf("%s：找不到 `calendarDate` ⇒ ★ 本门的判据对象漂移了 ⇒ 请同步本门", partitionDatePath14368)
	}

	usesUTC := strings.Contains(text, "t.UTC()") || strings.Contains(text, "u := t.UTC()")
	if !usesUTC {
		// 已修（或换了实现）⇒ 不再要求 FIXME 登记
		return
	}

	if !strings.Contains(text, fixmeMarker14368) {
		t.Errorf("%s：`calendarDate` 仍取 **UTC 日历日**，但源文件里没有 `%s` 登记 "+
			"⇒ ★★ 该口径与分区正典（Asia/Shanghai 零点）错位 8 小时（每天 00:00~08:00 的数据落前一天，"+
			"每月 1 日凌晨落上月分区）⇒ ⇒ 请在本文件里登记该事实（改口径需配套数据搬家，不能顺手改）",
			partitionDatePath14368, fixmeMarker14368)
		return
	}
	if !strings.Contains(text, "Asia/Shanghai") {
		t.Errorf("%s：`%s` 存在但没有提到 Asia/Shanghai ⇒ ★ 登记内容不完整 "+
			"⇒ 请写清「正典是 Asia/Shanghai 零点、当前实现是 UTC 日历日、后果与改法」",
			partitionDatePath14368, fixmeMarker14368)
	}
}
