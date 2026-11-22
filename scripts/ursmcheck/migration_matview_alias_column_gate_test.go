package ursm_test

// migration_matview_alias_column_gate_test.go —— 「物化视图别名子查询必须选全外层引用的列」
// 这道性质门（2026-11-23 211 部署破树轮）
//
// ★★★ **为什么要有这道门（★ 本轮的触发点）**：
//
//	并行车提交 761e641ed「fix(analytics): D2 去重修复——会话宇宙 JOIN sessions 改为
//	DISTINCT ON 子查询」把迁移 994 的三处
//	`JOIN sessions s` 改写成
//	`JOIN (SELECT DISTINCT ON (session_id, tenant_id) <列清单> FROM sessions …) s`。
//	★ 那次改写**只落了 canonical 一份**（★ embeddata 副本仍是旧的 `JOIN sessions s`）
//	⇒ ★ 新装通道与升级通道会跑**不同的字节**。
//	★ 而更致命的是：★ **改写时把 `agent_name` 漏在了 SELECT 清单外** ——
//	★ 外层 `agent_usage_aggregates` 视图用了 `s.agent_name`
//	⇒ ★ PG 报 `column s.agent_name does not exist`
//	⇒ ★ **211 部署在「DB 修订序列」阶段 FATAL**，★ 候选槽未起、★ 未切流。
//	★ ⇒ 全机所有部署被一颗别人写的、**静态可判**的错挡住。
//
// ★★★ **为什么这类错值得单独一道门（而不是靠 review）**：
//
//	① **形态是固定的**：★ 「别名子查询 + 外层引用别名列」是 SQL 里极常见的写法，
//	   ★ 而**子查询的列清单**与**外层的引用**分处两个位置、★ 中间隔着几十行 ——
//	   ★ 人眼 review 几乎必然漏掉（★ 本次就是这样漏的）。
//
//	② **失败形态极差**：★ 不是「视图建不出来」这么简单 —— ★ PG 在**解析期**就报
//	   `column s.xxx does not exist`，★ 而迁移是**非事务流式 psql** 投递
//	   （★ `scripts/apply-db-revision-sequence.sh`）⇒ ★ 前面的语句已经落库、
//	   ★ 后面的没跑 ⇒ ★ 台账与产物**半对半错**，★ 现场只能靠「重跑一遍」收敛。
//
//	③ **它拦的是"别人写的错"**：★ 本仓是多人并行车，★ 静态门是唯一能在
//	   ★ **提交前**把这类错挡在 origin/main 之外的东西。
//
// ★★★ **判据（★ 现算、★ 不写死结论）**：
//
//	对每个 `CREATE MATERIALIZED VIEW … AS <query>` 语句：
//	  ① 找出全部 `JOIN ( <子查询> ) <别名>` 形态的**别名子查询**；
//	  ② 解析子查询的 SELECT 清单（★ 跳过 `DISTINCT ON (…)` 头部、★ 按顶层逗号切列、
//	     ★ 取每列的输出名：★ `col` / `col AS out`）；
//	  ③ 扫描整个语句里 `<别名>.<列>` 形式的**限定引用**（★ 排除别名是别名前缀的情况：
//	     ★ `ss.` 不算 `s.`）；
//	  ④ ★ **每个被引用的列都必须在子查询的 SELECT 清单里** —— 否则报违例。
//
// ★★★ **本门的牙（★ 合成夹具 + 对照臂）**：
//
//	★ 判据写成**纯函数**（★ `matViewAliasJudge`）⇒ ⇒ 合成夹具可以在内存里跑
//	（★ 仓内真文件零改动）⇒ ★ **对照臂**直接把 994 的**破树形态**（★ 漏 `agent_name`）
//	与**修好形态**分别喂进去：★ 前者必须被判红、★ 后者必须判绿。
//	⇒ ★ 这道门**自带变异自证**，★ 不依赖「谁去改仓内文件再跑一遍」。
//
// ★★★ **与兄弟门的分工（★ 两道门都留着、★ 覆盖面不重叠）**：
//
//	★ `domains/analytics/session_universe_column_closure_test.go`
//	  （★ 并行车 2026-11-23 的 `af5a22298` 落的）⇒ ★ 守的是**会话宇宙族**：
//	  ★ 994 的三个 `FROM sessions` 视图块 + ★ **Go 侧** `SessionUniverseJoin/Where`
//	  ★ 的对拍面；★ 它还额外钉住「**不得回退成直接 JOIN sessions**」（★ D2 根因）。
//	★ **本门**守的是**全部物化视图迁移**（★ 判据面 = 10 个文件 / 31 条语句）：
//	  ★ 判据**不限别名**（★ 不只是 `s`）、★ **不限来源表**（★ 不只是 `sessions`）、
//	  ★ **不限迁移号**（★ 未来任何新增 MV 都自动进判据面）。
//	⇒ ★ 两道门是**同族不同面**：★ 一道按「业务族」钉、★ 一道按「SQL 形态」钉。

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const (
	matViewMigRootRel = "../../sql/migrations/startup"

	// ★ 判据面下限：★ 实测含 CREATE MATERIALIZED VIEW 的迁移文件为 10 份。
	//   ★ 这是**下限**（★ 不是从清单抄来的基线 —— 抄基线 = 自证）。
	matViewMinJudgedFiles = 5
)

