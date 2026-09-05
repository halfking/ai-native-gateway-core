package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
)

func TestParseVendorErrorHours(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want int
		err  bool
	}{
		{name: "default", want: 24},
		{name: "one hour", raw: "1", want: 1},
		{name: "one day", raw: "24", want: 24},
		{name: "seven days", raw: "168", want: 168},
		{name: "invalid text", raw: "abc", err: true},
		{name: "unsupported window", raw: "48", err: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseVendorErrorHours(tt.raw)
			if tt.err {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("parseVendorErrorHours() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("parseVendorErrorHours() = %d, want %d", got, tt.want)
			}
		})
	}
}

var vendorCredentialMetaColumns = []string{
	"id", "label", "provider_id", "health_status", "health_error", "health_latency_ms",
	"availability_state", "state_reason_code", "state_reason_detail", "state_updated_at",
	"quota_state", "lifecycle_status", "circuit_state", "consecutive_failures",
	"manual_disabled", "balance_usd", "balance_currency",
}

func vendorCredentialMetaRows() *pgxmock.Rows {
	updated := time.Date(2026, 8, 29, 10, 11, 12, 0, time.UTC)
	healthError := "rate limited"
	reasonCode := "auto_cool"
	reasonDetail := "too many failures"
	balanceCurrency := "USD"
	return pgxmock.NewRows(vendorCredentialMetaColumns).AddRow(
		int64(42), "credential-a", int64(7), "warning", &healthError, nil,
		"rate_limited", &reasonCode, &reasonDetail, &updated, "ok", "active", "open", 5,
		false, nil, &balanceCurrency,
	)
}

func vendorSummaryRows(closeErr error) *pgxmock.Rows {
	lastSeen := time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC)
	rows := pgxmock.NewRows([]string{"error_kind", "count", "last_seen", "distinct_status_codes"}).
		AddRow("rate_limit", 3, lastSeen, 1)
	if closeErr != nil {
		rows.CloseError(closeErr)
	}
	return rows
}

// 2026-09-05 审计闭环1：recent failures 行形状 = supplier_errors_unified
// 13 列（末列 upstream_response_preview 来自 candidate_failure_logs_unified
// LEFT JOIN 回补）。
func vendorRecentRows(closeErr error) *pgxmock.Rows {
	ts := time.Date(2026, 8, 29, 9, 0, 0, 0, time.UTC)
	message := "upstream failed"
	preview := "{\"error\":\"busy\"}"
	stage := "upstream"
	supplier := "zhipu"
	errorCode := "1210"
	retryable := true
	rows := pgxmock.NewRows([]string{
		"occurred_at", "request_id", "model", "attempt_seq", "error_type", "error_message",
		"http_status", "is_retryable", "stage", "supplier", "error_code", "latency_ms",
		"upstream_response_preview",
	}).AddRow(ts, "req-1", "model-a", 1, "transient", &message, nil, &retryable, &stage, &supplier, &errorCode, nil, &preview)
	if closeErr != nil {
		rows.CloseError(closeErr)
	}
	return rows
}

func vendorQualityRows(closeErr error) *pgxmock.Rows {
	profileDate := time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC)
	rows := pgxmock.NewRows([]string{"profile_date", "total_score", "availability_score", "stability_score"}).
		AddRow(profileDate, 78.5, 80.0, 75.0)
	if closeErr != nil {
		rows.CloseError(closeErr)
	}
	return rows
}

func newVendorErrorRequest(query string, auth *AuthContext) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/api/vendors/credentials/42/error-detail"+query, nil)
	req.SetPathValue("id", "42")
	return SetAuthContext(req, auth)
}

