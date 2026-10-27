package partguard

// 本文件补上 partguard 的第二类盲区：**目标表名在运行时决定**的 DML。
//
// 背景（R169 实测坐实）：scan.go 的字面量扫描器只看字符串字面量，且要求
// 表名紧跟 UPDATE/DELETE/TRUNCATE。于是下面这四种写法里的表名它一个都看不见：
//
//	`UPDATE ` + tbl + ` SET …`                      // Go 字符串拼接
//	fmt.Sprintf("UPDATE %s SET …", tbl)              // 占位符
//	`DELETE FROM ` + tbl + ` WHERE …`
//	`TRUNCATE ` + tbl
//
// 而仓内**确实存在**这类写法对分区父表的真实写（父表 = hot+columnar 迁移后
// citus-columnar 引擎会直接拒绝 UPDATE/DELETE）：
//
//	domains/session/v2/turn_writer.go:691          每个 turn 落库后回填压缩字段
//	domains/session/v2/session_aggregator.go:485   聚合 task_type 回填
//	internal/sessionv2mirror/abandoned_turn.go:212 每个失败请求
//	internal/sessionv2mirror/final_success_turn.go:234 每个成功请求
//
// ⇒ 在本文件之前，那道门是**绿的**，而红线已被违反——这正是 R78 门自己
// 声明要挡的「第 4 条」。门看不见 ⇒ 挡不住。
//
// ── 手法（照仓内既有先例，不另创一套）────────────────────────────────────
// domains/session/v2/session_turns_dead_column_test.go 已经为同一族问题
// 做过三件事，本文件照抄其形态：
//  1. 拼接还原：把 `+` 表达式树还原成一段文本，非字符串部分替换成占位符；
//  2. 占位符形态：占位符紧跟在语句关键字之后 ⇒ 判为「表名在运行时决定」；
//  3. 归属：只有在**同文件邻近**出现目标表的字面量时才归属——保守归属，
//     宁可漏也不误报（先例同款取舍）。
//
// 为什么归属要保守：动态目标**本身**不可静态判定，把它算成某个父表的写是
// 一种推测。推测若被门当成事实，就会出现「门红了但其实不是违规」的噪声，
// 而噪声会让下一位维护者开始忽略这道门。宁可少认、不可乱认。
//
// ★ 与死列门的一处**刻意不同**：死列门只关心 session_turns 两张表；本门
// 关心**全部分区父表**（partitionParents）。归属表名因此要跟父表集合求交，
// 而不是跟固定的两张表求交。

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
)

// dynamicMarker 是「目标表名在此处由运行时决定」的占位符。
//
// 用 \x00 而不是先例里的 "<dynamic>"：占位符要出现在**正则的表名位置**上，
// 而 "<dynamic>" 里含字母，会被随后的标识符捕获组吃掉（"dynamic" 会被当成
// 表名）。用不可见字符就没有这个歧义。
const dynamicMarker = "\x00"

// dynamicStmtPatterns 是「语句关键字后面紧跟占位符」的形态表。
//
// 形态与 scan.go 的 stmtPatterns 对齐（同样只认 UPDATE/DELETE/TRUNCATE），
// 差别只在于表名位置不是标识符而是占位符。
var dynamicStmtPatterns = []struct {
	stmt string
	re   *regexp.Regexp
}{
	{"UPDATE", regexp.MustCompile(`(?i)UPDATE\s+(?:ONLY\s+)?(?:public\.)?` + dynamicMarker)},
	{"DELETE", regexp.MustCompile(`(?i)DELETE\s+FROM\s+(?:ONLY\s+)?(?:public\.)?` + dynamicMarker)},
	{"TRUNCATE", regexp.MustCompile(`(?i)TRUNCATE\s+(?:TABLE\s+)?(?:ONLY\s+)?(?:public\.)?` + dynamicMarker)},
}

// formatVerbRE 是 fmt.Sprintf 的占位符动词。%d/%f 等数值动词不参与——
// 表名不可能是数字，纳入只会制造噪声。
var formatVerbRE = regexp.MustCompile(`%[svq]`)

// 归属层级。写进 DynamicSite.Tier，好让人工复核时知道「这条归属是怎么来的」。
const (
	tierRange    = "range"     // Tier 0：for … range []string{…} 的字面量表名集合
	tierCallSite = "call-site" // Tier 1：函数参数的同文件调用点实参
	tierWindow   = "window"    // Tier A：邻近 ±20 行字面量（保守兜底）
	tierNone     = "none"      // 未归属到任何表
)

