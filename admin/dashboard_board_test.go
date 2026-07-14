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

func TestBoardTrendsUseResolveWithFallback(t *testing.T) {
	src, err := os.ReadFile("dashboard_board_queries.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	if !strings.Contains(body, "resolveBoardTrends") {
		t.Fatal("board trends must use resolveBoardTrends with logs fallback")
	}
	if !strings.Contains(body, "sqlTrendBucket") {
		t.Fatal("board trends must bucket with sqlTrendBucket")
	}
}