func TestGetVendorCredentialErrorDetail200AndScope(t *testing.T) {
	tests := []struct {
		name     string
		auth     *AuthContext
		tenantID string
	}{
		{name: "tenant admin", auth: &AuthContext{TenantID: "tenant-a", Role: "tenant_admin"}, tenantID: "tenant-a"},
		{name: "superadmin", auth: &AuthContext{TenantID: "ignored", Role: "super_admin"}, tenantID: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock, err := pgxmock.NewPool()
			if err != nil {
				t.Fatal(err)
			}
			defer mock.Close()
			mock.ExpectQuery("SELECT id, label, provider_id, health_status").WithArgs(int64(42), tt.tenantID).WillReturnRows(vendorCredentialMetaRows())
			mock.ExpectQuery("SELECT error_type, COUNT").WithArgs(int64(42), tt.tenantID, pgxmock.AnyArg()).WillReturnRows(vendorSummaryRows(nil))
			mock.ExpectQuery("SELECT u.occurred_at, u.request_id").WithArgs(int64(42), tt.tenantID, pgxmock.AnyArg()).WillReturnRows(vendorRecentRows(nil))
			mock.ExpectQuery("SELECT p.profile_date, p.total_score").WithArgs(int64(42), tt.tenantID).WillReturnRows(vendorQualityRows(nil))

			rec := httptest.NewRecorder()
			(&vendorCredentialErrorHandlers{db: mock}).getVendorCredentialErrorDetail(rec, newVendorErrorRequest("?hours=1", tt.auth))
			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			var body struct {
				CredentialID    int    `json:"credential_id"`
				Hours           int    `json:"hours"`
				CredentialLabel string `json:"credential_label"`
				Quality         []struct {
					ProfileDate string `json:"profile_date"`
				} `json:"quality_scores_7d"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.CredentialID != 42 || body.Hours != 1 || body.CredentialLabel != "credential-a" {
				t.Fatalf("unexpected body: %+v", body)
			}
			if len(body.Quality) != 1 || body.Quality[0].ProfileDate != "2026-08-28" {
				t.Fatalf("quality date=%q", body.Quality[0].ProfileDate)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestGetVendorCredentialErrorDetailValidationAndNotFound(t *testing.T) {
	tests := []struct {
		name, path string
		wantStatus int
		wantCode   string
		setup      func(pgxmock.PgxPoolIface)
	}{
		{name: "invalid id", path: "/api/vendors/credentials/nope/error-detail", wantStatus: http.StatusBadRequest, wantCode: "invalid_credential_id"},
		{name: "invalid hours", path: "/api/vendors/credentials/42/error-detail?hours=2", wantStatus: http.StatusBadRequest, wantCode: "invalid_hours"},
		{name: "not found", path: "/api/vendors/credentials/42/error-detail", wantStatus: http.StatusNotFound, wantCode: "credential_not_found", setup: func(mock pgxmock.PgxPoolIface) {
			mock.ExpectQuery("SELECT id, label, provider_id, health_status").WithArgs(int64(42), "").WillReturnError(pgx.ErrNoRows)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock, err := pgxmock.NewPool()
			if err != nil {
				t.Fatal(err)
			}
			defer mock.Close()
			if tt.setup != nil {
				tt.setup(mock)
			}
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			id := "42"
			if strings.Contains(tt.name, "invalid id") {
				id = "nope"
			}
			req.SetPathValue("id", id)
			req = SetAuthContext(req, &AuthContext{Role: "super_admin"})
			rec := httptest.NewRecorder()
			(&vendorCredentialErrorHandlers{db: mock}).getVendorCredentialErrorDetail(rec, req)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			var body struct {
				Error struct{ Code, Detail, Message, TraceID string } `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Error.Code != tt.wantCode || body.Error.Detail == "" {
				t.Fatalf("error envelope=%+v", body.Error)
			}
			if body.Error.Message != "" || body.Error.TraceID != "" {
				t.Fatalf("breaking envelope fields: %+v", body.Error)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestGetVendorCredentialErrorDetailDBNotConfigured503(t *testing.T) {
	rec := httptest.NewRecorder()
	req := newVendorErrorRequest("", &AuthContext{Role: "super_admin"})
	(&vendorCredentialErrorHandlers{}).getVendorCredentialErrorDetail(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"code":"db_not_configured"`) {
		t.Fatalf("body=%s", rec.Body.String())
	}
}

func TestGetVendorCredentialErrorDetailQueryScanAndRowsErrors(t *testing.T) {
	tests := []struct {
		name      string
		configure func(pgxmock.PgxPoolIface)
		wantCode  string
	}{
		{name: "metadata query", configure: func(m pgxmock.PgxPoolIface) {
			m.ExpectQuery("SELECT id, label, provider_id, health_status").WithArgs(int64(42), "").WillReturnError(errors.New("query failed"))
		}, wantCode: "credential_query_failed"},
		{name: "metadata scan", configure: func(m pgxmock.PgxPoolIface) {
			m.ExpectQuery("SELECT id, label, provider_id, health_status").WithArgs(int64(42), "").WillReturnRows(pgxmock.NewRows(vendorCredentialMetaColumns).AddRow("bad", "credential-a", int64(7), "warning", nil, nil, "ready", nil, nil, nil, "ok", "active", "closed", 0, false, nil, nil))
		}, wantCode: "credential_query_failed"},
		{name: "summary query", configure: func(m pgxmock.PgxPoolIface) {
			m.ExpectQuery("SELECT id, label, provider_id, health_status").WithArgs(int64(42), "").WillReturnRows(vendorCredentialMetaRows())
			m.ExpectQuery("SELECT error_type, COUNT").WithArgs(int64(42), "", pgxmock.AnyArg()).WillReturnError(errors.New("query failed"))
		}, wantCode: "error_summary_query_failed"},
		{name: "summary scan", configure: func(m pgxmock.PgxPoolIface) {
			m.ExpectQuery("SELECT id, label, provider_id, health_status").WithArgs(int64(42), "").WillReturnRows(vendorCredentialMetaRows())
			m.ExpectQuery("SELECT error_type, COUNT").WithArgs(int64(42), "", pgxmock.AnyArg()).WillReturnRows(pgxmock.NewRows([]string{"error_kind", "count", "last_seen", "distinct_status_codes"}).AddRow("bad", "not-int", time.Now(), 1))
		}, wantCode: "error_summary_query_failed"},
		{name: "summary rows", configure: func(m pgxmock.PgxPoolIface) {
			m.ExpectQuery("SELECT id, label, provider_id, health_status").WithArgs(int64(42), "").WillReturnRows(vendorCredentialMetaRows())
			m.ExpectQuery("SELECT error_type, COUNT").WithArgs(int64(42), "", pgxmock.AnyArg()).WillReturnRows(vendorSummaryRows(errors.New("rows failed")))
		}, wantCode: "error_summary_query_failed"},
		{name: "recent query", configure: func(m pgxmock.PgxPoolIface) {
			m.ExpectQuery("SELECT id, label, provider_id, health_status").WithArgs(int64(42), "").WillReturnRows(vendorCredentialMetaRows())
			m.ExpectQuery("SELECT error_type, COUNT").WithArgs(int64(42), "", pgxmock.AnyArg()).WillReturnRows(vendorSummaryRows(nil))
			m.ExpectQuery("SELECT u.occurred_at, u.request_id").WithArgs(int64(42), "", pgxmock.AnyArg()).WillReturnError(errors.New("query failed"))
		}, wantCode: "recent_failures_query_failed"},
		{name: "recent scan", configure: func(m pgxmock.PgxPoolIface) {
			m.ExpectQuery("SELECT id, label, provider_id, health_status").WithArgs(int64(42), "").WillReturnRows(vendorCredentialMetaRows())
			m.ExpectQuery("SELECT error_type, COUNT").WithArgs(int64(42), "", pgxmock.AnyArg()).WillReturnRows(vendorSummaryRows(nil))
			m.ExpectQuery("SELECT u.occurred_at, u.request_id").WithArgs(int64(42), "", pgxmock.AnyArg()).WillReturnRows(pgxmock.NewRows([]string{"occurred_at", "request_id", "model", "attempt_seq", "error_type", "error_message", "http_status", "is_retryable", "stage", "supplier", "error_code", "latency_ms", "upstream_response_preview"}).AddRow("bad", "req", "model", 1, "kind", nil, nil, nil, nil, nil, nil, nil, nil))
		}, wantCode: "recent_failures_query_failed"},
		{name: "recent rows", configure: func(m pgxmock.PgxPoolIface) {
			m.ExpectQuery("SELECT id, label, provider_id, health_status").WithArgs(int64(42), "").WillReturnRows(vendorCredentialMetaRows())
			m.ExpectQuery("SELECT error_type, COUNT").WithArgs(int64(42), "", pgxmock.AnyArg()).WillReturnRows(vendorSummaryRows(nil))
			m.ExpectQuery("SELECT u.occurred_at, u.request_id").WithArgs(int64(42), "", pgxmock.AnyArg()).WillReturnRows(vendorRecentRows(errors.New("rows failed")))
		}, wantCode: "recent_failures_query_failed"},
		{name: "quality query", configure: func(m pgxmock.PgxPoolIface) {
			m.ExpectQuery("SELECT id, label, provider_id, health_status").WithArgs(int64(42), "").WillReturnRows(vendorCredentialMetaRows())
			m.ExpectQuery("SELECT error_type, COUNT").WithArgs(int64(42), "", pgxmock.AnyArg()).WillReturnRows(vendorSummaryRows(nil))
			m.ExpectQuery("SELECT u.occurred_at, u.request_id").WithArgs(int64(42), "", pgxmock.AnyArg()).WillReturnRows(vendorRecentRows(nil))
			m.ExpectQuery("SELECT p.profile_date, p.total_score").WithArgs(int64(42), "").WillReturnError(errors.New("query failed"))
		}, wantCode: "quality_scores_query_failed"},
		{name: "quality scan", configure: func(m pgxmock.PgxPoolIface) {
			m.ExpectQuery("SELECT id, label, provider_id, health_status").WithArgs(int64(42), "").WillReturnRows(vendorCredentialMetaRows())
			m.ExpectQuery("SELECT error_type, COUNT").WithArgs(int64(42), "", pgxmock.AnyArg()).WillReturnRows(vendorSummaryRows(nil))
			m.ExpectQuery("SELECT u.occurred_at, u.request_id").WithArgs(int64(42), "", pgxmock.AnyArg()).WillReturnRows(vendorRecentRows(nil))
			m.ExpectQuery("SELECT p.profile_date, p.total_score").WithArgs(int64(42), "").WillReturnRows(pgxmock.NewRows([]string{"profile_date", "total_score", "availability_score", "stability_score"}).AddRow("bad", 1.0, 1.0, 1.0))
		}, wantCode: "quality_scores_query_failed"},
		{name: "quality rows", configure: func(m pgxmock.PgxPoolIface) {
			m.ExpectQuery("SELECT id, label, provider_id, health_status").WithArgs(int64(42), "").WillReturnRows(vendorCredentialMetaRows())
			m.ExpectQuery("SELECT error_type, COUNT").WithArgs(int64(42), "", pgxmock.AnyArg()).WillReturnRows(vendorSummaryRows(nil))
			m.ExpectQuery("SELECT u.occurred_at, u.request_id").WithArgs(int64(42), "", pgxmock.AnyArg()).WillReturnRows(vendorRecentRows(nil))
			m.ExpectQuery("SELECT p.profile_date, p.total_score").WithArgs(int64(42), "").WillReturnRows(vendorQualityRows(errors.New("rows failed")))
		}, wantCode: "quality_scores_query_failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock, err := pgxmock.NewPool()
			if err != nil {
				t.Fatal(err)
			}
			defer mock.Close()
			tt.configure(mock)
			rec := httptest.NewRecorder()
			(&vendorCredentialErrorHandlers{db: mock}).getVendorCredentialErrorDetail(rec, newVendorErrorRequest("", &AuthContext{Role: "super_admin"}))
			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), `"code":"`+tt.wantCode+`"`) {
				t.Fatalf("want code %s body=%s", tt.wantCode, rec.Body.String())
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
