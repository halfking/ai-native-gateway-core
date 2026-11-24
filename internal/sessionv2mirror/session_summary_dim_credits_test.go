//go:build !integration

package sessionv2mirror

// session_summary_dim_credits_test.go —— 积分列（total_credits_charged）写入门
// （2026-11-24 L1 修订轮）。
//
// ★ 背景（DEFECT 2）：session_summaries.total_credits_charged 由迁移 990 建列，
//
//	但 990 同时定义的 update_session_summary() **没有建触发器** ⇒ 那条 plpgsql
//	路径从未被调用；而本写入器（SessionSummaryDimWriter）是 session_summaries
//	聚合列的**唯一写方** ⇒ 积分列长期为 0 ⇒ 4 个会话分析物化视图的
//	external_cost = SUM(ss.total_credits_charged) **恒为 0**
//	（external_cost 结构性地不存在，界面上的「外部成本」永远是 0）。
//
// ★ 这道门守什么（两条，都必须成立）：
//	① SQL 里真的写这一列：INSERT 列清单里有它，且 ON CONFLICT DO UPDATE
//	   **累加**它（累加而不是覆盖 —— 一行会话会来多次请求）；
//	② 参数编号与调用点**同步**：SQL 引用的最大 $N 必须 == 调用点绑定的实参
//	   个数，且被引用的参数集合恰为 1..N。
//
// ★ 为什么②必须是门（而不是"看着对就行"）：SQL 与调用点是两处独立维护的
//
//	文本，改一处忘另一处**编译器不会报**（Go 侧只是多传/少传一个
//	interface{}）⇒ 运行期才会炸（"bind message supplies N parameters, but
//	prepared statement requires M"）⇒ 生产上表现为整条会话聚合停写。
//	同理，**新增参数必须追加在最后**（$16）：重排既有 $N 会让所有参数的
//	含义漂移，而这种漂移在文本上完全看不出来（SQL 仍然"自洽"）。
//
// ★ 为什么是静态门（不依赖数据库）：本写入器持有的是具体类型 *pgxpool.Pool，
//
//	没有可注入的 seam ⇒ 无法用 mock 断言"传了什么参数"。静态判据零依赖、
//	零假绿，变异臂天然可做（改坏参数编号 ⇒ 门立刻红）。
import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// creditsAccumulationRE 匹配 DO UPDATE 里的累加式赋值（参数序号不写死）。
var creditsAccumulationRE = regexp.MustCompile(
	`total_credits_charged\s*=\s*session_summaries\.total_credits_charged\s*\+\s*\$\d+`)

// readSessionSummaryDimSource 读 session_summary_dim.go 源码（静态判据用）。
func readSessionSummaryDimSource(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("session_summary_dim.go")
	if err != nil {
		t.Fatalf("读 session_summary_dim.go 失败：%v", err)
	}
	return string(b)
}

// TestSessionSummaryUpsertWritesCreditsColumn 钉测①：积分列真的被写。
func TestSessionSummaryUpsertWritesCreditsColumn(t *testing.T) {
	// INSERT 列清单里必须有这一列（不在清单里 ⇒ 永远写不进去）。
	insertList := sessionSummaryInsertColumnList(t)
	if !strings.Contains(insertList, "total_credits_charged") {
		t.Fatalf("INSERT 列清单里没有 total_credits_charged ⇒ 积分永远写不进 "+
			"session_summaries（实际清单：%s）", insertList)
	}

	// DO UPDATE 必须**累加**（同一会话会来多次请求 ⇒ 覆盖会丢历史积分）。
	if !creditsAccumulationRE.MatchString(sessionSummaryUpsertSQL) {
		t.Fatalf("ON CONFLICT DO UPDATE 没有累加 total_credits_charged ⇒ " +
			"后来的请求会覆盖已有积分（期望形如 " +
			"'total_credits_charged = session_summaries.total_credits_charged + $N'）")
	}

	// 同一参数必须同时出现在 INSERT 的 VALUES 与 DO UPDATE 里
	// （两处用不同参数 ⇒ 插入值与累加值不是一个数）。
	insertParam, ok := creditsParamIndexInValues(t)
	if !ok {
		t.Fatal("INSERT 的 VALUES 里找不到 total_credits_charged 对应的参数")
	}
	updateParam, ok := creditsParamIndexInUpdate(t)
	if !ok {
		t.Fatal("ON CONFLICT DO UPDATE 里找不到 total_credits_charged 对应的参数")
	}
	if insertParam != updateParam {
		t.Fatalf("积分在 INSERT 用 $%d、在 DO UPDATE 用 $%d —— 两处必须是同一个参数",
			insertParam, updateParam)
	}
}

