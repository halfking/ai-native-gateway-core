package ursm

import (
	"testing"
	"time"
)

func TestClassifyError(t *testing.T) {
	tests := []struct {
		name      string
		errorKind string
		want      ErrorClass
	}{
		// 忽略类错误
		{"empty", "", ErrorClassIgnored},
		{"canceled", "canceled", ErrorClassIgnored},
		{"client_bug", "client_bug", ErrorClassIgnored},
		{"model_not_found_client", "model_not_found_client", ErrorClassIgnored},
		{"invalid_request", "invalid_request", ErrorClassIgnored},

		// 永久故障
		{"auth", "auth", ErrorClassPermanent},
		{"auth_failed", "auth_failed", ErrorClassPermanent},
		{"auth_revoked", "auth_revoked", ErrorClassPermanent},
		{"model_not_found", "model_not_found", ErrorClassPermanent},
		{"quota_permanent", "quota_permanent", ErrorClassPermanent},

		// 临时故障
		{"rate_limit", "rate_limit", ErrorClassTransient},
		{"upstream_down", "upstream_down", ErrorClassTransient},
		{"timeout", "timeout", ErrorClassTransient},
		{"network_error", "network_error", ErrorClassTransient},

		// 未知错误默认为临时故障
		{"unknown", "unknown_error", ErrorClassTransient},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClassifyError(tt.errorKind); got != tt.want {
				t.Errorf("ClassifyError(%q) = %v, want %v", tt.errorKind, got, tt.want)
			}
		})
	}
}

func TestErrorClassString(t *testing.T) {
	tests := []struct {
		class ErrorClass
		want  string
	}{
		{ErrorClassIgnored, "ignored"},
		{ErrorClassTransient, "transient"},
		{ErrorClassPermanent, "permanent"},
		{ErrorClass(999), "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := tt.class.String(); got != tt.want {
				t.Errorf("ErrorClass.String() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestValidateRecordRequest(t *testing.T) {
	tests := []struct {
		name    string
		req     RecordRequestAPI
		wantErr bool
	}{
		{
			name: "valid_request",
			req: RecordRequestAPI{
				RequestID:    "req-123",
				CredentialID: 1,
				RawModel:     "gpt-4",
				SessionID:    "sess-456",
				Success:      true,
				LatencyMs:    100,
				Timestamp:    time.Now(),
			},
			wantErr: false,
		},
		{
			name: "missing_request_id",
			req: RecordRequestAPI{
				CredentialID: 1,
				RawModel:     "gpt-4",
				SessionID:    "sess-456",
				Timestamp:    time.Now(),
			},
			wantErr: true,
		},
		{
			name: "invalid_credential_id",
			req: RecordRequestAPI{
				RequestID:    "req-123",
				CredentialID: 0,
				RawModel:     "gpt-4",
				SessionID:    "sess-456",
				Timestamp:    time.Now(),
			},
			wantErr: true,
		},
		{
			name: "negative_latency",
			req: RecordRequestAPI{
				RequestID:    "req-123",
				CredentialID: 1,
				RawModel:     "gpt-4",
				SessionID:    "sess-456",
				LatencyMs:    -1,
				Timestamp:    time.Now(),
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateRecordRequest(tt.req)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateRecordRequest() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateUpdateProvider(t *testing.T) {
	enabled := true
	disabled := false

	tests := []struct {
		name    string
		req     UpdateProviderAPI
		wantErr bool
	}{
		{
			name: "valid_enable",
			req: UpdateProviderAPI{
				ProviderID: 1,
				Enabled:    &enabled,
				Reason:     "test",
				Actor:      "admin",
			},
			wantErr: false,
		},
		{
			name: "valid_manual_disable",
			req: UpdateProviderAPI{
				ProviderID:     1,
				ManualDisabled: &disabled,
				Reason:         "test",
				Actor:          "admin",
			},
			wantErr: false,
		},
		{
			name: "no_fields",
			req: UpdateProviderAPI{
				ProviderID: 1,
				Reason:     "test",
				Actor:      "admin",
			},
			wantErr: true,
		},
		{
			name: "invalid_provider_id",
			req: UpdateProviderAPI{
				ProviderID: 0,
				Enabled:    &enabled,
				Reason:     "test",
				Actor:      "admin",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateUpdateProvider(tt.req)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateUpdateProvider() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateUpdateCredential(t *testing.T) {
	availState := "ready"
	quotaState := "ok"

	tests := []struct {
		name    string
		req     UpdateCredentialAPI
		wantErr bool
	}{
		{
			name: "valid_availability",
			req: UpdateCredentialAPI{
				CredentialID:      1,
				AvailabilityState: &availState,
				Reason:            "test",
				Actor:             "admin",
			},
			wantErr: false,
		},
		{
			name: "valid_quota",
			req: UpdateCredentialAPI{
				CredentialID: 1,
				QuotaState:   &quotaState,
				Reason:       "test",
				Actor:        "admin",
			},
			wantErr: false,
		},
		{
			name: "invalid_availability_state",
			req: UpdateCredentialAPI{
				CredentialID:      1,
				AvailabilityState: stringPtr("invalid"),
				Reason:            "test",
				Actor:             "admin",
			},
			wantErr: true,
		},
		{
			name: "no_fields",
			req: UpdateCredentialAPI{
				CredentialID: 1,
				Reason:       "test",
				Actor:        "admin",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateUpdateCredential(tt.req)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateUpdateCredential() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
