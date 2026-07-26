package ursm

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestProviderState_IsAvailable(t *testing.T) {
	tests := []struct {
		name     string
		state    ProviderState
		expected bool
	}{
		{
			name: "enabled and not manually disabled",
			state: ProviderState{
				ProviderID:     1,
				Enabled:        true,
				ManualDisabled: false,
			},
			expected: true,
		},
		{
			name: "disabled",
			state: ProviderState{
				ProviderID:     2,
				Enabled:        false,
				ManualDisabled: false,
			},
			expected: false,
		},
		{
			name: "manually disabled",
			state: ProviderState{
				ProviderID:     3,
				Enabled:        true,
				ManualDisabled: true,
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.state.IsAvailable()
			assert.Equal(t, tt.expected, result)

			if !result {
				reason := tt.state.UnavailableReason()
				assert.NotEmpty(t, reason)
			}
		})
	}
}

func TestCredentialState_IsAvailable(t *testing.T) {
	tests := []struct {
		name     string
		state    CredentialState
		expected bool
		reason   string
	}{
		{
			name: "fully available",
			state: CredentialState{
				CredentialID:      1,
				Status:            "active",
				LifecycleStatus:   "active",
				AvailabilityState: "ready",
				QuotaState:        "ok",
				ManualDisabled:    false,
			},
			expected: true,
		},
		{
			name: "status inactive",
			state: CredentialState{
				CredentialID:    2,
				Status:          "inactive",
				LifecycleStatus: "active",
			},
			expected: false,
			reason:   "credential_status_inactive",
		},
		{
			name: "auth failed",
			state: CredentialState{
				CredentialID:      3,
				Status:            "active",
				LifecycleStatus:   "active",
				AvailabilityState: "auth_failed",
			},
			expected: false,
			reason:   "credential_auth_failed",
		},
		{
			name: "quota exhausted",
			state: CredentialState{
				CredentialID:      4,
				Status:            "active",
				LifecycleStatus:   "active",
				AvailabilityState: "ready",
				QuotaState:        "permanently_exhausted",
			},
			expected: false,
			reason:   "credential_quota_permanently_exhausted",
		},
		{
			name: "manually disabled",
			state: CredentialState{
				CredentialID:    5,
				Status:          "active",
				LifecycleStatus: "active",
				ManualDisabled:  true,
			},
			expected: false,
			reason:   "credential_manual_disabled",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.state.IsAvailable()
			assert.Equal(t, tt.expected, result)

			if !result {
				reason := tt.state.UnavailableReason()
				assert.NotEmpty(t, reason)
				if tt.reason != "" {
					assert.Equal(t, tt.reason, reason)
				}
			}
		})
	}
}

func TestModelState_IsAvailable(t *testing.T) {
	tests := []struct {
		name     string
		state    ModelState
		expected bool
	}{
		{
			name: "fully available",
			state: ModelState{
				CredentialID:     1,
				RawModel:         "gpt-4",
				OfferAvailable:   true,
				BindingAvailable: true,
				ProbeState:       "healthy_confirmed",
			},
			expected: true,
		},
		{
			name: "offer unavailable",
			state: ModelState{
				CredentialID:     2,
				RawModel:         "gpt-4",
				OfferAvailable:   false,
				BindingAvailable: true,
				ProbeState:       "unknown",
			},
			expected: false,
		},
		{
			name: "binding unavailable",
			state: ModelState{
				CredentialID:     3,
				RawModel:         "gpt-4",
				OfferAvailable:   true,
				BindingAvailable: false,
				ProbeState:       "unknown",
			},
			expected: false,
		},
		{
			name: "broken confirmed",
			state: ModelState{
				CredentialID:     4,
				RawModel:         "gpt-4",
				OfferAvailable:   true,
				BindingAvailable: true,
				ProbeState:       "broken_confirmed",
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.state.IsAvailable()
			assert.Equal(t, tt.expected, result)

			if !result {
				reason := tt.state.UnavailableReason()
				assert.NotEmpty(t, reason)
			}
		})
	}
}

func TestNodeState_IsAvailable(t *testing.T) {
	now := time.Now()
	futureTime := now.Add(1 * time.Hour)
	pastTime := now.Add(-1 * time.Hour)

	tests := []struct {
		name     string
		state    NodeState
		expected bool
	}{
		{
			name: "healthy node",
			state: NodeState{
				CredentialID:        1,
				RawModel:            "gpt-4",
				ConsecutiveFailures: 0,
				SuccessCount:        100,
				Disabled:            false,
			},
			expected: true,
		},
		{
			name: "consecutive failures below threshold",
			state: NodeState{
				CredentialID:        2,
				RawModel:            "gpt-4",
				ConsecutiveFailures: 2,
				Disabled:            false,
			},
			expected: true,
		},
		{
			name: "consecutive failures at threshold",
			state: NodeState{
				CredentialID:        3,
				RawModel:            "gpt-4",
				ConsecutiveFailures: 3,
				Disabled:            false,
			},
			expected: false,
		},
		{
			name: "disabled with future expiry",
			state: NodeState{
				CredentialID:        4,
				RawModel:            "gpt-4",
				ConsecutiveFailures: 0,
				Disabled:            true,
				DisabledUntil:       &futureTime,
			},
			expected: false,
		},
		{
			name: "disabled with past expiry",
			state: NodeState{
				CredentialID:        5,
				RawModel:            "gpt-4",
				ConsecutiveFailures: 0,
				Disabled:            true,
				DisabledUntil:       &pastTime,
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.state.IsAvailable()
			assert.Equal(t, tt.expected, result)

			if !result {
				reason := tt.state.UnavailableReason()
				assert.NotEmpty(t, reason)
			}
		})
	}
}

func TestLayer_String(t *testing.T) {
	tests := []struct {
		layer    Layer
		expected string
	}{
		{LayerProvider, "provider"},
		{LayerCredential, "credential"},
		{LayerModel, "model"},
		{LayerNode, "node"},
		{Layer(999), "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			result := tt.layer.String()
			assert.Equal(t, tt.expected, result)
		})
	}
}
