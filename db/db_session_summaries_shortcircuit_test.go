package db

// db_session_summaries_shortcircuit_test.go — 形状对账守卫的契约门。
//
// 这个守卫唯一可能造成的伤害是**静默跳过本该执行的 DDL**：往 reconcile SQL
// 里加了一列，却忘了把它写进 sessionSummariesCanonicalColumns，守卫就会在
// 那列缺失的库上也短路，重建永远不发生，而且没有任何错误。
//
// 所以这里不测"守卫会短路"（那是库里的事），只钉清单与 SQL 的一致性：
// 从 session_summaries_schema.go 的 `ADD COLUMN IF NOT EXISTS <name>` 逐条
// 抽取，与清单双向比对（SQL 有而清单无 / 清单有而 SQL 无都算漂移）。
// 索引与约束同理。

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

func readSessionSummariesSource(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("session_summaries_schema.go")
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestSessionSummariesShortCircuitColumnListMatchesSQL(t *testing.T) {
	src := readSessionSummariesSource(t)

	addColumn := regexp.MustCompile(`ADD COLUMN IF NOT EXISTS\s+(\w+)`)
	inSQL := map[string]bool{}
	for _, match := range addColumn.FindAllStringSubmatch(src, -1) {
		inSQL[match[1]] = true
	}
	if len(inSQL) == 0 {
		t.Fatal("session_summaries_schema.go 里没解析到 ADD COLUMN IF NOT EXISTS；" +
			"解析规则失效会让这道门永远绿")
	}

	inList := map[string]bool{}
	for _, col := range sessionSummariesCanonicalColumns {
		if inList[col] {
			t.Errorf("清单里 %s 重复", col)
		}
		inList[col] = true
	}

	for col := range inSQL {
		if !inList[col] {
			t.Errorf("SQL 补了列 %s，但守卫清单里没有：形状已齐全的库会被误短路，"+
				"该列永远补不上", col)
		}
	}
	for col := range inList {
		if !inSQL[col] {
			t.Errorf("守卫清单里有 %s，但 reconcile SQL 不再补这一列：清单已漂移", col)
		}
	}
	t.Logf("列清单与 SQL 一致：%d 列", len(inList))
}

func TestSessionSummariesShortCircuitObjectListsMatchSQL(t *testing.T) {
	src := readSessionSummariesSource(t)

	for _, spec := range []struct {
		what  string
		regex *regexp.Regexp
		list  []string
	}{
		{"索引", regexp.MustCompile(`CREATE (?:UNIQUE )?INDEX (?:IF NOT EXISTS )?(\w+)`), sessionSummariesCanonicalIndexes},
		{"约束", regexp.MustCompile(`ADD CONSTRAINT (\w+)`), sessionSummariesCanonicalConstraints},
	} {
		inSQL := map[string]bool{}
		for _, match := range spec.regex.FindAllStringSubmatch(src, -1) {
			inSQL[match[1]] = true
		}
		if len(inSQL) == 0 {
			t.Fatalf("%s：没从 SQL 里解析到任何名字，解析规则失效会让这道门永远绿", spec.what)
		}
		inList := map[string]bool{}
		for _, name := range spec.list {
			if inList[name] {
				t.Errorf("%s 清单里 %s 重复", spec.what, name)
			}
			inList[name] = true
		}
		for name := range inSQL {
			if !inList[name] {
				t.Errorf("SQL 建的%s %s 不在守卫清单里：守卫可能在它缺失时短路", spec.what, name)
			}
		}
		for name := range inList {
			if !inSQL[name] {
				t.Errorf("守卫清单里的%s %s 已不在 SQL 中：清单漂移", spec.what, name)
			}
		}
		t.Logf("%s清单与 SQL 一致：%v", spec.what, sortedCopy(inList))
	}
}

// TestSessionSummariesShortCircuitIsWiredBeforeDDL 钉住短路点在 DDL 之前：
// 守卫放在 reconcile 之后等于没放。
func TestSessionSummariesShortCircuitIsWiredBeforeDDL(t *testing.T) {
	src := readSessionSummariesSource(t)
	fnStart := strings.Index(src, "func (d *DB) ensureSessionSummariesCanonical(")
	if fnStart < 0 {
		t.Fatal("找不到 ensureSessionSummariesCanonical")
	}
	body := src[fnStart:]
	guard := strings.Index(body, "d.sessionSummariesCanonicalComplete(ctx)")
	ddl := strings.Index(body, "ADD COLUMN IF NOT EXISTS tenant_id")
	if guard < 0 {
		t.Fatal("ensureSessionSummariesCanonical 没有调用 sessionSummariesCanonicalComplete：" +
			"守卫存在但没接线，热表仍会在每次开机被独占锁")
	}
	if ddl < 0 {
		t.Skip("reconcile SQL 的首条 ADD COLUMN 形态变了，无法定位 DDL 位置")
	}
	if guard > ddl {
		t.Fatal("守卫排在 DDL 之后：短路不生效")
	}
}

func sortedCopy(in map[string]bool) []string {
	out := make([]string, 0, len(in))
	for k := range in {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
