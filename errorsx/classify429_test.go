package errorsx

import (
	"testing"
	"time"
)

// TestClassifyQuota429Body covers the body-keyword classification that the
// OmniFree free_quota_tracker path uses to distinguish transient rate_limit
// from periodic quota_exhausted. These are the OmniRoute classify429.ts
// patterns ported to Go.
func TestClassifyQuota429Body(t *testing.T) {
	tests := []struct {
		name string
		body string
		want ErrorKind
	}{
		{
			name: "empty body defaults to rate_limit",
			body: "",
			want: KindRateLimit,
		},
		{
			name: "plain rate limit message",
			body: `{"error":{"message":"Too many requests, please slow down"}}`,
			want: KindRateLimit,
		},
		{
			name: "daily limit quota exhausted (Cloudflare-style)",
			body: `{"error":{"message":"You have exceeded your daily free allocation"}}`,
			want: KindQuotaPermanent,
		},
		{
			name: "monthly quota with reset timestamp → periodic",
			body: `{"error":{"message":"usage limit exceeded","window_type":"monthly","quota will reset at 2026-08-11 00:00:00"}}`,
			want: KindQuotaPeriodic,
		},
		{
			name: "Google Gemini RESOURCE_EXHAUSTED with reset after → periodic",
			body: `{"error":{"code":429,"message":"Quota exceeded for quota metric 'GenerateContent' and limit 'GenerateContent' per minute. The quota will reset after 2026-08-10T12:34:56Z.","status":"RESOURCE_EXHAUSTED"}}`,
			want: KindQuotaPeriodic,
		},
		{
			name: "Antigravity individual quota reached → permanent",
			body: `{"error":{"message":"individual quota reached"}}`,
			want: KindQuotaPermanent,
		},
		{
			name: "INSUFFICIENT_G1_CREDITS_BALANCE → permanent",
			body: `{"error":{"code":"INSUFFICIENT_G1_CREDITS_BALANCE"}}`,
			want: KindQuotaPermanent,
		},
		{
			name: "out of credits → permanent",
			body: `{"error":{"message":"You are out of credits"}}`,
			want: KindQuotaPermanent,
		},
		{
			name: "智谱AI 1310 with reset → periodic",
			body: `{"error":{"code":"1310","message":"您的配额已用尽，限额将在 2026-08-11 00:00:00 重置"}}`,
			want: KindQuotaPeriodic,
		},
		{
			name: "budget exceeded no reset → permanent",
			body: `{"error":{"code":"budget_exceeded","message":"Organization balance insufficient"}}`,
			want: KindQuotaPermanent,
		},
		{
			name: "hard limit reached → permanent",
			body: `{"error":{"message":"You have hit your hard limit"}}`,
			want: KindQuotaPermanent,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyQuota429Body([]byte(tt.body))
			if got != tt.want {
				t.Errorf("ClassifyQuota429Body(%q) = %q, want %q", tt.body, got, tt.want)
			}
		})
	}
}

// TestNextQuotaReset covers the reset-time computation shared by both the
// credential writer (paid path) and the OmniFree quota tracker (free path).
func TestNextQuotaReset(t *testing.T) {
	now := time.Date(2026, 8, 10, 14, 30, 0, 0, time.UTC)
	nextMidnight := time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC)
	nextMonth := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		body string
		want time.Time
	}{
		{
			name: "no hint → next UTC midnight (daily default)",
			body: `{"error":"too many requests"}`,
			want: nextMidnight,
		},
		{
			name: "monthly keyword → first of next month",
			body: `{"error":"your monthly limit has been reached"}`,
			want: nextMonth,
		},
		{
			name: "explicit reset timestamp in body wins",
			body: `{"error":"quota will reset at 2026-08-15 08:00:00"}`,
			want: time.Date(2026, 8, 15, 8, 0, 0, 0, time.UTC),
		},
		{
			name: "past timestamp ignored → midnight default",
			body: `{"error":"quota reset at 2020-01-01 00:00:00"}`,
			want: nextMidnight,
		},
		{
			name: "Chinese 月 → next month",
			body: `{"error":"您的每月配额已用尽"}`,
			want: nextMonth,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NextQuotaReset(tt.body, now)
			if !got.Equal(tt.want) {
				t.Errorf("NextQuotaReset(%q) = %v, want %v", tt.body, got, tt.want)
			}
		})
	}
}