// matViewAliasJudge —— ★ 判据的**纯函数**核心。
//
// 入参：一个完整的 `CREATE MATERIALIZED VIEW … ;` 语句文本。
// 出参：违例描述列表（★ 空列表 = 判定通过）。
func matViewAliasJudge(stmt string) []string {
	stripped := matViewStripComments(stmt)

	var violations []string

	// ① 找出全部别名子查询：★ `JOIN ( … ) <别名>`。
	for _, sub := range matViewFindAliasSubqueries(stripped) {
		selected := sub.selected
		if sub.star {
			// ★ `SELECT *` ⇒ 列集不可静态判定 ⇒ 放行（★ 但记一条提示）。
			continue
		}
		refs := matViewAliasRefs(stripped, sub.alias, sub.bodyStart, sub.bodyEnd)
		for _, ref := range refs {
			if _, ok := selected[ref]; !ok {
				violations = append(violations, fmt.Sprintf(
					"别名 `%s` 的限定引用 `%s.%s` 不在子查询的 SELECT 清单里（清单=[%s]）"+
						"⇒ PG 解析期报 `column %s.%s does not exist` ⇒ 迁移必 FATAL",
					sub.alias, sub.alias, ref, matViewSortedKeys(selected),
					sub.alias, ref))
			}
		}
	}
	return violations
}

// matViewStripComments —— 去掉 `--` 整行注释与 `/* */` 块注释。
//
// ★ 为什么必须去注释：★ 迁移文件头大量解释「为什么」，★ 而注释里出现的
// ★ `s.agent_name` / `JOIN ( … ) s` 等形态会让判据**被说明文字满足**
// ⇒ ★ 门变成「文档里提到过就算数」（★ 876/879 那道门的同款纪律）。
func matViewStripComments(s string) string {
	var b strings.Builder
	inBlock := false
	for _, line := range strings.Split(s, "\n") {
		trimmed := strings.TrimSpace(line)
		if inBlock {
			if idx := strings.Index(trimmed, "*/"); idx >= 0 {
				inBlock = false
				trimmed = trimmed[idx+2:]
			} else {
				continue
			}
		}
		if idx := strings.Index(trimmed, "/*"); idx >= 0 {
			if end := strings.Index(trimmed[idx:], "*/"); end >= 0 {
				trimmed = trimmed[:idx] + trimmed[idx+end+2:]
			} else {
				inBlock = true
				trimmed = trimmed[:idx]
			}
		}
		if idx := strings.Index(trimmed, "--"); idx >= 0 {
			trimmed = trimmed[:idx]
		}
		b.WriteString(trimmed)
		b.WriteString("\n")
	}
	return b.String()
}

