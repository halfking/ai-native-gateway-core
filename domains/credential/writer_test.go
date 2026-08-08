package credential

import (
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/errorsx"
)

func TestCoolingDurationMatchesPythonDefaults(t *testing.T) {
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
