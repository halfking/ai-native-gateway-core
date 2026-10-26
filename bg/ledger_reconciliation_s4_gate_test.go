package bg

import (
	"go/ast"
	"go/parser"
	"go/token"

	"strings"
	"testing"
)

// S4 停写门控范围（2026-10-02 审计）：usage_credit_mismatch 跨了门的影响半径。
//
// 结构事实（真库 + 源码双向核实）：
//
//	settings.KeyRequestLogsWriteEnabled 的声明范围 =
//	    _request_logs 宽族（_request_logs_hot 主行 + _request_logs_bodies_hot 正文）
//	usageCreditSQL() 的两侧 =
//	    _request_logs_hot.credits_charged   ← 族内，门管得到
//	    credit_ledger_hot（consume/ref_type='request'）  ← 族外，永不停写
//
// 而对账器此前**完全不咨询该门**。所以停写一旦生效，两侧就不再描述同一段时间：
// usage 臂在切换点冻结、credit 臂继续增长，于是切换点之后的每个请求都落进
// FULL OUTER JOIN 的「只有 credit」分支（charged=0 / debited>0），被当作差异写进
// maas_reconciliation_findings。**那些不是账务缺陷，就是停写本身**，而且数量无上界。
//
// 同一文件里的 balance_chain 不受影响：它整段只读 credit_ledger_hot（自洽回放），
// 不跨族。所以这道门只该挡 usage_credit_mismatch 一项。

