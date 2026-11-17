// projection_columns_exist_test.go —— **投影列必须在真库存在**的门。
//
// ── 为什么要有这道门 ──────────────────────────────────────────────────
//
// 2026-11-16 12h 修订审计轮 P1（本门要防的正是它）：
//
//	5a73df106（2026-11-15 P2 审计修复）在 detector.go 的
//	detectorPolicySelectCols 投影里用了三个新列：
//
//	    COALESCE(llm_detection_fail_closed, false),
//	    COALESCE(llm_detection_sample_rate, 1.0),
//	    COALESCE(llm_detection_timeout_sec, 30)
//
//	★ 但这三个列名**在仓内只出现在这一个 .go 文件里**——全仓 *.sql 零命中：
//	  既不在 sql/objects/tables/prompt_injection_policies.sql 的表定义里，
//	  也不在任何 startup 迁移里。
//	⇒ 读侧 SQL 必然报 42703 column does not exist
//	  → getPolicy 报错 → Detect 报错
//	    → domains/hooks/promptinjection/hook.go 记一行 metadata 后 return nil
//	      （verdict=allow）
//	        → **每一次请求都放行，提示词注入检测域整体静默失效**。
//
// ★ 提交说明里写的「读侧 COALESCE 已防 NULL」是**误判**：COALESCE 挡的是
//   「列存在但值为 NULL」，挡不住「列根本不存在」。而"列根本不存在"恰恰
//   是本次缺陷的形态。
//
// ★ 为什么已有的 null_hardening_test.go 没抓住：那道门只做「投影串里含
//   COALESCE(...)」与「投影列数 == Scan 实参数」的**同源自证**——
//   两者都只看 .go 文件，**从不解析 sql/objects/tables** ⇒
//   「投影引用了库里没有的列」对它是不可见的。
//
// ── 本门的判据 ────────────────────────────────────────────────────────
//
// 对每张被投影的表：
//
//	已知列集合 = 表定义（sql/objects/tables/<table>.sql 的 CREATE TABLE）
//	           ∪ startup 迁移里对该表 ADD COLUMN 的列
//	                （sql/migrations/startup/*.sql）
//
//	★ 投影里引用的**每一个**列都必须落在已知列集合里。
//	  少一个 ⇒ 报错并指出「哪个投影列在真库不存在」。
//
// ★ 为什么迁移也算：加列只能走迁移通道（baseline dump 是活库 pg_dump 的
//   产物，手改会被下一次 dump 覆盖——scripts/checks/sync-baseline-copies.sh
//   头注明令「不要手改这三份 dump 里的任何一行」）⇒
//   「列在迁移里」与「列在表定义里」是同等有效的存在性证明。
//
// ★ 降级读投影（detectorPolicySelectColsLegacy）也要过同一道门——
//   它是迁移落地前的兜底读面，用的列必须同样真实存在。
//
// ★ 本门是**静态**门（不连库）：它证明的是「仓内的 SQL 声明面覆盖了
//   投影」，不是「某个具体环境的库已迁移」。后者由部署侧的迁移回填负责。

package promptinjection

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const (
	// repoRootFromPkg 是仓根相对本包（domains/promptinjection/）的相对路径。
	repoRootFromPkg = "../.."
)

// schemaTables 是本门覆盖的表 → 它们的投影串常量。
//
// ★ 键必须与 null_hardening_test.go 的 projections 保持一致（同一批表）。
var schemaTables = map[string]string{
	"prompt_injection_rules":    detectorRuleSelectCols,
	"prompt_injection_policies": detectorPolicySelectCols,
	"severity_action_matrix":    detectorSeverityActionSelectCols,
	// ★ 2026-11-17 审计轮 P0（F-PI-01）：判官引擎配置表**必须**进这道门。
	//
	//	原实现的引擎查询读的是 model_name / api_key / base_url /
	//	confidence_threshold —— 四个列在正典表里**都不存在**
	//	⇒ 每次调用必报 42703 ⇒ 判官层从不产出结论，
	//	而当时这道门只覆盖三张表 ⇒ 本表**在门的盲区里**。
	//	⇒ 本表进 coverage 之后，任何「写回库里没有的列」都会当场红。
	"prompt_injection_llm_engines": detectorLLMEngineSelectCols,
}

