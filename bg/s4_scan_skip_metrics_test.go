package bg

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// ---------------------------------------------------------------------------
// 门 A：闭集默认拒绝（源码侧）
//
// 断言方向刻意是**「能产出的 reason 必须在闭集内」**，不是「闭集里的必须被用到」。
// 反方向证伪不了（一个 reason 可以只在特定配置下才走到），而正方向是默认拒绝：
// 任何新写的 comparability 函数、或它返回的新字面量/新常量，只要产出新 reason
// 就必须登记，否则门红。
//
// 识别 comparability 函数的判据是**具名结果** `(comparable bool, reason string)`。
// 不是按函数名白名单 —— 那样新增一个跳过的检查源时门会静默放过。
// bg 里其余返回 (bool, string) 的函数（probeCredentialWithCapability /
// probeCredential / miniChat / miniAnthropic）全部是**无名**结果，不会被扫到，
// 也不该被扫到：它们返回的是错误文案，不是跳过原因键。
// ---------------------------------------------------------------------------

func TestS4ScanSkipReasonsAreClosed(t *testing.T) {
	// 1) 收集包级字符串常量：name -> value。
	constValues := map[string]string{}
	parsed := map[string]*ast.File{}
	for _, name := range []string{"ledger_reconciliation.go", "credential_recovery.go", "s4_scan_skip_metrics.go"} {
		f, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		parsed[name] = f
		for _, d := range f.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, ident := range vs.Names {
					if i >= len(vs.Values) {
						continue
					}
					if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
						if s, err := strconv.Unquote(lit.Value); err == nil {
							constValues[ident.Name] = s
						}
					}
				}
			}
		}
	}

	// 2) 扫出所有 comparability 函数，收敛它们能产出的 reason。
	seen := map[string]bool{}
	for _, f := range parsed {
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil || fd.Type.Results == nil || len(fd.Type.Results.List) != 2 {
				continue
			}
			second := fd.Type.Results.List[1]
			if len(second.Names) != 1 || second.Names[0].Name != "reason" {
				continue // 无名结果 → 不是 comparability 函数
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				switch v := n.(type) {
				case *ast.BasicLit:
					if v.Kind == token.STRING {
						if s, err := strconv.Unquote(v.Value); err == nil && s != "" {
							seen[s] = true
						}
					}
				case *ast.Ident:
					if val, ok := constValues[v.Name]; ok && val != "" {
						seen[val] = true
					}
				}
				return true
			})
		}
	}

	if len(seen) == 0 {
		t.Fatal("no comparability function found — the scanner stopped recognizing them, " +
			"so this gate is vacuously green. Check the (bool, reason string) signature match.")
	}
	// 空串被上面的扫描排除是有意的：两个 comparability 函数的成功分支都是
	// `return true, ""`，空串表示「本轮可判定、真跑了」，不是跳过原因键。
	// 把它算进闭集会让门要求登记一个恒为空的标签值。
	for reason := range seen {
		if !s4ScanSkipReasonRegistered(reason) {
			t.Errorf("skip reason %q is producible but not registered in s4ScanSkipReasons.\n"+
				"  Consequence: recordS4ScanSkipState refuses to write it into "+
				"s4ScanSkippedLastRun, so the gauge stays 0 and /metrics reports "+
				"\"ran, nothing skipped\" for a scan that did NOT run.\n"+
				"  Fix: add it to s4ScanSkipReasons AND give it alert coverage in "+
				"deploy/prometheus/rules/s4-scan-skip.yml.", reason)
		}
	}
}

// ---------------------------------------------------------------------------
// 门 B：接线（结构）
//
// 只断言「函数体里出现了 recordS4ScanSkipState」是不够的 —— 那正是本文件
// 第一节记录过的失败形状：调用被写在某条 return 之前，另一条 return 就漏了。
// 所以断言的是 **defer** 调用：defer 覆盖所有 return 分支，包括未来新增的。
// ---------------------------------------------------------------------------

