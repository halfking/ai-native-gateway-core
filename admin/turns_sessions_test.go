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

func TestEnsureTurnsNonNil(t *testing.T) {
	// nil 会话不 panic
	ensureTurnsNonNil(nil)

	// 无轮次（过滤后）→ 归一化为非 nil 空切片，避免 JSON null
	g := &TurnsSessionGroup{SessionID: "gw_s_empty"}
	ensureTurnsNonNil(g)
	if g.Turns == nil {
		t.Fatal("expected non-nil Turns after ensureTurnsNonNil")
	}
	if len(g.Turns) != 0 {
		t.Fatalf("expected empty Turns, got %d", len(g.Turns))
	}

	// 已有轮次 → 保持不变
	filled := &TurnsSessionGroup{SessionID: "gw_s_filled", Turns: []TurnGroupItem{{TurnNo: 1}}}
	ensureTurnsNonNil(filled)
	if len(filled.Turns) != 1 {
		t.Fatalf("expected existing turns preserved, got %d", len(filled.Turns))
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
		// search now matches title/topic/intent/summary → 4 placeholders $11..$14,
		// cursor follows at $15/$16 (was $14/$15 before intent was added).
		"COALESCE(s.title, st.title, ss.title) ILIKE '%'||$11||'%'",
		"COALESCE(s.summary, ss.summary) ILIKE '%'||$14||'%'",
		"(s.updated_at, s.session_id) < ($15, $16)",
	}
	for _, c := range checks {
		if !strings.Contains(where, c) {
			t.Errorf("where missing %q\nwhere=%s", c, where)
		}
	}

	if len(args) != 16 {
		t.Fatalf("expected 16 args, got %d: %v", len(args), args)
	}
	if nextArg != 17 {
		t.Fatalf("expected nextArg 17, got %d", nextArg)
	}
	if args[len(args)-1] != "gw_s1" {
		t.Fatalf("expected cursor session id arg, got %v", args[len(args)-1])
	}
}

func TestDeriveSessionParent(t *testing.T) {
	cases := []struct {
		sessionID string
		parent    string
		relation  string
	}{
		{"gt_gw_abc123", "gw_abc123", "auto_title"},
		{"gs_gw_abc123", "gw_abc123", "auto_summary"},
		{"gw_abc123", "", ""},
		{"", "", ""},
	}
	for _, c := range cases {
		parent, relation := deriveSessionParent(c.sessionID)
		if parent != c.parent || relation != c.relation {
			t.Errorf("deriveSessionParent(%q) = (%q,%q), want (%q,%q)",
				c.sessionID, parent, relation, c.parent, c.relation)
		}
	}
}

func TestApplySessionParent(t *testing.T) {
	// handoff 持久化记录优先
	g := &TurnsSessionGroup{SessionID: "gw_new", ParentSessionID: strPtr("gw_old")}
	applySessionParent(g)
	if *g.ParentSessionID != "gw_old" || *g.ParentRelation != "handoff" {
		t.Fatalf("handoff parent should win, got %v %v", g.ParentSessionID, g.ParentRelation)
	}
	// 无 handoff 记录时按前缀推导
	g2 := &TurnsSessionGroup{SessionID: "gt_gw_orig"}
	applySessionParent(g2)
	if g2.ParentSessionID == nil || *g2.ParentSessionID != "gw_orig" || *g2.ParentRelation != "auto_title" {
		t.Fatalf("prefix derivation failed: %v %v", g2.ParentSessionID, g2.ParentRelation)
	}
	// 普通会话无父
	g3 := &TurnsSessionGroup{SessionID: "gw_plain"}
	applySessionParent(g3)
	if g3.ParentSessionID != nil || g3.ParentRelation != nil {
		t.Fatalf("plain session should have no parent, got %v %v", g3.ParentSessionID, g3.ParentRelation)
	}
}

func TestBuildTurnsSessionWhere_TurnLevelFiltersSession(t *testing.T) {
	// model / provider / status_code 应通过 EXISTS 在会话级排除无匹配轮次的会话
	req := httptest.NewRequest(http.MethodGet, "/api/admin/turns/sessions?model=m1&provider=p1&status_code=500", nil)
	where, args, nextArg := buildTurnsSessionWhere(req, "t1", time.Time{}, time.Time{}, time.Time{}, "", 1)

	for _, c := range []string{
		"s.tenant_id = $1",
		"ft.model = $2",
		"ft.provider = $3",
		"ft.status_code = $4",
	} {
		if !strings.Contains(where, c) {
			t.Errorf("where missing %q\nwhere=%s", c, where)
		}
	}
	if !strings.Contains(where, "EXISTS (SELECT 1 FROM public.session_turns ft") {
		t.Errorf("expected EXISTS subquery on session_turns: %s", where)
	}
	if len(args) != 4 || nextArg != 5 {
		t.Fatalf("expected 4 args / nextArg=5, got %d args / nextArg=%d", len(args), nextArg)
	}
	if args[3] != 500 {
		t.Fatalf("status_code arg should be int 500, got %v (%T)", args[3], args[3])
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
