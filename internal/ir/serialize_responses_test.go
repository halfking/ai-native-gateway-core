package ir

import (
	"encoding/json"
	"testing"
)

// SerializeResponsesRequest serializes an InternalRequest into an OpenAI
// Responses API request body (POST /v1/responses). This file is the spec
// §7.1 IR main-path extension for the Responses request direction.
//
// Wire shape (see platform.openai.com/docs/api-reference/responses/create):
//
//	{
//	  "model": "gpt-4o",
//	  "instructions": "You are a helpful assistant.",
//	  "input": [
//	    {"role":"user","content":[{"type":"input_text","text":"..."}]},
//	    {"role":"assistant","content":[{"type":"output_text","text":"..."}]}
//	  ],
//	  "max_output_tokens": 1024,
//	  "temperature": 0.7,
//	  "top_p": 0.9,
//	  "stream": true,
//	  "tools": [{"type":"function","name":"...","description":"...","parameters":{...}}],
//	  "tool_choice": "auto",
//	  "previous_response_id": "resp_abc",
//	  "metadata": {}
//	}
//
// Key differences from Chat Completions (SerializeOpenAI):
//   - input[] replaces messages[]
//   - each message's content is an array of typed blocks:
//       user/system/developer → {"type":"input_text","text":...}
//       assistant             → {"type":"output_text","text":...}
//       image                 → {"type":"input_image","image_url":...}
//       file                  → {"type":"input_file","file_data":...}
//   - system prompt becomes top-level "instructions" (string), NOT a message
//   - max_output_tokens replaces max_tokens
//   - tools entries are flat: {"type":"function","name":...,"parameters":...}
//     (no nested {"function":{...}} wrapper)

// ─── Basic request ──────────────────────────────────────────────────────────

func TestSerializeResponsesRequest_Basic(t *testing.T) {
	req := &InternalRequest{
		Model:  "gpt-4o",
		System: &SystemPrompt{Content: "You are a helpful assistant."},
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "What is the weather in Tokyo?"}}},
		},
	}

	body, err := SerializeResponsesRequest(req)
	if err != nil {
		t.Fatal(err)
	}

	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}

	// model passthrough
	if out["model"] != "gpt-4o" {
		t.Errorf("model = %v, want gpt-4o", out["model"])
	}

	// system → instructions (string), NOT a message
	if out["instructions"] != "You are a helpful assistant." {
		t.Errorf("instructions = %v, want system text", out["instructions"])
	}
	if _, hasMessages := out["messages"]; hasMessages {
		t.Error("messages key present; Responses uses input[], not messages[]")
	}

	// input array present with one user message
	input, ok := out["input"].([]any)
	if !ok {
		t.Fatalf("input is not an array: %T", out["input"])
	}
	if len(input) != 1 {
		t.Fatalf("len(input) = %d, want 1", len(input))
	}
	msg := input[0].(map[string]any)
	if msg["role"] != "user" {
		t.Errorf("input[0].role = %v, want user", msg["role"])
	}

	// content is a typed block array
	content, ok := msg["content"].([]any)
	if !ok {
		t.Fatalf("input[0].content is not array: %T", msg["content"])
	}
	if len(content) != 1 {
		t.Fatalf("len(content) = %d, want 1", len(content))
	}
	block := content[0].(map[string]any)
	if block["type"] != "input_text" {
		t.Errorf("content[0].type = %v, want input_text", block["type"])
	}
	if block["text"] != "What is the weather in Tokyo?" {
		t.Errorf("content[0].text = %v, want the prompt", block["text"])
	}
}

func TestSerializeResponsesRequest_AssistantOutputText(t *testing.T) {
	req := &InternalRequest{
		Model: "gpt-4o",
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}},
			{Role: "assistant", Content: []ContentBlock{{Type: "text", Text: "hello there"}}},
		},
	}

	body, err := SerializeResponsesRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	_ = json.Unmarshal(body, &out)

	input := out["input"].([]any)
	asst := input[1].(map[string]any)
	if asst["role"] != "assistant" {
		t.Fatalf("assistant role = %v", asst["role"])
	}
	block := asst["content"].([]any)[0].(map[string]any)
	// assistant text blocks use output_text, not input_text
	if block["type"] != "output_text" {
		t.Errorf("assistant text type = %v, want output_text", block["type"])
	}
	if block["text"] != "hello there" {
		t.Errorf("assistant text = %v", block["text"])
	}
}

// ─── Tools + tool_choice ────────────────────────────────────────────────────

