package db

// session_family_contract_test.go —— 2026-10-24「v1 → session_turns 替换轮」。
//
// # 这道门在守什么
//
// v1 `_request_logs` 族退役（迁移 890）时，boot 路径上还有一条**自愈链**
// （ensureRequestLogSchema：31 列 + 13 索引）在给 v1 族补列补索引。那条链是
// 「迁移链的运行时镜像」——它在 v1 还在时是防「迁移台账有缺口」的保险；v1 退役
// 后它是**死代码**，而且是「每次 boot 对最热表取 ACCESS EXCLUSIVE（连同全部分区）」
// 的最后一处来源 ⇒ 本轮整链删除。
//
// ⇒ 删掉它之后，「v1 那一列谁来保证存在」这个问题**换了主人**：
//
//	旧：boot ensure 链（ALTER … ADD COLUMN）
//	新：**canonical 视图 + 启动迁移链**
//
// 理由是两条硬事实：
//
//	① PostgreSQL 的依赖跟踪：视图引用了 session_turns.origin_stage ⇒
//	   DROP COLUMN session_turns.origin_stage 会被拒（cannot drop column
//	   because other objects depend on it）⇒ 视图存在 ⇒ 它引用的列必然在。
//	② canonical 视图的体由 `canonicalSessionOnlyDDL` 现场生成，引用
//	   `t.<col>` / `d.<col>`；列不在 ⇒ CREATE VIEW 阶段就 42P01/42703 ⇒
//	   boot **响亮失败**（而不是「偷偷补一列」）。
//
// ⇒ 于是「v1 族那一列由谁保证」必须重新被**证明**一次 —— 这就是本门。
//
// # 本门的判据（承重）
//
// canonical 投影引用的每一个会话族列，**必须**由
// 「基线 `sql/schema/01-schema.sql` ∪ `sql/migrations/startup/*.sql`」建出来。
//
// 判据方向是「投影 ⊆ 迁移产物」，不是「迁移产物 ⊆ 投影」：
//
//	· 少一侧 ⇒ 有人给投影加了一列却没写迁移 ⇒ 新装库上视图建不出来 ⇒
//	  boot 中止 ⇒ **红**。
//	· 多一侧 ⇒ 迁移建了投影用不到的列（无害，但那是另一条门的题材）。
//
// # 为什么这不是「给每个列写一遍 ADD COLUMN IF NOT EXISTS」
//
// 那条路（把 ensure 链改写成会话族版本）看着对称，实测是最差的选择：
// `ALTER TABLE … ADD COLUMN IF NOT EXISTS` **不省锁** —— PG 必须先对父表和
// 每一个分区取 ACCESS EXCLUSIVE 才能判断列在不在，一个已经存在几个月的列
// 照样要付一次全量独占扫描（生产实测：420 次调用、均值 2,289 ms、最长单次
// 104,322 ms，对象是库里最忙的表）。把它搬到 session_turns（更热、分区更多）
// 就是**把一个已知事故搬到一个更大的对象上**。
//
// ⇒ 正确形态是「迁移链建列 + CI 门证明投影 ⊆ 迁移产物」：
// 运行时零 DDL、零锁；漂移在 CI 里就红。

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// repoRootForTest walks up from this file to the directory holding go.mod.
//
// ⚠ 自带一份而不是复用 admin 包的同名 helper：跨包复用需要把 helper 挪到
// 一个非 _test 的包（那会让测试专用代码进生产二进制）⇒ 复制一份更便宜。
func repoRootForTest(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(thisFile)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod above %s", filepath.Dir(thisFile))
		}
		dir = parent
	}
}

// ─────────────────────────── 取列集合 ───────────────────────────

// sqlColumnRe 抽出「CREATE TABLE <tb> ( … )」体里的列名。
var sqlColumnRe = regexp.MustCompile(`(?is)CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?(?:public\.)?([a-z_][a-z0-9_]*)\s*\(`)

// addColumnRe 抽出「ALTER TABLE <tb> … ADD COLUMN [IF NOT EXISTS] <col>」。
var addColumnRe = regexp.MustCompile(`(?is)ALTER\s+TABLE\s+(?:IF\s+EXISTS\s+)?(?:ONLY\s+)?(?:public\.)?([a-z_][a-z0-9_]*)\b(.*?);`)

// addColumnNameRe 从 ALTER TABLE 的体里取 ADD COLUMN 的列名。
var addColumnNameRe = regexp.MustCompile(`(?is)ADD\s+COLUMN\s+(?:IF\s+NOT\s+EXISTS\s+)?"?([a-z_][a-z0-9_]*)"?`)