// DynamicSite 描述一处「目标表名在运行时决定」的 DML 站点。
//
// Tables 是**归属**结果：解析到的可能目标表里，属于分区父表的那部分。
// 空切片 = 未归属到任何父表（可能是普通表的动态写，也可能是一个我们
// 看不见的父表写——两种都必须在门里显式登记，见 allowedDynamicDML）。
type DynamicSite struct {
	File   string
	Line   int
	Stmt   string
	Tables []string
	Tier   string
}

// sqlPart 是拼接还原后的一个片段：要么是字面量文本，要么是动态部分。
type sqlPart struct {
	text     string
	dynamic  bool
	ident    string // 动态部分若是裸标识符，记住它的名字（供 Tier 0/1 解析）
	baseLine int    // 该片段在源文件里的起始行（1-based）
}

// joinedSQL 是还原后的文本 + 各片段在文本中的起始偏移。
//
// 为什么需要偏移表：Go 源码里的行号与 SQL 文本里的行号无关（SQL 可能是
// 一个跨行的 raw string）。要把「命中位置」换算成「源码行号」，必须知道
// 命中落在哪个片段、该片段里前面有多少个换行——这正是 baseLine 的用途。
type joinedSQL struct {
	text    string
	parts   []sqlPart
	offsets []int
}

// lineAt 把文本偏移换算成源码行号。
func (j joinedSQL) lineAt(off int) int {
	for i, p := range j.parts {
		start := j.offsets[i]
		end := start + len(p.text)
		if off >= start && off <= end {
			return p.baseLine + strings.Count(p.text[:off-start], "\n")
		}
	}
	return 0
}

// idents 返回该文本里携带的标识符名（动态表名参数的线索）。
//
// ★ 不要求片段本身是 dynamic：fmt.Sprintf 路径的标识符挂在**字面量片段**上
//
//	（格式串是字面量，占位符动词在文本里，参数名要单独记）。
func (j joinedSQL) idents() []string {
	var out []string
	for _, p := range j.parts {
		if p.ident != "" {
			out = append(out, p.ident)
		}
	}
	return out
}

// joinParts 把片段拼成一段文本。
func joinParts(parts []sqlPart) joinedSQL {
	var b strings.Builder
	offsets := make([]int, 0, len(parts))
	for _, p := range parts {
		offsets = append(offsets, b.Len())
		if p.dynamic {
			b.WriteString(dynamicMarker)
			continue
		}
		b.WriteString(p.text)
	}
	return joinedSQL{text: b.String(), parts: parts, offsets: offsets}
}

// restoreConcat 把 Go 的 `+` 拼接表达式还原成片段序列。
//
// 非字符串的部分（变量、函数调用、字段选择）替换成动态片段——它是「表名在
// 运行时决定」的标记，扫描器据此把这条语句归到动态一类。
//
// ★ 只处理 `+`。`fmt.Sprintf` 走另一条路径（restoreFormatString）：它的
// 格式串是**单个**字面量，参数不进文本，只能靠占位符动词标记。
func restoreConcat(e ast.Expr, fset *token.FileSet) ([]sqlPart, bool) {
	switch v := e.(type) {
	case *ast.BasicLit:
		if v.Kind != token.STRING {
			return nil, false
		}
		s, err := strconv.Unquote(v.Value)
		if err != nil {
			return nil, false
		}
		return []sqlPart{{text: s, baseLine: fset.Position(v.Pos()).Line}}, true
	case *ast.BinaryExpr:
		if v.Op != token.ADD {
			return nil, false
		}
		l, okL := restoreConcat(v.X, fset)
		if !okL {
			return nil, false
		}
		r, okR := restoreConcat(v.Y, fset)
		if !okR {
			return nil, false
		}
		return append(l, r...), true
	case *ast.ParenExpr:
		return restoreConcat(v.X, fset)
	case *ast.Ident:
		return []sqlPart{{dynamic: true, ident: v.Name}}, true
	case *ast.CallExpr, *ast.SelectorExpr, *ast.IndexExpr,
		*ast.StarExpr, *ast.UnaryExpr:
		return []sqlPart{{dynamic: true}}, true
	}
	return nil, false
}