// matViewAliasSubquery —— 一个别名子查询的解析结果。
type matViewAliasSubquery struct {
	alias     string
	selected  map[string]bool
	star      bool
	bodyStart int
	bodyEnd   int
}

// matViewFindAliasSubqueries —— 找出语句里全部 `JOIN ( … ) <别名>` 形态。
func matViewFindAliasSubqueries(s string) []matViewAliasSubquery {
	var out []matViewAliasSubquery
	joinRe := regexp.MustCompile(`(?i)\bJOIN\s*\(`)
	for _, loc := range joinRe.FindAllStringIndex(s, -1) {
		open := loc[1] - 1 // '(' 的下标
		close := matViewMatchParen(s, open)
		if close < 0 {
			continue
		}
		body := s[open+1 : close]
		rest := s[close+1:]
		aliasRe := regexp.MustCompile(`^\s*([a-z_][a-z0-9_]*)`)
		m := aliasRe.FindStringSubmatch(rest)
		if m == nil {
			continue
		}
		alias := m[1]
		selected, star := matViewParseSelectList(body)
		out = append(out, matViewAliasSubquery{
			alias:     alias,
			selected:  selected,
			star:      star,
			bodyStart: open + 1,
			bodyEnd:   close,
		})
	}
	return out
}

// matViewMatchParen —— 从 open（'(' 的下标）起找配对的 ')'，★ 跳过字符串字面量。
func matViewMatchParen(s string, open int) int {
	depth := 0
	inStr := false
	for i := open; i < len(s); i++ {
		c := s[i]
		if inStr {
			if c == '\'' {
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
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// matViewParseSelectList —— 解析子查询的 SELECT 清单。
//
// ★ 处理：`SELECT DISTINCT ON (a, b) c, d FROM t` ⇒ 清单 = [c, d]
// ★       `SELECT c AS out, d FROM t` ⇒ 清单 = [out, d]
func matViewParseSelectList(body string) (map[string]bool, bool) {
	sel := map[string]bool{}
	upper := strings.ToUpper(body)
	idx := strings.Index(upper, "SELECT")
	if idx < 0 {
		return sel, false
	}
	rest := body[idx+len("SELECT"):]
	upperRest := strings.ToUpper(rest)

	// ★ `DISTINCT ON ( … )` 头部：★ 跳过它，★ 但**括号内不是列清单**。
	if strings.HasPrefix(strings.TrimSpace(upperRest), "DISTINCT") {
		onIdx := strings.Index(upperRest, "ON")
		if onIdx < 0 {
			return sel, false
		}
		afterOn := rest[onIdx+2:]
		trimmed := strings.TrimLeft(afterOn, " \t\r\n")
		if !strings.HasPrefix(trimmed, "(") {
			return sel, false
		}
		close := matViewMatchParen(trimmed, 0)
		if close < 0 {
			return sel, false
		}
		rest = trimmed[close+1:]
	}

	// ★ 取 SELECT 清单段：★ 到**顶层**的 FROM 为止。
	depth := 0
	inStr := false
	end := -1
	for i := 0; i < len(rest); i++ {
		c := rest[i]
		if inStr {
			if c == '\'' {
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
		if depth == 0 && i+4 <= len(rest) && strings.EqualFold(rest[i:i+4], "FROM") {
			before := rest[i-1]
			if before == ' ' || before == '\t' || before == '\n' || before == '\r' {
				end = i
				break
			}
		}
	}
	if end < 0 {
		return sel, false
	}
	list := rest[:end]

	star := false
	for _, item := range matViewSplitTopLevel(list) {
		name := matViewOutputName(item)
		if name == "" {
			continue
		}
		if name == "*" {
			star = true
			continue
		}
		sel[name] = true
	}
	return sel, star
}

// matViewSplitTopLevel —— 按**顶层逗号**切列（★ 括号内的逗号不切）。
func matViewSplitTopLevel(s string) []string {
	var out []string
	depth := 0
	inStr := false
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inStr {
			if c == '\'' {
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
		case ',':
			if depth == 0 {
				out = append(out, strings.TrimSpace(s[start:i]))
				start = i + 1
			}
		}
	}
	out = append(out, strings.TrimSpace(s[start:]))
	return out
}

// matViewOutputName —— 取一列的输出名：`col` / `col AS out` / `col out`。
func matViewOutputName(item string) string {
	item = strings.TrimSpace(item)
	if item == "" {
		return ""
	}
	upper := strings.ToUpper(item)
	if idx := strings.Index(upper, " AS "); idx >= 0 {
		return strings.TrimSpace(item[idx+4:])
	}
	// ★ `col alias` 形态（★ 无 AS）：★ 取最后一段。
	if fields := strings.Fields(item); len(fields) > 1 {
		last := fields[len(fields)-1]
		if last != "" && !strings.ContainsAny(last, "(),") {
			return last
		}
	}
	if strings.ContainsAny(item, "(),") {
		return ""
	}
	return item
}

// matViewAliasRefs —— 找出语句里 `<别名>.<列>` 的限定引用（★ 排除子查询体内部）。
//
// ★ `ss.` **不算** `s.`：★ 前一个字符必须是**非字母数字/下划线**。
func matViewAliasRefs(s, alias string, bodyStart, bodyEnd int) []string {
	re := regexp.MustCompile(`(?:^|[^A-Za-z0-9_."])` + regexp.QuoteMeta(alias) + `\.([A-Za-z_][A-Za-z0-9_]*)`)
	var out []string
	seen := map[string]bool{}
	for _, loc := range re.FindAllStringSubmatchIndex(s, -1) {
		start, end := loc[0], loc[1]
		col := s[loc[2]:loc[3]]
		// ★ 子查询体内部的引用不算（★ 那是关联子查询，★ 语义另说）。
		if bodyStart >= 0 && start >= bodyStart && end <= bodyEnd {
			continue
		}
		if seen[col] {
			continue
		}
		seen[col] = true
		out = append(out, col)
	}
	return out
}

// matViewSortedKeys —— 清单的稳定展示（★ 让报错信息可复现）。
func matViewSortedKeys(m map[string]bool) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			if keys[j] < keys[i] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
	return strings.Join(keys, ", ")
}

// matViewStatements —— 抽出一个文件里全部 `CREATE MATERIALIZED VIEW … ;` 语句。
func matViewStatements(content string) []string {
	var out []string
	upper := strings.ToUpper(content)
	idx := 0
	for {
		pos := strings.Index(upper[idx:], "CREATE MATERIALIZED VIEW")
		if pos < 0 {
			break
		}
		start := idx + pos
		// ★ 语句终点：★ 顶层 ';'（★ 括号/引号感知）。
		depth := 0
		inStr := false
		end := -1
		for i := start; i < len(content); i++ {
			c := content[i]
			if inStr {
				if c == '\'' {
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
			case ';':
				if depth == 0 {
					end = i
				}
			}
			if end >= 0 {
				break
			}
		}
		if end < 0 {
			break
		}
		out = append(out, content[start:end+1])
		idx = end + 1
	}
	return out
}

// TestMatViewAliasColumnsAreSelected —— ★ 仓内真文件全量判据。
func TestMatViewAliasColumnsAreSelected(t *testing.T) {
	root := filepath.Join("..", "..", "sql", "migrations", "startup")
	var files []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".sql") {
			return nil
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}

	judgedFiles := 0
	judgedStmts := 0
	for _, path := range files {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		content := string(b)
		if !strings.Contains(strings.ToUpper(content), "CREATE MATERIALIZED VIEW") {
			continue
		}
		stmts := matViewStatements(content)
		if len(stmts) == 0 {
			continue
		}
		judgedFiles++
		for _, stmt := range stmts {
			judgedStmts++
			for _, v := range matViewAliasJudge(stmt) {
				t.Errorf("%s：%s", path, v)
			}
		}
	}

	if judgedFiles < matViewMinJudgedFiles {
		t.Errorf("判据面只覆盖了 %d 个含物化视图的迁移文件（下限 %d）："+
			"门要么退化了、要么仓内形态变了 —— 请复核后再改这个下限",
			judgedFiles, matViewMinJudgedFiles)
	}
	if judgedStmts < matViewMinJudgedFiles {
		t.Errorf("判据面只覆盖了 %d 条物化视图语句（下限 %d）：门退化", judgedStmts, matViewMinJudgedFiles)
	}
	t.Logf("判据面：%d 个迁移文件 / %d 条物化视图语句", judgedFiles, judgedStmts)
}

// ★★ 对照臂：★ 994 的**破树形态**必须被判红。
//
// ★ 这段夹具**逐字取自 761e641ed 引入的形状**（★ 外层用 `s.agent_name`、
// ★ 子查询清单里**没有** `agent_name`）⇒ ★ 若门没有牙，★ 它会判绿。
const matViewBrokenFixture = `
CREATE MATERIALIZED VIEW public.agent_usage_aggregates AS
SELECT
    NULLIF(s.agent_name, '')                       AS agent_name,
    ss.tenant_id                                   AS tenant_id
FROM session_summaries ss
JOIN (
  SELECT DISTINCT ON (session_id, tenant_id)
    session_id, tenant_id, is_probe, owner_user, client_type, title
  FROM sessions
  ORDER BY session_id, tenant_id, partition_date DESC
) s
  ON s.session_id = ss.session_key
GROUP BY NULLIF(s.agent_name, '');
`

// ★★ 对照臂：★ 同一形状**补上** `agent_name` 后必须判绿。
const matViewFixedFixture = `
CREATE MATERIALIZED VIEW public.agent_usage_aggregates AS
SELECT
    NULLIF(s.agent_name, '')                       AS agent_name,
    ss.tenant_id                                   AS tenant_id
FROM session_summaries ss
JOIN (
  SELECT DISTINCT ON (session_id, tenant_id)
    session_id, tenant_id, is_probe, owner_user, client_type, title, agent_name
  FROM sessions
  ORDER BY session_id, tenant_id, partition_date DESC
) s
  ON s.session_id = ss.session_key
GROUP BY NULLIF(s.agent_name, '');
`

func TestMatViewAliasJudgeHasTeeth(t *testing.T) {
	// ① 破树形态：★ 必须判红、★ 且必须点名 `agent_name`。
	broken := matViewAliasJudge(matViewBrokenFixture)
	if len(broken) == 0 {
		t.Fatalf("判据对破树形态判绿了：★ 门没有牙 ⇒ ★ 994 这类漏列还会再次上线")
	}
	found := false
	for _, v := range broken {
		if strings.Contains(v, "agent_name") {
			found = true
		}
	}
	if !found {
		t.Errorf("判据判红了但没点名 agent_name（违例=%v）：★ 报错必须能直接指出漏的是哪一列", broken)
	}

	// ② 修好形态：★ 必须判绿。
	fixed := matViewAliasJudge(matViewFixedFixture)
	if len(fixed) != 0 {
		t.Errorf("判据对修好形态判红（违例=%v）：★ 判据过宽 ⇒ ★ 会变成噪声门", fixed)
	}

	// ③ ★ 别名前缀不得误判：★ `ss.` 不是 `s.`。
	prefixFixture := `
CREATE MATERIALIZED VIEW public.v AS
SELECT ss.tenant_id AS tenant_id
FROM session_summaries ss
JOIN (
  SELECT DISTINCT ON (session_id, tenant_id)
    session_id, tenant_id
  FROM sessions
) s
  ON s.session_id = ss.session_key;
`
	if v := matViewAliasJudge(prefixFixture); len(v) != 0 {
		t.Errorf("判据把 `ss.` 当成了 `s.`（违例=%v）：★ 别名前缀必须区分", v)
	}
}

// TestMatViewJudgeIgnoresComments —— ★ 注释里的形态不得满足判据。
//
// ★ 迁移文件头大量解释「为什么」，★ 而注释里出现的破树形态
// ★ 若被采信 ⇒ ★ 门变成「文档里提到过就算数」（★ 876/879 那道门的同款纪律）。
//
// ★ 本测试用**同一个夹具的两副面孔**做对照：
//
//	① 破树形态写在**注释里** ⇒ ★ 真实语句是修好的 ⇒ ★ 必须判绿（注释被剥掉）；
//	② 同一段形态**取消注释** ⇒ ★ 必须判红（★ 证明判据真的看得见这形态）。
func TestMatViewJudgeIgnoresComments(t *testing.T) {
	// ★ 破树形态的**注释版**（★ 整段都被注释掉）。
	const commentedVariant = `
-- 这段注释描述了一个破树形态（★ 判据不该采信它）：
-- JOIN (
--   SELECT DISTINCT ON (session_id, tenant_id)
--     session_id, tenant_id, is_probe, owner_user, client_type, title
--   FROM sessions
-- ) s
-- 外层引用 s.agent_name
CREATE MATERIALIZED VIEW public.v AS
SELECT s.agent_name AS agent_name
FROM session_summaries ss
JOIN (
  SELECT DISTINCT ON (session_id, tenant_id)
    session_id, tenant_id, is_probe, owner_user, client_type, title, agent_name
  FROM sessions
) s
  ON s.session_id = ss.session_key;
`
	// ★ 破树形态的**活体版**（★ 同一段形态取消注释）。
	const liveVariant = `
CREATE MATERIALIZED VIEW public.v AS
SELECT s.agent_name AS agent_name
FROM session_summaries ss
JOIN (
  SELECT DISTINCT ON (session_id, tenant_id)
    session_id, tenant_id, is_probe, owner_user, client_type, title
  FROM sessions
) s
  ON s.session_id = ss.session_key;
`
	// ★ 修好形态。
	const fixedVariant = `
CREATE MATERIALIZED VIEW public.v AS
SELECT s.agent_name AS agent_name
FROM session_summaries ss
JOIN (
  SELECT DISTINCT ON (session_id, tenant_id)
    session_id, tenant_id, is_probe, owner_user, client_type, title, agent_name
  FROM sessions
) s
  ON s.session_id = ss.session_key;
`

	// ① 破树形态**写在注释里** ⇒ ★ 真实语句是修好的 ⇒ ★ 必须判绿。
	if v := matViewAliasJudge(commentedVariant); len(v) != 0 {
		t.Errorf("判据采信了注释里的形态（违例=%v）：★ 注释必须被剥掉再判", v)
	}

	// ② 同一段形态**取消注释** ⇒ ★ 必须判红（★ 对照臂：证明判据真的看得见它）。
	if v := matViewAliasJudge(liveVariant); len(v) == 0 {
		t.Errorf("判据对同一形态的【取消注释版】判绿了：★ 说明 ① 的绿不是因为「看不出这形态」")
	}

	// ③ 修好形态 ⇒ ★ 必须判绿（★ 判据不能过宽）。
	if v := matViewAliasJudge(fixedVariant); len(v) != 0 {
		t.Errorf("判据对修好形态判红（违例=%v）：★ 判据过宽 ⇒ ★ 会变成噪声门", v)
	}
}
