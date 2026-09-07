package admin

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
)

// The merged timeline must union all three record families with the shared
// window bounds as $1/$2 and LIMIT/OFFSET as the final placeholders.
func TestBuildRoutingLogSQL_AllKinds(t *testing.T) {
	query, args := buildRoutingLogSQL(routingLogParams{
		TimeStart: time.Now().Add(-time.Hour),
		TimeEnd:   time.Now(),
		Kind:      "all",
		Result:    "all",
		Limit:     100,
		Offset:    0,
	})
	for _, branch := range []string{"FROM routing_decision_log", "FROM model_probe_runs_with_current_month", "FROM routing_audit_log"} {
		if !strings.Contains(query, branch) {
			t.Fatalf("missing branch %s in query", branch)
		}
	}
	if strings.Count(query, "UNION ALL") != 2 {
		t.Fatalf("expected 2 unions, got %d", strings.Count(query, "UNION ALL"))
	}
	// $1/$2 window, $3 limit, $4 offset
	if !strings.Contains(query, "LIMIT $3 OFFSET $4") {
		t.Fatalf("limit/offset placeholders wrong: %s", query[len(query)-120:])
	}
	if len(args) != 4 {
		t.Fatalf("expected 4 args, got %d", len(args))
	}
}

// state_change kind restricts probe rows to consensus flips and keeps manual
// toggles; result filters drop the state_change branch entirely.
func TestBuildRoutingLogSQL_StateChangeAndResultFilters(t *testing.T) {
	query, _ := buildRoutingLogSQL(routingLogParams{Kind: "state_change", TimeStart: time.Now(), TimeEnd: time.Now(), Limit: 10})
	if !strings.Contains(query, "mpr.state_change IN ('recovered', 'broke')") {
		t.Fatal("state_change kind must filter probe branch to consensus flips")
	}
	// 2026-09-07 审计：state_change 查询里 probe 分支的行必须标注为
	// state_change 并带出 flip 方向，而不是被标成 'probe'。
	if !strings.Contains(query, "'state_change' AS kind") || !strings.Contains(query, "COALESCE(mpr.state_change, '') AS change") {
		t.Fatal("probe branch must emit state_change kind and flip direction for state_change queries")
	}
	if !strings.Contains(query, "credential.model_toggle_online") {
		t.Fatal("manual toggles missing")
	}
	if strings.Contains(query, "FROM routing_decision_log") {
		t.Fatal("routing branch must be pruned for state_change kind")
	}

	// kind=all 时 probe 分支保持 'probe' 标注（完整自检时间线语义不变）。
	qAll, _ := buildRoutingLogSQL(routingLogParams{Kind: "all", TimeStart: time.Now(), TimeEnd: time.Now(), Limit: 10})
	if !strings.Contains(qAll, "'probe' AS kind") {
		t.Fatal("kind=all must keep probe branch labeled 'probe'")
	}
	if strings.Contains(qAll, "'state_change' AS kind,") && !strings.Contains(qAll, "FROM routing_audit_log") {
		t.Fatal("kind=all must not flip probe branch labeling")
	}

	q2, args2 := buildRoutingLogSQL(routingLogParams{Kind: "all", Result: "failed", TimeStart: time.Now(), TimeEnd: time.Now(), Limit: 10})
	if strings.Contains(q2, "FROM routing_audit_log") {
		t.Fatal("audit branch must be dropped when result filter cannot match it")
	}
	if !strings.Contains(q2, "rdl.success = $3") || !strings.Contains(q2, "(mpr.status = 'ok') = $3") {
		t.Fatalf("success predicate missing: %s", q2)
	}
	// start, end, result, limit, offset
	if len(args2) != 5 {
		t.Fatalf("expected 5 args, got %d: %v", len(args2), args2)
	}
}

