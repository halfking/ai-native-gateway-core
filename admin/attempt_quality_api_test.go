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
	// ★ 2026-10-20（890 真删轮）：读面断言**反转**。
	//
	// 原断言钉「读面必须是 `request_logs_with_current_month_without_request_class_due_at`」
	// —— 那张包装视图**直读 v1 族**（真库实测其定义体第一行就是 `FROM _request_logs_hot`），
	// 而 v1 族自 2026-10-13 停写后**恒 0 行** ⇒ 本端点此前**恒返回 0**（不报错）；
	// 890 删掉 v1 族后它会变成 **42P01**。
	//
	// ⇒ 读面改成**会话族原生源** db.SessionFamilyTurnsSourceSQL()
	// （hot ∪ parent + details 特征层 LEFT JOIN）⇒ 断言随之反转：
	//   · **负向**：不得再出现那两张 890 已 DROP 的包装视图；
	//   · **正向**：必须读到会话族的**两条腿**与 details 特征层
	//     （少一条腿 = 漏读一侧表，是这一族最贵的静默缺陷）。
	for _, forbidden := range []string{
		"request_logs_with_current_month_without_request_class_due_at",
		"request_logs_with_current_month_without_customer_id",
		"public._request_logs",
	} {
		if strings.Contains(query, forbidden) {
			t.Fatalf("final metrics query must not read the retired v1 relations (%q): %s", forbidden, query)
		}
	}
	//
	// ⚠ 两条腿必须用**各自的完整标记**断言，不能只写 "session_turns"：
	// "session_turns" 是 "session_turns_hot" 的**子串** ⇒ 只判子串时
	// 「只读 hot 一条腿」也能过 ⇒ **假绿**（实测：把源换成
	// db.SessionFamilyTurnsHotSourceSQL() 后，子串版断言仍绿）。
	for _, required := range []string{
		"FROM public.session_turns_hot t", // 热表腿
		"FROM public.session_turns t",     // 母表腿（漏它 = 漏读一侧表）
		"session_turn_details",            // 特征层（provider_id / model_chosen 的来源）
		"tenant_id = $1", "provider_id = $4", "credential_id = $5",
		"COALESCE(outbound_model, client_model) = $6",
	} {
		if !strings.Contains(query, required) {
			t.Fatalf("final metrics query missing %q: %s", required, query)
		}
	}
	if len(args) != 6 || args[0] != "tenant-a" || args[3] != int64(101) || args[4] != int64(11) || args[5] != "model-a" {
		t.Fatalf("final metrics args = %#v", args)
	}
}
