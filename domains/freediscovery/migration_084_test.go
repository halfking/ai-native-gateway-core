package freediscovery

import (
	"os"
	"strings"
	"testing"
)

// 结构性测试: 校验 084 迁移文件的 schema 不变量 (参考 handoff/migration_362_test.go 模式).
// 不执行 DDL — DDL 执行覆盖由数据库集成测试承担.

func readMigration084(t *testing.T, suffix string) string {
	t.Helper()
	name := "../../sql/migrations/084-freediscovery-schema" + suffix
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(data)
}

func TestMigration084_Up_CreatesThreeTables(t *testing.T) {
	up := readMigration084(t, ".sql")
	for _, table := range []string{"provider_templates", "discovery_tasks", "discovery_results"} {
		if !strings.Contains(up, "CREATE TABLE IF NOT EXISTS public."+table+" (") {
			t.Errorf("up migration must create table %s", table)
		}
	}
}

func TestMigration084_Up_RLSContract(t *testing.T) {
	up := readMigration084(t, ".sql")
	if !strings.Contains(up, "ENABLE ROW LEVEL SECURITY") {
		t.Fatal("up migration must enable RLS")
	}
	// 策略通过 FOREACH 循环 + format('tenant_isolation_%s') 拼接, 校验命名模式
	if !strings.Contains(up, "'tenant_isolation_' || table_name") {
		t.Fatal("missing tenant_isolation policy naming pattern")
	}
	// 循环必须覆盖三张表
	for _, table := range []string{"provider_templates", "discovery_tasks", "discovery_results"} {
		if !strings.Contains(up, "'"+table+"'") {
			t.Errorf("RLS loop must cover table %s", table)
		}
	}
	// get_current_tenant() 幂等保护必须存在 (075 被回滚后 084 仍可独立执行)
	if !strings.Contains(up, "get_current_tenant") {
		t.Fatal("up migration must idempotently ensure get_current_tenant()")
	}
	// tenant GUC 名是全库契约
	if !strings.Contains(up, "app.current_tenant") {
		t.Fatal("policies must reference app.current_tenant GUC")
	}
}

func TestMigration084_Up_TenantColumnContract(t *testing.T) {
	up := readMigration084(t, ".sql")
	// 三张表都必须带 tenant_id TEXT NOT NULL DEFAULT 'default' (075 契约)
	count := strings.Count(up, "tenant_id TEXT NOT NULL DEFAULT 'default'")
	if count < 3 {
		t.Fatalf("expected >=3 tenant_id columns with default, got %d", count)
	}
}

func TestMigration084_Up_UniqueConstraints(t *testing.T) {
	up := readMigration084(t, ".sql")
	if !strings.Contains(up, "UNIQUE(provider_code, tenant_id)") {
		t.Fatal("provider_templates must be unique per (provider_code, tenant_id)")
	}
	if !strings.Contains(up, "UNIQUE(task_id, model_id)") {
		t.Fatal("discovery_results must be unique per (task_id, model_id)")
	}
}

// TestMigration084_Up_AllTablesHaveUpdatedAt: omnifree_touch_updated_at()
// 触发器对三表循环挂载并引用 NEW.updated_at — 任何缺该列的表都会让
// 所有 UPDATE 报 42703 (E2E 实测教训, sqlmock 覆盖不到触发器行为).
func TestMigration084_Up_AllTablesHaveUpdatedAt(t *testing.T) {
	up := readMigration084(t, ".sql")
	// discovery_results 的建表语句必须显式含 updated_at
	if !strings.Contains(up, "updated_at TIMESTAMPTZ DEFAULT now()") {
		t.Fatal("discovery_results must define updated_at (trigger contract)")
	}
}

func TestMigration084_Up_CatalogExtension(t *testing.T) {
	up := readMigration084(t, ".sql")
	for _, col := range []string{"source_type", "discovery_task_id", "last_synced_at", "upstream_metadata"} {
		if !strings.Contains(up, "ADD COLUMN IF NOT EXISTS "+col) {
			t.Errorf("free_resource_catalog extension missing column %s", col)
		}
	}
}

func TestMigration084_Down_DropsTablesAndColumns(t *testing.T) {
	down := readMigration084(t, ".down.sql")
	for _, table := range []string{"discovery_results", "discovery_tasks", "provider_templates"} {
		if !strings.Contains(down, "DROP TABLE IF EXISTS public."+table+" CASCADE") {
			t.Errorf("down migration must drop %s", table)
		}
	}
	for _, col := range []string{"source_type", "discovery_task_id", "last_synced_at", "upstream_metadata"} {
		if !strings.Contains(down, "DROP COLUMN IF EXISTS "+col) {
			t.Errorf("down migration must drop catalog column %s", col)
		}
	}
	// 回滚顺序: 结果表必须先于任务表删除 (外键)
	if strings.Index(down, "discovery_results") > strings.Index(down, "discovery_tasks") {
		t.Fatal("down must drop discovery_results before discovery_tasks (FK order)")
	}
	// 不得删除 075 共享函数 (OmniFree 表仍依赖)
	if strings.Contains(down, "DROP FUNCTION") {
		t.Fatal("down migration must NOT drop shared functions (075 OmniFree depends on them)")
	}
}

func TestMigration084_UpDownAreTransactional(t *testing.T) {
	for _, suffix := range []string{".sql", ".down.sql"} {
		f := readMigration084(t, suffix)
		begin := strings.Index(f, "BEGIN;")
		commit := strings.Index(f, "COMMIT;")
		if begin < 0 || commit < 0 || begin > commit {
			t.Errorf("migration %s must be wrapped in BEGIN...COMMIT", suffix)
		}
	}
}
