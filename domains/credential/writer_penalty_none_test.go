package credential

import (
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/errorsx"
)

func TestCoolingDurationPenaltyNoneIsZero(t *testing.T) {
	for _, kind := range []errorsx.ErrorKind{
		errorsx.KindClientBug,
		errorsx.KindContextLength,
		errorsx.KindContentFilter,
		errorsx.KindConversion,
	} {
		if got := coolingDuration(kind, 0); got != 0 {
			t.Fatalf("coolingDuration(%s) = %s, want 0 (PenaltyNone)", kind, got)
		}
		// Retry-After 也不能打穿 none 归责。
		if got := coolingDuration(kind, time.Minute); got != 0 {
			t.Fatalf("coolingDuration(%s, retryAfter) = %s, want 0", kind, got)
		}
	}
	// 前提在场：可冷却 kind 仍有正时长。
	if got := coolingDuration(errorsx.KindRateLimit, 0); got <= 0 {
		t.Fatalf("coolingDuration(rate_limit) = %s, want >0", got)
	}
}
