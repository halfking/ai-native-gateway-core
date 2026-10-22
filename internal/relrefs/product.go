// product.go —— 腿 B：从**产品代码**里提取「按名引用的关系」。
//
// ★★★ 为什么必须用 `go/ast` 而不是正则扫 .go 文本：
//
//	Go 的 SQL 几乎都在**字符串字面量**里 ⇒ 正则会吃进注释里的 SQL。
//	⇒ `go/ast` 天然分开注释与字面量 ⇒ 不必自己写剥注释器
//	⇒ ⇒ 也不会踩「剥注释把普通字符串也剥掉」那个坑（§215）。
//	⇒ 零第三方依赖（只用标准库 go/parser + go/ast + go/token）。
//
// ★★★ 三档桶的语义（R63 §4「按名命中几乎总是假阳性」的机械化）：
//
//	强命中 = 关系名出现在【SQL 关系位置】（FROM / JOIN / INTO / UPDATE / TRUNCATE /
//	          REFERENCES / COPY 之后）⇒ ★★★ 最强证据：产品确实按名读它。
//	弱命中 = 关系名出现在【SQL 字面量的非关系位置】⇒ 中等证据 ⇒ ★★ 可能误报 ⇒ 需人验。
//	零命中 = 产品代码里**完全查不到**该名字 ⇒ ⇒ ★★ 在【无拼接嫌疑】的前提下，
//	          可判定「产品不依赖它」。
//
// ★★★ ★★ 第四件必须单列的东西：【拼接嫌疑】
//
//	`"SELECT … FROM " + tbl + " …"` 这类拼接点里，表名在 Go 变量中 ⇒
//	静态提取【抓不到】⇒ ⇒ ★★ 「按名零命中 ⇒ 不依赖」这条推论**被它推翻**。
//	⇒ 因此：凡存在「某字面量以该关系名**开头**且更长」的拼接嫌疑 ⇒
//	          该关系名**不得**被判为「可判定不依赖」⇒ ⇒ 这是【前提守卫】。
package relrefs

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// sqlContextRE = 判断一个字符串字面量是不是【SQL 上下文】。
//
// ★★★ ⚠⚠⚠ 第一版是「单个关键字」（SELECT|FROM|UPDATE|JOIN…）⇒ ★★★ **实测假阳性率极高**：
//
//	`update action log`（英文错误消息）、`update annotation: %w`、`from any candidate URL`
//	全被当成 SQL ⇒ ⇒ 后面 `UPDATE <ident>` / `FROM <ident>` 把普通英文单词抓成了表名
//	⇒ ⇒ 第一版读数 = 309 个「悬空引用」/ 904 处 ⇒ ⇒ ★★ 其中绝大多数是【假阳性】。
//	⇒ ⇒ ★★★ **教训：★ 「含一个 SQL 关键字」不构成 SQL 上下文 ⇒ 必须要求【成对结构】**。
//
// ★ 现版要求出现下列【结构化】片段之一（★ 而不是单个动词）：
//
//	SELECT … FROM ｜ INSERT INTO ｜ UPDATE … SET ｜ DELETE FROM ｜ WITH x AS (
//	TRUNCATE … ｜ COPY x FROM/TO ｜ REFERENCES x ｜ ALTER TABLE ｜ CREATE TABLE/VIEW/INDEX/FUNCTION
//	MERGE INTO ｜ ON CONFLICT ｜ RETURNING
//
// ★ 注意 Go 的 `\b` 是 ASCII 词边界 ⇒ 中文紧邻时两侧都算边界 ⇒
//
//	「中文里嵌一句 SELECT 也会被认出来」⇒ ★ 这正是我们要的。
var sqlContextRE = regexp.MustCompile(
	`(?i)\b(SELECT\s+[\s\S]{0,400}?\bFROM\b` +
		`|INSERT\s+INTO\b` +
		`|UPDATE\s+[\s\S]{0,200}?\bSET\b` +
		`|DELETE\s+FROM\b` +
		`|WITH\s+[A-Za-z_"][\w$".]*\s+AS\s*\(` +
		`|TRUNCATE\s+(?:TABLE\s+)?[A-Za-z_"][\w$".]*` +
		`|COPY\s+[A-Za-z_"][\w$".]*\s+(?:FROM|TO)\b` +
		`|ALTER\s+TABLE\b` +
		`|CREATE\s+(?:OR\s+REPLACE\s+)?(?:TEMP\s+|TEMPORARY\s+|UNLOGGED\s+)?(?:MATERIALIZED\s+)?(?:TABLE|VIEW|INDEX|UNIQUE\s+INDEX|FUNCTION)\b` +
		`|MERGE\s+INTO\b` +
		`|ON\s+CONFLICT\b` +
		`|RETURNING\b)`)

