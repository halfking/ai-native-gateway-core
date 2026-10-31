package admin

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// R89-DR（207 号）：把 `view_with_null_padded` 族的**判据强弱**做成可见的。
//
// 206 号换掉 `admin/memora_handlers.go` 的裸读之后，族计数实测位移是
// `view_with_null_padded` 13 → **14**，而该文件的触发是**假阳性**：补位列 `id`
// 的全部命中是 `:107-108` 的注释散文与 `:168`/`:663` 的 **JSON 响应键**，
// 而那条读点只产出 `COUNT(*)` 与 `client_model`。
//
// 根因在 `nullPaddedPredicateHit`：它只要求补位列名**出现在同一个 span 内**
// （函数体或包级字面量），**不要求出现在 SQL 里**。span 是函数体 ⇒ 里面既有
// SQL 字面量、也有 Go 代码（map 键、结构体字段、注释），而 `\bid\b` 分不清
// `client_model` 那种 SQL 列引用与 `"id": fact.ID` 那种 JSON 键。
//
// ⚠️ **本测试不改分类器**，理由见 206 号 §六 + playbook §116：它的**保守方向
// 是刻意选的**（`:1386-1387`「宁可留在本族并要求具名论证，也不要把一个真触发
// 悄悄放出族外」），把判据收紧必须**先证不会放进真触发**（playbook §106）——
// 而「哪些是真触发」正是本测试要测量的东西，在测量完成前收紧是循环论证。
// 本测试的职责是：**把每一条命中的证据暴露出来**，让「具名论证」有东西可依。
//
// 判据分层（刻意分层，不合并）：
//
//	span 级 = 现有分类器用的那层（函数体里出现过列名）——**不改动**；
//	字面量级 = 列名出现在**一个形似 SQL 的字符串字面量**里。
//
// 「形似 SQL」的判据是字面量正文含 SQL 关键字（SELECT/FROM/WHERE/…）。这条
// 判据是**保守**的：它会漏掉「SQL 由多个字面量拼接、而含列名的那片碎片本身
// 没有关键字」的形态。
//
// ★ R170 更新见证：本门原引用的真触发 `bg/shared_pick.go:82-96` **已不成立**
//
//	—— 该文件在 R170 改成读会话族原生源，不再提 canonical 视图 ⇒
//	⇒ 它的命中不再来自「读视图 + 补位列」的组合。
//	⇒ 新的见证是 `admin/usage_enhanced.go`：视图名在 `"BaseTable"` 配置常量串
//	（**不含 SQL 关键字** ⇒ 不是 SQL 形字面量），`id` 在 `" LEFT JOIN providers
//	p ON p.id = ul.provider_id"` 片段里（**不含视图名**）⇒
//	⇒ 「同一片字面量里既有视图名又有补位列」这条逐字面量判据对它**恒不命中**。
//
// ⇒ 所以 `non-sql-only` **是「值得人看一眼」而不是「已证假阳性」**。
var nullPaddedSqlShapedRE = regexp.MustCompile(`(?i)\b(SELECT|FROM|WHERE|GROUP\s+BY|JOIN|ORDER\s+BY|LIMIT|INSERT|UPDATE|DELETE)\b`)

// nullPaddedLiteralEvidence 报告：在**形似 SQL 的字符串字面量**里命中的补位列。
// 返回 (命中的列, 命中的字面量数)。解析失败时返回 ok=false（**不**静默当成
// 「无证据」——那正是 201 号 §102 记的那类恒真陷阱）。
func nullPaddedLiteralEvidence(raw string, cols []string) (hits []string, literals int, ok bool) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "src.go", raw, 0)
	if err != nil {
		return nil, 0, false
	}
	seen := map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		bl, ok := n.(*ast.BasicLit)
		if !ok || bl.Kind != token.STRING {
			return true
		}
		text, uerr := strconv.Unquote(bl.Value)
		if uerr != nil {
			// 反引号原始字符串 strconv.Unquote 也支持；失败则退回去引号的
			// 朴素截断，并**不算**证据（宁可少报也不误报）。
			return true
		}
		if !nullPaddedSqlShapedRE.MatchString(text) {
			return true
		}
		literals++
		for _, col := range cols {
			if nullPaddedColWordRE(col).MatchString(text) && !seen[col] {
				seen[col] = true
				hits = append(hits, col)
			}
		}
		return true
	})
	sort.Strings(hits)
	return hits, literals, true
}

