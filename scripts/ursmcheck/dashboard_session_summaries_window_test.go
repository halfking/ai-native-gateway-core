package ursm_test

// dashboard 聚合的时间窗守卫（2026-10-21 新增）
//
// # 背景：同一次响应里两个数字口径不一致
//
// `/api/admin/dashboard/session-health` 的响应由三段查询拼成：
//
//   · queryDistribution  —— 健康度分布（COUNT(*) FILTER …）
//   · queryHealthTrend   —— 健康度趋势（按天 GROUP BY）
//   · queryTopIssues     —— 常见问题 top5
//
// ★ 2026-10-21（R73 复验轮）实测：**只有 queryHealthTrend 带时间窗**，
// 另外两段**没有** ⇒ 对 `session_summaries`（生产 **830,913 行 / 825 MB**）
// 做**全表聚合** ⇒ 生产 154 上本端点撞 15s ctx deadline ⇒ **HTTP 500**。
//
// ⇒ ⇒ ★★ **同一个响应里，分布是「全量」、趋势是「近 N 天」** ⇒ 这是**口径
// 不一致**（不是「两种口径各有道理」）⇒ 修法 = 把缺失的两段补上**与 trend
// 逐字同款**的窗口谓词（`first_request_at >= NOW() - INTERVAL 'N days'`，
// N = `params.Days`，默认 7）。
//
// # 生产实测（同一条聚合语句，四种形状）
//
//	无窗口                              ⇒ 1,451 ms（Parallel Seq Scan 83 万行）
//	+ first_request_at 窗口              ⇒   660 ms（**仍 Seq Scan**：该列无索引）
//	+ last_request_at 窗口               ⇒ **Index Scan** ✅（同形状重测 330 / 465 ms，
//	                                        ⇒ **只承诺计划形态，不承诺毫秒数**）
//
// ⇒ ⚠ **窗口谓词用 `first_request_at` ⇒ 该列无任何索引 ⇒ 只能 Seq Scan + Filter**
// ⇒ 本门只保证「有窗口」（防全表聚合回归），**不保证窗口是索引支撑的**。
//
// # ★ 2026-10-21 复核：上一版注释里「需属主批 DDL 窗口」的说法**已撤销**
//
// 复核结论（生产实测 + 本地同版本 PG 17.10 最小复现）：
//
//	① **PG 17.10 允许把「非首列」当 index qual**（实测：`(tenant_id,
//	   last_request_at DESC)` 上只有 `last_request_at >= …` 也能产生
//	   `Index Cond`，本地 `enable_seqscan=off` 与生产强制索引**双向复现**）
//	   ⇒ ⇒ 「非首列 ⇒ 一定用不上索引」是**错的先验**。
//	② 生产 `idx_session_summaries_tenant_time (tenant_id, last_request_at
//	   DESC)` **已经在库里** ⇒ ⇒ 改用 `last_request_at` 窗口是**零 DDL**。
//	③ ⇒ ⇒ 「给 `session_summaries` 建窗口列索引（要 DDL 窗口）」这条建议
//	   **不成立** ⇒ 不需要向属主申请 DDL 窗口。
//
// ⇒ 仍**保留** `first_request_at` 的理由（不是性能，是口径一致性）：
//   `queryHealthTrend` 用的是 `first_request_at` ⇒ 若 distribution 改用
//   `last_request_at` ⇒ 同一个响应里两个数字**又变成两种口径**
//   （本次修的正是这个不一致）⇒ ⇒ 要换就得**三条查询一起换**
//   （属主拍板：「近 N 天活跃」vs「近 N 天新建」是语义变更）。
//
// # 这道门钉的是什么
//
//	① 凡是对 `session_summaries` 做**聚合**（COUNT/AVG/SUM/GROUP BY）的函数
//	   ⇒ **必须**带时间窗谓词；
//	② 若该函数引用了 `params.Days` ⇒ 窗口的天数必须**跟随参数**（字面量里是
//	   `%d days`）⇒ 不许写死数字。
//
// ⚠ **按 key 查**（无聚合）的读端不受此门约束 —— 例如
// `appendExecutionScope` 里的 `EXISTS (SELECT 1 FROM session_summaries s
// WHERE s.session_key = $n)`：它是**点查**，加时间窗反而会漏行。
// ⇒ ⇒ 判据用「**是否含聚合**」而不是「是否出现 FROM session_summaries」。
//
// ⚠ 本门**只做静态判定**（读源码 + AST），不连库 ⇒ 不产生任何生产写入。

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

const dashboardapiPkgRel = "admin/dashboardapi"

