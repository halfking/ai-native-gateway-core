package bg

// analyze_duration_metrics_test.go — analyze 整趟耗时可观测性的契约门
// （runbook §10.106.29）。
//
// 背景：analyze 路径此前**既没有耗时日志、也没有任何 Prometheus 指标**
// （metrics.go 里 analyze 相关一条都没有），而它受 Go 侧 10min 预算 +
// SET LOCAL statement_timeout='10min' 双重约束。promote 路径有
// hotTablePromoteDurationSeconds，analyze 没有——这个不对称就是本门要钉的。
//
// 三条门分工：
//   A（运行时）指标真的存在且真的记录；
//   B（源码）analyze 路径确实在记耗时；
//   C（源码·顺序）**计时起点在节流槽认领之后**——否则被跳过的 tick 也会
//      往序列里灌一个 0 秒样本，把均值拉低，而这正是原来「只记 tables」
//      那条日志不可读���同一个坑。

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

// parseFuncDecl 解析文件并返回指定方法的 AST。
func parseFuncDecl(t *testing.T, file, method string) (*ast.File, *ast.FuncDecl) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, nil, 0)
	require.NoErrorf(t, err, "parse %s: %v", file, err)
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Recv == nil || fd.Name.Name != method {
			continue
		}
		return f, fd
	}
	t.Fatalf("method %s not found in %s", method, file)
	return nil, nil
}

// TestAnalyzeDurationMetricRecords: 运行时门——指标存在、且 Observe 真的生效。
//
// 读的是**直方图的样本数与总和**（从默认 registry gather），
// 而不是「调用没 panic」——后者在 Observe 被移除或记错目标时同样成立。
func TestAnalyzeDurationMetricRecords(t *testing.T) {
	beforeCount, beforeSum := gatherAnalyzeHistogram(t)

	recordAnalyzeDuration(42.5)

	afterCount, afterSum := gatherAnalyzeHistogram(t)
	require.Equal(t, beforeCount+1, afterCount,
		"recordAnalyzeDuration must add exactly one sample")
	require.InDelta(t, beforeSum+42.5, afterSum, 0.001,
		"the observed seconds must land in the histogram sum")
}

// gatherAnalyzeHistogram returns (sample count, sample sum) of the analyze
// duration histogram, read from the default registry.
func gatherAnalyzeHistogram(t *testing.T) (uint64, float64) {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	require.NoError(t, err)

	var count uint64
	var sum float64
	var found bool
	for _, mf := range families {
		if mf.GetName() != "llm_gateway_analyze_stats_duration_seconds" {
			continue
		}
		found = true
		require.Lenf(t, mf.Metric, 1,
			"whole-pass granularity: the series must carry no labels, got %d children", len(mf.Metric))
		h := mf.Metric[0].GetHistogram()
		require.NotNil(t, h, "metric must be a histogram")
		count, sum = h.GetSampleCount(), h.GetSampleSum()
	}
	require.True(t, found,
		"llm_gateway_analyze_stats_duration_seconds must be registered in the default registry")
	return count, sum
}

// TestAnalyzePathRecordsDuration: 源码门——analyze 路径确实在记耗时。
func TestAnalyzePathRecordsDuration(t *testing.T) {
	f, fd := parseFuncDecl(t, "partition_manager.go", "analyzePartitionStats")
	require.NotNil(t, f)
	require.NotNil(t, fd)

	var sawRecord, sawLogField bool
	ast.Inspect(fd, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch calleeName(call) {
		case "recordAnalyzeDuration":
			sawRecord = true
		case "Info":
			// 只认真的把 duration_s 放进 key-value 列表的调用，
			// 不靠注释里出现这个词。
			for _, a := range call.Args {
				bl, ok := a.(*ast.BasicLit)
				if ok && bl.Kind == token.STRING {
					if s, err := unquoteSafe(bl); err == nil && s == "duration_s" {
						sawLogField = true
					}
				}
			}
		}
		return true
	})

	require.True(t, sawRecord,
		"analyzePartitionStats must call recordAnalyzeDuration — otherwise the pass "+
			"stays unobservable and the 10min budget cannot be watched")
	require.True(t, sawLogField,
		"the analyze stats log line must carry duration_s as a real slog key-value, not "+
			"just mention it in a comment")
}

