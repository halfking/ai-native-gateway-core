package freeresource

import (
	"os"
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

func TestParseRetryAfter_StaleRateLimitResetUsesDefaultBackoff(t *testing.T) {
	now := time.Unix(1700000000, 0).UTC()
	for _, reset := range []string{"0", "1699999990", "1700000000"} {
		retry, resetAt := parseRetryAfter(now, map[string]string{"X-RateLimit-Reset": reset})
		if retry != 60 {
			t.Errorf("X-RateLimit-Reset %q should use 60s default backoff, got %d", reset, retry)
		}
		if !resetAt.Equal(now.Add(60 * time.Second)) {
			t.Errorf("X-RateLimit-Reset %q expected reset=now+60s, got %v", reset, resetAt)
		}
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

func TestParseRetryAfter_RetryAfterZeroUsesDefaultBackoff(t *testing.T) {
	now := time.Unix(1700000000, 0).UTC()
	retry, reset := parseRetryAfter(now, map[string]string{"Retry-After": "0"})
	if retry != 60 {
		t.Errorf("Retry-After 0 should use 60s default backoff, got %d", retry)
	}
	if !reset.Equal(now.Add(60 * time.Second)) {
		t.Errorf("expected reset=now+60s, got %v", reset)
	}

	retryRel, resetRel := parseRetryAfter(now, map[string]string{"Retry-After": "0s"})
	if retryRel != 60 {
		t.Errorf("Retry-After 0s should use 60s default backoff, got %d", retryRel)
	}
	if !resetRel.Equal(now.Add(60 * time.Second)) {
		t.Errorf("expected relative reset=now+60s, got %v", resetRel)
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

// TestParseRetryAfter_StaleHTTPDateUsesDefaultBackoff ensures a past HTTP-date
// in Retry-After (clock drift) applies the 60s default backoff instead of
// clamping to 0 and leaving resetAt in the past (which would let Preflight
// immediately re-enable the exhausted credential).
func TestParseRetryAfter_StaleHTTPDateUsesDefaultBackoff(t *testing.T) {
	now := time.Date(2024, 8, 15, 14, 30, 0, 0, time.UTC)
	past := now.Add(-5 * time.Minute).UTC()
	hdr := past.Format(httpTimeFormat)
	retry, reset := parseRetryAfter(now, map[string]string{"Retry-After": hdr})
	if retry != 60 {
		t.Errorf("stale HTTP-date should use 60s default backoff, got %d", retry)
	}
	if !reset.Equal(now.Add(60 * time.Second)) {
		t.Errorf("expected reset=now+60s, got %v", reset)
	}
}

// TestParseRetryAfter_RelativeUnits covers Groq-style "6s"/"5m"/"2h"/"1d"
// Retry-After values (non-RFC but emitted by several providers). Without
// parseRetryAfterRelative these fall through to HTTP-date parse (fail) →
// (0, now) → exhausted keys retried immediately.
func TestParseRetryAfter_RelativeUnits(t *testing.T) {
	now := time.Unix(1700000000, 0).UTC()
	tests := []struct {
		header  string
		wantSec int
	}{
		{"6s", 6},
		{"5m", 300},
		{"2h", 7200},
		{"1d", 86400},
		{" 30s ", 30}, // whitespace tolerated
		{"10M", 600},  // uppercase unit
	}
	for _, tt := range tests {
		t.Run(tt.header, func(t *testing.T) {
			retry, reset := parseRetryAfter(now, map[string]string{"Retry-After": tt.header})
			if retry != tt.wantSec {
				t.Errorf("Retry-After %q: retry = %d, want %d", tt.header, retry, tt.wantSec)
			}
			wantReset := now.Add(time.Duration(tt.wantSec) * time.Second)
			if !reset.Equal(wantReset) {
				t.Errorf("Retry-After %q: reset = %v, want %v", tt.header, reset, wantReset)
			}
		})
	}
}

// TestParseRetryAfter_RelativeUnitsRejected ensures non-duration suffixes
// don't get mis-parsed as relative units (e.g. an HTTP-date fragment like
// "GMT" trailing must not be read as a unit).
func TestParseRetryAfter_RelativeUnitsRejected(t *testing.T) {
	now := time.Unix(1700000000, 0).UTC()
	// "5x" is not a recognized unit → should fall through, not return 5s.
	retry, _ := parseRetryAfter(now, map[string]string{"Retry-After": "5x"})
	if retry == 5 {
		t.Fatal("unrecognized unit 'x' was treated as seconds")
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

// TestParseRetryAfter_AcceptsCanonicalMIMEKeys 验证 parseRetryAfter 在
// http.Header 经过 CanonicalMIMEHeaderKey 规范化后 (例如 "X-Ratelimit-Reset")
// 仍能正确命中, 修复 streaming.handler_autocombo.flattenHeaders 路径
// 上的 silent miss. streaming 层 handler 测试 (handler_autocombo_test.go)
// 通过 X-Ratelimit-Reset 形式传头, 这里做直接覆盖.
func TestParseRetryAfter_AcceptsCanonicalMIMEKeys(t *testing.T) {
	now := time.Unix(1700000000, 0).UTC()
	headers := map[string]string{
		"X-Ratelimit-Reset": "1800000000", // 已经是 canonical form
	}
	retry, reset := parseRetryAfter(now, headers)
	if reset.Unix() != 1800000000 {
		t.Errorf("canonical X-Ratelimit-Reset should win, got reset %v", reset)
	}
	if retry <= 0 {
		t.Errorf("retry should be positive, got %d", retry)
	}

	// 反向: 大小写混写也应命中.
	headersLower := map[string]string{
		"x-ratelimit-reset": "1800000000",
	}
	retry2, reset2 := parseRetryAfter(now, headersLower)
	if reset2.Unix() != 1800000000 || retry2 <= 0 {
		t.Errorf("lowercase header should also match, got retry=%d reset=%v", retry2, reset2)
	}

	// Retry-After canonical form.
	headersRetry := map[string]string{
		"Retry-After": "45",
	}
	retry3, reset3 := parseRetryAfter(now, headersRetry)
	if retry3 != 45 {
		t.Errorf("Retry-After canonical should give 45s, got %d", retry3)
	}
	if reset3.Unix() != now.Add(45*time.Second).Unix() {
		t.Errorf("reset should be now+45s, got %v", reset3)
	}
}

// TestLookupHeader_CaseInsensitive 验证 lookupHeader 对大小写折叠
// 各种情形都能正确命中, 是 429 校准路径修复的关键守门.
func TestLookupHeader_CaseInsensitive(t *testing.T) {
	cases := []struct {
		name    string
		headers map[string]string
		key     string
		want    string
		wantOK  bool
	}{
		{
			name:    "exact_match",
			headers: map[string]string{"X-RateLimit-Limit": "100"},
			key:     "X-RateLimit-Limit",
			want:    "100",
			wantOK:  true,
		},
		{
			name:    "canonical_lookup",
			headers: map[string]string{"X-Ratelimit-Limit": "100"},
			key:     "X-RateLimit-Limit",
			want:    "100",
			wantOK:  true,
		},
		{
			name:    "lowercase_key",
			headers: map[string]string{"x-ratelimit-limit": "100"},
			key:     "X-RateLimit-Limit",
			want:    "100",
			wantOK:  true,
		},
		{
			name:    "missing",
			headers: map[string]string{"X-Other": "v"},
			key:     "X-RateLimit-Limit",
			want:    "",
			wantOK:  false,
		},
		{
			name:    "nil_map",
			headers: nil,
			key:     "X-RateLimit-Limit",
			want:    "",
			wantOK:  false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := lookupHeader(tc.headers, tc.key)
			if ok != tc.wantOK {
				t.Errorf("ok mismatch: got %v want %v", ok, tc.wantOK)
			}
			if got != tc.want {
				t.Errorf("value mismatch: got %q want %q", got, tc.want)
			}
			if v := lookupHeaderValue(tc.headers, tc.key); v != tc.want {
				t.Errorf("lookupHeaderValue mismatch: got %q want %q", v, tc.want)
			}
		})
	}
}

const httpTimeFormat = "Mon, 02 Jan 2006 15:04:05 GMT"

func TestQuotaTracker_Record(t *testing.T) {
	// 集成测试, 需要真实 PostgreSQL. live-DB 路径已由 rls_helper_test.go
	// (OMNIFREE_TEST_DB_URL gated) 覆盖 Record/CorrectFromHeaders/Preflight;
	// 这里保留 short-mode skip 占位, 待 Round 6 接入 CI postgres 容器后补齐.
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	if os.Getenv("OMNIFREE_TEST_DB_URL") == "" {
		t.Skip("OMNIFREE_TEST_DB_URL not set; skipping live-DB integration test")
	}
}

func TestQuotaTracker_Preflight(t *testing.T) {
	// 同上: live-DB 覆盖见 rls_helper_test.go TestPreflight_RLSIsolation.
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	if os.Getenv("OMNIFREE_TEST_DB_URL") == "" {
		t.Skip("OMNIFREE_TEST_DB_URL not set; skipping live-DB integration test")
	}
}

func TestQuotaTracker_CorrectFromHeaders(t *testing.T) {
	// 同上: live-DB 覆盖见 rls_helper_test.go TestCorrectFromHeaders_SetsExhausted.
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	if os.Getenv("OMNIFREE_TEST_DB_URL") == "" {
		t.Skip("OMNIFREE_TEST_DB_URL not set; skipping live-DB integration test")
	}
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