// legacyProjections 是降级读投影（迁移落地前的兜底读面）。
var legacyProjections = map[string]string{
	"prompt_injection_policies": detectorPolicySelectColsLegacy,
}

// TestProjectedColumnsExistInSchema 判据：投影里的每个列都必须真实存在。
func TestProjectedColumnsExistInSchema(t *testing.T) {
	for table, proj := range schemaTables {
		known := knownColumns(t, table)
		for _, col := range projectionColumns(t, proj) {
			if !known[col] {
				t.Errorf("表 %s：投影引用了列 %q，但真库里没有这一列 ⇒ "+
					"查询必然报 42703 column does not exist ⇒ 读侧整条路径失败。\n"+
					"★ 本门防的正是 2026-11-16 12h 审计轮 P1（llm_detection_* 三列"+
					"只存在于 .go 文件、没有任何迁移 ⇒ 注入检测域整体静默全放行）。\n"+
					"修法：给该表补一条 startup 迁移（ADD COLUMN IF NOT EXISTS），"+
					"并在 installer 的 embeddata + StartupFiles + tsv 四处登记。\n"+
					"已知列集合里没有 %q。", table, col, col)
			}
		}
	}
}

// TestLegacyProjectionColumnsExistInSchema 判据：降级读投影的列同样必须存在。
func TestLegacyProjectionColumnsExistInSchema(t *testing.T) {
	for table, proj := range legacyProjections {
		known := knownColumns(t, table)
		for _, col := range projectionColumns(t, proj) {
			if !known[col] {
				t.Errorf("表 %s：降级读投影（detectorPolicySelectColsLegacy）引用了"+
					"不存在的列 %q ⇒ 迁移落地前的兜底读面自己也会失败 ⇒ "+
					"降级路径形同虚设。", table, col)
			}
		}
	}
}

// TestLegacyProjectionIsMainProjectionMinusLLMColumns 钉住降级读与主投影的
// 形状关系：降级读 = 主投影**恰好去掉**三个 LLM 判官控制列。
//
// ★ 为什么要有：降级读是「列还没迁移时」的兜底 ⇒ 它必须与主投影逐列对齐，
//
//	否则加/减列时两边会漂移，而漂移的后果是 Scan 实参与投影列数不等
//	（运行时 "number of field descriptions must equal number of destinations"）。
func TestLegacyProjectionIsMainProjectionMinusLLMColumns(t *testing.T) {
	main := projectionColumns(t, detectorPolicySelectCols)
	legacy := projectionColumns(t, detectorPolicySelectColsLegacy)

	mainSet := map[string]bool{}
	for _, c := range main {
		mainSet[c] = true
	}
	legacySet := map[string]bool{}
	for _, c := range legacy {
		legacySet[c] = true
	}

	wantDropped := []string{
		"llm_detection_fail_closed",
		"llm_detection_sample_rate",
		"llm_detection_timeout_sec",
	}
	for _, c := range wantDropped {
		if !mainSet[c] {
			t.Errorf("主投影里没有列 %q ⇒ 「降级读 = 主投影去掉三列」的前提已变，"+
				"请同步改这条门与降级读常量。", c)
		}
		if legacySet[c] {
			t.Errorf("降级读投影里仍有列 %q ⇒ 降级读在列还没迁移的库上一样会报 42703。", c)
		}
		delete(mainSet, c)
	}
	for c := range mainSet {
		if !legacySet[c] {
			t.Errorf("列 %q 在主投影里但不在降级读投影里 ⇒ 两边形状漂移 "+
				"（多/少一列都会让 Scan 实参与投影列数不等）。", c)
		}
	}
	for c := range legacySet {
		if !mainSet[c] {
			t.Errorf("列 %q 在降级读投影里但不在主投影里 ⇒ 两边形状漂移。", c)
		}
	}
}

