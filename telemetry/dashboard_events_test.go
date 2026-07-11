package telemetry

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
)

func TestDashboardEventRecorderIncludesHotEventsInAccessStats(t *testing.T) {
	mockDB, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("new mock database: %v", err)
	}
	defer mockDB.Close()

	recorder := newDashboardEventRecorder(mockDB, nil)
	event := &DashboardEvent{
		EventID:      "event-hot-1",
		EventType:    "api_access",
		Timestamp:    time.Now(),
		TenantID:     "tenant-1",
		UserID:       "user-1",
		APIPath:      "/admin/dashboard",
		APIMethod:    "GET",
		StatusCode:   200,
		ResponseTime: 15,
	}

	mockDB.ExpectExec(regexp.QuoteMeta("INSERT INTO dashboard_access_events_hot")).
		WithArgs(
			event.EventID, event.EventType, event.Timestamp,
			event.TenantID, event.UserID, event.UserRole, event.SessionID,
			event.APIPath, event.APIMethod, event.APIVersion,
			pgxmock.AnyArg(),
			event.StatusCode, event.ResponseTime, event.CacheHit, event.DataSize,
			event.ErrorCode, event.ErrorMessage,
			event.ClientIP, event.UserAgent, event.Referer,
			event.DBQueryTime, event.CacheQueryTime,
		).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	if err := recorder.insertEvent(context.Background(), event); err != nil {
		t.Fatalf("insert hot event: %v", err)
	}

	mockDB.ExpectQuery(`WITH access_events[\s\S]*dashboard_access_events_hot[\s\S]*dashboard_access_events archived`).
		WillReturnRows(pgxmock.NewRows([]string{
			"total", "unique_users", "unique_tenants", "avg_response", "p95", "p99", "cache_hit_rate", "error_rate",
		}).AddRow(int64(1), int64(1), int64(1), 15.0, 15.0, 15.0, 0.0, 0.0))
	mockDB.ExpectQuery(`WITH access_events[\s\S]*dashboard_access_events_hot[\s\S]*dashboard_access_events archived`).
		WillReturnRows(pgxmock.NewRows([]string{
			"api_path", "request_count", "avg_response", "error_rate", "cache_hit_rate",
		}).AddRow(event.APIPath, int64(1), 15.0, 0.0, 0.0))

	stats, err := recorder.GetAccessStats(context.Background(), 1)
	if err != nil {
		t.Fatalf("GetAccessStats: %v", err)
	}
	if stats.TotalRequests != 1 || len(stats.TopAPIs) != 1 || stats.TopAPIs[0].APIPath != event.APIPath {
		t.Fatalf("stats = %+v, want the just-written hot event", stats)
	}
	if err := mockDB.ExpectationsWereMet(); err != nil {
		t.Fatalf("database expectations: %v", err)
	}
}