// cteNameRE = CTE（WITH x AS (...)）里的名字。
//
// ★★★ CTE 名【不是关系】⇒ 它出现在 `WITH base AS (…) SELECT … FROM base` 里
//
//	⇒ 若不排除 ⇒ `base` / `buckets` / `bucket_totals` / `agg` / `active_failures` …
//	全被当成表名 ⇒ ⇒ ★★ 这正是第一版假阳性的一大类。
var cteNameRE = regexp.MustCompile(`(?i)(?:\bWITH\s+(?:RECURSIVE\s+)?|,)\s*("?[A-Za-z_][\w$]*"?)\s*(?:\([^)]*\))?\s+AS\s*(?:NOT\s+MATERIALIZED\s+|MATERIALIZED\s*)?\(`)

// relPositionRE = SQL 里的【关系位置】⇒ 产品按名引用关系的最强证据。
//
// ★ 位置词：FROM / JOIN / INTO / UPDATE / TRUNCATE / REFERENCES / COPY。
// ★ 处理三个易错形态：
//
//	① `FROM (` ⇒ 那是函数调用（EXTRACT(x FROM y)）⇒ 不是关系 ⇒ 排除。
//	② `ONLY t` / `t*`（PG 继承）⇒ 允许可选前缀与尾部星号。
//	③ `INSERT INTO` / `COPY x FROM` / `COPY x TO` ⇒ COPY 后可跟 FROM/TO ⇒ 只取 COPY 后的第一个名。
var relPositionRE = regexp.MustCompile(
	`(?i)\b(?:FROM|JOIN|UPDATE|TRUNCATE|REFERENCES|COPY)\s+(?:TABLE\s+)?(ONLY\s+)?(` + qualifiedIdentPattern + `)\s*(\*)?`)

// insertIntoRE = `INSERT INTO <table>`。
//
// ★★★ 为什么把 INTO 从关系位置里【拆出来】：
//
//	`SELECT … INTO some_var` 是 **PL/pgSQL 的变量赋值**，不是表
//	⇒ 实测第一版把 `parent_kind` / `hot_kind` / `new_interval` / `moved_count`
//	 这些**函数体内的变量名**当成了表名 ⇒ ⇒ 假阳性。
//	⇒ 而 `INSERT INTO <table>` 里的表是真关系 ⇒ ⇒ 用【独立正则】保留它。
var insertIntoRE = regexp.MustCompile(
	`(?i)\bINSERT\s+INTO\s+(` + qualifiedIdentPattern + `)`)

