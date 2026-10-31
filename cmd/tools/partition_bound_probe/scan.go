package main

// scan.go —— 静态侧：逐【SQL 字面量】判定它的 ts 谓词形态。
//
// # 为什么必须是「逐字面量」而不是「逐文件」
//
// R174 轮的口径是**文件级**：「该文件有真实生产读点 且 **整份文件**出现 ts 的范围谓词」。
// ⇒ ⇒ 那个口径会把「同一文件里另一个表的查询」也算进来 ⇒ ⇒ 候选集偏大。
//
// ⇒ 本工具改成**逐字面量**：一个 SQL 字面量一个判定 ⇒ ⇒
//   · 同一文件的不同读点可以分属不同形态；
//   · 「这个字面量读的是不是 canonical 视图」与「它的 ts 谓词是什么形态」**分开判**。
//
// # 三类形态的判据
//
//	param-absolute   —— `ts >= $1`（参数化绝对窗口）⇒ **运行时可折算月份** ⇒ 迁移候选
//	relative         —— `now() - interval '…'`（相对窗口）⇒ **编译期不可知** ⇒ 不是候选
//	literal-absolute —— `ts >= DATE '2026-01-01'`（字面量绝对窗口）⇒ 编译期可知 ⇒ 是候选
//
// ★ 判据只回答「**下界在编译期/运行时能不能被算出来**」，
//   **不回答「加了它会不会更快」** —— 后者只能实测（见 measure 子命令）。

