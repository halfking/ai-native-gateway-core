package admin

import (
	"os"
	"strings"
	"testing"
)

func TestDashboardBoardRoutesRegistered(t *testing.T) {
	src, err := os.ReadFile("handler.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	for _, route := range []string{
		"/api/admin/dashboard/board",
		"/api/admin/dashboard/board/error-drill",
	} {
		if !strings.Contains(body, route) {
			t.Fatalf("handler.go missing route %s", route)
		}
	}
}

func TestBoardSummaryPrefersMinuteTable(t *testing.T) {
	src, err := os.ReadFile("dashboard_board_queries.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "request_stats_minute") {
		t.Fatal("board summary must read request_stats_minute first")
	}
}

func TestBoardFallbackFromRequestLogs(t *testing.T) {
	for _, file := range []string{"dashboard_board_fallback.go", "dashboard_board_queries.go"} {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		body := string(src)
		if !strings.Contains(body, "request_logs_hot") && !strings.Contains(body, "requestLogsFromClause") {
			t.Fatalf("%s must include request_logs fallback", file)
		}
	}
}

func TestBoardDataSourceMixed(t *testing.T) {
	src, degraded := boardDataSource(true, false, true)
	if src != "mixed" || !degraded {
		t.Fatalf("expected mixed degraded, got %q %v", src, degraded)
	}
	src, degraded = boardDataSource(true, true, true)
	if src != "request_stats_minute" || degraded {
		t.Fatalf("expected minute source, got %q %v", src, degraded)
	}
}
