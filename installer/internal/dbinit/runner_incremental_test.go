package dbinit

import (
	"strings"
	"testing"
)

// TestLedgerQueryPinned 钉住增量模式的判据口径：探测的必须是
// schema_migrations 台账的 version 列 —— 它是迁移尾部自登记 INSERT 维护的
// 那张表。判据读错表（比如漂到 gateway_db_revision_sequences）会让增量
// 模式在客户库上静默退化为「全部重放」或「全部跳过」。
func TestLedgerQueryPinned(t *testing.T) {
	if !strings.Contains(ledgerQuery, "schema_migrations") {
		t.Fatalf("ledgerQuery must read the schema_migrations ledger, got %q", ledgerQuery)
	}
	if !strings.Contains(ledgerQuery, "version") {
		t.Fatalf("ledgerQuery must select the version column, got %q", ledgerQuery)
	}
}

// TestMigrationVersionOf 钉住文件名 → version 前缀的解析规则。
func TestMigrationVersionOf(t *testing.T) {
	cases := []struct {
		name string
		want int
		ok   bool
	}{
		{"978_pi_canonical_rule_set.sql", 978, true},
		{"startup/980_pi_analysis_detail_views.sql", 980, true}, // 带目录前缀也要稳
		{"511_state_transitions_table.sql", 511, true},
		{"session_turns_hot_bootstrap.sql", 0, false}, // head cluster：无数字前缀
		{"abc_no_number.sql", 0, false},
		{"", 0, false},
	}
	for _, c := range cases {
		got, ok := migrationVersionOf(c.name)
		if ok != c.ok || got != c.want {
			t.Errorf("migrationVersionOf(%q) = (%d, %v), want (%d, %v)", c.name, got, ok, c.want, c.ok)
		}
	}
}

// TestStartupPendingIncrementalSemantics 钉住增量跳过语义的三个分支：
// 已登记 ⇒ 跳过；未登记 ⇒ 执行；解析不出前缀 ⇒ 恒执行（幂等契约兜底）。
func TestStartupPendingIncrementalSemantics(t *testing.T) {
	applied := map[int]bool{977: true, 978: true}
	if startupPending("978_pi_canonical_rule_set.sql", applied) {
		t.Error("ledger-registered 978 must be skipped in incremental mode")
	}
	if !startupPending("980_pi_analysis_detail_views.sql", applied) {
		t.Error("unregistered 980 must be pending")
	}
	if !startupPending("session_turns_hot_bootstrap.sql", applied) {
		t.Error("files without a numeric prefix must always run (idempotent bootstrap)")
	}
	// 空台账（len==0）由 InitSchema 的 incremental 开关挡在前面；
	// 这里钉住「函数本身对空 map 不会误跳过」的底座行为。
	if !startupPending("978_pi_canonical_rule_set.sql", map[int]bool{}) {
		t.Error("empty ledger must not skip anything")
	}
}

// TestStartupFilesIncrementalAgainstRealManifest 用真实 StartupFiles 清单验证
// 增量模式的行为边界：给定一个「登记到 977」的假想客户库，978 起的迁移
// 必须 pending；同时至少存在一个无前缀文件（head cluster bootstrap）保持
// 恒执行 —— 保证真实清单与本代码路径兼容，而不是只在合成用例里成立。
func TestStartupFilesIncrementalAgainstRealManifest(t *testing.T) {
	runner := NewRunner("citus", "user", "db", "/tmp/sql")
	if len(runner.StartupFiles) == 0 {
		t.Fatal("StartupFiles manifest is empty")
	}
	applied := map[int]bool{977: true}
	pending, noPrefix := 0, 0
	for _, name := range runner.StartupFiles {
		if _, ok := migrationVersionOf(name); !ok {
			noPrefix++
			continue
		}
		if startupPending(name, applied) {
			pending++
		}
	}
	if pending == 0 {
		t.Fatalf("with ledger up to 977, migrations >= 978 must be pending; manifest has %d files", len(runner.StartupFiles))
	}
	if noPrefix == 0 {
		t.Fatal("manifest is expected to contain at least one non-prefixed head-cluster file; if that changed, revisit startupPending semantics")
	}
}
