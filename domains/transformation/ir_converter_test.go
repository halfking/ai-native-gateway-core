package transformation

import (
	"encoding/json"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/ir"
)

// mockIRAdapter is a test double for IRConverterAdapter.
type mockIRAdapter struct {
	parseOpenAIFunc                func([]byte) (*ir.InternalRequest, error)
	parseAnthropicFunc             func([]byte) (*ir.InternalRequest, error)
	serializeOpenAIFunc            func(*ir.InternalRequest) ([]byte, error)
	serializeAnthropicFunc         func(*ir.InternalRequest) ([]byte, error)
	parseOpenAIResponseFunc        func([]byte) (*ir.InternalResponse, error)
	parseAnthropicResponseFunc     func([]byte) (*ir.InternalResponse, error)
	serializeOpenAIResponseFunc    func(*ir.InternalResponse, string) ([]byte, error)
	serializeAnthropicResponseFunc func(*ir.InternalResponse, string) ([]byte, error)
	// Phase E (2026-07-01): Responses API methods (Responses client target).
	// Mirrors the production IRConverter interface so mockIRAdapter
	// stays structurally compatible as the interface evolves.
	serializeResponsesFunc         func(*ir.StreamChunk, string) string
	serializeResponsesResponseFunc func(*ir.InternalResponse, string) ([]byte, error)
}

func (m *mockIRAdapter) ParseOpenAI(body []byte) (*ir.InternalRequest, error) {
	if m.parseOpenAIFunc != nil {
		return m.parseOpenAIFunc(body)
	}
	return &ir.InternalRequest{Model: "gpt-4o"}, nil
}

func (m *mockIRAdapter) ParseAnthropic(body []byte) (*ir.InternalRequest, error) {
	if m.parseAnthropicFunc != nil {
		return m.parseAnthropicFunc(body)
	}
	return &ir.InternalRequest{Model: "claude-sonnet-4"}, nil
}

func (m *mockIRAdapter) SerializeOpenAI(req *ir.InternalRequest) ([]byte, error) {
	if m.serializeOpenAIFunc != nil {
		return m.serializeOpenAIFunc(req)
	}
	return []byte(`{"model":"gpt-4o"}`), nil
}

func (m *mockIRAdapter) SerializeAnthropic(req *ir.InternalRequest) ([]byte, error) {
	if m.serializeAnthropicFunc != nil {
		return m.serializeAnthropicFunc(req)
	}
	return []byte(`{"model":"claude-sonnet-4"}`), nil
}

func (m *mockIRAdapter) ParseOpenAIResponse(body []byte) (*ir.InternalResponse, error) {
	if m.parseOpenAIResponseFunc != nil {
		return m.parseOpenAIResponseFunc(body)
	}
	return &ir.InternalResponse{ID: "chatcmpl-1"}, nil
}

func (m *mockIRAdapter) ParseAnthropicResponse(body []byte) (*ir.InternalResponse, error) {
	if m.parseAnthropicResponseFunc != nil {
		return m.parseAnthropicResponseFunc(body)
	}
	return &ir.InternalResponse{ID: "msg_1"}, nil
}

func (m *mockIRAdapter) SerializeOpenAIResponse(resp *ir.InternalResponse, clientModel string) ([]byte, error) {
	if m.serializeOpenAIResponseFunc != nil {
		return m.serializeOpenAIResponseFunc(resp, clientModel)
	}
	return []byte(`{"id":"chatcmpl-1","model":"gpt-4o"}`), nil
}

func (m *mockIRAdapter) SerializeAnthropicResponse(resp *ir.InternalResponse, clientModel string) ([]byte, error) {
	if m.serializeAnthropicResponseFunc != nil {
		return m.serializeAnthropicResponseFunc(resp, clientModel)
	}
	return []byte(`{"id":"msg_1","model":"claude-sonnet-4"}`), nil
}

// SerializeResponses (Phase E): default returns empty SSE if no override.
// Tests that exercise the Responses path set serializeResponsesFunc.
func (m *mockIRAdapter) SerializeResponses(chunk *ir.StreamChunk, itemID string) string {
	if m.serializeResponsesFunc != nil {
		return m.serializeResponsesFunc(chunk, itemID)
	}
	return ""
}

