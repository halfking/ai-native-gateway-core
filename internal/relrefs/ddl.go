// ddl.go —— 腿 A：枚举仓内**全部 DDL 形态**建出的关系名。
//
// ★★★ 为什么腿 A 必须枚举【全部形态】而不是只扫 `CREATE TABLE`：
//
//	只扫 CREATE TABLE ⇒ 视图、物化视图、函数返回表、分区子表、重命名后的表
//	全部【看不见】⇒ ⇒ 「悬空引用」会被严重高估 ⇒ ⇒ 门会变成恒红。
//	⇒ 这是 R63 手工做 98 条候选时反复踩到的坑（§4）。
//
// ★ 产出两套集合：
//   - 全名集合（`schema.name` / 裸 `name`）⇒ 用于判定「仓内确实建出了它」
//   - 裸名集合（去 schema 的最后一段）⇒ 用于匹配「产品侧省略了 schema 前缀」的引用
//
// ★ 两套都保留 ⇒ 因为产品侧引用常见两种写法：
//
//	`public.session_turns` 与 `session_turns` ⇒ 必须都能命中。
package relrefs

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ── 标识符解析 ──────────────────────────────────────────────────────────

// identPattern = 一个（可带引号的）SQL 标识符。
const identPattern = `(?:"[^"]+"|[A-Za-z_][A-Za-z0-9_$]*)`

// qualifiedIdentPattern = `a.b` / `"a"."b"` / `a . b` ⇒ 允许引号与空白。
const qualifiedIdentPattern = identPattern + `(?:\s*\.\s*` + identPattern + `)*`

var (
	// createTableRE = CREATE [UNLOGGED] TABLE [IF NOT EXISTS] <name>
	//
	// ★ 注意：`CREATE TABLE x PARTITION OF y` 也被它捕获（捕获的是 x）⇒
	//   子分区名本身就是关系名 ⇒ 必须收进集合。
	createTableRE = regexp.MustCompile(
		`(?i)\bCREATE\s+(?:(?:GLOBAL|LOCAL)\s+)?(?:(?:TEMP|TEMPORARY|UNLOGGED)\s+)?TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?(` + qualifiedIdentPattern + `)`)

	// createViewRE = CREATE [OR REPLACE] [TEMP|RECURSIVE] [MATERIALIZED] VIEW <name>
	createViewRE = regexp.MustCompile(
		`(?i)\bCREATE\s+(?:OR\s+REPLACE\s+)?(?:(?:TEMP|TEMPORARY|RECURSIVE)\s+)*(?:MATERIALIZED\s+)?VIEW\s+(?:IF\s+NOT\s+EXISTS\s+)?(` + qualifiedIdentPattern + `)`)

	// createFuncRE = CREATE [OR REPLACE] FUNCTION <name>
	createFuncRE = regexp.MustCompile(
		`(?i)\bCREATE\s+(?:OR\s+REPLACE\s+)?FUNCTION\s+(` + qualifiedIdentPattern + `)`)

	// returnsTableRE = 该函数是否 RETURNS TABLE(...) ⇒ 是则产出「返回表的关系名」
	//
	// ★ 判据 = 函数名之后到下一个分号为止的片段里出现 `RETURNS TABLE`。
	// ★ 必须用【分号】切分而不是全文搜，否则「函数 A 不返回表、函数 B 返回表」
	//   会让 A 也被误判为关系 ⇒ 悬空引用被高估。
	returnsTableRE = regexp.MustCompile(`(?i)\bRETURNS\s+TABLE\b`)

	// renameToRE = ALTER TABLE [IF EXISTS] <old> RENAME TO <new>
	renameToRE = regexp.MustCompile(
		`(?i)\bALTER\s+TABLE\s+(?:IF\s+EXISTS\s+)?(` + qualifiedIdentPattern + `)\s+RENAME\s+TO\s+(` + qualifiedIdentPattern + `)`)

	// createSchemaRE = CREATE SCHEMA [IF NOT EXISTS] <name>
	createSchemaRE = regexp.MustCompile(
		`(?i)\bCREATE\s+SCHEMA\s+(?:IF\s+NOT\s+EXISTS\s+)?(` + qualifiedIdentPattern + `)`)

	// createTypeRE = CREATE TYPE <name>（枚举/复合类型也是关系 ⇒ 可能被 SELECT 引用）
	createTypeRE = regexp.MustCompile(
		`(?i)\bCREATE\s+(?:OR\s+REPLACE\s+)?TYPE\s+(?:IF\s+NOT\s+EXISTS\s+)?(` + qualifiedIdentPattern + `)`)
)

