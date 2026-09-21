package transformation

import (
	"encoding/json"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/ir"
	"github.com/kaixuan/llm-gateway-go/internal/irconv"
)

// irAdapterForTest mirrors cmd/gateway's irAdapter: the raw ir package
// functions behind the irconv.Converter interface, so the TransportIRConverter
// wrapper is exercised against the real parsers/serializers.
type irAdapterForTest struct{}

var _ irconv.Converter = (*irAdapterForTest)(nil)

func (a *irAdapterForTest) ParseOpenAI(body []byte) (*ir.InternalRequest, error) {
	return ir.ParseOpenAI(body)
}
func (a *irAdapterForTest) ParseAnthropic(body []byte) (*ir.InternalRequest, error) {
	return ir.ParseAnthropic(body)
}
func (a *irAdapterForTest) ParseResponses(body []byte) (*ir.InternalRequest, error) {
	return ir.ParseResponses(body)
}
func (a *irAdapterForTest) SerializeOpenAI(req *ir.InternalRequest) ([]byte, error) {
	return ir.SerializeOpenAI(req)
}
func (a *irAdapterForTest) SerializeAnthropic(req *ir.InternalRequest) ([]byte, error) {
	return ir.SerializeAnthropic(req)
}
func (a *irAdapterForTest) ParseAnthropicResponse(body []byte) (*ir.InternalResponse, error) {
	return ir.ParseAnthropicResponse(body)
}
func (a *irAdapterForTest) ParseOpenAIResponse(body []byte) (*ir.InternalResponse, error) {
	return ir.ParseOpenAIResponse(body)
}
func (a *irAdapterForTest) SerializeOpenAIResponse(resp *ir.InternalResponse, clientModel string) ([]byte, error) {
	return ir.SerializeOpenAIResponse(resp, clientModel)
}
func (a *irAdapterForTest) SerializeAnthropicResponse(resp *ir.InternalResponse, clientModel string) ([]byte, error) {
	return ir.SerializeAnthropicResponse(resp, clientModel)
}
func (a *irAdapterForTest) SerializeResponses(chunk *ir.StreamChunk, itemID string) string {
	return chunk.SerializeResponses(itemID)
}
func (a *irAdapterForTest) SerializeResponsesResponse(resp *ir.InternalResponse, clientModel string) ([]byte, error) {
	return ir.SerializeResponsesResponse(resp, clientModel)
}

// TestTransportIRConverter_ResponseExtRestore_DoesNotMergeProtocolShapes
// 钉住 2026-09-21 R50 根修:响应侧扩展提取曾复用请求字段白名单,把 OpenAI 标准
// 响应字段(choices/created/object)当扩展回填到 IR 序列化的 Anthropic 体上,产生
// 双协议合并体;下游 classify 先命中 choices 再按 OpenAI 重转换,usage 读不到
// prompt_tokens 归零(线上 154/245 复现:上游 prompt_tokens=178,客户端 0)。
// 修复后跨协议序列化产物只含目标协议字段,usage 原样保留。
func TestTransportIRConverter_ResponseExtRestore_DoesNotMergeProtocolShapes(t *testing.T) {
	conv := NewTransportIRConverter(&irAdapterForTest{})

	upstream := []byte(`{"id":"06fffeef","object":"chat.completion","created":1789971439,"model":"MiniMax-M3","choices":[{"finish_reason":"stop","index":0,"message":{"content":"<think>hmm</think>\n\nok","role":"assistant"}}],"usage":{"prompt_tokens":178,"completion_tokens":21,"total_tokens":199},"service_tier":"standard","vendor_extra":{"trace":"keep-me"}}`)

	resp, err := conv.ParseOpenAIResponse(upstream)
	if err != nil {
		t.Fatalf("ParseOpenAIResponse: %v", err)
	}
	out, err := conv.SerializeAnthropicResponse(resp, "minimax-m3")
	if err != nil {
		t.Fatalf("SerializeAnthropicResponse: %v", err)
	}

	var m map[string]json.RawMessage
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal output: %v; body=%s", err, out)
	}
	for _, k := range []string{"choices", "object", "service_tier", "created"} {
		if _, exists := m[k]; exists {
			t.Errorf("cross-protocol body leaked OpenAI standard field %q: %s", k, out)
		}
	}
	var usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	}
	if err := json.Unmarshal(m["usage"], &usage); err != nil {
		t.Fatalf("unmarshal usage: %v", err)
	}
	if usage.InputTokens != 178 || usage.OutputTokens != 21 {
		t.Errorf("usage = %+v, want input=178 output=21; body=%s", usage, out)
	}
	if _, exists := m["vendor_extra"]; !exists {
		t.Errorf("genuinely non-standard vendor field must survive extension restore: %s", out)
	}
}

// 同协议(openai→openai)往返不因过滤而丢标准字段:序列化器自己会写
// id/created/object/model/choices/usage,扩展机制无需携带它们。
func TestTransportIRConverter_ResponseExtRestore_SameProtocolKeepsStandardFields(t *testing.T) {
	conv := NewTransportIRConverter(&irAdapterForTest{})

	upstream := []byte(`{"id":"abc","object":"chat.completion","created":123,"model":"m","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`)
	resp, err := conv.ParseOpenAIResponse(upstream)
	if err != nil {
		t.Fatalf("ParseOpenAIResponse: %v", err)
	}
	out, err := conv.SerializeOpenAIResponse(resp, "m")
	if err != nil {
		t.Fatalf("SerializeOpenAIResponse: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, k := range []string{"id", "object", "created", "model", "choices", "usage"} {
		if _, exists := m[k]; !exists {
			t.Errorf("same-protocol round-trip lost standard field %q: %s", k, out)
		}
	}
}

// R51 审计 P3:Anthropic 响应的 container(Claude 4.5+ code execution 容器)
// 此前不在 IR 响应结构里,又被响应标准字段集认作"标准字段"——解析器不认、
// 扩展兜底也不收,被静默丢弃。现在解析器把 container 原文捕获进
// InternalResponse.Extensions,序列化后经扩展还原原样带出。
func TestTransportIRConverter_AnthropicResponseContainerRoundTrip(t *testing.T) {
	conv := NewTransportIRConverter(&irAdapterForTest{})

	container := `{"id":"cntn_01X8","type":"code_execution_container","expires_at":"2026-09-22T00:00:00Z","skills":[{"type":"anthropic","skill_id":"pdf","version":"latest"}]}`
	upstream := []byte(`{"id":"msg_01A","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":2},"container":` + container + `}`)

	resp, err := conv.ParseAnthropicResponse(upstream)
	if err != nil {
		t.Fatalf("ParseAnthropicResponse: %v", err)
	}
	out, err := conv.SerializeAnthropicResponse(resp, "claude-sonnet-4-5")
	if err != nil {
		t.Fatalf("SerializeAnthropicResponse: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal output: %v; body=%s", err, out)
	}
	if got, want := string(m["container"]), string(container); got != want {
		t.Errorf("container = %s, want verbatim %s", got, want)
	}
}
