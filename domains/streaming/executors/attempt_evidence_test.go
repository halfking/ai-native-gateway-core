package executors

import (
	"testing"
	"time"
)

func TestResponseConfirmationUsesDispatchTimeWhenTraceUnavailable(t *testing.T) {
	dispatch := time.Date(2026, 11, 23, 10, 1, 0, 0, time.UTC)
	if got := sentEvidenceTime(0, dispatch); !got.Equal(dispatch) {
		t.Fatalf("fallback=%v want dispatch=%v", got, dispatch)
	}
}

func TestExecutorAttemptKeepsParentAndUsesVerifiedTrafficClass(t *testing.T) {
	for _, tc := range []struct {
		origin, wantClass, wantClient string
		businessKey                   bool
	}{
		{"self_check", "platform", "platform-automation", false},
		{"", "business", "", true},
		{"", "unknown", "", false},
	} {
		class, client, _, tenant, request := providerAttemptClassification(&ExecParams{
			PlatformOrigin: tc.origin, AttemptBusinessKey: tc.businessKey, TenantID: "a", RequestID: "server-request",
		})
		if class != tc.wantClass || client != tc.wantClient || tenant != "a" || request != "server-request" {
			t.Errorf("class=%s client=%s tenant=%s request=%s", class, client, tenant, request)
		}
	}
}