// restoreFormatString 取 fmt.Sprintf 的格式串，并把 %s/%v/%q 换成占位符。
//
// 只认 fmt.Sprintf（不是任何"第一个参数是字符串"的调用）：别的格式化函数
// 的占位符语义不由我们定义，猜错会造出假动态站点。
//
// 第二个实参若是裸标识符，记下它的名字——那是"表名从哪来"的唯一线索。
func restoreFormatString(call *ast.CallExpr, fset *token.FileSet) (joinedSQL, bool) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return joinedSQL{}, false
	}
	x, ok := sel.X.(*ast.Ident)
	if !ok || x.Name != "fmt" || sel.Sel.Name != "Sprintf" {
		return joinedSQL{}, false
	}
	if len(call.Args) == 0 {
		return joinedSQL{}, false
	}
	lit, ok := call.Args[0].(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return joinedSQL{}, false
	}
	s, err := strconv.Unquote(lit.Value)
	if err != nil {
		return joinedSQL{}, false
	}
	if !strings.Contains(s, "%") {
		return joinedSQL{}, false
	}
	marked := formatVerbRE.ReplaceAllString(s, dynamicMarker)
	part := sqlPart{text: marked, baseLine: fset.Position(lit.Pos()).Line}
	parts := []sqlPart{part}
	// 第一个 %s 对应 args[1] ⇒ 那是表名参数的通常位置。
	if len(call.Args) > 1 {
		if id, ok := call.Args[1].(*ast.Ident); ok {
			parts[0].ident = id.Name
		}
	}
	return joinedSQL{text: marked, parts: parts, offsets: []int{0}}, true
}

// dynamicHit 是一次动态 DML 命中。
type dynamicHit struct {
	stmt string
	off  int // 命中在文本中的起始偏移
}

// matchDynamic 找出文本里全部「语句关键字 + 占位符」的命中。
//
// 命中可能不止一处：一条 SQL 文本里可以既有 UPDATE 又有 DELETE（CTE 形态）。
// 全部报出来，别只报第一个——只报第一个会让门少看见一条。
func matchDynamic(text string) []dynamicHit {
	var hits []dynamicHit
	for _, p := range dynamicStmtPatterns {
		for _, loc := range p.re.FindAllStringIndex(text, -1) {
			hits = append(hits, dynamicHit{stmt: p.stmt, off: loc[0]})
		}
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].off < hits[j].off })
	return hits
}

// attributionWindow 是 Tier A（邻近窗口）的半径（行）。
//
// 与死列门同款（那边是 ±20 行）。窗口太窄会漏掉"表名列表在别处"的写法，
// 太宽会把不相关的父表名拉进来——实测两种都发生过：
//
//	过窄：domains/session/v2/session_digest_backfill.go 的调用点在窗口外 1 行
//	      （:436 传 "public.session_turns"，窗口下界是 :437）⇒ 漏。
//	过宽：domains/session/v2/session_aggregator.go 的动态站点只写 turns 两张表，
//	      窗口里却有同文件另一条写 sessions 的 SQL ⇒ 误归属。
//
// ⇒ 窗口只能是**兜底**，精确解析（Tier 0/1）优先。
const attributionWindow = 20

// normalizeTable 把表名归一成不带 schema 的小写形式。
func normalizeTable(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	s = strings.Trim(s, `"`)
	if i := strings.LastIndex(s, "."); i >= 0 {
		s = s[i+1:]
	}
	return s
}

// collectRangeTables 收集「for … x := range []string{…}」这类可直接读出
// 表名集合的绑定（Tier 0）。
//
// ★ 作用域是**当前函数**，不是整个文件：同一个标识符名在两个函数里可以
//
//	指不同的表（partition_manager.go 的两个函数都叫 `table`：一个是
//	request_stats_minute 遍历变量，一个是 stats_event_inbox 的形参）⇒
//	文件级映射会把两者混为一谈，并**丢掉**真正的父表归属。
//
// 只收**纯字面量**列表：列表里混了变量就整体放弃（半解析的集合比不解析更
// 危险——它看起来像精确解）。
func collectRangeTables(node ast.Node) map[string][]string {
	out := map[string][]string{}
	if node == nil {
		return out
	}
	ast.Inspect(node, func(n ast.Node) bool {
		rs, ok := n.(*ast.RangeStmt)
		if !ok {
			return true
		}
		lit, ok := rs.X.(*ast.CompositeLit)
		if !ok {
			return true
		}
		var names []string
		for _, el := range lit.Elts {
			bl, ok := el.(*ast.BasicLit)
			if !ok || bl.Kind != token.STRING {
				return true // 不是纯字面量表名列表 ⇒ 不解析
			}
			s, err := strconv.Unquote(bl.Value)
			if err != nil {
				return true
			}
			names = append(names, normalizeTable(s))
		}
		if len(names) == 0 {
			return true
		}
		var idents []*ast.Ident
		if rs.Value != nil {
			if id, ok := rs.Value.(*ast.Ident); ok {
				idents = append(idents, id)
			}
		}
		if rs.Key != nil {
			if id, ok := rs.Key.(*ast.Ident); ok {
				idents = append(idents, id)
			}
		}
		for _, id := range idents {
			if id.Name == "_" {
				continue
			}
			out[id.Name] = append(out[id.Name], names...)
		}
		return true
	})
	return out
}

