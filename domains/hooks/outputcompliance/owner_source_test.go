package outputcompliance

import (
	"context"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
)

func TestAuthenticatedCallerOwnerControlsRedaction(t *testing.T) {
	lookup := func(_ context.Context, sessionID, tenantID string) string {
		if sessionID != "gw-session" || tenantID != "tenant-a" {
			t.Fatalf("unexpected lookup scope %q %q", sessionID, tenantID)
		}
		return "alice"
	}
	for _, tc := range []struct {
		name        string
		callerOwner string
		redact      bool
	}{
		{"same_owner", "alice", false},
		{"different_owner", "bob", true},
		{"missing_authenticated_owner", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			it := NewOutputComplianceInterceptor(&protocolChecker{}, lookup)
			result, err := it.processBody(context.Background(), &response.InterceptRequest{
				SessionID: "gw-session", TenantID: "tenant-a", CallerOwner: tc.callerOwner,
				ResponseBody: []byte(`{"choices":[{"message":{"content":"13800138000"}}]}`),
			})
			if err != nil || result == nil || result.ShouldBlock {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if got := len(result.ModifiedBody) > 0; got != tc.redact {
				t.Fatalf("redacted=%t, want %t; body=%s", got, tc.redact, result.ModifiedBody)
			}
		})
	}
}
