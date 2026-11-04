//go:build !integration

package ursm_test

// AUDIT-GUARD: scripts/ursmcheck 属 GUARD_PACKAGES_SLOW ⇒ 本门在 `make guards-ursmcheck` 上真跑。
//
// fk_target_key_gate_test.go —— 「FK 目标表必须有 PK/UNIQUE 精确覆盖」静态门（F-9 继承项）。
//
// # 这道门守的是哪件东西
//
// 2026-11-04 f2af856c1（迁移 936 幂等自愈补 subscription_plans 主键）的根因：
// 既有环境（211）的 `subscription_plans` 是基线 dump 形态 —— **没有主键**
// （只有 code UNIQUE），而 936 要建 `subscription_plan_entitlements` 并以
// `FOREIGN KEY (plan_id) REFERENCES public.subscription_plans(id)` 引用它
// ⇒ PostgreSQL 以 **42870**（there is no unique constraint matching given keys
// for referenced table）拒绝 ⇒ 部署中止。上一轮审计（0329 §4.2 序号 9）裁决：
// 要建机器门防同族。本门就是那个门。
//
// # 判据口径（★ PG 真实语义，不是近似）
//
// 每条 FK 的 (目标表, 目标列集) 必须被目标表上**某一条** PRIMARY KEY /
// UNIQUE 约束（或无 WHERE、纯列的 CREATE UNIQUE INDEX）**精确覆盖**：
// 覆盖 = 键的列集与 FK 引用列集**相等（顺序无关）**。这正是 PG 的行为
// （transformFkeyCheckAttrs 找属性集完全相同的唯一索引）——UNIQUE(a,b)
// **不**支持 FK→(a)，前缀/超集都不行 ⇒ 本门不做「被包含」宽松判定，
// 宽了会把 42870 漏放过去。
//
// # 数据源（纯静态解析，不连真库）
//
//   - 基线三件：sql/schema/00-prereqs.sql / 01-schema.sql / 02-seed.sql
//
// # 与同域姊妹门的分工（2026-11-04 合并调和，5be6b2f0e 同日各建一门）
//
// sql/schema/fk_target_unique_constraint_gate_test.go（并行会话建）守的是
// **全新安装基线**域：基线是整体一个单元、排在任何迁移之前 ⇒ 它额外守
// 「FK 目标表必须在基线内」＋「ATTACH PARTITION 子表列覆盖父表」。
// 本门守的是**基线＋已注册迁移链的终态**：迁移里新加的 FK（含迁移间
// 先表后键的时序）只有这里看得见。两门判据 A（FK 覆盖）形状相同、
// 数据源分工如上 ⇒ 改判据时**两门一起看**，不要让同判据平行漂移。
//   - **已注册**的 startup 迁移链：sql/schema/installed_startup_migrations.tsv
//     （该 TSV 由 runner.go 的 StartupFiles 生成、被 startup_manifest_test
//     逐条钉住 ⇒ 是注册链的 SSOT）。**不注册的迁移文件不解析**：目录里有
//     495 个迁移号但只有 ~243 个在链上，退役文件的 FK 永不在 fresh install
//     执行，把它们算进来只会制造一堆「死文件豁免」。
//
// 解析的语句形态（只有三种，全部行锚定）：
// CREATE TABLE 块（表级/列级约束、列内联 REFERENCES、表级 FOREIGN KEY）、
// ALTER TABLE ... ADD CONSTRAINT ...（PRIMARY KEY / UNIQUE / FOREIGN KEY）、
// CREATE [UNIQUE] INDEX（无 WHERE、纯列名的才配作 FK 目标键）。
//
// # 刻意不解析的东西（诚实边界）
//
//   - `$$` 动态 SQL（DO 块里的条件 ADD CONSTRAINT，如 936/612/825 的幂等
//     自愈块）——静态解析器**看不到**这类运行期才成立的键。它们落进豁免
//     清单（逐条带理由），不假装静态可见。豁免是双向棘轮：缺口消失（比如
//     哪天基线补了 PK）⇒ 豁免条目必须同步删除，否则门红。
//   - 字符串/注释里的内容一律按空白处理（字符级 SQL 词法扫描，保留行号），
//     所以字面量里的 "REFERENCES" 之类不会造假锚点。
//
// # 已知近似（都偏保守，不会漏放 42870）
//
//   ① E'...' 转义字符串里的 \' 按普通字符近似（本仓 DDL 未用该形态）；
//   ② 目标表的键允许来自链上**更晚**的迁移 —— fresh install 是整链跑完的
//      终态；「同一迁移内先建 FK 后建键」的时序错误由 integration gate 的
//      「startup 已知缺口 = 0」判据兜底，不在本门口径内；
//   ③ 裸 `REFERENCES t`（不带列清单）按引用目标表 PRIMARY KEY 处理。
//
// @see sql/migrations/startup/936_subscription_plan_entitlements.sql（缺陷修复本体）
// @see scripts/ursmcheck/dangling_relation_gate_test.go（同族：静态 DDL 解析 + 双向豁免棘轮）

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// fkGateRepoRoot = 仓根（本文件在 scripts/ursmcheck/ 下 ⇒ 往上两级）。
const fkGateRepoRoot = "../.."