func TestS4ScanSkipStateIsPublishedOnEveryReturnPath(t *testing.T) {
	cases := []struct{ file, fn string }{
		{"ledger_reconciliation.go", "RunOnce"},
		{"credential_recovery.go", "scanLookbackRecoveries"},
	}
	for _, tc := range cases {
		t.Run(tc.fn, func(t *testing.T) {
			f, err := parser.ParseFile(token.NewFileSet(), tc.file, nil, 0)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			var fn *ast.FuncDecl
			for _, d := range f.Decls {
				if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == tc.fn {
					fn = fd
					break
				}
			}
			if fn == nil {
				t.Fatalf("%s not found in %s", tc.fn, tc.file)
			}
			found := false
			for _, stmt := range fn.Body.List {
				decl, ok := stmt.(*ast.DeferStmt)
				if !ok {
					continue
				}
				if call, ok := decl.Call.Fun.(*ast.Ident); ok && call.Name == "recordS4ScanSkipState" {
					found = true
				}
			}
			if !found {
				t.Errorf("%s does not `defer recordS4ScanSkipState(...)`.\n"+
					"  A plain call would only cover the return path it sits on; every other "+
					"return (query error, no-candidates, hook-nil) would leave the previous "+
					"run's skip state in place and keep reporting it.", tc.fn)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 门 C：resetSkipped 必须早于所有早退
//
// scanLookbackRecoveries 的 hook 早退曾位于 resetSkipped 之上，于是「什么都没做
// 的一轮」返回**上一轮**的 skip 列表。当时是潜在的（hook 构造后不变），但一旦把
// 列表发布到 /metrics，它就变成运维可见的谎报。钉住顺序。
// ---------------------------------------------------------------------------

func TestResetSkippedPrecedesEarlyReturnInLookbackScan(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "credential_recovery.go", nil, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var fn *ast.FuncDecl
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == "scanLookbackRecoveries" {
			fn = fd
			break
		}
	}
	if fn == nil {
		t.Fatal("scanLookbackRecoveries not found")
	}

	resetPos, hookNilPos := token.NoPos, token.NoPos
	for _, stmt := range fn.Body.List {
		ast.Inspect(stmt, func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.CallExpr:
				// `r.resetSkipped()` 的 Fun 是 *ast.SelectorExpr（方法带接收者），
				// 不是 *ast.Ident。第一版只认 Ident，于是「找不到 resetSkipped」
				// ——门红在一个从未存在过的缺陷上。这也是为什么它必须变异验证：
				// 一个恒红的门和没有门一样没用。
				switch f := v.Fun.(type) {
				case *ast.Ident:
					if f.Name == "resetSkipped" && resetPos == token.NoPos {
						resetPos = v.Pos()
					}
				case *ast.SelectorExpr:
					if f.Sel.Name == "resetSkipped" && resetPos == token.NoPos {
						resetPos = v.Pos()
					}
				}
			case *ast.BinaryExpr:
				// `r.ursmRecoverSink == nil && r.probeSubmitter == nil` 解析成
				// BinaryExpr{Op: LAND, X: BinaryExpr{Op: EQL, X: SelectorExpr, Y: nil}, ...}
				// —— 选择器在 be.X，be.Y 是 `nil` 标识符。第一版查的是 be.Y，
				// 于是「早退不见了」这种凭空报错又出现一次。
				if be, ok := v.X.(*ast.BinaryExpr); ok && be.Op == token.EQL {
					if se, ok := be.X.(*ast.SelectorExpr); ok && se.Sel.Name == "ursmRecoverSink" && hookNilPos == token.NoPos {
						hookNilPos = v.Pos()
					}
				}
			}
			return true
		})
	}
	if resetPos == token.NoPos {
		t.Fatal("scanLookbackRecoveries no longer calls resetSkipped() at all — " +
			"SkippedChecks() would accumulate across runs forever")
	}
	if hookNilPos == token.NoPos {
		t.Fatal("the hook-nil early return is gone; this test needs updating")
	}
	if resetPos > hookNilPos {
		t.Error("resetSkipped() is called AFTER the hook-nil early return — a run that " +
			"returns immediately still reports the previous run's skips")
	}
}

// ---------------------------------------------------------------------------
// 门 D：显式归零（行为）
//
// 这是 §9.35 M3 那个 bug 的正面回应：悲观初始化 + 正常分支忘设回 false ⇒
// 字段恒为 true。「跳过一轮、随后恢复」必须回到 0，否则运维会永远看到一个
// 已经修好的扫描仍在跳过。
// ---------------------------------------------------------------------------

func TestSkippedGaugeIsExplicitlyResetWhenTheRunResumes(t *testing.T) {
	const worker = s4ScanWorkerLedgerReconciliation
	gauge := func() float64 {
		return testutil.ToFloat64(s4ScanSkippedLastRun.WithLabelValues(worker, s4ScanSkipReasonStopWrite))
	}

	recordS4ScanSkipState(worker, []string{s4ScanSkipReasonStopWrite}, time.Unix(1000, 0))
	if got := gauge(); got != 1 {
		t.Fatalf("after a gated run the gauge = %v, want 1 — the skip is not observable, "+
			"which is the whole gap this metric closes", got)
	}

	recordS4ScanSkipState(worker, nil, time.Unix(2000, 0))
	if got := gauge(); got != 0 {
		t.Fatalf("after a clean run the gauge = %v, want 0. A gauge that is only ever set "+
			"to 1 and never cleared is the §9.35 M3 failure: the field is permanently true "+
			"and the alert would outlive the stop-write it was raised for.", got)
	}
}

// ---------------------------------------------------------------------------
// 门 E：未登记 reason 不得被静默吞掉（默认拒绝的行为侧）
//
// 这是闭集门 A 的运行时兜底。断言的是「不谎报」：未登记的 reason 绝不能把 gauge
// 写成 1（那会让告警表达式依赖一个未登记标签值），但也绝不能悄悄消失 —— 它必须
// 落在兜底计数器上。
// ---------------------------------------------------------------------------

func TestUnregisteredReasonIsCountedAndNeverSetsTheGauge(t *testing.T) {
	const worker = s4ScanWorkerLedgerReconciliation
	const bogus = "a_reason_nobody_registered"

	before := testutil.ToFloat64(s4ScanUnregisteredSkipTotal.WithLabelValues(worker, bogus))
	recordS4ScanSkipState(worker, []string{bogus}, time.Unix(3000, 0))

	if got := testutil.ToFloat64(s4ScanUnregisteredSkipTotal.WithLabelValues(worker, bogus)); got != before+1 {
		t.Errorf("unregistered reason counter = %v, want %v — an unregistered skip source "+
			"would be dropped on the floor and the gauge would keep reporting 0 "+
			"(i.e. \"ran fine\") for a scan that never ran", got, before+1)
	}
	// The gauge's label space must stay exactly the closed enum: writing the bogus
	// value there would let the alert expression depend on an unregistered label.
	if got := testutil.ToFloat64(s4ScanSkippedLastRun.WithLabelValues(worker, s4ScanSkipReasonStopWrite)); got != 0 {
		t.Errorf("the closed-enum gauge = %v after an unregistered-reason run, want 0 — "+
			"a different reason key must not be laundered into the stop-write label", got)
	}
}
