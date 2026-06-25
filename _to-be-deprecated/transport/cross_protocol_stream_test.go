
package transport

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domain"
)

// Phase 2 跨协议流式测试：Anthropic SSE → OpenAI SSE / OpenAI SSE → Anthropic SSE
//
// 验证 IR 流式路径在跨协议场景下能正确：
//   - 配对 Anthropic 的 `event:` + `data:` 双行
//   - 转换每种 SSE 事件类型
//   - 发送正确的 [DONE] sentinel

// 真实 Anthropic SSE 事件序列
const anthropicSSEFull = `event: message_start
data: {"type":"message_start","message":{"id":"msg_01","type":"message","role":"assistant","content":[],"model":"claude-sonnet-4-20250514","stop_reason":null,"usage":{"input_tokens":10,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" world"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}

event: message_stop
data: {"type":"message_stop"}

`

func TestIRTransport_ConvertStream_AnthropicToOpenAI(t *testing.T) {
	tr := NewIRTransport()
	w := httptest.NewRecorder()
	resp := mockSSEUpstreamResponse(t, anthropicSSEFull)

	env := domain.NewEnvelopeBuilder("stream-1").
		WithTransport(&domain.TransportContext{
			W:                w,
			IsStream:         true,
			ClientProtocol:   "openai-chat",
			UpstreamProtocol: "anthropic-messages",
			ClientModel:      "gpt-4o",
			OutboundModel:    "claude-sonnet-4-20250514",
		}).
		Build()

	if err := tr.ConvertStream(context.Background(), env, resp); err != nil {
		t.Fatalf("ConvertStream: %v", err)
	}

	body := w.Body.String()

	// 关键断言 1：每个 content_block_delta 应转为 OpenAI delta chunk
	if !strings.Contains(body, "Hello") {
		t.Errorf("OpenAI output should contain 'Hello': %s", body)
	}
	if !strings.Contains(body, "world") {
		t.Errorf("OpenAI output should contain 'world': %s", body)
	}

	// 关键断言 2：包含 OpenAI choices 字段
	if !strings.Contains(body, `"choices"`) && !strings.Contains(body, `"delta"`) {
		t.Errorf("OpenAI output should contain choices/delta: %s", body)
	}

	// 关键断言 3：流结束应发 [DONE]
	if !strings.Contains(body, "[DONE]") {
		t.Errorf("OpenAI output should end with [DONE]: %s", body)
	}

	// 关键断言 4：成功完成应 RecordSuccess
	if tr.cb.State() != CircuitClosed {
		t.Errorf("after successful stream cb state = %s, want closed", tr.cb.State())
	}
}

// --- 流式降级集成测试：3 次错误后熔断 ---

func TestIRTransport_ConvertStream_TriggersCircuitBreaker(t *testing.T) {
	// 短阈值熔断器便于测试
	cb := &StreamCircuitBreaker{
		threshold: 2,
		window:    1000000 * 1000, // 1000s
		cooldown:  1000000 * 1000,
		state:     CircuitClosed,
	}
	tr := NewIRTransport()
	tr.SetCircuitBreaker(cb)

	// 喂入无效 SSE 数据（payload 是非 JSON）
	invalidSSE := "data: not-valid-json\n\ndata: also-invalid\n\n"
	w := httptest.NewRecorder()
	resp := mockSSEUpstreamResponse(t, invalidSSE)

	env := domain.NewEnvelopeBuilder("err-1").
		WithTransport(&domain.TransportContext{
			W:                w,
			IsStream:         true,
			ClientProtocol:   "openai-chat",
			UpstreamProtocol: "openai-chat",
			ClientModel:      "gpt-4o",
		}).
		Build()

	// ConvertStream 不应因单错退出，应继续处理
	if err := tr.ConvertStream(context.Background(), env, resp); err != nil {
		t.Fatalf("ConvertStream should not return error on parse failures: %v", err)
	}

	// 2 次解析错误应熔断
	if cb.State() != CircuitOpen {
		t.Fatalf("after 2 parse errors state = %s, want open", cb.State())
	}
	if !cb.ShouldFallback() {
		t.Fatal("ShouldFallback should be true when open")
	}
}

// --- 熔断开启后 Factory 应降级 ---

