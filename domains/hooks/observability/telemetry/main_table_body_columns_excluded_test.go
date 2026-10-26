package telemetry

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRequestLogMainTableExcludesBodyColumns pins the migration-573 contract:
// the main write target must never reference the three body columns
// (request_body / response_body / outbound_body) in its INSERT or UPDATE
// statements. Bodies live exclusively in the side table (formerly
// _request_logs_bodies_hot via upsertRequestLogBodies; since migration 890 the
// session family session_bodies / session_bodies_hot).
//
// 2026-08-26 incident (SQLSTATE 42703, 154/245): a client.go build lagged
// behind migration 573 (which dropped the three body columns) and every
// telemetry write died with `column "request_body" of relation
// "_request_logs_hot" does not exist` — _request_logs_hot went effectively
// empty while the fallback path silently absorbed the data. This offline
// guard fails the moment anyone reintroduces those columns into the main-table
// SQL, whether by code regression or by a migration that re-adds the columns
// and tempts a future "quick fix".
//
// ★ 2026-10-20（迁移 890，v1 族退役）：本门**改指**到新写入目标
// （usage_ledger_hot）——v1 宽表随 890 删除，旧锚点已不存在。
// 契约本身不变：主表不碰正文列。
//
// It reads client.go source only — no network, no database.
func TestRequestLogMainTableExcludesBodyColumns(t *testing.T) {
	src, err := os.ReadFile("client.go")
	if err != nil {
		t.Fatalf("read client.go: %v", err)
	}
	srcStr := string(src)
	forbidden := []string{"request_body", "response_body", "outbound_body"}

	stmts := map[string]struct {
		signature string
		sqlAnchor string
	}{
		"insertRequestLog": {"func (c *Client) insertRequestLog", "INSERT INTO usage_ledger_hot ("},
		"updateRequestLog": {"func (c *Client) updateRequestLog", "UPDATE usage_ledger_hot"},
	}

	for name, st := range stmts {
		fn := extractFunc(srcStr, st.signature)
		if fn == "" {
			t.Fatalf("%s not found in client.go", st.signature)
		}
		sqlStart := strings.Index(fn, st.sqlAnchor)
		if sqlStart < 0 {
			t.Fatalf("%s: SQL anchor %q not found", name, st.sqlAnchor)
		}
		sqlEnd := strings.Index(fn[sqlStart:], "`") + sqlStart
		if sqlEnd < sqlStart {
			t.Fatalf("%s: unterminated SQL literal", name)
		}
		// Strip SQL comments so mentions inside -- comments don't mask a real
		// regression (and comments documenting the side-table move don't trip it).
		sql := regexp.MustCompile(`--[^\n]*`).ReplaceAllString(fn[sqlStart:sqlEnd], "")

		for _, col := range forbidden {
			if regexp.MustCompile(`\b` + col + `\b`).MatchString(sql) {
				t.Errorf("%s: main-table SQL references forbidden body column %q — bodies belong in the session family (migration 573 / 42703 regression)", name, col)
			}
		}

		// For the INSERT, also assert the explicit column list so the failure
		// message names the offending position, not just the identifier.
		if name == "insertRequestLog" {
			cols := parseIdentList(sql, sqlIndex(sql, "(", 0)+1, sqlIndex(sql, ") VALUES", 0))
			if len(cols) == 0 {
				t.Fatalf("insertRequestLog: column list parse failed")
			}
			for i, col := range cols {
				for _, bad := range forbidden {
					if col == bad {
						t.Errorf("insertRequestLog: column #%d is %q (body columns must not be in the main write target)", i+1, bad)
					}
				}
			}
		}
	}
}

