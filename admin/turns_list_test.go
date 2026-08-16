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
func TestBuildTurnsListWhere_ScopedDimensions(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/admin/turns?"+
		"project_id=p_demo&namespace=team-a&parent_request_id=parent-7&task_type=code", nil)
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	where, args, nextArg := buildTurnsListWhere(req, "tenant-1",
		now.Add(-time.Hour), now, time.Time{}, "", 0)

	wantClauses := []string{
		"t.ts >= $1",
		"t.ts <= $2",
		"t.tenant_id = $3",
		"t.project_id = $4",
		"t.namespace = $5",
		"t.parent_request_id = $6",
		"t.task_type = $7",
	}
	for _, c := range wantClauses {
		if !strings.Contains(where, c) {
			t.Errorf("expected clause %q missing in where=%s", c, where)
		}
	}
	if strings.Contains(where, "JOIN public.sessions") {
		t.Errorf("cross-turn query must not join public.sessions: %s", where)
	}
	if nextArg != 8 {
		t.Errorf("expected nextArg=8 (limit placeholder), got %d", nextArg)
	}
	if len(args) != 7 {
		t.Fatalf("expected 7 args, got %d (%v)", len(args), args)
	}
	want := []any{
		now.Add(-time.Hour), now, "tenant-1",
		"p_demo", "team-a", "parent-7", "code",
	}
	for i, v := range want {
		if args[i] != v {
			t.Errorf("arg[%d] = %v, want %v", i, args[i], v)
		}
	}
}

// TestBuildTurnsListWhere_RejectsCrossSessionCursor ensures the composite
// cursor clause references all three components of (ts, session_id, turn_no).
func TestBuildTurnsListWhere_RejectsCrossSessionCursor(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/admin/turns", nil)
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	where, args, nextArg := buildTurnsListWhere(req, "", now, now,
		now.Add(-time.Minute), "gw_prev", 5)
	if !strings.Contains(where, "(t.ts, t.session_id, t.turn_no) < ($3, $4, $5)") {
		t.Errorf("expected composite cursor clause, got where=%s", where)
	}
	if nextArg != 6 || len(args) != 5 {
		t.Errorf("expected 5 args / nextArg=6, got %d / %d", len(args), nextArg)
	}
}
