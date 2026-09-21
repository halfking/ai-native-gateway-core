package dbinit

import "testing"

func TestStartupFilesIncludeRequestJourneyOutboxPrerequisites(t *testing.T) {
	runner := NewRunner("citus", "user", "db", "/tmp/sql")
	want := []string{
		"511_state_transitions_table.sql",
		"515_state_transitions_seq_unique.sql",
		"521_repair_state_transitions_tenant.sql",
		"530_request_journey_contract.sql",
		"531_request_journey_tenant_uniqueness.sql",
		"552_request_journey_durable_outbox.sql",
		"553_approval_resume_claim.sql",
		"600_outbound_body_to_bodies_hot.sql",
		"601_request_logs_bodies_drop_metadata.sql",
		"602_request_logs_promote_atomic.sql",
		"618_request_journey_snapshot_receipts.sql",
		"656_auto_route_selections_hot.sql",
		"657_durable_llm_tasks_decision_history.sql",
		"session_turns_hot_bootstrap.sql",
	}
	positions := make(map[string]int, len(runner.StartupFiles))
	for i, name := range runner.StartupFiles {
		positions[name] = i
	}
	for _, name := range want {
		if _, ok := positions[name]; !ok {
			t.Errorf("startup migration %s is missing", name)
		}
	}
	for i := 1; i < len(want); i++ {
		if positions[want[i-1]] >= positions[want[i]] {
			t.Errorf("startup migrations are out of order: %s before %s", want[i-1], want[i])
		}
	}
}

func TestStartupFilesIncludeSessionSummaryAndCacheRepairs(t *testing.T) {
	runner := NewRunner("citus", "user", "db", "/tmp/sql")
	want := []string{
		"655_session_summaries_schema_reconcile.sql",
		"560_session_summaries_tenant_uniqueness.sql",
		"563_session_summary_trigger_on_hot.sql",
		"564_session_summary_backfill_safe.sql",
		"572_session_summary_large_token_ratio.sql",
		"606_session_summaries_agent_expert_tags.sql",
		"627_candidate_failure_logs_aggregation_id_unified.sql",
		"644_tuning_views_selfcheck_and_candidate_failure_cache.sql",
		"645_session_bodies_hot_request_unique_repair.sql",
	}
	positions := make(map[string]int, len(runner.StartupFiles))
	for i, name := range runner.StartupFiles {
		positions[name] = i
	}
	for _, name := range want {
		if _, ok := positions[name]; !ok {
			t.Errorf("startup migration %s is missing", name)
		}
	}
	for i := 1; i < len(want); i++ {
		if positions[want[i-1]] >= positions[want[i]] {
			t.Errorf("startup migrations are out of order: %s before %s", want[i-1], want[i])
		}
	}
}

// TestStartupFilesHaveNoDuplicates（R51, 2026-09-21）：733/734 曾被注册两次
// （一对错位在 session_turns_hot_bootstrap 之前、一对在其后）。既有测试用
// map 记录首次出现位置、contains 语义，列表内重复不可见；而 InitSchema 按
// 序逐文件 applySQL 不去重，同一迁移会被重复应用。本测试钉住「列表无重复
// 文件名」与 733/734 的位置约束。
func TestStartupFilesHaveNoDuplicates(t *testing.T) {
	runner := NewRunner("citus", "user", "db", "/tmp/sql")
	seen := make(map[string]int, len(runner.StartupFiles))
	for i, name := range runner.StartupFiles {
		if prev, dup := seen[name]; dup {
			t.Errorf("startup migration %s registered twice (positions %d and %d)", name, prev, i)
			continue
		}
		seen[name] = i
	}
	// 733/734 依赖 session_turns_hot_bootstrap 建的 hot 表：bootstrap 在前、
	// 733 在前、734 在后。
	for _, name := range []string{
		"session_turns_hot_bootstrap.sql", "733_session_turn_details.sql", "734_request_logs_view_details_join.sql",
	} {
		if _, ok := seen[name]; !ok {
			t.Fatalf("startup migration %s is missing", name)
		}
	}
	if !(seen["session_turns_hot_bootstrap.sql"] < seen["733_session_turn_details.sql"] &&
		seen["733_session_turn_details.sql"] < seen["734_request_logs_view_details_join.sql"]) {
		t.Errorf("733/734 must run after session_turns_hot_bootstrap, got positions bootstrap=%d 733=%d 734=%d",
			seen["session_turns_hot_bootstrap.sql"], seen["733_session_turn_details.sql"], seen["734_request_logs_view_details_join.sql"])
	}
}
