package main

import (
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

// Class 是一个拼接点的解析结论。
type Class int

const (
	// ClassUnresolved 表示关系名无法静态确定。**默认分支**：宁可不可判定也不猜。
	ClassUnresolved Class = iota
	// ClassReadsV1 表示关系名解析到（或可能解析到）v1 宽族。
	ClassReadsV1
	// ClassReadsCanonical 表示关系名解析到 canonical 视图或会话族。
	ClassReadsCanonical
)

// v1Tables 是 S4 要退役的 v1 宽族**基表**关系名（与
// admin/v1_direct_padded_column_reader_test.go 的 v1DirectTables 同源）。
//
// 这里是**唯一**的真相源。早先还有一条 canonicalViewRE（用正则排除
// `request_logs_with_current_month*` 视图名）加一条 `request_logs_` 前缀兜底，
// 三者并存。两次变异验证都证明它们是**冗余的**：
//
//	变异 A（加回前缀兜底）→ TestCanonicalViewIsNotAV1Relation 仍绿
//	变异 B（删掉视图正则）→ 同一道门仍绿
//
// 行为被**两个多余的真相源**同时决定了，所以任何删掉其中一个的变异都测不出来。
// 这与本文件族系里反复出现的那个教训是同一条：admin 那道门早先也因为
// 「门拿错误源与错误派生式互相校验」而一直绿着。⇒ 精确集合留下，其余删掉。
var v1Tables = map[string]bool{
	"request_logs": true, "request_logs_hot": true,
	"request_logs_bodies": true, "request_logs_bodies_hot": true,
}

// fragmentTailRE 匹配「以 FROM/JOIN（+ 空白）结尾」的 SQL 片段。
//
// 只匹配结尾，不匹配中间：`FROM request_logs_hot` 是**完整**字面量，扫描器看得见；
// 只有 `FROM ` 后面还要拼东西时，关系名才不可知。
var fragmentTailRE = regexp.MustCompile(`(?i)\b(from|join)\s+$`)

// Site 是一个「关系名不是字面量」的 SQL 拼接点。
type Site struct {
	File     string
	Line     int
	Operand  string   // 操作数在源码里的文本，例如 src.TurnsTable
	Resolved []string // 解析出的候选关系名；空 = 不可判定
}

// Classification 决定这个拼接点属于哪一类。
//
// 判定必须**逐候选**看而不是「只要有一个候选是 v1 就算 v1」：一个切换层函数可能
// 按参数在 v1 与 canonical 之间二选一（maas.requestLogsSource 就是这样），那它是
// **条件性**读 v1 —— 与「恒定读 v1」的风险等级不同，但绝不是安全。所以两个分支
// 混在一起时按最坏情况算 v1：它确实会在某个入参下命中，而门看不见这个条件。
func (s Site) Classification() Class {
	if len(s.Resolved) == 0 {
		return ClassUnresolved
	}
	sawV1, sawOther := false, false
	for _, r := range s.Resolved {
		if isV1Relation(r) {
			sawV1 = true
		} else {
			sawOther = true
		}
	}
	if sawV1 {
		return ClassReadsV1
	}
	_ = sawOther
	return ClassReadsCanonical
}

func (s Site) String() string {
	if len(s.Resolved) == 0 {
		return fmt.Sprintf("%s:%d  操作数=%s  → 不可静态解析", s.File, s.Line, s.Operand)
	}
	return fmt.Sprintf("%s:%d  操作数=%s  → %s",
		s.File, s.Line, s.Operand, strings.Join(s.Resolved, " | "))
}

// isV1Relation 判断一个解析出的字符串是否指向 v1 宽族**基表**。
//
// 两个必须容忍的形态：
//   - 带别名的 FROM 子句：切换层返回的是 "request_logs_hot AS r"，取第一个 token。
//   - 换行与缩进：拼接点后面常跟着另一个拼接的 alias。
//
// ⚠ **不要加「排除 canonical 视图」的前缀或正则判断**（§9.45 变异验证 A/B）：
// 视图名 request_logs_with_current_month* 根本不在 v1Tables 里，精确相等已经
// 把它们排除。曾经这里还有一条正则守卫 + 一条 request_logs_ 前缀兜底，两次
// 删改都测不出差别——因为行为被三个真相源同时决定。多余的真相源不是防御，
// 是「看起来在防、实际测不出」的缺口。视图名之所以不会被误判，是因为**它不在
// 那个集合里**，仅此而已。
func isV1Relation(name string) bool {
	t := strings.TrimSpace(strings.ToLower(name))
	if t == "" {
		return false
	}
	if i := strings.IndexAny(t, " \t\n("); i >= 0 {
		t = t[:i]
	}
	t = strings.Trim(t, `"`)
	return v1Tables[t]
}

// AuditRepo 扫描整个仓库，返回所有关系名不是字面量的 SQL 拼接点。
func AuditRepo(root string) ([]Site, error) {
	var files []string
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			base := filepath.Base(p)
			if base == "vendor" || base == ".git" || base == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(p, ".go") {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)

	// **按包分组解析**：切换层函数（requestLogsSource、logsSourceFromSQL、
	// requestLogsFromClause、boardRequestLogsFromClause）都定义在一个文件里、
	// 被同包的其他文件调用。只做单文件解析会把它们判成不可知——那不是保守，
	// 是把「能查清的事」报成「查不清」。
	byDir := map[string][]string{}
	var order []string
	for _, f := range files {
		d := filepath.Dir(f)
		if _, ok := byDir[d]; !ok {
			order = append(order, d)
		}
		byDir[d] = append(byDir[d], f)
	}
	sort.Strings(order)

	var out []Site
	for _, d := range order {
		sites, err := auditPackage(d, byDir[d])
		if err != nil {
			continue // 解析失败不是本工具的事（构建会报）
		}
		out = append(out, sites...)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Line < out[j].Line
	})
	return dedupeSites(out), nil
}

