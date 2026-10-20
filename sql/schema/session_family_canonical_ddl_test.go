package schema

// session_family_canonical_ddl_test.go —— 正典 DDL 的两道门（2026-10-20，审计 D3 / D3c）。
//
// D3：正典 DDL 里的 session_bodies COMMENT 声称「使用columnar存储格式，配合
// zstd压缩，预计可节省60-80%磁盘空间」—— 与实现**相反**：本族因
// promote_session_bodies 用 ON CONFLICT DO NOTHING（列存关系不能承载唯一
// 索引）而必须留 heap，压缩由 TOAST 的 lz4 完成
// （765_bodies_columnar_storage.sql:127-128，理由见其 :13-21；
// 829_bodies_columnar_rollback.sql 记录了列存方案被回滚）。
//
// D3c：正典目录 sql/objects/tables/ 与真实 schema 已漂移，且**缺表**：
// 迁移里创建的 session 族表有 14 张在正典目录里没有任何条目（session_tools
// 也在内）⇒ 下一轮审计「照正典文件读」会读出一张不存在的表。
//
// 两道门都刻意**不做**「集合相等」：正典目录里还有若干「不在任何迁移里的
// 对象」（它们是这些对象的唯一 DDL 出处，删掉就再也建不出来），因此本门
// 钉的是**包含关系**（迁移建的表 ⊆ 正典有条目且条目真的声明了这张表）。

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

const (
	canonicalTablesDir   = "../../sql/objects/tables"
	startupMigrationsDir = "../../sql/migrations/startup"
)

// storageEngineNegations are the markers that turn an engine mention into a
// *correction* rather than a claim. A canonical DDL file may discuss
// "columnar" (e.g. to say "this table never was columnar"), but it may not
// assert it.
var storageEngineNegations = []string{"从未", "不可", "必须", "回滚", "NOT ", "never"}

// commentBlockRe captures a SQL string literal used as a table comment.
var commentBlockRe = regexp.MustCompile(`(?is)COMMENT\s+ON\s+TABLE\s+public\.([a-z0-9_]+)\s+IS\s+('(?:[^']|'')*')`)

// readCanonicalTable reads one canonical object file.
func readCanonicalTable(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(canonicalTablesDir, name+".sql"))
	if err != nil {
		t.Fatalf("read canonical object %s: %v", name, err)
	}
	return string(raw)
}

// TestCanonicalSessionBodiesDDLStatesTheRealStorageEngine is the D3 gate.
//
// The assertion is on the *absence of the false claim* plus the *presence of
// the true one* — not on the mere presence of the word "heap", which a file
// could satisfy while still claiming columnar elsewhere.
func TestCanonicalSessionBodiesDDLStatesTheRealStorageEngine(t *testing.T) {
	raw := readCanonicalTable(t, "session_bodies")

	for _, forbidden := range []string{
		"使用columnar存储格式",
		"配合zstd压缩",
		"columnar存储格式",
	} {
		if strings.Contains(raw, forbidden) {
			t.Errorf("正典 DDL 仍声称未验证的存储引擎（%q）：\n%s\n"+
				"真相是 heap + lz4 TOAST（765_bodies_columnar_storage.sql:127-128）；"+
				"本族因 promote 用 ON CONFLICT DO NOTHING 而不能列存（765:13-21）",
				forbidden, raw)
		}
	}
	for _, required := range []string{"heap", "lz4", "从未"} {
		if !strings.Contains(raw, required) {
			t.Errorf("正典 DDL 缺少存储引擎真相标记 %q（应写明 heap + lz4 TOAST，且明确「从未列存」）：\n%s",
				required, raw)
		}
	}
}

// TestCanonicalSessionFamilyStorageEngineClaimsAreCorrections generalises the
// D3 gate to the whole session family: a canonical file may *mention* a
// storage engine, but only inside a comment that also corrects the record.
// A plain claim ("本表使用列存") has no negation marker and is a defect.
func TestCanonicalSessionFamilyStorageEngineClaimsAreCorrections(t *testing.T) {
	entries, err := os.ReadDir(canonicalTablesDir)
	if err != nil {
		t.Fatalf("read canonical tables dir: %v", err)
	}
	checked := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		if !strings.HasPrefix(e.Name(), "session") {
			continue
		}
		checked++
		raw, err := os.ReadFile(filepath.Join(canonicalTablesDir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		for _, m := range commentBlockRe.FindAllStringSubmatch(string(raw), -1) {
			block := m[2]
			lowered := strings.ToLower(block)
			if !strings.Contains(lowered, "columnar") && !strings.Contains(lowered, "zstd") {
				continue
			}
			corrected := false
			for _, marker := range storageEngineNegations {
				if strings.Contains(block, marker) {
					corrected = true
					break
				}
			}
			if !corrected {
				t.Errorf("%s 的表注释提到存储引擎（columnar/zstd）却没有修正标记（%v）：\n%s\n"+
					"正典 DDL 可以讨论引擎，但只能以「本表并非如此」的方式讨论；"+
					"无标记的提及会被下一个读者读成声明",
					e.Name(), storageEngineNegations, block)
			}
		}
	}
	if checked == 0 {
		t.Fatal("没有扫描到任何 session_* 正典文件——量具坏了（门会恒绿）")
	}
}

// TestCanonicalObjectsCoverEveryMigratedSessionTable is the D3c gate.
//
// Containment, not equality: every table created by a startup migration must
// have a canonical object file that really declares it. The reverse direction
// is deliberately not asserted — sql/objects/tables/ also holds objects that
// no migration creates (they are the only DDL those objects have), and the
// monthly partition snapshots are ATTACH files, not CREATE TABLE.
func TestCanonicalObjectsCoverEveryMigratedSessionTable(t *testing.T) {
	entries, err := os.ReadDir(startupMigrationsDir)
	if err != nil {
		t.Fatalf("read startup migrations dir: %v", err)
	}
	createRe := regexp.MustCompile(`(?i)CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?public\.(session_[a-z0-9_]+)\s*\(`)
	migrated := map[string]string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(startupMigrationsDir, e.Name()))
		if err != nil {
			t.Fatalf("read migration %s: %v", e.Name(), err)
		}
		for _, m := range createRe.FindAllStringSubmatch(string(raw), -1) {
			if _, seen := migrated[m[1]]; !seen {
				migrated[m[1]] = e.Name()
			}
		}
	}
	if len(migrated) < 20 {
		t.Fatalf("只扫描到 %d 张迁移创建的 session_* 表——量具坏了（门会恒绿）", len(migrated))
	}

	names := make([]string, 0, len(migrated))
	for name := range migrated {
		names = append(names, name)
	}
	sort.Strings(names)

	missing := 0
	for _, name := range names {
		objPath := filepath.Join(canonicalTablesDir, name+".sql")
		raw, err := os.ReadFile(objPath)
		if err != nil {
			missing++
			t.Errorf("迁移 %s 创建的表 %s 在正典目录里没有条目（%s）\n"+
				"  正典目录是静态 jsonb lint 的列映射来源，也是下一轮审计读形状的地方；"+
				"  缺条目 = 读出一张不存在的表",
				migrated[name], name, objPath)
			continue
		}
		declRe := regexp.MustCompile(`(?i)CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?public\.` + regexp.QuoteMeta(name) + `\s*\(`)
		if !declRe.Match(raw) {
			t.Errorf("正典文件 %s 存在但没有真正声明表 %s", objPath, name)
		}
	}
	t.Logf("扫描到 %d 张迁移创建的 session_* 表，正典目录缺 %d 个条目", len(migrated), missing)
}
