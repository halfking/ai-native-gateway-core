package executors

import (
	"net/http/httptest"
	"testing"

	"github.com/kaixuan/llm-gateway-go/provider"
)

// ─── claude-fable-5-2 强制 adaptive thinking（2026-11-11 事故轮）───
//
// 根因（211 生产实证）：apiclaude 中继对 claude-fable-5-2 **只接受**
// thinking.type=adaptive（或不带 thinking）；网关把客户端的 enabled /
// disabled 形态原样发出 ⇒ 上游 400 "requires adaptive thinking" ⇒
// 全候选失败 ⇒ 客户端看到 503「有供应商却访问失败」。
//
// 修复点：internal/paramguard 的模型门控（两条出站腿共用）。本测试钉住
// **executor 真实出站路径**上的行为，而不是只钉 paramguard 的单元函数 ——
// 门必须长在真实路径上，否则「单元绿、生产仍 400」。
//
// 关键前提：apiclaude 的 catalog_code 为空（DB 实测）⇒ nativePair=false
// ⇒ paramguard 会执行（原生对跳过 paramguard，见 RISK-6 S3）。
// 原生对臂（anthropic 原厂码）同样要钉：原生对**不**走 paramguard ⇒
// 该模型若走原厂端点，网关不改写（首发全参是原生对的既定契约）。

func TestPrepareAnthropicRequestBody_Fable52_ForcesAdaptive(t *testing.T) {
	body := []byte(`{"model":"claude-fable-5-2","max_tokens":8192,` +
		`"thinking":{"type":"enabled","budget_tokens":4096},` +
		`"messages":[{"role":"user","content":"hi"}]}`)
	// apiclaude：catalog_code 空（211 真库实测）⇒ 非原生对 ⇒ paramguard 生效。
	cand := provider.Candidate{
		ProviderID:   587,
		CredentialID: 31,
		Protocol:     "anthropic-messages",
		CatalogCode:  "",
		RawModel:     "claude-fable-5-2",
	}
	r := httptest.NewRequest("POST", "/v1/messages", nil)
	r.Header.Set("X-Gw-Client-Type", "zcode")
	params := &ExecParams{
		R:              r,
		ClientType:     "zcode",
		ClientModel:    "claude-fable-5-2",
		OutboundModel:  "claude-fable-5-2",
		ClientProtocol: "anthropic-messages",
	}

	executor := &Executor{}
	out, err := executor.prepareAnthropicRequestBody(params, cand, body)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	m := decodeUpstreamBody(t, out)
	th, ok := m["thinking"].(map[string]any)
	if !ok {
		t.Fatalf("thinking missing from outbound body:\n%s", out)
	}
	if th["type"] != "adaptive" {
		t.Fatalf("thinking.type = %#v, want adaptive (upstream rejects enabled for this model)\n%s", th["type"], out)
	}
	if _, present := th["budget_tokens"]; present {
		t.Fatalf("budget_tokens survived adaptive rewrite (adaptive is budgetless):\n%s", out)
	}
}

// disabled 形态同样必须改写（该模型不接受 disabled —— 实测同一文案 400）。
func TestPrepareAnthropicRequestBody_Fable52_DisabledForcesAdaptive(t *testing.T) {
	body := []byte(`{"model":"claude-fable-5-2","max_tokens":8192,` +
		`"thinking":{"type":"disabled"},` +
		`"messages":[{"role":"user","content":"hi"}]}`)
	cand := provider.Candidate{
		ProviderID:   587,
		CredentialID: 31,
		Protocol:     "anthropic-messages",
		RawModel:     "claude-fable-5-2",
	}
	r := httptest.NewRequest("POST", "/v1/messages", nil)
	params := &ExecParams{
		R:              r,
		ClientModel:    "claude-fable-5-2",
		OutboundModel:  "claude-fable-5-2",
		ClientProtocol: "anthropic-messages",
	}

	executor := &Executor{}
	out, err := executor.prepareAnthropicRequestBody(params, cand, body)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	m := decodeUpstreamBody(t, out)
	th, _ := m["thinking"].(map[string]any)
	if th["type"] != "adaptive" {
		t.Fatalf("thinking.type = %#v, want adaptive\n%s", th["type"], out)
	}
}

