package transformation

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domain"
)

func TestLegacyTransport_Convert_Q1_OpenAIToOpenAI(t *testing.T) {
	tr := NewLegacyTransport()
	env := newEnvelope("openai-chat", "openai-chat", openaiBody, "gpt-4o")

	out, err := tr.Convert(context.Background(), env)
	if err != nil {
		t.Fatalf("Convert Q1: %v", err)
	}
	// Q1: 直通，body 应原样返回
	if !strings.Contains(string(out), "gpt-4o") {
		t.Fatalf("Q1: missing model: %s", out)
	}
}

func TestLegacyTransport_Convert_Q2_AnthropicToOpenAI(t *testing.T) {
	tr := NewLegacyTransport()
	env := newEnvelope("anthropic-messages", "openai-chat", anthropicBody, "gpt-4o")

	out, err := tr.Convert(context.Background(), env)
	if err != nil {
		t.Fatalf("Convert Q2: %v", err)
	}
	// Q2: Anthropic → OpenAI，输出应包含 OpenAI 风格 messages
	if !strings.Contains(string(out), "messages") {
		t.Fatalf("Q2: missing messages: %s", out)
	}
}

func TestLegacyTransport_Convert_Q3_OpenAIToAnthropic(t *testing.T) {
	tr := NewLegacyTransport()
	env := newEnvelope("openai-chat", "anthropic-messages", openaiBody, "claude-sonnet-4-20250514")

	out, err := tr.Convert(context.Background(), env)
	if err != nil {
		t.Fatalf("Convert Q3: %v", err)
	}
	// Q3: OpenAI → Anthropic，输出应包含 max_tokens
	if !strings.Contains(string(out), "max_tokens") {
		t.Fatalf("Q3: missing max_tokens: %s", out)
	}
}

func TestLegacyTransport_Convert_Q4_AnthropicToAnthropic(t *testing.T) {
	tr := NewLegacyTransport()
	env := newEnvelope("anthropic-messages", "anthropic-messages", anthropicBody, "claude-sonnet-4-20250514")

	out, err := tr.Convert(context.Background(), env)
	if err != nil {
		t.Fatalf("Convert Q4: %v", err)
	}
	// Q4: 直通
	if !strings.Contains(string(out), "claude-sonnet-4-20250514") {
		t.Fatalf("Q4: missing model: %s", out)
	}
}

func TestLegacyTransport_Convert_Unsupported(t *testing.T) {
	tr := NewLegacyTransport()
	env := newEnvelope("unknown", "openai-chat", openaiBody, "gpt-4o")
	if _, err := tr.Convert(context.Background(), env); err == nil {
		t.Fatal("Convert with unsupported protocol should error")
	}
}

func TestLegacyTransport_ConvertResponse_Q1_Passthrough(t *testing.T) {
	tr := NewLegacyTransport()
	env := newEnvelope("openai-chat", "openai-chat", openaiBody, "gpt-4o")

	out, err := tr.ConvertResponse(context.Background(), env, []byte(openaiResponse))
	if err != nil {
		t.Fatalf("ConvertResponse Q1: %v", err)
	}
	if !strings.Contains(string(out), "chatcmpl-1") {
		t.Fatalf("Q1 response: missing id: %s", out)
	}
}

func TestLegacyTransport_ConvertResponse_Q3_OpenAIClient_AnthropicUpstream(t *testing.T) {
	tr := NewLegacyTransport()
	env := newEnvelope("openai-chat", "anthropic-messages", openaiBody, "gpt-4o")

	out, err := tr.ConvertResponse(context.Background(), env, []byte(anthropicResponse))
	if err != nil {
		t.Fatalf("ConvertResponse Q3: %v", err)
	}
	// Anthropic → OpenAI 响应
	if !strings.Contains(string(out), "choices") {
		t.Fatalf("Q3 response: missing choices: %s", out)
	}
}

func TestLegacyTransport_ConvertResponse_Q2_AnthropicClient_OpenAIUpstream(t *testing.T) {
	tr := NewLegacyTransport()
	env := newEnvelope("anthropic-messages", "openai-chat", anthropicBody, "gpt-4o")

	out, err := tr.ConvertResponse(context.Background(), env, []byte(openaiResponse))
	if err != nil {
		t.Fatalf("ConvertResponse Q2: %v", err)
	}
	// Q2 响应：Anthropic client ← OpenAI upstream，直通
	if string(out) != openaiResponse {
		t.Fatalf("Q2: response should pass through, got %s", out)
	}
}

func TestLegacyTransport_ConvertResponse_Unsupported(t *testing.T) {
	tr := NewLegacyTransport()
	env := newEnvelope("unknown", "openai-chat", openaiBody, "gpt-4o")
	if _, err := tr.ConvertResponse(context.Background(), env, []byte(openaiResponse)); err == nil {
		t.Fatal("ConvertResponse with unsupported protocol should error")
	}
}