// sqlKeywordNames = 不能当关系名的 SQL 关键字/伪关键字。
//
// ★ 实测假阳性来源：`LATERAL`（`FROM x, LATERAL (…)`）、`VALUES`、`UNNEST`、`ONLY`、
//
//	`EXISTS`、`ANY`、`SOME`、`ARRAY` 等。
var sqlKeywordNames = map[string]bool{
	"lateral": true, "values": true, "unnest": true, "only": true,
	"exists": true, "any": true, "some": true, "array": true,
	"select": true, "from": true, "where": true, "group": true, "order": true,
	"limit": true, "offset": true, "union": true, "intersect": true, "except": true,
	"on": true, "and": true, "or": true, "not": true, "in": true, "as": true,
	"case": true, "when": true, "then": true, "else": true, "end": true,
	"true": true, "false": true, "null": true, "default": true,
	"with": true, "recursive": true, "returning": true, "using": true,
	// ★★★ `SET` 的来源实测：`ON CONFLICT (…) DO UPDATE SET col = …`
	//   ⇒ ⇒ `UPDATE` 后面紧跟的 `SET` 及其后是【列名】不是表名
	//   ⇒ ⇒ 实测第一版因此抓出 178 处 `set` ⇒ ⇒ 必须进黑名单。
	"set": true, "update": true, "insert": true, "delete": true, "truncate": true,
	"references": true, "copy": true, "join": true, "into": true, "create": true,
	"alter": true, "drop": true, "table": true, "view": true, "index": true,
	"do": true, "nothing": true, "conflict": true,
	// ★ `SKIP` 的来源实测：`FOR UPDATE SKIP LOCKED` ⇒ 10 处假阳性
	"skip": true, "locked": true, "rows": true, "row": true,
	// ★ `of` 来源实测：拼接字面量里的英文散文（`a column this deployment…`）
	"of": true, "is": true, "it": true, "to": true, "by": true,
}

// relationPos = SQL 里一个关系位置。
type relationPos struct {
	// Name = 规整后的关系名
	Name string
	// Index = 该位置在原文中的字节偏移
	Index int
}

// extractRelationPositions 提取 SQL 里【关系位置】上的关系名。
//
// ★★★ 关键修正（★ 自检的反向夹具抓到的）：★ **必须排除【括号内】的 FROM** ——
//
//	`EXTRACT(day FROM ts)` / `SUBSTRING(x FROM y FOR z)` / `TRIM(BOTH x FROM y)`
//	这些**函数参数里的 FROM** 不是关系位置 ⇒ ⇒ 纯正则会把字段名当表名 ⇒ 假阳性。
//	⇒ 判据 = 该关键字处的【括号深度 == 0】。
//	⇒ ★ 而 `FROM (`（子查询）本来就匹配不上 ident ⇒ 无需额外处理。
//
// ★★★ 另两类排除（★ 第一版假阳性的两大来源）：
//
//	① CTE 名（`WITH base AS (…) … FROM base`）⇒ 不是关系。
//	② **函数调用**（`FROM generate_series(...)`）⇒ 后面的名字是函数名，不是表名。
//	   ⇒ 判据 = 名字后面（跳过空白）紧跟 `(`。
//	③ **别名.列名**（`FROM providers p … SELECT c.credential_id`）⇒ `c.credential_id`
//	   是【表别名 + 列名】，不是 `schema.表` ⇒ ⇒ 若不排除 ⇒ 实测出现
//	   `b.aggregation_bucket` / `c.ts` / `excluded.carry_level` 等一堆假阳性。
//	   ⇒ 判据 = 限定名的第一段**不在【仓内建出的 schema 名集合】里** ⇒ ⇒ 它不是 schema 限定。
func extractRelationPositions(sqlText string, ddl *ddlRelations, globalCTEs globalCTENames) []relationPos {
	ctes := collectCTENames(sqlText)
	var out []relationPos
	for _, m := range relPositionRE.FindAllStringSubmatchIndex(sqlText, -1) {
		start := m[0]
		if parenDepthAt(sqlText, start) != 0 {
			continue
		}
		nameStart, nameEnd := m[4], m[5]
		if nameStart < 0 {
			continue
		}
		name := normalizeIdent(sqlText[nameStart:nameEnd])
		if name == "" {
			continue
		}
		// ★ ① CTE 名排除（★ 本字面量内的 ＋ ★ 全仓全局的）
		if ctes[bareName(name)] || globalCTEs[bareName(name)] {
			continue
		}
		// ★ ② 函数调用排除：名字后面紧跟 `(` ⇒ 是函数不是表
		if isCallSite(sqlText, nameEnd) {
			continue
		}
		// ★ ③ 别名.列名排除
		if isAliasQualified(name, ddl) {
			continue
		}
		// ★ ④ SQL 关键字/伪关键字排除
		if sqlKeywordNames[bareName(name)] {
			continue
		}
		out = append(out, relationPos{Name: name, Index: start})
	}

	// ★ `INSERT INTO <table>` 里的表是真关系 ⇒ 单独收
	for _, m := range insertIntoRE.FindAllStringSubmatchIndex(sqlText, -1) {
		nameStart, nameEnd := m[2], m[3]
		if nameStart < 0 {
			continue
		}
		name := normalizeIdent(sqlText[nameStart:nameEnd])
		if name == "" || sqlKeywordNames[bareName(name)] {
			continue
		}
		if isAliasQualified(name, ddl) {
			continue
		}
		out = append(out, relationPos{Name: name, Index: m[0]})
	}
	return out
}