func TestFactory_StreamFallbackOnCircuitOpen(t *testing.T) {
	cb := &StreamCircuitBreaker{
		threshold: 1,
		window:    1000000 * 1000,
		cooldown:  1000000 * 1000,
		state:     CircuitClosed,
	}
	cb.RecordError() // 熔断

	f := NewTransportFactory()
	f.irTransport.SetCircuitBreaker(cb)
	t.Setenv("TRANSPORT_LAYER_IR_ENABLED", "true")
	t.Setenv("TRANSPORT_IR_ROLLOUT_PERCENT", "100")
	f.Reload()

	// 流式请求应被降级到 Legacy
	streamEnv := domain.NewEnvelopeBuilder("s1").
		WithTenant(&domain.TenantContext{ID: "t1"}).
		WithTransport(&domain.TransportContext{
			IsStream:         true,
			ClientProtocol:   "openai-chat",
			UpstreamProtocol: "anthropic-messages",
			ClientModel:      "gpt-4o",
		}).
		Build()
	if got := f.Pick(context.Background(), streamEnv).Implementation(); got != "legacy" {
		t.Errorf("stream with open circuit: Pick() = %s, want legacy", got)
	}

	// 非流式请求仍走 IR
	nonStreamEnv := domain.NewEnvelopeBuilder("ns1").
		WithTenant(&domain.TenantContext{ID: "t1"}).
		WithTransport(&domain.TransportContext{
			IsStream:         false,
			ClientProtocol:   "openai-chat",
			UpstreamProtocol: "anthropic-messages",
			ClientModel:      "gpt-4o",
		}).
		Build()
	if got := f.Pick(context.Background(), nonStreamEnv).Implementation(); got != "ir" {
		t.Errorf("non-stream with open circuit: Pick() = %s, want ir (熔断仅影响流式)", got)
	}
}

// --- 熔断开启时 ConvertStream 入口应立即返回 ---

func TestIRTransport_ConvertStream_RejectsWhenCircuitOpen(t *testing.T) {
	cb := &StreamCircuitBreaker{
		threshold: 1,
		window:    1000000 * 1000,
		cooldown:  1000000 * 1000,
		state:     CircuitClosed,
	}
	cb.RecordError() // 熔断

	tr := NewIRTransport()
	tr.SetCircuitBreaker(cb)

	w := httptest.NewRecorder()
	resp := mockSSEUpstreamResponse(t, "data: {}\n\n")

	env := domain.NewEnvelopeBuilder("blocked").
		WithTransport(&domain.TransportContext{
			W:                w,
			IsStream:         true,
			ClientProtocol:   "openai-chat",
			UpstreamProtocol: "openai-chat",
		}).
		Build()

	err := tr.ConvertStream(context.Background(), env, resp)
	if err != ErrStreamCircuitOpen {
		t.Fatalf("expected ErrStreamCircuitOpen, got %v", err)
	}
	if w.Body.Len() != 0 {
		t.Errorf("body should be empty when circuit open: %s", w.Body.String())
	}
}

// --- 真实 Q1 OpenAI → OpenAI 流式直通仍工作（回归测试）---

func TestIRTransport_ConvertStream_OpenAIToOpenAI_FullSequence(t *testing.T) {
	tr := NewIRTransport()
	w := httptest.NewRecorder()

	openaiSSE := "data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\" there\"},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"
	resp := mockSSEUpstreamResponse(t, openaiSSE)

	env := domain.NewEnvelopeBuilder("r1").
		WithTransport(&domain.TransportContext{
			W:                w,
			IsStream:         true,
			ClientProtocol:   "openai-chat",
			UpstreamProtocol: "openai-chat",
			ClientModel:      "gpt-4o",
		}).
		Build()

	if err := tr.ConvertStream(context.Background(), env, resp); err != nil {
		t.Fatalf("ConvertStream: %v", err)
	}

	body := w.Body.String()
	if !strings.Contains(body, "hi") {
		t.Errorf("missing 'hi': %s", body)
	}
	if !strings.Contains(body, "there") {
		t.Errorf("missing 'there': %s", body)
	}
	if !strings.Contains(body, "[DONE]") {
		t.Errorf("missing [DONE]: %s", body)
	}
}

// --- Anthropic → Anthropic 直通（Q4）---

func TestIRTransport_ConvertStream_AnthropicToAnthropic(t *testing.T) {
	tr := NewIRTransport()
	w := httptest.NewRecorder()
	resp := mockSSEUpstreamResponse(t, anthropicSSEFull)

	env := domain.NewEnvelopeBuilder("anthropic-stream").
		WithTransport(&domain.TransportContext{
			W:                w,
			IsStream:         true,
			ClientProtocol:   "anthropic-messages",
			UpstreamProtocol: "anthropic-messages",
			ClientModel:      "claude-sonnet-4-20250514",
		}).
		Build()

	if err := tr.ConvertStream(context.Background(), env, resp); err != nil {
		t.Fatalf("ConvertStream: %v", err)
	}

	body := w.Body.String()
	// Anthropic → Anthropic：应保留 event: 行
	if !strings.Contains(body, "event:") {
		t.Errorf("Anthropic client should see event: lines: %s", body)
	}
	if !strings.Contains(body, "Hello") {
		t.Errorf("missing 'Hello': %s", body)
	}
}
