package streaming

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/authentication"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/externalhost"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/externalhost/contract"
)

// mockHookHost implements HookHost for testing
type mockHookHost struct {
	onRequestFunc  func(ctx context.Context, hookCtx *contract.Context, req *contract.RequestPayload) *externalhost.Outcome
	onResponseFunc func(ctx context.Context, hookCtx *contract.Context, resp *contract.ResponsePayload) *externalhost.Outcome
	timeoutDur     time.Duration
}

func (m *mockHookHost) OnRequest(ctx context.Context, hookCtx *contract.Context, req *contract.RequestPayload) *externalhost.Outcome {
	if m.onRequestFunc != nil {
		return m.onRequestFunc(ctx, hookCtx, req)
	}
	return &externalhost.Outcome{Decision: contract.DecisionContinue}
}

func (m *mockHookHost) OnResponse(ctx context.Context, hookCtx *contract.Context, resp *contract.ResponsePayload) *externalhost.Outcome {
	if m.onResponseFunc != nil {
		return m.onResponseFunc(ctx, hookCtx, resp)
	}
	return &externalhost.Outcome{Decision: contract.DecisionContinue}
}

func (m *mockHookHost) Timeout() time.Duration {
	if m.timeoutDur > 0 {
		return m.timeoutDur
	}
	return 5 * time.Second
}

// TestSetExternalHooks verifies the SetExternalHooks method works correctly
func TestSetExternalHooks(t *testing.T) {
	handler := &ChatHandler{}

	// Verify nil by default
	if handler.externalHooks != nil {
		t.Error("Expected externalHooks to be nil by default")
	}

	// Create and set integrator
	mockHost := &mockHookHost{}
	integrator := externalhost.NewPipelineIntegrator(mockHost)
	handler.SetExternalHooks(integrator)

	// Verify it was set
	if handler.externalHooks == nil {
		t.Error("Expected externalHooks to be set after calling SetExternalHooks")
	}

	// Verify can be set to nil
	handler.SetExternalHooks(nil)
	if handler.externalHooks != nil {
		t.Error("Expected externalHooks to be nil after setting to nil")
	}
}

// TestDetermineOrigin verifies origin classification logic
func TestDetermineOrigin(t *testing.T) {
	tests := []struct {
		name       string
		remoteAddr string
		keyInfo    *authentication.KeyInfo
		want       contract.Origin
	}{
		{
			name:       "localhost IPv4",
			remoteAddr: "127.0.0.1:12345",
			keyInfo:    nil,
			want:       contract.OriginLocal,
		},
		{
			name:       "localhost IPv6",
			remoteAddr: "[::1]:12345",
			keyInfo:    nil,
			want:       contract.OriginLocal,
		},
		{
			name:       "private network 10.x (treated as remote per W3.4 privacy)",
			remoteAddr: "10.0.0.5:12345",
			keyInfo:    nil,
			want:       contract.OriginRemote,
		},
		{
			name:       "private network 192.168.x (treated as remote per W3.4 privacy)",
			remoteAddr: "192.168.1.100:12345",
			keyInfo:    nil,
			want:       contract.OriginRemote,
		},
		{
			name:       "private network 172.16.x (treated as remote per W3.4 privacy)",
			remoteAddr: "172.16.0.1:12345",
			keyInfo:    nil,
			want:       contract.OriginRemote,
		},
		{
			name:       "private network 172.31.x (treated as remote per W3.4 privacy)",
			remoteAddr: "172.31.255.255:12345",
			keyInfo:    nil,
			want:       contract.OriginRemote,
		},
		{
			name:       "loopback 127.x.y.z any address in 127/8",
			remoteAddr: "127.5.6.7:12345",
			keyInfo:    nil,
			want:       contract.OriginLocal,
		},
		{
			name:       "gateway key (internal)",
			remoteAddr: "203.0.113.1:12345",
			keyInfo:    &authentication.KeyInfo{IsInternal: true},
			want:       contract.OriginGatewayKey,
		},
		{
			name:       "remote origin",
			remoteAddr: "203.0.113.1:12345",
			keyInfo:    nil,
			want:       contract.OriginRemote,
		},
		{
			name:       "remote with non-internal key",
			remoteAddr: "203.0.113.1:12345",
			keyInfo:    &authentication.KeyInfo{IsInternal: false},
			want:       contract.OriginRemote,
		},
		{
			name:       "public IP 172.32.x (not in private range)",
			remoteAddr: "172.32.0.1:12345",
			keyInfo:    nil,
			want:       contract.OriginRemote,
		},
		{
			name:       "public IP 172.15.x (not in private range)",
			remoteAddr: "172.15.255.255:12345",
			keyInfo:    nil,
			want:       contract.OriginRemote,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
			req.RemoteAddr = tt.remoteAddr

			got := determineOrigin(req, tt.keyInfo)
			if got != tt.want {
				t.Errorf("determineOrigin() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestIsLoopbackAddr verifies the loopback detection helper (2026-11-27 修订：
// 只认回环 127/8 与 ::1；私有网段按 remote 走隐私路径).
func TestIsLoopbackAddr(t *testing.T) {
	tests := []struct {
		name       string
		remoteAddr string
		want       bool
	}{
		{"localhost", "127.0.0.1:8080", true},
		{"localhost no port", "127.0.0.1", true},
		{"127/8 any address", "127.5.6.7:8080", true},
		{"127/8 edge", "127.255.255.255:8080", true},
		{"IPv6 localhost", "::1", true},
		{"IPv6 localhost no port", "::1", true},
		// 私有网段一律不算 loopback（W3.4 隐私原则：私网≠本机）
		{"10.x.x.x", "10.0.0.1:8080", false},
		{"10.x.x.x edge", "10.255.255.255:8080", false},
		{"192.168.x.x", "192.168.1.1:8080", false},
		{"192.168.x.x edge", "192.168.255.255:8080", false},
		{"172.16.x.x start", "172.16.0.0:8080", false},
		{"172.31.x.x end", "172.31.255.255:8080", false},
		{"public IP", "203.0.113.1:8080", false},
		{"public IP no port", "8.8.8.8", false},
		{"empty", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isLoopbackAddr(tt.remoteAddr)
			if got != tt.want {
				t.Errorf("isLoopbackAddr(%q) = %v, want %v", tt.remoteAddr, got, tt.want)
			}
		})
	}
}
