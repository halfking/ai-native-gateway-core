package telemetry

import (
	"net/http/httptest"
	"testing"
)

func TestExtractClientIP(t *testing.T) {
	tests := []struct {
		name       string
		headers    map[string]string
		remoteAddr string
		expectedIP string
	}{
		{
			name:       "X-Real-IP present",
			headers:    map[string]string{"X-Real-IP": "203.0.113.5"},
			remoteAddr: "10.0.0.1:12345",
			expectedIP: "203.0.113.5",
		},
		{
			name:       "X-Forwarded-For with single IP",
			headers:    map[string]string{"X-Forwarded-For": "203.0.113.10"},
			remoteAddr: "10.0.0.1:12345",
			expectedIP: "203.0.113.10",
		},
		{
			name:       "X-Forwarded-For with chain",
			headers:    map[string]string{"X-Forwarded-For": "203.0.113.20, 10.0.0.5, 192.168.1.1"},
			remoteAddr: "10.0.0.1:12345",
			expectedIP: "203.0.113.20",
		},
		{
			name: "X-Real-IP takes precedence over X-Forwarded-For",
			headers: map[string]string{
				"X-Real-IP":       "203.0.113.30",
				"X-Forwarded-For": "203.0.113.40",
			},
			remoteAddr: "10.0.0.1:12345",
			expectedIP: "203.0.113.30",
		},
		{
			name:       "Fallback to RemoteAddr",
			headers:    map[string]string{},
			remoteAddr: "192.168.1.100:54321",
			expectedIP: "192.168.1.100",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/test", nil)
			req.RemoteAddr = tt.remoteAddr
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}

			got := ExtractClientIP(req)
			if got != tt.expectedIP {
				t.Errorf("ExtractClientIP() = %v, want %v", got, tt.expectedIP)
			}
		})
	}
}

func TestMaskAPIKey(t *testing.T) {
	tests := []struct {
		name     string
		apiKey   string
		expected string
	}{
		{
			name:     "Standard OpenAI key",
			apiKey:   "sk-1234abcd5678efgh9012ijkl",
			expected: "sk-1234a***",
		},
		{
			name:     "Short key",
			apiKey:   "abc123",
			expected: "***",
		},
		{
			name:     "Empty key",
			apiKey:   "",
			expected: "",
		},
		{
			name:     "Exactly 8 chars",
			apiKey:   "12345678",
			expected: "***",
		},
		{
			name:     "9 chars",
			apiKey:   "123456789",
			expected: "12345678***",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MaskAPIKey(tt.apiKey)
			if got != tt.expected {
				t.Errorf("MaskAPIKey(%q) = %q, want %q", tt.apiKey, got, tt.expected)
			}
		})
	}
}

func TestExtractAgentName(t *testing.T) {
	tests := []struct {
		name          string
		headers       map[string]string
		expectedAgent string
	}{
		{
			name:          "X-Agent-Name header present",
			headers:       map[string]string{"X-Agent-Name": "my-custom-agent"},
			expectedAgent: "my-custom-agent",
		},
		{
			name:          "Claude Code User-Agent",
			headers:       map[string]string{"User-Agent": "Claude-Code/1.0"},
			expectedAgent: "claude-code",
		},
		{
			name:          "OpenCode User-Agent",
			headers:       map[string]string{"User-Agent": "OpenCode/2.0"},
			expectedAgent: "opencode",
		},
		{
			name:          "Cursor User-Agent",
			headers:       map[string]string{"User-Agent": "Cursor/1.5"},
			expectedAgent: "cursor",
		},
		{
			name:          "Python client",
			headers:       map[string]string{"User-Agent": "python-requests/2.28.0"},
			expectedAgent: "python-client",
		},
		{
			name:          "Curl",
			headers:       map[string]string{"User-Agent": "curl/7.88.1"},
			expectedAgent: "curl",
		},
		{
			name:          "No User-Agent",
			headers:       map[string]string{},
			expectedAgent: "unknown",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/test", nil)
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}

			got := ExtractAgentName(req)
			if got != tt.expectedAgent {
				t.Errorf("ExtractAgentName() = %v, want %v", got, tt.expectedAgent)
			}
		})
	}
}

func TestExtractAgentType(t *testing.T) {
	tests := []struct {
		name         string
		headers      map[string]string
		expectedType string
	}{
		{
			name:         "X-Agent-Type header present",
			headers:      map[string]string{"X-Agent-Type": "internal"},
			expectedType: "internal",
		},
		{
			name:         "CLI - curl",
			headers:      map[string]string{"User-Agent": "curl/7.88.1"},
			expectedType: "cli",
		},
		{
			name:         "CLI - claude-code",
			headers:      map[string]string{"User-Agent": "Claude-Code/1.0"},
			expectedType: "cli",
		},
		{
			name:         "API - python",
			headers:      map[string]string{"User-Agent": "python-requests/2.28.0"},
			expectedType: "api",
		},
		{
			name:         "API - Go",
			headers:      map[string]string{"User-Agent": "Go-http-client/1.1"},
			expectedType: "api",
		},
		{
			name:         "Bot - crawler",
			headers:      map[string]string{"User-Agent": "Googlebot/2.1"},
			expectedType: "bot",
		},
		{
			name:         "Mobile - iPhone",
			headers:      map[string]string{"User-Agent": "Mozilla/5.0 (iPhone; CPU iPhone OS 16_0)"},
			expectedType: "mobile",
		},
		{
			name:         "Mobile - Android",
			headers:      map[string]string{"User-Agent": "Mozilla/5.0 (Linux; Android 13)"},
			expectedType: "mobile",
		},
		{
			name:         "Web - Chrome",
			headers:      map[string]string{"User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/120.0"},
			expectedType: "web",
		},
		{
			name:         "Unknown",
			headers:      map[string]string{"User-Agent": "CustomClient/1.0"},
			expectedType: "unknown",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/test", nil)
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}

			got := ExtractAgentType(req)
			if got != tt.expectedType {
				t.Errorf("ExtractAgentType() = %v, want %v", got, tt.expectedType)
			}
		})
	}
}

func TestNewRequestMetadata(t *testing.T) {
	req := httptest.NewRequest("GET", "/test", nil)
	req.RemoteAddr = "192.168.1.100:54321"
	req.Header.Set("X-Real-IP", "203.0.113.5")
	req.Header.Set("X-Forwarded-For", "203.0.113.5, 10.0.0.1")
	req.Header.Set("User-Agent", "Claude-Code/1.0")

	meta := NewRequestMetadata(req)

	if meta.ClientIP != "203.0.113.5" {
		t.Errorf("ClientIP = %v, want 203.0.113.5", meta.ClientIP)
	}

	if meta.ClientForwardedFor != "203.0.113.5, 10.0.0.1" {
		t.Errorf("ClientForwardedFor = %v, want '203.0.113.5, 10.0.0.1'", meta.ClientForwardedFor)
	}

	if meta.AgentName != "claude-code" {
		t.Errorf("AgentName = %v, want claude-code", meta.AgentName)
	}

	if meta.AgentType != "cli" {
		t.Errorf("AgentType = %v, want cli", meta.AgentType)
	}
}

func TestExtractForwardedFor(t *testing.T) {
	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("X-Forwarded-For", "203.0.113.1, 10.0.0.1, 192.168.1.1")

	got := ExtractForwardedFor(req)
	expected := "203.0.113.1, 10.0.0.1, 192.168.1.1"

	if got != expected {
		t.Errorf("ExtractForwardedFor() = %v, want %v", got, expected)
	}
}