func TestSerializeResponsesRequest_ToolsAndToolChoice(t *testing.T) {
	params := json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}`)
	req := &InternalRequest{
		Model: "gpt-4o",
		Tools: []ToolDefinition{{
			Name:        "get_weather",
			Description: "Get current weather for a city",
			Parameters:  params,
		}},
		ToolChoice: &ToolChoice{Type: "auto"},
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "weather?"}}},
		},
	}

	body, err := SerializeResponsesRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	_ = json.Unmarshal(body, &out)

	tools, ok := out["tools"].([]any)
	if !ok {
		t.Fatalf("tools is not array: %T", out["tools"])
	}
	if len(tools) != 1 {
		t.Fatalf("len(tools) = %d, want 1", len(tools))
	}
	tool := tools[0].(map[string]any)
	// Responses tools are FLAT: type+name+description+parameters at top level.
	// No nested {"function":{...}} wrapper (that's the Chat Completions shape).
	if tool["type"] != "function" {
		t.Errorf("tool.type = %v, want function", tool["type"])
	}
	if tool["name"] != "get_weather" {
		t.Errorf("tool.name = %v, want get_weather", tool["name"])
	}
	if tool["description"] != "Get current weather for a city" {
		t.Errorf("tool.description = %v", tool["description"])
	}
	if _, hasNestedFn := tool["function"]; hasNestedFn {
		t.Error("tool has nested 'function' wrapper; Responses tools must be flat")
	}
	// parameters preserved as JSON schema object
	paramsOut, _ := json.Marshal(tool["parameters"])
	var ps map[string]any
	_ = json.Unmarshal(paramsOut, &ps)
	if ps["type"] != "object" {
		t.Errorf("parameters.type = %v, want object", ps["type"])
	}

	// tool_choice: "auto" passes through as a bare string
	if out["tool_choice"] != "auto" {
		t.Errorf("tool_choice = %v, want auto", out["tool_choice"])
	}
}

func TestSerializeResponsesRequest_ToolChoiceForced(t *testing.T) {
	req := &InternalRequest{
		Model:      "gpt-4o",
		Tools:      []ToolDefinition{{Name: "get_weather"}},
		ToolChoice: &ToolChoice{Type: "tool", Name: "get_weather"},
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "x"}}},
		},
	}
	body, err := SerializeResponsesRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	_ = json.Unmarshal(body, &out)

	tc, ok := out["tool_choice"].(map[string]any)
	if !ok {
		t.Fatalf("tool_choice = %T, want object for forced tool", out["tool_choice"])
	}
	if tc["type"] != "function" {
		t.Errorf("tool_choice.type = %v, want function", tc["type"])
	}
	if tc["name"] != "get_weather" {
		t.Errorf("tool_choice.name = %v, want get_weather", tc["name"])
	}
}

// ─── stream + temperature + previous_response_id ────────────────────────────

func TestSerializeResponsesRequest_StreamTempPrevID(t *testing.T) {
	req := &InternalRequest{
		Model:              "gpt-4o",
		Stream:             true,
		Temperature:        floatPtr(0.5),
		TopP:               floatPtr(0.8),
		MaxTokens:          512,
		PreviousResponseID: "resp_abc123",
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "continue"}}},
		},
	}

	body, err := SerializeResponsesRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	_ = json.Unmarshal(body, &out)

	if out["stream"] != true {
		t.Errorf("stream = %v, want true", out["stream"])
	}
	if out["temperature"] != 0.5 {
		t.Errorf("temperature = %v, want 0.5", out["temperature"])
	}
	if out["top_p"] != 0.8 {
		t.Errorf("top_p = %v, want 0.8", out["top_p"])
	}
	if out["max_output_tokens"] != float64(512) {
		t.Errorf("max_output_tokens = %v, want 512", out["max_output_tokens"])
	}
	if out["previous_response_id"] != "resp_abc123" {
		t.Errorf("previous_response_id = %v, want resp_abc123", out["previous_response_id"])
	}
}

func TestSerializeResponsesRequest_NoInstructionsWhenNoSystem(t *testing.T) {
	req := &InternalRequest{
		Model: "gpt-4o",
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}},
		},
	}
	body, err := SerializeResponsesRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	_ = json.Unmarshal(body, &out)
	if _, has := out["instructions"]; has {
		t.Error("instructions present when System is nil")
	}
}

// ─── nil safety ─────────────────────────────────────────────────────────────

func TestSerializeResponsesRequest_NilRequest(t *testing.T) {
	body, err := SerializeResponsesRequest(nil)
	if err == nil {
		t.Fatal("expected error for nil request, got nil")
	}
	if body != nil {
		t.Errorf("expected nil body for nil request, got %v", body)
	}
}

// ─── Structural diffs vs SerializeOpenAI ─────────────────────────────────────
//
// These tests assert the wire-format divergence documented in the task spec:
// input vs messages, instructions vs system message, max_output_tokens vs
// max_tokens. They guard against an accidental copy of SerializeOpenAI's shape.

func TestSerializeResponsesRequest_UsesInputNotMessages(t *testing.T) {
	req := &InternalRequest{
		Model: "gpt-4o",
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}},
		},
	}

	respBody, err := SerializeResponsesRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	chatBody, err := SerializeOpenAI(req)
	if err != nil {
		t.Fatal(err)
	}

	var respOut, chatOut map[string]any
	_ = json.Unmarshal(respBody, &respOut)
	_ = json.Unmarshal(chatBody, &chatOut)

	if _, has := respOut["input"]; !has {
		t.Error("Responses output missing 'input' key")
	}
	if _, has := respOut["messages"]; has {
		t.Error("Responses output should NOT have 'messages' key")
	}
	if _, has := chatOut["messages"]; !has {
		t.Error("Chat output should have 'messages' key (sanity check)")
	}
}

func TestSerializeResponsesRequest_SystemIsInstructionsNotMessage(t *testing.T) {
	req := &InternalRequest{
		Model:  "gpt-4o",
		System: &SystemPrompt{Content: "be helpful"},
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}},
		},
	}

	respBody, err := SerializeResponsesRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	var respOut map[string]any
	_ = json.Unmarshal(respBody, &respOut)

	if respOut["instructions"] != "be helpful" {
		t.Errorf("instructions = %v, want 'be helpful'", respOut["instructions"])
	}
	// input must contain ONLY the user message, not a synthesized system message
	input := respOut["input"].([]any)
	if len(input) != 1 {
		t.Fatalf("len(input) = %d, want 1 (system should be instructions, not a message)", len(input))
	}
	if input[0].(map[string]any)["role"] != "user" {
		t.Errorf("input[0].role = %v, want user", input[0].(map[string]any)["role"])
	}
}

func TestSerializeResponsesRequest_MaxOutputTokensNotMaxTokens(t *testing.T) {
	req := &InternalRequest{
		Model:     "gpt-4o",
		MaxTokens: 100,
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}},
		},
	}

	respBody, err := SerializeResponsesRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	chatBody, err := SerializeOpenAI(req)
	if err != nil {
		t.Fatal(err)
	}

	var respOut, chatOut map[string]any
	_ = json.Unmarshal(respBody, &respOut)
	_ = json.Unmarshal(chatBody, &chatOut)

	if respOut["max_output_tokens"] != float64(100) {
		t.Errorf("Responses max_output_tokens = %v, want 100", respOut["max_output_tokens"])
	}
	if _, has := respOut["max_tokens"]; has {
		t.Error("Responses output should NOT have 'max_tokens' key (use max_output_tokens)")
	}
	if chatOut["max_tokens"] != float64(100) {
		t.Errorf("Chat max_tokens = %v, want 100 (sanity)", chatOut["max_tokens"])
	}
}

// ─── Extensions bypass ────────────────────────────────────────────────────────

func TestSerializeResponsesRequest_FunctionCallAndOutput(t *testing.T) {
	call := ToolCall{ID: "call_1", Type: "function"}
	call.Function.Name = "get_weather"
	call.Function.Arguments = `{"city":"Tokyo"}`
	req := &InternalRequest{Messages: []Message{
		{Role: "assistant", ToolCalls: []ToolCall{call}},
		{Role: "tool", ToolCallID: "call_1", Content: []ContentBlock{{Type: "text", Text: "sunny"}}},
	}}
	body, err := SerializeResponsesRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	input := out["input"].([]any)
	if len(input) != 2 {
		t.Fatalf("input length = %d, want 2", len(input))
	}
	callItem := input[0].(map[string]any)
	if callItem["type"] != "function_call" || callItem["call_id"] != "call_1" || callItem["arguments"] != `{"city":"Tokyo"}` {
		t.Errorf("function_call item = %+v", callItem)
	}
	outputItem := input[1].(map[string]any)
	if outputItem["type"] != "function_call_output" || outputItem["call_id"] != "call_1" || outputItem["output"] != "sunny" {
		t.Errorf("function_call_output item = %+v", outputItem)
	}
}

func TestSerializeResponsesRequest_ExtensionsBypass(t *testing.T) {
	// Non-standard fields preserved by the transport Extensions extractor should
	// round-trip on the Responses wire when source is itself Responses.
	req := &InternalRequest{
		Model:  "gpt-4o",
		Stream: false,
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}},
		},
		SourceProtocol: ProtocolOpenAIResponses,
		Extensions: map[string]json.RawMessage{
			"reasoning": json.RawMessage(`{"effort":"high"}`),
		},
	}

	body, err := SerializeResponsesRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	_ = json.Unmarshal(body, &out)

	reasoning, has := out["reasoning"]
	if !has {
		t.Fatal("Extensions 'reasoning' was not restored to output")
	}
	rmap := reasoning.(map[string]any)
	if rmap["effort"] != "high" {
		t.Errorf("reasoning.effort = %v, want high", rmap["effort"])
	}
}

func TestSerializeResponsesRequest_Metadata(t *testing.T) {
	req := &InternalRequest{
		Model:    "gpt-4o",
		Metadata: &Metadata{UserID: "u_123"},
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}},
		},
	}
	body, err := SerializeResponsesRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	_ = json.Unmarshal(body, &out)

	meta, has := out["metadata"]
	if !has {
		t.Fatal("metadata key missing")
	}
	m := meta.(map[string]any)
	if m["user_id"] != "u_123" {
		t.Errorf("metadata.user_id = %v, want u_123", m["user_id"])
	}
}
