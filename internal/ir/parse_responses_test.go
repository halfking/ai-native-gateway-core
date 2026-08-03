package ir

import (
	"encoding/json"
	"testing"
)

// ParseResponses parses an OpenAI Responses API request body
// (POST /v1/responses) into an InternalRequest. This is the spec §7.1
// IR main-path extension for the Responses input direction, reversing the
// SerializeResponsesRequest mapping.
//
// See internal/ir/serialize_responses.go for the forward direction.

// ─── Basic request ────────────────────────────────────────────────────────

func TestParseResponses_Basic(t *testing.T) {
	body := []byte(`{
  "model": "gpt-4o",
  "instructions": "You are a helpful assistant.",
  "input": [
    {"role": "user", "content": [{"type": "input_text", "text": "What is the weather in Tokyo?"}]},
    {"role": "assistant", "content": [{"type": "output_text", "text": "It is sunny."}]}
  ]
}`)

	req, err := ParseResponses(body)
	if err != nil {
		t.Fatalf("ParseResponses: %v", err)
	}

	if req.Model != "gpt-4o" {
		t.Errorf("Model = %q, want gpt-4o", req.Model)
	}

	if req.SourceProtocol != ProtocolOpenAIResponses {
		t.Errorf("SourceProtocol = %q, want %q", req.SourceProtocol, ProtocolOpenAIResponses)
	}

	// instructions → System.Content
	if req.System == nil || req.System.Content != "You are a helpful assistant." {
		t.Errorf("System = %+v, want content \"You are a helpful assistant.\"", req.System)
	}

	// input → Messages, with content block type normalized to "text"
	if len(req.Messages) != 2 {
		t.Fatalf("len(Messages) = %d, want 2", len(req.Messages))
	}
	if req.Messages[0].Role != "user" {
		t.Errorf("Messages[0].Role = %q, want user", req.Messages[0].Role)
	}
	if len(req.Messages[0].Content) != 1 {
		t.Fatalf("Messages[0] content blocks = %d, want 1", len(req.Messages[0].Content))
	}
	if req.Messages[0].Content[0].Type != "text" {
		t.Errorf("Messages[0].Content[0].Type = %q, want \"text\"", req.Messages[0].Content[0].Type)
	}
	if req.Messages[0].Content[0].Text != "What is the weather in Tokyo?" {
		t.Errorf("Messages[0].Content[0].Text = %q, want the question", req.Messages[0].Content[0].Text)
	}

	// assistant output_text also normalizes to text
	if req.Messages[1].Role != "assistant" {
		t.Errorf("Messages[1].Role = %q, want assistant", req.Messages[1].Role)
	}
	if req.Messages[1].Content[0].Type != "text" || req.Messages[1].Content[0].Text != "It is sunny." {
		t.Errorf("Messages[1].Content[0] = %+v, want normalized text", req.Messages[1].Content[0])
	}
}

// ─── nil / empty body ─────────────────────────────────────────────────────

func TestParseResponses_EmptyBody(t *testing.T) {
	// nil body must not panic; returns an empty IR with the protocol set.
	req, err := ParseResponses(nil)
	if err != nil {
		t.Fatalf("ParseResponses(nil): %v", err)
	}
	if req == nil {
		t.Fatal("ParseResponses(nil) returned nil request")
	}
	if req.SourceProtocol != ProtocolOpenAIResponses {
		t.Errorf("SourceProtocol = %q, want %q", req.SourceProtocol, ProtocolOpenAIResponses)
	}

	// empty object body
	req2, err := ParseResponses([]byte(`{}`))
	if err != nil {
		t.Fatalf("ParseResponses({}): %v", err)
	}
	if req2.SourceProtocol != ProtocolOpenAIResponses {
		t.Errorf("SourceProtocol = %q, want %q", req2.SourceProtocol, ProtocolOpenAIResponses)
	}
}

// ─── tools + tool_choice ──────────────────────────────────────────────────

