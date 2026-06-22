package compressor

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