// isAliasQualified 判断这个名字是不是「表别名.列名」形态。
//
// ★ 判据 = 名字含 `.` 且第一段不是仓内建出的 schema ⇒ ⇒ 它不是 schema 限定。
// ★ ★ 保守说明：★ 若某张真表以「非 schema 的限定名」出现（例如 `otherdb.tbl`），
//
//	它会被这条排除掉 ⇒ ⇒ ★ 这是**已知漏检**⇒ ⇒ 记在诚实边界里（属主可按需放宽）。
func isAliasQualified(name string, ddl *ddlRelations) bool {
	i := strings.Index(name, ".")
	if i < 0 {
		return false
	}
	first := name[:i]
	if ddl == nil {
		// ★ 没有 schema 事实源时 ⇒ 【不排除】（宁可多报不可漏报）
		return false
	}
	return !ddl.schemas[first]
}

// sqlStmt = 字面量里的一条语句。
type sqlStmt struct {
	text  string
	isSQL bool
}

// extractRelationPositionsInSQLStatements 逐【语句】提取关系位置。
//
// ★ 只有含 SQL 结构的语句才参与 ⇒ ⇒ 拼接字面量里的英文散文被排除。
// ★ ★ 无分号的字面量 ⇒ 只有一句 ⇒ 按整段判定 ⇒ ⇒ 召回不损失。
func extractRelationPositionsInSQLStatements(raw string, ddl *ddlRelations, globalCTEs globalCTENames) []relationPos {
	stmts := splitSQLStatements(raw)
	var out []relationPos
	for _, st := range stmts {
		if !sqlContextRE.MatchString(st) {
			continue
		}
		out = append(out, extractRelationPositions(st, ddl, globalCTEs)...)
	}
	return out
}

// collectCTENames 收集一段 SQL 里所有 CTE 名。
func collectCTENames(sqlText string) map[string]bool {
	out := map[string]bool{}
	for _, m := range cteNameRE.FindAllStringSubmatch(sqlText, -1) {
		n := normalizeIdent(m[1])
		if n != "" {
			out[bareName(n)] = true
		}
	}
	return out
}

// isCallSite 判断名字结束位置之后（跳过空白）是否紧跟 `(`。
func isCallSite(s string, end int) bool {
	for i := end; i < len(s); i++ {
		switch s[i] {
		case ' ', '\t', '\n', '\r':
			continue
		case '(':
			return true
		default:
			return false
		}
	}
	return false
}