// SerializeResponsesResponse (Phase E): default returns empty body.
// Tests that exercise the non-stream Responses path set
// serializeResponsesResponseFunc.
func (m *mockIRAdapter) SerializeResponsesResponse(resp *ir.InternalResponse, clientModel string) ([]byte, error) {
	if m.serializeResponsesResponseFunc != nil {
		return m.serializeResponsesResponseFunc(resp, clientModel)
	}
	return []byte(`{}`), nil
}

// --- 测试：Parse 提取 extensions ---

func TestTransportIRConverter_ParseOpenAI_ExtractsExtensions(t *testing.T) {
	inner := &mockIRAdapter{
		parseOpenAIFunc: func(body []byte) (*ir.InternalRequest, error) {
			return &ir.InternalRequest{Model: "gpt-4o", Extensions: nil}, nil
		},
	}
	conv := NewTransportIRConverter(inner)

	// 输入带非标字段 custom_field
	body := []byte(`{"model":"gpt-4o","messages":[],"custom_field":"value123"}`)
	req, err := conv.ParseOpenAI(body)
	if err != nil {
		t.Fatalf("ParseOpenAI: %v", err)
	}

	// 验证 Extensions 被填充
	if len(req.Extensions) == 0 {
		t.Fatal("Extensions should be populated")
	}
	if string(req.Extensions["custom_field"]) != `"value123"` {
		t.Errorf("Extensions[custom_field] = %s, want \"value123\"", req.Extensions["custom_field"])
	}
}

func TestTransportIRConverter_ParseAnthropic_ExtractsExtensions(t *testing.T) {
	inner := &mockIRAdapter{
		parseAnthropicFunc: func(body []byte) (*ir.InternalRequest, error) {
			return &ir.InternalRequest{Model: "claude-sonnet-4", Extensions: nil}, nil
		},
	}
	conv := NewTransportIRConverter(inner)

	body := []byte(`{"model":"claude-sonnet-4","messages":[],"thinking_budget":5000}`)
	req, err := conv.ParseAnthropic(body)
	if err != nil {
		t.Fatalf("ParseAnthropic: %v", err)
	}

	if len(req.Extensions) == 0 {
		t.Fatal("Extensions should be populated")
	}
	if string(req.Extensions["thinking_budget"]) != `5000` {
		t.Errorf("Extensions[thinking_budget] = %s, want 5000", req.Extensions["thinking_budget"])
	}
}

// --- 测试：Serialize 还原 extensions ---

func TestTransportIRConverter_SerializeOpenAI_RestoresExtensions(t *testing.T) {
	inner := &mockIRAdapter{
		serializeOpenAIFunc: func(req *ir.InternalRequest) ([]byte, error) {
			// inner 返回的 body 不含 custom_field（IR 不处理非标字段）
			return []byte(`{"model":"gpt-4o","messages":[]}`), nil
		},
	}
	conv := NewTransportIRConverter(inner)

	req := &ir.InternalRequest{
		Model: "gpt-4o",
		Extensions: map[string]json.RawMessage{
			"custom_field": json.RawMessage(`"value123"`),
		},
	}
	out, err := conv.SerializeOpenAI(req)
	if err != nil {
		t.Fatalf("SerializeOpenAI: %v", err)
	}

	// 验证 custom_field 被还原到输出
	var m map[string]json.RawMessage
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("output invalid JSON: %v\n%s", err, out)
	}
	if string(m["custom_field"]) != `"value123"` {
		t.Errorf("custom_field not restored: %s", out)
	}
	if string(m["model"]) != `"gpt-4o"` {
		t.Errorf("model should remain: %s", out)
	}
}

