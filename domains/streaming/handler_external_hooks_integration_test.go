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

			got := determineOrigin(req, tt.keyInfo, 0)
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

// TestDetermineOriginTrustedProxy (W3 XFF 信任链, 2026-11-28) 验证受信代理层数
// 对来源判定的影响：同机 nginx（hops=1）之后 RemoteAddr 恒为 127.0.0.1，
// 必须按 XFF 穿透地址判类；链短于声明层数时 fail-closed 按 remote。
func TestDetermineOriginTrustedProxy(t *testing.T) {
	tests := []struct {
		name       string
		remoteAddr string
		xff        string
		hops       int
		keyInfo    *authentication.KeyInfo
		want       contract.Origin
	}{
		{
			name:       "nginx same-host: external client behind one proxy",
			remoteAddr: "127.0.0.1:443",
			xff:        "203.0.113.7",
			hops:       1,
			keyInfo:    nil,
			want:       contract.OriginRemote,
		},
		{
			name:       "nginx same-host: loopback client behind proxy stays local",
			remoteAddr: "127.0.0.1:443",
			xff:        "127.0.0.1",
			hops:       1,
			keyInfo:    nil,
			want:       contract.OriginLocal,
		},
		{
			name:       "nginx same-host: gateway key classified via XFF client",
			remoteAddr: "127.0.0.1:443",
			xff:        "203.0.113.7",
			hops:       1,
			keyInfo:    &authentication.KeyInfo{IsInternal: true},
			want:       contract.OriginGatewayKey,
		},
		{
			name:       "two proxy layers: right-anchored second element",
			remoteAddr: "10.0.0.2:443",
			xff:        "203.0.113.7, 10.0.0.1",
			hops:       2,
			keyInfo:    nil,
			want:       contract.OriginRemote,
		},
		{
			name:       "two proxy layers: proxied loopback inner hop is local",
			remoteAddr: "10.0.0.2:443",
			xff:        "127.0.0.1, 10.0.0.1",
			hops:       2,
			keyInfo:    nil,
			want:       contract.OriginLocal,
		},
		{
			name:       "chain shorter than declared hops: fail-closed remote",
			remoteAddr: "127.0.0.1:443",
			xff:        "203.0.113.7",
			hops:       2,
			keyInfo:    nil,
			want:       contract.OriginRemote,
		},
		{
			name:       "missing XFF with hops declared: fail-closed remote",
			remoteAddr: "127.0.0.1:443",
			xff:        "",
			hops:       1,
			keyInfo:    nil,
			want:       contract.OriginRemote,
		},
		{
			name:       "blank XFF elements do not count toward hops",
			remoteAddr: "127.0.0.1:443",
			xff:        "203.0.113.7, , ",
			hops:       1,
			keyInfo:    nil,
			want:       contract.OriginRemote,
		},
		{
			name:       "hops=0 ignores XFF (default behavior unchanged)",
			remoteAddr: "127.0.0.1:443",
			xff:        "203.0.113.7",
			hops:       0,
			keyInfo:    nil,
			want:       contract.OriginLocal,
		},
		{
			name:       "forged XFF without trusted hops is ignored (spoof attempt)",
			remoteAddr: "192.168.1.50:5555",
			xff:        "127.0.0.1",
			hops:       0,
			keyInfo:    nil,
			want:       contract.OriginRemote,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
			req.RemoteAddr = tt.remoteAddr
			if tt.xff != "" {
				req.Header.Set("X-Forwarded-For", tt.xff)
			}

			got := determineOrigin(req, tt.keyInfo, tt.hops)
			if got != tt.want {
				t.Errorf("determineOrigin(hops=%d) = %v, want %v", tt.hops, got, tt.want)
			}
		})
	}
}

// TestSetExternalHookTrustedProxyHops 验证 setter 的负值收敛与默认零值。
func TestSetExternalHookTrustedProxyHops(t *testing.T) {
	handler := &ChatHandler{}
	if handler.externalHookTrustedProxyHops != 0 {
		t.Fatal("expected default hops = 0")
	}
	handler.SetExternalHookTrustedProxyHops(1)
	if handler.externalHookTrustedProxyHops != 1 {
		t.Fatalf("hops = %d, want 1", handler.externalHookTrustedProxyHops)
	}
	handler.SetExternalHookTrustedProxyHops(-3)
	if handler.externalHookTrustedProxyHops != 0 {
		t.Fatalf("negative hops = %d, want 0", handler.externalHookTrustedProxyHops)
	}
}