// TestSessionSummaryUpsertParamCountMatchesCallSite 钉测②：参数编号与调用点同步。
func TestSessionSummaryUpsertParamCountMatchesCallSite(t *testing.T) {
	src := readSessionSummaryDimSource(t)

	params := referencedParamIndexes(t, sessionSummaryUpsertSQL)
	if len(params) == 0 {
		t.Fatal("SQL 里一个 $N 都没有 —— 判据失效")
	}
	maxIdx := 0
	seen := map[int]bool{}
	for _, p := range params {
		if p > maxIdx {
			maxIdx = p
		}
		seen[p] = true
	}
	// 被引用的参数集合必须恰为 1..maxIdx（不能有洞：$7 缺失 ⇒ 少传一个参数）。
	for i := 1; i <= maxIdx; i++ {
		if !seen[i] {
			t.Fatalf("SQL 里 $%d 没被引用（1..%d 里缺了一个）⇒ 与调用点的实参个数对不上",
				i, maxIdx)
		}
	}

	args := sessionSummaryUpsertBoundArgs(t, src)
	if len(args) != maxIdx {
		t.Fatalf("SQL 引用了 %d 个参数（最大 $%d），调用点只绑定了 %d 个实参 ⇒ "+
			"运行期会报 bind message supplies %d parameters, but prepared statement "+
			"requires %d（改 SQL 忘了改调用点，或反之）",
			len(params), maxIdx, len(args), len(args), maxIdx)
	}

	// ★ 既有参数不许被重排（重排 = 所有 $N 的含义漂移，文本上完全看不出来）。
	for _, frag := range []string{"$5::numeric", "$6::numeric", "$7::numeric", "$13::int8"} {
		if !strings.Contains(sessionSummaryUpsertSQL, frag) {
			t.Fatalf("既有参数片段 %q 不见了 ⇒ 既有参数被重排/改名（新增参数只准追加在最后）",
				frag)
		}
	}
}

// TestSessionSummaryUpsertCreditsArgComesFromEntry 钉测③：积分实参来自写入点。
func TestSessionSummaryUpsertCreditsArgComesFromEntry(t *testing.T) {
	src := readSessionSummaryDimSource(t)
	args := sessionSummaryUpsertBoundArgs(t, src)

	last := args[len(args)-1]
	if !strings.Contains(last, "entry.CreditsCharged") {
		t.Fatalf("最后一个绑定实参是 %q —— 期望它来自 entry.CreditsCharged（积分值在写入点就有）",
			last)
	}
	if !strings.Contains(last, "int64PtrOrZero") {
		t.Fatalf("最后一个绑定实参是 %q —— 期望用 int64PtrOrZero 归一（nil ⇒ 0；该列 NOT NULL）",
			last)
	}
}

// sessionSummaryInsertColumnList 取 INSERT 的列清单文本。
func sessionSummaryInsertColumnList(t *testing.T) string {
	t.Helper()
	marker := "INSERT INTO session_summaries ("
	idx := strings.Index(sessionSummaryUpsertSQL, marker)
	if idx < 0 {
		t.Fatal("SQL 里找不到 INSERT INTO session_summaries (")
	}
	rest := sessionSummaryUpsertSQL[idx+len(marker):]
	end := strings.Index(rest, ")")
	if end < 0 {
		t.Fatal("INSERT 列清单没有闭合")
	}
	return rest[:end]
}