// stripSQLComments 去掉 SQL 注释，避免注释里的 DDL 关键字被误当形态。
//
// ★ 与 cmd/tools/sql_source_indirection_audit/resolve.go 里的同名函数同源
//
//	（那边是 package main ⇒ 不可 import ⇒ 这里独立实现一份）。
//
// ★ 两个易错点：
//
//	① 字符串字面量里的 `--` 不是注释 ⇒ 必须先跳过引号内的内容。
//	② `/* ... */` 可以跨行。
func stripSQLComments(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	inLine, inBlock, inStr := false, false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inLine:
			if c == '\n' {
				inLine = false
				b.WriteByte(c)
			}
		case inBlock:
			if c == '*' && i+1 < len(s) && s[i+1] == '/' {
				inBlock = false
				i++
			}
		case inStr:
			b.WriteByte(c)
			if c == '\'' {
				// ★ 双写单引号 = 转义的单引号（'it''s'）⇒ 不算结束
				if i+1 < len(s) && s[i+1] == '\'' {
					b.WriteByte('\'')
					i++
					continue
				}
				inStr = false
			}
		default:
			switch {
			case c == '-' && i+1 < len(s) && s[i+1] == '-':
				inLine = true
				i++
			case c == '/' && i+1 < len(s) && s[i+1] == '*':
				inBlock = true
				i++
			case c == '\'':
				inStr = true
				b.WriteByte(c)
			default:
				b.WriteByte(c)
			}
		}
	}
	return b.String()
}

