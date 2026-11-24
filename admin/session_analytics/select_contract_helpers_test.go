package sessionanalytics

// select_contract_helpers_test.go —— SELECT 列表 / rows.Scan 目标的形状解析
// 辅助（2026-11-24 修订轮）。
//
// ★ 为什么需要这层解析（而不是拿正则凑合）：
//
//	这几条要钉的判据全都是**形状**判据：
//	  · 列表 SQL 里有没有 SELECT 出某个别名（分列成本）；
//	  · 某个别名的表达式里聚合的是哪一列（两列写反了）；
//	  · SELECT 的列数 == rows.Scan 的目标数（漏扫 ⇒ 该列恒 0）。
//	正则只能钉"字符串出现过"，钉不住"这是第几列"和"扫了几个目标" ⇒ 这里
//	写一个**够用就好**的迷你解析器：按顶层逗号切 SELECT 列表（括号内、
//	字符串内、注释内的逗号都不算分隔符），按顶层 " AS " 取别名。
//
// ★ 为什么值得自己写而不是引第三方 SQL 解析器：
//	本仓的门要在**零外部依赖**的静态层跑（真库门另算），而 SQL 解析器会
//	带来一大坨依赖与版本面；这里只需要"列数 + 别名 + 表达式"三件事，
//	自己写 200 行可审计的代码更符合本仓的"零假绿、零依赖"纪律。
//
// ★ 解析的两条纪律：
//	① 注释**先剥掉**再切列 —— 否则「给某一列写的说明注释里提到了另一列的
//	   名字」会污染该列的表达式文本（例如把 external_cost 写进
//	   internal_cost_usd 的表达式里 ⇒ 门误判"两列写反"）。
//	② 字符串字面量在**定位**时打码、在**取文本**时保留 —— 定位用打码
//	   文本（避免 'select' 之类字面量被当成关键字），取文本用原文。

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// selectColumn 一列 SELECT 表达式的解析结果。
type selectColumn struct {
	// Alias 该列的输出名：AS 别名；没有 AS 时取列引用本身的列名
	//（与 PG 的默认输出列名一致，如 "a.owner" → "owner"）。
	Alias string
	// Expr 该列的原始表达式文本（已剥注释、不含别名本身）。
	Expr string
}

// qualifiedIdentRE 匹配「（表名.）列名」形式的简单列引用。
var qualifiedIdentRE = regexp.MustCompile(`^(?:[A-Za-z_][A-Za-z0-9_$]*\.)?([A-Za-z_][A-Za-z0-9_$]*)$`)

// sqlSelectColumns 解析一段 SQL 的 SELECT 列表，返回每列的「别名 + 表达式」。
//
// 按**顶层**逗号切分（括号内、字符串内、注释内的逗号都不算分隔符），
// 因此 SUM(a.x + a.y) / COALESCE(..., 0) 这类表达式不会被切坏。
func sqlSelectColumns(t *testing.T, sqlText string) []selectColumn {
	t.Helper()
	list, ok := selectListText(sqlText)
	if !ok {
		t.Fatalf("SQL 里找不到顶层 SELECT 列表（片段：%.120q）", sqlText)
	}
	items := splitTopLevelCommas(list)
	cols := make([]selectColumn, 0, len(items))
	for _, item := range items {
		expr, alias := splitAlias(item)
		cols = append(cols, selectColumn{Alias: alias, Expr: expr})
	}
	return cols
}

// hasSelectAlias 该列清单里有没有指定别名的列。
func hasSelectAlias(cols []selectColumn, alias string) bool {
	for _, c := range cols {
		if c.Alias == alias {
			return true
		}
	}
	return false
}

// selectAliasExpr 取该别名的原始表达式文本（找不到时返回空串 ——
// 调用方据此断言"该别名必须存在"）。
func selectAliasExpr(cols []selectColumn, alias string) string {
	for _, c := range cols {
		if c.Alias == alias {
			return c.Expr
		}
	}
	return ""
}

// selectListText 取 SELECT 与 FROM 之间的列表文本（顶层关键字）。
func selectListText(sqlText string) (string, bool) {
	clean := maskSQLComments(sqlText)
	masked := maskSQLLiterals(clean)

	start := findTopLevelKeyword(masked, "select")
	if start < 0 {
		return "", false
	}
	from := findTopLevelKeyword(masked[start:], "from")
	if from < 0 {
		return "", false
	}
	return clean[start+len("select") : start+from], true
}

