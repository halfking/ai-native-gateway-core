package routing

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/ir"
	"github.com/kaixuan/llm-gateway-go/provider"
)

// irAdapterForTest implements IRConverter for testing.
type irAdapterForTest struct{}

func (a *irAdapterForTest) ParseOpenAI(body []byte) (*ir.InternalRequest, error) {
	return ir.ParseOpenAI(body)
}

func (a *irAdapterForTest) ParseAnthropic(body []byte) (*ir.InternalRequest, error) {
	return ir.ParseAnthropic(body)
}

func (a *irAdapterForTest) SerializeOpenAI(req *ir.InternalRequest) ([]byte, error) {
	return ir.SerializeOpenAI(req)
}

func (a *irAdapterForTest) SerializeAnthropic(req *ir.InternalRequest) ([]byte, error) {
	return ir.SerializeAnthropic(req)
}

// TestIRConverter_Q3_OpenAI_To_Anthropic tests the Q3 path:
// OpenAI client → irAdapter.ParseOpenAI → IR → irAdapter.SerializeAnthropic → Anthropic upstream.
func TestIRConverter_Q3_OpenAI_To_Anthropic(t *testing.T) {
	tests := []struct {
		name           string
		openAIBody     string
		wantModel      string
		wantSystem     string
		wantMsgCount   int
		wantTools      bool
		wantToolChoice bool
	}{
		{
			name: "basic message",
			openAIBody: `{
				"model": "gpt-4o",
				"messages": [{"role": "user", "content": "hello"}]
			}`,
			wantModel:    "gpt-4o",
			wantSystem:   "",
			wantMsgCount: 1,
			wantTools:    false,
		},
		{
			name: "with system prompt",
			openAIBody: `{
				"model": "gpt-4o",
				"messages": [
					{"role": "system", "content": "you are helpful"},
					{"role": "user", "content": "hi"}
				]
			}`,
			wantModel:    "gpt-4o",
			wantSystem:   "you are helpful",
			wantMsgCount: 1,
			wantTools:    false,
		},
		{
			name: "with tools",
			openAIBody: `{
				"model": "gpt-4o",
				"messages": [{"role": "user", "content": "what's the weather"}],
				"tools": [
					{
						"type": "function",
						"function": {
							"name": "get_weather",
							"description": "Get weather for a city",
							"parameters": {"type": "object", "properties": {"city": {"type": "string"}}}
						}
					}
				],
				"tool_choice": "auto"
			}`,
			wantModel:      "gpt-4o",
			wantSystem:     "",
			wantMsgCount:   1,
			wantTools:      true,
			wantToolChoice: true,
		},
		{
			name: "with sampling parameters",
			openAIBody: `{
				"model": "gpt-4o",
				"messages": [{"role": "user", "content": "hi"}],
				"temperature": 0.7,
				"top_p": 0.9,
				"max_tokens": 1024,
				"stop": ["END"]
			}`,
			wantModel:    "gpt-4o",
			wantMsgCount: 1,
			wantTools:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			adapter := &irAdapterForTest{}

			// Parse OpenAI → IR
			irReq, err := adapter.ParseOpenAI([]byte(tt.openAIBody))
			if err != nil {
				t.Fatalf("ParseOpenAI failed: %v", err)
			}

			// Verify IR fields
			if irReq.Model != tt.wantModel {
				t.Errorf("IR model = %q, want %q", irReq.Model, tt.wantModel)
			}
			if tt.wantSystem != "" {
				if irReq.System == nil || irReq.System.Content != tt.wantSystem {
					t.Errorf("IR system = %v, want %q", irReq.System, tt.wantSystem)
				}
			}
			if len(irReq.Messages) != tt.wantMsgCount {
				t.Errorf("IR message count = %d, want %d", len(irReq.Messages), tt.wantMsgCount)
			}
			if tt.wantTools && len(irReq.Tools) == 0 {
				t.Errorf("IR tools missing, want at least 1 tool")
			}
			if tt.wantToolChoice && irReq.ToolChoice == nil {
				t.Errorf("IR tool_choice missing, want non-nil")
			}

			// Serialize IR → Anthropic
			anthBody, err := adapter.SerializeAnthropic(irReq)
			if err != nil {
				t.Fatalf("SerializeAnthropic failed: %v", err)
			}

			// Parse Anthropic result to verify structure
			var anth map[string]any
			if err := json.Unmarshal(anthBody, &anth); err != nil {
				t.Fatalf("SerializeAnthropic output is not valid JSON: %v", err)
			}

			if anth["model"] != tt.wantModel {
				t.Errorf("Anthropic model = %v, want %q", anth["model"], tt.wantModel)
			}
			if tt.wantSystem != "" {
				if anth["system"] != tt.wantSystem {
					t.Errorf("Anthropic system = %v, want %q", anth["system"], tt.wantSystem)
				}
			}
			msgs, ok := anth["messages"].([]any)
			if !ok {
				t.Fatalf("Anthropic messages missing or not array")
			}
			if len(msgs) != tt.wantMsgCount {
				t.Errorf("Anthropic message count = %d, want %d", len(msgs), tt.wantMsgCount)
			}
		})
	}
}

