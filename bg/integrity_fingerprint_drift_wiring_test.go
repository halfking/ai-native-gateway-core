package bg

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

// 审计 §9.50：`IntegrityFingerprintDrift` 的可观测出口**必须与它自己的计数器
// 同生共死**。
//
// # 为什么这道门不是「数一下有几处」
//
// 该 worker 有两个内存计数器：`scannedCycles` / `skippedTicks`。停写之后它们是
// 这个检测器**唯一**的活性证据，而 `Stats()`（:131）只读 `scannedCycles`、
// `skippedTicks` 建告警前全仓无人读。§9.50 在 `tick()` 的三处计数点加了
// `recordFingerprintDrift{Scan,Skip}` 调用。
//
// 但「加了一处调用」是一次性的：以后任何人**新增**一个跳过分支而忘了导出指标，
// 或者**删除**一个分支而忘了删调用，两种漂移都没有任何东西会拦。⇒ 这里断言的不是
// 「现在有 N 处」，而是**结构不变式**：
//
//	在任意一个基本块内，`<counter>.Add(…)` 出现的次数 == 记录器调用出现的次数，
//	且**每条记录器语句紧跟在它对应的计数语句之后一行**。
//
// 位置用「所属基本块 + 块内下标」表示，而不是裸行号：行号会随任何无关编辑漂移，
// 且相邻两行的两个调用无法被区分开。
//
// 方向（只断言能被证明的那一侧）：这条不变式**对任意数量的分支都成立**，
// 不预设必须是两处 skip / 一处 scan。真要改分支数量时，这道门仍然绿。

// hit 记录一次调用出现的位置：所属基本块的标识 + 块内语句下标。
type hit struct {
	block string
	index int
}

func (h hit) String() string { return h.block + "#" + strconv.Itoa(h.index) }

// hitsInFile 收集指定文件里所有出现目标调用的语句位置。
//
// 目标支持两种写法：裸函数名（`recordFingerprintDriftSkip`）与 `接收者.方法`
// （`skippedTicks.Add`）。后者按接收者**字段名**匹配，且**两级 selector 都认**
// （`x.Add` 与 `w.x.Add`）——只认一级会在真代码上静默返回 false，
// 于是门恒绿而不报错（本门第一版就这样栽过，见 §9.50 的变异记录）。
func hitsInFile(t *testing.T, path, target string) []hit {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	require.NoError(t, err, "parse %s", path)

	// block 键必须**跨两次 hitsInFile 调用可比**（本门要把 counter 的位置与
	// recorder 的位置对比），所以用行号而不是 token.Pos——后者只在同一个
	// FileSet 内有意义，而每次调用都新建了一个。
	line := func(p token.Pos) int { return fset.Position(p).Line }
	key := func(kind string, p token.Pos) string {
		return kind + "@" + filepath.Base(path) + ":" + strconv.Itoa(line(p))
	}

	var out []hit
	var walkList func(stmts []ast.Stmt, block string)
	walkList = func(stmts []ast.Stmt, block string) {
		for i, stmt := range stmts {
			if hasCall(stmt, target) {
				out = append(out, hit{block: block, index: i})
			}
			walkNested(stmt, key, walkList)
		}
	}
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Body != nil {
			walkList(fn.Body.List, key("body", fn.Body.Pos()))
		}
	}
	return out
}

// walkNested 进入一个语句内部可能藏着调用的位置；每个 `[]ast.Stmt` 都是
// 一个**独立**的基本块（下标空间不共享）。
func walkNested(stmt ast.Stmt, key func(string, token.Pos) string, walkList func([]ast.Stmt, string)) {
	blk := func(s *ast.BlockStmt) { walkList(s.List, key("blk", s.Pos())) }
	switch s := stmt.(type) {
	case *ast.BlockStmt:
		blk(s)
	case *ast.IfStmt:
		if s.Init != nil {
			walkNested(s.Init, key, walkList)
		}
		blk(s.Body)
		if s.Else != nil {
			walkNested(s.Else, key, walkList)
		}
	case *ast.ForStmt:
		blk(s.Body)
	case *ast.RangeStmt:
		blk(s.Body)
	case *ast.SwitchStmt:
		for _, c := range s.Body.List {
			walkNested(c, key, walkList)
		}
	case *ast.TypeSwitchStmt:
		for _, c := range s.Body.List {
			walkNested(c, key, walkList)
		}
	case *ast.SelectStmt:
		for _, c := range s.Body.List {
			walkNested(c, key, walkList)
		}
	case *ast.CaseClause:
		walkList(s.Body, key("case", s.Pos()))
	case *ast.CommClause:
		walkList(s.Body, key("comm", s.Pos()))
	}
}