// parenDepthAt 返回 s[0:idx] 处的括号深度（★ 引号内的括号不算）。
func parenDepthAt(s string, idx int) int {
	depth := 0
	inStr := false
	for i := 0; i < idx && i < len(s); i++ {
		c := s[i]
		if inStr {
			if c == '\'' {
				if i+1 < len(s) && s[i+1] == '\'' {
					i++
					continue
				}
				inStr = false
			}
			continue
		}
		switch c {
		case '\'':
			inStr = true
		case '(':
			depth++
		case ')':
			depth--
		}
	}
	return depth
}

// identScanRE = 在一段文本里扫出所有标识符（用于【弱命中】判定）。
var identScanRE = regexp.MustCompile(`(?i)"?([A-Za-z_][A-Za-z0-9_$]*)"?`)

// ── 排除清单 ────────────────────────────────────────────────────────────

// excludedPathPrefixes = 【不参与产品侧扫描】的路径前缀。
//
// ★ 口径来源 = R104 轮实测决策：这些目录**也大量引用表名** ⇒ 不排除则差集被淹没。
// ★ 排除 `installer/` 与 `sql/`、`migrations/` ⇒ 因为它们是【DDL 侧】的来源，
//
//	若也进产品侧 ⇒ 「产品引用」会被自家 DDL 淹没 ⇒ 悬空引用恒为空。
var excludedPathPrefixes = []string{
	"vendor/",
	"installer/",
	"scripts/",
	"tools/",
	"cmd/tools/",
	"test/",
	"tests/",
	"examples/",
	"npm/",
	"web/",
	"web-mobile/",
	"deploy/",
	"docs/",
	"sql/",
	"migrations/",
	"internal/testdb/",
	// ★★★★ ★★★ 这一条是【本门第一次运行就抓出来的】—— ★ 不是先想到的：
	//
	//	`internal/relrefs/selftest.go` 是【非测试文件】⇒ ⇒ 它就是【产品代码】
	//	⇒ ⇒ 它的【自检夹具 SQL】（`FROM real_table` / `JOIN keepme k` / `FROM y`）
	//	⇒ ⇒ 被腿 B 当成产品引用扫进去了 ⇒ ⇒ 三个夹具名被判成「悬空引用」
	//	⇒ ⇒ 门当场红 ⇒ ⇒ ★★ 【门第一次运行就抓到了一个真实缺陷（而且是我自己写的）】
	//
	//	★ ⇒ 通用式：★ **扫描类工具必须把自己的目录排除掉**
	//	  （★ 或者把夹具放进 `_test.go` —— ★ 但夹具若被【非测试代码】引用（这里是
	//	  `RunSelfTest`），它就【不能】只待在 `_test.go` 里 ⇒ ⇒ 排除目录是必须的）。
	"internal/relrefs/",
}

// isExcludedPath 判断一个仓内相对路径是否属于排除范围。
func isExcludedPath(rel string) bool {
	rel = filepath.ToSlash(rel)
	if strings.HasSuffix(rel, "_test.go") {
		return true
	}
	for _, p := range excludedPathPrefixes {
		if strings.HasPrefix(rel, p) {
			return true
		}
	}
	return false
}

// ── 产出结构 ────────────────────────────────────────────────────────────

// relationRef = 产品侧的一次关系引用。
type relationRef struct {
	// Name = 规整后的名字（`public.session_turns` / `session_turns`）
	Name string
	// File = 仓内相对路径
	File string
	// Line = 行号（1 起）
	Line int
	// Strong = 是否出现在【关系位置】
	Strong bool
	// SQL = 来源字面量（截断到 200 字符，避免清单爆炸）
	SQL string
}

// globalCTENames = 【全仓】的 CTE 名集合。
//
// ★★★ 为什么必须【全局】而不是「逐字面量」：
//
//	拼接型 SQL 里，CTE 的**定义**与**引用**常常分处不同字面量 ——
//	实测：`WITH f AS (…)` + filterClause + `, w0 AS ( SELECT f.* … FROM f …)`
//	⇒ 逐字面量看 `f` 那个字面量时【看不到】`WITH f AS (` ⇒ ⇒ CTE 排除失效
//	⇒ ⇒ `f` / `w0` 这类 CTE 名被当成表名 ⇒ ⇒ 假阳性。
//	⇒ ⇒ ★★ 解法 = 先扫一遍全仓收集所有 CTE 名，再做第二遍的关系位置提取。
type globalCTENames map[string]bool

