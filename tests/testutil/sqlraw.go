package testutil

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SQL raw string 扫描器（仓内**唯一一份**实现）。
//
// ★ 存在理由（2026-08-09 事故）：154 生产切分支把 79.2% 的响应变成 500，
//
//	错误是 `syntax error at or near "audit" (SQLSTATE 42601)`。
//	根因：一段 Go 风格的 `//` 注释被插进了 SQL 字符串字面量里 —— PostgreSQL
//	只认 `--` 与 `/* */` ⇒ 解析器在第一个非引号记号处就炸了。
//	⇒ 仓里必须有��道门扫「SQL raw string 里有没有 `//` 开头的行」。
//
// ★★ 为什么扫描器必须**能看见拼接之后的那一半**（2026-10-29 实测）：
//
//	模态接受判据的两份 SQL 抄本被收成**唯一片段**时，片段是用
//	 `+` 拼接进主 SQL 的（仓内既有的 /*MODALITY_VERDICT_GATE*/ 也是这么
//	 替换的）。而**旧扫描器在拼接点会失明**：
//	 它按「一行只有一个反引号 = 块的开头」＋「块内的第一个反引号 = 块的结尾」
//	 配对 ⇒ 一行里出现**两个**反引号（`... AND (` + frag + ` ` 这种拆法）时，
//	 它在**第一个**反引号处就把块关掉 ⇒ **块之后的 SQL 整段不再被扫** ⇒
//	 那一段里的 `//` 注释对门**完全隐形**。
//	 ⇒ 实测证据见本文件末尾的 RawStringScanFixture（自证夹具）。
//
// ⇒ 所以本实现按**反引号计数**配对：一行里有几个反引号就处理几次
//
//	「开/关」转换 ⇒ 拼接后的那一半照样被扫。
//
// ★ 扫描范围：只扫**跨行**的 raw string。单行 raw string 不在门范围内
//
//	（沿用旧门的口径；SQL 片段都是跨行的）。
type RawStringBlock struct {
	// OpenLine 是这个 raw string 的开引号所在行（1 起）。
	OpenLine int
	// Body 是两个反引号之间的内容。
	Body string
}

// ScanMultiLineRawStrings returns every multi-line backtick raw string in src.
//
// ★ 与旧实现的关键差别：一行里有**多个**反引号时，逐个处理 ⇒ 拼接后的
//
//	那一半不会被丢掉（见文件头）。
func ScanMultiLineRawStrings(src string) []RawStringBlock {
	var blocks []RawStringBlock
	lines := strings.Split(src, "\n")
	inRaw := false
	var body strings.Builder
	openLine := 0

	for i, line := range lines {
		lineno := i + 1
		pos := 0
		for {
			if !inRaw {
				idx := strings.IndexByte(line[pos:], '`')
				if idx < 0 {
					break
				}
				// 一个 raw string 从这个反引号开始。
				inRaw = true
				openLine = lineno
				body.Reset()
				pos += idx + 1
				continue
			}
			idx := strings.IndexByte(line[pos:], '`')
			if idx < 0 {
				// 还没闭合 ⇒ 这一行整行都是内容。
				body.WriteString(line[pos:])
				body.WriteString("\n")
				pos = len(line)
				break
			}
			body.WriteString(line[pos : pos+idx])
			closedHere := strings.Contains(body.String(), "\n")
			if closedHere {
				blocks = append(blocks, RawStringBlock{OpenLine: openLine, Body: body.String()})
			}
			inRaw = false
			body.Reset()
			pos += idx + 1
		}
	}
	// 文件末尾仍未闭合 ⇒ 记下来（Go 源码本身编译不过，但这道门不假设编译通过）。
	if inRaw && strings.Contains(body.String(), "\n") {
		blocks = append(blocks, RawStringBlock{OpenLine: openLine, Body: body.String()})
	}
	return blocks
}

// GoStyleSQLCommentFinding 是一条「raw string 里出现了 `//` 开头行」的发现。
type GoStyleSQLCommentFinding struct {
	// File 是相对包目录的文件名。
	File string
	// Line 是 1 起的行号。
	Line int
	// Text 是那一行的内容（去掉行首空白）。
	Text string
}

// ScanPackageGoStyleSQLComments scans every **non-test** .go file in dir for
// "//"-leading lines inside multi-line raw strings.
//
// ★ 为什么扫「整个包的非测试文件」而不是维护一份文件清单：
//
//	清单会腐化 —— 2026-10-29 模态接受判据从 provider/client.go 搬到
//	provider/modality_accept.go 时，清单若没跟着改，那道门就会**继续守着
//	旧家、对新家完全失明**。⇒ 扫整个包 ⇒ 新文件自动进覆盖范围。
//
// ★ 为什么排除 _test.go：测试夹具里可能整段嵌着 Go 源码（那不是 SQL），
//
//	里面的 `//` 是**合法**的 Go 注释 ⇒ 不该判红。
//
// ★ 判据只认「行首（去空白后）以 // 开头」：那正是 PostgreSQL 会炸的形态
//
//	（它把 `//` 当成记号 ⇒ 解析到该行的第一个非引号 token 就报 42601）。
func ScanPackageGoStyleSQLComments(dir string) ([]GoStyleSQLCommentFinding, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("读目录 %s 失败：%w", dir, err)
	}
	var out []GoStyleSQLCommentFinding
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		for _, blk := range ScanMultiLineRawStrings(string(b)) {
			for offset, line := range strings.Split(blk.Body, "\n") {
				trimmed := strings.TrimLeft(line, " \t")
				if strings.HasPrefix(trimmed, "//") {
					out = append(out, GoStyleSQLCommentFinding{
						File: e.Name(),
						Line: blk.OpenLine + offset,
						Text: strings.TrimSpace(trimmed),
					})
				}
			}
		}
	}
	return out, nil
}

// GoStyleSQLCommentFindings returns every "//" comment line found inside a
// multi-line raw string, with the line number it sits on.
//
// The returned line numbers are absolute (1-based) in the original source.
//
// ★ 行号算术：块的 body 第 0 行**总是**「开引号那一行的剩余部分」（可能是
//
//	空串）⇒ body 第 k 行对应源文件第 (openLine + k) 行。
//	⇒ 拼接形态下（开引号在行中间）这个算式依然成立——实测见
//	provider/sql_comment_syntax_test.go 的 TestSQLCommentScanner_SeesPastConcatenation。
func GoStyleSQLCommentFindings(src string) []string {
	var out []string
	for _, blk := range ScanMultiLineRawStrings(src) {
		for offset, line := range strings.Split(blk.Body, "\n") {
			trimmed := strings.TrimLeft(line, " \t")
			if strings.HasPrefix(trimmed, "//") {
				out = append(out, fmt.Sprintf("%d", blk.OpenLine+offset))
			}
		}
	}
	return out
}
