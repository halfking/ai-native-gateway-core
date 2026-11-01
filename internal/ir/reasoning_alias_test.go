package ir

import (
	"strings"
	"testing"
)

// ollamaNonStreamReasoningBody is a verbatim capture of Ollama 0.40.11's
// OpenAI-compat `/v1/chat/completions` response for a thinking model
// (local, model qwen3.5:4b, 2026-11-02). Note the message object carries
// exactly {role, content, reasoning} — `reasoning_content` is ABSENT.
const ollamaNonStreamReasoningBody = `{
  "id": "chatcmpl-808",
  "object": "chat.completion",
  "created": 1793553221,
  "model": "qwen3.5:4b",
  "system_fingerprint": "fp_ollama",
  "choices": [
    {
      "index": 0,
      "message": {
        "role": "assistant",
        "content": "",
        "reasoning": "Thinking Process:\n\n1.  **Analyze the Request:**\n    *   Task: explain."
      },
      "finish_reason": "length"
    }
  ],
  "usage": {
    "prompt_tokens": 17,
    "completion_tokens": 128,
    "total_tokens": 145
  }
}`

// TestParseOpenAIResponseOllamaReasoningAlias pins the Ollama /v1 dialect:
// the thinking trace lives under `message.reasoning`, not `message.reasoning_content`.
func TestParseOpenAIResponseOllamaReasoningAlias(t *testing.T) {
	irResp, err := ParseOpenAIResponse([]byte(ollamaNonStreamReasoningBody))
	if err != nil {
		t.Fatalf("ParseOpenAIResponse: %v", err)
	}
	if !strings.Contains(irResp.ReasoningContent, "Thinking Process") {
		t.Fatalf("ReasoningContent = %q, want it to carry the Ollama `reasoning` trace", irResp.ReasoningContent)
	}
	if irResp.ReasoningContent != "Thinking Process:\n\n1.  **Analyze the Request:**\n    *   Task: explain." {
		t.Fatalf("ReasoningContent = %q, want the verbatim trace (no mutation)", irResp.ReasoningContent)
	}
}

// TestParseOpenAIResponseReasoningContentPrecedence pins that the standard
// OpenAI field wins when a provider sends BOTH spellings.
func TestParseOpenAIResponseReasoningContentPrecedence(t *testing.T) {
	body := `{"choices":[{"message":{"role":"assistant","content":"hi",
	  "reasoning_content":"standard","reasoning":"alias"},"finish_reason":"stop"}]}`
	irResp, err := ParseOpenAIResponse([]byte(body))
	if err != nil {
		t.Fatalf("ParseOpenAIResponse: %v", err)
	}
	if irResp.ReasoningContent != "standard" {
		t.Fatalf("ReasoningContent = %q, want %q (standard field must win)", irResp.ReasoningContent, "standard")
	}
}

// TestParseOpenAIResponseObjectValuedReasoning pins that an object-valued
// `reasoning` (the Responses-API config echo `{"effort":"high"}`) neither
// breaks the parse of the whole envelope nor becomes a trace.
func TestParseOpenAIResponseObjectValuedReasoning(t *testing.T) {
	body := `{"choices":[{"message":{"role":"assistant","content":"ok",
	  "reasoning":{"effort":"high"}},"finish_reason":"stop"}]}`
	irResp, err := ParseOpenAIResponse([]byte(body))
	if err != nil {
		t.Fatalf("ParseOpenAIResponse must not fail on an object-valued `reasoning`: %v", err)
	}
	if irResp.ReasoningContent != "" {
		t.Fatalf("ReasoningContent = %q, want empty (an object is a config, not a trace)", irResp.ReasoningContent)
	}
	if len(irResp.Content) == 0 {
		t.Fatal("Content lost — the envelope must still parse")
	}
}

// TestParseOpenAIStreamChunkOllamaReasoningAlias pins the streaming half of
// the Ollama /v1 dialect: the trace arrives as `delta.reasoning`.
func TestParseOpenAIStreamChunkOllamaReasoningAlias(t *testing.T) {
	payload := `{"id":"chatcmpl-214","object":"chat.completion.chunk","created":1793553297,
	  "model":"qwen3.5:4b","choices":[{"index":0,
	  "delta":{"role":"assistant","content":"","reasoning":"Thinking"},"finish_reason":null}]}`
	chunk, err := ParseOpenAIStreamChunk("data: " + payload + "\n\n")
	if err != nil {
		t.Fatalf("ParseOpenAIStreamChunk: %v", err)
	}
	if chunk == nil || chunk.Delta == nil {
		t.Fatal("chunk or chunk.Delta is nil")
	}
	if chunk.Delta.ReasoningContent != "Thinking" {
		t.Fatalf("Delta.ReasoningContent = %q, want %q", chunk.Delta.ReasoningContent, "Thinking")
	}
	if chunk.Delta.DeltaType != "reasoning" {
		t.Fatalf("Delta.DeltaType = %q, want %q", chunk.Delta.DeltaType, "reasoning")
	}
}

// TestParseOpenAIStreamChunkReasoningContentPrecedence pins that the
// standard OpenAI spelling wins when both are present in one delta.
func TestParseOpenAIStreamChunkReasoningContentPrecedence(t *testing.T) {
	payload := `{"choices":[{"index":0,
	  "delta":{"reasoning_content":"standard","reasoning":"alias"},"finish_reason":null}]}`
	chunk, err := ParseOpenAIStreamChunk("data: " + payload + "\n\n")
	if err != nil {
		t.Fatalf("ParseOpenAIStreamChunk: %v", err)
	}
	if chunk.Delta.ReasoningContent != "standard" {
		t.Fatalf("Delta.ReasoningContent = %q, want %q", chunk.Delta.ReasoningContent, "standard")
	}
}

// TestParseOpenAIStreamChunkObjectValuedReasoning pins that an object-valued
// `reasoning` in a delta cannot break the chunk parse.
func TestParseOpenAIStreamChunkObjectValuedReasoning(t *testing.T) {
	payload := `{"choices":[{"index":0,
	  "delta":{"content":"hi","reasoning":{"effort":"high"}},"finish_reason":null}]}`
	chunk, err := ParseOpenAIStreamChunk("data: " + payload + "\n\n")
	if err != nil {
		t.Fatalf("ParseOpenAIStreamChunk must not fail on an object-valued `reasoning`: %v", err)
	}
	if chunk.Delta.ReasoningContent != "" {
		t.Fatalf("Delta.ReasoningContent = %q, want empty", chunk.Delta.ReasoningContent)
	}
	if chunk.Delta.Content != "hi" {
		t.Fatalf("Delta.Content = %q, want %q", chunk.Delta.Content, "hi")
	}
}

// TestReasoningAliasDoesNotChangeEmptySemantics lives in
// internal/emptyoutcome (which imports ir, so ir cannot import it back) —
// see emptyoutcome/reasoning_alias_empty_semantics_test.go.
