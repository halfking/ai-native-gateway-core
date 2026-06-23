package transport

import (
	"context"
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
	bag, err := e.Extract([]byte(openaiBody), nil)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if bag == nil {
		t.Fatal("Extract: nil bag")
	}
	if len(bag.ClientRaw) == 0 {
		t.Fatal("ClientRaw should be populated")
	}
	if _, ok := bag.ClientRaw["model"]; !ok {
		t.Fatal("ClientRaw should contain model")
	}
}

func TestIRExtensionRestorer_Restore_NoOp(t *testing.T) {
	r := &IRExtensionRestorer{}
	// Phase 0.6 MVP：Restore 是 no-op
	body := []byte(`{"id":"x"}`)
	out, err := r.Restore(body, &domain.ExtensionsBag{Headers: map[string]string{"k": "v"}})
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if string(out) != string(body) {
		t.Fatalf("Restore MVP should be no-op, got %s", out)
	}
}