import (
	"encoding/csv"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// ★★ canonical 视图的名字**不硬编码在源码里** —— 它是 `--canonical-view` 传进来的。
//
// 为什么：★ 本仓有一道门（`admin/canonical_read_points_registry_test.go` 的
// `TestCanonicalReadPointRegistryCoversEveryMentionedFile`）会扫**全仓生产 .go**
// 里对 canonical 视图名的提及 ⇒ ⇒ **工具源码里一旦出现那个名字，就会被当成
// 「一个读 canonical 视图的生产文件」** ⇒ ⇒ 而它**不是** ⇒ ⇒ 门会红。
//
// ⇒ ★ 改成「名字由参数传入」之后：★ 门不再误报，★ 而且工具**对任何 canonical 视图都适用**。
var (
	// reCanonicalName / reCanonicalRead 在运行期按视图名构造。
	reCanonicalName *regexp.Regexp
	reCanonicalRead *regexp.Regexp
)

// bindCanonicalView 按给定的视图名构造两个正则。
func bindCanonicalView(viewName string) {
	quoted := regexp.QuoteMeta(viewName)
	reCanonicalName = regexp.MustCompile(`(?i)\b` + quoted + `\b`)
	reCanonicalRead = regexp.MustCompile(`(?i)\b(?:from|join)\s+(?:public\.)?` + quoted + `\b`)
}

var (
	// reParamAbs 判参数化绝对窗口（ts 谓词的比较对象是 $n）。
	//
	// ★★ 占位符有**三种**写法，都要认（★ 漏了任何一种，那个读点就会被判成「无窗口」
	//   ⇒ ⇒ 候选集被低估 ⇒ ⇒ 这是本轮实测到的**第三类形态盲点**）：
	//   ① `$1`   —— pgx 的具名位置参数；
	//   ② `$%d`  —— Go 的 `fmt.Sprintf` 编号参数（★ `admin/provider_models.go:417` 就是这种）；
	//   ③ `${n}` —— 占位插值。
	reParamAbs = regexp.MustCompile(`(?i)(?:\b([a-z_][a-z0-9_]*)\.)?\b([a-z_][a-z0-9_]*)\s*(>=|<=|>|<|between)\s*(?:\$\d+|\$%d|\$\{[a-z_][a-z0-9_]*\}|%d)`)

	// reRelative 判相对窗口。
	//
	// ★★ 相对窗口的下界写法也不止一种（★ 漏了任何一种 ⇒ 那个读点被判成「无窗口」）：
	//   ① `now() - interval '1 day'`（最常见）
	//   ② `now() - $1 * INTERVAL '1 day'`（参数化长度）
	//   ③ `now() - ($2 || ' minutes')::interval`（★ interval 由表达式拼出）
	//   ④ `NOW() - INTERVAL '…'`（大写）
	//   ⑤ `now() - ($1 * INTERVAL '1 day')`（带括号）
	// ⇒ 用「`-` 之后 40 字符内出现 interval」来覆盖 ②③⑤，①④自然也覆盖。
	//   （上界 40 字符 + 禁止跨 `;` ⇒ 不会把下一条语句的 interval 吃进来。）
	reRelative = regexp.MustCompile(`(?i)(?:now\(\)|current_timestamp|current_date)\s*-\s*[^;]{0,40}?interval\b`)
)

// tsColumnLike 是「可能承载时间窗」的列名白名单。
//
// ★ 不是所有 `>= $n` 都是时间窗（也有 `cost_usd >= $1` 这种）⇒
//
//	⇒ 用列名收窄，避免把金额/计数阈值算成「窗口」。
var tsColumnLike = map[string]bool{
	"ts": true, "t0_arrived_at": true, "occurred_at": true, "created_at": true,
	"updated_at": true, "started_at": true, "ended_at": true, "finished_at": true,
	"first_ts": true, "last_ts": true, "request_ts": true, "arrived_at": true,
	"partition_date": true,
}

// literalFact 是一个 SQL 字面量的判定结果。
type literalFact struct {
	File string
	Line int
	// ReadsCanonical 这个字面量是否提到了 canonical 视图。
	ReadsCanonical bool
	// CanonicalRole 是它提到视图的**角色**：
	//   "read"   —— 出现在 FROM/JOIN 位置（真读）
	//   "holder" —— 别处持有视图名（Go 字符串片段，SQL 由别处拼）
	CanonicalRole string
	// Form 是 ts 谓词形态：param-absolute / relative / literal-absolute / none
	Form string
	// Column 是参与窗口判定的列名（若有）。
	Column string
	// Predicate 是命中的谓词原文（截断）。
	Predicate string
	// Digest 是字面量首行的摘要（便于人眼定位）。
	Digest string
}

// runScan 跑静态扫描。
func runScan(argv []string) {
	fs := flag.NewFlagSet("scan", flag.ExitOnError)
	root := fs.String("root", ".", "仓根路径")
	out := fs.String("out", "", "TSV 输出路径（默认 stdout）")
	view := fs.String("canonical-view", os.Getenv("CANONICAL_VIEW"), "★ canonical 视图名（必填；不硬编码在源码里）")
	_ = fs.Parse(argv)
	if *view == "" {
		fmt.Fprintln(os.Stderr, "--canonical-view 必填（★ 视图名不硬编码在源码里 ⇒ 工具对任何视图都适用）")
		os.Exit(2)
	}
	bindCanonicalView(*view)

	facts, err := scanLiterals(*root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "扫描失败: %v\n", err)
		os.Exit(1)
	}
	sort.Slice(facts, func(i, j int) bool {
		if facts[i].File != facts[j].File {
			return facts[i].File < facts[j].File
		}
		return facts[i].Line < facts[j].Line
	})

	w := csv.NewWriter(os.Stdout)
	if *out != "" {
		f, err := os.Create(*out)
		if err != nil {
			fmt.Fprintf(os.Stderr, "写输出失败: %v\n", err)
			os.Exit(1)
		}
		defer f.Close()
		w = csv.NewWriter(f)
	}
	_ = w.Write([]string{"file", "line", "reads_canonical", "canonical_role", "form", "column", "predicate", "digest"})
	byFile := map[string]map[string]int{}
	for _, f := range facts {
		_ = w.Write([]string{
			f.File, strconv.Itoa(f.Line), strconv.FormatBool(f.ReadsCanonical), f.CanonicalRole,
			f.Form, f.Column, f.Predicate, f.Digest,
		})
		if byFile[f.File] == nil {
			byFile[f.File] = map[string]int{}
		}
		byFile[f.File][f.Form]++
	}
	w.Flush()
	if err := w.Error(); err != nil {
		fmt.Fprintf(os.Stderr, "写输出失败: %v\n", err)
		os.Exit(1)
	}

	// 汇总：两种口径都给出来。
	//
	// ★ 口径 A（**逐字面量**）：同一个 SQL 字面量里既有 canonical 视图、又有 param-absolute 窗口。
	//   ⇒ 精确，但会漏掉「视图名与 ts 谓词分处两个字面量」的那种文件。
	// ★ 口径 B（**文件级配对**，= R174 轮的口径）：文件里有读 canonical 的字面量，
	//   且文件里另有 param-absolute 窗口的字面量。
	//   ⇒ 会把「那个窗口其实属于另一张表的查询」也算进来 ⇒ 偏大。
	// ⇒ 两者之差就是**必须人工读源码消歧**的量（R174 的「13 纯 + 1 混合」就是这个口径的产物）。
	type fileFacts struct {
		hasCanonical bool
		forms        map[string]int
		canonicalLit int
	}
	files := map[string]*fileFacts{}
	for _, f := range facts {
		ff := files[f.File]
		if ff == nil {
			ff = &fileFacts{forms: map[string]int{}}
			files[f.File] = ff
		}
		ff.forms[f.Form]++
		if f.ReadsCanonical {
			ff.hasCanonical = true
			ff.canonicalLit++
		}
	}

	var candA, candB []string
	for n, ff := range files {
		if !ff.hasCanonical {
			continue
		}
		if ff.forms["param-absolute"] > 0 {
			candB = append(candB, n)
		}
	}
	for _, f := range facts {
		if f.ReadsCanonical && f.Form == "param-absolute" {
			candA = append(candA, f.File)
		}
	}
	candA = dedupSorted(candA)
	candB = dedupSorted(candB)

	fmt.Fprintf(os.Stderr, "== 口径 A（逐字面量：同一字面量内既有视图又有参数化窗口）= %d 个文件\n", len(candA))
	for _, n := range candA {
		fmt.Fprintf(os.Stderr, "   %s\n", n)
	}
	fmt.Fprintf(os.Stderr, "== 口径 B（文件级配对，= R174 轮口径）= %d 个文件\n", len(candB))
	for _, n := range candB {
		fmt.Fprintf(os.Stderr, "   %s\n", n)
	}
	onlyB := diff(candB, candA)
	fmt.Fprintf(os.Stderr, "== 只在口径 B（★ 必须人工读源码消歧）= %d 个文件\n", len(onlyB))
	for _, n := range onlyB {
		fmt.Fprintf(os.Stderr, "   %s\n", n)
	}
}

