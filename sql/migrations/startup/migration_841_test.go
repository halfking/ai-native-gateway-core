package startup

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const m841 = "841_monthly_partition_retention.sql"

func read841(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", name, err)
	}
	return string(b)
}

// TestMigration841_ConfigTableShipsEmpty pins the property that makes this
// migration safe to deploy on its own: an empty config table drops nothing.
// A migration that seeds retention days would delete production partitions as
// a side effect of being applied — the whole point of "机制先建、策略留空".
func TestMigration841_ConfigTableShipsEmpty(t *testing.T) {
	sql := read841(t, m841)

	if !strings.Contains(sql, "CREATE TABLE IF NOT EXISTS public.llm_gateway_partition_retention") {
		t.Fatal("缺少保留期配置表 llm_gateway_partition_retention")
	}
	// Any INSERT INTO the config table inside the migration seeds a policy.
	for _, bad := range []string{
		"INSERT INTO public.llm_gateway_partition_retention",
		"insert into public.llm_gateway_partition_retention",
	} {
		if strings.Contains(sql, bad) {
			t.Errorf("迁移里出现了 %q —— 配置表必须建完即空，保留期是业务决定，不在迁移里写死", bad)
		}
	}
	// guard must reject nonsense retention values
	if !strings.Contains(sql, "retain_months >= 1") {
		t.Error("缺少 retain_months >= 1 的 CHECK —— 否则 0 或负数会把当月都算成过期")
	}
}

// TestMigration841_NeverExpiresCurrentOrFutureMonths pins the single most
// dangerous off-by-one in this feature. Partitions are pre-created ahead of
// time (request_logs_bodies_2026_11 exists while "now" is 2026-10), so a
// `<=` instead of `<` would DROP the live and the future partition.
func TestMigration841_NeverExpiresCurrentOrFutureMonths(t *testing.T) {
	sql := stripSQLComments(read841(t, m841)) // 复用 migration_602_test.go 里的同名helper

	if !strings.Contains(sql, "mth < cutoff") {
		t.Fatal("过期判定必须是严格小于 `mth < cutoff`")
	}
	if strings.Contains(sql, "mth <= cutoff") {
		t.Error("出现了 `mth <= cutoff` —— 那会让当月分区与预建的未来分区被 DROP")
	}
	// retain_months = N keeps N months INCLUDING the current one.
	if !strings.Contains(sql, "(cfg.retain_months - 1)") {
		t.Error("过期线应是「当月月初 - (N-1) 个月」（保留 N 个月含当月），而不是直接用 N")
	}
	// ★ Regression: the real-DB behaviour gate found this one. `right(relname, 6)`
	// yields `026_07` (YYYY_MM is SEVEN chars), so to_date returns a meaningless
	// date and `mth < cutoff` becomes true for EVERY partition — the current
	// month and the pre-created future month get DROPPED. A contract gate that
	// only asks "is `mth < cutoff` present" passed straight through it.
	if strings.Contains(sql, "right(child.relname, 6)") ||
		strings.Contains(sql, "right(c.relname, 6)") ||
		strings.Contains(sql, "right(relname, 6)") {
		t.Error("月份必须用 substring 抓 `YYYY_MM`（7 个字符）；right(...,6) 会取成 `026_07`，" +
			"导致当月与预建的未来分区也被判为过期")
	}
	if !strings.Contains(sql, "([0-9]{4}_[0-9]{2})$") {
		t.Error("月份抽取必须锚定 `([0-9]{4}_[0-9]{2})$` 的结尾捕获组")
	}
	if !strings.Contains(sql, "IF mth IS NULL THEN") {
		t.Error("月份解析失败必须显式 CONTINUE 跳过，不能让 NULL 参与比较")
	}
}

// TestMigration841_OnlyTargetsMonthNamedChildrenOfConfiguredParent guards
// against a DROP that lands on something that merely looks like a partition.
func TestMigration841_OnlyTargetsMonthNamedChildrenOfConfiguredParent(t *testing.T) {
	sql := read841(t, m841)

	if !strings.Contains(sql, "JOIN pg_inherits i ON i.inhrelid = c.oid") {
		t.Error("必须经 pg_inherits 取直接子分区，不能按名字猜")
	}
	if !strings.Contains(sql, "parent.relname = cfg.family") {
		t.Error("父表名必须等于配置里的族名，否则族名写错会误删别家的分区")
	}
	if !strings.Contains(sql, `c.relname ~ '_\d{4}_\d{2}$'`) {
		t.Error(`缺少分区名形态约束 c.relname ~ '_\d{4}_\d{2}$'`)
	}
	// Identifier must go through format %I, never string concatenation.
	if !strings.Contains(sql, "format('DROP TABLE IF EXISTS public.%I'") {
		t.Error("DROP 必须用 format %I 引用标识符，不能拼字符串")
	}
}