func TestTransportIRConverter_SerializeAnthropic_RestoresExtensions(t *testing.T) {
	inner := &mockIRAdapter{
		serializeAnthropicFunc: func(req *ir.InternalRequest) ([]byte, error) {
			return []byte(`{"model":"claude-sonnet-4","messages":[]}`), nil
		},
	}
	conv := NewTransportIRConverter(inner)

	req := &ir.InternalRequest{
		Model: "claude-sonnet-4",
		Extensions: map[string]json.RawMessage{
			"thinking_budget": json.RawMessage(`5000`),
		},
	}
	out, err := conv.SerializeAnthropic(req)
	if err != nil {
		t.Fatalf("SerializeAnthropic: %v", err)
	}

	var m map[string]json.RawMessage
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("output invalid JSON: %v", err)
	}
	if string(m["thinking_budget"]) != `5000` {
		t.Errorf("thinking_budget not restored: %s", out)
	}
}

// --- 测试：Response 方向的 extensions ---

func TestTransportIRConverter_ParseOpenAIResponse_ExtractsExtensions(t *testing.T) {
	inner := &mockIRAdapter{
		parseOpenAIResponseFunc: func(body []byte) (*ir.InternalResponse, error) {
			return &ir.InternalResponse{ID: "chatcmpl-1", Extensions: nil}, nil
		},
	}
	conv := NewTransportIRConverter(inner)

	body := []byte(`{"id":"chatcmpl-1","model":"gpt-4o","custom_resp":"xyz"}`)
	resp, err := conv.ParseOpenAIResponse(body)
	if err != nil {
		t.Fatalf("ParseOpenAIResponse: %v", err)
	}

	if len(resp.Extensions) == 0 {
		t.Fatal("Response Extensions should be populated")
	}
	if string(resp.Extensions["custom_resp"]) != `"xyz"` {
		t.Errorf("Extensions[custom_resp] = %s, want \"xyz\"", resp.Extensions["custom_resp"])
	}
}

func TestTransportIRConverter_SerializeOpenAIResponse_RestoresExtensions(t *testing.T) {
	inner := &mockIRAdapter{
		serializeOpenAIResponseFunc: func(resp *ir.InternalResponse, clientModel string) ([]byte, error) {
			return []byte(`{"id":"chatcmpl-1","model":"gpt-4o"}`), nil
		},
	}
	conv := NewTransportIRConverter(inner)

	resp := &ir.InternalResponse{
		ID:    "chatcmpl-1",
		Model: "gpt-4o",
		Extensions: map[string]json.RawMessage{
			"custom_resp": json.RawMessage(`"xyz"`),
		},
	}
	out, err := conv.SerializeOpenAIResponse(resp, "gpt-4o")
	if err != nil {
		t.Fatalf("SerializeOpenAIResponse: %v", err)
	}

	var m map[string]json.RawMessage
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("output invalid JSON: %v", err)
	}
	if string(m["custom_resp"]) != `"xyz"` {
		t.Errorf("custom_resp not restored: %s", out)
	}
}

// --- 测试：CircuitBreaker 集成 ---

func TestTransportIRConverter_CircuitBreaker_Trips(t *testing.T) {
	inner := &mockIRAdapter{
		parseOpenAIFunc: func(body []byte) (*ir.InternalRequest, error) {
			return nil, errMockParse
		},
	}
	cb := &StreamCircuitBreaker{
		threshold: 2,
		window:    1000000 * 1000,
		cooldown:  1000000 * 1000,
		state:     CircuitClosed,
	}
	conv := NewTransportIRConverter(inner)
	conv.SetCircuitBreaker(cb)

	// 2 次解析错误应触发熔断
	_, _ = conv.ParseOpenAI([]byte(`{}`))
	_, _ = conv.ParseOpenAI([]byte(`{}`))

	if cb.State() != CircuitOpen {
		t.Fatalf("after 2 errors state = %s, want open", cb.State())
	}

	// 第 3 次应被熔断器拒绝
	_, err := conv.ParseOpenAI([]byte(`{}`))
	if err != ErrConverterCircuitOpen {
		t.Fatalf("expected ErrConverterCircuitOpen, got %v", err)
	}
}

var errMockParse = &mockError{"mock parse error"}

type mockError struct{ s string }

func (e *mockError) Error() string { return e.s }

