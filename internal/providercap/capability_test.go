// Package providercap — capability_test.go
//
// 2026-09-25 vapeur/hxt-local 回归：openai-responses 协议必须解析出
// /v1/responses 探针端点（此前静默落入 chat 默认，探针 max_tokens=1 被
// vapeur 以 400 "Could not finish the message ..." 拒绝）。
package providercap

import (
	"net/http"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/upstreamurl"
)

func TestResolve_OpenAIResponsesUsesResponsesProbeEndpoint(t *testing.T) {
	cases := []struct {
		protocol string
		want     upstreamurl.Endpoint
	}{
		{"openai-responses", upstreamurl.EpResponses},
		{"openai-completions", upstreamurl.EpChatCompletions},
		{"", upstreamurl.EpChatCompletions},
		{"anthropic-messages", upstreamurl.EpMessages},
	}
	for _, tc := range cases {
		desc := Resolve(tc.protocol, "")
		if desc.ChatProbeEndpoint != tc.want {
			t.Errorf("Resolve(%q).ChatProbeEndpoint = %v, want %v", tc.protocol, desc.ChatProbeEndpoint, tc.want)
		}
	}
}

func TestResponsesProbeMaxOutputTokens_RespectsAPIFloor(t *testing.T) {
	// OpenAI Responses API 官方下限 16；vapeur 2026-09-25 实测 5 →
	// 400 "Invalid 'max_output_tokens': integer below minimum value.
	// Expected a value >= 16"。
	if ResponsesProbeMaxOutputTokens < 16 {
		t.Fatalf("ResponsesProbeMaxOutputTokens = %d, want >= 16", ResponsesProbeMaxOutputTokens)
	}
}

func TestProbeEndpointURL_ResponsesBuild(t *testing.T) {
	desc := Resolve("openai-responses", "")
	got := ProbeEndpointURL("https://api.vapeur.ai/v1", desc)
	if got != "https://api.vapeur.ai/v1/responses" {
		t.Fatalf("ProbeEndpointURL = %q, want https://api.vapeur.ai/v1/responses", got)
	}
}

// AuthStyle 兜底：openai-responses 分支不得改变默认 Bearer 认证形态。
func TestResolve_OpenAIResponsesKeepsBearerAuth(t *testing.T) {
	desc := Resolve("openai-responses", "")
	req, _ := http.NewRequest(http.MethodPost, "https://api.vapeur.ai/v1/responses", nil)
	ApplyAuthHeaders(req, desc, "k")
	if req.Header.Get("Authorization") != "Bearer k" {
		t.Fatalf("auth header = %q, want Bearer k", req.Header.Get("Authorization"))
	}
	if req.Header.Get("x-api-key") != "" {
		t.Fatalf("responses probes must not carry anthropic x-api-key header")
	}
}
