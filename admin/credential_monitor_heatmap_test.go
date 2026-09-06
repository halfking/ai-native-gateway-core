package admin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
)

func heatmapMockRows() *pgxmock.Rows {
	avg1, p951 := 1772, 2070
	avg2, p952 := 1950, 2062
	return pgxmock.NewRows([]string{
		"credential_id", "label", "provider_name", "raw_model_name", "time_bucket",
		"total_requests", "success_count", "failed_count", "success_rate",
		"avg_latency_ms", "p95_latency_ms", "error_distribution", "sample_request_ids",
	}).
		AddRow(3, "default", "商汤", "deepseek-v4-flash",
			time.Date(2026, 9, 6, 2, 15, 0, 0, time.UTC), 12, 11, 1, 0.9167,
			&avg1, &p951, []byte(`{"transient": 1}`), []string{"req-1"}).
		AddRow(3, "default", "商汤", "deepseek-v4-flash",
			time.Date(2026, 9, 6, 2, 30, 0, 0, time.UTC), 4, 4, 0, 1.0,
			&avg2, &p952, []byte(`{}`), []string{}).
		AddRow(3, "default", "商汤", "gpt-4o",
			time.Date(2026, 9, 6, 2, 15, 0, 0, time.UTC), 2, 0, 2, 0.0,
			nil, nil, []byte(`{"auth_failed": 2}`), []string{"req-2", "req-3"})
}

// Regression guard for the SQLSTATE 42803 incident (2026-09-07): the heatmap
// SQL must not nest an aggregate inside jsonb_object_agg, must filter probe
// rows on request_logs columns (the historical is_self_test column does not
// exist and probe rows may lack request_context_attrs rows), and must use
// epoch-aligned buckets because date_trunc rejects '5 minutes'.
func TestBuildHeatmapSQL_GuardsAgainst42803Regression(t *testing.T) {
	query, args := buildHeatmapSQL(heatmapQueryParams{
		TimeStart:       time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC),
		TimeEnd:         time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC),
		BucketSeconds:   300,
		ExcludeSelfTest: true,
	})

	// Error distribution is two-level: per-error_kind COUNT first, folded by
	// jsonb_object_agg afterwards — never COUNT inside the aggregate.
	if strings.Contains(query, "jsonb_object_agg(") && strings.Contains(query, "COUNT(*) FILTER (WHERE NOT success AND error_kind") {
		t.Fatal("nested aggregate inside jsonb_object_agg would fail with SQLSTATE 42803")
	}
	if !strings.Contains(query, "jsonb_object_agg(error_kind, error_count)") {
		t.Fatal("error distribution must aggregate pre-counted error_counts CTE")
	}
	// Probe exclusion uses request_logs-native markers.
	if !strings.Contains(query, "task_type, '') <> 'probe_triggered'") || !strings.Contains(query, "'probe' = ANY(rl.quality_flags)") {
		t.Fatal("self-test exclusion must use task_type/quality_flags on request_logs")
	}
	if strings.Contains(query, "is_self_test") {
		t.Fatal("request_logs has no is_self_test column (SQLSTATE 42703)")
	}
	// Epoch-aligned bucketing, not date_trunc.
	if !strings.Contains(query, "to_timestamp(floor(EXTRACT(EPOCH FROM rl.ts) / 300) * 300)") {
		t.Fatal("buckets must be epoch-aligned; date_trunc cannot express 5-minute widths")
	}
	// Two bound args: the time window.
	if len(args) != 2 {
		t.Fatalf("expected 2 bound args for unfiltered query, got %d", len(args))
	}
}