// TestNullPaddedFamilyHitsAreVisible 把该族每一条命中的**证据分层**打出来。
//
// 覆盖下限必带：判据是「集合相等/非空」时，**空集合同样满足**「没有可疑命中」
// ⇒ 抽取器坏掉就安静通过（201 号 §102）。所以这里同时断言
// ① 该族**非空**（实测 14）、② 分类表**非空**、③ 报告覆盖了**全部**该族文件。
func TestNullPaddedFamilyHitsAreVisible(t *testing.T) {
	root := repoRootFromCaller(t)

	if len(sessionArmNullPaddedColumns) == 0 {
		t.Fatalf("补位列清单为空（实测 6 列，由 db.RequestLogsViewPaddedSessionColumns() 派生）：" +
			"判据退化成「没有命中」，下面的报告与结论全部无效")
	}
	// ★ 890 之后（2026-10-20）：**分类表为空是正确状态**，不是「无分类可报」。
	//
	// 原来这里是一条地板（分类表为空 ⇒ Fatal），因为那时停写分级表覆盖了全部
	// v1 读方。890 之后 v1 族被物理删除 ⇒ 读方总体归零 ⇒ 分级表随之清空。
	//
	// ⇒ 地板翻成：**报告必须为空**（没有 v1 读方 ⇒ 没有 null-padded 族的成员），
	// 而「抽取器还有效」这条能力由下面的**阳性对照**守住。
	//
	// ⚠ 没有阳性对照的话，「报告为空」与「抽取器坏了」无法区分 —— 那正是
	// 201 号 §102 记的那类恒真陷阱。
	if len(requestLogsStopWriteClassification) == 0 {
		t.Logf("★ 890 之后停写分级表为空（v1 族已物理删除）—— 这是正确状态，" +
			"本门改守「报告为空 + 抽取器仍有效」。")
	}

	cols := make([]string, 0, len(sessionArmNullPaddedColumns))
	for c := range sessionArmNullPaddedColumns {
		cols = append(cols, c)
	}
	sort.Strings(cols)

	type row struct {
		file     string
		via      string // 现有分类器给的触发列
		litCols  []string
		litCount int
		parseOK  bool
	}
	var rows []row
	for _, file := range allKnownRequestLogsReaderFiles(t) {
		raw, err := os.ReadFile(filepath.Join(root, file))
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		src := string(raw)
		hit, via := nullPaddedPredicateHit(src)
		if !hit {
			continue
		}
		fam := sourceFamilyOf(src, isSwitchConsumerFile(t, root, file))
		if ind, ok := indirectRequestLogsReaders[file]; ok && ind.Family != "" {
			fam = ind.Family
		}
		if fam != familyViewNullPadded {
			continue
		}
		litCols, litCount, ok := nullPaddedLiteralEvidence(src, cols)
		rows = append(rows, row{file: file, via: via, litCols: litCols, litCount: litCount, parseOK: ok})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].file < rows[j].file })

	// ★ 890 之后（2026-10-20）：报告**必须为空**（没有 v1 读方 ⇒ 没有该族成员）。
	//
	// 原来这里是一条「报告不得为空（实测 14）」的下限。890 之后该族没有成员。
	// ⇒ 下限翻成：**空必须是因为抽取器还有效**，由下面的阳性对照证明。
	if len(rows) > 0 {
		var names []string
		for _, r := range rows {
			names = append(names, r.file)
		}
		sort.Strings(names)
		t.Errorf("`%s` 族仍有 %d 个成员 —— 890 之后没有 v1 读方：\n  %s",
			familyViewNullPadded, len(rows), strings.Join(names, "\n  "))
	}

	// ★ 阳性对照：两个抽取器仍然有效（合成源码）。
	//
	// ⚠ 没有这一条，「报告为空」与「抽取器坏了」无法区分 —— 201 号 §102 记的
	// 恒真陷阱。合成样本刻意取一个**补位列**放在形似 SQL 的字面量里。
	probeCol := ""
	for c := range sessionArmNullPaddedColumns {
		probeCol = c
		break
	}
	if probeCol == "" {
		t.Fatal("补位列清单为空 —— 无法做阳性对照")
	}
	synth := "package synth\n\nfunc q() string {\n\treturn `SELECT " + probeCol +
		" FROM request_logs_with_current_month rl WHERE " + probeCol + " IS NOT NULL`\n}\n"
	hit, via := nullPaddedPredicateHit(synth)
	if !hit {
		t.Errorf("阳性对照失败：nullPaddedPredicateHit 在合成源码上没命中补位列 %q —— "+
			"抽取器坏了，本门「报告为空」是空洞的", probeCol)
	} else if via != probeCol {
		t.Errorf("阳性对照失败：nullPaddedPredicateHit 的 via = %q，期望 %q", via, probeCol)
	}
	litHits, litCount, ok := nullPaddedLiteralEvidence(synth, []string{probeCol})
	if !ok {
		t.Error("阳性对照失败：nullPaddedLiteralEvidence 解析合成源码失败 —— 抽取器坏了")
	} else if litCount == 0 || len(litHits) != 1 || litHits[0] != probeCol {
		t.Errorf("阳性对照失败：nullPaddedLiteralEvidence 在合成源码上得到 hits=%v literals=%d，"+
			"期望恰好命中 %q", litHits, litCount, probeCol)
	}
	if fam := sourceFamilyOf(synth, false); fam != familyViewNullPadded {
		t.Errorf("阳性对照失败：sourceFamilyOf 把合成源码判成 %q，期望 %q —— 族分类器坏了",
			fam, familyViewNullPadded)
	}

	var sqlEvidence, needsReview, parseFailed []string
	var b strings.Builder
	b.WriteString("补位列命中证据分层（span 级 = 现有分类器用的那层；字面量级 = **via 列本身**出现在形似 SQL 的字面量里）\n")
	for _, r := range rows {
		// ⚠️ 判据必须与被测量对象**同源**：拿 `via` 列自己去问字面量级证据，
		// 不能问「文件里任一补位列有没有字面量证据」——第一版就是这么写的，
		// 结果 admin/credential_monitor_heatmap.go 的 via 是
		// credits_rate_multiplier（只出现在**注释**里），却被它 SQL 字面量中
		// **另一列** `id` 的存在判成了 SQL-LIT。那是拿 A 的证据替 B 背书。
		viaHasLiteral := false
		for _, c := range r.litCols {
			if c == r.via {
				viaHasLiteral = true
			}
		}
		switch {
		case !r.parseOK:
			parseFailed = append(parseFailed, r.file)
			b.WriteString("  PARSE-FAIL   " + r.file + "  via=" + r.via + "  ← 解析失败，不得当成「无证据」\n")
		case viaHasLiteral:
			sqlEvidence = append(sqlEvidence, r.file)
			b.WriteString("  SQL-LIT      " + r.file + "  via=" + r.via + "\n")
		default:
			needsReview = append(needsReview, r.file)
			b.WriteString("  NON-SQL-ONLY " + r.file + "  via=" + r.via +
				"  ← via 列只在 Go 代码/注释里出现（文件内其他补位列的字面量级证据：" +
				strings.Join(r.litCols, ",") + "）\n")
		}
	}
	b.WriteString(fmtCounts("SQL-LIT", len(sqlEvidence)) +
		fmtCounts("NON-SQL-ONLY", len(needsReview)) +
		fmtCounts("PARSE-FAIL", len(parseFailed)))
	t.Logf("\n%s", b.String())

	// ── 棘轮：NON-SQL-ONLY 必须逐条具名 ──────────────────────────────────
	//
	// 为什么只有这一半能变成致命断言：判据是**单向可证的** —— 「该列在**任何**
	// 形似 SQL 的字面量里都没出现过」⇒ 它在本文件里**不可能**被任何 SQL 语句
	// 引用 ⇒ 作为「谓词触发」必是假阳性。**不存在假阴性方向**（playbook §106
	// 的前置条件因此满足），所以把它升为致命是安全的。
	//
	// 反过来，SQL-LIT **不能**升为「已证真触发」：`top_problems.go:195` 的
	// `c.id`、`usage.go:341/456/460` 的 `ak.id`/`c.id`/`p.id`、
	// `probe_history.go:110-111/540` 的 `c.id`/`p.id`/`mc.id`、
	// `credential_monitor.go:283/306/333/336` 的 `c.id`/`p.id` —— 全是**别的表**
	// 的 id 列，而补位列 `id` 在会话臂恒 NULL（裁决见
	// db/request_logs_view_padded_columns.go）。**「出现在 SQL 里」与「是视图的
	// 那一列」是两回事**，本门分不出来，所以不在这半边下结论。
	//
	// 另一半（跨表 `id`）本轮**已逐个打开确认存在**，但**没有**全部 10 个都查，
	// 因此不登记成清单、也不下结论——那需要把判据升到「列-表绑定级」，
	// 而那要先证不会放进真触发（playbook §106）。已确认的跨表实例：
	// admin/top_problems.go:195（c.id = rl.credential_id，c 是 credentials）、
	// admin/usage.go:341/342/456/457/460（ak.id / app.id / c.id / p.id）、
	// admin/probe_history.go:110-111/167/232/346/378/540（c.id / p.id / mc.id / npr.id，
	// 视图那条 SQL 在 :480 只用 outbound_model/credential_id/ts/…）、
	// admin/credential_monitor.go:283/306/329/333/336/344/367/360（c.id / p.id，
	// 对视图的关联子查询 :360 只用 rl.credential_id）。
	declared := map[string]bool{}
	for p := range nullPaddedNonSqlOnlyJustified {
		declared[p] = true
	}
	got := map[string]bool{}
	for _, p := range needsReview {
		got[p] = true
	}

	var undeclared []string
	for p := range got {
		if !declared[p] {
			undeclared = append(undeclared, p)
		}
	}
	sort.Strings(undeclared)
	for _, p := range undeclared {
		t.Errorf("补位列命中在**任何 SQL 字面量里都不出现**（%s 此前未登记）：\n"+
			"  它不可能被本文件的任何 SQL 语句引用 ⇒ 作为「谓词级空」触发必是假阳性，"+
			"而 `sourceFamilyOf` 仍会把它分进 %s 族。\n"+
			"  处置二选一：(a) 若它确实是假阳性，把它登记进 "+
			"nullPaddedNonSqlOnlyJustified 并在理由里写清命中位置；"+
			"(b) 若它其实**是**真触发，说明「形似 SQL」这个判据漏了它"+
			"（例如 SQL 由多片拼接且含列名的那片本身没有 SQL 关键字），"+
			"请把该形态登记进本门并放宽判据——**不要**直接把它从族里踢出去。",
			p, familyViewNullPadded)
	}

	var stale []string
	for p, why := range nullPaddedNonSqlOnlyJustified {
		if !got[p] {
			stale = append(stale, p+"（理由：登记为假阳性，但实测已能在 SQL 字面量里看到该列）")
			continue
		}
		if strings.TrimSpace(why) == "" {
			t.Errorf("nullPaddedNonSqlOnlyJustified[%q] 的理由为空：具名论证不能是空串", p)
		}
	}
	sort.Strings(stale)
	for _, s := range stale {
		t.Errorf("nullPaddedNonSqlOnlyJustified 里有条目已不再成立（移除即可）：%s", s)
	}
}

