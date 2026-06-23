package admin

import (
	"testing"
)

// TestDeriveModelEffectiveState_PriorityOrder covers the 5-state priority chain.
// Priority (first match wins):
//   1. credentialManualDisabled (整凭据被禁用) -> "manual_disabled"
//   2. binding_unavailable_reason == "manual_offline" -> "manual_disabled"
//   3. probe_state == "broken_confirmed" -> "probe_broken"
//   4. offer_available == false -> "offer_missing"
//   5. binding_available == false -> "binding_missing"
//   6. default -> "available"
func TestDeriveModelEffectiveState_PriorityOrder(t *testing.T) {
	mkStr := func(s string) *string { return &s }

	tests := []struct {
		name                    string
		ms                      *CredentialModelStatus
		credentialManualDisabled bool
		want                    string
	}{
		{
			name: "整凭据 manual_disabled 优先级最高",
			ms: &CredentialModelStatus{
				OfferAvailable:   false,
				BindingAvailable: false,
				ProbeState:       "broken_confirmed",
			},
			credentialManualDisabled: true,
			want:                    "manual_disabled",
		},
		{
			name: "per-model manual_offline 优先级高于 probe_broken",
			ms: &CredentialModelStatus{
				OfferAvailable:           true,
				BindingAvailable:         false,
				BindingUnavailableReason: mkStr("manual_offline"),
				ProbeState:               "broken_confirmed",
			},
			credentialManualDisabled: false,
			want:                    "manual_disabled",
		},
		{
			name: "probe_broken 优先级高于 offer_missing",
			ms: &CredentialModelStatus{
				OfferAvailable:   false,
				BindingAvailable: true,
				ProbeState:       "broken_confirmed",
			},
			credentialManualDisabled: false,
			want:                    "probe_broken",
		},
		{
			name: "offer_missing",
			ms: &CredentialModelStatus{
				OfferAvailable:   false,
				BindingAvailable: true,
				ProbeState:       "healthy_confirmed",
			},
			credentialManualDisabled: false,
			want:                    "offer_missing",
		},
		{
			name: "binding_missing",
			ms: &CredentialModelStatus{
				OfferAvailable:           true,
				BindingAvailable:         false,
				BindingUnavailableReason: mkStr("rate_limited"),
				ProbeState:               "healthy_confirmed",
			},
			credentialManualDisabled: false,
			want:                    "binding_missing",
		},
		{
			name: "available 全部正常",
			ms: &CredentialModelStatus{
				OfferAvailable:   true,
				BindingAvailable: true,
				ProbeState:       "healthy_confirmed",
			},
			credentialManualDisabled: false,
			want:                    "available",
		},
		{
			name: "probe unknown + binding 缺失 = binding_missing (probe 不参与后 3 档)",
			ms: &CredentialModelStatus{
				OfferAvailable:           true,
				BindingAvailable:         false,
				BindingUnavailableReason: mkStr("auth_failed"),
				ProbeState:               "unknown",
			},
			credentialManualDisabled: false,
			want:                    "binding_missing",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := deriveModelEffectiveState(tt.ms, tt.credentialManualDisabled)
			if got != tt.want {
				t.Fatalf("deriveModelEffectiveState() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestHumanizeDisabledReason covers the human-readable reason strings.
// Returns "" when fully available.
func TestHumanizeDisabledReason(t *testing.T) {
	mkStr := func(s string) *string { return &s }

	tests := []struct {
		name string
		ms   *CredentialModelStatus
		want string
	}{
		{
			name: "available 返回空",
			ms: &CredentialModelStatus{
				EffectiveState: "available",
			},
			want: "",
		},
		{
			name: "manual_offline 优先于 effective_state",
			ms: &CredentialModelStatus{
				EffectiveState:           "manual_disabled",
				BindingUnavailableReason: mkStr("manual_offline"),
			},
			want: "管理员手动下线",
		},
		{
			name: "整凭据 manual_disabled",
			ms: &CredentialModelStatus{
				EffectiveState: "manual_disabled",
			},
			want: "整凭据被禁用",
		},
		{
			name: "probe_broken",
			ms: &CredentialModelStatus{
				EffectiveState: "probe_broken",
			},
			want: "探测失败 (broken_confirmed)",
		},
		{
			name: "offer_missing 带原因",
			ms: &CredentialModelStatus{
				EffectiveState:         "offer_missing",
				OfferUnavailableReason: mkStr("deprecated"),
			},
			want: "Offer 不可用: deprecated",
		},
		{
			name: "offer_missing 无原因",
			ms: &CredentialModelStatus{
				EffectiveState: "offer_missing",
			},
			want: "Offer 缺失",
		},
		{
			name: "binding_missing 带原因",
			ms: &CredentialModelStatus{
				EffectiveState:           "binding_missing",
				BindingUnavailableReason: mkStr("rate_limited"),
			},
			want: "Binding 不可用: rate_limited",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := humanizeDisabledReason(tt.ms)
			if got != tt.want {
				t.Fatalf("humanizeDisabledReason() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestMonitorSummarySchemaVersion_NotZero ensures the schema version constant
// is set to a non-zero value, otherwise the cache would never invalidate.
func TestMonitorSummarySchemaVersion_NotZero(t *testing.T) {
	if monitorSummarySchemaVersion == 0 {
		t.Fatalf("monitorSummarySchemaVersion must be > 0; cache key would not differentiate schemas")
	}
}