// paramIndex 返回标识符在函数形参里的序号；不在形参里则返回 -1。
func paramIndex(fn *ast.FuncDecl, ident string) int {
	if fn == nil || fn.Type == nil || fn.Type.Params == nil {
		return -1
	}
	idx := 0
	for _, field := range fn.Type.Params.List {
		for _, name := range field.Names {
			if name.Name == ident {
				return idx
			}
			idx++
		}
	}
	return -1
}

// callSiteTables 收集「函数参数当表名」时，同文件调用点传入的**字面量**
// 实参（Tier 1）。
//
// 为什么需要它：partition_manager 的 deleteTerminalRowsBatched 与
// session_digest_backfill 的 execDigestUpdate 都是"表名由调用方给"的形状，
// 函数体里没有任何表名字面量 ⇒ 只能从调用点回推。
func callSiteTables(f *ast.File, fn *ast.FuncDecl, ident string) []string {
	if fn == nil {
		return nil
	}
	idx := paramIndex(fn, ident)
	if idx < 0 {
		return nil
	}
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch callee := call.Fun.(type) {
		case *ast.Ident:
			if callee.Name != fn.Name.Name {
				return true
			}
		case *ast.SelectorExpr:
			if callee.Sel.Name != fn.Name.Name {
				return true
			}
		default:
			return true
		}
		if idx >= len(call.Args) {
			return true
		}
		bl, ok := call.Args[idx].(*ast.BasicLit)
		if !ok || bl.Kind != token.STRING {
			return true
		}
		s, err := strconv.Unquote(bl.Value)
		if err != nil {
			return true
		}
		out = append(out, normalizeTable(s))
		return true
	})
	return out
}

