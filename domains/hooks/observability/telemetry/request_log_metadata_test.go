package telemetry

import (
	"net/http"
	"testing"
)

// Test P1.4 observability metadata extraction from HTTP request
func TestExtractObservabilityMetadata(t *testing.T) {
	tests := []struct {
		name     string
		headers  map[string]string
		wantIP   string
		wantFwd  string
		wantName string
		wantType string
	}{
		{
			name: "X-Real-IP takes precedence",
			headers: map[string]string{
				"X-Real-IP":       "1.2.3.4",
				"X-Forwarded-For": "5.6.7.8, 9.10.11.12",
			},
			wantIP:   "1.2.3.4",
			wantFwd:  "5.6.7.8, 9.10.11.12",
			wantName: "unknown",
			wantType: "unknown",
		},
		{
			name: "X-Forwarded-For first IP when no X-Real-IP",
			headers: map[string]string{
				"X-Forwarded-For": "1.2.3.4, 5.6.7.8",
			},
			wantIP:   "1.2.3.4",
			wantFwd:  "1.2.3.4, 5.6.7.8",
			wantName: "unknown",
			wantType: "unknown",
		},
		{
			name: "claude-code agent detection",
			headers: map[string]string{
				"User-Agent": "claude-code/1.0",
			},
			wantIP:   "127.0.0.1",
			wantFwd:  "",
			wantName: "claude-code",
			wantType: "cli",
		},
		{
			name: "opencode agent detection",
			headers: map[string]string{
				"User-Agent": "opencode/2.3.4",
			},
			wantIP:   "127.0.0.1",
			wantFwd:  "",
			wantName: "opencode",
			wantType: "cli",
		},
		{
			name: "X-Agent-Name header precedence",
			headers: map[string]string{
				"X-Agent-Name": "custom-bot",
				"User-Agent":   "curl/7.68.0",
			},
			wantIP:   "127.0.0.1",
			wantFwd:  "",
			wantName: "custom-bot",
			wantType: "cli",
		},
		{
			name: "X-Agent-Type header precedence",
			headers: map[string]string{
				"X-Agent-Type": "api",
				"User-Agent":   "Mozilla/5.0",
			},
			wantIP:   "127.0.0.1",
			wantFwd:  "",
			wantName: "unknown",
			wantType: "api",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, _ := http.NewRequest("POST", "/v1/chat/completions", nil)
			req.RemoteAddr = "127.0.0.1:12345"
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}

			ctx := ExtractObservabilityContext(req)

			if ctx.ClientIP != tt.wantIP {
				t.Errorf("ClientIP = %q, want %q", ctx.ClientIP, tt.wantIP)
			}
			if ctx.ClientForwardedFor != tt.wantFwd {
				t.Errorf("ClientForwardedFor = %q, want %q", ctx.ClientForwardedFor, tt.wantFwd)
			}
			if ctx.AgentName != tt.wantName {
				t.Errorf("AgentName = %q, want %q", ctx.AgentName, tt.wantName)
			}
			if ctx.AgentType != tt.wantType {
				t.Errorf("AgentType = %q, want %q", ctx.AgentType, tt.wantType)
			}
		})
	}
}

// Test API key fingerprint masking
func TestAPIKeyFingerprint(t *testing.T) {
	tests := []struct {
		name string
		key  string
		want string
	}{
		{
			name: "standard API key",
			key:  "sk-1234abcd5678efgh",
			want: "sk-1234a***",
		},
		{
			name: "short key",
			key:  "abc",
			want: "***",
		},
		{
			name: "empty key",
			key:  "",
			want: "",
		},
		{
			name: "exact 8 chars",
			key:  "12345678",
			want: "***",
		},
		{
			name: "9 chars",
			key:  "123456789",
			want: "12345678***",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MaskAPIKeyFingerprint(tt.key)
			if got != tt.want {
				t.Errorf("MaskAPIKeyFingerprint(%q) = %q, want %q", tt.key, got, tt.want)
			}
		})
	}
}