// TestIRConverter_Q2_Anthropic_To_OpenAI tests the Q2 path:
// Anthropic client → irAdapter.ParseAnthropic → IR → irAdapter.SerializeOpenAI → OpenAI upstream.
func TestIRConverter_Q2_Anthropic_To_OpenAI(t *testing.T) {
	tests := []struct {
		name         string
		anthropicBody string
		wantModel    string
		wantSystem   string
		wantMsgCount int
		wantTools    bool
	}{
		{
			name: "basic message",
			anthropicBody: `{
				"model": "claude-3-5-sonnet",
				"messages": [{"role": "user", "content": "hello"}]
			}`,
			wantModel:    "claude-3-5-sonnet",
			wantSystem:   "",
			wantMsgCount: 1,
			wantTools:    false,
		},
		{
			name: "with system prompt",
			anthropicBody: `{
				"model": "claude-3-5-sonnet",
				"system": "you are helpful",
				"messages": [{"role": "user", "content": "hi"}]
			}`,
			wantModel:    "claude-3-5-sonnet",
			wantSystem:   "you are helpful",
			wantMsgCount: 1, // IR stores system separately; Messages has only user msg
			wantTools:    false,
		},
		{
			name: "with tools",
			anthropicBody: `{
				"model": "claude-3-5-sonnet",
				"messages": [{"role": "user", "content": "what's the weather"}],
				"tools": [
					{
						"name": "get_weather",
						"description": "Get weather for a city",
						"input_schema": {"type": "object", "properties": {"city": {"type": "string"}}}
					}
				]
			}`,
			wantModel:    "claude-3-5-sonnet",
			wantSystem:   "",
			wantMsgCount: 1,
			wantTools:    true,
		},
		{
			name: "with sampling parameters",
			anthropicBody: `{
				"model": "claude-3-5-sonnet",
				"messages": [{"role": "user", "content": "hi"}],
				"temperature": 0.7,
				"top_p": 0.9,
				"max_tokens": 1024,
				"stop_sequences": ["END"]
			}`,
			wantModel:    "claude-3-5-sonnet",
			wantMsgCount: 1,
			wantTools:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			adapter := &irAdapterForTest{}

			// Parse Anthropic → IR
			irReq, err := adapter.ParseAnthropic([]byte(tt.anthropicBody))
			if err != nil {
				t.Fatalf("ParseAnthropic failed: %v", err)
			}

			// Verify IR fields
			if irReq.Model != tt.wantModel {
				t.Errorf("IR model = %q, want %q", irReq.Model, tt.wantModel)
			}
			if tt.wantSystem != "" {
				if irReq.System == nil || irReq.System.Content != tt.wantSystem {
					t.Errorf("IR system = %v, want %q", irReq.System, tt.wantSystem)
				}
			}
			if len(irReq.Messages) != tt.wantMsgCount {
				t.Errorf("IR message count = %d, want %d", len(irReq.Messages), tt.wantMsgCount)
			}
			if tt.wantTools && len(irReq.Tools) == 0 {
				t.Errorf("IR tools missing, want at least 1 tool")
			}

			// Serialize IR → OpenAI
			openAIBody, err := adapter.SerializeOpenAI(irReq)
			if err != nil {
				t.Fatalf("SerializeOpenAI failed: %v", err)
			}

			// Parse OpenAI result to verify structure
			var openAI map[string]any
			if err := json.Unmarshal(openAIBody, &openAI); err != nil {
				t.Fatalf("SerializeOpenAI output is not valid JSON: %v", err)
			}

			if openAI["model"] != tt.wantModel {
				t.Errorf("OpenAI model = %v, want %q", openAI["model"], tt.wantModel)
			}
			msgs, ok := openAI["messages"].([]any)
			if !ok {
				t.Fatalf("OpenAI messages missing or not array")
			}
			if len(msgs) == 0 {
				t.Errorf("OpenAI messages should not be empty")
			}
			if tt.wantTools {
				tools, ok := openAI["tools"].([]any)
				if !ok || len(tools) == 0 {
					t.Errorf("OpenAI tools missing or empty, want at least 1 tool")
				}
			}
		})
	}
}

