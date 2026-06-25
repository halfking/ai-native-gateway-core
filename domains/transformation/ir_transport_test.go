package transformation

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domain"
)

const (
	openaiBody = `{
		"model": "gpt-4o",
		"max_tokens": 1024,
		"messages": [{"role": "user", "content": "hello"}]
	}`

	anthropicBody = `{
		"model": "claude-sonnet-4-20250514",
		"max_tokens": 1024,
		"messages": [{"role": "user", "content": "hello"}]
	}`

	openaiResponse = `{
		"id": "chatcmpl-1",
		"model": "gpt-4o",
		"choices": [{"index": 0, "message": {"role": "assistant", "content": "hi"}, "finish_reason": "stop"}],
		"usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}
	}`

	anthropicResponse = `{
		"id": "msg_1",
		"model": "claude-sonnet-4-20250514",
		"content": [{"type": "text", "text": "hi"}],
		"stop_reason": "end_turn",
		"usage": {"input_tokens": 1, "output_tokens": 1}
	}`
)

func TestIRTransport_Convert_Q1_OpenAIToOpenAI(t *testing.T) {
	tr := NewIRTransport()
	env := newEnvelope("openai-chat", "openai-chat", openaiBody, "gpt-4o")

	out, err := tr.Convert(context.Background(), env)
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("Convert: empty output")
	}
	// Q1: OpenAI → OpenAI（IR 路径：parse + serialize roundtrip）
	if !contains(out, "gpt-4o") {
		t.Fatalf("Q1: output missing gpt-4o: %s", out)
	}
}

func TestIRTransport_Convert_Q2_AnthropicToOpenAI(t *testing.T) {
	tr := NewIRTransport()
	env := newEnvelope("anthropic-messages", "openai-chat", anthropicBody, "gpt-4o")

	out, err := tr.Convert(context.Background(), env)
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	// Q2: Anthropic → OpenAI，输出应包含 OpenAI 风格字段
	if !contains(out, `"messages"`) {
		t.Fatalf("Q2: output missing messages: %s", out)
	}
}

func TestIRTransport_Convert_Q3_OpenAIToAnthropic(t *testing.T) {
	tr := NewIRTransport()
	env := newEnvelope("openai-chat", "anthropic-messages", openaiBody, "claude-sonnet-4-20250514")

	out, err := tr.Convert(context.Background(), env)
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	// Q3: OpenAI → Anthropic，输出应包含 max_tokens
	if !contains(out, "max_tokens") {
		t.Fatalf("Q3: output missing max_tokens: %s", out)
	}
}

func TestIRTransport_Convert_Q4_AnthropicToAnthropic(t *testing.T) {
	tr := NewIRTransport()
	env := newEnvelope("anthropic-messages", "anthropic-messages", anthropicBody, "claude-sonnet-4-20250514")

	out, err := tr.Convert(context.Background(), env)
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	// Q4: Anthropic → Anthropic（roundtrip）
	if !contains(out, "claude-sonnet-4-20250514") {
		t.Fatalf("Q4: output missing model: %s", out)
	}
}

func TestIRTransport_ConvertResponse_Q1_OpenAI(t *testing.T) {
	tr := NewIRTransport()
	env := newEnvelope("openai-chat", "openai-chat", openaiBody, "gpt-4o")

	out, err := tr.ConvertResponse(context.Background(), env, []byte(openaiResponse))
	if err != nil {
		t.Fatalf("ConvertResponse: %v", err)
	}
	if !contains(out, "chatcmpl-1") {
		t.Fatalf("Q1 response: missing id: %s", out)
	}
}

func TestIRTransport_ConvertResponse_Q3_OpenAIClient_AnthropicUpstream(t *testing.T) {
	tr := NewIRTransport()
	env := newEnvelope("openai-chat", "anthropic-messages", openaiBody, "gpt-4o")

	out, err := tr.ConvertResponse(context.Background(), env, []byte(anthropicResponse))
	if err != nil {
		t.Fatalf("ConvertResponse: %v", err)
	}
	// Anthropic 响应转 OpenAI：应包含 choices
	if !contains(out, "choices") {
		t.Fatalf("Q3 response: missing choices: %s", out)
	}
}

func TestIRTransport_Convert_NilInput(t *testing.T) {
	tr := NewIRTransport()
	if _, err := tr.Convert(context.Background(), nil); err == nil {
		t.Fatal("Convert(nil) should error")
	}
}

func TestIRTransport_Convert_UnsupportedProtocol(t *testing.T) {
	tr := NewIRTransport()
	env := newEnvelope("unknown", "openai-chat", openaiBody, "gpt-4o")
	if _, err := tr.Convert(context.Background(), env); err == nil {
		t.Fatal("Convert(unknown protocol) should error")
	}
}

func TestIRTransport_Implementation(t *testing.T) {
	tr := NewIRTransport()
	if tr.Implementation() != "ir" {
		t.Fatalf("Implementation() = %s, want ir", tr.Implementation())
	}
}

// helpers

func newEnvelope(client, upstream string, body string, model string) *domain.RequestEnvelope {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	return domain.NewEnvelopeBuilder("test-req").
		WithTransport(&domain.TransportContext{
			R:                req,
			BodyBytes:        []byte(body),
			ClientProtocol:   client,
			UpstreamProtocol: upstream,
			ClientModel:      model,
		}).
		Build()
}

func contains(b []byte, substr string) bool {
	return strings.Contains(string(b), substr)
}