func TestTransportIRConverter_CircuitBreaker_RecordsSuccess(t *testing.T) {
	inner := &mockIRAdapter{
		parseOpenAIFunc: func(body []byte) (*ir.InternalRequest, error) {
			return &ir.InternalRequest{Model: "gpt-4o"}, nil
		},
		serializeOpenAIFunc: func(req *ir.InternalRequest) ([]byte, error) {
			return []byte(`{"model":"gpt-4o"}`), nil
		},
	}
	cb := &StreamCircuitBreaker{
		threshold: 10,
		window:    1000000 * 1000,
		cooldown:  1000000 * 1000,
		state:     CircuitClosed,
	}
	conv := NewTransportIRConverter(inner)
	conv.SetCircuitBreaker(cb)

	// Parse 成功不记录错误
	_, _ = conv.ParseOpenAI([]byte(`{"model":"gpt-4o"}`))

	// Serialize 成功应 RecordSuccess
	req := &ir.InternalRequest{Model: "gpt-4o"}
	_, _ = conv.SerializeOpenAI(req)

	// 熔断器应仍为 Closed
	if cb.State() != CircuitClosed {
		t.Fatalf("after success state = %s, want closed", cb.State())
	}
}

// --- 测试：无 extensions 时无副作用 ---

func TestTransportIRConverter_NoExtensions_NoSideEffect(t *testing.T) {
	inner := &mockIRAdapter{
		parseOpenAIFunc: func(body []byte) (*ir.InternalRequest, error) {
			return &ir.InternalRequest{Model: "gpt-4o"}, nil
		},
		serializeOpenAIFunc: func(req *ir.InternalRequest) ([]byte, error) {
			return []byte(`{"model":"gpt-4o"}`), nil
		},
	}
	conv := NewTransportIRConverter(inner)

	// 标准请求（无非标字段）
	body := []byte(`{"model":"gpt-4o","messages":[]}`)
	req, err := conv.ParseOpenAI(body)
	if err != nil {
		t.Fatalf("ParseOpenAI: %v", err)
	}

	// Extensions 应为空或 nil
	if len(req.Extensions) != 0 {
		t.Errorf("standard request should have empty Extensions: %v", req.Extensions)
	}

	// Serialize 不应添加任何字段
	out, err := conv.SerializeOpenAI(req)
	if err != nil {
		t.Fatalf("SerializeOpenAI: %v", err)
	}
	if string(out) != `{"model":"gpt-4o"}` {
		t.Errorf("output should be unchanged: %s", out)
	}
}

// --- 测试：Ollama 特有字段保留 ---

func TestTransportIRConverter_OllamaFields_Preserved(t *testing.T) {
	inner := &mockIRAdapter{
		parseOpenAIFunc: func(body []byte) (*ir.InternalRequest, error) {
			return &ir.InternalRequest{Model: "ollama/llama3", Extensions: nil}, nil
		},
		serializeOpenAIFunc: func(req *ir.InternalRequest) ([]byte, error) {
			return []byte(`{"model":"ollama/llama3","messages":[]}`), nil
		},
	}
	conv := NewTransportIRConverter(inner)

	// 包含 Ollama 特有字段的请求
	body := []byte(`{"model":"ollama/llama3","messages":[],"keep_alive":"5m","format":"json","context":[1,2,3]}`)
	req, err := conv.ParseOpenAI(body)
	if err != nil {
		t.Fatalf("ParseOpenAI: %v", err)
	}

	// 验证 Extensions 包含 Ollama 字段
	if len(req.Extensions) == 0 {
		t.Fatal("Ollama fields should be extracted to Extensions")
	}
	if string(req.Extensions["keep_alive"]) != `"5m"` {
		t.Errorf("keep_alive = %s, want \"5m\"", req.Extensions["keep_alive"])
	}
	if string(req.Extensions["format"]) != `"json"` {
		t.Errorf("format = %s, want \"json\"", req.Extensions["format"])
	}
	if string(req.Extensions["context"]) != `[1,2,3]` {
		t.Errorf("context = %s, want [1,2,3]", req.Extensions["context"])
	}

	// Serialize 应还原所有 Ollama 字段
	out, err := conv.SerializeOpenAI(req)
	if err != nil {
		t.Fatalf("SerializeOpenAI: %v", err)
	}

	var m map[string]json.RawMessage
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("output invalid JSON: %v", err)
	}
	if string(m["keep_alive"]) != `"5m"` {
		t.Errorf("keep_alive not restored: %s", out)
	}
	if string(m["format"]) != `"json"` {
		t.Errorf("format not restored: %s", out)
	}
	if string(m["context"]) != `[1,2,3]` {
		t.Errorf("context not restored: %s", out)
	}
}