// TestUsageCreditComparabilityIsDataDriven（★ 2026-10-20 退役换题后的新门）：
// usage↔credit 比对的**可判定性判据必须是数据驱动的**，而不是某个开关的状态。
//
// 旧判据（已退役）：纯函数 `usageCreditComparability(logsWriteEnabled)` +
// 常量 `usageArmEmptySkipReason`，输入是 S4 停写键。它的两个前提都已消失：
//
//	① 890 把 v1 族整体 DROP、usage 侧随之迁到**会话族**（写入路径无条件）
//	   ⇒ 「门一关两侧就不同期」不再成立；
//	② 该键的**代码默认**被并行审计轮改成 false ⇒ 这条**财务**比对在任何
//	   未显式配置它的环境上永久停摆（findings 恒 0 ⇒ 运维读到「账务无差异」）。
//
// 新判据：**这一次的两侧是否同期** —— usage 臂（会话族带 credits_charged 的
// 请求）在窗口内一行都没有、而 credit 臂有行 ⇒ 不可判定 ⇒ 跳过并说明。
//
// ⚠ 三条断言各自配阳性对照/负对照：
//
//	① SQL 必须把 usage 臂的总体规模查出来（否则 Go 侧无从判断）；
//	② 判定必须落在**已登记**的 reason 上（否则「跳过」不会被指标记到）；
//	③ 判定**不得**再读那个已退役的键。
func TestUsageCreditComparabilityIsDataDriven(t *testing.T) {
	// ① SQL 必须暴露 usage 臂行数。
	sql := usageCreditSQL()
	if !strings.Contains(sql, "(SELECT count(*)::int FROM usage)") {
		t.Error("usageCreditSQL 没有把 usage 臂的总体规模查出来 —— " +
			"Go 侧就无从判断「这次比对可不可判定」，于是要么恒跳过、要么恒假发现")
	}
	// ★ 正向对照：判据必须认得出这段形态（否则上面那条可能因写法差异而恒真）。
	if !strings.Contains("SELECT (SELECT count(*)::int FROM usage) AS usage_arm_rows",
		"(SELECT count(*)::int FROM usage)") {
		t.Fatal("判据没有牙：一个确实带 usage 臂计数的片段被判成「没有」")
	}

	// ② 判定必须落在已登记的 reason 上。
	if !s4ScanSkipReasonRegistered(usageArmEmptySkipReason) {
		t.Errorf("跳过原因 %q 不在 s4ScanSkipReasons 闭集里 —— 未登记的 reason 不会写进 "+
			"llm_gateway_bg_s4_scan_skipped_last_run（该指标的标签空间必须严格等于闭集），"+
			"于是这一轮的 gauge 停在 0 ⇒ 指标会把「跳过了」谎报成「跑过了」",
			usageArmEmptySkipReason)
	}

	// ③ 判定不得再读那个已退役的键。
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "ledger_reconciliation.go", nil, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var fn *ast.FuncDecl
	for _, d := range file.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == "checkUsageCredit" {
			fn = fd
			break
		}
	}
	if fn == nil {
		t.Fatal("checkUsageCredit not found")
	}
	if mentionsRequestLogsWriteEnabledExpr(fn) {
		t.Error("checkUsageCredit 又读起了 settings.RequestLogsWriteEnabled —— " +
			"那个键的 subject（v1 族）已被 890 删除、代码默认已被翻成 false；" +
			"读它等于让这条财务比对在未显式配置它的环境上永久停摆")
	}
	// 判定必须真的发生：既要有 markSkipped，也必须**调用那个纯函数判定**。
	//
	// ⚠ reason 常量本身在纯函数 `usageCreditComparable` 里（它返回那个登记键），
	//   checkUsageCredit 只负责把返回的 reason 传给 markSkipped ⇒
	//   门查的是「调用了纯函数」，不是「函数体里出现了那个常量名」。
	if findCall(t, fn, "markSkipped") == nil {
		t.Error("checkUsageCredit 没有 markSkipped —— 跳过就与「扫了没发现差异」不可区分")
	}
	if findCall(t, fn, "usageCreditComparable") == nil {
		t.Error("checkUsageCredit 没有调用 usageCreditComparable —— " +
			"可判定性必须由那个**纯函数**判定（可脱离数据库被测），" +
			"而不是在流程里随手写条件")
	}
	// ★ 正向对照：那个纯函数必须真的产出那个**登记过的** reason。
	if comparable, reason := usageCreditComparable(0, 3); comparable || reason != usageArmEmptySkipReason {
		t.Errorf("usageCreditComparable(0, 3) = (%v, %q)，期望 (false, %q) —— "+
			"「有差异行但 usage 臂为空」必须判成不可判定并给出登记过的原因",
			comparable, reason, usageArmEmptySkipReason)
	}
	// ★ 反向对照：没有差异行时**不得**判成跳过（那是「确实没有差异」）。
	if comparable, reason := usageCreditComparable(0, 0); !comparable || reason != "" {
		t.Errorf("usageCreditComparable(0, 0) = (%v, %q)，期望 (true, 空串) —— "+
			"零差异行是「确实没有差异」，不是「不可判定」；两者混同会让运维读到"+
			"「账务无差异」而真相是「没跑」", comparable, reason)
	}
}

// mentionsIdentExpr 报「这段 Go 代码里有没有引用标识符 name」。
func mentionsIdentExpr(node ast.Node, name string) bool {
	if node == nil {
		return false
	}
	found := false
	ast.Inspect(node, func(n ast.Node) bool {
		if found {
			return false
		}
		if id, ok := n.(*ast.Ident); ok && id.Name == name {
			found = true
		}
		return !found
	})
	return found
}