// creditsParamIndexInValues 取 INSERT 的 VALUES 里积分列用的参数序号。
func creditsParamIndexInValues(t *testing.T) (int, bool) {
	t.Helper()
	values := strings.Index(sessionSummaryUpsertSQL, ") VALUES (")
	if values < 0 {
		t.Fatal("SQL 里找不到 VALUES 段")
	}
	rest := sessionSummaryUpsertSQL[values+len(") VALUES ("):]
	end := strings.Index(rest, ") ON CONFLICT")
	if end < 0 {
		t.Fatal("VALUES 段没有闭合")
	}
	cols := sessionSummaryInsertColumnList(t)
	idx := columnIndexInList(cols, "total_credits_charged")
	if idx < 0 {
		return 0, false
	}
	items := splitTopLevelArgs(rest[:end])
	if idx >= len(items) {
		return 0, false
	}
	return paramIndexIn(items[idx])
}

// creditsParamIndexInUpdate 取 DO UPDATE 里积分列用的参数序号。
func creditsParamIndexInUpdate(t *testing.T) (int, bool) {
	t.Helper()
	idx := strings.Index(sessionSummaryUpsertSQL, "ON CONFLICT")
	if idx < 0 {
		t.Fatal("SQL 里找不到 ON CONFLICT 段")
	}
	rest := sessionSummaryUpsertSQL[idx:]
	for _, line := range strings.Split(rest, "\n") {
		if !strings.Contains(line, "total_credits_charged") {
			continue
		}
		if n, ok := paramIndexIn(line); ok {
			return n, true
		}
	}
	return 0, false
}

// columnIndexInList 返回列清单里某列的位置（0 起）。
func columnIndexInList(list, column string) int {
	items := splitTopLevelArgs(list)
	for i, it := range items {
		if strings.EqualFold(strings.TrimSpace(it), column) {
			return i
		}
	}
	return -1
}

// paramIndexIn 从一段 SQL 文本里取唯一的 $N（返回序号）。
func paramIndexIn(text string) (int, bool) {
	ms := regexp.MustCompile(`\$(\d+)`).FindAllStringSubmatch(text, -1)
	if len(ms) == 0 {
		return 0, false
	}
	n, err := strconv.Atoi(ms[0][1])
	if err != nil {
		return 0, false
	}
	return n, true
}

// referencedParamIndexes 取出 SQL 里被引用的全部 $N（去重、升序）。
func referencedParamIndexes(t *testing.T, sqlText string) []int {
	t.Helper()
	ms := regexp.MustCompile(`\$(\d+)`).FindAllStringSubmatch(sqlText, -1)
	seen := map[int]bool{}
	var out []int
	for _, m := range ms {
		n, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		if seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	return out
}

// sessionSummaryUpsertBoundArgs 取调用点 w.pool.Exec(...) 绑定的实参
// （跳过 ctx 与 SQL 常量本身）。
func sessionSummaryUpsertBoundArgs(t *testing.T, src string) []string {
	t.Helper()
	marker := "w.pool.Exec(ctx, sessionSummaryUpsertSQL"
	idx := strings.Index(src, marker)
	if idx < 0 {
		t.Fatal("源码里找不到 w.pool.Exec(ctx, sessionSummaryUpsertSQL")
	}
	open := idx + strings.Index(src[idx:], "(")
	depth := 0
	end := -1
	for i := open; i < len(src); i++ {
		switch src[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				end = i
			}
		}
		if end >= 0 {
			break
		}
	}
	if end < 0 {
		t.Fatal("w.pool.Exec( 没有闭合")
	}
	items := splitTopLevelArgs(src[open+1 : end])
	if len(items) < 2 {
		t.Fatalf("w.pool.Exec( 的实参只有 %d 个 —— 判据失效", len(items))
	}
	// items[0] = ctx，items[1] = SQL 常量，其余才是绑定参数。
	return items[2:]
}

// splitTopLevelArgs 按**顶层**逗号切分（括号内的逗号不算分隔符）。
func splitTopLevelArgs(s string) []string {
	var out []string
	depth := 0
	last := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, strings.TrimSpace(s[last:i]))
				last = i + 1
			}
		}
	}
	if item := strings.TrimSpace(s[last:]); item != "" {
		out = append(out, item)
	}
	return out
}