func dedupeSites(in []Site) []Site {
	seen := map[string]bool{}
	var out []Site
	for _, s := range in {
		k := fmt.Sprintf("%s:%d:%s", s.File, s.Line, s.Operand)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, s)
	}
	return out
}

// pkgEnv 是整个包共享的解析环境。
//
// 变量绑定**按函数作用域**保存（globals + locals 分离），这不是洁癖：
// `logsTable` 在 admin 包里被三个互不相干的函数各绑一次（dashboard_board_queries
// 的 boardRequestLogsFromClause、usage_credits 的 requestLogsFromClause、tenants
// 自己的 logsTable），三者返回**不同的表**。第一版按包级绑定，「首个胜出」于是让
// 三个调用点都拿到第一个函数的值——输出一份**格式正确、数值合理、结论全错**的
// 报告。那比不报危险得多：读者会照着它去改正确的代码。
type pkgEnv struct {
	globals map[string][]string
	locals  map[string][]string
	funcs   map[string][]string
}

func newPkgEnv() *pkgEnv {
	return &pkgEnv{globals: map[string][]string{}, locals: map[string][]string{}, funcs: map[string][]string{}}
}

// enterFunc 清空局部绑定（每个函数体开始时调用）。
func (env *pkgEnv) enterFunc() { env.locals = map[string][]string{} }

func auditPackage(dir string, files []string) ([]Site, error) {
	env := newPkgEnv()
	parsed := map[string]*ast.File{}
	fset := token.NewFileSet()
	for _, p := range files {
		f, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			continue
		}
		parsed[p] = f
	}

	// 三遍，顺序不能换：
	//  1. 函数返回值（不依赖绑定）
	//  2. 包级绑定（不依赖局部）
	//  3. 逐函数：进函数 → 收集该函数体内的局部绑定 → 只在该函数体内找拼接点
	for _, f := range parsed {
		collectFuncReturns(f, env)
	}
	for _, f := range parsed {
		for _, d := range f.Decls {
			if gd, ok := d.(*ast.GenDecl); ok && gd.Tok == token.VAR {
				collectStringBindings(gd, env, env.globals)
			}
		}
	}

	var out []Site
	paths := make([]string, 0, len(parsed))
	for p := range parsed {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		f := parsed[p]
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			env.enterFunc()
			collectStringBindings(fd.Body, env, env.locals)
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				bin, ok := n.(*ast.BinaryExpr)
				if !ok || bin.Op != token.ADD {
					return true
				}
				var ops []ast.Node
				flattenConcat(bin, &ops)
				if countStringLits(ops) == 0 {
					return true // 数值加法，与 SQL 无关
				}
				for i, o := range ops {
					frag, ok := stringLit(o)
					if !ok || !fragmentTailRE.MatchString(frag) {
						continue
					}
					if i+1 >= len(ops) {
						continue
					}
					out = append(out, Site{
						File:     p,
						Line:     fset.Position(o.Pos()).Line,
						Operand:  exprText(ops[i+1]),
						Resolved: env.resolve(ops[i+1], 0),
					})
				}
				return true
			})
		}
	}
	return out, nil
}

func flattenConcat(n ast.Node, out *[]ast.Node) {
	if bin, ok := n.(*ast.BinaryExpr); ok && bin.Op == token.ADD {
		flattenConcat(bin.X, out)
		flattenConcat(bin.Y, out)
		return
	}
	*out = append(*out, n)
}

func countStringLits(ops []ast.Node) int {
	n := 0
	for _, o := range ops {
		if _, ok := stringLit(o); ok {
			n++
		}
	}
	return n
}

