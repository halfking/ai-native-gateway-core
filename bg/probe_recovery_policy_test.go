package bg

import (
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/errorsx"
)

func TestProbeBackoffForKindNetworkStaysShort(t *testing.T) {
	for _, kind := range []errorsx.ErrorKind{
		errorsx.KindNetwork, errorsx.KindTimeout, errorsx.KindTransient,
		errorsx.KindUpstreamDown, errorsx.KindEmptyResponse,
	} {
		if got := ProbeBackoffForKind(kind, 1); got != 5*time.Second {
			t.Fatalf("%s attempt 1 = %v, want 5s", kind, got)
		}
		if got := ProbeBackoffForKind(kind, 4); got != time.Minute {
			t.Fatalf("%s attempt 4 = %v, want 60s", kind, got)
		}
		if got := ProbeBackoffForKind(kind, 8); got != time.Minute {
			t.Fatalf("%s attempt 8 = %v, want capped 60s", kind, got)
		}
	}
}

func TestProbeBackoffForKindQuotaUsesFixedPolicy(t *testing.T) {
	if got := ProbeBackoffForKind(errorsx.KindQuotaPeriodic, 3); got != 5*time.Minute {
		t.Fatalf("periodic = %v, want 5m", got)
	}
	if got := ProbeBackoffForKind(errorsx.KindQuotaBalance, 8); got != 2*time.Minute {
		t.Fatalf("balance = %v, want 2m", got)
	}
}

func TestProbeBackoffForErrCodeClassifiesTransport(t *testing.T) {
	if got := ProbeBackoffForErrCode("network_error", 8); got != time.Minute {
		t.Fatalf("network_error = %v, want 60s", got)
	}
	if got := ProbeBackoffForErrCode("timeout", 3); got != 30*time.Second {
		t.Fatalf("timeout attempt 3 = %v, want 30s", got)
	}
	if got := ProbeBackoffForErrCode("http_503", 2); got != 15*time.Second {
		t.Fatalf("http_503 attempt 2 = %v, want 15s", got)
	}
	if got := ProbeBackoffForErrCode("quota_periodic", 9); got != 5*time.Minute {
		t.Fatalf("quota_periodic = %v, want 5m", got)
	}
}

// Rate-limit / concurrency kinds carry a scheduling interval in the policy
// (3m / 5m). Re-probing a 429 after 5s just burns the limit again, so the
// policy interval must act as a floor under the generic ladder.
func TestProbeBackoffForKindRateLimitHonoursPolicyFloor(t *testing.T) {
	if got := ProbeBackoffForKind(errorsx.KindRateLimit, 1); got != 3*time.Minute {
		t.Fatalf("rate_limit attempt 1 = %v, want 3m floor", got)
	}
	if got := ProbeBackoffForKind(errorsx.KindRateLimit, 7); got != 6*time.Hour {
		t.Fatalf("rate_limit attempt 7 = %v, want ladder tail 6h", got)
	}
	if got := ProbeBackoffForErrCode("http_429", 2); got != 3*time.Minute {
		t.Fatalf("http_429 attempt 2 = %v, want 3m floor", got)
	}
}

func TestClassifyProbeErrCodeUnknownIsNotAuth(t *testing.T) {
	if got := classifyProbeErrCode("gateway_pin_unsupported"); got != "" {
		t.Fatalf("unknown code classified as %q, want empty (generic ladder)", got)
	}
	if got := ProbeBackoffForErrCode("gateway_pin_unsupported", 1); got != 5*time.Second {
		t.Fatalf("unknown attempt 1 = %v, want generic ladder 5s", got)
	}
}

func TestQueuePumpHoldoffDoesNotBuryFastRetry(t *testing.T) {
	if nodeProbeQueuePumpHoldoff > time.Minute {
		t.Fatalf("pump holdoff %v buries 5s/30s network retries", nodeProbeQueuePumpHoldoff)
	}
	if nodeProbeQueuePumpHoldoff < 15*time.Second {
		t.Fatalf("pump holdoff %v is too short and will re-enqueue in-flight rows", nodeProbeQueuePumpHoldoff)
	}
}