// TestMigration841_DropIsAudited: DROP is irreversible; the log is the only
// trace afterwards.
func TestMigration841_DropIsAudited(t *testing.T) {
	sql := read841(t, m841)

	if !strings.Contains(sql, "CREATE TABLE IF NOT EXISTS public.llm_gateway_partition_drop_log") {
		t.Fatal("缺少 DROP 审计表 llm_gateway_partition_drop_log")
	}
	if !strings.Contains(sql, "INSERT INTO public.llm_gateway_partition_drop_log") {
		t.Error("每次 DROP 必须写一行审计日志")
	}
}

// TestMigration841_DownDoesNotDropPartitions: the rollback withdraws the
// mechanism only. Silently dropping live partitions on rollback would be a
// far worse failure than the feature being wrong.
func TestMigration841_DownDoesNotDropPartitions(t *testing.T) {
	down := read841(t, filepath.Join(".", "841_monthly_partition_retention.down.sql"))

	for _, want := range []string{
		"DROP FUNCTION IF EXISTS public.llm_gateway_drop_expired_month_partitions",
		"DROP FUNCTION IF EXISTS public.llm_gateway_expired_month_partitions",
		"DROP TABLE IF EXISTS public.llm_gateway_partition_drop_log",
		"DROP TABLE IF EXISTS public.llm_gateway_partition_retention",
	} {
		if !strings.Contains(down, want) {
			t.Errorf(".down.sql 缺少 %q", want)
		}
	}
	// Whitelist, not substring blacklist: a blacklist entry like
	// "DROP TABLE IF EXISTS public._2026_" can never match a real line such as
	// "... public.session_turns_2026_09" ⇒ the check would be tautologically
	// true. Mutation M7 proved it. Scan what actually gets dropped instead.
	allowed := map[string]bool{
		"llm_gateway_partition_drop_log":  true,
		"llm_gateway_partition_retention": true,
	}
	for _, m := range regexp.MustCompile(`DROP\s+TABLE\s+IF\s+EXISTS\s+public\.([A-Za-z0-9_]+)`).
		FindAllStringSubmatch(down, -1) {
		name := m[1]
		if !allowed[name] {
			t.Errorf(".down.sql 在删非本迁移创建的关系：public.%s —— 回滚只应撤机制，不该动业务数据", name)
		}
	}
}

// TestMigration841_RegisteredInInstallerEmbeddata: the installer must ship the
// same bytes, or production gets a different migration than the repo describes.
func TestMigration841_RegisteredInInstallerEmbeddata(t *testing.T) {
	src := read841(t, m841)
	cp := read841(t,
		filepath.Join("..", "..", "..", "installer", "cmd", "llm-gw-installer",
			"embeddata", "startup", m841))

	if src != cp {
		t.Error("installer embeddata 里的 841 与仓库源文件**逐字节不一致**")
	}
}

// TestMigration841_GoCallerDegradesOnMissingTable: with 841 rolled back the
// sweep must be a silent no-op, not a per-cycle WARN storm.
func TestMigration841_GoCallerDegradesOnMissingTable(t *testing.T) {
	b, err := os.ReadFile("../../../bg/partition_manager.go")
	if err != nil {
		t.Fatalf("读 bg/partition_manager.go 失败: %v", err)
	}
	s := string(b)

	start := strings.Index(s, "func (pm *PartitionManager) dropExpiredMonthlyPartitions(")
	if start < 0 {
		t.Fatal("未找到 dropExpiredMonthlyPartitions")
	}
	body := s[start:]
	if end := strings.Index(body, "\n}\n"); end > 0 {
		body = body[:end+3]
	}
	if !strings.Contains(body, "isUndefinedTable") {
		t.Error("sweep 未处理 42P01（表不存在）⇒ 回滚后会每个 cycle 刷 WARN")
	}
	if !strings.Contains(body, "isUndefinedFunction") {
		t.Error("sweep 未处理 42883（函数不存在）⇒ 函数被单独删掉时会刷 WARN")
	}
	// Must be Info, not Debug: auditing needs to see "nothing expired" too.
	if !strings.Contains(body, `slog.Info("partition_manager: monthly partition retention sweep"`) {
		t.Error("sweep 结果必须是 Info 级，否则审计时看不到「本来该删却没删」")
	}
}