// productScan = 腿 B 的产出。
type productScan struct {
	// refs = 全部关系引用（强 + 弱）
	refs []relationRef
	// strongNames = 强命中名字集合
	strongNames map[string]bool
	// weakNames = 弱命中名字集合（字面量里出现过，但不在关系位置）
	weakNames map[string]bool
	// prefixSuspects = 【拼接嫌疑】：某字面量以该名字开头且更长
	//
	// ★ ⇒ 凡在此集合里的名字，**不得**被判为「可判定不依赖」。
	prefixSuspects map[string]bool
	// files = 扫过的 .go 文件数
	files int
	// stringLits = 取到的字符串字面量总数
	stringLits int
	// sqlLits = 其中被判为 SQL 上下文的
	sqlLits int
	// globalCTECount = 全仓 CTE 名个数（★ 第一遍收集 ⇒ 第二遍排除用）
	globalCTECount int
}

// rawLit = 一个字符串字面量（已剥掉 SQL 注释）。
type rawLit struct {
	rel  string
	line int
	text string
}

// scanProductRepo 扫描仓内 .go 文件，提取产品侧的关系引用。
//
// ★★★ 【两遍扫描】—— 第二遍依赖第一遍的结果：
//
//	第一遍：收集【全仓】的 CTE 名（★ 拼接型 SQL 里 CTE 的定义与引用分处不同字面量
//	        ⇒ ⇒ 逐字面量看是【看不到】CTE 定义的 ⇒ ⇒ CTE 排除失效）
//	第二遍：提取关系位置（★ 用第一遍的全局 CTE 名做排除）
func scanProductRepo(root string, goFiles []string, ddl *ddlRelations) (*productScan, error) {
	ps := &productScan{
		strongNames:    map[string]bool{},
		weakNames:      map[string]bool{},
		prefixSuspects: map[string]bool{},
	}
	lits, err := collectProductLiterals(root, goFiles, ps)
	if err != nil {
		return nil, err
	}

	// ── 第一遍之二：★ 从【Go 侧字面量】里抽 DDL ──
	//
	// ★★★★ 这一步是本轮实测逼出来的（★ 不是先想到的）：
	//
	//	DDL **不只存在于 `.sql` 文件** ⇒ 它也在 **Go 字符串**里：
	//	  · `db.ensureCredentialClientQuotaSchema`（ensure 链 ⇒ 运行时建表）
	//	  · `admin/session_export.go` 的 `CREATE TABLE IF NOT EXISTS session_packs`
	//	⇒ ⇒ ★★ 若腿 A 只扫 `.sql` ⇒ ⇒ 这两张表会被误报成「悬空引用」
	//	⇒ ⇒ ★★ **⇒ 腿 A 的搜索面缺口 = 「DDL 的载体不止一种」**
	//	⇒ ⇒ ★★ 解法 = 同一个字面量集合，★ 既抽 DDL 也抽引用（⇒ 两腿共用一遍扫描）。
	for _, l := range lits {
		scanDDLFile(ddl, l.text)
	}

	// ── 第一遍之三：全仓 CTE 名 ──
	globalCTEs := globalCTEFromLiterals(lits)
	ps.globalCTECount = len(globalCTEs)

	// ── 第二遍：提取关系位置 ──
	for _, l := range lits {
		processLiteral(ps, l, ddl, globalCTEs)
	}
	return ps, nil
}

