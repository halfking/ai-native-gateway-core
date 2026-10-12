package admin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestStatsRebuildRangeParsing pins the request contract of the historical
// rebuild endpoint. Both bounds are inclusive YYYY-MM-DD in UTC; the returned
// upper bound is exclusive (start of the next day) so the re-aggregation
// covers the whole final day instead of stopping at 00:00.
func TestStatsRebuildRangeParsing(t *testing.T) {
	t.Run("explicit inclusive range", func(t *testing.T) {
		from, to, err := statsRebuildRange("2026-09-01", "2026-09-03")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		wantFrom := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
		wantTo := time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC)
		if !from.Equal(wantFrom) {
			t.Errorf("from = %v, want %v", from, wantFrom)
		}
		if !to.Equal(wantTo) {
			t.Errorf("to = %v, want %v (exclusive bound covering 09-03 in full)", to, wantTo)
		}
	})

	t.Run("defaults to last 7 days ending today", func(t *testing.T) {
		from, to, err := statsRebuildRange("", "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if d := to.Sub(from); d != 7*24*time.Hour {
			t.Errorf("default span = %v, want 7 days", d)
		}
		now := time.Now().UTC()
		today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
		if !to.Equal(today.AddDate(0, 0, 1)) {
			t.Errorf("default to = %v, want start of tomorrow (today included)", to)
		}
	})

	t.Run("rejects reversed range", func(t *testing.T) {
		if _, _, err := statsRebuildRange("2026-09-03", "2026-09-01"); err == nil {
			t.Fatal("expected an error when to precedes from")
		}
	})

	t.Run("rejects unparsable dates", func(t *testing.T) {
		if _, _, err := statsRebuildRange("not-a-date", ""); err == nil {
			t.Fatal("expected an error for a bad from")
		}
		if _, _, err := statsRebuildRange("", "2026-13-45"); err == nil {
			t.Fatal("expected an error for a bad to")
		}
	})

	t.Run("rejects range beyond retention", func(t *testing.T) {
		// Older minutes are already cleaned up by statsRetentionDays, so
		// re-scanning them is pure cost. The bound stops an operator from
		// kicking off a month-long scan that hammers request_logs.
		if _, _, err := statsRebuildRange("2020-01-01", "2026-09-01"); err == nil {
			t.Fatal("expected an error for a range wider than the retention window")
		} else if !strings.Contains(err.Error(), "retention") {
			t.Errorf("error should name the retention bound, got: %v", err)
		}
	})

	t.Run("accepts exactly the retention width", func(t *testing.T) {
		today := time.Now().UTC()
		from := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.UTC).
			AddDate(0, 0, -(statsRebuildMaxDays - 1))
		if _, _, err := statsRebuildRange(
			from.Format("2006-01-02"), today.Format("2006-01-02")); err != nil {
			t.Fatalf("a %d-day span must be accepted: %v", statsRebuildMaxDays, err)
		}
	})
}

// TestDashboardRollupRebuildEndpointWired keeps the route registered and
// guarded. Without this, the whole rebuild path is unreachable and the stored
// rollup silently stays polluted for the full retention window.
func TestDashboardRollupRebuildEndpointWired(t *testing.T) {
	src, err := os.ReadFile("handler.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	if !strings.Contains(body, "/api/admin/dashboard/rollup/rebuild") {
		t.Fatal("handler.go missing the rollup rebuild route")
	}
	// superAdmin, not plain admin: it rewrites every tenant's aggregates.
	if !strings.Contains(body, "h.superAdmin(h.handleDashboardRollupRebuild)") {
		t.Fatal("rollup rebuild must be superAdmin — it rewrites all-tenant aggregates")
	}
}

// TestStatsMinuteRebuilderInjectedFromMain keeps the worker wiring honest: the
// endpoint answers 503 without it, which would look like a transient outage
// rather than a missing injection.
func TestStatsMinuteRebuilderInjectedFromMain(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "cmd", "gateway", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "SetStatsMinuteRebuilder(statsMinuteRollup)") {
		t.Fatal("cmd/gateway/main.go must call adminHandler.SetStatsMinuteRebuilder(statsMinuteRollup);\n" +
			"  without it the endpoint returns 503 forever and stored rollups stay polluted")
	}
}