// splitTopLevelLines 把 DDL 体按「顶层逗号/换行」切成条目。
// 目的：跳过 CONSTRAINT / PRIMARY KEY / UNIQUE / CHECK / FOREIGN KEY / LIKE
// 这些不是列的条目。
func splitTopLevelLines(body string) []string {
	var out []string
	depth := 0
	cur := strings.Builder{}
	inSingle := false
	for _, r := range body {
		switch {
		case r == '\'':
			inSingle = !inSingle
			cur.WriteRune(r)
		case inSingle:
			cur.WriteRune(r)
		case r == '(':
			depth++
			cur.WriteRune(r)
		case r == ')':
			depth--
			cur.WriteRune(r)
		case (r == ',' || r == '\n') && depth == 0:
			if s := strings.TrimSpace(cur.String()); s != "" {
				out = append(out, s)
			}
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	if s := strings.TrimSpace(cur.String()); s != "" {
		out = append(out, s)
	}
	return out
}

// collectDDLColumns 返回「该 DDL 文本为 table 建出来的列名集合」。
func collectDDLColumns(text, table string) map[string]bool {
	cols := map[string]bool{}
	// CREATE TABLE
	for _, m := range sqlColumnRe.FindAllStringSubmatchIndex(text, -1) {
		if m[2] == -1 {
			continue
		}
		if !strings.EqualFold(text[m[2]:m[3]], table) {
			continue
		}
		i := m[1] - 1 // 指向 '('
		depth := 0
		j := i
		for j < len(text) {
			if text[j] == '(' {
				depth++
			} else if text[j] == ')' {
				depth--
				if depth == 0 {
					break
				}
			}
			j++
		}
		if j >= len(text) {
			continue
		}
		for _, entry := range splitTopLevelLines(text[i+1 : j]) {
			up := strings.ToUpper(entry)
			if strings.HasPrefix(up, "CONSTRAINT") || strings.HasPrefix(up, "PRIMARY") ||
				strings.HasPrefix(up, "UNIQUE") || strings.HasPrefix(up, "CHECK") ||
				strings.HasPrefix(up, "FOREIGN") || strings.HasPrefix(up, "EXCLUDE") ||
				strings.HasPrefix(up, "LIKE") {
				continue
			}
			fields := strings.Fields(entry)
			if len(fields) == 0 {
				continue
			}
			cols[strings.Trim(fields[0], `"`)] = true
		}
	}
	// ALTER TABLE … ADD COLUMN
	for _, m := range addColumnRe.FindAllStringSubmatchIndex(text, -1) {
		if m[2] == -1 {
			continue
		}
		if !strings.EqualFold(text[m[2]:m[3]], table) {
			continue
		}
		for _, cm := range addColumnNameRe.FindAllStringSubmatch(text[m[4]:m[5]], -1) {
			cols[cm[1]] = true
		}
	}
	return cols
}

// migrationCreatedColumns 汇总「基线 ∪ 启动迁移链」为 table 建出来的列。
func migrationCreatedColumns(t *testing.T, repoRoot, table string) map[string]bool {
	t.Helper()
	cols := map[string]bool{}

	baseline := filepath.Join(repoRoot, "sql", "schema", "01-schema.sql")
	src, err := os.ReadFile(baseline)
	if err != nil {
		t.Fatalf("read baseline: %v", err)
	}
	for c := range collectDDLColumns(string(src), table) {
		cols[c] = true
	}

	dir := filepath.Join(repoRoot, "sql", "migrations", "startup")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read startup migrations: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		for c := range collectDDLColumns(string(b), table) {
			cols[c] = true
		}
	}
	return cols
}

// ───────────────────── 投影引用的会话族列 ─────────────────────

// turnAliasRe 抽出投影表达式里对 session_turns（别名 t）的列引用。
var turnAliasRe = regexp.MustCompile(`\bt\.([a-z_][a-z0-9_]*)`)

// projectedTurnColumns 返回 canonical 投影从 session_turns 引用的列名集合。
//
// ⚠ 口径说明：投影分两段 —— `projectionExprsV2` 给出 `t.<col>` 直映/派生列，
//
//	`detailsProjectionColumns` 给出 details 层的 `d.<col>` 列
//	（见 buildSessionProjectionExprs）。两段都要覆盖。
func projectedTurnColumns(t *testing.T) map[string]bool {
	t.Helper()
	cols := map[string]bool{}
	for _, expr := range projectionExprsV2 {
		for _, m := range turnAliasRe.FindAllStringSubmatch(expr, -1) {
			cols[m[1]] = true
		}
	}
	return cols
}

// projectedDetailsColumns 返回 canonical 投影从 session_turn_details 引用的列名集合。
func projectedDetailsColumns(t *testing.T) map[string]bool {
	t.Helper()
	cols := map[string]bool{}
	for name := range detailsProjectionColumns {
		cols[name] = true
	}
	return cols
}

// ──────────────────────────── 判据 ────────────────────────────

// TestSessionFamilyProjectionColumnsAreMigrationCreated 是 v1 ensure 链删掉之后
// 接管「列由谁保证」的那道门。
//
// 承重判据：canonical 视图引用的每一个会话族列，都必须由
// 「基线 ∪ 启动迁移链」建出来 —— 少一个 ⇒ 红。
func TestSessionFamilyProjectionColumnsAreMigrationCreated(t *testing.T) {
	root := repoRootForTest(t)

	turnCols := projectedTurnColumns(t)
	if len(turnCols) == 0 {
		t.Fatal("投影里一个 t.* 列都没有 —— 判据的输入面没了（投影被改写或别名变了）")
	}
	detailCols := projectedDetailsColumns(t)
	if len(detailCols) == 0 {
		t.Fatal("detailsProjectionColumns 是空的 —— 判据的输入面没了")
	}

	createdTurns := migrationCreatedColumns(t, root, "session_turns")
	createdDetails := migrationCreatedColumns(t, root, "session_turn_details")

	var missingTurns, missingDetails []string
	for c := range turnCols {
		if !createdTurns[c] {
			missingTurns = append(missingTurns, c)
		}
	}
	for c := range detailCols {
		if !createdDetails[c] {
			missingDetails = append(missingDetails, c)
		}
	}
	sort.Strings(missingTurns)
	sort.Strings(missingDetails)

	if len(missingTurns) > 0 {
		t.Errorf("canonical 视图引用了 %d 个 session_turns 列，但它们**不是**由基线/启动迁移建出来的：\n  %s\n"+
			"⇒ 新装库上 canonical 视图会 42703 ⇒ boot 中止。\n"+
			"⇒ 修法：给这些列补一条启动迁移（并登记进 installer 的 StartupFiles / embeddata 孪生副本），"+
			"而不是把 v1 那条 ensure 链改写成会话族版本（那会把一次已知锁事故搬到一个更大的对象上）。",
			len(missingTurns), strings.Join(missingTurns, "\n  "))
	}
	if len(missingDetails) > 0 {
		t.Errorf("canonical 视图引用了 %d 个 session_turn_details 列，但它们**不是**由基线/启动迁移建出来的：\n  %s\n"+
			"⇒ 新装库上 canonical 视图会 42703 ⇒ boot 中止。",
			len(missingDetails), strings.Join(missingDetails, "\n  "))
	}
}

// TestSessionFamilyContractScannerHasDiscriminatingPower 是**阳性对照**：
// 一道断言「缺失集为空」的判据，必须证明它的扫描器**看得见**缺失。
//
// ⚠ 没有这一条，上一条可能因为「解析器什么都没匹配到」而恒绿 ——
// 那是最坏的失败模式：门是绿的，而新装库照样起不来。
func TestSessionFamilyContractScannerHasDiscriminatingPower(t *testing.T) {
	root := repoRootForTest(t)

	t.Run("扫描器必须看得见一个不存在的列", func(t *testing.T) {
		created := migrationCreatedColumns(t, root, "session_turns")
		if created["definitely_not_a_real_column_zzz"] {
			t.Fatal("扫描器把一个不存在的列算成存在 —— 判据失效")
		}
	})

	t.Run("扫描器必须看得见一个真实存在的列", func(t *testing.T) {
		created := migrationCreatedColumns(t, root, "session_turns")
		// ts 是基线里的列（CREATE TABLE session_turns 的第一分区之一）。
		if !created["ts"] {
			t.Error("扫描器没认出基线里的 session_turns.ts —— 判据的输入面没被读到（恒真空）")
		}
		// request_status 是迁移链补的列（不是基线里的）⇒ 证明 ALTER 分支也活着。
		if !created["request_status"] {
			t.Error("扫描器没认出迁移链补的 session_turns.request_status —— ALTER 分支没被读到（恒真空）")
		}
	})

	t.Run("投影的两段都必须非空", func(t *testing.T) {
		turn := projectedTurnColumns(t)
		detail := projectedDetailsColumns(t)
		if len(turn) < 50 {
			t.Errorf("t.* 列只有 %d 个 —— 投影解析面不对", len(turn))
		}
		if len(detail) < 20 {
			t.Errorf("d.* 列只有 %d 个 —— details 解析面不对", len(detail))
		}
	})
}