// hasCall 判断**这一条语句本身**里是否出现目标调用。
//
// 必须是浅匹配：遇到 *ast.BlockStmt 就停止下探。否则外层的 `switch` 节点会
// 「认领」它内部 case 体里的每一次调用，位置被归到函数体上，块内下标也错
// ——门会以一种与真实代码无关的方式变红（或变绿）。
func hasCall(stmt ast.Stmt, target string) bool {
	recv, method := splitTarget(target)
	found := false
	ast.Inspect(stmt, func(n ast.Node) bool {
		if _, ok := n.(*ast.BlockStmt); ok {
			// 嵌套块归它自己那一轮 walkList 管。
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fn := call.Fun.(type) {
		case *ast.Ident:
			// 裸函数调用；method 非空时（如 "skippedTicks.Add"）不算命中。
			if method == "" && fn.Name == recv {
				found = true
			}
		case *ast.SelectorExpr:
			if method == "" {
				return true
			}
			switch outer := fn.X.(type) {
			case *ast.Ident:
				if outer.Name == recv && fn.Sel.Name == method {
					found = true
				}
			case *ast.SelectorExpr:
				if outer.Sel.Name == recv && fn.Sel.Name == method {
					found = true
				}
			}
		}
		return true
	})
	return found
}

// splitTarget 把 "x.Add" 拆成 ("x", "Add")，裸名字则得到 (name, "")。
func splitTarget(target string) (recv, method string) {
	for i := 0; i < len(target); i++ {
		if target[i] == '.' {
			return target[:i], target[i+1:]
		}
	}
	return target, ""
}

func TestFingerprintDriftCountersAndMetricsAreWiredTogether(t *testing.T) {
	const src = "integrity_fingerprint_drift.go"

	for _, c := range []struct{ counter, metric, what string }{
		{"skippedTicks.Add", "recordFingerprintDriftSkip", "skip"},
		{"scannedCycles.Add", "recordFingerprintDriftScan", "scan"},
	} {
		counters := hitsInFile(t, src, c.counter)
		recorders := hitsInFile(t, src, c.metric)

		require.NotEmpty(t, counters,
			"%s disappeared from %s — if that was intentional, delete %s() in the same commit",
			c.counter, src, c.metric)
		require.NotEmpty(t, recorders,
			"%s() is called nowhere in %s — the %s metric would be decoration (§9.37/§9.50)",
			c.metric, src, c.what)

		byBlock := map[string][]int{}
		for _, h := range recorders {
			byBlock[h.block] = append(byBlock[h.block], h.index)
		}
		for _, h := range counters {
			idx := byBlock[h.block]
			require.Contains(t, idx, h.index+1,
				"%s at %s has no %s() immediately after it in the same block. "+
					"Every counter increment must be exported in the very next statement, "+
					"otherwise the counter and the Prometheus series disagree about "+
					"whether the detector is alive.", c.counter, h, c.metric)
		}
		require.Len(t, recorders, len(counters),
			"%s() is called %d times but %s only %d times — a recorder without its "+
				"counter would count an event the in-process counter never saw",
			c.metric, len(recorders), c.counter, len(counters))
	}
}

// TestFingerprintDriftStartSeedsLastScan 钉住「启动即置为进程启动时刻」这个决定。
//
// 这不是可有可无的初始化：探针**每进程最多跑一次**，所以「启动后一次都没扫过」
// 是停写之后的真实形态。若把这个 gauge 初始化成 0，`time() - 0` 恒为巨大值 ⇒
// **每个刚起来的进程都立刻告警**；若完全不初始化，序列不存在 ⇒ 告警永不响。
func TestFingerprintDriftStartSeedsLastScan(t *testing.T) {
	const src = "integrity_fingerprint_drift.go"
	require.Len(t, hitsInFile(t, src, "recordFingerprintDriftStart"), 1,
		"recordFingerprintDriftStart must be called from exactly one place (the worker's Start), "+
			"otherwise last_scan_unix is seeded somewhere it does not mean \"this process started\"")

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, src, nil, 0)
	require.NoError(t, err)

	var start *ast.FuncDecl
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == "Start" && fn.Body != nil {
			start = fn
		}
	}
	require.NotNil(t, start, "no Start() in %s", src)

	calls := 0
	ast.Inspect(start.Body, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "recordFingerprintDriftStart" {
				calls++
			}
		}
		return true
	})
	require.Equal(t, 1, calls, "recordFingerprintDriftStart must be wired inside Start()")
}

// TestFingerprintDriftMetricsFileExists 守住指标定义文件没被删成空壳。
// 判据是「文件存在且三个指标名都还在」——**不是**「有人在读它」，
// 后者是 deploy/prometheus/rules 那道门的事（§9.37：没有告警读的指标是装饰）。
func TestFingerprintDriftMetricsFileExists(t *testing.T) {
	data, err := os.ReadFile("integrity_fingerprint_drift_metrics.go")
	require.NoError(t, err, "the metric definitions file must exist")
	for _, name := range []string{
		"llm_gateway_bg_fingerprint_drift_scanned_total",
		"llm_gateway_bg_fingerprint_drift_skipped_total",
		"llm_gateway_bg_fingerprint_drift_last_scan_unix",
	} {
		require.Contains(t, string(data), name, "metric %s is no longer defined", name)
	}
}
