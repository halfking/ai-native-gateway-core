package executors

import (
	"net/http/httptest"
	"testing"

	"github.com/kaixuan/llm-gateway-go/provider"
)

// 批判式审计轮（2026-10-28）补测：上一轮的端到端只覆盖了 openai 协议
// 入向（legacy 路径）；zcode 的实际形态是 anthropic 协议入向（/v1/messages）
// → IR 分支。该路径上 nativePair 只经 `irReq.NativePassthrough` 影响
// Extensions 还原；IR-handled 字段（thinking 等）走方言渲染（必需的协议
// 转换，非「参数剥离」）。本测试把这条路径钉死：
//
//	正臂（nativePair=true）  ：Extensions 原样透传 + thinking 渲染为 glm 方言
//	对照臂（nativePair=false）：Extensions 被方言守卫裁剪（门有牙）
//
// Extensions 靶字段选 `moderation`：KindDialectOnly 且登记方言
// [openai-chat, responses] 不含 glm ⇒ dst=glm 时 paramreg 判 Drop ——
// 这是「非原生对必然被裁、原生对必须保留」的可区分字段。
// （`enable_thinking` 是 KindPortable，Portable 不按方言裁 ⇒ 区分不出；
//
//	IR-handled 字段不走 Extensions，不受 NativePassthrough 影响。）
func TestFinalizeNativePair_AnthropicInbound_VendorExtensionSurvives(t *testing.T) {
	// zcode 实际形态：anthropic 协议入向（/v1/messages）→ GLM 候选。
	anthropicBody := `{
		"model": "glm-5.3",
		"max_tokens": 1024,
		"messages": [{"role":"user","content":"hi"}],
		"thinking": {"type":"enabled","budget_tokens":4096},
		"search_parameters": {"mode": "auto"},
		"zcode_vendor_flag": {"a": 1}
	}`
	executor := &Executor{IR: &irAdapterForTest{}}
	cand := provider.Candidate{
		ProviderID:   7,
		CredentialID: 55,
		Protocol:     "openai-completions",
		CatalogCode:  "zhipu",
		RawModel:     "glm-5.3",
	}
	params := &ExecParams{
		R:              httptest.NewRequest("POST", "/v1/messages", nil),
		ClientModel:    "glm-5.3",
		OutboundModel:  "glm-5.3",
		ClientProtocol: "anthropic-messages",
	}

	t.Run("nativePair=true: extension verbatim + thinking rendered for glm", func(t *testing.T) {
		body, err := executor.finalizeOpenAIUpstreamBodyNative(params, cand, []byte(anthropicBody), true)
		if err != nil {
			t.Fatalf("finalize: %v", err)
		}
		m := decodeUpstreamBody(t, body)
		if _, ok := m["search_parameters"]; !ok {
			t.Fatalf("native pair: search_parameters dropped by dialect guard (NativePassthrough not wired?):\n%s", body)
		}
		if _, ok := m["zcode_vendor_flag"]; !ok {
			t.Fatalf("native pair: unregistered vendor field dropped:\n%s", body)
		}
		// thinking 是 IR-handled 字段：协议转换（anthropic → glm 方言）必须
		// 仍然生效 —— 直通不等于丢掉协议语义。
		th, ok := m["thinking"].(map[string]any)
		if !ok {
			t.Fatalf("native pair: thinking missing (glm dialect rendering broken):\n%s", body)
		}
		if th["type"] != "enabled" {
			t.Fatalf("native pair: thinking.type = %v, want \"enabled\" (glm dialect keeps enabled)\n%s", th["type"], body)
		}
	})

	t.Run("nativePair=false: dialect guard still crops search_parameters", func(t *testing.T) {
		body, err := executor.finalizeOpenAIUpstreamBodyNative(params, cand, []byte(anthropicBody), false)
		if err != nil {
			t.Fatalf("finalize: %v", err)
		}
		m := decodeUpstreamBody(t, body)
		if _, ok := m["search_parameters"]; ok {
			t.Fatalf("non-native: dialect guard did NOT crop search_parameters:\n%s", body)
		}
		if _, ok := m["zcode_vendor_flag"]; !ok {
			t.Fatalf("non-native: unregistered field must stay fail-open:\n%s", body)
		}
	})
}
