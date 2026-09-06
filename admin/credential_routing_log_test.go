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
	if !strings.Contains(query, "credential.model_toggle_online") {
		t.Fatal("manual toggles missing")
	}
	if strings.Contains(query, "FROM routing_decision_log") {
		t.Fatal("routing branch must be pruned for state_change kind")
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
	mock.ExpectQuery(`SELECT COUNT\(\*\) OVER\(\) AS total, u\.\*[\s\S]*UNION ALL[\s\S]*ORDER BY u\.ts DESC`).
		WithArgs(start, end, 100, 0).
		WillReturnRows(pgxmock.NewRows([]string{
			"total", "ts", "kind", "change", "model", "credential_id", "credential_label",
			"provider_name", "success", "status", "latency_ms", "error_code", "error_message",
			"request_id", "tier", "source", "actor",
		}).
			AddRow(2, ts, "state_change", &change, "claude-opus-5", &credID, "zhima-max", "智码",
				nil, "credential.model_toggle_offline", nil, nil, &errMsg, nil, nil, "manual", &actor).
			AddRow(2, ts, "probe", nil, "gpt-5.6-terra", &credID, "gpt", "联界",
				&probeSuccess, "network", &latency, &errCode, &errMsg, &reqID, &tier, "routing_4xx", nil))

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
	second := entries[1]
	if second.Success == nil || *second.Success {
		t.Fatalf("probe success mapping wrong: %+v", second)
	}
	if second.RequestID == nil || *second.RequestID != "req-abc" || second.Tier == nil || *second.Tier != 2 {
		t.Fatalf("probe request/tier mapping wrong: %+v", second)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
