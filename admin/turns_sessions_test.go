package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHandleTurnsSessions_ServiceUnavailableWithoutDB(t *testing.T) {
	h := NewHandler(nil, "test-secret", nil)
	req := httptest.NewRequest(http.MethodGet, "/api/admin/turns/sessions", nil)
	req.Header.Set("X-Test-Admin", "1")
	req.Header.Set("X-Tenant-ID", "default")
	req.Header.Set("X-User-ID", "tester")
	req.Header.Set("X-Role", "super_admin")
	rr := httptest.NewRecorder()
	h.handleTurnsSessions(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 with nil db, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestHandleTurnsSessions_MethodNotAllowed(t *testing.T) {
	h := NewHandler(nil, "test-secret", nil)
	req := httptest.NewRequest(http.MethodPost, "/api/admin/turns/sessions", nil)
	req.Header.Set("X-Test-Admin", "1")
	req.Header.Set("X-Tenant-ID", "default")
	req.Header.Set("X-User-ID", "tester")
	req.Header.Set("X-Role", "super_admin")
	rr := httptest.NewRecorder()
	h.handleTurnsSessions(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 for POST, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestComputeSessionAggs(t *testing.T) {
	saved := 120
	strategy := "truncate"
	now := time.Now().UTC()

	g := &TurnsSessionGroup{
		SessionID:   "gw_s1",
		ModelsUsed:  []string{},
		Compression: TurnsCompressionAgg{Strategies: []string{}},
		Turns: []TurnGroupItem{
			{TurnNo: 1, Ts: now.Add(-2 * time.Minute), Model: "gpt-4o", Provider: "openai", StatusCode: 200, Success: true, CompressionApplied: true, CompressionStrategy: &strategy, CompressionTokensSaved: &saved},
			{TurnNo: 2, Ts: now.Add(-1 * time.Minute), Model: "gpt-4o", Provider: "openai", StatusCode: 200, Success: true, AttemptNo: 1},
			{TurnNo: 3, Ts: now, Model: "claude-3-5", Provider: "anthropic", StatusCode: 503, Success: false, AttemptNo: 2},
		},
	}
	computeSessionAggs(g)

	if g.DurationMs != 2*60*1000 {
		t.Fatalf("expected duration 120000ms, got %d", g.DurationMs)
	}
	if g.FailoverCount != 2 {
		t.Fatalf("expected failover 2, got %d", g.FailoverCount)
	}
	if g.ErrorCount != 1 {
		t.Fatalf("expected error 1, got %d", g.ErrorCount)
	}
	if len(g.ModelsUsed) != 2 {
		t.Fatalf("expected 2 models, got %v", g.ModelsUsed)
	}
	if g.Compression.AppliedCount != 1 || g.Compression.TokensSaved != 120 {
		t.Fatalf("compression agg mismatch: %+v", g.Compression)
	}
	if len(g.Compression.Strategies) != 1 || g.Compression.Strategies[0] != "truncate" {
		t.Fatalf("compression strategies mismatch: %+v", g.Compression.Strategies)
	}
}

func TestComputeSessionAggs_NilSafe(t *testing.T) {
	computeSessionAggs(nil) // must not panic
	empty := &TurnsSessionGroup{ModelsUsed: []string{}, Compression: TurnsCompressionAgg{Strategies: []string{}}}
	computeSessionAggs(empty)
	if empty.DurationMs != 0 || empty.FailoverCount != 0 || empty.ErrorCount != 0 {
		t.Fatalf("expected zero aggs, got %+v", empty)
	}
}

func TestBuildTurnsSessionWhere(t *testing.T) {
	now := time.Now().UTC()
	tsFrom := now.Add(-time.Hour)
	tsTo := now

	req := httptest.NewRequest(http.MethodGet, "/api/admin/turns/sessions?"+
		"tenant=t1&project_id=p1&task_id=tk1&owner_user=u1&client=myapp&tags=a,b&search=hello", nil)
	where, args, nextArg := buildTurnsSessionWhere(req, "t1", tsFrom, tsTo, now.Add(-time.Minute), "gw_s1", 1)

	checks := []string{
		"COALESCE(ss.first_request_at, s.created_at) >= $1",
		"COALESCE(ss.first_request_at, s.created_at) <= $2",
		"s.tenant_id = $3",
		"ss.gw_project_id = $4",
		"sd.task_id = $5",
		"sd.owner_user = $6",
		"(sd.client_id = $7 OR sd.application_code = $8 OR s.client_type = $9)",
		"ss.user_tags && $10",
		"ILIKE '%'||$11||'%'",
		"(s.updated_at, s.session_id) < ($14, $15)",
	}
	for _, c := range checks {
		if !strings.Contains(where, c) {
			t.Errorf("where missing %q\nwhere=%s", c, where)
		}
	}

	if len(args) != 15 {
		t.Fatalf("expected 15 args, got %d: %v", len(args), args)
	}
	if nextArg != 16 {
		t.Fatalf("expected nextArg 16, got %d", nextArg)
	}
	if args[len(args)-1] != "gw_s1" {
		t.Fatalf("expected cursor session id arg, got %v", args[len(args)-1])
	}
}

func TestBuildTurnsSessionWhere_Empty(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/admin/turns/sessions", nil)
	where, args, nextArg := buildTurnsSessionWhere(req, "", time.Time{}, time.Time{}, time.Time{}, "", 1)
	// 无任何条件（含时间未指定）→ 无子句，供"默认最近 20 个会话"
	if nextArg != 1 || len(args) != 0 || where != "" {
		t.Fatalf("expected nextArg=1 args=0 where='', got %d %d %q", nextArg, len(args), where)
	}
}

func TestBuildTurnsSessionWhere_NoTimeWindowStillCursor(t *testing.T) {
	now := time.Now().UTC()
	req := httptest.NewRequest(http.MethodGet, "/api/admin/turns/sessions?project_id=p1", nil)
	where, args, nextArg := buildTurnsSessionWhere(req, "", time.Time{}, time.Time{}, now.Add(-time.Minute), "gw_s1", 1)
	if !strings.Contains(where, "ss.gw_project_id = $1") {
		t.Fatalf("project clause should be $1 when no time window: %s", where)
	}
	if !strings.Contains(where, "(s.updated_at, s.session_id) < ($2, $3)") {
		t.Fatalf("cursor should follow at $2,$3: %s", where)
	}
	if nextArg != 4 || len(args) != 3 {
		t.Fatalf("expected nextArg=4 args=3, got %d %d", nextArg, len(args))
	}
}
