package admin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
)

var decisionColumns = []string{
	"ts", "request_id", "model", "tier", "success", "latency_ms", "error_class",
	"chosen_provider_id", "client_model", "outbound_model", "sticky_hit",
}

func TestRunCredentialDecisions_BindsScopeAndMapsRows(t *testing.T) {
	t.Run("super admin", func(t *testing.T) {
		mock, err := pgxmock.NewPool()
		if err != nil {
			t.Fatal(err)
		}
		defer mock.Close()

		rows := pgxmock.NewRows(decisionColumns).AddRow(
			time.Date(2026, 8, 20, 12, 0, 0, 0, time.FixedZone("UTC+8", 8*60*60)),
			"request-1", "gpt-4o", 2, true, 123, "", 7, "client-gpt", "outbound-gpt", true,
		)
		mock.ExpectQuery(`FROM routing_decision_log_with_current_month rdl[\s\S]*LIMIT \$2`).
			WithArgs(21, 30).
			WillReturnRows(rows)

		got, err := runCredentialDecisions(context.Background(), mock, credentialDecisionParams{CredentialID: 21, Limit: 30})
		if err != nil {
			t.Fatalf("runCredentialDecisions: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("got %d decisions, want 1", len(got))
		}
		d := got[0]
		if d.TS != "2026-08-20T04:00:00Z" || d.RequestID != "request-1" || d.Model != "gpt-4o" || d.Tier == nil || *d.Tier != 2 || !d.Success || d.LatencyMs == nil || *d.LatencyMs != 123 || d.ChosenProviderID == nil || *d.ChosenProviderID != 7 || d.ClientModel == nil || *d.ClientModel != "client-gpt" || d.OutboundModel == nil || *d.OutboundModel != "outbound-gpt" || d.StickyHit == nil || !*d.StickyHit {
			t.Fatalf("unexpected decision mapping: %+v", d)
		}
		if d.ErrorClass == nil || *d.ErrorClass != "" {
			t.Fatalf("error class mapping drifted: %+v", d)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("tenant and model", func(t *testing.T) {
		mock, err := pgxmock.NewPool()
		if err != nil {
			t.Fatal(err)
		}
		defer mock.Close()

		mock.ExpectQuery(`FROM routing_decision_log_with_current_month rdl[\s\S]*rdl\.tenant_id = \$2[\s\S]*credentials c[\s\S]*c\.tenant_id = \$2[\s\S]*lower\(rdl\.model\)[\s\S]*LIMIT \$4`).
			WithArgs(21, "tenant-a", "GPT-4O", 30).
			WillReturnRows(pgxmock.NewRows(decisionColumns))

		got, err := runCredentialDecisions(context.Background(), mock, credentialDecisionParams{
			CredentialID: 21, Limit: 30, IsTenantAdmin: true, TenantID: "tenant-a", ModelFilter: "GPT-4O",
		})
		if err != nil {
			t.Fatalf("runCredentialDecisions: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("got %d decisions, want empty result", len(got))
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestRunCredentialDecisions_QueryAndIterationErrors(t *testing.T) {
	params := credentialDecisionParams{CredentialID: 21, Limit: 30, IsTenantAdmin: true, TenantID: "tenant-a"}

	t.Run("query error", func(t *testing.T) {
		mock, err := pgxmock.NewPool()
		if err != nil {
			t.Fatal(err)
		}
		defer mock.Close()
		mock.ExpectQuery(`FROM routing_decision_log_with_current_month`).WithArgs(21, "tenant-a", 30).
			WillReturnError(errors.New("query unavailable"))
		if _, err := runCredentialDecisions(context.Background(), mock, params); err == nil || !strings.Contains(err.Error(), "query failed") {
			t.Fatalf("expected wrapped query error, got %v", err)
		}
	})

	t.Run("rows iteration error", func(t *testing.T) {
		mock, err := pgxmock.NewPool()
		if err != nil {
			t.Fatal(err)
		}
		defer mock.Close()
		rows := pgxmock.NewRows(decisionColumns).AddRow(
			time.Date(2026, 8, 20, 12, 0, 0, 0, time.FixedZone("UTC+8", 8*60*60)),
			"request-1", "gpt-4o", 2, true, 123, "", 7, "client-gpt", "outbound-gpt", true,
		).AddRow(
			time.Now(), "request-2", "gpt-4o", nil, false, nil, nil, nil, nil, nil, nil,
		).RowError(1, errors.New("row scan failed")).CloseError(errors.New("stream interrupted"))
		mock.ExpectQuery(`FROM routing_decision_log_with_current_month`).WithArgs(21, "tenant-a", 30).
			WillReturnRows(rows)
		got, err := runCredentialDecisions(context.Background(), mock, params)
		if err == nil || !strings.Contains(err.Error(), "rows iteration failed") {
			t.Fatalf("expected wrapped iteration error, got %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("got %d decisions before iteration error, want 1", len(got))
		}
	})
}

func TestHandleCredentialDecisionsGuards(t *testing.T) {
	t.Run("nil db", func(t *testing.T) {
		m := &CredentialMonitorHandlers{h: &Handler{}}
		req := httptest.NewRequest(http.MethodGet, "/api/credentials/decisions?credential_id=21", nil)
		rr := httptest.NewRecorder()
		m.handleCredentialDecisions(rr, req)
		if rr.Code != http.StatusServiceUnavailable {
			t.Fatalf("status=%d body=%s, want 503", rr.Code, rr.Body.String())
		}
	})

	t.Run("missing credential id", func(t *testing.T) {
		m := &CredentialMonitorHandlers{h: &Handler{}}
		req := httptest.NewRequest(http.MethodGet, "/api/credentials/decisions", nil)
		rr := httptest.NewRecorder()
		m.handleCredentialDecisions(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("status=%d body=%s, want 400", rr.Code, rr.Body.String())
		}
	})

	t.Run("tenant admin cannot mutate credential", func(t *testing.T) {
		m := &CredentialMonitorHandlers{h: &Handler{}}
		for name, handler := range map[string]http.HandlerFunc{
			"promote":               m.handlePromote,
			"demote":                m.handleDemote,
			"set concurrency auto":  m.handleSetConcurrencyAuto,
			"model toggle":          m.handleModelToggle,
			"clear manual disabled": m.handleClearManualDisabled,
			"set manual disabled":   m.handleSetManualDisabled,
		} {
			t.Run(name, func(t *testing.T) {
				req := httptest.NewRequest(http.MethodPost, "/api/credentials/test", nil)
				req = SetAuthContext(req, &AuthContext{Role: "tenant_admin", TenantID: "tenant-a", IsJWT: true})
				rr := httptest.NewRecorder()
				handler(rr, req)
				if rr.Code != http.StatusForbidden {
					t.Fatalf("status=%d body=%s, want 403", rr.Code, rr.Body.String())
				}
			})
		}
	})
}

func testDBURL() string {
	return os.Getenv("TEST_DATABASE_URL")
}