// v1FamilyWritePattern 匹配「SQL 语句里出现 v1 族表名」的形态。
//
// ⚠ 刻意**不**用裸子串 "_request_logs"：本包的 Prometheus 指标 Help 文本里
// 就有 "_request_logs rows" 这种散文式提及（见 empty_response_metrics.go），
// 裸子串会把**说明文字**误判成**写语句** ⇒ 门会假红、且会被「改文案」而不是
// 「改代码」绕过去。
//
// 只认「表名紧跟在 INTO / FROM / UPDATE / TABLE / JOIN 之后」的形态——
// 这才是 SQL 里引用表的方式。
//
// ⚠ 表名**不带词尾边界**：_request_logs 是 _request_logs_hot /
// _request_logs_bodies_hot / _request_logs_archive 的前缀，而下划线是单词
// 字符 ⇒ `\b` 会让前缀匹配失效（实测：\b 版本抓不到 "UPDATE _request_logs_hot"）。
var v1FamilyWritePattern = regexp.MustCompile(`(?i)\b(?:INTO|FROM|UPDATE|TABLE|JOIN)\s+_request_logs`)

// TestNoV1RequestLogFamilyWriteRemains —— 迁移 890 落库后的**源码级**退役门：
// 本包**任何**生产源文件都不得再有指向 v1 族表（_request_logs /
// _request_logs_hot / _request_logs_bodies* / _request_logs_archive）的
// SQL 语句。
//
// 为什么需要它（而不能只靠各条判据自己看住）：
//
//	① v1 族是 42P01 的来源——只要有一处漏网的写语句，生产上就是「整条写入
//	   事务回滚」，而不是「少写一张表」。
//	② 退役是**跨文件**的：client.go 里删干净了，别的文件（新加的 writer、
//	   补写的迁移脚本）仍可能把 v1 写语句接回来 ⇒ 只钉 client.go 的门看不见。
//	③ 判据只看**字符串字面量**（go/ast 的 BasicLit），注释里的历史叙述
//	   （本包大量注释仍在讲 v1 族的历史）不会误触发 ⇒ 门可以长期存活。
//
// 正向对照（方法纪律第 2 条）：本门断言的是「没有」，必须先证明**扫描器本身
// 抓得到东西**——否则「扫描器什么都没扫到」与「真的没有 v1 写」长得一模一样。
// 对照物：本包里真实存在的 usage_ledger_hot 写入（同一套扫描规则必须命中它）。
func TestNoV1RequestLogFamilyWriteRemains(t *testing.T) {
	// ── 正向对照：同一套扫描规则必须能抓到真实的写入语句 ──
	control := "INSERT INTO usage_ledger_hot (request_id, ts) VALUES ($1, now())"
	require.False(t, v1FamilyWritePattern.MatchString(control),
		"usage_ledger_hot 不是 v1 族表（这条断言是扫描规则的语义自检）")
	require.True(t, regexp.MustCompile(`(?i)\b(?:INTO|FROM|UPDATE|TABLE|JOIN)\s+usage_ledger_hot\b`).MatchString(control),
		"正向对照：扫描规则必须能命中真实的写语句（否则下面的『没有』是假绿）")
	require.True(t, v1FamilyWritePattern.MatchString("UPDATE _request_logs_hot SET x = 1"),
		"正向对照：扫描规则必须能命中 v1 族的写语句形态")
	require.False(t, v1FamilyWritePattern.MatchString("Successful _request_logs rows whose body is empty"),
		"散文式提及（指标 Help 文本）不得被判成写语句")

	// ── 真实扫描：本包所有生产源文件 ──
	fset := token.NewFileSet()
	productionFiles := 0
	for _, name := range telemetrySourceFiles(t) {
		productionFiles++
		file, err := parser.ParseFile(fset, name, nil, 0)
		require.NoError(t, err, "parse %s", name)
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			if v1FamilyWritePattern.MatchString(lit.Value) {
				t.Errorf("%s:%d: 生产代码里仍有指向 v1 族表的 SQL 语句 —— v1 族随迁移 890 删除，"+
					"写它必然 42P01（正文与宽表落点已迁到会话族 / usage_ledger_hot）",
					name, fset.Position(lit.Pos()).Line)
			}
			return true
		})
	}
	require.NotZero(t, productionFiles, "没有扫描到任何生产源文件 ⇒ 下面的『没有』是假绿")
}
