// credential_monitor_sliding_window_canonical_test.go — R167（2026-10-27）
// 长窗抽屉回退查询从 canonical 视图改走「底表两臂」的守卫。
//
// ★ 为什么这组守卫是必需的（★ 与本轮实测一一对应）：
//
//	① 长窗查询**不得**再触碰 canonical 视图。回归点是「有人把它改回视图」
//	   或「有人把视图的 UNION ALL 结构搬进底表版」——两者都会让 118 列投影
//	   的逐行表达式计算回到热路径。
//	   252 生产实测（本次）：视图 count 129,996.963 ms（2 分 10 秒）vs
//	   底表两臂 40,110.155 ms ⇒ 3.24 倍。
//	② canonical 参考臂**必须**仍然读视图。它存在的唯一目的就是当生产路径
//	   改写后还能证明「新写法和视图语义一致」；如果它也跟着改成底表，
//	   对拍就变成自己和自己比 ⇒ 等价性论证归零。
//	③ 两臂的 SELECT 列表必须**逐位同名同序**。UNION ALL 按位置配对，
//	   外层 WHERE/ORDER 引用的列名取自首臂 ⇒ 列序漂移 ⇒ 语义漂移
//	   （历史上 #153 就因为把 tenant_id 插进两臂的不同位置炸过 8 小时）。
//	④ canonical 参考臂**不得被生产代码引用**。它只是对拍参考臂；接回生产
//	   就等于把 ① 的回归重新做一遍。
package admin

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// deadRelationRefs 是 v1 族退役（迁移 890）后不得再出现在读侧 SQL 里的关系名。
// 命中任何一个 ⇒ 42P01（关系不存在）。
var deadRelationRefs = []string{
	"_request_logs",
	"_request_logs_hot",
	"request_logs_with_current_month_without_request_class_due_at",
}

// TestSlidingWindowLongQueryDoesNotTouchCanonicalView 负向守卫：长窗生产
// 查询不得触碰 canonical 视图，也不得引用已退役的 v1 族关系。
//
// ★ 判据是**关系名出现在查询文本里**，不是「plan 里有没有那个节点」——
// 视图形态下 planner 有时能把视图臂优化掉（本地实测 count(*) 形态下
// details 侧被消除），那时 plan 看不出问题，但 118 列投影的表达式计算
// 仍然逐行发生 ⇒ 必须从**文本**这一层拦住。
func TestSlidingWindowLongQueryDoesNotTouchCanonicalView(t *testing.T) {
	q := slidingWindowQuery()
	if strings.Contains(q, "request_logs_with_current_month") {
		t.Fatalf("长窗查询触碰了 canonical 视图（应读底表两臂）:\n%s", q)
	}
	// 两臂都在：hot 臂 + 母表冷表臂。少一条臂 ⇒ 长窗会漏掉 hot 保留期内的行。
	if !strings.Contains(q, "session_turns_hot") {
		t.Fatalf("长窗查询缺 hot 臂:\n%s", q)
	}
	if !regexp.MustCompile(`FROM\s+session_turns\s+t`).MatchString(q) {
		t.Fatalf("长窗查询缺母表冷表臂:\n%s", q)
	}
	for _, dead := range deadRelationRefs {
		if strings.Contains(q, dead) {
			t.Fatalf("长窗查询引用了已退役的 v1 族关系 %q（迁移 890 后是 42P01）:\n%s", dead, q)
		}
	}
	// 729 迁移建的表达式索引伺服 credential+窗过滤 ⇒ 谓词必须逐字写成
	// 那个 CASE 表达式（PG 用语义等价匹配索引表达式，但**函数包裹**不等价）。
	if !strings.Contains(q, "CASE WHEN t.credential_id ~ '^[0-9]+$' THEN t.credential_id::bigint END") {
		t.Fatalf("长窗查询的 credential 谓词与 729 表达式索引表达式不一致（索引将不参与）:\n%s", q)
	}
}

// TestSlidingWindowCanonicalQueryReadsCanonicalView 正向守卫：对拍参考臂
// 必须仍然读 canonical 视图。
//
// ★ 如果这条红了，说明有人把参考臂也改成了底表 ⇒ 对拍变成自己和自己比 ⇒
// 语义等价性的证据链断掉（本轮的所有等价结论都建立在「底表两臂 ≡ 视图」
// 这条对拍上）。
func TestSlidingWindowCanonicalQueryReadsCanonicalView(t *testing.T) {
	q := slidingWindowCanonicalQuery()
	if !strings.Contains(q, "FROM request_logs_with_current_month") {
		t.Fatalf("canonical 参考臂不读 canonical 视图（对拍将退化为自我比较）:\n%s", q)
	}
	for _, dead := range deadRelationRefs {
		if strings.Contains(q, dead) {
			t.Fatalf("canonical 参考臂引用了已退役的 v1 族关系 %q:\n%s", dead, q)
		}
	}
	// 投影列必须与生产长窗查询逐位一致（否则对拍的是不同形状的两组列）。
	want := slidingWindowQuery()
	if got, expect := projectionColumnNames(t, q), projectionColumnNames(t, want); got != expect {
		t.Fatalf("canonical 参考臂与生产长窗查询的投影列不一致:\n canonical=%s\n production=%s", got, expect)
	}
}