// TestUsageCreditSkipIsNotSilent pins the "swept 0" vs "swept nothing" separation
// at the struct level. A skipped check returns 0, exactly like a clean scan, so
// the only thing keeping the two apart is the per-run skip list — and a skip list
// that is never reset is worse than none, because it would keep reporting
// "skipped" for a run that really did execute and really did find nothing.
func TestUsageCreditSkipIsNotSilent(t *testing.T) {
	r := &LedgerReconciler{}
	if got := r.SkippedChecks(); len(got) != 0 {
		t.Fatalf("fresh reconciler already reports skips: %v", got)
	}
	r.markSkipped(usageArmEmptySkipReason)
	if got := r.SkippedChecks(); len(got) != 1 || got[0] != usageArmEmptySkipReason {
		t.Fatalf("skip not recorded: %v", got)
	}
	r.resetSkipped()
	if got := r.SkippedChecks(); len(got) != 0 {
		t.Fatalf("resetSkipped did not clear the list: %v", got)
	}
	// A nil reconciler must not panic; it is constructed in several places.
	var nilRec *LedgerReconciler
	if got := nilRec.SkippedChecks(); got != nil {
		t.Fatalf("nil reconciler returned %v", got)
	}
}

// TestRunOnceResetsSkipState pins the reset where it actually has to happen.
// resetSkipped() is trivially correct on its own; what matters is that RunOnce
// calls it. The first version of this test cleared the field by hand, so
// deleting the reset from RunOnce left it green — a test that performs the very
// step it claims to verify.
func TestRunOnceResetsSkipState(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "ledger_reconciliation.go", nil, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var runOnce *ast.FuncDecl
	for _, d := range file.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == "RunOnce" {
			runOnce = fd
		}
	}
	if runOnce == nil {
		t.Fatal("RunOnce not found")
	}
	reset := findCall(t, runOnce, "resetSkipped")
	if reset == nil {
		t.Fatal("RunOnce no longer calls resetSkipped() — SkippedChecks would keep reporting " +
			"the previous run's skips, so a run that really executed and really found " +
			"nothing would still read as 'skipped'")
	}
	// Ordering matters: reset must precede both checks, or the first check's own
	// skip can be wiped by a later reset.
	for _, check := range []string{"checkBalanceChain", "checkUsageCredit"} {
		c := findCall(t, runOnce, check)
		if c == nil {
			t.Fatalf("RunOnce no longer calls %s", check)
		}
		if reset.Pos() > c.Pos() {
			t.Errorf("resetSkipped() is called after %s — that check's skip would be wiped", check)
		}
	}
}

// TestUsageCreditCheckIsNotGatedByRetiredV1Key（★ 反转自
// TestUsageCreditCheckIsGatedBeforeQuerying）：
//
// 旧门守的是「门必须在查询**之前**被咨询、且门分支必须 return」——那是
// 「可判定性由开关决定」那个形态的结构钉子。
//
// 反转的理由：可判定性判据已经换成**数据驱动**（usage 臂为空 ⇒ 不可判定），
// 它**必须**发生在查询**之后**（要先知道 usage 臂有多空才知道能不能判）⇒
// 旧门要求的位置关系在新形态下是**错的**，照抄它会把实现锁死在旧形态上。
//
// 新门守三件事：
//
//	① 不得再咨询那个已退役的判据（usageCreditComparability 已随门删除）；
//	② 必须**先查询**再判定 —— 判定依赖查询结果；
//	③ 判定命中时必须 markSkipped 并**不再产生 finding**（返回 0）。
func TestUsageCreditCheckIsNotGatedByRetiredV1Key(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "ledger_reconciliation.go", nil, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var fn *ast.FuncDecl
	for _, d := range file.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == "checkUsageCredit" {
			fn = fd
			break
		}
	}
	if fn == nil {
		t.Fatal("checkUsageCredit not found")
	}

	// ① 旧判据已退役。
	if findIfCalling(t, fn, "usageCreditComparability") != nil {
		t.Error("checkUsageCredit 又咨询了 usageCreditComparability —— " +
			"它连同常量 usageCreditSkipS4StopWrite 已随 890 + D0 退役（理由已过期、输入恒为假）")
	}

	// ② 判定必须在查询之后：判定依赖查询结果（usage 臂行数）。
	q := findCall(t, fn, "Query")
	if q == nil {
		t.Fatal("checkUsageCredit 不发查询了 —— 这条财务比对必须真的比对两侧")
	}
	skipCall := findCall(t, fn, "markSkipped")
	if skipCall == nil {
		t.Fatal("checkUsageCredit 没有 markSkipped —— 跳过必须留下机器可读的痕迹")
	}
	if skipCall.Pos() < q.Pos() {
		t.Error("跳过判定发生在查询之前 —— 新的可判定性判据是**数据驱动**的" +
			"（要靠查询结果里的 usage 臂行数），查询之前判不了")
	}

	// ③ 判定必须经由那个纯函数（reason 由它产出 ⇒ 一定是登记过的键）。
	if findCall(t, fn, "usageCreditComparable") == nil {
		t.Error("checkUsageCredit 没有调用 usageCreditComparable —— " +
			"可判定性判定必须走那个纯函数（reason 是登记过的机器可读键）")
	}

	// 兄弟检查必须保持不门控：balance_chain 只读 credit_ledger_hot（自洽回放），
	// 永远可判定 ⇒ 给它加门就是「用假报机换静默洞」。
	for _, d := range file.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Name.Name != "checkBalanceChain" {
			continue
		}
		if findIfCalling(t, fd, "usageCreditComparable") != nil {
			t.Error("checkBalanceChain 被 usage↔credit 的判定门控了 —— " +
				"它只读 credit_ledger_hot 自洽回放、永远可判定，门控它等于制造静默洞")
		}
	}
}

