package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
)

// 2026-09-05 审计闭环1：GET /api/errors/trend 回归。
// 主路径读 supplier_error_stats 预聚合；空窗口时回退 supplier_errors_unified
// 明细聚合（source=fallback），保证聚合器刚部署时趋势图仍有数据。

func statsTrendRows() *pgxmock.Rows {
	bucket := time.Date(2026, 9, 5, 8, 0, 0, 0, time.UTC)
	bySupplier := []byte(`{"zhipu":3,"openai":1}`)
	byType := []byte(`{"rate_limit":2,"timeout":2}`)
	return pgxmock.NewRows([]string{"stat_time", "error_count", "unique_requests", "affected_users", "by_supplier", "by_error_type"}).
		AddRow(bucket, 4, 3, 4, bySupplier, byType)
}

func trendBreakdownRows() *pgxmock.Rows {
	return pgxmock.NewRows([]string{"kind", "key", "n"}).
		AddRow("type", "rate_limit", 2).
		AddRow("type", "timeout", 2).
		AddRow("supplier", "zhipu", 3).
		AddRow("supplier", "openai", 1)
}

func TestErrorsTrendFromStats(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	mock.ExpectQuery("FROM supplier_error_stats").
		WithArgs("hour", pgxmock.AnyArg(), pgxmock.AnyArg(), "", int64(0), "").
		WillReturnRows(statsTrendRows())
	mock.ExpectQuery("FROM supplier_errors_unified").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), "", int64(0), "").
		WillReturnRows(trendBreakdownRows())

	req := httptest.NewRequest(http.MethodGet, "/api/errors/trend?hours=24", nil)
	req = SetAuthContext(req, &AuthContext{Role: "super_admin"})
	rec := httptest.NewRecorder()
	(&errorsTrendHandlers{db: mock}).getErrorsTrend(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}

	var body struct {
		Source      string `json:"source"`
		Granularity string `json:"granularity"`
		TimeSeries  []struct {
			Timestamp  time.Time      `json:"timestamp"`
			ErrorCount int            `json:"error_count"`
			BySupplier map[string]int `json:"by_supplier"`
		} `json:"time_series"`
		Summary struct {
			TotalErrors   int `json:"total_errors"`
			TopErrorTypes []struct {
				Key   string `json:"key"`
				Count int    `json:"count"`
			} `json:"top_error_types"`
			TopSuppliers []struct {
				Key   string `json:"key"`
				Count int    `json:"count"`
			} `json:"top_suppliers"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Source != "stats" {
		t.Fatalf("source=%s", body.Source)
	}
	if body.Granularity != "hour" || len(body.TimeSeries) != 1 {
		t.Fatalf("granularity=%s series=%d", body.Granularity, len(body.TimeSeries))
	}
	if body.Summary.TotalErrors != 4 {
		t.Fatalf("total=%d", body.Summary.TotalErrors)
	}
	if len(body.Summary.TopErrorTypes) != 2 || len(body.Summary.TopSuppliers) != 2 {
		t.Fatalf("breakdowns: %+v", body.Summary)
	}
	if body.TimeSeries[0].BySupplier["zhipu"] != 3 {
		t.Fatalf("by_supplier=%v", body.TimeSeries[0].BySupplier)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestErrorsTrendFallsBackToUnifiedDetail(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	// stats 空窗口 → 回退明细聚合。
	// 调用序列：stats 明细（空）→ stats breakdown（空）→ unified 明细 →
	// unified breakdown（loadFromStats 与 loadFromDetail 各跑一次 breakdown）。
	mock.ExpectQuery("FROM supplier_error_stats").
		WithArgs("minute", pgxmock.AnyArg(), pgxmock.AnyArg(), "", int64(0), "").
		WillReturnRows(pgxmock.NewRows([]string{"stat_time", "error_count", "unique_requests", "affected_users", "by_supplier", "by_error_type"}))
	mock.ExpectQuery("FROM supplier_errors_unified").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"kind", "key", "n"}))
	bucket := time.Date(2026, 9, 5, 8, 30, 0, 0, time.UTC)
	mock.ExpectQuery("FROM supplier_errors_unified").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"bucket", "error_count", "unique_requests", "affected_users", "by_supplier", "by_error_type"}).
			AddRow(bucket, 2, 2, 2, []byte(`{"zhipu":2}`), []byte(`{"timeout":2}`)))
	mock.ExpectQuery("FROM supplier_errors_unified").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(trendBreakdownRows())

	req := httptest.NewRequest(http.MethodGet, "/api/errors/trend?hours=1", nil)
	req = SetAuthContext(req, &AuthContext{Role: "super_admin"})
	rec := httptest.NewRecorder()
	(&errorsTrendHandlers{db: mock}).getErrorsTrend(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Source      string `json:"source"`
		Granularity string `json:"granularity"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Source != "fallback" {
		t.Fatalf("source=%s, want fallback", body.Source)
	}
	if body.Granularity != "minute" {
		t.Fatalf("granularity=%s, want minute for hours=1", body.Granularity)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestErrorsTrendValidationAndDBErrors(t *testing.T) {
	newMock := func(t *testing.T) pgxmock.PgxPoolIface {
		t.Helper()
		mock, err := pgxmock.NewPool()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(mock.Close)
		return mock
	}
	t.Run("invalid hours", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/errors/trend?hours=48", nil)
		(&errorsTrendHandlers{db: newMock(t)}).getErrorsTrend(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status=%d", rec.Code)
		}
	})
	t.Run("invalid granularity", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/errors/trend?granularity=week", nil)
		(&errorsTrendHandlers{db: newMock(t)}).getErrorsTrend(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status=%d", rec.Code)
		}
	})
	t.Run("invalid credential_id", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/errors/trend?credential_id=abc", nil)
		(&errorsTrendHandlers{db: newMock(t)}).getErrorsTrend(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status=%d", rec.Code)
		}
	})
	t.Run("stats db error surfaces 500", func(t *testing.T) {
		mock := newMock(t)
		mock.ExpectQuery("FROM supplier_error_stats").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnError(errors.New("db down"))
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/errors/trend", nil)
		(&errorsTrendHandlers{db: mock}).getErrorsTrend(rec, req)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
	})
	t.Run("db not configured", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/errors/trend", nil)
		(&errorsTrendHandlers{}).getErrorsTrend(rec, req)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status=%d", rec.Code)
		}
	})
}
