package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
)

// 2026-10-07 审计回归门（R49-C1 同根补漏）：/api/candidate-failures 家族
// 三个 DB 端点都跑在 withRLSBypassTx 的事务里（app.bypass_rls=true，RLS 对
// 谓词零贡献），修复前 SQL 无任何租户过滤 ⇒ tenant_admin 可跨租户读失败
// 台账与上游响应体预览。与 errors_trend_test.go 的门同款：期望正则锚定
// SQL 文本中的租户谓词，无谓词的旧实现必然匹配失败（无匹配 ⇒ 查询报错
// ⇒ 500，而非静默越权）。
func TestCandidateFailuresTenantScopedForTenantAdmin(t *testing.T) {
	t.Run("list 腿", func(t *testing.T) {
		mock, err := pgxmock.NewPool()
		if err != nil {
			t.Fatal(err)
		}
		defer mock.Close()

		rows := pgxmock.NewRows([]string{
			"id", "ts", "request_id", "tenant_id", "credential_id", "provider_id",
			"raw_model_name", "attempt_index", "error_kind", "error_message",
			"upstream_status_code", "upstream_response_preview", "latency_ms",
			"retryable", "session_id",
		}).AddRow(nil, time.Now(), "req-1", "acme", 7, 3, "minimax-m3", 1,
			"transient", "upstream 502", nil, nil, nil, nil, nil)
		// 参数序：since, kind, retryable, session, limit, tenantID。
		mock.ExpectQuery(`tenant_id = \$6`).
			WithArgs(pgxmock.AnyArg(), "", "", "", pgxmock.AnyArg(), "acme").
			WillReturnRows(rows)

		req := httptest.NewRequest(http.MethodGet, "/api/candidate-failures?limit=10", nil)
		req = SetAuthContext(req, &AuthContext{Role: "tenant_admin", TenantID: "acme"})
		rec := httptest.NewRecorder()
		(&candidateFailureHandlers{db: mock}).listCandidateFailures(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("credential 维度腿", func(t *testing.T) {
		mock, err := pgxmock.NewPool()
		if err != nil {
			t.Fatal(err)
		}
		defer mock.Close()

		rows := pgxmock.NewRows([]string{
			"id", "ts", "request_id", "raw_model_name", "attempt_index",
			"error_kind", "upstream_status_code", "upstream_response_preview",
			"latency_ms", "session_id",
		})
		// 参数序：credID, since, tenantID, limit。
		mock.ExpectQuery(`tenant_id = \$3`).
			WithArgs(42, pgxmock.AnyArg(), "acme", pgxmock.AnyArg()).
			WillReturnRows(rows)

		req := httptest.NewRequest(http.MethodGet, "/api/candidate-failures/credential/42", nil)
		req.SetPathValue("id", "42")
		req = SetAuthContext(req, &AuthContext{Role: "tenant_admin", TenantID: "acme"})
		rec := httptest.NewRecorder()
		(&candidateFailureHandlers{db: mock}).getCandidateFailuresByCredential(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("stats 腿", func(t *testing.T) {
		mock, err := pgxmock.NewPool()
		if err != nil {
			t.Fatal(err)
		}
		defer mock.Close()

		rows := pgxmock.NewRows([]string{
			"raw_model_name", "error_kind", "credential_id", "provider_id",
			"count", "retryable_count", "distinct_status_codes", "last_seen", "first_seen",
		})
		// 参数序：since, tenantID。
		mock.ExpectQuery(`tenant_id = \$2`).
			WithArgs(pgxmock.AnyArg(), "acme").
			WillReturnRows(rows)

		req := httptest.NewRequest(http.MethodGet, "/api/candidate-failures/stats", nil)
		req = SetAuthContext(req, &AuthContext{Role: "tenant_admin", TenantID: "acme"})
		rec := httptest.NewRecorder()
		(&candidateFailureHandlers{db: mock}).getCandidateFailureStats(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("super_admin 不受限（空租户参数）", func(t *testing.T) {
		mock, err := pgxmock.NewPool()
		if err != nil {
			t.Fatal(err)
		}
		defer mock.Close()

		rows := pgxmock.NewRows([]string{
			"id", "ts", "request_id", "tenant_id", "credential_id", "provider_id",
			"raw_model_name", "attempt_index", "error_kind", "error_message",
			"upstream_status_code", "upstream_response_preview", "latency_ms",
			"retryable", "session_id",
		})
		// super_admin ⇒ EffectiveTenantIDAll 为空串 ⇒ 谓词放行全租户。
		mock.ExpectQuery(`tenant_id = \$6`).
			WithArgs(pgxmock.AnyArg(), "", "", "", pgxmock.AnyArg(), "").
			WillReturnRows(rows)

		req := httptest.NewRequest(http.MethodGet, "/api/candidate-failures", nil)
		req = SetAuthContext(req, &AuthContext{Role: "super_admin", TenantID: ""})
		rec := httptest.NewRecorder()
		(&candidateFailureHandlers{db: mock}).listCandidateFailures(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
}