func TestParseResponses_ToolsAndToolChoice(t *testing.T) {
	body := []byte(`{
  "model": "gpt-4o",
  "tools": [
    {
      "type": "function",
      "name": "get_weather",
      "description": "Get current weather for a city",
      "parameters": {
        "type": "object",
        "properties": {"city": {"type": "string"}},
        "required": ["city"]
      }
    }
  ],
  "tool_choice": "auto",
  "input": [{"role": "user", "content": [{"type": "input_text", "text": "weather?"}]}]
}`)

	req, err := ParseResponses(body)
	if err != nil {
		t.Fatalf("ParseResponses: %v", err)
	}

	// One flat-format function tool parsed into a ToolDefinition.
	if len(req.Tools) != 1 {
		t.Fatalf("len(Tools) = %d, want 1", len(req.Tools))
	}
	tool := req.Tools[0]
	if tool.Name != "get_weather" {
		t.Errorf("tool.Name = %q, want get_weather", tool.Name)
	}
	if tool.Description != "Get current weather for a city" {
		t.Errorf("tool.Description = %q", tool.Description)
	}
	// parameters carried as raw JSON.
	var params map[string]any
	if err := json.Unmarshal(tool.Parameters, &params); err != nil {
		t.Fatalf("tool.Parameters not valid JSON: %v", err)
	}
	if params["type"] != "object" {
		t.Errorf("parameters.type = %v, want object", params["type"])
	}

	// tool_choice = "auto".
	if req.ToolChoice == nil {
		t.Fatal("ToolChoice is nil")
	}
	if req.ToolChoice.Type != "auto" {
		t.Errorf("ToolChoice.Type = %q, want auto", req.ToolChoice.Type)
	}
}

func TestParseResponses_ToolChoiceForced(t *testing.T) {
	body := []byte(`{
  "model": "gpt-4o",
  "tool_choice": {"type": "function", "name": "get_weather"},
  "input": "weather?"
}`)
	req, err := ParseResponses(body)
	if err != nil {
		t.Fatalf("ParseResponses: %v", err)
	}
	if req.ToolChoice == nil {
		t.Fatal("ToolChoice is nil")
	}
	if req.ToolChoice.Name != "get_weather" {
		t.Errorf("ToolChoice.Name = %q, want get_weather", req.ToolChoice.Name)
	}
}

// ─── input simplified form (bare string) ──────────────────────────────────

func TestParseResponses_InputString(t *testing.T) {
	// Simplified form: "input" is a bare string rather than an array.
	body := []byte(`{"model":"gpt-4o","input":"Hello, what is 2+2?"}`)
	req, err := ParseResponses(body)
	if err != nil {
		t.Fatalf("ParseResponses: %v", err)
	}
	if len(req.Messages) != 1 {
		t.Fatalf("len(Messages) = %d, want 1", len(req.Messages))
	}
	if req.Messages[0].Role != "user" {
		t.Errorf("Messages[0].Role = %q, want user", req.Messages[0].Role)
	}
	if len(req.Messages[0].Content) != 1 {
		t.Fatalf("content blocks = %d, want 1", len(req.Messages[0].Content))
	}
	if req.Messages[0].Content[0].Type != "text" || req.Messages[0].Content[0].Text != "Hello, what is 2+2?" {
		t.Errorf("Messages[0].Content[0] = %+v, want normalized text", req.Messages[0].Content[0])
	}
}

// ─── sampling/stream/previous_response_id/max_output_tokens ────────────────

func TestParseResponses_SamplingAndChaining(t *testing.T) {
	body := []byte(`{
  "model": "gpt-4o",
  "input": "continue",
  "max_output_tokens": 1024,
  "temperature": 0.7,
  "top_p": 0.9,
  "stream": true,
  "previous_response_id": "resp_abc123",
  "stop": ["END"]
}`)

	req, err := ParseResponses(body)
	if err != nil {
		t.Fatalf("ParseResponses: %v", err)
	}

	if req.MaxTokens != 1024 {
		t.Errorf("MaxTokens = %d, want 1024", req.MaxTokens)
	}
	if req.Temperature == nil || *req.Temperature != 0.7 {
		t.Errorf("Temperature = %v, want 0.7", req.Temperature)
	}
	if req.TopP == nil || *req.TopP != 0.9 {
		t.Errorf("TopP = %v, want 0.9", req.TopP)
	}
	if !req.Stream {
		t.Errorf("Stream = false, want true")
	}
	if req.PreviousResponseID != "resp_abc123" {
		t.Errorf("PreviousResponseID = %q, want resp_abc123", req.PreviousResponseID)
	}
	if len(req.Stop) != 1 || req.Stop[0] != "END" {
		t.Errorf("Stop = %v, want [END]", req.Stop)
	}
}

// ─── Round-trip Parse → Serialize (key fields preserved) ───────────────────

