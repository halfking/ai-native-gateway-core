package startup

// session_turns_host_tags_migration_test.go —— 迁移 885 的静态契约门。
//
// 缺陷本体（审计 A5，2026-10-20）：会话族**全族无 host/host_name/instance_id
// 列**（「哪台机器写的这轮」无处可查），且**轮次级无 tags**（标签只到
// session 级，public.session_tags / 迁移 351）。
//
// 这道门钉的是迁移 885 的**对称性**，因为对称性不是审美问题而是正确性：
// 迁移 707 给 promote_session_turns_hot_to_partition 加了**列集合契约校验**
// （父表与 hot 的 (列名:类型:非空) 集合必须全等，否则 promote 当场
// RAISE EXCEPTION 'session_turns hot/parent column contract has drifted'）。
// 只给一侧加列 ⇒ promote 直接报错 ⇒ 热表永远排不进月分区。
//
// 为什么是静态门而不是真库门：真库门需要「父表与 hot 同时存在且同形」的
// 库，而这恰恰是本迁移要**建立**的前提 —— 用一个尚未成立的前提去验证
// 前提，是本仓吃过亏的形态（AGENTS.md「判据红了先怀疑自己的前提」）。

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

const migration885 = "885_session_turns_host_tags.sql"

// addColumnRe extracts the ADD COLUMN list of one ALTER TABLE statement.
var addColumnRe = regexp.MustCompile(`(?is)ALTER\s+TABLE\s+public\.(session_turns|session_turns_hot)\s+(.*?);`)

// TestMigration885AddsHostAndTagsToBothTables pins the symmetry.
func TestMigration885AddsHostAndTagsToBothTables(t *testing.T) {
	raw, err := os.ReadFile(migration885)
	if err != nil {
		t.Fatalf("read %s: %v", migration885, err)
	}
	body := string(raw)

	cols := map[string]string{}
	for _, m := range addColumnRe.FindAllStringSubmatch(body, -1) {
		cols[m[1]] = m[2]
	}
	for _, table := range []string{"session_turns", "session_turns_hot"} {
		list, ok := cols[table]
		if !ok {
			t.Fatalf("%s 没有对 public.%s 做 ALTER TABLE —— 两列必须两侧同形", migration885, table)
		}
		for _, want := range []string{"host", "tags"} {
			if !regexp.MustCompile(`(?i)ADD\s+COLUMN\s+IF\s+NOT\s+EXISTS\s+` + want + `\b`).MatchString(list) {
				t.Errorf("public.%s 的 ALTER 里没有 ADD COLUMN %s（列集合契约要求两侧同形）：\n%s", table, want, list)
			}
		}
	}
	if cols["session_turns"] != cols["session_turns_hot"] {
		t.Errorf("父表与 hot 的 ADD COLUMN 列表必须逐字相同（类型/默认值不同即破坏 707 的列集合契约）：\n父表：%q\nhot ：%q",
			cols["session_turns"], cols["session_turns_hot"])
	}
}

// TestMigration885DoesNotDropOrRename pins the "no destructive DDL" rule.
//
// The family is live and the promote contract compares **column sets**: a
// DROP COLUMN on one side only would break the contract, and a RENAME would
// break every reader that names the column.
func TestMigration885DoesNotDropOrRename(t *testing.T) {
	raw, err := os.ReadFile(migration885)
	if err != nil {
		t.Fatalf("read %s: %v", migration885, err)
	}
	body := string(raw)
	// Strip comment lines first: the header explains the down script, and a
	// naive scan would trip over the word DROP in the prose.
	var code []string
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}
		code = append(code, line)
	}
	codeBody := strings.Join(code, "\n")
	for _, forbidden := range []string{"DROP COLUMN", "RENAME", "DROP TABLE", "ALTER COLUMN"} {
		if strings.Contains(strings.ToUpper(codeBody), forbidden) {
			t.Errorf("%s 含有破坏性 DDL（%s）：本迁移只允许加列", migration885, forbidden)
		}
	}
}

// TestMigration885DownDropsBothColumns pins the down script: it must remove
// the two columns from **both** tables (one-sided removal would break the
// same column-set contract).
func TestMigration885DownDropsBothColumns(t *testing.T) {
	raw, err := os.ReadFile("885_session_turns_host_tags.down.sql")
	if err != nil {
		t.Fatalf("read down script: %v", err)
	}
	body := string(raw)
	for _, table := range []string{"session_turns", "session_turns_hot"} {
		block := regexp.MustCompile(`(?is)ALTER\s+TABLE\s+public\.` + table + `\s+(.*?);`).FindStringSubmatch(body)
		if block == nil {
			t.Fatalf("down 脚本没有对 public.%s 做 ALTER TABLE", table)
		}
		for _, want := range []string{"host", "tags"} {
			if !strings.Contains(block[1], "DROP COLUMN IF EXISTS "+want) {
				t.Errorf("down 脚本对 public.%s 没有 DROP COLUMN IF EXISTS %s", table, want)
			}
		}
	}
}