// TestSlidingWindowLongQueryArmsPositionallyAligned 钉住「底表两臂」的
// UNION ALL 位置对齐不变式：两臂恰有 2 条，且 SELECT 列表**逐位同名同序**。
//
// ★ 为什么这条现在是「两臂」而不是「一臂」：热窗是 1 臂（v1 臂已随 890
// 退役），长窗是 2 臂（hot ∪ 母表）⇒ 而 UNION ALL 按**位置**配对，
// 外层 WHERE/ORDER 引用的列名取自**首臂** ⇒ 两条臂的列序一旦漂移，
// 外层引用的列在第二臂上就是另一列 ⇒ 结果集静默出错（不是报错）。
func TestSlidingWindowLongQueryArmsPositionallyAligned(t *testing.T) {
	q := slidingWindowQuery()
	inner, outer := splitInnerOuterQuery(t, q)

	unionAllLine := regexp.MustCompile(`(?m)^\s*UNION ALL\s*$`)
	arms := unionAllLine.Split(inner, -1)
	if len(arms) != 2 {
		t.Fatalf("长窗查询应恰有 2 条臂（hot ∪ 母表），实得 %d:\n%s", len(arms), q)
	}

	first := orderedAliases(innerSelectList(t, arms[0]))
	for i, arm := range arms[1:] {
		got := orderedAliases(innerSelectList(t, arm))
		if len(got) != len(first) {
			t.Fatalf("第 %d 臂的投影列数与首臂不一致: %d vs %d:\n%s", i+1, len(got), len(first), q)
		}
		for j := range first {
			if got[j] != first[j] {
				t.Fatalf("第 %d 臂第 %d 个投影列与首臂不一致: %q vs %q（UNION ALL 按位置配对 ⇒ 语义漂移）:\n%s",
					i+1, j+1, got[j], first[j], q)
			}
		}
	}

	// 外层 WHERE/ORDER 引用的每个列名都必须在投影里存在（否则 42703）。
	innerAliases := projectedAliases(innerSelectList(t, arms[0]))
	for _, col := range []string{"credential_id", "outbound_model", "client_model", "tenant_id", "ts"} {
		if !innerAliases[col] {
			t.Fatalf("内层投影缺列 %q（外层引用它 ⇒ 解析期 42703 ⇒ 端点 500）:\n%s", col, q)
		}
	}
	_ = outer
}

// TestSlidingWindowCanonicalQueryIsTestOnly 静态守卫：canonical 参考臂
// **不得**被生产代码引用。
//
// ★ 用 AST 而不是文本匹配：文本匹配会漏掉跨行调用，也会把函数定义本身
// 误判成引用。
func TestSlidingWindowCanonicalQueryIsTestOnly(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(thisFile)
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse admin package: %v", err)
	}
	for _, pkg := range pkgs {
		for fname, file := range pkg.Files {
			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				// ★★ 必须同时认两种 Fun 形态：
				//   - *ast.SelectorExpr：跨包调用（pkg.Func()）
				//   - *ast.Ident：**同包调用**（Func()）——★ 本门第一版只查
				//     SelectorExpr ⇒ 同包调用检不出来 ⇒ 门给的是假绿。
				//     真仓变异臂 M3 就是同包调用（把生产查询换成参考臂）⇒
				//     第一版门在 M3 上判绿，被变异臂当场抓获。
				switch fun := call.Fun.(type) {
				case *ast.SelectorExpr:
					if fun.Sel.Name != "slidingWindowCanonicalQuery" {
						return true
					}
				case *ast.Ident:
					if fun.Name != "slidingWindowCanonicalQuery" {
						return true
					}
				default:
					return true
				}
				t.Errorf("%s: 生产代码引用了 canonical 参考臂 slidingWindowCanonicalQuery（它只供对拍；接回生产＝把视图的列膨胀放回热路径）",
					fname)
				return true
			})
		}
	}
}

// orderedAliases 按位置返回一段 SELECT 列表的投影别名（有序）。
func orderedAliases(selectList string) []string {
	var out []string
	for _, item := range splitTopLevelSelect(selectList) {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if i := strings.LastIndex(strings.ToUpper(item), " AS "); i >= 0 {
			out = append(out, strings.TrimSpace(item[i+4:]))
			continue
		}
		if i := strings.LastIndex(item, "."); i >= 0 {
			out = append(out, strings.TrimSpace(item[i+1:]))
			continue
		}
		out = append(out, item)
	}
	return out
}

// projectionColumnNames 返回一条查询**最外层** SELECT 列表的列名序列。
func projectionColumnNames(t *testing.T, q string) string {
	t.Helper()
	sel := strings.Index(q, "SELECT")
	if sel < 0 {
		t.Fatalf("query has no SELECT:\n%s", q)
	}
	list := q[sel+len("SELECT"):]
	if loc := regexp.MustCompile(`(?m)^\s*FROM\s`).FindStringIndex(list); loc != nil {
		list = list[:loc[0]]
	}
	return strings.Join(orderedAliases(list), ",")
}