// collectProductLiterals 收集仓内全部 .go 文件的字符串字面量（★ 排除清单已应用）。
//
// ★★ 单独成函数是为了让【幂等自检】能复用同一批字面量 ⇒
//
//	⇒ 幂等检查不必重新解析 2000+ 个文件 ⇒ ⇒ 门的时间从 56s 降到 ~20s。
func collectProductLiterals(root string, goFiles []string, ps *productScan) ([]rawLit, error) {
	var lits []rawLit
	fset := token.NewFileSet()
	for _, rel := range goFiles {
		if isExcludedPath(rel) {
			continue
		}
		ps.files++
		path := filepath.Join(root, rel)
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		f, err := parser.ParseFile(fset, path, b, parser.SkipObjectResolution)
		if err != nil {
			// ★ 解析失败不许静默跳过 ⇒ 否则门会以为「这个文件里没有引用」。
			return nil, err
		}
		lits = append(lits, collectLiterals(ps, rel, fset, f)...)
	}
	return lits, nil
}

// globalCTEFromLiterals 从一批字面量里收集【全仓】CTE 名。
func globalCTEFromLiterals(lits []rawLit) globalCTENames {
	globalCTEs := globalCTENames{}
	for _, l := range lits {
		for _, m := range cteNameRE.FindAllStringSubmatch(l.text, -1) {
			n := normalizeIdent(m[1])
			if n != "" {
				globalCTEs[bareName(n)] = true
			}
		}
	}
	return globalCTEs
}

// buildDDLFromLiterals 从一批字面量里抽 DDL（★ Go 侧的 CREATE TABLE/VIEW 等）。
func buildDDLFromLiterals(ddl *ddlRelations, lits []rawLit) {
	for _, l := range lits {
		scanDDLFile(ddl, l.text)
	}
}

// extractStrongNames 只算【强命中】名字集合（★ 幂等自检用）。
func extractStrongNames(lits []rawLit, ddl *ddlRelations, globalCTEs globalCTENames) map[string]bool {
	out := map[string]bool{}
	for _, l := range lits {
		for _, p := range extractRelationPositionsInSQLStatements(l.text, ddl, globalCTEs) {
			out[p.Name] = true
		}
	}
	return out
}

// collectLiterals 收集一个文件里全部字符串字面量（已剥 SQL 注释）。
func collectLiterals(ps *productScan, rel string, fset *token.FileSet, f *ast.File) []rawLit {
	var out []rawLit
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		ps.stringLits++
		raw, err := strconv.Unquote(lit.Value)
		if err != nil {
			// ★ 非法字面量（拼接出来的）⇒ 跳过（★ 不是 SQL 上下文）
			return true
		}
		// ★★★★ 关键：★ **腿 B 也必须剥 SQL 注释** ——
		// ★ 腿 A 有剥注释、腿 B 原本没有 ⇒ ⇒ 第一版读数里大量假阳性来自
		//   【SQL 字面量里的 `-- 注释`】：注释里的英文单词被当成了表名
		//   （实测：`-- Keep analytics isolated from …` ⇒ 抓出 `a` / `both` / `collapsing`）。
		// ⇒ ⇒ ★★ 两腿的预处理**各自取舍**（★ 刻意【不对称】）：
		//   腿 A（DDL）只剥注释 ⇒ **不剥字符串**（函数体里的 CREATE TEMP TABLE 是真 DDL）
		//   腿 B（引用）剥注释 ＋ 剥普通字符串（散文会污染引用判定）⇒ 保留 dollar-quote 区段。
		raw = stripSQLComments(raw)
		// ★★ 腿 B 还要剥【普通单引号字符串】（★ 散文会污染引用判定）
		//   ⇒ 而 dollar-quoted 区段（函数体）**保留** ⇒ ⇒ 函数体里的真引用不丢。
		raw = stripSQLStrings(raw)
		out = append(out, rawLit{rel: rel, line: fset.Position(lit.Pos()).Line, text: raw})
		return true
	})
	return out
}

