package admin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/kaixuan/llm-gateway-go/domains/providerprofile"
	"github.com/kaixuan/llm-gateway-go/domains/requestjourney"
)

type finalMetricsRow struct {
	metrics finalRequestMetrics
}

func (r finalMetricsRow) Scan(dest ...any) error {
	*dest[0].(*int64) = r.metrics.TotalRequests
	*dest[1].(*int64) = r.metrics.SuccessRequests
	*dest[2].(*int64) = r.metrics.FailureRequests
	*dest[3].(*int64) = r.metrics.TotalTokens
	return nil
}

type attemptQualityAnalyzerStub struct {
	tenantID   string
	start      time.Time
	end        time.Time
	aggregates []providerprofile.AttemptQualityAggregate
	err        error
}

func (s *attemptQualityAnalyzerStub) Analyze(_ context.Context, tenantID string, start, end time.Time) ([]providerprofile.AttemptQualityAggregate, error) {
	s.tenantID = tenantID
	s.start = start
	s.end = end
	return s.aggregates, s.err
}

func TestAttemptQualityAPIUsesAuthenticatedTenantAndAppliesFilters(t *testing.T) {
	analyzer := &attemptQualityAnalyzerStub{aggregates: []providerprofile.AttemptQualityAggregate{
		{ProviderID: 101, CredentialID: 11, Model: "model-a", AttemptTotal: 2},
		{ProviderID: 202, CredentialID: 22, Model: "model-b", AttemptTotal: 1},
	}}
	api := NewAttemptQualityAPI(analyzer)
	req := httptest.NewRequest(http.MethodGet, "/api/admin/quality/attempts?hours=12&provider_id=202&model=model-b", nil)
	req = SetAuthContext(req, &AuthContext{TenantID: "tenant-a", Role: "tenant_admin", IsJWT: true})
	w := httptest.NewRecorder()

	api.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	if analyzer.tenantID != "tenant-a" || analyzer.end.Sub(analyzer.start) != 12*time.Hour {
		t.Fatalf("analyzer scope = tenant=%q range=%s", analyzer.tenantID, analyzer.end.Sub(analyzer.start))
	}
	if !strings.Contains(w.Body.String(), `"provider_id":202`) || strings.Contains(w.Body.String(), `"provider_id":101`) {
		t.Fatalf("filtered response = %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"observation_status":"complete"`) {
		t.Fatalf("response does not report complete observation: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"final_request_metrics":{"total_requests":0,"success_requests":0,"failure_requests":0,"total_tokens":0,"success_rate":0}`) {
		t.Fatalf("response does not separate final request metrics: %s", w.Body.String())
	}
}

func TestAttemptQualityAPIReportsDegradedObservationStatus(t *testing.T) {
	analyzer := &attemptQualityAnalyzerStub{aggregates: []providerprofile.AttemptQualityAggregate{{
		ProviderID: 101, CredentialID: 11, Model: "model-a", AttemptTotal: 1, ObservationDegradedCount: 1,
	}}}
	req := httptest.NewRequest(http.MethodGet, "/api/admin/quality/attempts", nil)
	req = SetAuthContext(req, &AuthContext{TenantID: "tenant-a", Role: "tenant_admin", IsJWT: true})
	w := httptest.NewRecorder()

	NewAttemptQualityAPI(analyzer).ServeHTTP(w, req)

	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"observation_status":"`+string(requestjourney.ObservationDegraded)+`"`) {
		t.Fatalf("degraded response = status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestAttemptQualityAPIRejectsInvalidInputAndReportsAnalyzerFailure(t *testing.T) {
	tests := []struct {
		name       string
		url        string
		analyzer   *attemptQualityAnalyzerStub
		statusCode int
	}{
		{name: "missing tenant", url: "/api/admin/quality/attempts", analyzer: &attemptQualityAnalyzerStub{}, statusCode: http.StatusBadRequest},
		{name: "invalid hours", url: "/api/admin/quality/attempts?hours=0", analyzer: &attemptQualityAnalyzerStub{}, statusCode: http.StatusBadRequest},
		{name: "invalid provider", url: "/api/admin/quality/attempts?provider_id=zero", analyzer: &attemptQualityAnalyzerStub{}, statusCode: http.StatusBadRequest},
		{name: "analyzer failure", url: "/api/admin/quality/attempts", analyzer: &attemptQualityAnalyzerStub{err: errors.New("database unavailable")}, statusCode: http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.url, nil)
			if tt.name != "missing tenant" {
				req = SetAuthContext(req, &AuthContext{TenantID: "tenant-a", Role: "tenant_admin", IsJWT: true})
			}
			w := httptest.NewRecorder()
			NewAttemptQualityAPI(tt.analyzer).ServeHTTP(w, req)
			if w.Code != tt.statusCode {
				t.Fatalf("status = %d, want %d, body=%s", w.Code, tt.statusCode, w.Body.String())
			}
			if tt.name == "analyzer failure" && strings.Contains(w.Body.String(), "database unavailable") {
				t.Fatal("internal analyzer error leaked to client")
			}
		})
	}
}

func TestLoadFinalRequestMetricsScopesTenantAndFilters(t *testing.T) {
	var query string
	var args []any
	queryRow := func(_ context.Context, gotQuery string, gotArgs ...any) pgx.Row {
		query = gotQuery
		args = gotArgs
		return finalMetricsRow{metrics: finalRequestMetrics{TotalRequests: 4, SuccessRequests: 3, FailureRequests: 1, TotalTokens: 99}}
	}
	start := time.Unix(1_700_000_000, 0).UTC()
	got, err := loadFinalRequestMetrics(context.Background(), queryRow, "tenant-a", start, start.Add(time.Hour), 101, 11, "model-a")
	if err != nil || got.SuccessRate != 0.75 || got.TotalTokens != 99 {
		t.Fatalf("loadFinalRequestMetrics() = (%+v, %v)", got, err)
	}
	for _, required := range []string{"FROM request_logs_hot", "tenant_id = $1", "provider_id = $4", "credential_id = $5", "COALESCE(outbound_model, client_model) = $6"} {
		if !strings.Contains(query, required) {
			t.Fatalf("final metrics query missing %q: %s", required, query)
		}
	}
	if len(args) != 6 || args[0] != "tenant-a" || args[3] != int64(101) || args[4] != int64(11) || args[5] != "model-a" {
		t.Fatalf("final metrics args = %#v", args)
	}
}
