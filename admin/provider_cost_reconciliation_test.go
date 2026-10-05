package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/providerprofile"
)

// fakeCostReconciliationService 内存版 CostReconciliationService。
type fakeCostReconciliationService struct {
	imported []providerprofile.ProviderBill
	records  []providerprofile.ReconciliationRecord
}

func (s *fakeCostReconciliationService) ImportBill(ctx context.Context, bill providerprofile.ProviderBill) (*providerprofile.ReconciliationRecord, error) {
	s.imported = append(s.imported, bill)
	rec := &providerprofile.ReconciliationRecord{
		ProviderID:          bill.ProviderID,
		Month:               bill.Month,
		ProviderTotalCost:   bill.TotalCost,
		ProviderTotalTokens: bill.TotalTokens,
		CostDiffRate:        0.2,
		TokenDiffRate:       0,
		DataSource:          bill.DataSource,
	}
	return rec, nil
}

func (s *fakeCostReconciliationService) ListByMonth(ctx context.Context, month time.Time) ([]providerprofile.ReconciliationRecord, error) {
	return s.records, nil
}

func newCostReconTestServer(t *testing.T, svc *fakeCostReconciliationService) *httptest.Server {
	t.Helper()
	h := NewProviderCostReconciliationHandler(svc)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux, nil)
	return httptest.NewServer(mux)
}

// TestProviderCostReconciliationHandler_ImportBill 验证账单导入端点：
// 合法请求透传字段并返回记录；非法请求返回 4xx。
func TestProviderCostReconciliationHandler_ImportBill(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantStatus int
	}{
		{name: "合法导入", body: `{"provider_id":42,"month":"2026-07","input_tokens":500,"output_tokens":500,"total_tokens":1000,"total_cost":100.5,"notes":"7月账单"}`, wantStatus: http.StatusOK},
		{name: "缺少provider_id", body: `{"month":"2026-07","total_cost":100}`, wantStatus: http.StatusBadRequest},
		{name: "月份格式错误", body: `{"provider_id":42,"month":"2026/07","total_cost":100}`, wantStatus: http.StatusBadRequest},
		{name: "非法JSON", body: `{`, wantStatus: http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &fakeCostReconciliationService{}
			ts := newCostReconTestServer(t, svc)
			defer ts.Close()

			resp, err := http.Post(ts.URL+"/api/admin/provider-cost-reconciliation/bill", "application/json", strings.NewReader(tt.body))
			if err != nil {
				t.Fatalf("post: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tt.wantStatus {
				t.Errorf("status = %d, want %d", resp.StatusCode, tt.wantStatus)
			}
			if tt.wantStatus == http.StatusOK && len(svc.imported) != 1 {
				t.Errorf("ImportBill calls = %d, want 1", len(svc.imported))
			}
		})
	}
}

// TestProviderCostReconciliationHandler_ImportBillFields 验证导入字段
// 被正确解析（provider_id/month/tokens/cost），data_source 固定 manual。
func TestProviderCostReconciliationHandler_ImportBillFields(t *testing.T) {
	svc := &fakeCostReconciliationService{}
	ts := newCostReconTestServer(t, svc)
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/admin/provider-cost-reconciliation/bill", "application/json",
		strings.NewReader(`{"provider_id":42,"month":"2026-07","input_tokens":500,"output_tokens":500,"total_tokens":1000,"total_cost":100.5,"notes":"7月账单"}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if len(svc.imported) != 1 {
		t.Fatalf("ImportBill calls = %d, want 1", len(svc.imported))
	}
	got := svc.imported[0]
	if got.ProviderID != 42 {
		t.Errorf("ProviderID = %d, want 42", got.ProviderID)
	}
	wantMonth := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	if !got.Month.Equal(wantMonth) {
		t.Errorf("Month = %v, want %v", got.Month, wantMonth)
	}
	if got.TotalTokens != 1000 || got.TotalCost != 100.5 || got.InputTokens != 500 || got.OutputTokens != 500 {
		t.Errorf("bill fields = %+v", got)
	}
	if got.DataSource != "manual" {
		t.Errorf("DataSource = %q, want manual", got.DataSource)
	}
	if got.Notes != "7月账单" {
		t.Errorf("Notes = %q, want 7月账单", got.Notes)
	}

	// 响应体携带 diff 率（“月度对账 diff 可查”的最小载体）。
	var payload struct {
		CostDiffRate  float64 `json:"cost_diff_rate"`
		TokenDiffRate float64 `json:"token_diff_rate"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.CostDiffRate != 0.2 {
		t.Errorf("cost_diff_rate = %v, want 0.2", payload.CostDiffRate)
	}
}

// TestProviderCostReconciliationHandler_List 验证按月查询对账记录端点。
func TestProviderCostReconciliationHandler_List(t *testing.T) {
	month := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	svc := &fakeCostReconciliationService{records: []providerprofile.ReconciliationRecord{
		{ProviderID: 1, Month: month, GatewayTotalCost: 100, ProviderTotalCost: 95, CostDiffRate: 0.0526},
	}}
	ts := newCostReconTestServer(t, svc)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/admin/provider-cost-reconciliation?month=2026-07")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var payload struct {
		Items []providerprofile.ReconciliationRecord `json:"items"`
		Count int                                    `json:"count"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Count != 1 || len(payload.Items) != 1 {
		t.Fatalf("items = %+v, count = %d", payload.Items, payload.Count)
	}
	if payload.Items[0].ProviderID != 1 || payload.Items[0].CostDiffRate != 0.0526 {
		t.Errorf("item = %+v", payload.Items[0])
	}

	// 缺 month 参数：默认当前月（不报错）。
	resp2, err := http.Get(ts.URL + "/api/admin/provider-cost-reconciliation")
	if err != nil {
		t.Fatalf("get (default month): %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("status (default month) = %d, want 200", resp2.StatusCode)
	}
}
