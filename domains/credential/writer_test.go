package credential

import (
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// TestCoolingDurationPerKind verifies coolingDuration() returns the expected
// Go-side default cooling window for each ErrorKind. Note: despite the old
// name ("MatchesPythonDefaults"), this table asserts the Go implementation's
// intended defaults, NOT a Python reference implementation — renamed 2026-08-09
// to stop implying a cross-language contract that does not exist.
func TestCoolingDurationPerKind(t *testing.T) {
	tests := []struct {
		kind errorsx.ErrorKind
		want time.Duration
	}{
		// KindConcurrent now uses 5-minute cooling (was 15s) so that
		// the credential stays out of rotation long enough for the
		// upstream's concurrency window to clear. Without this, the
		// same credential would be re-picked and re-fail in a tight
		// loop, masking the actual provider outage.
		{errorsx.KindConcurrent, 5 * time.Minute},
		// 2026-08-09: KindRateLimit cooling 调整为 3 分钟（原 15 分钟）。
		// rate_limit 通常是短期限流（每分钟配额耗尽），3 分钟后配额窗口
		// 通常已滚动。上游提供 Retry-After 时优先用其值。
		{errorsx.KindRateLimit, 3 * time.Minute},
		// 2026-07-09 (问题2): KindStreamTimeout now uses 5-minute cooling
		// (was 30s, shared with Transient/Timeout). Stream-no-feedback
		// failures (first_byte_timeout / stream_timeout / EOF-without-DONE)
		// need a longer cooling so the (credential,model) pair stays out of
		// the routable view long enough to stop the "frontend hangs but
		// credential still takes traffic" loop.
		{errorsx.KindStreamTimeout, 5 * time.Minute},
		{errorsx.KindTransient, 30 * time.Second},
		{errorsx.KindTimeout, 30 * time.Second},
		{errorsx.KindUpstreamDown, 60 * time.Second},
		{errorsx.KindUpstreamOverloaded, 60 * time.Second},
		{errorsx.KindNetwork, 120 * time.Second},
	}
	for _, tt := range tests {
		if got := coolingDuration(tt.kind, 0); got != tt.want {
			t.Fatalf("coolingDuration(%s)=%s want %s", tt.kind, got, tt.want)
		}
	}
}

func TestCoolingDurationHonorsRetryAfter(t *testing.T) {
	retryAfter := 42 * time.Second
	if got := coolingDuration(errorsx.KindRateLimit, retryAfter); got != retryAfter {
		t.Fatalf("coolingDuration retry-after=%s want %s", got, retryAfter)
	}
}

func TestTrimDetail(t *testing.T) {
	if got := trimDetail(""); got != nil {
		t.Fatal("empty detail should return nil")
	}
	detail := strings.Repeat("x", 501)
	got := trimDetail(detail)
	if got == nil {
		t.Fatal("non-empty detail should return pointer")
	}
	if len(*got) != 500 {
		t.Fatalf("trimmed length=%d want 500", len(*got))
	}
}

func TestInferQuotaRecoverAtIsFutureMidnight(t *testing.T) {
	for _, detail := range []string{"daily limit", "weekly quota", "monthly quota"} {
		got := inferQuotaRecoverAt(detail)
		if !got.After(time.Now().UTC()) {
			t.Fatalf("recover_at for %q is not in the future: %s", detail, got)
		}
		if got.Hour() != 0 || got.Minute() != 0 || got.Second() != 0 || got.Nanosecond() != 0 {
			t.Fatalf("recover_at for %q should be midnight UTC, got %s", detail, got)
		}
	}
}

// 2026-07-21 P0 fix: when the upstream body carries an explicit reset
// timestamp, inferQuotaRecoverAt must return it (so CredentialRecovery
// worker can flip the credential back to ready when the window opens)
// rather than fall back to day-based heuristics. 智谱AI 1310 returns
// "限额将在 YYYY-MM-DD HH:MM:SS 重置。" — the timestamp is the only
// reliable signal for when the upstream quota window resets.
func TestInferQuotaRecoverAtUsesExplicitTimestamp(t *testing.T) {
	want := time.Now().UTC().AddDate(0, 0, 3).
		Truncate(time.Hour).Add(14*time.Hour + 32*time.Minute + 20*time.Second)
	layout := "2006-01-02 15:04:05"
	wantStr := want.Format(layout)

	for _, detail := range []string{
		`您的限额将在 ` + wantStr + ` 重置。`,
		`You have exceeded the quota; will reset at ` + wantStr,
		`quota exceeded, retry at ` + wantStr,
	} {
		got := inferQuotaRecoverAt(detail)
		if !got.Equal(want) {
			t.Errorf("inferQuotaRecoverAt(%q) = %s, want %s", detail, got.Format(layout), wantStr)
		}
	}
}

func TestInferQuotaRecoverAtRejectsPastTimestamp(t *testing.T) {
	past := time.Now().UTC().AddDate(0, 0, -3).Format("2006-01-02 15:04:05")
	got := inferQuotaRecoverAt("您的限额将在 " + past + " 重置。")
	parsed, _ := time.Parse("2006-01-02 15:04:05", past)
	if got.Equal(parsed) {
		t.Fatalf("inferQuotaRecoverAt should not return a past timestamp %s", past)
	}
	if !got.After(time.Now().UTC()) {
		t.Fatalf("recover_at should still be in the future, got %s", got)
	}
}

// TestInferQuotaRecoverAtFiveHourWindow covers the 2026-08-18 fix: a
// quota-exhausted body that mentions a 5-hour window (智谱AI GLM Coding
// Plan) recovers at the NEXT 5-hour boundary (00/05/10/15/20 北京时间),
// not the next UTC midnight. The old default stretched a 凌晨 5 点重置的
// 窗口到北京 08:00，凭据白白多挂 3 小时。
func TestInferQuotaRecoverAtFiveHourWindow(t *testing.T) {
	// 2026-08-18 03:30 北京时间 = 2026-08-17 19:30 UTC。当前 5h 窗口
	// (00:00-05:00 北京) 在 05:00 结束，recover_at 必须是 05:00 北京
	// = 2026-08-17 21:00 UTC，而不是次日 UTC 零点（北京 08:00）。
	now := time.Date(2026, 8, 17, 19, 30, 0, 0, time.UTC)
	for _, detail := range []string{
		`{"error":"usage limit exceeded","window_type":"five_hour"}`,
		`usage limit exceeded, resets every 5 hours`,
		`You have exceeded your 5-hour usage window`,
		`本周期（每 5 小时）用量已达上限`,
		`5小时额度已用尽`,
	} {
		got := inferQuotaRecoverAtNow(detail, now)
		want := time.Date(2026, 8, 17, 21, 0, 0, 0, time.UTC)
		if !got.Equal(want) {
			t.Errorf("inferQuotaRecoverAtNow(%q) = %s, want next 5h boundary %s", detail, got.UTC(), want.UTC())
		}
	}

	// 恰好在窗口边界之后（05:00:01 北京 = 21:00:01 UTC）→ 下一边界 10:00 北京。
	now = time.Date(2026, 8, 17, 21, 0, 1, 0, time.UTC)
	got := inferQuotaRecoverAtNow(`usage limit exceeded every 5 hours`, now)
	want := time.Date(2026, 8, 18, 2, 0, 0, 0, time.UTC) // 10:00 北京
	if !got.Equal(want) {
		t.Errorf("boundary+1s: inferQuotaRecoverAtNow = %s, want %s", got.UTC(), want.UTC())
	}
}

// TestInferQuotaRecoverAtFiveHourRegexNegative guards against the
// five-hour branch swallowing unrelated bodies: a plain weekly/monthly
// message must keep the day-based heuristics.
func TestInferQuotaRecoverAtFiveHourRegexNegative(t *testing.T) {
	now := time.Date(2026, 8, 17, 19, 30, 0, 0, time.UTC)
	// "weekly" body keeps the next-Monday-UTC-midnight semantics.
	got := inferQuotaRecoverAtNow("You have exceeded your weekly usage cap", now)
	if got.Hour() != 0 || got.Minute() != 0 {
		t.Errorf("weekly body should still snap to UTC midnight, got %s", got.UTC())
	}
}

func TestNextFiveHourBoundaryAlignment(t *testing.T) {
	// Every returned boundary must be a 00/05/10/15/20 mark in UTC+8 and
	// strictly in the future.
	for _, utc := range []time.Time{
		time.Date(2026, 8, 17, 16, 0, 0, 0, time.UTC),   // 北京 00:00 整点
		time.Date(2026, 8, 17, 16, 0, 1, 0, time.UTC),   // 北京 00:00:01
		time.Date(2026, 8, 17, 20, 59, 59, 0, time.UTC), // 北京 04:59:59
	} {
		got := nextFiveHourBoundary(utc)
		if !got.After(utc) {
			t.Fatalf("boundary %s not after now %s", got, utc)
		}
		local := got.In(cstZone)
		if local.Hour()%5 != 0 || local.Minute() != 0 || local.Second() != 0 {
			t.Fatalf("boundary %s is not a 5h mark in UTC+8 (local %s)", got, local)
		}
	}
}
