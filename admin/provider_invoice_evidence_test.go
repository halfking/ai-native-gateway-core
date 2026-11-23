package admin

import (
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/kaixuan/llm-gateway-go/domains/reconciliation"
)

func TestProviderInvoiceEvidenceScopeAndFilters(t *testing.T) {
	for _, tt := range []struct {
		role, method, query string
		status              int
	}{
		{"user", "GET", "?provider_id=1&month=2026-11", http.StatusForbidden},
		{"tenant_admin", "POST", "", http.StatusForbidden},
		{"tenant_admin", "GET", "?provider_id=1&day=2026-11-01", http.StatusForbidden},
		{"super_admin", "DELETE", "", http.StatusMethodNotAllowed},
		{"super_admin", "GET", "?provider_id=1&month=2026-2", http.StatusBadRequest},
		{"super_admin", "GET", "?invoice_id=bad", http.StatusBadRequest},
		{"super_admin", "GET", "?provider_id=1&month=2026-11&format=csv", http.StatusBadRequest},
		{"super_admin", "GET", "?provider_id=1&month=2026-11", http.StatusServiceUnavailable},
		{"super_admin", "GET", "?provider_id=1&day=2026-11-01", http.StatusServiceUnavailable},
		{"super_admin", "GET", "?day=2026-11-01", http.StatusServiceUnavailable},
		{"super_admin", "GET", "?month=2026-11", http.StatusBadRequest},
		{"super_admin", "GET", "?provider_id=none&day=2026-11-01", http.StatusBadRequest},
		{"super_admin", "GET", "?provider_id=1&day=2026-11-01&month=2026-11", http.StatusBadRequest},
		{"super_admin", "GET", "?provider_id=1&day=2026-11-01&timezone=Local", http.StatusBadRequest},
	} {
		req := httptest.NewRequest(tt.method, "/api/admin/provider-cost-reconciliation/invoices"+tt.query, nil)
		req = SetAuthContext(req, &AuthContext{Role: tt.role, UserID: 8})
		w := httptest.NewRecorder()
		(&Handler{}).handleProviderInvoices(w, req)
		if w.Code != tt.status {
			t.Errorf("%s %s %s: got %d want %d", tt.role, tt.method, tt.query, w.Code, tt.status)
		}
	}
}

func TestSupplierContractRateScopeAndFilters(t *testing.T) {
	for _, tt := range []struct {
		role, method, query string
		status              int
	}{
		{"tenant_admin", "GET", "?provider_id=1&credential_id=2&model=m", http.StatusForbidden},
		{"user", "POST", "", http.StatusForbidden},
		{"super_admin", "DELETE", "", http.StatusMethodNotAllowed},
		{"super_admin", "GET", "?provider_id=1&credential_id=2", http.StatusBadRequest},
		{"super_admin", "GET", "?provider_id=18446744073709551616&credential_id=2&model=m", http.StatusBadRequest},
		{"super_admin", "GET", "?provider_id=1&credential_id=2&model=m", http.StatusServiceUnavailable},
	} {
		req := httptest.NewRequest(tt.method, "/api/admin/provider-cost-reconciliation/rates"+tt.query, nil)
		req = SetAuthContext(req, &AuthContext{Role: tt.role, UserID: 8})
		w := httptest.NewRecorder()
		(&Handler{}).handleSupplierContractRates(w, req)
		if w.Code != tt.status {
			t.Errorf("%s %s %s: got %d want %d", tt.role, tt.method, tt.query, w.Code, tt.status)
		}
	}
}

func TestProviderInvoiceEvidenceCSVRetainsPrecisionAndProtectsText(t *testing.T) {
	id := uuid.New()
	w := httptest.NewRecorder()
	writeProviderInvoiceCSV(w, id, reconciliation.InvoiceReport{
		Limit: 1, Offset: 2, Lines: []json.RawMessage{json.RawMessage(`{"line_ref":"=label","invoice_amount":"-0.007000000000000001","invoice_currency":"USD","comparable_estimate":null,"bucket_generated_images":"0","bucket_observed_generated_images":null,"bucket_observed_video_output_duration_us":"9007199254740993"}`)},
	}, false)
	rows, err := csv.NewReader(strings.NewReader(w.Body.String())).ReadAll()
	if err != nil || len(rows) != 2 || rows[1][0] != "'=label" || rows[1][3] != "-0.007000000000000001" || rows[1][4] != "USD" || rows[1][16] != "" {
		t.Fatalf("CSV %q error=%v", w.Body.String(), err)
	}
	fields := make(map[string]string, len(rows[0]))
	for index, name := range rows[0] {
		fields[name] = rows[1][index]
	}
	if fields["bucket_generated_images"] != "0" || fields["bucket_observed_generated_images"] != "" ||
		fields["bucket_observed_video_output_duration_us"] != "9007199254740993" {
		t.Fatalf("media quantities did not retain zero, missing, and precise values: %v", fields)
	}
	if w.Header().Get("X-Invoice-ID") != id.String() || w.Header().Get("X-Report-Offset") != "2" {
		t.Fatalf("missing export metadata: %v", w.Header())
	}
}
