package streaming

import (
	"net/http/httptest"
	"testing"
)

func TestParseGatewayCapabilities(t *testing.T) {
	r := httptest.NewRequest("POST", "/v1/responses", nil)
	r.Header.Add("X-Gw-Capabilities", " Status-Events, durable-recovery ")
	r.Header.Add("X-Gw-Capabilities", "DURABLE-RECOVERY,unknown")
	caps := ParseGatewayCapabilities(r)
	if !caps.StatusEvents || !caps.DurableRecovery {
		t.Fatalf("capabilities = %+v", caps)
	}
}

func TestParsePreferRespondAsync(t *testing.T) {
	r := httptest.NewRequest("POST", "/v1/responses", nil)
	r.Header.Set("Prefer", "wait=10, RESPOND-ASYNC; handling=lenient")
	if !PrefersRespondAsync(r) {
		t.Fatal("respond-async preference was not detected")
	}
}

func TestEvaluateDurableEligibility(t *testing.T) {
	base := DurableEligibilityInput{
		DurableEnabled: true, TenantAllowed: true, DurableHeader: true,
		Stream: true, APIKeyID: 7, TenantID: "tenant-a", SessionExists: true,
		SessionAPIKeyID: 7, SessionTenantID: "tenant-a",
	}
	if got := EvaluateDurableEligibility(base); !got.Allowed {
		t.Fatalf("valid durable stream rejected: %+v", got)
	}
	cases := []struct {
		name   string
		mutate func(*DurableEligibilityInput)
		reason string
	}{
		{"feature disabled", func(v *DurableEligibilityInput) { v.DurableEnabled = false }, "durable_disabled"},
		{"tenant denied", func(v *DurableEligibilityInput) { v.TenantAllowed = false }, "tenant_not_allowed"},
		{"header missing", func(v *DurableEligibilityInput) { v.DurableHeader = false }, "durable_not_requested"},
		{"session provisional", func(v *DurableEligibilityInput) { v.SessionExists = false }, "session_not_persisted"},
		{"session foreign key", func(v *DurableEligibilityInput) { v.SessionAPIKeyID = 8 }, "session_owner_mismatch"},
		{"session foreign tenant", func(v *DurableEligibilityInput) { v.SessionTenantID = "tenant-b" }, "session_owner_mismatch"},
		{"nonstream no async", func(v *DurableEligibilityInput) { v.Stream = false }, "async_capability_required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := base
			tc.mutate(&in)
			got := EvaluateDurableEligibility(in)
			if got.Allowed || got.Reason != tc.reason {
				t.Fatalf("eligibility = %+v, want denied %q", got, tc.reason)
			}
		})
	}
	nonStream := base
	nonStream.Stream = false
	nonStream.DurableRecoveryCapability = true
	if got := EvaluateDurableEligibility(nonStream); !got.Allowed {
		t.Fatalf("capability-enabled nonstream rejected: %+v", got)
	}
	nonStream.DurableRecoveryCapability = false
	nonStream.PreferRespondAsync = true
	if got := EvaluateDurableEligibility(nonStream); !got.Allowed {
		t.Fatalf("respond-async nonstream rejected: %+v", got)
	}
}

func TestDurableHeaderRequiresExplicitTrue(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  bool
	}{
		{"true", true}, {" TRUE ", true}, {"1", false}, {"yes", false}, {"", false},
	} {
		r := httptest.NewRequest("POST", "/v1/messages", nil)
		r.Header.Set("X-Gw-Durable", tc.value)
		if got := RequestsDurable(r); got != tc.want {
			t.Errorf("RequestsDurable(%q) = %v, want %v", tc.value, got, tc.want)
		}
	}
}