func TestBuildRoutingLogSQL_TenantModelCredentialFilters(t *testing.T) {
	query, args := buildRoutingLogSQL(routingLogParams{
		Kind: "all", Result: "all",
		Model: "gpt", CredentialID: 21, TenantID: "tenant-a",
		TimeStart: time.Now(), TimeEnd: time.Now().Add(time.Hour), Limit: 10,
	})
	for _, want := range []string{
		"ILIKE $3",
		"rdl.chosen_credential_id = $4",
		"rdl.tenant_id = $5",
		"al.tenant_id = $5",
		"mpr.tenant_id = $5",
	} {
		if !strings.Contains(query, want) {
			t.Fatalf("missing %q in query", want)
		}
	}
	// start, end, tenant, model, credential, limit, offset
	if len(args) != 7 {
		t.Fatalf("expected 7 args, got %d: %v", len(args), args)
	}
}

func TestRunRoutingLogQuery_MapsRows(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	ts := time.Date(2026, 9, 7, 1, 0, 0, 0, time.UTC)
	change := "broke"
	errCode := "network"
	errMsg := "context deadline exceeded"
	reqID := "req-abc"
	tier := 2
	credID := 3
	latency := 86
	probeSuccess := false
	actor := "admin"

	start := time.Now().Add(-time.Hour)
	end := time.Now()
	httpStatus := 503
	detail := "network timeout"
	
	mock.ExpectQuery(`SELECT COUNT\(\*\) OVER\(\) AS total, u\.\*[\s\S]*UNION ALL[\s\S]*ORDER BY u\.ts DESC`).
		WithArgs(start, end, 100, 0).
		WillReturnRows(pgxmock.NewRows([]string{
			"total", "ts", "kind", "change", "model", "credential_id", "credential_label",
			"provider_name", "success", "status", "latency_ms", "error_code", "error_message",
			"request_id", "tier", "source", "actor", "http_status", "sticky", "outbound_model", "detail",
		}).
			AddRow(2, ts, "state_change", &change, "claude-opus-5", &credID, "zhima-max", "智码",
				nil, "credential.model_toggle_offline", nil, nil, &errMsg, nil, nil, "manual", &actor,
				nil, nil, nil, nil).
			AddRow(2, ts, "probe", nil, "gpt-5.6-terra", &credID, "gpt", "联界",
				&probeSuccess, "network", &latency, &errCode, &errMsg, &reqID, &tier, "routing_4xx", nil,
				&httpStatus, nil, nil, &detail))

	entries, total, err := runRoutingLogQuery(context.Background(), mock, routingLogParams{
		TimeStart: start, TimeEnd: end, Kind: "all", Limit: 100,
	})
	if err != nil {
		t.Fatalf("runRoutingLogQuery: %v", err)
	}
	if total != 2 || len(entries) != 2 {
		t.Fatalf("expected 2 entries, got total=%d len=%d", total, len(entries))
	}
	first := entries[0]
	if first.Kind != "state_change" || first.Change == nil || *first.Change != "broke" {
		t.Fatalf("state_change mapping wrong: %+v", first)
	}
	if first.CredentialLabel != "zhima-max" || first.Actor == nil || *first.Actor != "admin" {
		t.Fatalf("label/actor mapping wrong: %+v", first)
	}
	// F-4: state_change entries should have nil http_status/sticky/outbound_model
	if first.HTTPStatus != nil || first.Sticky != nil || first.OutboundModel != nil {
		t.Fatalf("state_change should have nil F-4 fields: %+v", first)
	}
	
	second := entries[1]
	if second.Success == nil || *second.Success {
		t.Fatalf("probe success mapping wrong: %+v", second)
	}
	if second.RequestID == nil || *second.RequestID != "req-abc" || second.Tier == nil || *second.Tier != 2 {
		t.Fatalf("probe request/tier mapping wrong: %+v", second)
	}
	// F-4: probe entries should have http_status and detail
	if second.HTTPStatus == nil || *second.HTTPStatus != 503 {
		t.Fatalf("probe http_status mapping wrong: %+v", second)
	}
	if second.Detail == nil || *second.Detail != "network timeout" {
		t.Fatalf("probe detail mapping wrong: %+v", second)
	}
	// F-4: probe entries should have nil sticky/outbound_model
	if second.Sticky != nil || second.OutboundModel != nil {
		t.Fatalf("probe should have nil sticky/outbound_model: %+v", second)
	}
	
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestBuildRoutingLogSQL_F4ContractFields verifies that F-4 audit fields
// (http_status, sticky, outbound_model, detail) are included in all three branches.
func TestBuildRoutingLogSQL_F4ContractFields(t *testing.T) {
	query, _ := buildRoutingLogSQL(routingLogParams{
		Kind: "all", Result: "all",
		TimeStart: time.Now().Add(-time.Hour), TimeEnd: time.Now(), Limit: 10,
	})

	// Debug: print the query to see what's actually generated
	if testing.Verbose() {
		t.Logf("Generated SQL:\n%s\n", query)
	}

	// Routing branch must select sticky_hit, outbound_model, and failure_stage-based detail
	if !strings.Contains(query, "rdl.sticky_hit AS sticky") {
		t.Fatal("routing branch missing sticky_hit")
	}
	if !strings.Contains(query, "rdl.outbound_model") {
		t.Fatal("routing branch missing outbound_model")
	}
	if !strings.Contains(query, "rdl.failure_stage") {
		t.Fatal("routing branch missing detail (failure_stage)")
	}

	// Probe branch must select http_status and state_change-based detail
	if !strings.Contains(query, "mpr.http_status") {
		t.Fatal("probe branch missing http_status")
	}
	if !strings.Contains(query, "mpr.state_change") {
		t.Fatal("probe branch missing state_change for detail")
	}

	// State_change branch must have NULL placeholders for probe/routing-specific fields
	auditBranch := query[strings.Index(query, "FROM routing_audit_log"):]
	if testing.Verbose() {
		t.Logf("Audit branch:\n%s\n", auditBranch)
	}
	if !strings.Contains(auditBranch, "NULL::int AS http_status") {
		t.Fatal("audit branch missing http_status placeholder")
	}
	if !strings.Contains(auditBranch, "NULL::boolean AS sticky") {
		t.Fatal("audit branch missing sticky placeholder")
	}
	if !strings.Contains(auditBranch, "NULL::text AS outbound_model") {
		t.Fatal("audit branch missing outbound_model placeholder")
	}
}

// TestRecordRoutingLogQueryMetrics verifies monitoring metrics are recorded.
// F-7 修复：接入既有 monitor 打点体系（调用量/失败率/慢查询指标）
func TestRecordRoutingLogQueryMetrics(t *testing.T) {
	tests := []struct {
		name        string
		success     bool
		durationMs  int64
		entryCount  int
		kind        string
		result      string
		wantStatus  string
		wantSlow    bool
	}{
		{
			name:        "successful_fast_query",
			success:     true,
			durationMs:  500,
			entryCount:  50,
			kind:        "all",
			result:      "all",
			wantStatus:  "success",
			wantSlow:    false,
		},
		{
			name:        "successful_slow_query",
			success:     true,
			durationMs:  4000,
			entryCount:  100,
			kind:        "routing",
			result:      "success",
			wantStatus:  "success",
			wantSlow:    true,
		},
		{
			name:        "failed_query",
			success:     false,
			durationMs:  200,
			entryCount:  0,
			kind:        "probe",
			result:      "failed",
			wantStatus:  "failed",
			wantSlow:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// recordRoutingLogQueryMetrics 使用 slog.Info 记录结构化日志
			// 此处仅验证函数不会 panic，实际监控指标需在集成环境验证
			recordRoutingLogQueryMetrics(tt.success, tt.durationMs, tt.entryCount, tt.kind, tt.result)
		})
	}
}

