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

	if g.TotalTurns != 3 || g.TotalTokens != 0 || g.TotalCostUSD != 0 {
		t.Fatalf("aggregate totals mismatch: turns=%d tokens=%d cost=%v", g.TotalTurns, g.TotalTokens, g.TotalCostUSD)
	}
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
		"s.updated_at >= $1",
		"s.updated_at <= $2",
		"s.tenant_id = $3",
		"COALESCE(NULLIF(ss.gw_project_id, ''), sd.project_id) = $4",
		"sd.task_id = $5",
		"sd.owner_user = $6",
		"(sd.client_id = $7 OR sd.application_code = $8 OR s.client_type = $9)",
		"ss.user_tags && $10",
		// search now matches title/topic/intent/summary → 4 placeholders $11..$14,
		// cursor follows at $15/$16 (was $14/$15 before intent was added).
		"CASE WHEN tstate.tenant_id IS NOT NULL THEN COALESCE(tstate.title, '') ELSE COALESCE(NULLIF(s.title, ''), st.title, ss.title, '') END ILIKE '%'||$11||'%'",
		"COALESCE(NULLIF(s.summary, ''), ss.summary, '') ILIKE '%'||$14||'%'",
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
	if !strings.Contains(where, "EXISTS (SELECT 1 FROM public.session_turns_with_current_month ft") {
		t.Errorf("expected EXISTS subquery on session_turns_with_current_month: %s", where)
	}
	if strings.Contains(where, "JOIN public.sessions") {
		t.Errorf("turn-level EXISTS must not join public.sessions: %s", where)
	}
	if len(args) != 4 || nextArg != 5 {
		t.Fatalf("expected 4 args / nextArg=5, got %d args / nextArg=%d", len(args), nextArg)
	}
	if args[3] != 500 {
		t.Fatalf("status_code arg should be int 500, got %v (%T)", args[3], args[3])
	}
}

func TestLastActiveTurnQuery_UsesCurrentMonthView(t *testing.T) {
	query := (&Handler{}).lastActiveTurnQuery("t.model", "t.model IS NOT NULL", "")
	if !strings.Contains(query, "FROM public.session_turns_with_current_month t") {
		t.Fatalf("turn filter query must use current-month view: %s", query)
	}
	if strings.Contains(query, "FROM public.session_turns t") {
		t.Fatalf("turn filter query must not read the base table directly: %s", query)
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
	if !strings.Contains(where, "COALESCE(NULLIF(ss.gw_project_id, ''), sd.project_id) = $1") {

		t.Fatalf("project clause should be $1 when no time window: %s", where)
	}
	if !strings.Contains(where, "(s.updated_at, s.session_id) < ($2, $3)") {
		t.Fatalf("cursor should follow at $2,$3: %s", where)
	}
	if nextArg != 4 || len(args) != 3 {
		t.Fatalf("expected nextArg=4 args=3, got %d %d", nextArg, len(args))
	}
}

func TestBuildTurnsSessionWhere_APIKeyAndStatus(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/admin/turns/sessions?api_key_id=42&status=active", nil)
	where, args, nextArg := buildTurnsSessionWhere(req, "t1", time.Time{}, time.Time{}, time.Time{}, "", 1)
	if !strings.Contains(where, "rl.api_key_id = $2") {
		t.Fatalf("missing api_key_id EXISTS clause: %s", where)
	}
	if !strings.Contains(where, "s.status = $3") {
		t.Fatalf("missing status clause: %s", where)
	}
	if !strings.Contains(where, "request_logs_with_current_month") {
		t.Fatalf("api_key filter must join request_logs view: %s", where)
	}
	if len(args) != 3 || nextArg != 4 {
		t.Fatalf("expected 3 args / nextArg=4, got %d / %d", len(args), nextArg)
	}
	if args[1] != int64(42) {
		t.Fatalf("api_key_id arg want 42, got %v (%T)", args[1], args[1])
	}
	if args[2] != "active" {
		t.Fatalf("status arg want active, got %v", args[2])
	}
}

func TestLastActiveSource_WindowUsedByFilterOptions(t *testing.T) {
	src := (&Handler{}).lastActiveSource("sd.task_id", " JOIN session_dim sd ON true",
		"ss.first_request_at > NOW() - INTERVAL '30 days' AND sd.task_id != ''", "")
	if !strings.Contains(src, "INTERVAL '30 days'") {
		t.Fatalf("session filter options must constrain 30d window: %s", src)
	}
}

func TestBuildTurnsSessionWhere_UsesUpdatedAt(t *testing.T) {
	now := time.Now().UTC()
	where, _, _ := buildTurnsSessionWhere(
		httptest.NewRequest(http.MethodGet, "/api/admin/turns/sessions", nil),
		"", now.Add(-time.Hour), now, time.Time{}, "", 1)
	for _, c := range []string{"s.updated_at >= $1", "s.updated_at <= $2"} {
		if !strings.Contains(where, c) {
			t.Fatalf("where missing %q\nwhere=%s", c, where)
		}
	}
	if strings.Contains(where, "first_request_at") {
		t.Fatalf("time window must not use first_request_at: %s", where)
	}
}

func TestTurnsSessionsListSQL_SlimMainQuery(t *testing.T) {
	sql := turnsSessionsListSQL()
	if strings.Contains(sql, "request_logs_with_current_month") {
		t.Fatalf("main query must not join request_logs: %s", sql)
	}
	if strings.Contains(sql, "handoff_logs_with_current_month") {
		t.Fatalf("main query must not lateral join handoff_logs: %s", sql)
	}
	if !strings.Contains(sql, "session_title_states tstate") {
		t.Fatalf("main query must join tstate for search: %s", sql)
	}
	// 2026-08-26: 验证 session_analysis_metadata LATERAL join 已就位 —— 排序
	// 规则（status='final' 优先 + updated_at DESC）和投影列（status/schema_version/
	// input_hash/source_task_id/updated_at/payload）必须出现，避免后续重构悄悄
	// 退化到普通 LEFT JOIN 而导致一对多行重复或误读到旧 provisional。
	if !strings.Contains(sql, "LEFT JOIN LATERAL") {
		t.Fatalf("main query must use LATERAL join for session_analysis_metadata: %s", sql)
	}
	if !strings.Contains(sql, "session_analysis_metadata sam") {
		t.Fatalf("main query must alias session_analysis_metadata as sam: %s", sql)
	}
	if !strings.Contains(sql, "(sam.status = 'final') DESC") {
		t.Fatalf("main query must prefer status='final' over 'provisional': %s", sql)
	}
	if !strings.Contains(sql, "sam.payload") {
		t.Fatalf("main query must project sam.payload for SessionAnalysisView.Payload: %s", sql)
	}
}

func TestResolveTurnsSessionsTenant(t *testing.T) {
	cases := []struct {
		name  string
		auth  *AuthContext
		query string
		want  string
	}{
		{"tenant_admin scoped", &AuthContext{Role: "tenant_admin", TenantID: "t1"}, "", "t1"},
		{"super_admin all tenants", &AuthContext{Role: "super_admin", TenantID: "default"}, "", ""},
		{"super_admin explicit tenant", &AuthContext{Role: "super_admin", TenantID: "default"}, "?tenant=acme", "acme"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/admin/turns/sessions"+c.query, nil)
			req = SetAuthContext(req, c.auth)
			if got := resolveTurnsSessionsTenant(req); got != c.want {
				t.Fatalf("resolveTurnsSessionsTenant() = %q, want %q", got, c.want)
			}
		})
	}
}