func TestParseResponses_RoundTripWithSerialize(t *testing.T) {
	// A canonical Responses body covering the main field set.
	srcBody := []byte(`{
  "model": "gpt-4o",
  "instructions": "You are helpful.",
  "input": [
    {"role": "user", "content": [{"type": "input_text", "text": "hi"}]},
    {"role": "assistant", "content": [{"type": "output_text", "text": "hello"}]}
  ],
  "max_output_tokens": 256,
  "temperature": 0.5,
  "stream": true,
  "tools": [
    {"type": "function", "name": "f", "description": "d", "parameters": {"type": "object"}}
  ],
  "tool_choice": "auto",
  "previous_response_id": "resp_xyz"
}`)

	parsed, err := ParseResponses(srcBody)
	if err != nil {
		t.Fatalf("ParseResponses: %v", err)
	}

	roundTripped, err := SerializeResponsesRequest(parsed)
	if err != nil {
		t.Fatalf("SerializeResponsesRequest: %v", err)
	}

	var out map[string]any
	if err := json.Unmarshal(roundTripped, &out); err != nil {
		t.Fatalf("round-trip output not valid JSON: %v", err)
	}

	if out["model"] != "gpt-4o" {
		t.Errorf("model round-trip = %v", out["model"])
	}
	if out["instructions"] != "You are helpful." {
		t.Errorf("instructions round-trip = %v", out["instructions"])
	}
	if out["max_output_tokens"] != float64(256) {
		t.Errorf("max_output_tokens round-trip = %v", out["max_output_tokens"])
	}
	if temp, _ := out["temperature"].(float64); temp != 0.5 {
		t.Errorf("temperature round-trip = %v", out["temperature"])
	}
	if s, _ := out["stream"].(bool); !s {
		t.Errorf("stream round-trip = %v", out["stream"])
	}
	if out["previous_response_id"] != "resp_xyz" {
		t.Errorf("previous_response_id round-trip = %v", out["previous_response_id"])
	}

	// input[] must have two items (user + assistant), system hoisted to instructions.
	input, _ := out["input"].([]any)
	if len(input) != 2 {
		t.Errorf("input round-trip len = %d, want 2", len(input))
	}

	// One flat function tool.
	tools, _ := out["tools"].([]any)
	if len(tools) != 1 {
		t.Errorf("tools round-trip len = %d, want 1", len(tools))
	} else {
		tool := tools[0].(map[string]any)
		if tool["name"] != "f" {
			t.Errorf("tool name round-trip = %v", tool["name"])
		}
	}

	if out["tool_choice"] != "auto" {
		t.Errorf("tool_choice round-trip = %v", out["tool_choice"])
	}
}

// ─── image/file input blocks ───────────────────────────────────────────────

func TestParseResponses_ImageAndFileBlocks(t *testing.T) {
	body := []byte(`{
  "model": "gpt-4o",
  "input": [
    {"role": "user", "content": [
      {"type": "input_image", "image_url": "https://example.com/x.png", "detail": "high"},
      {"type": "input_file", "file": {"filename": "doc.pdf", "file_data": "data:application/pdf;base64,JVBERi0="}}
    ]}
  ]
}`)

	req, err := ParseResponses(body)
	if err != nil {
		t.Fatalf("ParseResponses: %v", err)
	}
	if len(req.Messages) != 1 || len(req.Messages[0].Content) != 2 {
		t.Fatalf("content blocks = %d, want 2", len(req.Messages[0].Content))
	}

	imgBlock := req.Messages[0].Content[0]
	if imgBlock.Type != "image" || imgBlock.Image == nil {
		t.Errorf("image block = %+v, want normalized image", imgBlock)
	} else if imgBlock.Image.URL != "https://example.com/x.png" {
		t.Errorf("image URL = %q", imgBlock.Image.URL)
	}

	docBlock := req.Messages[0].Content[1]
	if docBlock.Type != "document" || docBlock.Document == nil {
		t.Errorf("document block = %+v, want normalized document", docBlock)
	}
}

// ─── unknown fields → Extensions + anomaly ────────────────────────────────

func TestParseResponses_UnknownFieldPreserved(t *testing.T) {
	body := []byte(`{"model":"gpt-4o","input":"hi","custom_vendor_flag":"keep-me"}`)
	req, err := ParseResponses(body)
	if err != nil {
		t.Fatalf("ParseResponses: %v", err)
	}
	if req.Extensions == nil {
		t.Fatal("Extensions is nil; unknown field should be preserved")
	}
	raw, ok := req.Extensions["custom_vendor_flag"]
	if !ok {
		t.Fatalf("custom_vendor_flag not in Extensions: %+v", req.Extensions)
	}
	// Value is the raw JSON string token including quotes.
	if string(raw) != `"keep-me"` {
		t.Errorf("custom_vendor_flag raw = %s, want \"keep-me\"", raw)
	}
}
