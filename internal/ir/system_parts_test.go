package ir

import (
	"encoding/json"
	"testing"
)

// Parts-only System (Anthropic array system / Gemini systemInstruction parse
// output) must reach OpenAI system messages and Responses instructions.
// Regression for the 2026-09-05 round-2 audit P0 A-#13: serialize_openai and
// serialize_responses only read System.Content, so array-form system prompts
// were silently dropped on the default IR conversion path.
func TestSystemPartsReachOpenAIAndResponses(t *testing.T) {
	anthropicRaw := []byte(`{
		"model": "claude-sonnet-4-5",
		"max_tokens": 128,
		"system": [
			{"type": "text", "text": "You are a careful assistant."},
			{"type": "text", "text": "Answer in Chinese."}
		],
		"messages": [
			{"role": "user", "content": [{"type": "text", "text": "hi"}]}
		]
	}`)

	req, err := ParseAnthropic(anthropicRaw)
	if err != nil {
		t.Fatalf("ParseAnthropic failed: %v", err)
	}
	if req.System == nil || req.System.Content != "" || len(req.System.Parts) != 2 {
		t.Fatalf("expected Parts-only system, got: %+v", req.System)
	}

	// OpenAI direction.
	openaiOut, err := SerializeOpenAI(req)
	if err != nil {
		t.Fatalf("SerializeOpenAI failed: %v", err)
	}
	var openaiWire struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(openaiOut, &openaiWire); err != nil {
		t.Fatalf("unmarshal openai: %v", err)
	}
	if len(openaiWire.Messages) == 0 || openaiWire.Messages[0].Role != "system" {
		t.Fatalf("first OpenAI message must be system, got: %+v", openaiWire.Messages)
	}
	var sysText string
	if err := json.Unmarshal(openaiWire.Messages[0].Content, &sysText); err != nil {
		t.Fatalf("system content not a string: %v", err)
	}
	want := "You are a careful assistant.\nAnswer in Chinese."
	if sysText != want {
		t.Fatalf("system text mismatch:\n got: %q\nwant: %q", sysText, want)
	}

	// Responses direction.
	respOut, err := SerializeResponsesRequest(req)
	if err != nil {
		t.Fatalf("SerializeResponsesRequest failed: %v", err)
	}
	var respWire struct {
		Instructions string `json:"instructions"`
	}
	if err := json.Unmarshal(respOut, &respWire); err != nil {
		t.Fatalf("unmarshal responses: %v", err)
	}
	if respWire.Instructions != want {
		t.Fatalf("instructions mismatch:\n got: %q\nwant: %q", respWire.Instructions, want)
	}
}

// Gemini systemInstruction parses into Parts as well; the same loss applied to
// Gemini-native clients routed to OpenAI-family upstreams.
func TestGeminiSystemInstructionReachesOpenAI(t *testing.T) {
	geminiRaw := []byte(`{
		"contents": [
			{"role": "user", "parts": [{"text": "hello"}]}
		],
		"systemInstruction": {"parts": [{"text": "Be terse."}]}
	}`)

	req, err := ParseGemini(geminiRaw)
	if err != nil {
		t.Fatalf("ParseGemini failed: %v", err)
	}
	if req.System == nil || len(req.System.Parts) == 0 {
		t.Fatalf("expected Parts-only system from Gemini, got: %+v", req.System)
	}

	openaiOut, err := SerializeOpenAI(req)
	if err != nil {
		t.Fatalf("SerializeOpenAI failed: %v", err)
	}
	var openaiWire struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(openaiOut, &openaiWire); err != nil {
		t.Fatalf("unmarshal openai: %v", err)
	}
	if len(openaiWire.Messages) == 0 || openaiWire.Messages[0].Role != "system" || openaiWire.Messages[0].Content != "Be terse." {
		t.Fatalf("Gemini systemInstruction lost on OpenAI serialize: %+v", openaiWire.Messages)
	}
}