func TestLegacyTransport_Convert_NilInput(t *testing.T) {
	tr := NewLegacyTransport()
	if _, err := tr.Convert(context.Background(), nil); err == nil {
		t.Fatal("Convert(nil) should error")
	}
}

func TestLegacyTransport_ConvertResponse_NilInput(t *testing.T) {
	tr := NewLegacyTransport()
	if _, err := tr.ConvertResponse(context.Background(), nil, []byte("{}")); err == nil {
		t.Fatal("ConvertResponse(nil) should error")
	}
}

func TestLegacyTransport_Implementation(t *testing.T) {
	tr := NewLegacyTransport()
	if tr.Implementation() != "legacy" {
		t.Fatalf("Implementation() = %s, want legacy", tr.Implementation())
	}
}

func TestIRProtocolDetector_Detect(t *testing.T) {
	d := &IRProtocolDetector{}
	proto, conf := d.Detect([]byte(openaiBody), nil)
	if proto != "openai-chat" {
		t.Errorf("Detect(openai) = %s, want openai-chat (conf=%v)", proto, conf)
	}
	proto2, _ := d.Detect([]byte(anthropicBody), nil)
	if proto2 != "anthropic-messages" {
		t.Errorf("Detect(anthropic) = %s, want anthropic-messages", proto2)
	}
}

func TestIRExtensionExtractor_Extract(t *testing.T) {
	e := &IRExtensionExtractor{}
	// 含非标准字段 custom_field + cache
	body := []byte(`{"model":"gpt-4o","messages":[],"custom_field":"value123","cache":true}`)
	bag, err := e.Extract(body, nil)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if bag == nil {
		t.Fatal("Extract: nil bag")
	}
	// 非标准字段应被提取
	if _, ok := bag.ClientRaw["custom_field"]; !ok {
		t.Fatal("ClientRaw should contain custom_field")
	}
	if _, ok := bag.ClientRaw["cache"]; !ok {
		t.Fatal("ClientRaw should contain cache")
	}
	// 标准字段不应被提取
	if _, ok := bag.ClientRaw["model"]; ok {
		t.Fatal("ClientRaw should NOT contain standard field model")
	}
	if _, ok := bag.ClientRaw["messages"]; ok {
		t.Fatal("ClientRaw should NOT contain standard field messages")
	}
}

func TestIRExtensionExtractor_Headers(t *testing.T) {
	e := &IRExtensionExtractor{}
	h := http.Header{}
	h.Set("anthropic-beta", "beta-2023-06-01")
	bag, err := e.Extract(nil, h)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if bag.Headers["anthropic-beta"] != "beta-2023-06-01" {
		t.Fatalf("Headers anthropic-beta = %q, want beta-2023-06-01", bag.Headers["anthropic-beta"])
	}
}

func TestIRExtensionRestorer_Restore_MergesMissing(t *testing.T) {
	r := &IRExtensionRestorer{}
	body := []byte(`{"id":"x","model":"gpt-4o"}`)
	bag := &domain.ExtensionsBag{
		ClientRaw: map[string]json.RawMessage{
			"custom_field": json.RawMessage(`"value123"`),
			"cache":        json.RawMessage(`true`),
		},
	}
	out, err := r.Restore(body, bag)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("output invalid JSON: %v\n%s", err, out)
	}
	if string(m["custom_field"]) != `"value123"` {
		t.Errorf("custom_field = %s, want \"value123\"", m["custom_field"])
	}
	if string(m["cache"]) != `true` {
		t.Errorf("cache = %s, want true", m["cache"])
	}
}

func TestIRExtensionRestorer_Restore_DoesNotOverwrite(t *testing.T) {
	r := &IRExtensionRestorer{}
	// 目标已有 model 字段，ExtensionsBag 也带 model —— 不应覆盖
	body := []byte(`{"model":"gpt-4o"}`)
	bag := &domain.ExtensionsBag{
		ClientRaw: map[string]json.RawMessage{
			"model": json.RawMessage(`"claude"`),
		},
	}
	out, err := r.Restore(body, bag)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if strings.Contains(string(out), "claude") {
		t.Fatalf("Restore should not overwrite existing fields: %s", out)
	}
}

func TestIRExtensionRestorer_Restore_NilBag(t *testing.T) {
	r := &IRExtensionRestorer{}
	body := []byte(`{"id":"x"}`)
	out, err := r.Restore(body, nil)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if string(out) != string(body) {
		t.Fatalf("Restore nil bag should be no-op: %s", out)
	}
}

func TestIRExtensionRestorer_Restore_InvalidJSON(t *testing.T) {
	r := &IRExtensionRestorer{}
	body := []byte(`not json`)
	bag := &domain.ExtensionsBag{
		ClientRaw: map[string]json.RawMessage{"k": json.RawMessage(`"v"`)},
	}
	out, err := r.Restore(body, bag)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	// 无效 JSON 时原样返回
	if string(out) != string(body) {
		t.Fatalf("Restore invalid JSON should return as-is: %s", out)
	}
}