// knownColumns 收集一张表的已知列集合：
// 表定义（sql/objects/tables/<table>.sql）∪ startup 迁移的 ADD COLUMN。
func knownColumns(t *testing.T, table string) map[string]bool {
	t.Helper()
	cols := map[string]bool{}

	// ① 表定义
	ddlPath := filepath.Join(repoRootFromPkg, "sql", "objects", "tables", table+".sql")
	b, err := os.ReadFile(ddlPath)
	if err != nil {
		t.Fatalf("读取表定义失败 %s：%v", ddlPath, err)
	}
	for _, c := range ddlColumnsFromCreateTable(string(b)) {
		cols[c] = true
	}

	// ② startup 迁移里的 ADD COLUMN
	startupDir := filepath.Join(repoRootFromPkg, "sql", "migrations", "startup")
	entries, err := os.ReadDir(startupDir)
	if err != nil {
		t.Fatalf("读取 startup 迁移目录失败 %s：%v", startupDir, err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		mb, err := os.ReadFile(filepath.Join(startupDir, e.Name()))
		if err != nil {
			t.Fatalf("读取迁移失败 %s：%v", e.Name(), err)
		}
		for _, c := range addedColumns(string(mb), table) {
			cols[c] = true
		}
	}
	return cols
}

// ddlColumnsFromCreateTable 从 CREATE TABLE 语句体里提取列名。
func ddlColumnsFromCreateTable(sqlText string) []string {
	lower := strings.ToLower(sqlText)
	open := strings.Index(lower, "create table")
	if open < 0 {
		return nil
	}
	body := sqlText[open:]
	lp := strings.Index(body, "(")
	if lp < 0 {
		return nil
	}
	body = body[lp+1:]
	// 截到匹配的右括号（表定义里没有嵌套括号，但 CHECK 约束里有 ⇒ 需计数）。
	depth := 1
	end := len(body)
	for i, r := range body {
		if r == '(' {
			depth++
		} else if r == ')' {
			depth--
			if depth == 0 {
				end = i
				break
			}
		}
	}
	body = body[:end]

	var out []string
	for _, part := range splitTopLevel(body) {
		p := strings.TrimSpace(part)
		if p == "" {
			continue
		}
		upper := strings.ToUpper(p)
		// 表级约束不是列。
		for _, kw := range []string{"CONSTRAINT", "PRIMARY KEY", "UNIQUE", "CHECK", "FOREIGN KEY", "EXCLUDE", "LIKE"} {
			if strings.HasPrefix(upper, kw) {
				p = ""
				break
			}
		}
		if p == "" {
			continue
		}
		name := firstIdentifier(p)
		if name != "" {
			out = append(out, name)
		}
	}
	return out
}

// addedColumns 从一个迁移文件里提取「对 table 做 ADD COLUMN」的列名。
func addedColumns(sqlText, table string) []string {
	// ALTER TABLE [ONLY] public.<table> ADD COLUMN [IF NOT EXISTS] <col>
	re := regexp.MustCompile(`(?i)ALTER\s+TABLE\s+(?:ONLY\s+)?(?:public\.)?"?` + regexp.QuoteMeta(table) + `"?\s+ADD\s+COLUMN\s+(?:IF\s+NOT\s+EXISTS\s+)?"?([a-zA-Z_][a-zA-Z0-9_]*)`)
	var out []string
	for _, m := range re.FindAllStringSubmatch(sqlText, -1) {
		out = append(out, m[1])
	}
	return out
}

// projectionColumns 从投影串里提取它引用的列名。
//
// 形态：`col` 或 `COALESCE(col, default)`（可带 `::text` 转型）。
func projectionColumns(t *testing.T, proj string) []string {
	t.Helper()
	var out []string
	for _, part := range splitTopLevel(proj) {
		p := strings.TrimSpace(part)
		if p == "" {
			continue
		}
		if strings.HasPrefix(strings.ToUpper(p), "COALESCE(") {
			inner := p[len("COALESCE("):]
			if idx := strings.Index(inner, ","); idx >= 0 {
				inner = inner[:idx]
			}
			inner = strings.TrimSpace(inner)
			if idx := strings.Index(inner, "::"); idx >= 0 {
				inner = inner[:idx]
			}
			p = strings.TrimSpace(inner)
		} else {
			if idx := strings.Index(p, "::"); idx >= 0 {
				p = p[:idx]
			}
			p = strings.TrimSpace(p)
		}
		p = firstIdentifier(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// splitTopLevel 按**顶层**逗号切分（忽略括号内的逗号）。
func splitTopLevel(s string) []string {
	var out []string
	depth := 0
	start := 0
	for i, r := range s {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	out = append(out, s[start:])
	return out
}

// firstIdentifier 取一段里的第一个标识符。
func firstIdentifier(s string) string {
	s = strings.TrimSpace(s)
	for i, r := range s {
		if r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			continue
		}
		if i == 0 {
			return ""
		}
		return s[:i]
	}
	return s
}