// nullPaddedNonSqlOnlyJustified 登记「via 列在**任何**形似 SQL 的字面量里都不
// 出现」的命中——即**已证**的假阳性。键是文件，值是带命中位置的具名理由。
//
// 空理由 = 没登记 = 门会红。这不是为了给假阳性开后门，而是让**「这一族里
// 哪些命中是噪音」变成一份有名有姓的清单**：本门**不能**判 SQL-LIT 那一半
// （跨表 `id` 分不出来），所以这一半也不能自动接受。
var nullPaddedNonSqlOnlyJustified = map[string]string{
	// ★ 2026-10-20（迁移 890，v1 族真删）：本表**已清空**。
	//
	// 五条登记的对象都是「被分类器分进 reads_view_with_null_padded_predicate 族的
	// 文件」——而 890 之后 **没有任何生产文件再读 v1** ⇒ 那个族**没有成员**
	// ⇒ 「已证假阳性」的清单随之清空。
	//
	// ⚠ 本表**保留**（不删）：它守的是「凡被本族收下的命中，若其 via 列在任何
	// 形似 SQL 的字面量里都不出现，就必须逐条具名」这条规矩。
}

func fmtCounts(label string, n int) string {
	return "  —— " + label + " = " + itoaTest(n) + "\n"
}

func itoaTest(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