// Test X-Forwarded-For sanitization (bounded to 512 chars)
func TestSanitizeForwardedFor(t *testing.T) {
	longChain := ""
	for i := 0; i < 100; i++ {
		if i > 0 {
			longChain += ", "
		}
		longChain += "192.168.1.1"
	}

	tests := []struct {
		name    string
		input   string
		wantLen int
	}{
		{
			name:    "short chain",
			input:   "1.2.3.4, 5.6.7.8",
			wantLen: len("1.2.3.4, 5.6.7.8"),
		},
		{
			name:    "long chain bounded to 512",
			input:   longChain,
			wantLen: 512,
		},
		{
			name:    "empty",
			input:   "",
			wantLen: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizeForwardedFor(tt.input)
			if len(got) != tt.wantLen {
				t.Errorf("SanitizeForwardedFor() len = %d, want %d", len(got), tt.wantLen)
			}
			if tt.wantLen == 512 && len(tt.input) > 512 {
				// Verify truncation happened
				if len(got) > 512 {
					t.Errorf("SanitizeForwardedFor() did not truncate: len = %d", len(got))
				}
			}
		})
	}
}

// Test RequestLogEntry enrichment with observability metadata
func TestEnrichRequestLogWithContext(t *testing.T) {
	ctx := &ObservabilityContext{
		ClientIP:           "1.2.3.4",
		ClientForwardedFor: "1.2.3.4, 5.6.7.8",
		AgentName:          "claude-code",
		AgentType:          "cli",
		APIKeyFingerprint:  "sk-1234ab",
		SessionTitle:       "Test Session",
		TaskID:             "TASK-123",
		UpstreamEndpoint:   "https://api.anthropic.com/v1/messages",
		UpstreamProtocol:   "anthropic",
		ProtocolConversion: ptrBool(true),
	}

	entry := &RequestLogEntry{
		RequestID: "test-request-1",
		TenantID:  "tenant-1",
		Success:   true,
	}

	EnrichRequestLogWithContext(entry, ctx)

	if entry.ClientIP == nil || *entry.ClientIP != "1.2.3.4" {
		t.Errorf("ClientIP not enriched: %v", entry.ClientIP)
	}
	if entry.ClientForwardedFor == nil || *entry.ClientForwardedFor != "1.2.3.4, 5.6.7.8" {
		t.Errorf("ClientForwardedFor not enriched: %v", entry.ClientForwardedFor)
	}
	if entry.AgentName == nil || *entry.AgentName != "claude-code" {
		t.Errorf("AgentName not enriched: %v", entry.AgentName)
	}
	if entry.AgentType == nil || *entry.AgentType != "cli" {
		t.Errorf("AgentType not enriched: %v", entry.AgentType)
	}
	if entry.APIKeyFingerprint == nil || *entry.APIKeyFingerprint != "sk-1234ab" {
		t.Errorf("APIKeyFingerprint not enriched: %v", entry.APIKeyFingerprint)
	}
	if entry.SessionTitle == nil || *entry.SessionTitle != "Test Session" {
		t.Errorf("SessionTitle not enriched: %v", entry.SessionTitle)
	}
	if entry.TaskID == nil || *entry.TaskID != "TASK-123" {
		t.Errorf("TaskID not enriched: %v", entry.TaskID)
	}
	if entry.UpstreamEndpoint == nil || *entry.UpstreamEndpoint != "https://api.anthropic.com/v1/messages" {
		t.Errorf("UpstreamEndpoint not enriched: %v", entry.UpstreamEndpoint)
	}
	if entry.UpstreamProtocol == nil || *entry.UpstreamProtocol != "anthropic" {
		t.Errorf("UpstreamProtocol not enriched: %v", entry.UpstreamProtocol)
	}
	if entry.ProtocolConversion == nil || *entry.ProtocolConversion != true {
		t.Errorf("ProtocolConversion not enriched: %v", entry.ProtocolConversion)
	}
}

func ptrBool(b bool) *bool {
	return &b
}
