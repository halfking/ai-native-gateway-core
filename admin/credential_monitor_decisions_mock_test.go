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

func decisionMockRows() *pgxmock.Rows {
	return pgxmock.NewRows([]string{"ts", "request_id", "model", "tier", "success", "latency_ms", "error_class", "chosen_provider_id", "client_model", "outbound_model", "sticky_hit"}).AddRow(
		time.Date(2026, 8, 20, 12, 0, 0, 0, time.FixedZone("UTC+8", 8*60*60)), "request-1", "gpt-4o", 2, true, 123, nil, 7, "gpt-4o", "gpt-4o-2026", true)
}

func TestRunCredentialDecisions_BindsTenantModelAndMapsRow(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	mock.ExpectQuery(`SELECT[\s\S]*routing_decision_log[\s\S]*rdl\.tenant_id = \$2[\s\S]*lower\(rdl\.model\)[\s\S]*LIMIT \$4`).
		WithArgs(21, "tenant-a", "GPT-4O", 30).
		WillReturnRows(decisionMockRows())
	got, err := runCredentialDecisions(context.Background(), mock, credentialDecisionParams{
		CredentialID: 21, Limit: 30, IsTenantAdmin: true, TenantID: "tenant-a", ModelFilter: "GPT-4O",
	})
	if err != nil {
		t.Fatalf("runCredentialDecisions: %v", err)
	}
	if len(got) != 1 || got[0].TS != "2026-08-20T04:00:00Z" || got[0].Model != "gpt-4o" {
		t.Fatalf("unexpected decision mapping: %+v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRunCredentialDecisions_PropagatesQueryAndRowsErrors(t *testing.T) {
	params := credentialDecisionParams{CredentialID: 21, Limit: 30, IsTenantAdmin: true, TenantID: "tenant-a"}
	for _, tc := range []struct {
		name string
		rows *pgxmock.Rows
		err  error
	}{
		{name: "query", err: errors.New("query unavailable")},
		{name: "rows", rows: decisionMockRows().CloseError(errors.New("stream interrupted"))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mock, err := pgxmock.NewPool()
			if err != nil {
				t.Fatal(err)
			}
			defer mock.Close()
			expect := mock.ExpectQuery(`SELECT[\s\S]*routing_decision_log[\s\S]*`).WithArgs(21, "tenant-a", 30)
			if tc.err != nil {
				expect.WillReturnError(tc.err)
			} else {
				expect.WillReturnRows(tc.rows)
			}
			got, err := runCredentialDecisions(context.Background(), mock, params)
			if err == nil || !strings.Contains(err.Error(), "failed") {
				t.Fatalf("expected wrapped error, got %v", err)
			}
			if tc.rows != nil && len(got) != 1 {
				t.Fatalf("expected row before iteration failure, got %d", len(got))
			}
		})
	}
}

func TestHandleCredentialDecisions_NilDB(t *testing.T) {
	m := &CredentialMonitorHandlers{h: &Handler{}}
	req := httptest.NewRequest(http.MethodGet, "/api/credentials/decisions?credential_id=21", nil)
	rr := httptest.NewRecorder()
	m.handleCredentialDecisions(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d body=%s", rr.Code, rr.Body.String())
	}
}