// attributeWindow 是 Tier A：在**同文件邻近**找出出现过的分区父表。
//
// 判据是表名以标识符形态出现在窗口文本里，不是"这个文件里出现过"——
// 后者会把整个文件里任何一次提及都算进来。
func attributeWindow(srcLines []string, line int, parentSet map[string]bool) []string {
	lo := line - attributionWindow - 1
	if lo < 0 {
		lo = 0
	}
	hi := line + attributionWindow
	if hi > len(srcLines) {
		hi = len(srcLines)
	}
	if lo >= hi {
		return nil
	}
	window := strings.Join(srcLines[lo:hi], "\n")
	var out []string
	for p := range parentSet {
		re := regexp.MustCompile(`(?i)\b(?:public\.)?` + regexp.QuoteMeta(p) + `\b`)
		if re.MatchString(window) {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

// dynCtx 是解析动态目标所需的上下文。
type dynCtx struct {
	f           *ast.File
	fset        *token.FileSet
	srcLines    []string
	rangeTables map[string][]string // 仅当前函数内的 range 字面量表绑定（Tier 0）
	parentSet   map[string]bool
	fn          *ast.FuncDecl // 站点所在函数（Tier 1 用）
}

// resolve 解析一个动态站点的可能目标表。
//
// 三级优先：Tier 0（range 字面量表）→ Tier 1（调用点实参）→ Tier A（邻近窗口）。
// 前两级是**精确**的（目标表集合可枚举），第三级是保守超集。
//
// ★★ 关键语义：**「解析成功且集合里没有父表」= 该站点不写父表**，
//
//	此时**不得**再退回 Tier A。R169 变异臂 M2 实测抓到过这个缺陷：
//	把 {hot, parent} 改成 {hot} 后，Tier A 窗口仍从邻近注释里捞到
//	"session_turns" ⇒ 门照样绿 ⇒ 门对「把父表目标删掉」这件事完全失明。
//	⇒ Tier A 只在**完全解析不出**时兜底。
func (c *dynCtx) resolve(idents []string, line int) ([]string, string) {
	// Tier 0
	var tier0 []string
	resolved := false
	for _, id := range idents {
		names, ok := c.rangeTables[id]
		if !ok {
			continue
		}
		resolved = true
		tier0 = append(tier0, names...)
	}
	if resolved {
		return c.parentOnly(tier0), tierRange
	}
	// Tier 1
	var tier1 []string
	resolved = false
	for _, id := range idents {
		names := callSiteTables(c.f, c.fn, id)
		if len(names) == 0 {
			continue
		}
		resolved = true
		tier1 = append(tier1, names...)
	}
	if resolved {
		return c.parentOnly(tier1), tierCallSite
	}
	// Tier A
	return c.parentOnly(attributeWindow(c.srcLines, line, c.parentSet)), tierWindow
}

// parentOnly 取集合里属于分区父表的部分（去重 + 排序）。
func (c *dynCtx) parentOnly(names []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, n := range names {
		n = normalizeTable(n)
		if n == "" || seen[n] || !c.parentSet[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// scanDynamicInFile 在单个文件里找出动态 DML 站点。
func scanDynamicInFile(f *ast.File, fset *token.FileSet, src []byte, rel string, parentSet map[string]bool) []DynamicSite {
	srcLines := strings.Split(string(src), "\n")
	var out []DynamicSite

	// 每个**顶层声明**单独建一份上下文：range 字面量表绑定按函数作用域解析
	// （同名变量在不同函数里指不同的表，见 collectRangeTables 的注释）。
	scanBody := func(node ast.Node, fn *ast.FuncDecl) {
		if node == nil {
			return
		}
		ctx := &dynCtx{
			f:           f,
			fset:        fset,
			srcLines:    srcLines,
			rangeTables: collectRangeTables(node),
			parentSet:   parentSet,
			fn:          fn,
		}
		ast.Inspect(node, func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.BinaryExpr:
				if v.Op != token.ADD {
					return true
				}
				parts, ok := restoreConcat(v, fset)
				if !ok {
					return true
				}
				j := joinParts(parts)
				if !strings.Contains(j.text, dynamicMarker) {
					// 整条拼接里没有动态部分 ⇒ 交给字面量扫描器（它逐个片段扫）。
					// ★ 这里**故意不**对"全是字面量的拼接"再跑一遍字面量判据：
					//   片段已经被扫过一遍，再跑就会同一条语句被数两次。
					return true
				}
				hits := matchDynamic(j.text)
				if len(hits) == 0 {
					return true
				}
				for _, h := range hits {
					line := j.lineAt(h.off)
					tables, tier := ctx.resolve(j.idents(), line)
					out = append(out, DynamicSite{
						File: rel, Line: line, Stmt: h.stmt, Tables: tables, Tier: tier,
					})
				}
				return false // 已消费整条拼接 ⇒ 不再单独扫子片段
			case *ast.CallExpr:
				j, ok := restoreFormatString(v, fset)
				if !ok {
					return true
				}
				hits := matchDynamic(j.text)
				if len(hits) == 0 {
					return true
				}
				for _, h := range hits {
					line := j.lineAt(h.off)
					tables, tier := ctx.resolve(j.idents(), line)
					out = append(out, DynamicSite{
						File: rel, Line: line, Stmt: h.stmt, Tables: tables, Tier: tier,
					})
				}
				return true
			}
			return true
		})
	}

	for _, d := range f.Decls {
		switch v := d.(type) {
		case *ast.FuncDecl:
			scanBody(v.Body, v)
		case *ast.GenDecl:
			scanBody(v, nil)
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Line < out[j].Line
	})
	return out
}

// CollectDynamicSites 扫描 root 下所有非测试 .go 文件，返回动态 DML 站点。
//
// 与 CollectViolations 同范围、同排除项（SQLite 路径同样排除）。
func CollectDynamicSites(root string) ([]DynamicSite, error) {
	return collectDynamicSites(root, false)
}

func collectDynamicSites(root string, includeSQLite bool) ([]DynamicSite, error) {
	parentSet := make(map[string]bool, len(partitionParents))
	for _, p := range partitionParents {
		parentSet[p] = true
	}

	var out []DynamicSite
	fset := token.NewFileSet()
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			switch info.Name() {
			case ".git", "node_modules", "vendor", "web", ".build-local", "bin", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		src, rerr := os.ReadFile(p)
		if rerr != nil {
			return nil
		}
		f, perr := parser.ParseFile(fset, p, src, 0)
		if perr != nil {
			return nil // 解析不了的源文件不是本门的判据范围
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if !includeSQLite && isSQLitePath(rel) {
			return nil
		}
		out = append(out, scanDynamicInFile(f, fset, src, rel, parentSet)...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