// ─────────────────────────────────────────────────────────────────────────────
// 第一部分：SQL 净化器 —— 把字符串/注释内容替换成空格，保留换行（行号不变）。
//
// 为什么必须做这一步：基线 01-schema.sql 前段全是 `AS $$...$$` 的存储过程，
// 迁移里也有 $tag$ 形态；不做词法级处理，「按行找 $$ 跳过」会在奇数个 $$ 或
// 字面量内 $$ 上失配，把半个文件的 DDL 吃掉（原型阶段实测撞过）。净化后
// 所有模式匹配只看「真正的代码字符」，字面量造假锚点的整类问题消失。
// ─────────────────────────────────────────────────────────────────────────────

type fkGateLexerState int

const (
	fkGateNormal fkGateLexerState = iota
	fkGateLineComment
	fkGateBlockComment
	fkGateSingleQuote
	fkGateDoubleQuote
	fkGateDollar
)

// fkGateIndexRune 在 runes[from:] 里找 tag 的第一个出现（rune 空间），无则 -1。
func fkGateIndexRune(runes, tag []rune, from int) int {
	if len(tag) == 0 {
		return -1
	}
	for i := from; i+len(tag) <= len(runes); i++ {
		match := true
		for j := range tag {
			if runes[i+j] != tag[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

// fkGateSanitizeSQL 把 input 中字符串字面量 / 注释里的字符替换为空格（换行保留），
// 返回与 input 等长的文本 ⇒ 行号逐行对应。
//
// 语法覆盖：`--` 行注释、`/* /* 嵌套 */ */` 块注释（PG 支持嵌套）、
// `'...'`（” 转义）、`"..."` 标识符、`$tag$...$tag$` / `$$...$$` dollar 字符串。
// 已知近似：E'...' 的 \' 转义按普通字符处理（见文件头「已知近似」①）。
func fkGateSanitizeSQL(input string) string {
	out := []rune(input)
	runes := []rune(input)
	n := len(runes)
	isTagWord := func(r rune) bool {
		return r == '_' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
	}
	state := fkGateNormal
	dollarTag := ""
	blockDepth := 0
	for i := 0; i < n; i++ {
		c := runes[i]
		switch state {
		case fkGateNormal:
			switch {
			case c == '-' && i+1 < n && runes[i+1] == '-':
				state = fkGateLineComment
				out[i] = ' '
			case c == '/' && i+1 < n && runes[i+1] == '*':
				state = fkGateBlockComment
				blockDepth = 1
				out[i] = ' '
				out[i+1] = ' '
				i++
			case c == '\'':
				state = fkGateSingleQuote
				out[i] = ' '
			case c == '"':
				state = fkGateDoubleQuote
				out[i] = ' '
			case c == '$':
				// 尝试识别 $tag$ / $$；不成形则当普通字符。
				j := i + 1
				for j < n && isTagWord(runes[j]) {
					j++
				}
				if j < n && runes[j] == '$' && j > i+1 || (j == i+1 && j < n && runes[j] == '$') {
					dollarTag = string(runes[i : j+1])
					for k := i; k <= j; k++ {
						out[k] = ' '
					}
					state = fkGateDollar
					i = j
				}
			}
		case fkGateLineComment:
			if c == '\n' {
				state = fkGateNormal
			} else {
				out[i] = ' '
			}
		case fkGateBlockComment:
			if c == '/' && i+1 < n && runes[i+1] == '*' {
				blockDepth++
				out[i] = ' '
				out[i+1] = ' '
				i++
			} else if c == '*' && i+1 < n && runes[i+1] == '/' {
				blockDepth--
				out[i] = ' '
				out[i+1] = ' '
				i++
				if blockDepth == 0 {
					state = fkGateNormal
				}
			} else if c != '\n' {
				out[i] = ' '
			}
		case fkGateSingleQuote:
			if c == '\'' {
				if i+1 < n && runes[i+1] == '\'' {
					out[i] = ' '
					out[i+1] = ' '
					i++
				} else {
					state = fkGateNormal
				}
			}
			if c != '\n' {
				out[i] = ' '
			}
		case fkGateDoubleQuote:
			if c == '"' {
				if i+1 < n && runes[i+1] == '"' {
					out[i] = ' '
					out[i+1] = ' '
					i++
				} else {
					state = fkGateNormal
				}
			}
			if c != '\n' {
				out[i] = ' '
			}
		case fkGateDollar:
			if c == '$' {
				// 找同 tag 的闭串（PG 语义：同 tag 才闭合）。
				// ★ 必须在 rune 空间里找：dollar 体内可以有中文等多字节字符，
				//   按 string 字节偏移会错位越界（fixture 阶段实测 panic）。
				tag := []rune(dollarTag)
				idx := fkGateIndexRune(runes, tag, i)
				if idx < 0 {
					// 未闭合：余下全文按字符串处理。
					for k := i; k < n; k++ {
						if runes[k] != '\n' {
							out[k] = ' '
						}
					}
					i = n
					break
				}
				end := idx + len(tag)
				for k := i; k < end; k++ {
					if runes[k] != '\n' {
						out[k] = ' '
					}
				}
				i = end - 1 // for 循环会再 +1
				state = fkGateNormal
				dollarTag = ""
			} else if c != '\n' {
				out[i] = ' '
			}
		}
	}
	return string(out)
}

// ─────────────────────────────────────────────────────────────────────────────
// 第二部分：净化文本上的三种语句形态解析。
// ─────────────────────────────────────────────────────────────────────────────

var (
	fkGateReCreateTable = regexp.MustCompile(`^CREATE TABLE (?:IF NOT EXISTS )?(?:public\.)?(\w+)\s*\($`)
	fkGateReAlterTable  = regexp.MustCompile(`^ALTER TABLE (?:ONLY )?(?:public\.)?(\w+)`)
	fkGateReCreateIndex = regexp.MustCompile(`(?i)^CREATE (UNIQUE )?INDEX`)

	// 表体 blob 上的表级约束（PRIMARY KEY / UNIQUE，含 PG15 NULLS NOT DISTINCT）。
	fkGateReTableLevelKey = regexp.MustCompile(`(?im)^(?:[ \t]*CONSTRAINT \w+ )?(PRIMARY KEY|UNIQUE)(?: NULLS NOT DISTINCT)? \(([^)]+)\)`)
	// 表体 blob 上的表级 FOREIGN KEY。
	fkGateReTableLevelFK = regexp.MustCompile(`(?im)^(?:[ \t]*CONSTRAINT \w+ )?FOREIGN KEY \(([^)]*)\) REFERENCES (?:public\.)?(\w+)\s*(?:\(([^)]*)\))?`)

	// 列级（同一行内）：裸 PRIMARY KEY / UNIQUE（后面不跟 "("）。
	fkGateReColPK       = regexp.MustCompile(`(?i)\bPRIMARY KEY\b`)
	fkGateReColPKParen  = regexp.MustCompile(`(?i)\bPRIMARY KEY[ \t]*\(`)
	fkGateReColUnique   = regexp.MustCompile(`(?i)\bUNIQUE\b`)
	fkGateReColUniqPar  = regexp.MustCompile(`(?i)\bUNIQUE[ \t]*\(`)
	fkGateReInlineFKRef = regexp.MustCompile(`(?i)\bREFERENCES (?:public\.)?(\w+)[ \t]*(?:\(([^)]*)\))?`)

	// ALTER TABLE 语句（多行拼接后）上的 ADD CONSTRAINT。
	fkGateReAlterKey = regexp.MustCompile(`(?i)ADD CONSTRAINT \w+ (PRIMARY KEY|UNIQUE)(?: NULLS NOT DISTINCT)? \(([^)]*)\)`)
	fkGateReAlterFK  = regexp.MustCompile(`(?i)ADD (?:CONSTRAINT \w+ )?FOREIGN KEY \(([^)]*)\) REFERENCES (?:ONLY )?(?:public\.)?(\w+)[ \t]*(?:\(([^)]*)\))?`)

	// CREATE UNIQUE INDEX ... ON tbl (cols)。
	fkGateReIndexOn = regexp.MustCompile(`(?i)\bON (?:ONLY )?(?:public\.)?(\w+)[ \t]*(?:USING \w+)?[ \t]*\(([^)]*)\)`)

	fkGateReIdentifier = regexp.MustCompile(`^\w+$`)
	fkGateReInclude    = regexp.MustCompile(`(?i)\)[ \t]*INCLUDE[ \t]*\(`)
	fkGateReFirstWord  = regexp.MustCompile(`^(\w+)`)
)

// fkGateNormCols 把 "(a, b DESC)" / "(lower(c))" / "(a) INCLUDE (b)" 归一成列名清单；
// 非纯标识符的项原样保留（FK 目标键判定时会被拒掉 —— 表达式列当不了 FK 键）。
func fkGateNormCols(raw string) []string {
	cut := fkGateReInclude.ReplaceAllString(raw, ",")
	var out []string
	for _, c := range strings.Split(cut, ",") {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		c = strings.Trim(c, `"`)
		if idx := strings.IndexAny(c, " \t"); idx >= 0 {
			c = c[:idx] // 去 ASC/DESC/operclass 等后缀
		}
		out = append(out, c)
	}
	return out
}

type fkGateKey struct {
	kind string // PRIMARY KEY / UNIQUE / UNIQUE INDEX
	cols []string
	src  string
}

type fkGateFK struct {
	src    string // 文件基名（多文件重复声明时逗号并列）
	line   int    // 语句起始行（1-based）
	table  string // FK 所在表
	cols   []string
	target string
	tcols  []string // 空清单 = 裸 REFERENCES（按引用目标 PK 处理）
}

// fkGateKeyID 豁免清单的键形状（顺序无关性由调用方排序保证 —— 本仓 DDL 列序
// 在 FK 与键两侧一致，这里按声明序原样拼）。
func fkGateFKID(fk fkGateFK) string {
	return fmt.Sprintf("%s(%s)->%s(%s)",
		fk.table, strings.Join(fk.cols, ","), fk.target, strings.Join(fk.tcols, ","))
}

type fkGateParser struct {
	tables map[string]string // 表名 → 首个声明源
	keys   map[string][]fkGateKey
	fks    []fkGateFK
	fkSeen map[string]bool
}

func newFkGateParser() *fkGateParser {
	return &fkGateParser{
		tables: map[string]string{},
		keys:   map[string][]fkGateKey{},
		fkSeen: map[string]bool{},
	}
}

func (p *fkGateParser) addKey(tbl, kind string, cols []string, src string) {
	p.keys[tbl] = append(p.keys[tbl], fkGateKey{kind: kind, cols: cols, src: src})
}

func (p *fkGateParser) addFK(fk fkGateFK) {
	id := fkGateFKID(fk)
	if p.fkSeen[id] {
		for i := range p.fks {
			if fkGateFKID(p.fks[i]) == id {
				if !strings.Contains(p.fks[i].src, fk.src) {
					p.fks[i].src += "," + fk.src
				}
				return
			}
		}
		return
	}
	p.fkSeen[id] = true
	p.fks = append(p.fks, fk)
}

// parseSanitized 在净化后的行上跑三种语句形态的提取。
func (p *fkGateParser) parseSanitized(src string, lines []string) {
	n := len(lines)
	i := 0
	for i < n {
		s := strings.TrimSpace(lines[i])
		if s == "" {
			i++
			continue
		}
		if m := fkGateReCreateTable.FindStringSubmatch(s); m != nil {
			tbl := m[1]
			if _, ok := p.tables[tbl]; !ok {
				p.tables[tbl] = src
			}
			i++
			type bodyLine struct {
				line int
				text string
			}
			var body []bodyLine
			for i < n {
				bl := strings.TrimSpace(lines[i])
				line := i + 1
				i++
				if bl == "" {
					continue
				}
				if strings.HasPrefix(bl, ")") { // ");" 与分区表的裸 ")" 都是体结束
					break
				}
				bl = regexp.MustCompile(`,[ \t]*$`).ReplaceAllString(bl, "")
				body = append(body, bodyLine{line: line, text: bl})
			}
			// 列级形态（逐行）：列名在行首；REFERENCES 可能折行到下一行
			//（此时行首是 REFERENCES，列名取上一行首词）。
			// ★ 表级约束行（CONSTRAINT x FOREIGN KEY ... / PRIMARY KEY (...) /
			//   UNIQUE (...) / CHECK (...)）在这里【跳过】——它们由下面的
			//   体 blob 表级正则处理；两处都收会把同一条 FK 收成两条
			//   （其中一条的「列名」是 CONSTRAINT 这个首词 —— 原型阶段
			//   实测撞过，fixture 自检钉住这一条）。
			lastCol := ""
			for _, bl := range body {
				fw := fkGateReFirstWord.FindStringSubmatch(bl.text)
				if fw == nil {
					continue
				}
				col := fw[1]
				isConstraintLine := strings.EqualFold(col, "CONSTRAINT") || strings.EqualFold(col, "FOREIGN") ||
					strings.EqualFold(col, "PRIMARY") || strings.EqualFold(col, "UNIQUE") || strings.EqualFold(col, "CHECK")
				if isConstraintLine {
					// 表级行只推进 lastCol 的清空（它们不是列定义）。
					lastCol = ""
					continue
				}
				if strings.EqualFold(col, "REFERENCES") && lastCol != "" {
					col = lastCol // 折行的列内联 REFERENCES
				} else {
					lastCol = col
				}
				if fkGateReColPK.MatchString(bl.text) && !fkGateReColPKParen.MatchString(bl.text) {
					p.addKey(tbl, "PRIMARY KEY", []string{col}, src)
				}
				if fkGateReColUnique.MatchString(bl.text) && !fkGateReColUniqPar.MatchString(bl.text) {
					p.addKey(tbl, "UNIQUE", []string{col}, src)
				}
				if mf := fkGateReInlineFKRef.FindStringSubmatch(bl.text); mf != nil {
					tcols := []string{}
					if mf[2] != "" {
						tcols = fkGateNormCols(mf[2])
					}
					p.addFK(fkGateFK{src: src, line: bl.line, table: tbl, cols: []string{col},
						target: mf[1], tcols: tcols})
				}
			}
			// 表级形态（整个体 blob 上找）。
			var sb strings.Builder
			for _, bl := range body {
				sb.WriteString(bl.text)
				sb.WriteByte('\n')
			}
			blob := sb.String()
			for _, mk := range fkGateReTableLevelKey.FindAllStringSubmatch(blob, -1) {
				p.addKey(tbl, strings.ToUpper(mk[1]), fkGateNormCols(mk[2]), src)
			}
			for _, mf := range fkGateReTableLevelFK.FindAllStringSubmatch(blob, -1) {
				tcols := []string{}
				if mf[3] != "" {
					tcols = fkGateNormCols(mf[3])
				}
				startLine := 0
				if len(body) > 0 {
					startLine = body[0].line
				}
				p.addFK(fkGateFK{src: src, line: startLine, table: tbl,
					cols: fkGateNormCols(mf[1]), target: mf[2], tcols: tcols})
			}
			continue
		}
		if m := fkGateReAlterTable.FindStringSubmatch(s); m != nil {
			tbl := m[1]
			startLine := i + 1
			stmtLines := []string{s}
			for !strings.HasSuffix(stmtLines[len(stmtLines)-1], ";") && i+1 < n {
				i++
				stmtLines = append(stmtLines, strings.TrimSpace(lines[i]))
			}
			i++
			stmt := strings.Join(stmtLines, " ")
			for _, mk := range fkGateReAlterKey.FindAllStringSubmatch(stmt, -1) {
				p.addKey(tbl, strings.ToUpper(mk[1]), fkGateNormCols(mk[2]), src)
			}
			for _, mf := range fkGateReAlterFK.FindAllStringSubmatch(stmt, -1) {
				tcols := []string{}
				if mf[3] != "" {
					tcols = fkGateNormCols(mf[3])
				}
				p.addFK(fkGateFK{src: src, line: startLine, table: tbl,
					cols: fkGateNormCols(mf[1]), target: mf[2], tcols: tcols})
			}
			continue
		}
		if mi := fkGateReCreateIndex.FindStringSubmatch(s); mi != nil {
			stmtLines := []string{s}
			for !strings.HasSuffix(stmtLines[len(stmtLines)-1], ";") && i+1 < n {
				i++
				stmtLines = append(stmtLines, strings.TrimSpace(lines[i]))
			}
			i++
			stmt := strings.Join(stmtLines, " ")
			// 只有 UNIQUE 且无 WHERE（非部分索引）才配作 FK 目标键。
			if mi[1] != "" && !regexp.MustCompile(`(?i)\bWHERE\b`).MatchString(stmt) {
				if mo := fkGateReIndexOn.FindStringSubmatch(stmt); mo != nil {
					cols := fkGateNormCols(mo[2])
					plain := len(cols) > 0
					for _, c := range cols {
						if !fkGateReIdentifier.MatchString(c) {
							plain = false
						}
					}
					if plain {
						p.addKey(mo[1], "UNIQUE INDEX", cols, src)
					}
				}
			}
			continue
		}
		i++
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 第三部分：量具自检（fixture 级 —— 量具坏了 ⇒ 门直接红，而不是门绿量具瞎）。
// ─────────────────────────────────────────────────────────────────────────────

func TestFkGateSanitizerHidesStringsAndKeepsLines(t *testing.T) {
	in := "-- comment REFERENCES evil(id)\n" +
		"CREATE TABLE public.t (\n" +
		"    note text DEFAULT 'fake REFERENCES evil(id)',\n" +
		"    body text,\n" +
		"    proc text DEFAULT $$BEGIN PERFORM 1; END$$,\n" +
		"    id integer PRIMARY KEY\n" +
		");\n"
	out := fkGateSanitizeSQL(in)
	if got, want := strings.Count(out, "\n"), strings.Count(in, "\n"); got != want {
		t.Fatalf("净化器必须保行号：in=%d 行 out=%d 行", want, got)
	}
	if strings.Contains(out, "REFERENCES evil") {
		t.Fatalf("注释/字面量里的 REFERENCES 必须被净化掉，残留：%q", out)
	}
	if !strings.Contains(out, "id integer PRIMARY KEY") {
		t.Fatalf("真代码字符不得被净化：%q", out)
	}
	// dollar 字符串（含内部分号）必须整体净化。
	if strings.Contains(out, "PERFORM") {
		t.Fatalf("$$ 字符串体必须整体净化：%q", out)
	}
}

func TestFkGateParserExtractsAllFourDeclarationShapes(t *testing.T) {
	sql := fkGateSanitizeSQL(`
CREATE TABLE public.parent_a (
    id integer NOT NULL,
    code character varying(32) NOT NULL,
    UNIQUE (code)
);
CREATE TABLE public.parent_b (
    bar integer NOT NULL,
    baz text NOT NULL,
    -- PK 覆盖 (bar, baz) 与 child 的 FK 列集精确相等 ⇒ 合法
    //（若 PK 只有 (bar)，FK→(bar,baz) 按 PG 语义同样是 42870 —— 精确覆盖口径）
    CONSTRAINT parent_b_pkey PRIMARY KEY (bar, baz)
);
CREATE TABLE public.child (
    id serial PRIMARY KEY,
    a_id integer NOT NULL REFERENCES parent_a(id),
    b_bar integer NOT NULL,
    b_baz text NOT NULL,
    CONSTRAINT fk_child_b FOREIGN KEY (b_bar, b_baz) REFERENCES parent_b (bar, baz) ON DELETE CASCADE
);
ALTER TABLE public.child
    ADD CONSTRAINT child_unique_pair UNIQUE (a_id, b_bar);
CREATE UNIQUE INDEX parent_a_code_key ON public.parent_a USING btree (code);
`)
	p := newFkGateParser()
	p.parseSanitized("fixture.sql", strings.Split(sql, "\n"))

	if len(p.tables) != 3 {
		t.Fatalf("应解析出 3 张表，实得 %v", p.tables)
	}
	// 列级 REFERENCES（含折行场景由 612 真文件覆盖）与表级 FOREIGN KEY。
	if len(p.fks) != 2 {
		t.Fatalf("应解析出 2 条 FK，实得 %+v", p.fks)
	}
	// UNIQUE(code) 表级键 + parent_b 的 CONSTRAINT PK + child 的列级 PK
	// + child 的 ALTER UNIQUE + parent_a 的 CREATE UNIQUE INDEX。
	if len(p.keys["parent_a"]) != 2 {
		t.Fatalf("parent_a 应有 2 个键（表级 UNIQUE + CREATE UNIQUE INDEX），实得 %+v", p.keys["parent_a"])
	}
	if len(p.keys["parent_b"]) != 1 || p.keys["parent_b"][0].kind != "PRIMARY KEY" {
		t.Fatalf("parent_b 应有 CONSTRAINT 形态 PK，实得 %+v", p.keys["parent_b"])
	}
	// 覆盖判定：child(a_id)→parent_a(id) 无键（parent_a 只有 code 键）⇒ 违例；
	// child(b_bar,b_baz)→parent_b(bar,baz) 精确覆盖 ⇒ 合法。
	violations := fkGateViolations(p)
	if len(violations) != 1 {
		t.Fatalf("fixture 应恰有 1 条违例（child→parent_a(id)），实得 %+v", violations)
	}
	if violations[0].target != "parent_a" {
		t.Fatalf("违例目标应为 parent_a，实得 %+v", violations[0])
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 第四部分：主门 —— 真仓全量解析 + 双向豁免棘轮。
// ─────────────────────────────────────────────────────────────────────────────

// exemptedFkTargetGaps = 已定性的「静态缺键」豁免清单（FK 形状 → 理由）。
//
// ★★ 【每条必须有具体理由】（同 dangling_relation 门纪律：写「同上」= 没写）。
// ★★ 【双向棘轮】：缺口消失（如基线哪天补了 PK）⇒ 豁免必须同步删；
//
//	反过来，豁免了不存在的缺口 ⇒ 门也红（防止豁免表预先做大）。
//
// ★★ 这些缺口的共同形态：修复键的语句在 `$$` 动态 SQL（幂等 DO 块）里，
//
//	静态口径刻意不可见（见文件头「刻意不解析的东西」）。
//
// ★★ 棘轮闭合史：`subscription_plan_entitlements(plan_id)->subscription_plans(id)`
// 曾因 936 DO 块自愈在此豁免（f2af856c1）；并行会话 5be6b2f0e（同日）把
// subscription_plans_pkey 补进了基线三份副本（全新安装 42803 中止的修复）⇒
// 静态缺口消失 ⇒ 按双向棘轮「缺口消失必须删豁免」**自动摘除**（门当场红、
// 按红修）—— 这正是棘轮设计的行为，不是判据放松。
var exemptedFkTargetGaps = map[string]string{
	// 612 同文件先执行幂等 DO 块（10-20 行）补 credential_model_bindings_pkey，
	// 再建 credential_model_capabilities 表 ⇒ 运行期 FK 必然成立。
	"credential_model_capabilities(credential_model_binding_id)->credential_model_bindings(id)": "612 同文件幂等 DO 块先补 credential_model_bindings_pkey 再建本表（静态口径看不到 $$ 内的键）⇒ 运行期自愈",
	// 825 同文件 DO 块（75-90 行）幂等补 models_canonical_pkey，再建
	// model_modality_verification 并引用 models_canonical(id)。
	"model_modality_verification(canonical_id)->models_canonical(id)": "825 同文件幂等 DO 块先补 models_canonical_pkey 再建本表（静态口径看不到 $$ 内的键）⇒ 运行期自愈",
}

// fkGateViolations 返回「目标表缺精确覆盖键」的 FK 清单（按 src:line 排序稳定输出）。
func fkGateViolations(p *fkGateParser) []fkGateFK {
	var out []fkGateFK
	for _, fk := range p.fks {
		if _, ok := p.tables[fk.target]; !ok {
			out = append(out, fk) // 目标表在静态 DDL 里不存在
			continue
		}
		want := map[string]bool{}
		for _, c := range fk.tcols {
			want[c] = true
		}
		bareRef := len(fk.tcols) == 0 // 裸 REFERENCES = 引用目标 PK
		covered := false
		for _, k := range p.keys[fk.target] {
			if bareRef {
				if k.kind == "PRIMARY KEY" {
					covered = true
				}
				continue
			}
			if len(k.cols) != len(fk.tcols) {
				continue
			}
			got := map[string]bool{}
			for _, c := range k.cols {
				got[c] = true
			}
			same := len(got) == len(want)
			for c := range want {
				if !got[c] {
					same = false
				}
			}
			if same {
				covered = true
			}
		}
		if !covered {
			out = append(out, fk)
		}
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].src != out[b].src {
			return out[a].src < out[b].src
		}
		return out[a].line < out[b].line
	})
	return out
}

// fkGateResolveSources 返回基线三件 + 已注册 startup 迁移链（TSV 顺序）。
// 注册项解析不到文件 ⇒ 直接 Fatal（清单与磁盘漂移本身就是回归）。
func fkGateResolveSources(t *testing.T) []string {
	t.Helper()
	base := []string{
		filepath.Join(fkGateRepoRoot, "sql", "schema", "00-prereqs.sql"),
		filepath.Join(fkGateRepoRoot, "sql", "schema", "01-schema.sql"),
		filepath.Join(fkGateRepoRoot, "sql", "schema", "02-seed.sql"),
	}
	roots := []string{
		filepath.Join(fkGateRepoRoot, "sql", "migrations", "startup"),
		filepath.Join(fkGateRepoRoot, "sql", "migrations", "startup", "up"),
		filepath.Join(fkGateRepoRoot, "installer", "cmd", "llm-gw-installer", "embeddata", "startup"),
	}
	tsvPath := filepath.Join(fkGateRepoRoot, "sql", "schema", "installed_startup_migrations.tsv")
	raw, err := os.ReadFile(tsvPath)
	if err != nil {
		t.Fatalf("读注册链 TSV 失败: %v", err)
	}
	var out []string
	out = append(out, base...)
	for _, ln := range strings.Split(string(raw), "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" || strings.HasPrefix(ln, "#") {
			continue
		}
		fields := strings.Split(ln, "\t")
		name := fields[len(fields)-1]
		found := ""
		for _, r := range roots {
			cand := filepath.Join(r, name)
			if st, err := os.Stat(cand); err == nil && !st.IsDir() {
				found = cand
				break
			}
		}
		if found == "" {
			t.Fatalf("注册链里的迁移在三个根目录都解析不到: %q（TSV=%s）——清单与磁盘漂移，先修注册链", name, tsvPath)
		}
		out = append(out, found)
	}
	return out
}

func TestFkTargetsHaveKeyableConstraint(t *testing.T) {
	p := newFkGateParser()
	sources := fkGateResolveSources(t)
	for _, path := range sources {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("读源文件失败 %s: %v", path, err)
		}
		sanitized := fkGateSanitizeSQL(string(raw))
		// 不变量：净化器只把非换行字符替换成空格 ⇒ 换行数与行结构必须逐行不变。
		// （多字节字符换成 1 字节空格会改变【字节】长度 —— 那不是问题；
		//   本门需要的是【行号】不变，所以钉换行数而不是字节数。）
		if got, want := strings.Count(sanitized, "\n"), strings.Count(string(raw), "\n"); got != want {
			t.Fatalf("净化器必须保行结构（换行数不变）: %s sanitized=%d raw=%d", path, got, want)
		}
		p.parseSanitized(filepath.Base(path), strings.Split(sanitized, "\n"))
	}

	// 量具未腐化门（读数掉到这个量级说明解析器坏了 —— 沿用 navCoverage/metricguard 纪律）。
	if len(p.tables) < 250 {
		t.Fatalf("解析出的表数仅 %d（历史量级 ~403）—— 解析器疑似腐化", len(p.tables))
	}
	if totalKeys := func() int {
		n := 0
		for _, ks := range p.keys {
			n += len(ks)
		}
		return n
	}(); totalKeys < 350 {
		t.Fatalf("解析出的 PK/UNIQUE 键数仅 %d（历史量级 ~498）—— 解析器疑似腐化", totalKeys)
	}
	if len(p.fks) < 40 {
		t.Fatalf("解析出的 FK 数仅 %d（历史量级 ~59）—— 解析器疑似腐化", len(p.fks))
	}

	// 头号案例必须可见：612 修的那个形状必须仍被解析器抓到并被豁免。
	// （否则解析器悄悄变瞎、豁免表变成死重，门对同族新缺陷的牙就没了。）
	// ⚠ 原旗舰案例（936 的 subscription_plan_entitlements）的静态缺口已被
	// 5be6b2f0e 补基线主键消除 ⇒ 旗舰换到仍豁免中的 612 形状。
	headline := "credential_model_capabilities(credential_model_binding_id)->credential_model_bindings(id)"
	foundHeadline := false
	foundExempted := false
	for _, fk := range p.fks {
		if fkGateFKID(fk) == headline {
			foundHeadline = true
			if _, ok := exemptedFkTargetGaps[headline]; ok {
				foundExempted = true
			}
		}
	}
	if !foundHeadline {
		t.Fatalf("旗舰案例 %s 未被解析器抓到 —— FK 提取腐化，豁免表正在空转", headline)
	}
	if !foundExempted {
		t.Fatalf("旗舰案例 %s 未被豁免表登记 —— 豁免表与静态缺口失配", headline)
	}

	violations := fkGateViolations(p)

	// 判据 ①：每条违例都必须在豁免清单里且理由具体。
	var unregistered []string
	for _, fk := range violations {
		id := fkGateFKID(fk)
		reason, ok := exemptedFkTargetGaps[id]
		if !ok {
			unregistered = append(unregistered,
				fmt.Sprintf("  %s:%d  %s  [目标表现有键: %s]",
					fk.src, fk.line, id, fkGateAvailableKeys(p, fk.target)))
			continue
		}
		if strings.TrimSpace(reason) == "" {
			t.Fatalf("豁免条目理由为空（写「同上」= 没写）: %s", id)
		}
	}
	if len(unregistered) > 0 {
		t.Fatalf("发现 %d 条「FK 目标表无 PK/UNIQUE 精确覆盖」的静态缺口（PG 42870 同族，f2af856c1 即此形）：\n%s\n"+
			"修法二选一：给目标表补 PRIMARY KEY/UNIQUE 精确覆盖该列集；"+
			"或在 exemptedFkTargetGaps 登记豁免并写明具体理由（运行期自愈的 DO 块属后者）。",
			len(unregistered), strings.Join(unregistered, "\n"))
	}

	// 判据 ②：豁免清单不许做大 —— 每条豁免都必须仍对应一条当前真违例。
	var stale []string
	for id := range exemptedFkTargetGaps {
		still := false
		for _, fk := range violations {
			if fkGateFKID(fk) == id {
				still = true
				break
			}
		}
		if !still {
			stale = append(stale, id)
		}
	}
	if len(stale) > 0 {
		t.Fatalf("豁免条目已失效（静态缺口已消失，应删除豁免）：\n  %s",
			strings.Join(stale, "\n  "))
	}
}

// fkGateAvailableKeys 供失败消息展示目标表现有键（帮助定位「差在哪」）。
func fkGateAvailableKeys(p *fkGateParser, tbl string) string {
	parts := []string{}
	for _, k := range p.keys[tbl] {
		parts = append(parts, fmt.Sprintf("%s(%s)", k.kind, strings.Join(k.cols, ",")))
	}
	if len(parts) == 0 {
		return "（无任何键）"
	}
	return strings.Join(parts, " / ")
}