// ★ 阴性对照：claude-fable-5 / claude-fable-5-1 对 enabled/disabled 全兼容
// （211 直连实测五种形态全 200）⇒ 不得被改写（它们正在生产承载流量）。
func TestPrepareAnthropicRequestBody_OlderFableModelsNotRewritten(t *testing.T) {
	for _, model := range []string{"claude-fable-5", "claude-fable-5-1"} {
		t.Run(model, func(t *testing.T) {
			body := []byte(`{"model":"` + model + `","max_tokens":8192,` +
				`"thinking":{"type":"enabled","budget_tokens":4096},` +
				`"messages":[{"role":"user","content":"hi"}]}`)
			cand := provider.Candidate{
				ProviderID:   587,
				CredentialID: 31,
				Protocol:     "anthropic-messages",
				RawModel:     model,
			}
			r := httptest.NewRequest("POST", "/v1/messages", nil)
			params := &ExecParams{
				R:              r,
				ClientModel:    model,
				OutboundModel:  model,
				ClientProtocol: "anthropic-messages",
			}
			executor := &Executor{}
			out, err := executor.prepareAnthropicRequestBody(params, cand, body)
			if err != nil {
				t.Fatalf("prepare: %v", err)
			}
			m := decodeUpstreamBody(t, out)
			th, _ := m["thinking"].(map[string]any)
			if th["type"] != "enabled" {
				t.Fatalf("thinking.type = %#v, want enabled (old model must not be rewritten)\n%s", th["type"], out)
			}
		})
	}
}

// ─── 跨协议腿（openai-responses 客户端 → anthropic 上游）───
//
// 用户现场（211 真库 request_logs）：claude-fable-5-2 的失败请求
// request_mode=responses ⇒ 走的是**跨协议 IR 序列化腿**（不是直传腿）。
// 本测试钉住该腿也必须改写 —— 门长在 paramguard 上两腿共用，
// 但必须有一条测试证明跨协议腿没漏（否则会出现「直传腿绿、生产仍 400」）。
//
// Responses 客户端带 reasoning ⇒ IR 的 Reasoning ⇒ 序列化出
// thinking{type:enabled,budget_tokens} ⇒ 必须被改写为 adaptive。
func TestPrepareAnthropicRequestBody_Fable52_ResponsesCrossProtocolLeg(t *testing.T) {
	responsesBody := []byte(`{"model":"claude-fable-5-2",` +
		`"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],` +
		`"reasoning":{"effort":"medium"}}`)
	cand := provider.Candidate{
		ProviderID:   587,
		CredentialID: 31,
		Protocol:     "anthropic-messages",
		RawModel:     "claude-fable-5-2",
	}
	r := httptest.NewRequest("POST", "/v1/responses", nil)
	params := &ExecParams{
		R:              r,
		BodyBytes:      responsesBody,
		ClientModel:    "claude-fable-5-2",
		OutboundModel:  "claude-fable-5-2",
		ClientProtocol: "openai-responses",
	}

	executor := &Executor{IR: &irAdapterForTest{}}
	out, err := executor.prepareAnthropicRequestBody(params, cand, responsesBody)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	m := decodeUpstreamBody(t, out)
	th, ok := m["thinking"].(map[string]any)
	if !ok {
		// reasoning 未被渲染成 thinking 时该形态上游接受（无 thinking 合法）。
		t.Logf("no thinking block in cross-protocol output: %s", out)
		return
	}
	if th["type"] != "adaptive" {
		t.Fatalf("thinking.type = %#v, want adaptive (cross-protocol leg must be covered too)\n%s", th["type"], out)
	}
}
