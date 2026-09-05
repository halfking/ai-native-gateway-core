package startup

import (
	"os"
	"strings"
	"testing"
)

// TestMigration555GoalRunActionsLeaseFencing：验证 migration 555 为
// goal_run_actions 增补 lease + fencing 列与 claim 路径索引
// （设计 13 §6.2，Wave 3-A）。
func TestMigration555GoalRunActionsLeaseFencing(t *testing.T) {
	up, err := os.ReadFile("555_goal_run_actions_lease_fencing.sql")
	if err != nil {
		t.Fatal(err)
	}
	body := string(up)

	// 必须包含 4 个新增列
	for _, column := range []string{"lease_owner", "lease_until", "fencing_token", "claimed_at"} {
		if !strings.Contains(body, column) {
			t.Errorf("555 missing column %q", column)
		}
	}

	// ADD COLUMN 必须幂等（IF NOT EXISTS）
	if !strings.Contains(body, "ADD COLUMN IF NOT EXISTS lease_owner") {
		t.Error("555 lease_owner must use ADD COLUMN IF NOT EXISTS for idempotency")
	}
	if !strings.Contains(body, "ADD COLUMN IF NOT EXISTS fencing_token") {
		t.Error("555 fencing_token must use ADD COLUMN IF NOT EXISTS for idempotency")
	}

	// 必须包含 claim 路径索引
	for _, idx := range []string{
		"idx_goal_run_actions_schedulable",
		"idx_goal_run_actions_lease_expiry",
	} {
		if !strings.Contains(body, idx) {
			t.Errorf("555 missing index %q", idx)
		}
	}

	// schedulable 索引必须是 partial index on (retry_at, action_id) where status='pending'
	if !strings.Contains(body, "CREATE INDEX IF NOT EXISTS idx_goal_run_actions_schedulable") {
		t.Error("555 must create schedulable index with IF NOT EXISTS")
	}

	// fence_token 注释必须存在（让 Reviewers 知道 fencing 含义）
	if !strings.Contains(body, "fencing") {
		t.Error("555 must mention 'fencing' in column comments")
	}

	// 必须包含 POST_CONDITION 验证
	if !strings.Contains(body, "POST_CONDITION") {
		t.Error("555 missing POST_CONDITION verification queries")
	}
}

// TestMigration555Down：验证 down migration 清理新增列与索引。
func TestMigration555Down(t *testing.T) {
	down, err := os.ReadFile("555_goal_run_actions_lease_fencing.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	body := string(down)

	// 必须回退新增列
	for _, drop := range []string{
		"DROP COLUMN IF EXISTS lease_owner",
		"DROP COLUMN IF EXISTS lease_until",
		"DROP COLUMN IF EXISTS fencing_token",
		"DROP COLUMN IF EXISTS claimed_at",
	} {
		if !strings.Contains(body, drop) {
			t.Errorf("555 down missing %q", drop)
		}
	}

	// 必须回退新增索引
	for _, drop := range []string{
		"DROP INDEX IF EXISTS idx_goal_run_actions_lease_expiry",
		"DROP INDEX IF EXISTS idx_goal_run_actions_schedulable",
	} {
		if !strings.Contains(body, drop) {
			t.Errorf("555 down missing %q", drop)
		}
	}

	// 必须重建原 retry 索引
	if !strings.Contains(body, "CREATE INDEX IF NOT EXISTS idx_goal_run_actions_retry") {
		t.Error("555 down must restore original idx_goal_run_actions_retry index")
	}

	// 必须包含 POST_CONDITION
	if !strings.Contains(body, "POST_CONDITION") {
		t.Error("555 down missing POST_CONDITION")
	}
}

// TestMigration555Idempotent：migration 上半部分必须可重复执行。
func TestMigration555Idempotent(t *testing.T) {
	up, err := os.ReadFile("555_goal_run_actions_lease_fencing.sql")
	if err != nil {
		t.Fatal(err)
	}
	body := string(up)

	// ADD COLUMN 必须带 IF NOT EXISTS
	if !strings.Contains(body, "ADD COLUMN IF NOT EXISTS") {
		t.Error("555 must use ADD COLUMN IF NOT EXISTS for idempotency")
	}

	// CREATE INDEX 必须带 IF NOT EXISTS
	if !strings.Contains(body, "CREATE INDEX IF NOT EXISTS") {
		t.Error("555 must use CREATE INDEX IF NOT EXISTS for idempotency")
	}

	// DROP INDEX 必须带 IF EXISTS（用于替换旧索引）
	if !strings.Contains(body, "DROP INDEX IF EXISTS") {
		t.Error("555 must use DROP INDEX IF EXISTS for idempotency")
	}
}
