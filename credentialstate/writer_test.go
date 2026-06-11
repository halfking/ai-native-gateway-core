package credentialstate

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
		{errorsx.KindRateLimit, 900 * time.Second},
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