func TestNextQuotaReset_UsesInjectedNowForTimestampStaleness(t *testing.T) {
	// This timestamp is long before the real wall clock used in CI, but it is in
	// the future relative to the injected `now`. NextQuotaReset must therefore
	// accept it; using time.Now() internally would incorrectly discard it.
	now := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	want := time.Date(2020, 1, 1, 0, 5, 0, 0, time.UTC)
	got := NextQuotaReset(`{"error":"quota resets at 2020-01-01 00:05:00"}`, now)
	if !got.Equal(want) {
		t.Fatalf("NextQuotaReset uses wall clock? got %v, want %v", got, want)
	}
}

// TestNextQuotaReset_RFC3339Timestamps covers ISO-8601 with timezone suffixes
// (Google Gemini, OpenAI proxies, Zhipu). Without these layouts the scanner
// falls through to next-UTC-midnight and the credential is parked for hours
// after the real reset.
func TestNextQuotaReset_RFC3339Timestamps(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 30, 0, 0, time.UTC)
	tests := []struct {
		name string
		body string
		want time.Time
	}{
		{
			name: "Gemini-style Z suffix",
			body: `{"error":"quota will reset after 2026-08-10T12:34:56Z"}`,
			want: time.Date(2026, 8, 10, 12, 34, 56, 0, time.UTC),
		},
		{
			name: "numeric offset (+08:00)",
			body: `{"error":"reset at 2026-08-10T20:34:56+08:00"}`,
			want: time.Date(2026, 8, 10, 12, 34, 56, 0, time.UTC),
		},
		{
			name: "negative offset",
			body: `{"error":"reset at 2026-08-10T04:34:56-08:00"}`,
			want: time.Date(2026, 8, 10, 12, 34, 56, 0, time.UTC),
		},
		{
			name: "naive layout still works",
			body: `{"error":"reset at 2026-08-10 13:00:00"}`,
			want: time.Date(2026, 8, 10, 13, 0, 0, 0, time.UTC),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NextQuotaReset(tt.body, now)
			if !got.Equal(tt.want) {
				t.Fatalf("NextQuotaReset(%q) = %v, want %v", tt.body, got, tt.want)
			}
		})
	}
}

// TestNextQuotaReset_RFC3339NanoFractionalSeconds locks the RFC3339Nano
// layout (fractional seconds) in classify.go's scanQuotaResetTimestamp.
// Providers like Google Gemini RESOURCE_EXHAUSTED and OpenAI proxies may
// emit sub-second precision (e.g. "2026-08-10T12:34:56.789Z"). Without
// the RFC3339Nano layout the scanner falls back to RFC3339 (whole seconds)
// or — worse — to the next-UTC-midnight default, parking the credential
// for hours past the real reset.
func TestNextQuotaReset_RFC3339NanoFractionalSeconds(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 30, 0, 0, time.UTC)
	tests := []struct {
		name string
		body string
		want time.Time
	}{
		{
			name: "milliseconds with Z suffix",
			body: `{"error":"quota will reset after 2026-08-10T12:34:56.789Z"}`,
			want: time.Date(2026, 8, 10, 12, 34, 56, 789_000_000, time.UTC),
		},
		{
			name: "microseconds with +08:00 offset",
			body: `{"error":"reset at 2026-08-10T20:34:56.123456+08:00"}`,
			want: time.Date(2026, 8, 10, 12, 34, 56, 123_456_000, time.UTC),
		},
		{
			name: "nanoseconds with -05:00 offset",
			body: `{"error":"reset at 2026-08-10T07:34:56.000000001-05:00"}`,
			want: time.Date(2026, 8, 10, 12, 34, 56, 1, time.UTC),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NextQuotaReset(tt.body, now)
			if !got.Equal(tt.want) {
				t.Fatalf("NextQuotaReset(%q) = %v, want %v", tt.body, got, tt.want)
			}
		})
	}
}