func stringLit(n ast.Node) (string, bool) {
	bl, ok := n.(*ast.BasicLit)
	if !ok || bl.Kind != token.STRING {
		return "", false
	}
	v, err := strconv.Unquote(bl.Value)
	return v, err == nil
}

// collectStringBindings 收集 `X = <表达式>` / `X := <表达式>` 形式的绑定，
// 写入 dst（globals 或 locals）。
//
// ⚠ 关键点一：RHS **可以是一次函数调用**。`logsTable, alias := requestLogsSource(days)`
// 是本项目里最常见的绑定形态，只认字面量 RHS 会让这些变量彻底查不到，
// 然后它们会掉进「未知标识符」——早先一版把未知标识符原样当作候选表名，
// 结果被判成「名为 logsTable 的非 v1 表 ⇒ 退役安全」。**把「不知道」报成「安全」
// 是本工具最坏的失效方向。**
//
// ⚠ 关键点二：写入 dst 而不是一张全局表。变量是函数作用域的。
func collectStringBindings(n ast.Node, env *pkgEnv, dst map[string][]string) {
	record := func(name string, rhs ast.Node) {
		if _, exists := dst[name]; exists {
			return // 首个绑定胜出
		}
		dst[name] = env.resolve(rhs, 0)
	}
	ast.Inspect(n, func(node ast.Node) bool {
		switch d := node.(type) {
		case *ast.ValueSpec:
			for i, name := range d.Names {
				if i < len(d.Values) {
					record(name.Name, d.Values[i])
				}
			}
		case *ast.AssignStmt:
			if d.Tok != token.DEFINE {
				return true
			}
			for i, lhs := range d.Lhs {
				if id, ok := lhs.(*ast.Ident); ok && i < len(d.Rhs) {
					record(id.Name, d.Rhs[i])
				}
			}
		}
		return true
	})
}

// collectFuncReturns 收集**同包**每个具名函数的字符串返回值（第一个结果位）。
//
// 只收「所有 return 的第一个结果都是字面量」的函数——那种可以被静态解析。
// 含条件分支的（`if days <= 7 { return A }; return B`）两个 return 都是字面量，
// 因此也会被收进来，取并集：这正是我们要的，**条件性读 v1** 也能被发现。
func collectFuncReturns(f *ast.File, env *pkgEnv) {
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		var rets []string
		allLit := true
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			ret, ok := n.(*ast.ReturnStmt)
			if !ok || len(ret.Results) == 0 {
				return true
			}
			v, ok := stringLit(ret.Results[0])
			if !ok {
				allLit = false
				return true
			}
			rets = append(rets, v)
			return true
		})
		if allLit && len(rets) > 0 {
			env.funcs[fd.Name.Name] = dedupeStrings(rets)
		}
	}
}

func dedupeStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// resolve 解析一个表达式可能取到的字符串集合。空结果 = 不可判定。
//
// **注意这里没有 `case *ast.Ident: return []string{e.Name}` 这样的兜底。**
// 早先有一版把未知标识符原样当作候选表名，于是 `logsTable`（未解析）被判成
// 「名为 logsTable 的非 v1 表」⇒ 归入「退役安全」。一个把「不知道」报成「安全」
// 的审计工具比没有工具更坏。未知就是未知。
func (env *pkgEnv) resolve(n ast.Node, depth int) []string {
	if depth > 4 {
		return nil
	}
	switch e := n.(type) {
	case *ast.BasicLit:
		if v, ok := stringLit(e); ok {
			return []string{v}
		}
		return nil
	case *ast.Ident:
		if vals, ok := env.locals[e.Name]; ok {
			return vals // 可能为空（绑定存在但解析不出）⇒ 不可判定
		}
		if vals, ok := env.globals[e.Name]; ok {
			return vals
		}
		return nil
	case *ast.SelectorExpr:
		// struct 字段 / 跨包常量：不猜。
		return nil
	case *ast.CallExpr:
		name := calleeName(e.Fun)
		if name == "" {
			return nil // 跨包函数：单包内无法解析
		}
		return env.funcs[name]
	case *ast.ParenExpr:
		return env.resolve(e.X, depth+1)
	}
	return nil
}

func calleeName(fun ast.Expr) string {
	switch e := fun.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		if _, ok := e.X.(*ast.Ident); ok {
			// 带包前缀（db.SessionFamilyTurnsSourceSQL）：跨包，返回 ""。
			return ""
		}
		return calleeName(e.X) + "." + e.Sel.Name
	}
	return ""
}

func exprText(n ast.Node) string {
	switch e := n.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		return exprText(e.X) + "." + e.Sel.Name
	case *ast.CallExpr:
		if nm := calleeName(e.Fun); nm != "" {
			return nm + "(...)"
		}
		return "(...)"
	}
	return fmt.Sprintf("%T", n)
}