// dashboardSessionSummariesReader 读出 dashboardapi 包里「对 session_summaries
// 做聚合」的函数（函数名 + 文件 + 是否引用 params.Days）。
type dashboardSessionSummariesReader struct {
	file       string
	funcName   string
	usesParams bool
}

var (
	// 聚合判据：出现任一即视为「聚合读端」。
	dashboardAggRe = regexp.MustCompile(`(?i)\b(count|avg|sum)\s*\(`)
	// 「FROM session_summaries」判据（允许 public. 前缀）。
	dashboardTableRe = regexp.MustCompile(`(?i)FROM\s+(public\.)?session_summaries\b`)
	// 时间窗判据：某时间列 >= NOW() - INTERVAL '...'。
	dashboardWindowRe = regexp.MustCompile(
		`(?i)(first_request_at|last_request_at|created_at)\s*>=\s*NOW\(\)\s*-\s*INTERVAL`)
	// ★ 窗口**子句**的精确形态（用来取窗口天数）：只认「列 >= NOW() - INTERVAL 'X'」
	//   这一种。★ 不能用「字面量里含 day」来判天量级 —— 那会误伤
	//   `DATE(first_request_at) + INTERVAL '1 day'` 这类**算术表达式**
	//   （session_trend.go 的 closed_count 用的就是它）⇒ ⇒ 第一版判据因此
	//   在**正确代码**上假阳性 ⇒ 阳性对照把它逼出来了。
	dashboardWindowClauseRe = regexp.MustCompile(
		`(?i)(?:first_request_at|last_request_at|created_at)\s*>=\s*NOW\(\)\s*-\s*INTERVAL\s+'([^']*)'`)
)

// dashboardFuncSQL 收集一个函数里所有字符串字面量（已剥 SQL 注释）。
// ★ stripSQLComments 复用同包 session_family_attachments_predicate_test.go
// 里的那一个（同包不能重复定义；它是 R65 轮「断言 SQL 文本前必须剥注释」
// 的产物）。
func dashboardFuncSQL(fn *ast.FuncDecl) string {
	var b strings.Builder
	ast.Inspect(fn, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		v, err := strconv.Unquote(lit.Value)
		if err != nil {
			return true
		}
		b.WriteString(stripSQLComments(v))
		b.WriteByte('\n')
		return true
	})
	return b.String()
}

// dashboardFuncUsesParamsDays 判断函数是否引用 `params.Days`。
func dashboardFuncUsesParamsDays(fn *ast.FuncDecl) bool {
	found := false
	ast.Inspect(fn, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if sel.Sel.Name != "Days" {
			return true
		}
		if x, ok := sel.X.(*ast.Ident); ok && x.Name == "params" {
			found = true
		}
		return true
	})
	return found
}

// dashboardSessionSummariesAggregators 扫出全部「对 session_summaries 做聚合」
// 的函数。
func dashboardSessionSummariesAggregators(t *testing.T, root string) []dashboardSessionSummariesReader {
	t.Helper()
	dir := filepath.Join(root, dashboardapiPkgRel)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读目录 %s 失败: %v", dir, err)
	}
	var out []dashboardSessionSummariesReader
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(dir, name)
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("解析 %s 失败: %v", path, err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			sql := dashboardFuncSQL(fn)
			if !dashboardTableRe.MatchString(sql) {
				continue
			}
			if !dashboardAggRe.MatchString(sql) {
				// 点查（按 key 查单个/存在性）⇒ 不受本门约束。
				continue
			}
			out = append(out, dashboardSessionSummariesReader{
				file:       name,
				funcName:   fn.Name.Name,
				usesParams: dashboardFuncUsesParamsDays(fn),
			})
		}
	}
	return out
}