// TestIRConverter_FinalizeOpenAIUpstreamBody_IRPath tests that when e.IR is set,
// finalizeOpenAIUpstreamBody uses the IR path (Q2: anthropic → openai).
func TestIRConverter_FinalizeOpenAIUpstreamBody_IRPath(t *testing.T) {
	adapter := &irAdapterForTest{}
	executor := &Executor{IR: adapter}

	anthropicBody := `{
		"model": "claude-3-5-sonnet",
		"system": "you are a helpful assistant",
		"messages": [
			{"role": "user", "content": "hello"}
		]
	}`

	req := httptest.NewRequest("POST", "/v1/messages", nil)
	cand := provider.Candidate{
		ProviderID:   1,
		CredentialID: 100,
		Protocol:     "openai-completions",
		RawModel:     "gpt-4o",
	}
	params := &ExecParams{
		R:             req,
		BodyBytes:     []byte(anthropicBody),
		ClientModel:   "claude-3-5-sonnet",
		OutboundModel: "gpt-4o",
		ClientProtocol: "anthropic-messages",
	}

	result, err := executor.finalizeOpenAIUpstreamBody(params, cand, []byte(anthropicBody))
	if err != nil {
		t.Fatalf("finalizeOpenAIUpstreamBody failed: %v", err)
	}

	// Verify output is valid OpenAI JSON
	var openAI map[string]any
	if err := json.Unmarshal(result, &openAI); err != nil {
		t.Fatalf("finalizeOpenAIUpstreamBody output is not valid JSON: %v", err)
	}

	if openAI["model"] != "gpt-4o" {
		t.Errorf("model = %v, want %q", openAI["model"], "gpt-4o")
	}
}

// TestIRConverter_PrepareAnthropicRequestBody_IRPath tests that when e.IR is set,
// prepareAnthropicRequestBody uses the IR path (Q3: openai → anthropic).
func TestIRConverter_PrepareAnthropicRequestBody_IRPath(t *testing.T) {
	adapter := &irAdapterForTest{}
	executor := &Executor{IR: adapter}

	openAIBody := `{
		"model": "gpt-4o",
		"messages": [
			{"role": "system", "content": "you are helpful"},
			{"role": "user", "content": "hello"}
		],
		"tools": [
			{
				"type": "function",
				"function": {
					"name": "get_weather",
					"description": "Get weather",
					"parameters": {"type": "object", "properties": {"city": {"type": "string"}}}
				}
			}
		],
		"tool_choice": "auto"
	}`

	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	cand := provider.Candidate{
		ProviderID:   2,
		CredentialID: 200,
		Protocol:     "anthropic-messages",
		RawModel:     "claude-3-5-sonnet",
	}
	params := &ExecParams{
		R:             req,
		BodyBytes:     []byte(openAIBody),
		ClientModel:   "gpt-4o",
		OutboundModel: "claude-3-5-sonnet",
		ClientProtocol: "openai-completions",
	}

	result, err := executor.prepareAnthropicRequestBody(params, cand, []byte(openAIBody))
	if err != nil {
		t.Fatalf("prepareAnthropicRequestBody failed: %v", err)
	}

	// Verify output is valid Anthropic JSON
	var anth map[string]any
	if err := json.Unmarshal(result, &anth); err != nil {
		t.Fatalf("prepareAnthropicRequestBody output is not valid JSON: %v", err)
	}

	if anth["model"] != "claude-3-5-sonnet" {
		t.Errorf("model = %v, want %q", anth["model"], "claude-3-5-sonnet")
	}
	if anth["system"] != "you are helpful" {
		t.Errorf("system = %v, want %q", anth["system"], "you are helpful")
	}
	msgs, ok := anth["messages"].([]any)
	if !ok {
		t.Fatalf("messages missing or not array")
	}
	if len(msgs) != 1 {
		t.Errorf("message count = %d, want 1", len(msgs))
	}
}