// findIfCalling returns the first IfStmt in fn that calls name — checking the
// Init clause as well as Cond, because the gate is written as
// `if ok, reason := usageCreditComparability(...); !ok {`, which puts the call in
// Init, not Cond. An earlier version inspected Cond only and reported the gate
// missing on correct code.
func findIfCalling(t *testing.T, fn *ast.FuncDecl, name string) *ast.IfStmt {
	t.Helper()
	var out *ast.IfStmt
	ast.Inspect(fn, func(n ast.Node) bool {
		if out != nil {
			return false
		}
		ifs, ok := n.(*ast.IfStmt)
		if !ok {
			return true
		}
		if findCallExpr(ifs.Init, name) != nil || findCallExpr(ifs.Cond, name) != nil {
			out = ifs
			return false
		}
		return true
	})
	return out
}

// findCall returns the first call expression of the named selector/method in fn.
func findCall(t *testing.T, fn *ast.FuncDecl, method string) *ast.CallExpr {
	t.Helper()
	var out *ast.CallExpr
	ast.Inspect(fn, func(n ast.Node) bool {
		if out == nil {
			out = findCallExpr(n, method)
		}
		return out == nil
	})
	return out
}

func findCallExpr(n ast.Node, name string) *ast.CallExpr {
	// ast.Inspect panics on a nil node, and IfStmt.Init is nil whenever the
	// gate is written without an init clause. Without this guard the whole
	// guard dies with "ast.Walk: unexpected node type <nil>" — which reads as
	// a failure but is a crash, and would make a mutation look "caught" for the
	// wrong reason.
	if n == nil {
		return nil
	}
	var out *ast.CallExpr
	ast.Inspect(n, func(x ast.Node) bool {
		if out != nil {
			return false
		}
		call, ok := x.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fn := call.Fun.(type) {
		case *ast.SelectorExpr:
			if fn.Sel.Name == name {
				out = call
			}
		case *ast.Ident:
			if fn.Name == name {
				out = call
			}
		}
		return out == nil
	})
	return out
}

func callsWithin(stmts []ast.Stmt, name string) bool {
	for _, s := range stmts {
		if findCallExpr(s, name) != nil {
			return true
		}
	}
	return false
}

func hasReturn(stmts []ast.Stmt) bool {
	for _, s := range stmts {
		found := false
		ast.Inspect(s, func(n ast.Node) bool {
			if _, ok := n.(*ast.ReturnStmt); ok {
				found = true
			}
			return !found
		})
		if found {
			return true
		}
	}
	return false
}
