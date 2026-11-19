package admin

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/settings"
)

// 时区轮（2026-11-20）：resolveUsageTimeRange 的日切口径从 UTC 改为
// settings.system.timezone（默认 Asia/Shanghai）。本文件按新契约钉测，
// 用 SystemTimezoneLocationForTest 固定时区，避免测试机本地时区影响。

func withSystemTZ(t *testing.T, name string) {
	t.Helper()
	settings.SystemTimezoneLocationForTest(name)
	t.Cleanup(func() { settings.SystemTimezoneLocationForTest("") })
}

func TestResolveUsageTimeRange_DefaultDays(t *testing.T) {
	withSystemTZ(t, "Asia/Shanghai")
	// No start/end params → fall back to days=N (default 7).
	r := httptest.NewRequest("GET", "/api/usage/2", nil)
	start, end, err := resolveUsageTimeRange(r, 7)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	now := time.Now()
	if end.After(now.Add(2 * time.Second)) {
		t.Errorf("end should be ~now, got %v (now=%v)", end, now)
	}
	// start = 系统时区「今天」零点回退 6 天（含今天的 7 日窗）。
	todayStart := settings.SystemDayStart(now)
	expectedStart := todayStart.AddDate(0, 0, -6)
	if !start.Equal(expectedStart) {
		t.Errorf("start = %v, want %v (system-tz day start - 6d)", start, expectedStart)
	}
}

func TestResolveUsageTimeRange_ExplicitDays(t *testing.T) {
	withSystemTZ(t, "Asia/Shanghai")
	// ?days=30 → 含今天的 30 日历日窗口（首日零点到 now）。
	r := httptest.NewRequest("GET", "/api/usage/2?days=30", nil)
	start, end, err := resolveUsageTimeRange(r, 7)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	span := end.Sub(start)
	if span < 29*24*time.Hour || span > 30*24*time.Hour+1*time.Hour {
		t.Errorf("span = %v, want ~30d (29d+today elapsed)", span)
	}
}

func TestResolveUsageTimeRange_ClampDays(t *testing.T) {
	withSystemTZ(t, "Asia/Shanghai")
	// ?days=0 → clamped to 1. ?days=999 → clamped to 366.
	for _, tc := range []struct {
		in   string
		want time.Duration
	}{
		{"days=0", 24 * time.Hour},
		{"days=999", 366 * 24 * time.Hour},
		{"days=-5", 24 * time.Hour},
	} {
		r := httptest.NewRequest("GET", "/api/usage/2?"+tc.in, nil)
		start, end, err := resolveUsageTimeRange(r, 7)
		if err != nil {
			t.Fatalf("%s: unexpected err: %v", tc.in, err)
		}
		span := end.Sub(start)
		// span = now - 系统日切起点；days=1 时为当天已流逝时长（0~24h），
		// days=366 时为 365d + 当天已流逝。上界留 25h slop。
		low, high := tc.want-25*time.Hour, tc.want+25*time.Hour
		if span < low || span > high {
			t.Errorf("%s: span = %v, want ~%v", tc.in, span, tc.want)
		}
	}
}

func TestResolveUsageTimeRange_CustomRange(t *testing.T) {
	withSystemTZ(t, "Asia/Shanghai")
	// start=2026-06-01&end=2026-06-07 → +08 的 [06-01 00:00, 06-08 00:00)
	// = UTC [2026-05-31T16:00Z, 2026-06-07T16:00Z)——钉死时区换算。
	r := httptest.NewRequest("GET", "/api/usage/2?start=2026-06-01&end=2026-06-07", nil)
	start, end, err := resolveUsageTimeRange(r, 7)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	wantStart := time.Date(2026, 5, 31, 16, 0, 0, 0, time.UTC)
	wantEnd := time.Date(2026, 6, 7, 16, 0, 0, 0, time.UTC)
	if !start.Equal(wantStart) {
		t.Errorf("start = %v, want %v", start, wantStart)
	}
	if !end.Equal(wantEnd) {
		t.Errorf("end = %v, want %v (end exclusive of +08 end+1day)", end, wantEnd)
	}
}

func TestResolveUsageTimeRange_CustomRangeUTCZone(t *testing.T) {
	// 时区=UTC 时与旧契约一致（回归护栏：UTC 部署者不受影响）。
	withSystemTZ(t, "UTC")
	r := httptest.NewRequest("GET", "/api/usage/2?start=2026-06-01&end=2026-06-07", nil)
	start, end, err := resolveUsageTimeRange(r, 7)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !start.Equal(time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("start = %v", start)
	}
	if !end.Equal(time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("end = %v", end)
	}
}

