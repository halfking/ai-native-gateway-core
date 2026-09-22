// data/ir_field_golden_test.go
//
// D-01: IR JSON golden 对拍（多协议矩阵）。
// 这里只断言：每个协议的输出 JSON 是稳定的（不漂移），便于后续 golden 校验。
//
// 跑测：
//   go test -race -timeout 60s ./tests/48h-audit/D01-ir-lifecycle/data/...

package data

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/ir"
)

// goldenReq 构造一个稳定输入，用于每个协议的 JSON 形态断言。
func goldenReq() *ir.InternalRequest {
	return &ir.InternalRequest{
		Model:    "mock-stress-fast",
		Stream:   false,
		Messages: []ir.Message{{Role: "user", Content: []ir.ContentBlock{{Type: "text", Text: "1+1=?"}}}},
		MaxTokens: 16,
	}
}

// TestData_OpenAISerialize_StableJSON 防止 OpenAI 协议 JSON 字段漂移。
// 任何非兼容变化都会让这个测试 FAIL，作为 schema 变更的早期告警。
func TestData_OpenAISerialize_StableJSON(t *testing.T) {
	body, err := ir.SerializeOpenAI(goldenReq())
	if err != nil {
		t.Fatalf("serialize openai: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"model", "messages", "max_tokens"} {
		if _, ok := m[key]; !ok {
			t.Errorf("missing required key %q in OpenAI body: %s", key, body)
		}
	}
	// stream 字段在 false 时可被 omitempty 省略（默认），不强制必出现
}

// TestData_AnthropicSerialize_StableJSON
func TestData_AnthropicSerialize_StableJSON(t *testing.T) {
	body, err := ir.SerializeAnthropic(goldenReq())
	if err != nil {
		t.Fatalf("serialize anthropic: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"messages", "max_tokens"} {
		if _, ok := m[key]; !ok {
			t.Errorf("missing required key %q in Anthropic body: %s", key, body)
		}
	}
}

// TestData_ResponsesSerialize_StableJSON
func TestData_ResponsesSerialize_StableJSON(t *testing.T) {
	body, err := ir.SerializeResponsesRequest(goldenReq())
	if err != nil {
		t.Fatalf("serialize responses: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"input", "max_output_tokens"} {
		if _, ok := m[key]; !ok {
			t.Errorf("missing required key %q in Responses body: %s", key, body)
		}
	}
}

// TestData_GeminiSerialize_StableJSON —— Gemini model 在 URL，不在 body。
func TestData_GeminiSerialize_StableJSON(t *testing.T) {
	body, err := ir.SerializeGemini(goldenReq())
	if err != nil {
		t.Fatalf("serialize gemini: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := m["contents"]; !ok {
		t.Errorf("missing required key %q in Gemini body: %s", "contents", body)
	}
	if _, hasModel := m["model"]; hasModel {
		t.Errorf("Gemini body MUST NOT carry model (URL-path convention): %s", body)
	}
}

// TestData_AllProtocols_NoLeakOfInternalFields 防止内部字段泄漏到对外 body。
func TestData_AllProtocols_NoLeakOfInternalFields(t *testing.T) {
	req := goldenReq()
	// 假设 InternalRequest 有不该出现在 body 的字段：
	// RequestClass、TransportContext、InternalID 等
	for _, ser := range []struct {
		name string
		fn   func(*ir.InternalRequest) ([]byte, error)
	}{
		{"openai", ir.SerializeOpenAI},
		{"anthropic", ir.SerializeAnthropic},
		{"gemini", ir.SerializeGemini},
		{"responses", ir.SerializeResponsesRequest},
	} {
		body, err := ser.fn(req)
		if err != nil {
			t.Fatalf("%s serialize: %v", ser.name, err)
		}
		s := string(body)
		for _, banned := range []string{"internal_id", "transport_ctx", "request_class"} {
			if strings.Contains(strings.ToLower(s), banned) {
				t.Errorf("%s body leaks internal field %q: %s", ser.name, banned, s)
			}
		}
	}
}