package persist

// writer 的 ON CONFLICT 推断列必须被权威基线里的唯一约束覆盖（2026-10-06，审计 §10.59）
//
// 这道门存在的理由是一次真实的生产事故：
//
// 已部署 schema：  PRIMARY KEY (snapshot_ts, credential_id, raw_model_name)   ← 3 列
// writer 的 SQL：  ON CONFLICT (snapshot_ts, tenant_id, credential_id, raw_model_name)  ← 4 列
// ⇒ PG 找不到匹配的推断索引，每次插入抛
//   ERROR: there is no unique or exclusion constraint matching the
//          ON CONFLICT specification (SQLSTATE 42P10)
// ⇒ URSM v2 快照持久化**成功率 0**，表 0 行，而日志只记 WARN，不告警。
//   实测：2026-10-03 23:32 起持续，701 条，跨两个进程代次。
//
// 为什么仓库里的门没抓到：
//   - 453（建表，3 列 PK）、463（改 4 列 PK）、818、830 各自都有测试；
//   - 但**没有任何一条判据核对「writer 的 ON CONFLICT 列」与
//     「权威基线 01-schema.sql 里的唯一约束」是否一致**。
//   基线是对的（4 列），writer 是对的（4 列），**是生产 schema 漂移在 453 形态**；
//   于是两边各自都对、中间那个漂移没人管。
//
// 这道门钉的正是那条缺失的连接：只要 ON CONFLICT 列与基线唯一约束不一致，红。

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// 权威基线：全新安装实际执行的那份 DDL。
const baselineSchema = "../../../../sql/schema/01-schema.sql"

var (
	reONConflict = regexp.MustCompile(`(?is)ON\s+CONFLICT\s*\(([^)]*)\)`)
	// 形如 PRIMARY KEY (a, b, c) 或 UNIQUE (a, b) 或 ADD CONSTRAINT x UNIQUE (a, b)
	reUniqueCols = regexp.MustCompile(`(?is)(PRIMARY\s+KEY|UNIQUE)\s*\(([^)]*)\)`)
	// CREATE TABLE [IF NOT EXISTS] <schema>.<name> ( ... 直到下一个 ')'
	reCreateTable = regexp.MustCompile(`(?is)CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?([a-z0-9_."]+)\s*\(`)
)

func splitCols(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		// 去掉可能存在的排序/修饰与引号
		p = strings.Trim(p, `"`)
		if i := strings.Index(p, " "); i > 0 {
			p = p[:i]
		}
		p = strings.TrimSuffix(p, ",")
		if p != "" {
			out = append(out, strings.ToLower(p))
		}
	}
	return out
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// baselineUniqueColumnSets 返回基线里 <表名> 的全部唯一约束列集合。
// 只解析该表自己的 CREATE TABLE 段，避免拿到别的表的同名约束。
func baselineUniqueColumnSets(t *testing.T, table string) [][]string {
	t.Helper()
	src := readFile(t, filepath.FromSlash(baselineSchema))
	start := strings.Index(src, "CREATE TABLE "+table+" (")
	if start < 0 {
		start = strings.Index(src, "CREATE TABLE public."+table+" (")
	}
	if start < 0 {
		t.Fatalf("基线 %s 里找不到 CREATE TABLE %s —— 门需要同步", baselineSchema, table)
	}
	// 该表的段以第一个顶层 ')' 结束（列定义里没有嵌套括号到那一层之前的 ')'），
	// 但约束常写在表外（ALTER TABLE … ADD CONSTRAINT），所以要多取一段。
	seg := src[start:]
	if i := strings.Index(seg[1:], "\n);"); i > 0 {
		seg = seg[:i+3]
	}
	// 追加紧随其后的 ALTER TABLE 段（约束常在那里）
	if a := strings.Index(src[start:], "ALTER TABLE ONLY public."+table); a > 0 {
		seg += src[start+a : start+a+1500]
	}

	var sets [][]string
	for _, m := range reUniqueCols.FindAllStringSubmatch(seg, -1) {
		sets = append(sets, splitCols(m[2]))
	}
	if len(sets) == 0 {
		t.Fatalf("基线 %s 的 %s 段里没解析出任何唯一约束 —— 门可能已失效，请复核", baselineSchema, table)
	}
	return sets
}

func sameSet(a, b []string) bool {
	as := append([]string(nil), a...)
	bs := append([]string(nil), b...)
	sort.Strings(as)
	sort.Strings(bs)
	if len(as) != len(bs) {
		return false
	}
	for i := range as {
		if as[i] != bs[i] {
			return false
		}
	}
	return true
}

// TestWriterONConflictIsCoveredByBaselineUniqueConstraint 是本文件的正身。
//
// PG 的 ON CONFLICT (cols) 要求存在一个**恰好**由这些列构成的唯一索引；
// 少一列、多一列、换顺序都不行。所以这里比的是**集合相等**，不是包含。
func TestWriterONConflictIsCoveredByBaselineUniqueConstraint(t *testing.T) {
	writer := readFile(t, "writer.go")
	m := reONConflict.FindStringSubmatch(writer)
	if m == nil {
		t.Fatal("writer.go 里没有解析到 ON CONFLICT 子句 —— writer 形态变了，请同步本判据")
	}
	want := splitCols(m[1])
	if len(want) == 0 {
		t.Fatal("ON CONFLICT 列为空")
	}
	t.Logf("writer 的 ON CONFLICT 推断列（%d 个）= %v", len(want), want)

	sets := baselineUniqueColumnSets(t, "ursm_node_snapshot_min")
	for _, s := range sets {
		if sameSet(s, want) {
			return // 有唯一约束恰好覆盖 ⇒ 合法
		}
	}

	var got []string
	for _, s := range sets {
		got = append(got, "["+strings.Join(s, ", ")+"]")
	}
	t.Errorf("writer 的 ON CONFLICT (%s) 在权威基线 %s 的 ursm_node_snapshot_min 上"+
		"找不到**完全相同**的唯一约束；基线现有的是 %s。\n"+
		"PG 的 ON CONFLICT 推断要求唯一索引的列集合与之**完全相等**"+
		"（少一列、多一列、换顺序都不成立），不成立时每次插入抛 SQLSTATE 42P10，"+
		"而日志只记 WARN ⇒ 静默的全量写入失败。\n"+
		"★ 这正是 2026-10-03 起生产上发生的那件事：schema 漂移在 453 的 3 列 PK，"+
		"而基线与 writer 都是 4 列。修法是让 schema 回到基线形态（迁移 463 的 PK），"+
		"不是改 writer。",
		strings.Join(want, ", "), baselineSchema, strings.Join(got, " / "))
}