// splitAlias 拆出「表达式 + 别名」。
//
// ★ 取**最后一个**顶层 " AS "：PG 允许 SELECT a AS b，但本仓的 SQL 里
//
//	"AS" 只出现在别名位置 ⇒ 最后一个就是别名。找不到 AS 时，若整列是
//	简单列引用 ⇒ 用列名当别名（PG 的默认输出列名）。
func splitAlias(item string) (string, string) {
	clean := strings.TrimSpace(maskSQLComments(item))
	masked := maskSQLLiterals(clean)

	pos := -1
	depth := 0
	for i := 0; i < len(masked); {
		switch masked[i] {
		case '(':
			depth++
			i++
		case ')':
			depth--
			i++
		default:
			if depth == 0 && isWordBoundary(masked, i) &&
				strings.EqualFold(clean[i:minInt(i+2, len(clean))], "as") &&
				!isWordCharAt(masked, i+2) {
				pos = i
				i += 2
				continue
			}
			i++
		}
	}
	if pos >= 0 {
		expr := strings.TrimSpace(clean[:pos])
		alias := strings.TrimSpace(clean[pos+2:])
		alias = strings.Trim(alias, `"`)
		return expr, alias
	}
	if m := qualifiedIdentRE.FindStringSubmatch(clean); m != nil {
		return clean, m[1]
	}
	return clean, ""
}

// scanDestinationsOf 数某个函数里 rows.Scan 的目标个数。
//
// ★ 为什么要数它：SELECT 少一列而 Scan 多扫一个（或反之）⇒ 编译通过、
//
//	运行期报类型错，甚至静默串列；而"结构体里声明了字段但 SQL 没 SELECT"
//	这一类缺陷（恒 0）在 JSON 形状钉测里**完全看不出来** ⇒ 必须有一层
//	"列数 == 扫描目标数"的形状判据。
//
// 实现：按 funcMarker 定位函数 → 取函数体（花括号配平）→ 找第一处
// "rows.Scan(" → 括号配平取实参文本 → 按**顶层**逗号切分计数。
func scanDestinationsOf(t *testing.T, src, funcMarker string) int {
	t.Helper()
	idx := strings.Index(src, funcMarker)
	if idx < 0 {
		t.Fatalf("源码里找不到 %q", funcMarker)
	}
	body := goFuncBody(src, idx)
	if body == "" {
		t.Fatalf("%s 的函数体没找到（花括号没配上）", funcMarker)
	}
	scanIdx := strings.Index(body, "rows.Scan(")
	if scanIdx < 0 {
		t.Fatalf("%s 里找不到 rows.Scan(", funcMarker)
	}
	open := scanIdx + len("rows.Scan(") - 1 // 指向 '(' 本身
	masked := goCodeMask(body)
	closeIdx := matchParen(masked, open)
	if closeIdx < 0 {
		t.Fatalf("%s 的 rows.Scan( 没有闭合", funcMarker)
	}
	args := body[open+1 : closeIdx]
	return len(splitGoArgs(args))
}

// readPackageSource 读同包源码文件（静态判据用）。
func readPackageSource(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("读 %s 失败：%v", name, err)
	}
	return string(b)
}

// ── SQL 文本工具 ─────────────────────────────────────────────────────

// maskSQLComments 把 SQL 注释抹成空格（长度不变，换行保留）。
//
// ★ 为什么要抹注释：注释里若提到别的列名，会污染该列的表达式文本 ⇒
//
//	"两列写反"的判据会被注释误触发。
func maskSQLComments(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		switch {
		case s[i] == '-' && i+1 < len(s) && s[i+1] == '-':
			for i < len(s) && s[i] != '\n' {
				b.WriteByte(' ')
				i++
			}
		case s[i] == '/' && i+1 < len(s) && s[i+1] == '*':
			depth := 0
			for i < len(s) {
				if s[i] == '/' && i+1 < len(s) && s[i+1] == '*' {
					depth++
					b.WriteString("  ")
					i += 2
					continue
				}
				if s[i] == '*' && i+1 < len(s) && s[i+1] == '/' {
					depth--
					b.WriteString("  ")
					i += 2
					if depth == 0 {
						break
					}
					continue
				}
				if s[i] == '\n' {
					b.WriteByte('\n')
				} else {
					b.WriteByte(' ')
				}
				i++
			}
		case s[i] == '\'' || s[i] == '"':
			quote := s[i]
			b.WriteByte(quote)
			i++
			for i < len(s) {
				if s[i] == quote {
					b.WriteByte(quote)
					i++
					if i < len(s) && s[i] == quote {
						b.WriteByte(quote)
						i++
						continue
					}
					break
				}
				b.WriteByte(s[i])
				i++
			}
		default:
			b.WriteByte(s[i])
			i++
		}
	}
	return b.String()
}

