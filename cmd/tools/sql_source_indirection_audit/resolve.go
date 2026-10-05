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

// fromRelationRE 匹配「解析出的字符串里，某个 FROM/JOIN 后面跟着的关系名」。
//
// # 为什么需要它：isV1Relation 的「取第一个 token」在**子查询**上给错答案，
// 而那个方向正是本工具自称的最坏失效方向（见 isV1Relation 的注释）。
var fromRelationRE = regexp.MustCompile(`(?i)\b(?:from|join)\s+("(?:[^"]|"")+"|[a-z_][a-z0-9_$]*)`)

// isIdentByte 与 admin 包的同名函数同义，但**不复用**：
// 那个函数在 admin 包的测试文件里，而本工具是独立的 main module，
// 引用它要么把工具挂到 admin 上（错的依赖方向），要么复制（两份真相源）。
// 一个字节级谓词，复制比接线便宜。
func isIdentByte(b byte) bool {
	return b == '_' ||
		(b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

// isRelationFragmentTail 判断一段 SQL 片段是不是「FROM/JOIN 后面要拼关系名」。
//
// 它比 fragmentTailRE 多一步，而且那一步是**实测逼出来的**。
//
// `fragmentTailRE` 是 `(?i)\b(from|join)\s+$`。它分不清两种完全不同的 `from`：
//
//	… LEFT JOIN  + tbl            ← FROM 子句，这里拼的是**关系名**（要报）
//	… IS DISTINCT FROM  + balArg   ← 比较运算符，这里拼的是**值**（不该报）
//
// 2026-10-05 实测到第二类进了报告：
//
//	admin/provider_credential.go:637
//	  valueChanged := "balance_usd IS DISTINCT FROM " + balArg
//	  → balArg 被当成关系名
//
// ⚠ 方向上这是**多报**（本工具自述的安全方向），所以它不会让人漏掉什么 v1 读点。
// 但它会污染清单：27 个文件里有 1 个是被这条误报的，而清单的
// `Consequence` 要写「退役时会怎样」——对着一个比较运算符写不出有意义的话。
// ⇒ 清单要么登记一条「其实不是读点」，要么把工具修对。修工具更省事。
//
// Go 的 RE2 没有负向断言，所以「前面那个词是不是 distinct」在代码里判。
func isRelationFragmentTail(frag string) bool {
	if !fragmentTailRE.MatchString(frag) {
		return false
	}
	// ⚠ **顺序是承重的，而且我第一版写反了。**
	// 先按长度切尾部关键词的话，末 4 个字符是 `"ROM "`（`FROM ` 的后 4 个）
	// 而不是 `"FROM"` ⇒ 等值比较永远不成立 ⇒ 这道过滤静默失效、
	// `IS DISTINCT FROM` 仍然进报告。实测抓到的。
	// ⇒ 必须**先** TrimRight 掉空白，**再**切关键词。
	trimmed := strings.TrimRight(frag, " \t\n\r")
	for _, kw := range []string{"from", "join"} {
		if len(trimmed) >= len(kw) &&
			strings.EqualFold(trimmed[len(trimmed)-len(kw):], kw) {
			trimmed = strings.TrimRight(trimmed[:len(trimmed)-len(kw)], " \t\n\r")
			break
		}
	}
	i := len(trimmed)
	for i > 0 && isIdentByte(trimmed[i-1]) {
		i--
	}
	return !strings.EqualFold(trimmed[i:], "distinct")
}

// sqlLineCommentRE 剥掉解析结果里的 SQL 行注释。
//
// ⚠ **它今天不改变任何一条真实判定，别把它当门来依赖。**
// 实测（2026-10-05，逐字 diff 全仓输出）：剥与不剥，输出**完全相同**。
// 原因是仓库里唯一挂在解析结果上的 SQL 注释是 tenants.go 那句
// `-- sqlreadguard:allow …（hot 腿同查询内联；… UNION ALL）`——它里面有
// "UNION ALL" 但**没有** `from`/`join` 加标识符，所以扫不到。
//
// 留着它只有一个理由，且必须写明：它**只可能减少误报、不可能制造漏报**
// （剥掉一段注释只会让可扫的关系名变少，而少的那部分本来就不是查询）。
// 曾经为它写过一道门，夹具是
// `-- 历史实现读 request_logs，已改`——**仓库里不存在这种注释**，
// 于是那道门在「删掉剥注释」这个变异下依然绿：一个为虚构场景写的判据，
// 测不出任何东西。⇒ 门已删，只留这一行归一化，并把这个测量结果记在这里，
// 免得下一个人再拿它当「必须有门」的理由。
var sqlLineCommentRE = regexp.MustCompile(`--[^\n]*`)

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
// 三个必须容忍的形态：
//   - 带别名的 FROM 子句：切换层返回的是 "request_logs_hot AS r"，取第一个 token。
//   - 换行与缩进：拼接点后面常跟着另一个拼接的 alias。
//   - **整个解析结果是一段子查询 / UNION**，关系名不止一个。
//
// # 「取第一个 token」曾经把一段真·v1 读法判成「退役安全」（2026-10-05 实测）
//
// `admin/tenants.go:770` 把 logsTable 定义成
//
//	(SELECT … FROM request_logs_hot
//	 UNION ALL
//	 SELECT … FROM request_logs) -- sqlreadguard:allow …
//
// 然后 `FROM ` + logsTable + ` WHERE tenant_id = $1` 查租户统计。
// 第一个 token 是 `(select` ⇒ 不在 v1Tables ⇒ 本函数返回 false ⇒
// Classification 返回 ClassReadsCanonical ⇒ 工具输出把 tenants.go 列进
// **「解析到 canonical 视图 / 会话族（退役安全）」**。
//
// 它读的是**两张 v1 底表**，而报告说它退役安全。
//
// 这与 §9.45 记的「把『不知道』报成安全」**不是同一件事**：那一条是不可判定
// 被当成安全，输出里还留着「不可静态解析」的标签；这一条是**已经解析出内容、
// 却解析错了**，而且错得**自信**——字符串非空、形状规整、报告读起来完全正常。
// 同一族的失效方向，但更隐蔽，因为「自信的错」在输出里没有任何视觉信号。
//
// ⇒ 解析结果里**多于一个关系名**时，必须逐个 FROM/JOIN 扫过去，一个都不许漏。
// 多扫的代价是可能把一段只是提到 v1 的子查询算成 v1 读点；对退役清单而言
// 多报是安全方向。这里有答案可查，所以不该退化成「不可判定」。
//
// ⚠ **不要加「排除 canonical 视图」的前缀或正则判断**（§9.45 变异验证 A/B）：
// 视图名 request_logs_with_current_month* 根本不在 v1Tables 里，精确相等已经
// 把它们排除。曾经这里还有一条正则守卫 + 一条 request_logs_ 前缀兜底，两次
// 删改都测不出差别——因为行为被三个真相源同时决定。多余的真相源不是防御，
// 是「看起来在防、实际测不出」的缺口。视图名之所以不会被误判，是因为**它不在
// 那个集合里**，仅此而已。
func isV1Relation(name string) bool {
	raw := strings.TrimSpace(name)
	if raw == "" {
		return false
	}
	// 老口径：纯关系名 / 带别名的关系名都走这一支。
	if v1Tables[firstRelationToken(raw)] {
		return true
	}
	// 子查询形态：剥掉 SQL 行注释后逐个 FROM/JOIN 取关系名。
	stripped := sqlLineCommentRE.ReplaceAllString(raw, " ")
	for _, m := range fromRelationRE.FindAllStringSubmatch(stripped, -1) {
		if v1Tables[firstRelationToken(m[1])] {
			return true
		}
	}
	return false
}

// firstRelationToken 从一段 SQL 片段里取出**第一个**关系名 token。
//
// 只取 token、不在这里判断它是不是关系名——判断留给调用方的集合查表。
// 这样 `(SELECT …` 与 `"request_logs_hot"` 两种起手都落到同一处逻辑上。
func firstRelationToken(s string) string {
	t := strings.TrimSpace(strings.ToLower(s))
	if t == "" {
		return ""
	}
	if i := strings.IndexAny(t, " \t\n("); i >= 0 {
		t = t[:i]
	}
	return strings.Trim(t, `"`)
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
		// ⚠ `_test.go` 必须排除，理由不是「测试不算生产代码」这句套话，
		// 而是**测试文件会把断言消息当成关系名**（2026-10-05 实测）。
		//
		// `TestRequestLogsReadInventoryIsComplete` 的报错文本里有
		// `"… read request_logs but are absent from …:\n  %s"`，它以
		// `from ` 结尾、被 fragmentTailRE 认成拼接点，于是
		// `操作数=*ast.BasicLit → requestLogsReadInventory — S4 would silently
		// stop feeding them:` 进了**「退役安全」**桶。
		//
		// 这不是噪声，是**内容错误的关系名**：输出把一句英文断言当成了一张表。
		// 它和本文件其余口径也一致——requestLogsReadInventory、exposure 抽取器
		// 都按「非 _test.go 的生产文件」为总体。
		//
		// ⚠ 代价要说清楚：真库测试里确实有直读 v1 的 SQL
		// （db/repoint_value_fidelity_realdb_test.go 读 canonical 视图），
		// 排除后本工具**不再**覆盖它们。那是有意的——它们是测试的对照源，
		// 不是会被 S4 停写的读方；真要盘点它们，另一道门按自己的口径管。
		if strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, "_test.go") {
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

	cross := crossProviderFuncs(root)
	var out []Site
	for _, d := range order {
		sites, err := auditPackage(d, byDir[d], cross)
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
	// cross 是**跨包**（crossProviderPath）的函数返回值表，crossAlias 是
	// 当前文件给那个包起的本地别名。别名逐文件变（`db` / `dbpkg`），
	// 所以它**不能**是包级常量 —— 见 resolve 的 SelectorExpr 分支。
	cross      map[string][]string
	crossAlias string
}

func newPkgEnv() *pkgEnv {
	return &pkgEnv{
		globals: map[string][]string{},
		locals:  map[string][]string{},
		funcs:   map[string][]string{},
		cross:   map[string][]string{},
	}
}

// enterFunc 清空局部绑定（每个函数体开始时调用）。
func (env *pkgEnv) enterFunc() { env.locals = map[string][]string{} }

func auditPackage(dir string, files []string, cross map[string][]string) ([]Site, error) {
	parsed := map[string]*ast.File{}
	fset := token.NewFileSet()
	for _, p := range files {
		f, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			continue
		}
		parsed[p] = f
	}

	return scanParsedWithCross(fset, parsed, cross)
}

// crossProviderFuncs 解析 crossProviderPath 那个包的**函数返回值表**，
// 供其它包按包限定调用查。
//
// ⚠ 它解析的是 db 包**自己的目录**（root 下的 `db`），不是按 import 路径
// 在磁盘上找 —— 因为那 21 处调用点分布在 6 个包里，逐一解析它们的 import
// 再去定位包目录，等于把「一次解析」变成 N 次。
// 代价：db 包若被重命名/移出 `db/`，这里会静默返回空表，
// **表现是退回「不可判定」而不是报出错误的关系名** —— 这是可承受的失败方向
// （工具宁可查不到，也不猜）。
func crossProviderFuncs(root string) map[string][]string {
	dir := filepath.Join(root, "db")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	fset := token.NewFileSet()
	parsed := map[string]*ast.File{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if err != nil {
			continue
		}
		parsed[name] = f
	}
	if len(parsed) == 0 {
		return nil
	}
	env := newPkgEnv()
	// 与 scanParsedWithCross 同一套「迭代到不动点」的收集次序，
	// 否则 db 包里那些引用包级投影常量的返回语句解析不出来。
	for pass := 0; pass < funcGlobalPasses; pass++ {
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
	}
	return env.funcs
}

// scanParsed enumerates every concat site in an already-parsed set of files.
//
// It is separate from auditPackage so the enumeration can be exercised against
// an in-memory fixture: the package-level `var` case that §9.237 fixed has no
// cheap on-disk instance to point a test at, and a regression test that needs
// the whole repository to run is a regression test that does not get run.
func scanParsed(fset *token.FileSet, parsed map[string]*ast.File) ([]Site, error) {
	return scanParsedWithCross(fset, parsed, nil)
}

// scanParsedWithCross 是带跨包解析表的版本。cross 为 nil 时行为与旧版一致
// （单元测试的内存夹具走这条路，不必构造 db 包）。
//
// # 为什么 funcs 与 globals 要**迭代**而不是各走一遍
//
// 它们互相依赖：包级 `var forensicsExportMessagesSQL = "…" + dbpkg.SessionBodiesSourceSQL() + "…"`
// 要靠 funcs 才能解析，而 `SessionFamilyBodiesSourceSQL` 的返回值里又引用了
// 包级 `var sessionBodiesSourceProjection`（要靠 globals）。
// 单遍的先后顺序必然有一边拿到空表 ⇒ 整条链又落回「不可判定」。
//
// ⇒ 迭代到不动点。**上界是 3 轮而不是「直到不变」**：万一将来出现
// `A 依赖 B 依赖 A` 的循环，「直到不变」会挂死，而 3 轮之后剩下的都是
// 解析不出的表达式 —— 那种情况的表现与今天完全一样（不可判定），不会更糟。
const funcGlobalPasses = 3

func scanParsedWithCross(fset *token.FileSet, parsed map[string]*ast.File, cross map[string][]string) ([]Site, error) {
	env := newPkgEnv()
	env.cross = cross
	if env.cross == nil {
		env.cross = map[string][]string{}
	}
	paths := make([]string, 0, len(parsed))
	for p := range parsed {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	for pass := 0; pass < funcGlobalPasses; pass++ {
		for _, p := range paths {
			// ★ crossAlias 必须在这里也逐文件设。
			// 第一版只在下面的扫描阶段设，于是 funcs 收集阶段的
			// `db.SessionFamilyTurnsSourceSQL() + " rl"` 查不到跨包表，
			// `logsSourceFromSQL()` 的一半分支**静默丢失** ——
			// 而丢的那一半正是会话族，剩下的一半是 v1 视图。
			// 表现不是「不可判定」而是**解析出一个不完整的并集**，
			// 那比解析不出更危险：它看起来是个正常的成功结果。
			env.crossAlias = importAlias(parsed[p], crossProviderPath)
			collectFuncReturns(parsed[p], env)
		}
		for _, p := range paths {
			for _, d := range parsed[p].Decls {
				if gd, ok := d.(*ast.GenDecl); ok && gd.Tok == token.VAR {
					collectStringBindings(gd, env, env.globals)
				}
			}
		}
	}

	var out []Site
	// scanConcat enumerates every `<string literal ending in a relation name>
	// <operand>` pair inside n and records a Site for each.
	//
	// It is a closure over `out`/`p`/`env` so that the two container kinds below
	// — function bodies and package-level var initialisers — go through exactly
	// one implementation. A second copy of this loop is how the two would drift,
	// and the drift is silent: one would keep finding sites the other misses.
	scanConcat := func(p string, n ast.Node) {
		ast.Inspect(n, func(node ast.Node) bool {
			bin, ok := node.(*ast.BinaryExpr)
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
				if !ok || !isRelationFragmentTail(frag) {
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

	for _, p := range paths {
		f := parsed[p]
		// 逐文件重置：别名是**这个文件**的 import 声明，不是包级属性。
		// `admin/` 里两种别名都出现过（`db` 1 处、`dbpkg` 20 处）。
		env.crossAlias = importAlias(f, crossProviderPath)
		for _, decl := range f.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Body == nil {
					continue
				}
				env.enterFunc()
				collectStringBindings(d.Body, env, env.locals)
				scanConcat(p, d.Body)

			case *ast.GenDecl:
				// §9.237: package-level `var` SQL used to be invisible here.
				//
				// The bindings were already collected into env.globals for
				// *resolution* (see the loop above), but the site enumeration
				// only ever looked inside function bodies. A `const` or `var`
				// holding a SQL string with a relation-name tail plus a call
				// therefore resolved fine when reached from a function and was
				// never reported on its own.
				//
				// The two known instances are the ones that had to be turned
				// from `const` into `var` precisely because they call a switch
				// layer: `admin/compression_stats.go`'s
				// compressionStatsEstimatedOrigSQL (3rd concat site, already
				// noted in the indirection manifest) and both SQL constants in
				// `domains/sessionforensics/export.go` (§9.233.8d). They were
				// covered by the admin-side reader inventory, so no read was
				// missed — but a tool that is treated as the authority for
				// "which files read v1 conditionally" would have said they do
				// not, which is a false negative in a retirement audit.
				//
				// enterFunc() is called for the same reason it is above: it
				// clears `locals`, and a package-level initialiser must resolve
				// against globals only. Leaving the previous function's locals
				// in scope would let a var resolve a name it could not actually
				// see at package level.
				if d.Tok != token.VAR {
					continue
				}
				env.enterFunc()
				for _, spec := range d.Specs {
					vs, ok := spec.(*ast.ValueSpec)
					if !ok {
						continue
					}
					for _, val := range vs.Values {
						scanConcat(p, val)
					}
				}
			}
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
		// §9.257：不再要求「所有 return 的第一个结果都是字面量」。
		//
		// 原口径（allLit）会因为一个 `return 另一个函数()` 的分支而**整函数作废** ——
		// `SessionBodiesSourceSQL` 正是这样：两支分别是
		// `return "request_logs_bodies_with_current_month"` 与
		// `return SessionFamilyBodiesSourceSQL()`，一见到后者整函数被丢进「不可判定」。
		//
		// ⚠ 改用 resolve 后**门槛降低**：以前「解析不出」⇒ 不登记；
		// 现在「解析不出」⇒ 该支贡献空、其余支仍登记。方向是**更敢下结论**，
		// 所以必须盯住它不会把非 SQL 的函数也登记进来 —— 这由
		// `TestCompleteRelationLiteralIsNotAConcatSite` 与 §9.257 的变异台账守着。
		// 仍然只取**第一个结果位**：多返回值函数的第二个及以后不是关系名。
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			ret, ok := n.(*ast.ReturnStmt)
			if !ok || len(ret.Results) == 0 {
				return true
			}
			rets = append(rets, env.resolve(ret.Results[0], 0)...)
			return true
		})
		if len(rets) > 0 {
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
		// struct 字段：**不猜**。
		// 唯一例外是「包限定」形态且该包就是 crossProviderPath 的 import 别名：
		// `db.SessionBodiesSourceSQL()` 是**函数调用**，它的返回值能从 db 包
		// 源码里读出来（§9.257）。`src.TurnsTable` 这种 struct 字段
		// 即使包前缀恰好是 db 也不在其列 —— 后者的取值来自两支 struct 字面量，
		// 跟函数返回值不是一回事，混进来就是把「猜」包装成「查」。
		if id, ok := e.X.(*ast.Ident); ok && env.crossAlias != "" && id.Name == env.crossAlias {
			if vals, ok := env.cross[e.Sel.Name]; ok {
				return vals
			}
		}
		return nil
	case *ast.CallExpr:
		name := calleeName(e.Fun)
		if name == "" {
			// 跨包：calleeName 对带包前缀的调用返回 ""，
			// 这里改为按 SelectExpr 直接查跨包表（同一处逻辑）。
			if sel, ok := e.Fun.(*ast.SelectorExpr); ok {
				if id, ok := sel.X.(*ast.Ident); ok && env.crossAlias != "" && id.Name == env.crossAlias {
					return env.cross[sel.Sel.Name]
				}
			}
			return nil
		}
		return env.funcs[name]
	case *ast.ParenExpr:
		return env.resolve(e.X, depth+1)
	case *ast.BinaryExpr:
		// §9.257：字符串拼接的返回值。两个分支的候选取**并集** ——
		// `SessionFamilyBodiesSourceSQL` 返回
		// `"… FROM public.session_bodies_hot sb" + " UNION ALL SELECT " + 投影 + " FROM public.session_bodies sb)"`，
		// 不接这一支则整条拼接只解析出空 ⇒ 又一次「把查得到的事报成查不到」。
		//
		// ⚠ 方向是**多报**：并集里可能多出只出现在某一片段里的关系名。
		// 本工具的退役清单宁可多报也不漏报（见 isRelationFragmentTail 的注释）。
		return append(env.resolve(e.X, depth+1), env.resolve(e.Y, depth+1)...)
	}
	return nil
}

// crossProviderPath 是本工具**唯一**愿意跨包跟随的依赖。
//
// 为什么是它、为什么只能是「导航」而不是「结论」：
//
//   - 这些切换层函数全部集中在 db 包，它们决定「读哪一族」。
//   - 本工具**不硬编码它们的返回值** —— 返回值是从 db 包源码里解析出来的。
//     所以这里存的只是一个**导入路径**，改错了最多变成「跟丢了、退回不可判定」，
//     不会变成「跟错了、报出假的关系名」。那种失败方向工具承受不起。
//
// ⚠ 反面：早先这里曾按**包名前缀**猜（「凡是带 db. 的就算」），
// 那会让 `dbpkg.` 与 `db.` 两种别名、以及任何恰好叫 db 的本地包混为一谈。
// 现在按**该文件里 db 包的实际 import 别名**解析，见 importAlias。
const crossProviderPath = "github.com/kaixuan/llm-gateway-go/db"

// importAlias 返回该文件给 crossProviderPath 起的**本地别名**。
// 没有这个 import 时返回空串（调用方的 resolve 因此不会去查跨包表）。
func importAlias(f *ast.File, path string) string {
	for _, imp := range f.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil || p != path {
			continue
		}
		if imp.Name != nil {
			return imp.Name.Name // 显式 dbpkg "…/db"
		}
		// 无显式名：本地名取路径最后一段（Go 语言规则）。
		if i := strings.LastIndex(p, "/"); i >= 0 {
			return p[i+1:]
		}
		return p
	}
	return ""
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
