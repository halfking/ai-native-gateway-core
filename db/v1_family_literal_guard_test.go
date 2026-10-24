package db

// v1_family_literal_guard_test.go —— 2026-10-24「v1 → session_turns 替换轮」的
// **共用工具**：在**字符串字面量**里找「对已退役 v1 关系的写 / DDL」。
//
// # 为什么必须用 go/ast 而不是「文本里找 ALTER TABLE _request_logs」
//
// db.go 里保留着「这里原来有一条 v1 ensure 链」的注释 —— 那是**承重证据**
// （下一个读代码的人要知道这一跳去哪了；AGENTS.md 的纪律：历史注释不许被
// 「清理」掉，因为它们是判据的依据）。
//
// ⇒ 用纯文本判据（`strings.Contains(text, "ALTER TABLE _request_logs")`）会把
//   **注释**当成可执行的东西 ⇒ 门恒红 ⇒ 门被关掉 ⇒ 真缺陷反而没人管。
//   （这正是本轮改写前三道门要面对的同一个坑。）
//
// ⇒ 正确形态：**只取字符串字面量**（go/ast 的 *ast.BasicLit）。
//   注释不在其中 ⇒ 历史证据不受影响；而真 DDL 一定在字符串里 ⇒ 跑不掉。
//
// # 与 admin 包那道总门的关系
//
// admin/v1_family_purge_gate_test.go 是**全仓**总门（扫描所有生产 .go 文件的
// 字符串字面量 + 890 之后的启动迁移）。本文件是 db 包内的**就地**工具：
// 让 db 包自己的三道门（848 / maas / work_type）能用同一把尺子判断
// 「这个函数的**可执行部分**里还有没有 v1 DDL」。

import (
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// v1RelationNames 是已随 890 退役的 v1 关系名（两种拼写 + 四个变体）。
//
// ⚠ canonical 视图 `request_logs_with_current_month` **不在此列** —— 890 之后它
// 仍然存在（内容换成会话族两臂体），把它算成 v1 会让判据恒红。
var v1RelationNames = []string{
	"request_logs", "_request_logs",
	"request_logs_hot", "_request_logs_hot",
	"request_logs_bodies", "_request_logs_bodies",
	"request_logs_bodies_hot", "_request_logs_bodies_hot",
	"request_logs_archive", "_request_logs_archive",
}

// v1WritePatterns 是「对 v1 关系做写 / DDL」的形态。
//
// ⚠ 每一条都要求 v1 关系名**紧跟在动词后面**（或紧跟在 `ON (` 后面），
//
//	这样英文散文里的「update … _request_logs」不会被误判。
var v1WritePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(?:alter\s+table(?:\s+if\s+exists)?|insert\s+into|update|delete\s+from|` +
		`truncate(?:\s+table)?|copy|drop\s+(?:table|index|view|function|materialized\s+view)|references)` +
		`\s+(?:only\s+)?(?:public\.)?(?:` + v1RelationAlternation() + `)\b`),
	regexp.MustCompile(`(?i)\bon\s+(?:public\.)?(?:` + v1RelationAlternation() + `)\s*\(`),
}

// v1RelationAlternation 把关系名拼成正则交替项（长的排前面 ⇒ 先匹配长的）。
func v1RelationAlternation() string {
	alt := make([]string, len(v1RelationNames))
	for i, n := range v1RelationNames {
		alt[i] = regexp.QuoteMeta(n)
	}
	for i := 0; i < len(alt); i++ {
		for j := i + 1; j < len(alt); j++ {
			if len(alt[j]) > len(alt[i]) {
				alt[i], alt[j] = alt[j], alt[i]
			}
		}
	}
	return strings.Join(alt, "|")
}

// v1WriteRefsInFuncLiterals 返回「该函数的字符串字面量里出现的 v1 写/DDL」。
//
// ⚠ 只看**字符串字面量**：Go 注释不在其中 ⇒ 历史注释不受影响；
//
//	真 DDL 一定在字符串里 ⇒ 跑不掉。
func v1WriteRefsInFuncLiterals(t *testing.T, file, funcName string) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}
	var out []string
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != funcName {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			text, err := strconv.Unquote(lit.Value)
			if err != nil {
				text = strings.Trim(lit.Value, "`")
			}
			for _, p := range v1WritePatterns {
				if p.MatchString(text) {
					out = append(out, strings.TrimSpace(text))
					return true
				}
			}
			return true
		})
		return out
	}
	return nil
}