// TestDashboardSessionSummariesAggregatesAreWindowed 钉住
// 「凡对 session_summaries 做聚合，必须带时间窗」。
func TestDashboardSessionSummariesAggregatesAreWindowed(t *testing.T) {
	root := repoRootFromUrsm(t)
	readers := dashboardSessionSummariesAggregators(t, root)
	if len(readers) == 0 {
		t.Fatalf("在 %s 里没扫到任何「对 session_summaries 做聚合」的函数 ⇒ "+
			"判据本身失效（扫描面不对），不是「没有问题」", dashboardapiPkgRel)
	}
	dir := filepath.Join(root, dashboardapiPkgRel)
	fset := token.NewFileSet()
	byFile := map[string][]dashboardSessionSummariesReader{}
	for _, r := range readers {
		byFile[r.file] = append(byFile[r.file], r)
	}
	for file, rs := range byFile {
		f, err := parser.ParseFile(fset, filepath.Join(dir, file), nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("解析 %s 失败: %v", file, err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			isReader := false
			for _, r := range rs {
				if r.funcName == fn.Name.Name {
					isReader = true
					break
				}
			}
			if !isReader {
				continue
			}
			sql := dashboardFuncSQL(fn)
			if dashboardWindowRe.MatchString(sql) {
				continue
			}
			t.Errorf("%s:%d %s：对 session_summaries 的聚合**没有时间窗** ⇒\n%s\n"+
				"  ⇒ 生产 154 实测：无窗口 = 1,451ms（Seq Scan 83 万行），"+
				"同响应里的兄弟查询（trend）带窗口 ⇒ **口径不一致**；"+
				"本端点撞 15s ctx deadline ⇒ HTTP 500。\n"+
				"  ⇒ 修法：补与 queryHealthTrend 逐字同款的窗口谓词"+
				"（`first_request_at >= NOW() - INTERVAL '%%d days'`, N = params.Days）",
				file, fset.Position(fn.Pos()).Line, fn.Name.Name, sql)
		}
	}
}

// TestDashboardWindowFollowsParamsDays 钉住「窗口的天数跟随 params.Days」。
//
// ★ 判据设计的两次修正（都是被变异验证逼出来的）：
//
//	第 1 版：判据 = 「函数引用了 params.Days ⇒ 窗口字面量里必须有 `%d days`」。
//	         ⇒ ★ **有洞**：把窗口改成硬编码 `'7 days'` **并且把 params.Days 的
//	           引用一起去掉**，整条判据就绕过去了 ⇒ **变异 M2 没红** ⇒
//	         ⇒ 「门没牙」而不是「变异无效」。
//	第 2 版（现行）：判据 = 「凡窗口是**天量级**（字面量含 day）⇒
//	         ① 字面量必须逐字是 `%d days`；② 该函数必须引用 params.Days」。
//	         ⇒ 这样「硬编码天数」在**两个方向**上都会被抓到。
//
// ⚠ **小时量级的窗口豁免**：例如 `session_active.go` 的
// `ss.last_request_at >= NOW() - INTERVAL '1 hour'`（语义是「近 1 小时活跃」，
// 与 `days` 参数是**另一个维度**）⇒ 不要求它跟随 params.Days ⇒
// ⇒ 否则这条门会误伤一个语义正确的实现。
func TestDashboardWindowFollowsParamsDays(t *testing.T) {
	root := repoRootFromUrsm(t)
	readers := dashboardSessionSummariesAggregators(t, root)
	dir := filepath.Join(root, dashboardapiPkgRel)
	fset := token.NewFileSet()
	for _, r := range readers {
		path := filepath.Join(dir, r.file)
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("解析 %s 失败: %v", path, err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || fn.Name.Name != r.funcName {
				continue
			}
			// 逐个字符串字面量看：含「天量级窗口」的必须逐字 `%d days`。
			ast.Inspect(fn, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				v, err := strconv.Unquote(lit.Value)
				if err != nil {
					return true
				}
				sql := stripSQLComments(v)
				if !dashboardWindowRe.MatchString(sql) {
					return true
				}
				// ★ 取窗口子句里的**天数规格**（只认 `列 >= NOW() - INTERVAL 'X'`）。
				for _, m := range dashboardWindowClauseRe.FindAllStringSubmatch(sql, -1) {
					spec := m[1]
					if !strings.Contains(strings.ToLower(spec), "day") {
						// 小时量级（'1 hour' 之类）⇒ 豁免（另一个语义维度）。
						continue
					}
					if !strings.Contains(sql, "%d days") {
						t.Errorf("%s:%d %s：天量级窗口没有逐字用 `%%d days` ⇒\n%s\n"+
							"  ⇒ 窗口天数被写死 ⇒ 请求的 days 参数（默认 7）对它无效 ⇒\n"+
							"  ⇒ 「近 N 天」这个契约形同虚设（且各面板口径会各写各的）",
							r.file, fset.Position(fn.Pos()).Line, r.funcName, sql)
						return true
					}
					if !r.usesParams {
						t.Errorf("%s:%d %s：窗口是 `%%d days` 形态，但函数**没有引用 params.Days** ⇒\n%s\n"+
							"  ⇒ 这个 `%%d` 没有参数可填 ⇒ 迁移/格式化会把字面量里的 %%d 留下 ⇒\n"+
							"  ⇒ 查询要么报参数个数不匹配，要么被填成 0 ⇒ 窗口失效",
							r.file, fset.Position(fn.Pos()).Line, r.funcName, sql)
					}
				}
				return true
			})
		}
	}
}