// TestIRConverter_LegacyPath tests that when e.IR is nil, the legacy callback path is used.
func TestIRConverter_LegacyPath(t *testing.T) {
	// Set up executor WITHOUT IR converter
	executor := &Executor{}

	openAIBody := `{"model": "gpt-4o", "messages": [{"role": "user", "content": "hi"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	cand := provider.Candidate{
		Protocol: "openai-completions",
		RawModel: "gpt-4o",
	}
	params := &ExecParams{
		R:           req,
		BodyBytes:   []byte(openAIBody),
		ClientModel: "gpt-4o",
	}

	// With nil IR, the legacy path is used (no conversion since ClientProtocol is not "anthropic-messages")
	result, err := executor.finalizeOpenAIUpstreamBody(params, cand, []byte(openAIBody))
	if err != nil {
		t.Fatalf("finalizeOpenAIUpstreamBody failed: %v", err)
	}

	// Result should be the original body (no conversion needed)
	if string(result) != openAIBody {
		t.Errorf("legacy path should return original body, got: %s", string(result))
	}
}

// TestIRConverter_RoundTrip_OpenAI_Anthropic_OpenAI tests:
// OpenAI → IR → Anthropic → IR → OpenAI (round trip preserves data).
func TestIRConverter_RoundTrip_OpenAI_Anthropic_OpenAI(t *testing.T) {
	adapter := &irAdapterForTest{}

	original := `{
		"model": "gpt-4o",
		"messages": [
			{"role": "system", "content": "you are a helpful assistant"},
			{"role": "user", "content": "what's the weather in tokyo?"},
			{"role": "assistant", "content": "I'll check that for you"},
			{"role": "tool", "tool_call_id": "call_123", "content": "sunny, 25C"},
			{"role": "assistant", "content": "It's sunny and 25C in Tokyo!"}
		],
		"temperature": 0.7,
		"top_p": 0.9,
		"max_tokens": 2048,
		"tools": [
			{
				"type": "function",
				"function": {
					"name": "get_weather",
					"description": "Get weather for a city",
					"parameters": {
						"type": "object",
						"properties": {
							"city": {"type": "string", "description": "city name"}
						},
						"required": ["city"]
					}
				}
			}
		],
		"tool_choice": {"type": "function", "function": {"name": "get_weather"}}
	}`

	// OpenAI → IR
	irReq, err := adapter.ParseOpenAI([]byte(original))
	if err != nil {
		t.Fatalf("ParseOpenAI failed: %v", err)
	}

	// IR → Anthropic
	anthBody, err := adapter.SerializeAnthropic(irReq)
	if err != nil {
		t.Fatalf("SerializeAnthropic failed: %v", err)
	}

	// Anthropic → IR
	irReq2, err := adapter.ParseAnthropic(anthBody)
	if err != nil {
		t.Fatalf("ParseAnthropic failed: %v", err)
	}

	// IR → OpenAI
	finalBody, err := adapter.SerializeOpenAI(irReq2)
	if err != nil {
		t.Fatalf("SerializeOpenAI failed: %v", err)
	}

	// Parse both to compare structure
	var orig, final map[string]any
	json.Unmarshal([]byte(original), &orig)
	json.Unmarshal(finalBody, &final)

	// Model should be preserved
	if final["model"] != orig["model"] {
		t.Errorf("model = %v, want %v", final["model"], orig["model"])
	}

	// Messages count should be preserved
	origMsgs := orig["messages"].([]any)
	finalMsgs := final["messages"].([]any)
	if len(finalMsgs) != len(origMsgs) {
		t.Errorf("message count = %d, want %d", len(finalMsgs), len(origMsgs))
	}

	// Temperature should be preserved
	if final["temperature"] != orig["temperature"] {
		t.Errorf("temperature = %v, want %v", final["temperature"], orig["temperature"])
	}

	// Tools should be preserved (converted to OpenAI format)
	origTools := orig["tools"].([]any)
	finalTools := final["tools"].([]any)
	if len(finalTools) != len(origTools) {
		t.Errorf("tool count = %d, want %d", len(finalTools), len(origTools))
	}
}