// dedupSorted 去重并排序。
func dedupSorted(in []string) []string {
	m := map[string]bool{}
	for _, s := range in {
		m[s] = true
	}
	out := make([]string, 0, len(m))
	for s := range m {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// diff 返回 a 里有、b 里没有的。
func diff(a, b []string) []string {
	m := map[string]bool{}
	for _, s := range b {
		m[s] = true
	}
	var out []string
	for _, s := range a {
		if !m[s] {
			out = append(out, s)
		}
	}
	return out
}

// scanLiterals 扫生产 .go 文件里的 SQL 字符串字面量。
func scanLiterals(root string) ([]literalFact, error) {
	var facts []literalFact
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			base := filepath.Base(path)
			if base == "vendor" || base == "node_modules" || base == ".git" ||
				base == "tools" || base == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		// cmd/tools/ 下的是审计工具本身（含旧名的口径定义），不是生产读点。
		if strings.HasPrefix(rel, "cmd/tools/") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
		if err != nil {
			return nil // 解析不了的文件不致命；跳过
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			text, err := strconv.Unquote(lit.Value)
			if err != nil || text == "" {
				return true
			}
			pos := fset.Position(lit.Pos())
			fact := literalFact{File: rel, Line: pos.Line}
			fact.ReadsCanonical = reCanonicalName.MatchString(text)
			fact.CanonicalRole = canonicalRole(text)
			fact.Form, fact.Column, fact.Predicate = classifyTsPredicate(text)
			fact.Digest = digest(text)
			facts = append(facts, fact)
			return true
		})
		return nil
	})
	return facts, err
}

// canonicalRole 判「这个字面量是以什么角色提到 canonical 视图的」。
func canonicalRole(s string) string {
	if reCanonicalRead.MatchString(s) {
		return "read"
	}
	if reCanonicalName.MatchString(s) {
		return "holder"
	}
	return ""
}

// looksLikeSQL 粗判一个字符串是不是 SQL（只收明显的）。
func looksLikeSQL(s string) bool {
	low := strings.ToLower(s)
	if !strings.Contains(low, "select") {
		return false
	}
	return strings.Contains(low, " from ") || strings.Contains(low, "\nfrom ") ||
		strings.Contains(low, "from public.") || strings.Contains(low, "from session")
}

// classifyTsPredicate 判定一个 SQL 字面量的 ts 谓词形态。
func classifyTsPredicate(sql string) (form string, column string, predicate string) {
	// ① 参数化绝对窗口：`col >= $n`
	if m := reParamAbs.FindStringSubmatch(sql); m != nil {
		col := strings.ToLower(m[2])
		if tsColumnLike[col] {
			return "param-absolute", col, m[0]
		}
	}
	// ② 相对窗口：`now() - interval '…'`
	if m := reRelative.FindString(sql); m != "" {
		return "relative", "", m
	}
	// ③ 字面量绝对窗口：`col >= DATE '2026-01-01'` / `col >= '2026-01-01'::date`
	if m := reLiteralAbs.FindStringSubmatch(sql); m != nil {
		return "literal-absolute", strings.ToLower(m[1]), m[0]
	}
	return "none", "", ""
}

// reLiteralAbs 判字面量绝对窗口。
var reLiteralAbs = regexp.MustCompile(`(?i)\b([a-z_][a-z0-9_]*)\s*(>=|<=|>|<|between)\s*(?:date|timestamp)\s*'`)

// digest 取字面量的首行摘要。
func digest(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 160 {
		s = s[:160] + "…"
	}
	return s
}