// stripSQLStrings 去掉 SQL 里【普通单引号字符串】的内容（★ 保留 dollar-quoted 区段）。
//
// ★★★ 为什么腿 B 必须剥它（★ 实测）：★
//
//	SQL 里常带【消息文本】当字符串字面量，例如：
//	  'a backlog dominated by artifacts cannot be told apart from real cost drift'
//	⇒ ⇒ 这些散文里的【普通英文单词】会被关系位置提取器当成表名
//	⇒ ⇒ 实测抓出 `real` / `the` / `a` 等假阳性。
//
// ★★★ 为什么**必须保留 dollar-quoted 区段**（★ 关键）：★
//
//	PL/pgSQL 的函数体整体就包在 `$$ … $$` 或 `$tag$ … $tag$` 里
//	⇒ ⇒ 函数体里的 `CREATE TEMP TABLE …` / `FROM some_table` 都是【真内容】
//	⇒ ⇒ ★★ 若把 dollar-quoted 区段也剥掉 ⇒ ⇒ 函数体里的 DDL 与引用**全部消失**
//	⇒ ⇒ ⇒ **腿 A 尤其不能剥**（`CREATE TEMP TABLE` 在函数体里是货真价实的 DDL）。
//
// ★ ⇒ ⇒ 所以两腿的预处理是【刻意不对称】的：
//
//	腿 A（DDL）：只剥注释，**不剥字符串**（要看见函数体里的 DDL）
//	腿 B（引用）：剥注释 **＋** 剥普通字符串（散文会污染引用判定）
func stripSQLStrings(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	inStr := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inStr {
			if c == '\'' {
				// ★ 双写单引号 = 转义的单引号（'it''s'）⇒ 不算结束
				if i+1 < len(s) && s[i+1] == '\'' {
					b.WriteString("''")
					i++
					continue
				}
				inStr = false
				b.WriteString("''")
				continue
			}
			// ★ 字符串内容替换成空格（★ 保留长度 ⇒ 行号/位置不变）
			if c != '\n' {
				b.WriteByte(' ')
			} else {
				b.WriteByte('\n')
			}
			continue
		}
		if c == '\'' {
			inStr = true
			b.WriteString("''")
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

// normalizeIdent 把一段（可能带引号与空白的）限定名规整成 `a.b` 形式。
//
// ★ 去掉每段的外层引号 ⇒ `"public"."session_turns"` ⇒ `public.session_turns`。
func normalizeIdent(raw string) string {
	parts := strings.Split(raw, ".")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if len(p) >= 2 && p[0] == '"' && p[len(p)-1] == '"' {
			p = p[1 : len(p)-1]
		}
		if p == "" {
			return ""
		}
		out = append(out, strings.ToLower(p))
	}
	return strings.Join(out, ".")
}

// bareName 取限定名的最后一段（去 schema）。
func bareName(qualified string) string {
	if i := strings.LastIndex(qualified, "."); i >= 0 {
		return qualified[i+1:]
	}
	return qualified
}

// ddlRelations = 腿 A 的产出。
type ddlRelations struct {
	// full = 全名集合（`public.session_turns` / `session_turns`）
	full map[string]bool
	// bare = 裸名集合（`session_turns`）
	bare map[string]bool
	// schemas = 建出的 schema 名
	schemas map[string]bool
	// perKind = 各形态的计数 ⇒ 用于「搜索面完整性互证」
	perKind map[string]int
	// files = 扫描过的 .sql 文件数
	files int
}

func newDDLRelations() *ddlRelations {
	return &ddlRelations{
		full:    map[string]bool{},
		bare:    map[string]bool{},
		schemas: map[string]bool{},
		perKind: map[string]int{},
	}
}

func (d *ddlRelations) add(kind, qualified string) {
	q := normalizeIdent(qualified)
	if q == "" {
		return
	}
	d.full[q] = true
	d.bare[bareName(q)] = true
	d.perKind[kind]++
}

// has 判断某个名字是否被仓内 DDL 建出。
//
// ★ 两个口径都试：全名匹配 与 裸名匹配 ⇒ 因为产品侧引用常常省略 schema。
func (d *ddlRelations) has(name string) bool {
	n := normalizeIdent(name)
	if n == "" {
		return false
	}
	if d.full[n] {
		return true
	}
	return d.bare[bareName(n)]
}

// scanDDLFile 从一个 .sql 文件里抽取关系名。
func scanDDLFile(d *ddlRelations, text string) {
	text = stripSQLComments(text)

	for _, m := range createTableRE.FindAllStringSubmatch(text, -1) {
		d.add("table", m[1])
	}
	for _, m := range createViewRE.FindAllStringSubmatch(text, -1) {
		d.add("view", m[1])
	}
	for _, m := range createTypeRE.FindAllStringSubmatch(text, -1) {
		d.add("type", m[1])
	}
	for _, m := range createSchemaRE.FindAllStringSubmatch(text, -1) {
		q := normalizeIdent(m[1])
		if q != "" {
			d.schemas[q] = true
		}
	}
	for _, m := range renameToRE.FindAllStringSubmatch(text, -1) {
		// ★ 重命名的【新名】是关系名；【旧名】通常已被 DROP ⇒ 不收。
		d.add("renamed", m[2])
	}

	// ★ 函数返回表 ⇒ 产出关系名。
	// ★ 必须按【分号】切分语句，逐句判断该函数是否 RETURNS TABLE。
	for _, stmt := range splitSQLStatements(text) {
		fm := createFuncRE.FindStringSubmatch(stmt)
		if fm == nil {
			continue
		}
		if returnsTableRE.MatchString(stmt) {
			d.add("function_returns_table", fm[1])
		}
	}
}

// splitSQLStatements 按分号切分 SQL 语句（粗粒度，够用于「该函数是否 RETURNS TABLE」的判定）。
//
// ★ 易错点：字符串字面量里的分号 ⇒ 会把语句切碎。
//
//	⇒ 所以先剥注释、再做「引号感知的」切分。
func splitSQLStatements(text string) []string {
	var out []string
	var cur strings.Builder
	inStr := false
	for i := 0; i < len(text); i++ {
		c := text[i]
		if inStr {
			cur.WriteByte(c)
			if c == '\'' {
				if i+1 < len(text) && text[i+1] == '\'' {
					cur.WriteByte('\'')
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
			cur.WriteByte(c)
		case ';':
			out = append(out, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(c)
		}
	}
	if strings.TrimSpace(cur.String()) != "" {
		out = append(out, cur.String())
	}
	return out
}

// scanDDLRepo 遍历仓内全部 .sql 文件并抽出关系集合。
//
// ★ 范围 = `git ls-files '*.sql'`（★ 用 git 的文件清单 ⇒ 不会把构建产物/临时文件算进来）
//
//	—— 若用目录遍历 ⇒ 会把 node_modules 之类算进去 ⇒ 集合被污染。
func scanDDLRepo(root string, sqlFiles []string) (*ddlRelations, error) {
	d := newDDLRelations()
	for _, rel := range sqlFiles {
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			return nil, err
		}
		d.files++
		scanDDLFile(d, string(b))
	}
	return d, nil
}
