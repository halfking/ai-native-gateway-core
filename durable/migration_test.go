package durable

import (
	"os"
	"strings"
	"testing"
)

// migrationPath 是 durable schema 的 SSOT migration（doc 18 §11.1/§11.2/§12.2）。
// 载体跟随仓内 startup 编号式惯例（最近为 510/511/513/514）。
const migrationPath = "../sql/migrations/startup/516_durable_llm_tasks.sql"

func readMigration(t *testing.T) string {
	t.Helper()
	src, err := os.ReadFile(migrationPath)
	if err != nil {
		t.Fatalf("read durable migration: %v", err)
	}
	return string(src)
}

// TestMigration_TasksTableColumns：durable_llm_tasks 必须包含 doc 18 §11.1
// 建议字段的全部关键列（任务标识/租户/加密快照/状态机/租约 fencing/终态结果）。
func TestMigration_TasksTableColumns(t *testing.T) {
	body := readMigration(t)
	for _, col := range []string{
		"id", "tenant_id", "request_id", "session_id",
		"protocol", "endpoint",
		"request_snapshot_ciphertext", "snapshot_version", "encryption_key_id",
		"request_hash", "status", "error_kind", "reason_code",
		"attempt_count", "next_retry_at", "deadline_at", "expires_at",
		"lease_owner", "lease_until", "fencing_token",
		"semantic_content_committed", "commit_state",
		"result_ciphertext", "result_object_ref",
		"result_hash", "result_version", "content_type",
		"policy", "created_at", "updated_at", "completed_at",
		// §11.3 write-ahead commit：tool_call checkpoint 需要保存 tool call
		// ID/类型/序号/参数摘要，落在 checkpoint 相关列。
		"checkpoint_payload",
	} {
		if !strings.Contains(body, col) {
			t.Errorf("durable_llm_tasks migration missing column %q", col)
		}
	}
}

// TestMigration_CommitStateConstraint：commit_state 必须非空默认 'none' 且
// CHECK 限定 none/metadata/content/tool_call/terminal（doc 18 §11.1）。
func TestMigration_CommitStateConstraint(t *testing.T) {
	body := readMigration(t)
	for _, want := range []string{
		"commit_state                  TEXT NOT NULL DEFAULT 'none'",
		"CHECK (commit_state IN ('none', 'metadata', 'content', 'tool_call', 'terminal'))",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("durable migration missing %q", want)
		}
	}
}

// TestMigration_Indexes：doc 18 §11.1 要求的索引下限——runnable partial
// index、(tenant_id,status)、unique (tenant_id,request_id)、lease 过期索引，
// 以及 deadline reaper（§11.3）与回源查询（§12.1）所需索引。
func TestMigration_Indexes(t *testing.T) {
	body := readMigration(t)
	for _, want := range []string{
		"ON durable_llm_tasks (status, next_retry_at)",
		"ON durable_llm_tasks (tenant_id, status)",
		"UNIQUE (tenant_id, request_id)",
		"ON durable_llm_tasks (lease_until)",
		"ON durable_llm_tasks (deadline_at)",
		"ON durable_llm_tasks (session_id, request_id",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("durable migration missing index clause %q", want)
		}
	}
}

// TestMigration_EventsAppendOnly：§12.2 append-only durable_llm_task_events
// 每条事件包含 task/request/session/tenant、attempt、from/to、reason、时间、
// fencing token。
func TestMigration_EventsAppendOnly(t *testing.T) {
	body := readMigration(t)
	if !strings.Contains(body, "CREATE TABLE IF NOT EXISTS durable_llm_task_events") {
		t.Fatal("migration must create durable_llm_task_events")
	}
	for _, col := range []string{
		"task_id", "request_id", "session_id", "tenant_id",
		"attempt", "from_status", "to_status", "reason", "fencing_token", "created_at",
	} {
		if !strings.Contains(body, col) {
			t.Errorf("durable_llm_task_events missing column %q", col)
		}
	}
}

// TestMigration_RLS：任务表与事件表启用 tenant RLS（doc 18 §11.2），
// 跟随仓内 511/322 的 app.current_tenant 隔离 + super_admin/bypass 惯例。
func TestMigration_RLS(t *testing.T) {
	body := readMigration(t)
	if strings.Count(body, "ENABLE ROW LEVEL SECURITY") < 3 {
		t.Fatal("tasks, events and pending outbox tables must enable RLS")
	}
	for _, want := range []string{
		"durable_llm_tasks_tenant_isolation",
		"durable_llm_task_events_tenant_isolation",
		"app.current_tenant",
		"app.bypass_rls",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("durable migration RLS missing %q", want)
		}
	}
}

func TestMigration_PendingOutbox(t *testing.T) {
	body := readMigration(t)
	for _, want := range []string{
		"CREATE TABLE IF NOT EXISTS durable_pending_outbox",
		"task_id         UUID PRIMARY KEY",
		"result_version  BIGINT NOT NULL",
		"next_attempt_at TIMESTAMPTZ NOT NULL",
		"idx_durable_pending_outbox_retry",
		"durable_pending_outbox_tenant_isolation",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("durable pending outbox migration missing %q", want)
		}
	}
}

// TestMigration_DownExists：回滚脚本存在且 DROP 两张表（doc 18 §19.2：
// 回滚不删表是运营纪律，脚本能力上仍需可回滚的 down 文件）。
func TestMigration_DownExists(t *testing.T) {
	src, err := os.ReadFile("../sql/migrations/startup/516_durable_llm_tasks.down.sql")
	if err != nil {
		t.Fatalf("read down migration: %v", err)
	}
	body := string(src)
	for _, want := range []string{
		"DROP TABLE IF EXISTS durable_pending_outbox",
		"DROP TABLE IF EXISTS durable_llm_task_events",
		"DROP TABLE IF EXISTS durable_llm_tasks",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("down migration missing %q", want)
		}
	}
}
