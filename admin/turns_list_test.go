package admin

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestBuildTurnsListWhere_ScopedDimensions pins the SQL contract for the new
// project / namespace / parent_request_id / task_type filters. Cross-turn
// queries must stay declarative (no JOIN public.sessions) so P0-Z4 acceptance
// stays valid.
//
// ★★ R181 轮：★ 参数编号整体后移两位 —— $1/$2 = partition_date 上下界，
//
//	$3/$4 = ts 窗口，$5… = 过滤条件。★ 原因：session_turns 按 partition_date
//	分区 ⇒ ⇒ 只按 ts 过滤 ⇒ PG 裁不掉分区 ⇒ ⇒ 必须把分区键带进谓词。
func TestBuildTurnsListWhere_ScopedDimensions(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/admin/turns?"+
		"project_id=p_demo&namespace=team-a&parent_request_id=parent-7&task_type=code", nil)
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	where, args, nextArg := buildTurnsListWhere(req, "tenant-1",
		now.Add(-time.Hour), now, time.Time{}, "", 0)

	wantClauses := []string{
		// ★★ R181 轮新增：★ 分区键下界（否则月分区裁不掉 ⇒ 每个分区都要探）。
		"t.partition_date >= $1",
		"t.partition_date <= $2",
		"t.ts >= $3",
		"t.ts <= $4",
		"t.tenant_id = $5",
		"t.project_id = $6",
		"t.namespace = $7",
		"t.parent_request_id = $8",
		"t.task_type = $9",
	}
	for _, c := range wantClauses {
		if !strings.Contains(where, c) {
			t.Errorf("expected clause %q missing in where=%s", c, where)
		}
	}
	if strings.Contains(where, "JOIN public.sessions") {
		t.Errorf("cross-turn query must not join public.sessions: %s", where)
	}
	if nextArg != 10 {
		t.Errorf("expected nextArg=10 (limit placeholder), got %d", nextArg)
	}
	if len(args) != 9 {
		t.Fatalf("expected 9 args, got %d (%v)", len(args), args)
	}
	// ★★ R181 轮：★ 前两个参数是 partition_date 上下界（UTC 日历日的字符串）。
	wantPDFrom, wantPDTo := sessionPartitionDateBounds(now.Add(-time.Hour), now)
	if args[0] != wantPDFrom || args[1] != wantPDTo {
		t.Errorf("args[0..1] = %v,%v want %v,%v", args[0], args[1], wantPDFrom, wantPDTo)
	}
	want := []any{
		now.Add(-time.Hour), now, "tenant-1",
		"p_demo", "team-a", "parent-7", "code",
	}
	for i, v := range want {
		if args[i+2] != v {
			t.Errorf("arg[%d] = %v, want %v", i+2, args[i+2], v)
		}
	}
}

// TestBuildTurnsListWhere_RejectsCrossSessionCursor ensures the composite
// cursor clause references all three components of (ts, session_id, turn_no).
//
// ★★ R181 轮：★ 编号后移两位 ⇒ 游标三元组落在 $5/$6/$7。
func TestBuildTurnsListWhere_RejectsCrossSessionCursor(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/admin/turns", nil)
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	where, args, nextArg := buildTurnsListWhere(req, "", now, now,
		now.Add(-time.Minute), "gw_prev", 5)
	if !strings.Contains(where, "(t.ts, t.session_id, t.turn_no) < ($5, $6, $7)") {
		t.Errorf("expected composite cursor clause, got where=%s", where)
	}
	if nextArg != 8 || len(args) != 7 {
		t.Errorf("expected 7 args / nextArg=8, got %d / %d", len(args), nextArg)
	}
}