func TestResolveUsageTimeRange_PartialRangeRejected(t *testing.T) {
	withSystemTZ(t, "Asia/Shanghai")
	// start without end (or vice versa) must be rejected — the
	// dashboard always sends both; half-open ranges lead to
	// surprising results.
	for _, q := range []string{
		"start=2026-06-01",
		"end=2026-06-07",
	} {
		r := httptest.NewRequest("GET", "/api/usage/2?"+q, nil)
		_, _, err := resolveUsageTimeRange(r, 7)
		if err == nil {
			t.Errorf("%s: expected error for half-open range, got nil", q)
		}
		if !strings.Contains(err.Error(), "must both be provided") {
			t.Errorf("%s: err = %v, want 'must both be provided'", q, err)
		}
	}
}

func TestResolveUsageTimeRange_InvalidDate(t *testing.T) {
	withSystemTZ(t, "Asia/Shanghai")
	for _, q := range []string{
		"start=2026/06/01&end=2026-06-07",
		"start=2026-06-01&end=not-a-date",
		"start=&end=2026-06-07",
	} {
		r := httptest.NewRequest("GET", "/api/usage/2?"+q, nil)
		_, _, err := resolveUsageTimeRange(r, 7)
		if err == nil {
			t.Errorf("%s: expected error, got nil", q)
		}
	}
}

func TestResolveUsageTimeRange_RangeTooLong(t *testing.T) {
	withSystemTZ(t, "Asia/Shanghai")
	// 400-day range must be rejected to prevent full-table scans.
	r := httptest.NewRequest("GET", "/api/usage/2?start=2024-01-01&end=2026-12-31", nil)
	_, _, err := resolveUsageTimeRange(r, 7)
	if err == nil {
		t.Fatal("expected error for >366d range, got nil")
	}
	if !strings.Contains(err.Error(), "cannot exceed 366 days") {
		t.Errorf("err = %v, want 366-day message", err)
	}
}

func TestResolveUsageTimeRange_EndBeforeStart(t *testing.T) {
	withSystemTZ(t, "Asia/Shanghai")
	r := httptest.NewRequest("GET", "/api/usage/2?start=2026-06-10&end=2026-06-01", nil)
	_, _, err := resolveUsageTimeRange(r, 7)
	if err == nil {
		t.Fatal("expected error when end < start, got nil")
	}
	if !strings.Contains(err.Error(), "on or after start") {
		t.Errorf("err = %v, want 'on or after start'", err)
	}
}

func TestResolveUsageTimeRange_SameDayRange(t *testing.T) {
	withSystemTZ(t, "Asia/Shanghai")
	// start == end → 24-hour window (single day, inclusive).
	r := httptest.NewRequest("GET", "/api/usage/2?start=2026-06-01&end=2026-06-01", nil)
	start, end, err := resolveUsageTimeRange(r, 7)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if end.Sub(start) != 24*time.Hour {
		t.Errorf("same-day range span = %v, want 24h", end.Sub(start))
	}
}

func TestValidateUsageTrendPeriod(t *testing.T) {
	got, err := validateUsageTrendPeriod("")
	if err != nil || got != "day" {
		t.Fatalf("empty period: got=%q err=%v want day", got, err)
	}
	for _, p := range []string{"minute", "hour", "day", "week", "month"} {
		got, err = validateUsageTrendPeriod(p)
		if err != nil || got != p {
			t.Fatalf("period %q: got=%q err=%v", p, got, err)
		}
	}
	_, err = validateUsageTrendPeriod("year")
	if err == nil {
		t.Fatal("expected error for invalid period")
	}
}

func TestValidateTrendGranularityWindow(t *testing.T) {
	start := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	end3d := start.Add(3 * 24 * time.Hour)
	end4d := start.Add(4 * 24 * time.Hour)
	end31d := start.Add(31 * 24 * time.Hour)
	end32d := start.Add(32 * 24 * time.Hour)

	if err := validateTrendGranularityWindow(start, end3d, "minute"); err != nil {
		t.Fatalf("3d minute window: %v", err)
	}
	// Day-truncated "最近 3 天" can span up to ~96h (e.g. 84h).
	end84h := start.Add(84 * time.Hour)
	if err := validateTrendGranularityWindow(start, end84h, "minute"); err != nil {
		t.Fatalf("84h minute window (calendar 3d): %v", err)
	}
	if err := validateTrendGranularityWindow(start, end4d, "minute"); err == nil {
		t.Fatal("expected error for 4d minute window")
	}
	if err := validateTrendGranularityWindow(start, end31d, "hour"); err != nil {
		t.Fatalf("31d hour window: %v", err)
	}
	if err := validateTrendGranularityWindow(start, end32d, "hour"); err == nil {
		t.Fatal("expected error for 32d hour window")
	}
	if err := validateTrendGranularityWindow(start, end32d, "day"); err != nil {
		t.Fatalf("32d day window should be allowed: %v", err)
	}
}
