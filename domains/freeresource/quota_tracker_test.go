package freeresource

import (
	"testing"
	"time"
)

func TestQuotaTracker_ComputeWindows(t *testing.T) {
	qt := &QuotaTracker{}
	ts := time.Date(2024, 8, 15, 14, 30, 0, 0, time.UTC)

	windows := qt.computeWindows(ts, []WindowType{
		WindowTypeHour5,
		WindowTypeDay1,
		WindowTypeDay7,
		WindowTypeMonth1,
	})

	if len(windows) != 4 {
		t.Errorf("expected 4 windows, got %d", len(windows))
	}

	for _, w := range windows {
		if w.Type == WindowTypeDay1 {
			expectedStart := time.Date(2024, 8, 15, 0, 0, 0, 0, time.UTC)
			if !w.Start.Equal(expectedStart) {
				t.Errorf("day-1 window start: expected %v, got %v", expectedStart, w.Start)
			}
		}
		if w.Type == WindowTypeMonth1 {
			expectedStart := time.Date(2024, 8, 1, 0, 0, 0, 0, time.UTC)
			if !w.Start.Equal(expectedStart) {
				t.Errorf("month-1 window start: expected %v, got %v", expectedStart, w.Start)
			}
		}
		if w.Type == WindowTypeHour5 {
			wantStart := time.Date(2024, 8, 15, 10, 0, 0, 0, time.UTC)
			if !w.Start.Equal(wantStart) {
				t.Errorf("hour-5 window start: expected %v, got %v", wantStart, w.Start)
			}
			wantEnd := wantStart.Add(5 * time.Hour)
			if !w.End.Equal(wantEnd) {
				t.Errorf("hour-5 window end: expected %v, got %v", wantEnd, w.End)
			}
		}
		if w.Type == WindowTypeDay7 {
			dayStart := time.Date(2024, 8, 15, 0, 0, 0, 0, time.UTC)
			wantStart := dayStart.Add(-6 * 24 * time.Hour)
			if !w.Start.Equal(wantStart) {
				t.Errorf("day-7 window start: expected %v, got %v", wantStart, w.Start)
			}
			if !w.End.Equal(wantStart.Add(7 * 24 * time.Hour)) {
				t.Errorf("day-7 window end: expected %v, got %v", wantStart.Add(7*24*time.Hour), w.End)
			}
		}
	}
}

func TestQuotaTracker_ComputeWindows_NormalisesNonUTC(t *testing.T) {
	qt := &QuotaTracker{}
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Skipf("Asia/Shanghai tzdata unavailable: %v", err)
	}
	ts := time.Date(2024, 8, 15, 22, 30, 0, 0, loc) // = 14:30 UTC

	windows := qt.computeWindows(ts, []WindowType{WindowTypeDay1})
	if len(windows) != 1 {
		t.Fatalf("expected 1 window, got %d", len(windows))
	}
	wanted := time.Date(2024, 8, 15, 0, 0, 0, 0, time.UTC)
	if !windows[0].Start.Equal(wanted) {
		t.Errorf("non-UTC ts should be normalised to UTC, expected %v, got %v", wanted, windows[0].Start)
	}
}

func TestQuotaTracker_ComputeWindows_Hour5AcrossHourBoundary(t *testing.T) {
	qt := &QuotaTracker{}
	// 14:50:00 → bucket 10:00-15:00; 15:01:00 → bucket 11:00-16:00.
	ts := time.Date(2024, 8, 15, 14, 50, 0, 0, time.UTC)
	w := qt.computeWindows(ts, []WindowType{WindowTypeHour5})
	if len(w) != 1 {
		t.Fatalf("expected 1 window, got %d", len(w))
	}
	if w[0].Start.Hour() != 10 {
		t.Errorf("hour-5 first bucket should start at 10:00, got %v", w[0].Start)
	}

	ts2 := time.Date(2024, 8, 15, 15, 1, 0, 0, time.UTC)
	w2 := qt.computeWindows(ts2, []WindowType{WindowTypeHour5})
	if w2[0].Start.Hour() != 11 {
		t.Errorf("hour-5 second bucket should start at 11:00, got %v", w2[0].Start)
	}
}

func TestQuotaTracker_Record_DefaultsTimestamp(t *testing.T) {
	qt := &QuotaTracker{}
	// 验证零时间戳走默认值, 不应当 panic.
	windows := qt.computeWindows(time.Time{}, []WindowType{WindowTypeDay1})
	if len(windows) != 1 {
		t.Errorf("expected 1 window, got %d", len(windows))
	}
	// 零时间戳经 UTC 归一为 year 1, 结果窗口应是 year 1 1月1日.
	if windows[0].Start.Year() != 1 {
		t.Errorf("zero ts should produce year-1 window, got start=%v", windows[0].Start)
	}
}

