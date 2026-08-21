package startup

import (
	"os"
	"strings"
	"testing"
)

// TestMigration554GoalRunsContract：验证 migration 553 包含关键合约（设计 13 §6.2，Wave 2-A）。
func TestMigration554GoalRunsContract(t *testing.T) {
	up, err := os.ReadFile("554_goal_runs.sql")
	if err != nil {
		t.Fatal(err)
	}
	body := string(up)

	// 必须包含三个核心表
	requiredTables := []string{
		"CREATE TABLE IF NOT EXISTS goal_runs",
		"CREATE TABLE IF NOT EXISTS goal_run_steps",
		"CREATE TABLE IF NOT EXISTS goal_run_actions",
	}
	for _, required := range requiredTables {
		if !strings.Contains(body, required) {
			t.Errorf("553 missing %q", required)
		}
	}

	// 必须包含状态机约束
	requiredConstraints := []string{
		"goal_runs_status_check",
		"goal_runs_version_positive",
		"goal_runs_counters_non_negative",
		"goal_runs_terminal_complete",
		"goal_runs_tenant_request_unique",
		"goal_run_steps_sequence_non_negative",
		"goal_run_actions_idempotency_unique",
	}
	for _, constraint := range requiredConstraints {
		if !strings.Contains(body, constraint) {
			t.Errorf("553 missing constraint %q", constraint)
		}
	}

	// 必须包含 CAS version 字段
	if !strings.Contains(body, "version") || !strings.Contains(body, "BIGINT NOT NULL DEFAULT 1") {
		t.Error("553 missing CAS version field")
	}

	// 必须包含租约字段
	for _, lease := range []string{"lease_owner", "lease_until"} {
		if !strings.Contains(body, lease) {
			t.Errorf("553 missing lease field %q", lease)
		}
	}

	// 必须包含 RLS 策略
	requiredPolicies := []string{
		"goal_runs_tenant_isolation",
		"goal_runs_super_admin_bypass",
		"goal_run_steps_tenant_isolation",
		"goal_run_steps_super_admin_bypass",
		"goal_run_actions_tenant_isolation",
		"goal_run_actions_super_admin_bypass",
	}
	for _, policy := range requiredPolicies {
		if !strings.Contains(body, policy) {
			t.Errorf("553 missing RLS policy %q", policy)
		}
	}

	// 必须包含关键索引
	requiredIndexes := []string{
		"idx_goal_runs_tenant_id",
		"idx_goal_runs_session_request",
		"idx_goal_runs_runnable",
		"idx_goal_runs_lease_expiry",
		"idx_goal_runs_deadline",
		"idx_goal_run_steps_request",
		"idx_goal_run_actions_run_status",
		"idx_goal_run_actions_retry",
	}
	for _, index := range requiredIndexes {
		if !strings.Contains(body, index) {
			t.Errorf("553 missing index %q", index)
		}
	}

	// 必须启用 RLS
	for _, table := range []string{"goal_runs", "goal_run_steps", "goal_run_actions"} {
		enableRLS := "ALTER TABLE " + table + " ENABLE ROW LEVEL SECURITY"
		if !strings.Contains(body, enableRLS) {
			t.Errorf("553 missing RLS enable for %s", table)
		}
	}

	// 必须包含 POST_CONDITION 验证 SQL
	if !strings.Contains(body, "POST_CONDITION") {
		t.Error("553 missing POST_CONDITION verification queries")
	}
}

// TestMigration554GoalRunsDown：验证 down migration 清理所有资源。
func TestMigration554GoalRunsDown(t *testing.T) {
	down, err := os.ReadFile("554_goal_runs.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	body := string(down)

	// 必须删除所有 RLS 策略
	requiredDrops := []string{
		"DROP POLICY IF EXISTS goal_run_actions_super_admin_bypass",
		"DROP POLICY IF EXISTS goal_run_actions_tenant_isolation",
		"DROP POLICY IF EXISTS goal_run_steps_super_admin_bypass",
		"DROP POLICY IF EXISTS goal_run_steps_tenant_isolation",
		"DROP POLICY IF EXISTS goal_runs_super_admin_bypass",
		"DROP POLICY IF EXISTS goal_runs_tenant_isolation",
	}
	for _, drop := range requiredDrops {
		if !strings.Contains(body, drop) {
			t.Errorf("553 down missing %q", drop)
		}
	}

	// 必须删除所有表
	requiredTableDrops := []string{
		"DROP TABLE IF EXISTS goal_run_actions CASCADE",
		"DROP TABLE IF EXISTS goal_run_steps CASCADE",
		"DROP TABLE IF EXISTS goal_runs CASCADE",
	}
	for _, drop := range requiredTableDrops {
		if !strings.Contains(body, drop) {
			t.Errorf("553 down missing %q", drop)
		}
	}

	// 必须包含 POST_CONDITION 验证
	if !strings.Contains(body, "POST_CONDITION") {
		t.Error("553 down missing POST_CONDITION verification")
	}
}

// TestMigration554GoalRunsIdempotent：验证 migration 幂等性（IF NOT EXISTS / DROP IF EXISTS）。
func TestMigration554GoalRunsIdempotent(t *testing.T) {
	up, err := os.ReadFile("554_goal_runs.sql")
	if err != nil {
		t.Fatal(err)
	}
	body := string(up)

	// 所有 CREATE TABLE 必须带 IF NOT EXISTS
	createTables := []string{
		"CREATE TABLE IF NOT EXISTS goal_runs",
		"CREATE TABLE IF NOT EXISTS goal_run_steps",
		"CREATE TABLE IF NOT EXISTS goal_run_actions",
	}
	for _, create := range createTables {
		if !strings.Contains(body, create) {
			t.Errorf("553 missing idempotent table creation: %q", create)
		}
	}

	// 所有 CREATE INDEX 必须带 IF NOT EXISTS
	if strings.Contains(body, "CREATE INDEX idx_") && !strings.Contains(body, "CREATE INDEX IF NOT EXISTS") {
		t.Error("553 CREATE INDEX must use IF NOT EXISTS for idempotency")
	}

	// 所有 DROP POLICY 必须带 IF EXISTS
	if strings.Contains(body, "DROP POLICY goal_") && !strings.Contains(body, "DROP POLICY IF EXISTS") {
		t.Error("553 DROP POLICY must use IF EXISTS for idempotency")
	}
}