// --- 测试：GLM 扩展字段保留 ---

func TestTransportIRConverter_GLMFields_Preserved(t *testing.T) {
	inner := &mockIRAdapter{
		parseOpenAIFunc: func(body []byte) (*ir.InternalRequest, error) {
			return &ir.InternalRequest{Model: "glm-4-plus", Extensions: nil}, nil
		},
		serializeOpenAIFunc: func(req *ir.InternalRequest) ([]byte, error) {
			return []byte(`{"model":"glm-4-plus","messages":[]}`), nil
		},
	}
	conv := NewTransportIRConverter(inner)

	// 包含 GLM 扩展字段的请求
	body := []byte(`{"model":"glm-4-plus","messages":[],"web_search":true,"retrieval":{"knowledge_base":"kb_001"}}`)
	req, err := conv.ParseOpenAI(body)
	if err != nil {
		t.Fatalf("ParseOpenAI: %v", err)
	}

	// 验证 Extensions 包含 GLM 字段
	if len(req.Extensions) == 0 {
		t.Fatal("GLM fields should be extracted to Extensions")
	}
	if string(req.Extensions["web_search"]) != `true` {
		t.Errorf("web_search = %s, want true", req.Extensions["web_search"])
	}
	if string(req.Extensions["retrieval"]) != `{"knowledge_base":"kb_001"}` {
		t.Errorf("retrieval = %s, want nested object", req.Extensions["retrieval"])
	}

	// Serialize 应还原所有 GLM 字段
	out, err := conv.SerializeOpenAI(req)
	if err != nil {
		t.Fatalf("SerializeOpenAI: %v", err)
	}

	var m map[string]json.RawMessage
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("output invalid JSON: %v", err)
	}
	if string(m["web_search"]) != `true` {
		t.Errorf("web_search not restored: %s", out)
	}
	if string(m["retrieval"]) != `{"knowledge_base":"kb_001"}` {
		t.Errorf("retrieval not restored: %s", out)
	}
}

// --- 测试：DeepSeek reasoning_tokens 字段保留 ---

func TestTransportIRConverter_DeepSeekFields_Preserved(t *testing.T) {
	inner := &mockIRAdapter{
		parseOpenAIResponseFunc: func(body []byte) (*ir.InternalResponse, error) {
			return &ir.InternalResponse{ID: "chatcmpl-1", Model: "deepseek-chat", Extensions: nil}, nil
		},
		serializeOpenAIResponseFunc: func(resp *ir.InternalResponse, clientModel string) ([]byte, error) {
			return []byte(`{"id":"chatcmpl-1","model":"deepseek-chat"}`), nil
		},
	}
	conv := NewTransportIRConverter(inner)

	// DeepSeek 响应包含 reasoning_tokens
	body := []byte(`{"id":"chatcmpl-1","model":"deepseek-chat","usage":{"reasoning_tokens":150,"total_tokens":200}}`)
	resp, err := conv.ParseOpenAIResponse(body)
	if err != nil {
		t.Fatalf("ParseOpenAIResponse: %v", err)
	}

	// 验证 Extensions 包含 reasoning_tokens (嵌套在 usage 中)
	if len(resp.Extensions) == 0 {
		t.Fatal("DeepSeek usage fields should be extracted to Extensions")
	}
	// usage 对象会被整体提取（因为包含非标字段 reasoning_tokens）
	if _, ok := resp.Extensions["usage"]; !ok {
		t.Errorf("usage object should be in Extensions: %v", resp.Extensions)
	}

	// Serialize 应还原 usage 字段
	out, err := conv.SerializeOpenAIResponse(resp, "deepseek-chat")
	if err != nil {
		t.Fatalf("SerializeOpenAIResponse: %v", err)
	}

	var m map[string]json.RawMessage
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("output invalid JSON: %v", err)
	}
	if _, ok := m["usage"]; !ok {
		t.Errorf("usage not restored: %s", out)
	}
}
