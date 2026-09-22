package admin

import (
	"testing"
	"time"
)

// Wave 1 A3 回归钉桩：非 default 租户的日志查询窗必须由后端强制 72h，
// 直连 API 不得绕过（此前仅前端 RequestLogsView 降档约束）。

func TestClampQueryWindowForTenant(t *testing.T) {
	end := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour

	cases := []struct {
		name         string
		tenantID     string
		span         time.Duration
		wantSpanLeft time.Duration // end-start after clamp
	}{
		{"tenant 7d clamped to 72h", "acme", 7 * day, 72 * time.Hour},
		{"tenant exactly 72h unchanged", "acme", 72 * time.Hour, 72 * time.Hour},
		{"tenant 1d unchanged", "acme", day, day},
		{"default tenant 7d unchanged", "default", 7 * day, 7 * day},
		{"legacy empty tenant 7d unchanged", "", 7 * day, 7 * day},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			start := end.Add(-tc.span)
			gotStart, gotEnd := clampQueryWindowForTenant(start, end, tc.tenantID)
			if !gotEnd.Equal(end) {
				t.Errorf("end moved: %v -> %v", end, gotEnd)
			}
			if got := gotEnd.Sub(gotStart); got != tc.wantSpanLeft {
				t.Errorf("span = %v, want %v", got, tc.wantSpanLeft)
			}
		})
	}

	t.Run("default tenant still capped at 366d", func(t *testing.T) {
		start := end.Add(-400 * day)
		gotStart, gotEnd := clampQueryWindowForTenant(start, end, "default")
		if got := gotEnd.Sub(gotStart); got != maxLogQueryWindow {
			t.Errorf("span = %v, want %v", got, maxLogQueryWindow)
		}
	})
}
