package ursm

import (
	"context"
	"testing"
	"time"
)

// mockProbeSubmitter 模拟探测提交器
type mockProbeSubmitter struct {
	submitted []struct {
		credentialID int
		model        string
	}
}

func (m *mockProbeSubmitter) SubmitModelProbe(ctx context.Context, credentialID int, model string) {
	m.submitted = append(m.submitted, struct {
		credentialID int
		model        string
	}{credentialID, model})
}

// setupTestManager 创建测试用Manager（简化版，不依赖真实DB/Redis）
func setupTestManager(t *testing.T) *Manager {
	// 使用nil DB和Redis，仅用于单元测试
	m := &Manager{
		config: DefaultConfig(),
	}

	// 创建简化的BatchWriter（用于测试）
	m.batchWriter = &BatchWriter{}

	return m
}

func TestRecordRequest_ValidationError(t *testing.T) {
	m := setupTestManager(t)

	tests := []struct {
		name string
		req  RecordRequestAPI
	}{
		{
			name: "missing_request_id",
			req: RecordRequestAPI{
				CredentialID: 1,
				RawModel:     "gpt-4",
				SessionID:    "sess-1",
				Timestamp:    time.Now(),
			},
		},
		{
			name: "invalid_credential_id",
			req: RecordRequestAPI{
				RequestID:    "req-1",
				CredentialID: 0,
				RawModel:     "gpt-4",
				SessionID:    "sess-1",
				Timestamp:    time.Now(),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := m.RecordRequest(context.Background(), tt.req)
			if err == nil {
				t.Error("expected validation error, got nil")
			}
		})
	}
}

func TestRecordRequest_ErrorClassification(t *testing.T) {
	tests := []struct {
		name       string
		errorKind  string
		wantIgnore bool
	}{
		{"ignored_canceled", "canceled", true},
		{"permanent_auth", "auth", false},
		{"transient_rate_limit", "rate_limit", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			class := ClassifyError(tt.errorKind)
			if (class == ErrorClassIgnored) != tt.wantIgnore {
				t.Errorf("ClassifyError(%q) = %v, wantIgnore %v", tt.errorKind, class, tt.wantIgnore)
			}
		})
	}
}

func TestUpdateProvider_ValidationError(t *testing.T) {
	m := setupTestManager(t)
	ctx := context.Background()

	tests := []struct {
		name string
		req  UpdateProviderAPI
	}{
		{
			name: "no_fields",
			req: UpdateProviderAPI{
				ProviderID: 1,
				Reason:     "test",
				Actor:      "admin",
			},
		},
		{
			name: "invalid_provider_id",
			req: UpdateProviderAPI{
				ProviderID:     0,
				ManualDisabled: boolPtr(true),
				Reason:         "test",
				Actor:          "admin",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := m.UpdateProvider(ctx, tt.req)
			if err == nil {
				t.Error("expected validation error, got nil")
			}
		})
	}
}

func TestUpdateCredential_ValidationError(t *testing.T) {
	m := setupTestManager(t)
	ctx := context.Background()

	tests := []struct {
		name string
		req  UpdateCredentialAPI
	}{
		{
			name: "no_fields",
			req: UpdateCredentialAPI{
				CredentialID: 1,
				Reason:       "test",
				Actor:        "admin",
			},
		},
		{
			name: "invalid_credential_id",
			req: UpdateCredentialAPI{
				CredentialID:      0,
				AvailabilityState: stringPtr("ready"),
				Reason:            "test",
				Actor:             "admin",
			},
		},
		{
			name: "invalid_availability_state",
			req: UpdateCredentialAPI{
				CredentialID:      1,
				AvailabilityState: stringPtr("invalid"),
				Reason:            "test",
				Actor:             "admin",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := m.UpdateCredential(ctx, tt.req)
			if err == nil {
				t.Error("expected validation error, got nil")
			}
		})
	}
}

func TestRecordProbeResult(t *testing.T) {
	m := setupTestManager(t)

	// 跳过这个测试，因为需要真实的DB连接
	t.Skip("skipping integration test that requires DB")

	result := ProbeResult{
		CredentialID:      1,
		ProbeModel:        "gpt-4",
		HealthStatus:      "healthy",
		AvailabilityState: "ready",
		ProbeState:        "healthy_confirmed",
		LatencyMs:         50,
		Timestamp:         time.Now(),
		Source:            "probe",
	}

	_ = m.RecordProbeResult(context.Background(), result)
}

func boolPtr(b bool) *bool {
	return &b
}

// 基准测试
func BenchmarkClassifyError(b *testing.B) {
	errorKinds := []string{
		"",
		"canceled",
		"auth",
		"rate_limit",
		"unknown_error",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ClassifyError(errorKinds[i%len(errorKinds)])
	}
}
