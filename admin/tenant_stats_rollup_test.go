package admin

import (
	"os"
	"strings"
	"testing"
)

// TestTenantStatsRollupPathPinsStopWriteGate: the rollup fast path must stay
// gated on RequestLogsWriteEnabled()==false. Dual-write traffic still needs
// the raw union (hot∪parent) so totals do not silently drift to a lagging
// hourly rollup while v1 is still the write source.
func TestTenantStatsRollupPathPinsStopWriteGate(t *testing.T) {
	src, err := os.ReadFile("tenant_stats_rollup.go")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	body := string(src)
	if !strings.Contains(body, "settings.RequestLogsWriteEnabled()") {
		t.Fatal("rollup path must consult settings.RequestLogsWriteEnabled")
	}
	if !strings.Contains(body, "return false") {
		t.Fatal("rollup path must fall through when write is enabled")
	}
	for _, needle := range []string{
		"dimension_type = 'provider_model'",
		"stats_usage_daily",
		"rollup by-model",
		"rollup daily",
	} {
		if !strings.Contains(body, needle) {
			t.Fatalf("rollup path missing %q", needle)
		}
	}
}

// TestGetTenantStatsCallsRollupBeforeRawUnion pins the call order in
// getTenantStats so a future edit cannot reintroduce the 7d 504 by skipping
// the fast path.
func TestGetTenantStatsCallsRollupBeforeRawUnion(t *testing.T) {
	src, err := os.ReadFile("tenants.go")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	body := string(src)
	rollup := strings.Index(body, "tryTenantStatsFromDailyRollup")
	raw := strings.Index(body, "tenantStatsLogsTableSQL")
	if rollup < 0 || raw < 0 {
		t.Fatalf("expected both rollup call and raw union builder; rollup=%d raw=%d", rollup, raw)
	}
	if rollup > raw {
		t.Fatal("rollup fast path must run before raw union logsTable build")
	}
}