func TestBuildHeatmapSQL_TenantAndFilterArgs(t *testing.T) {
	query, args := buildHeatmapSQL(heatmapQueryParams{
		TimeStart:     time.Now(),
		TimeEnd:       time.Now().Add(time.Hour),
		BucketSeconds: 60,
		TenantID:      "tenant-a",
		CredentialIDs: []int{3, 7},
		Models:        []string{"gpt-4o"},
	})
	if !strings.Contains(query, "c.tenant_id = $3") ||
		!strings.Contains(query, "rl.credential_id = ANY($4)") ||
		!strings.Contains(query, "= ANY($5)") {
		t.Fatalf("filter placeholders wrong: %s", query)
	}
	if len(args) != 5 {
		t.Fatalf("expected 5 bound args, got %d", len(args))
	}
}

func TestRunHeatmapQuery_GroupsRowsAndParsesDistribution(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	mock.ExpectQuery(`WITH base AS[\s\S]*request_logs_with_current_month[\s\S]*ORDER BY c\.id, bs\.raw_model_name, bs\.time_bucket`).
		WithArgs(time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC), time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)).
		WillReturnRows(heatmapMockRows())

	got, err := runHeatmapQuery(context.Background(), mock, heatmapQueryParams{
		TimeStart:     time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC),
		TimeEnd:       time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC),
		BucketSeconds: 300,
	})
	if err != nil {
		t.Fatalf("runHeatmapQuery: %v", err)
	}
	if len(got) != 1 || got[0].CredentialID != 3 || got[0].ProviderName != "商汤" {
		t.Fatalf("unexpected credential grouping: %+v", got)
	}
	if len(got[0].Models) != 2 {
		t.Fatalf("expected 2 model rows, got %d", len(got[0].Models))
	}
	flash := got[0].Models[0]
	if flash.RawModelName != "deepseek-v4-flash" || len(flash.Buckets) != 2 {
		t.Fatalf("unexpected first model: %+v", flash)
	}
	if flash.Buckets[0].Status != "ready" || flash.Buckets[0].ErrorDistribution["transient"] != 1 {
		t.Fatalf("bucket parse failed: %+v", flash.Buckets[0])
	}
	if len(flash.Buckets[0].SampleRequestIDs) != 1 || flash.Buckets[0].SampleRequestIDs[0] != "req-1" {
		t.Fatalf("sample ids not mapped: %+v", flash.Buckets[0].SampleRequestIDs)
	}
	if got[0].Models[1].Buckets[0].Status != "unreachable" {
		t.Fatalf("0%% success should be unreachable: %+v", got[0].Models[1].Buckets[0])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRunHeatmapQuery_PropagatesQueryError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	mock.ExpectQuery(`WITH base AS[\s\S]*`).
		WillReturnError(errors.New("aggregate function calls cannot be nested"))
	_, err = runHeatmapQuery(context.Background(), mock, heatmapQueryParams{
		TimeStart: time.Now(), TimeEnd: time.Now().Add(time.Hour), BucketSeconds: 60,
	})
	if err == nil || !strings.Contains(err.Error(), "query failed") {
		t.Fatalf("expected wrapped query error, got %v", err)
	}
}

func TestGranularitySeconds(t *testing.T) {
	cases := map[string]int{"1m": 60, "5m": 300, "15m": 900, "1h": 3600, "1d": 86400}
	for in, want := range cases {
		got, err := granularitySeconds(in)
		if err != nil || got != want {
			t.Fatalf("granularitySeconds(%s) = %d, %v; want %d", in, got, err, want)
		}
	}
	if _, err := granularitySeconds("7m"); err == nil {
		t.Fatal("expected error for invalid granularity")
	}
}

func TestHandleCredentialHeatmap_NilDB(t *testing.T) {
	m := &CredentialMonitorHandlers{h: &Handler{}}
	req := httptest.NewRequest(http.MethodGet, "/api/credentials/heatmap?time_start=2026-09-06T00:00:00Z&time_end=2026-09-07T00:00:00Z", nil)
	rr := httptest.NewRecorder()
	m.handleCredentialHeatmap(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d body=%s", rr.Code, rr.Body.String())
	}
}