// maskSQLLiterals 把字符串字面量/带引号标识符的**内容**抹成空格
// （长度不变）⇒ 定位关键字时不会把字面量里的 'from' 当成关键字。
func maskSQLLiterals(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] == '\'' || s[i] == '"' {
			quote := s[i]
			b.WriteByte(quote)
			i++
			for i < len(s) {
				if s[i] == quote {
					b.WriteByte(quote)
					i++
					if i < len(s) && s[i] == quote {
						b.WriteByte(quote)
						i++
						continue
					}
					break
				}
				if s[i] == '\n' {
					b.WriteByte('\n')
				} else {
					b.WriteByte(' ')
				}
				i++
			}
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// findTopLevelKeyword 找顶层（括号深度 0）出现的关键字（大小写不敏感）。
func findTopLevelKeyword(masked, keyword string) int {
	depth := 0
	for i := 0; i < len(masked); i++ {
		switch masked[i] {
		case '(':
			depth++
			continue
		case ')':
			depth--
			continue
		}
		if depth != 0 {
			continue
		}
		if !isWordBoundary(masked, i) {
			continue
		}
		end := i + len(keyword)
		if end > len(masked) {
			continue
		}
		if !strings.EqualFold(masked[i:end], keyword) {
			continue
		}
		if isWordCharAt(masked, end) {
			continue
		}
		return i
	}
	return -1
}

// splitTopLevelCommas 按顶层逗号切分（字符串内的逗号不算分隔符）。
func splitTopLevelCommas(text string) []string {
	masked := maskSQLLiterals(text)
	var out []string
	depth := 0
	last := 0
	for i := 0; i < len(masked); i++ {
		switch masked[i] {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, strings.TrimSpace(text[last:i]))
				last = i + 1
			}
		}
	}
	if item := strings.TrimSpace(text[last:]); item != "" {
		out = append(out, item)
	}
	return out
}

func isWordCharAt(s string, i int) bool {
	if i < 0 || i >= len(s) {
		return false
	}
	c := s[i]
	return c == '_' || c == '$' ||
		(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

func isWordBoundary(s string, i int) bool {
	if i > 0 && isWordCharAt(s, i-1) {
		return false
	}
	return true
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ── Go 源码工具 ───────────────────────────────────────────────────────

// goCodeMask 把 Go 源码里的注释与字符串/字符字面量内容抹成空格
// （长度不变、换行保留）⇒ 花括号/圆括号配平时不会被字面量里的括号带偏。
func goCodeMask(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		switch {
		case s[i] == '/' && i+1 < len(s) && s[i+1] == '/':
			for i < len(s) && s[i] != '\n' {
				b.WriteByte(' ')
				i++
			}
		case s[i] == '/' && i+1 < len(s) && s[i+1] == '*':
			for i < len(s) {
				if s[i] == '*' && i+1 < len(s) && s[i+1] == '/' {
					b.WriteString("  ")
					i += 2
					break
				}
				if s[i] == '\n' {
					b.WriteByte('\n')
				} else {
					b.WriteByte(' ')
				}
				i++
			}
		case s[i] == '"' || s[i] == '\'' || s[i] == '`':
			quote := s[i]
			b.WriteByte(' ')
			i++
			for i < len(s) {
				if quote != '`' && s[i] == '\\' && i+1 < len(s) {
					b.WriteString("  ")
					i += 2
					continue
				}
				if s[i] == quote {
					b.WriteByte(' ')
					i++
					break
				}
				if s[i] == '\n' {
					b.WriteByte('\n')
				} else {
					b.WriteByte(' ')
				}
				i++
			}
		default:
			b.WriteByte(s[i])
			i++
		}
	}
	return b.String()
}

// goFuncBody 取从 idx 起那个函数的函数体（含外层花括号）。
func goFuncBody(src string, idx int) string {
	masked := goCodeMask(src)
	open := strings.Index(masked[idx:], "{")
	if open < 0 {
		return ""
	}
	open += idx
	depth := 0
	for i := open; i < len(masked); i++ {
		switch masked[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return src[open : i+1]
			}
		}
	}
	return src[open:]
}

// matchParen 从 open（必须是 '(' 的位置）起找配平的 ')'，返回其下标。
func matchParen(masked string, open int) int {
	if open >= len(masked) || masked[open] != '(' {
		return -1
	}
	depth := 0
	for i := open; i < len(masked); i++ {
		switch masked[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// splitGoArgs 按顶层逗号切分 Go 实参列表。
func splitGoArgs(args string) []string {
	masked := goCodeMask(args)
	var out []string
	depth := 0
	last := 0
	for i := 0; i < len(masked); i++ {
		switch masked[i] {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, strings.TrimSpace(args[last:i]))
				last = i + 1
			}
		}
	}
	if item := strings.TrimSpace(args[last:]); item != "" {
		out = append(out, item)
	}
	return out
}
