package admin

import (
	"os"
	"strings"
	"testing"
)

func TestStatsCostAggregatesCastNumericToFloat8(t *testing.T) {
	src, err := os.ReadFile("stats.go")
	if err != nil {
		t.Fatalf("read stats.go: %v", err)
	}
	body := string(src)
	for _, marker := range []string{
		"COALESCE(SUM(cost_usd),0)::float8",
		"COALESCE(SUM(request_count),0)::bigint",
		"COALESCE(SUM(success_count),0)::bigint",
		"COALESCE(SUM(total_tokens),0)::bigint",
	} {
		if strings.Count(body, marker) < 2 {
			t.Fatalf("stats handlers must cast numeric aggregates to scan-safe types; found %d occurrences of %q", strings.Count(body, marker), marker)
		}
	}
	for _, marker := range []string{"($1::timestamptz)", "($2::timestamptz)"} {
		if !strings.Contains(body, marker) {
			t.Fatalf("stats date filters must cast query parameters to timestamptz; missing %q", marker)
		}
	}
}