func TestParseRetryAfter_PrefersXRateLimitReset(t *testing.T) {
	headers := map[string]string{
		"Retry-After":       "30",
		"X-RateLimit-Reset": "1800000000", // 2027-01-19
	}
	now := time.Unix(1700000000, 0).UTC()
	retry, reset := parseRetryAfter(now, headers)
	if reset.Unix() != 1800000000 {
		t.Errorf("X-RateLimit-Reset should win, got reset %v", reset)
	}
	want := int(time.Unix(1800000000, 0).Sub(now).Seconds())
	if retry != want {
		t.Errorf("retry should be derived from reset, got %d, want %d", retry, want)
	}
}

func TestParseRetryAfter_RetryAfterSeconds(t *testing.T) {
	headers := map[string]string{"Retry-After": "30"}
	now := time.Unix(1700000000, 0).UTC()
	retry, reset := parseRetryAfter(now, headers)
	if retry != 30 {
		t.Errorf("expected 30s, got %d", retry)
	}
	if !reset.Equal(now.Add(30 * time.Second)) {
		t.Errorf("expected reset=now+30s, got %v", reset)
	}
}

func TestParseRetryAfter_RetryAfterHTTPDate(t *testing.T) {
	now := time.Date(2024, 8, 15, 14, 30, 0, 0, time.UTC)
	// 用 60s 避免 time.Until 截断抖动.
	future := now.Add(60 * time.Second).UTC()
	hdr := future.Format(httpTimeFormat)
	t.Logf("formatted header: %q", hdr)
	headers := map[string]string{"Retry-After": hdr}
	retry, reset := parseRetryAfter(now, headers)
	if retry < 59 || retry > 60 {
		t.Errorf("expected ~60s from HTTP date, got %d (reset=%v)", retry, reset)
	}
	if !reset.Equal(future) {
		t.Errorf("expected reset %v, got %v", future, reset)
	}
}

func TestParseRetryAfter_NegativeRetryClamped(t *testing.T) {
	headers := map[string]string{"Retry-After": "-10"}
	now := time.Unix(1700000000, 0).UTC()
	retry, _ := parseRetryAfter(now, headers)
	if retry != 0 {
		t.Errorf("negative retry-after should clamp to 0, got %d", retry)
	}
}

func TestParseRetryAfter_NoHeaders(t *testing.T) {
	now := time.Unix(1700000000, 0).UTC()
	retry, reset := parseRetryAfter(now, nil)
	if retry != 0 {
		t.Errorf("no headers should yield 0 retry, got %d", retry)
	}
	if reset.Unix() != now.Unix() {
		t.Errorf("no headers should yield reset=now, got %v", reset)
	}
}

const httpTimeFormat = "Mon, 02 Jan 2006 15:04:05 GMT"

func TestQuotaTracker_Record(t *testing.T) {
	// 这是集成测试，需要真实数据库
	// 在 CI 环境中，应使用 Docker PostgreSQL 容器
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	// TODO: 实现完整的集成测试
	// 1. 创建测试数据库连接
	// 2. 插入测试凭据
	// 3. 调用 Record
	// 4. 验证 UPSERT 结果
}

func TestQuotaTracker_Preflight(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	// TODO: 实现完整的集成测试
	// 1. 准备测试数据
	// 2. 测试无记录场景（应返回 true）
	// 3. 测试配额充足场景（应返回 true）
	// 4. 测试配额耗尽场景（应返回 false）
	// 5. 测试过期自动重置场景
}

func TestQuotaTracker_CorrectFromHeaders(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	// TODO: 实现完整的集成测试
	// 测试各种 429 响应头组合
}

// 单元测试：验证窗口边界计算
func TestWindowBoundaries(t *testing.T) {
	tests := []struct {
		name      string
		ts        time.Time
		winType   WindowType
		wantStart time.Time
		wantEnd   time.Time
	}{
		{
			name:      "day-1 mid-day",
			ts:        time.Date(2024, 8, 15, 14, 30, 0, 0, time.UTC),
			winType:   WindowTypeDay1,
			wantStart: time.Date(2024, 8, 15, 0, 0, 0, 0, time.UTC),
			wantEnd:   time.Date(2024, 8, 16, 0, 0, 0, 0, time.UTC),
		},
		{
			name:      "month-1 mid-month",
			ts:        time.Date(2024, 8, 15, 14, 30, 0, 0, time.UTC),
			winType:   WindowTypeMonth1,
			wantStart: time.Date(2024, 8, 1, 0, 0, 0, 0, time.UTC),
			wantEnd:   time.Date(2024, 9, 1, 0, 0, 0, 0, time.UTC),
		},
	}

	qt := &QuotaTracker{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			windows := qt.computeWindows(tt.ts, []WindowType{tt.winType})
			if len(windows) != 1 {
				t.Fatalf("expected 1 window, got %d", len(windows))
			}

			w := windows[0]
			if !w.Start.Equal(tt.wantStart) {
				t.Errorf("start: expected %v, got %v", tt.wantStart, w.Start)
			}
			if !w.End.Equal(tt.wantEnd) {
				t.Errorf("end: expected %v, got %v", tt.wantEnd, w.End)
			}
		})
	}
}