// TestAnalyzeTimerStartsAfterSlotClaim: 顺序门，本门组最关键的一条。
//
// 断言的是**语句顺序**，不是「有没有调用」：若计时起点被挪到
// `if !slotClaimed { ... return }` 之前，被节流跳过的 tick 也会落进序列，
// 序列里就会混入大量 0 秒样本，把真实趟次的均值系统性拉低。
// 这与「中间件放行 ≠ 已鉴权」同族：**调用存在不等于它被放在了正确的位置**。
func TestAnalyzeTimerStartsAfterSlotClaim(t *testing.T) {
	_, fd := parseFuncDecl(t, "partition_manager.go", "analyzePartitionStats")
	require.NotNil(t, fd)
	require.NotNil(t, fd.Body)

	var posSlotGuard, posTimer, posRecord int = -1, -1, -1
	for i, stmt := range fd.Body.List {
		switch {
		case isThrottleSkipGuard(stmt):
			posSlotGuard = i
		case assignsTimer(stmt):
			posTimer = i
		case containsCallTo(stmt, "recordAnalyzeDuration"):
			posRecord = i
		}
	}

	require.NotEqual(t, -1, posSlotGuard,
		"the !slotClaimed early-return guard was not found in analyzePartitionStats — "+
			"if it was restructured, this gate must be re-derived, not silently skipped")
	require.NotEqual(t, -1, posTimer,
		"no timer assignment found — the pass duration would never be measured")
	require.NotEqual(t, -1, posRecord,
		"recordAnalyzeDuration is not called from analyzePartitionStats")

	require.Less(t, posSlotGuard, posTimer,
		"the timer must start AFTER the throttle-slot guard, otherwise skipped ticks "+
			"pollute the duration series with zero-second samples and drag the average down")
	require.Less(t, posTimer, posRecord,
		"the duration must be recorded after the timer starts")
}

// --- AST 小工具 ---

func unquoteSafe(bl *ast.BasicLit) (string, error) {
	s, err := parser.ParseExpr(bl.Value)
	if err != nil {
		return "", err
	}
	bl2, ok := s.(*ast.BasicLit)
	if !ok || bl2.Kind != token.STRING {
		return "", err
	}
	return strings.Trim(bl2.Value, `"`), nil
}

func containsIdent(stmt ast.Stmt, name string) bool {
	found := false
	ast.Inspect(stmt, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && id.Name == name {
			found = true
			return false
		}
		return true
	})
	return found
}

func isIfWithReturn(stmt ast.Stmt) bool {
	ifs, ok := stmt.(*ast.IfStmt)
	if !ok {
		return false
	}
	found := false
	ast.Inspect(ifs.Body, func(n ast.Node) bool {
		if _, ok := n.(*ast.ReturnStmt); ok {
			found = true
			return false
		}
		return true
	})
	return found
}

func assignsTimer(stmt ast.Stmt) bool {
	as, ok := stmt.(*ast.AssignStmt)
	if !ok {
		return false
	}
	for _, l := range as.Lhs {
		if id, ok := l.(*ast.Ident); ok && id.Name == "analyzeStart" {
			return true
		}
	}
	return false
}

func containsCallTo(stmt ast.Stmt, fn string) bool {
	found := false
	ast.Inspect(stmt, func(n ast.Node) bool {
		c, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if calleeName(c) == fn {
			found = true
			return false
		}
		return true
	})
	return found
}

// calleeName returns the final identifier of a call target, handling BOTH
// `f(...)` (Ident) and `pkg.f(...)` (SelectorExpr). Matching only on Ident
// silently skips every package-qualified call — which is exactly how the
// `slog.Info(..., "duration_s", ...)` key went missing while its sibling
// assertion (recordAnalyzeDuration, a plain Ident call) still passed.
func calleeName(c *ast.CallExpr) string {
	switch f := c.Fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		return f.Sel.Name
	}
	return ""
}

// isThrottleSkipGuard recognises **the throttle guard specifically**: the
// `if !slotClaimed { ... return }` statement that aborts a skipped tick.
//
// ⚠️ It must NOT match merely "contains the identifier slotClaimed and has a
// return". The very same function has an EARLIER statement that satisfies that
// loose test:
//
//	if err := ...Scan(&slotClaimed); err != nil { ...; return }   // 认领出错分支
//	if !slotClaimed { ...; return }                                // 真正要守的守卫
//
// Taking the first match anchors the ordering assertion on the error branch,
// which always sits before the timer — so the gate goes green even when the
// timer is moved in front of the real guard. That hole was found by mutation
// M2, not by reading the test.
func isThrottleSkipGuard(stmt ast.Stmt) bool {
	ifs, ok := stmt.(*ast.IfStmt)
	if !ok {
		return false
	}
	un, ok := ifs.Cond.(*ast.UnaryExpr)
	if !ok || un.Op != token.NOT {
		return false
	}
	id, ok := un.X.(*ast.Ident)
	if !ok || id.Name != "slotClaimed" {
		return false
	}
	// The guard must actually bail out, otherwise it is not a skip path.
	found := false
	ast.Inspect(ifs.Body, func(n ast.Node) bool {
		if _, ok := n.(*ast.ReturnStmt); ok {
			found = true
			return false
		}
		return true
	})
	return found
}
