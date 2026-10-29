package compression

import (
	"testing"
)

func TestValidateSessionID(t *testing.T) {
	tests := []struct {
		name      string
		sessionID string
		wantErr   bool
	}{
		{
			name:      "valid UUID-like",
			sessionID: "abc123def456",
			wantErr:   false,
		},
		{
			name:      "valid with hyphens",
			sessionID: "user-abc-123-def",
			wantErr:   false,
		},
		{
			name:      "too short",
			sessionID: "abc123",
			wantErr:   true,
		},
		{
			name:      "numeric only",
			sessionID: "12345678",
			wantErr:   true,
		},
		{
			name:      "contains 'test'",
			sessionID: "test-session-123",
			wantErr:   true,
		},
		{
			name:      "contains 'demo'",
			sessionID: "demo-user-456",
			wantErr:   true,
		},
		{
			name:      "contains 'default'",
			sessionID: "default-12345678",
			wantErr:   true,
		},
		{
			name:      "contains 'session' (forbidden)",
			sessionID: "user-session-789",
			wantErr:   true,
		},
		{
			name:      "contains 'temp'",
			sessionID: "temp-abc123def",
			wantErr:   true,
		},
		{
			name:      "contains 'placeholder'",
			sessionID: "placeholder-xyz",
			wantErr:   true,
		},
		{
			name:      "valid alphanumeric 8+",
			sessionID: "usr12abc",
			wantErr:   false,
		},
		{
			name:      "valid with underscores",
			sessionID: "cursor_abc_123",
			wantErr:   false,
		},
		{
			name:      "edge case: exactly 8 chars",
			sessionID: "abcd1234",
			wantErr:   false,
		},
		{
			// 211 生产实测形态：客户端发 session_<22hex>，DeriveGatewayID
			// 映成 gw_session_*。此前被 'session' 子串规则误拒 ⇒ 每轮压缩
			// 都当新会话、压缩历史断链。前缀段 + ≥8 位熵必须放行。
			name:      "production: gw_session_ + hex entropy",
			sessionID: "gw_session_d0d6aa0caac6ff2bfbe5cac",
			wantErr:   false,
		},
		{
			name:      "production: bare session_ + hex entropy",
			sessionID: "session_d0d6aa0caac6ff2bfbe5cac",
			wantErr:   false,
		},
		{
			name:      "session-<uuid> client convention",
			sessionID: "session-3f2a9c1e-8b7d-4e55-9a2b-6c1d2e3f4a5b",
			wantErr:   false,
		},
		{
			name:      "session_ + short tail stays rejected",
			sessionID: "session_ab12",
			wantErr:   true,
		},
		{
			name:      "bare 'session' stays rejected",
			sessionID: "session",
			wantErr:   true,
		},
		{
			name:      "session + digits without separator stays rejected",
			sessionID: "session12345678",
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSessionID(tt.sessionID)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateSessionID(%q) error = %v, wantErr %v", tt.sessionID, err, tt.wantErr)
			}
		})
	}
}
