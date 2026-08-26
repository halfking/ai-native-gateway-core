package admin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/providerprofile"
	"github.com/kaixuan/llm-gateway-go/domains/requestjourney"
)

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
		})
	}
}
