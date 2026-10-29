package executors

import (
	"net/http/httptest"
	"testing"
)

func TestClientTokenUsesBoundedClientType(t *testing.T) {
	for _, tt := range []struct {
		header string
		want   string
	}{
		{" Cursor ", "alice|cursor"},
		{"CLAUDE-CODE", "alice|claude-code"},
		{"my-custom-agent", "alice|unknown"},
		{"cursor|other", "alice|unknown"},
		{"   ", "alice|unknown"},
	} {
		req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
		req.Header.Set("X-Gw-Client-Type", tt.header)
		if got := clientTokenOf("alice", extractClientType(req)); got != tt.want {
			t.Errorf("header %q produced holder %q, want %q", tt.header, got, tt.want)
		}
	}
}

// TestExecutorsExtractClientType_MatchesStreamingTable（2026-10-29
// identification round）：本包 extractClientType 已委托
// clienttype.DetectFromRequest（原「行为一致镜像副本」在事实层面漂移过——
// 缺线材实态 MiniMaxCode 与 claude-cli）。委托后两处行为由实现天然一致，
// 这里钉住本包兜底路径上的关键形态。
func TestExecutorsExtractClientType_MatchesStreamingTable(t *testing.T) {
	for _, tt := range []struct {
		ua   string
		want string
	}{
		{"claude-cli/2.1.75 (external, cli)", "claude-code"},
		{"MiniMaxCode", "minimax-code"},
		{"MiniMax-Code/1.0.0 (macos; arm64)", "minimax-code"},
		{"ZCode/3.16.3 ai-sdk/provider-utils/4.0.39 runtime/node.js/24", "zcode"},
		{"DeepSeek-Code/1.0.0 (darwin; arm64)", "deepseek-code"},
		{"MiniMaxCoder/1.0", ""}, // 形近词负臂
		{"ZCodex/2.0", ""},       // 形近词负臂
	} {
		req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
		req.Header.Set("User-Agent", tt.ua)
		if got := extractClientType(req); got != tt.want {
			t.Errorf("extractClientType(%q) = %q, want %q", tt.ua, got, tt.want)
		}
	}
}