// processLiteral 处理一个字面量，产出关系引用。
func processLiteral(ps *productScan, l rawLit, ddl *ddlRelations, globalCTEs globalCTENames) {
	raw := l.text
	rel, line := l.rel, l.line
	if !sqlContextRE.MatchString(raw) {
		// ★ 即使不是 SQL 上下文，也要参与【拼接嫌疑】探测
		//   ⇒ 因为 `"session_turns" + suffix` 这种拼接不在 SQL 上下文里。
		detectPrefixSuspects(ps, ddl, raw)
		return
	}
	ps.sqlLits++
	detectPrefixSuspects(ps, ddl, raw)

	// ★★★★ ★【逐语句】判定 SQL 上下文 ——
	// ★ 拼接型字面量里会混入【英文散文】（实测：`… ; query references a column this
	//   deployment's schema does not have;` ⇒ 散文里的 `a` 被当成表名）。
	// ★ ⇒ 判据 = 按【分号】切句，**只有含 SQL 结构的句子**才参与关系位置提取。
	// ★ ⇒ ★★ 而【无分号】的拼接片段仍按整段判定 ⇒ ⇒ 不牺牲召回。
	// ★ ⇒ ★★ 这是「收窄判据前必须先证明不摘掉真保护」的落法：★ 逐句收窄，
	// ★   而【真候选三个】（prompt_injection_approvals / public.credential_client_quota /
	// ★   session_packs）必须在收窄后**仍然在列** ⇒ ⇒ 那就是收窄没摘掉真保护的证据。
	positions := extractRelationPositionsInSQLStatements(raw, ddl, globalCTEs)
	posSet := map[string]bool{}
	for _, p := range positions {
		ps.refs = append(ps.refs, relationRef{
			Name: p.Name, File: rel, Line: line, Strong: true, SQL: truncate(raw, 200),
		})
		ps.strongNames[p.Name] = true
		posSet[p.Name] = true
	}

	// ★ 弱命中 = 字面量里的标识符命中了【腿 A 建出的关系名】，但不在关系位置。
	if ddl != nil {
		for _, m := range identScanRE.FindAllStringSubmatch(raw, -1) {
			cand := normalizeIdent(m[1])
			if cand == "" || !ddl.has(cand) {
				continue
			}
			if posSet[cand] {
				continue // 已在强命中里 ⇒ 不重复计弱
			}
			ps.refs = append(ps.refs, relationRef{
				Name: cand, File: rel, Line: line, Strong: false, SQL: truncate(raw, 200),
			})
			ps.weakNames[cand] = true
		}
	}
}

// positionHas 判断该名字是否出现在关系位置上（用于避免强/弱重复计）。
func positionHas(sqlText, name string, ddl *ddlRelations, globalCTEs globalCTENames) bool {
	for _, p := range extractRelationPositions(sqlText, ddl, globalCTEs) {
		if p.Name == normalizeIdent(name) {
			return true
		}
	}
	return false
}

// detectPrefixSuspects 探测【拼接嫌疑】。
//
// ★ 判据 = 存在一个字面量，它【以该关系名开头】且【比它更长】⇒
//
//	⇒ 它可能就是 `"session_turns" + "_2026"` 这类拼接 ⇒
//	⇒ ★★ 该关系名不得被判为「可判定不依赖」。
//
// ★ 只用【裸名】比较 ⇒ 因为拼接时通常不带 schema 前缀。
func detectPrefixSuspects(ps *productScan, ddl *ddlRelations, literal string) {
	if ddl == nil || literal == "" {
		return
	}
	lit := strings.ToLower(literal)
	for name := range ddl.bare {
		if len(name) < 4 {
			continue // ★ 太短的名字前缀噪音太大（噪音会淹没信号）
		}
		if len(lit) > len(name) && strings.HasPrefix(lit, name) {
			ps.prefixSuspects[name] = true
		}
	}
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
