package startup

import (
	"os"
	"strings"
	"testing"
)

// TestMigration563SessionSummaryHotTrigger 钉死 request_count/cost 写路径修复契约：
// 正确列名函数体、触发器仅挂 request_logs_hot、父表显式 DROP、幂等 REPLACE 回填。
func TestMigration563SessionSummaryHotTrigger(t *testing.T) {
	up, err := os.ReadFile("563_session_summary_trigger_on_hot.sql")
	if err != nil {
		t.Fatal(err)
	}
	body := string(up)

	required := []string{
		"CREATE OR REPLACE FUNCTION update_session_summary()",
		"NEW.gw_session_id",
		"NEW.ts",
		"COALESCE(NEW.cost_usd, 0)",
		"NEW.outbound_model",
		"INSERT INTO session_summaries",
		"request_count = session_summaries.request_count + 1",
		"DROP TRIGGER IF EXISTS trg_update_session_summary ON request_logs",
		"CREATE TRIGGER trg_update_session_summary",
		"AFTER INSERT ON request_logs_hot",
		"WHEN (NEW.gw_session_id IS NOT NULL AND NEW.gw_session_id <> '')",
		"ON CONFLICT (session_key) DO UPDATE SET",
		"request_count = EXCLUDED.request_count",
		"FROM request_logs_hot h",
	}
	for _, needle := range required {
		if !strings.Contains(body, needle) {
			t.Errorf("563 up missing required fragment:\n%s", needle)
		}
	}

	forbidden := []string{
		"NEW.session_key",
		"NEW.created_at",
		"NEW.total_cost",
		"NEW.input_cost",
		"NEW.upstream_model",
		"INSERT INTO session_owners",
	}
	for _, needle := range forbidden {
		if strings.Contains(body, needle) {
			t.Errorf("563 up must not contain obsolete/unsafe fragment:\n%s", needle)
		}
	}

	// 父表不得重建触发器（promote 双计防护）
	if strings.Contains(body, "CREATE TRIGGER trg_update_session_summary\n    AFTER INSERT ON request_logs\n") {
		t.Error("563 must not recreate trg_update_session_summary on parent request_logs")
	}

	down, err := os.ReadFile("563_session_summary_trigger_on_hot.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(down), "DROP TRIGGER IF EXISTS trg_update_session_summary ON request_logs_hot") {
		t.Error("563 down must drop hot trigger")
	}
}

// TestUpdateSessionSummaryObjectUsesHotColumns 防止 sql/objects 旧 310 体被 object-sync 回写。
func TestUpdateSessionSummaryObjectUsesHotColumns(t *testing.T) {
	raw, err := os.ReadFile("../../objects/functions/update_session_summary.sql")
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, needle := range []string{"NEW.gw_session_id", "NEW.cost_usd", "NEW.outbound_model"} {
		if !strings.Contains(body, needle) {
			t.Errorf("objects update_session_summary.sql missing %q", needle)
		}
	}
	for _, bad := range []string{"NEW.session_key", "NEW.created_at", "NEW.total_cost"} {
		if strings.Contains(body, bad) {
			t.Errorf("objects update_session_summary.sql still has obsolete %q", bad)
		}
	}
}
