package emptyoutcome

import (
	"testing"
)

// ollamaReasoningOnlyBody is a verbatim capture of Ollama 0.40.11's
// OpenAI-compat `/v1/chat/completions` response for a thinking model
// (local, model qwen3.5:4b, 2026-11-02): the whole output budget went to
// the thinking channel, so `content` is empty while the trace sits under
// `message.reasoning` (Ollama's spelling — NOT OpenAI's `reasoning_content`).
const ollamaReasoningOnlyBody = `{
  "id": "chatcmpl-808",
  "object": "chat.completion",
  "model": "qwen3.5:4b",
  "choices": [
    {
      "index": 0,
      "message": {
        "role": "assistant",
        "content": "",
        "reasoning": "Thinking Process:\n\n1.  **Analyze the Request:**"
      },
      "finish_reason": "length"
    }
  ],
  "usage": {"prompt_tokens": 17, "completion_tokens": 128, "total_tokens": 145}
}`

// TestOllamaReasoningOnlyBodyStaysEmpty pins the 2026-10-17 owner decision
// against the Ollama /v1 dialect: a reasoning trace is progress, NOT an
// assistant answer.
//
// The IR layer now restores the relay of Ollama's `message.reasoning`
// (see internal/ir/reasoning_alias.go). That relay fix must NOT change the
// empty/non-empty verdict — a reasoning-only body is still an empty outcome,
// so the executor still fails over instead of laundering an empty answer to
// the client as a 200.
func TestOllamaReasoningOnlyBodyStaysEmpty(t *testing.T) {
	if !ChatBodyIsEmpty([]byte(ollamaReasoningOnlyBody)) {
		t.Fatal("ChatBodyIsEmpty = false, want true — a reasoning-only body is still empty")
	}
}

// TestOllamaReasoningOnlyBodyAttribution pins that the *attribution* still
// works on the Ollama dialect: finish_reason == "length" with
// completion_tokens > 0 ⇒ the output budget was exhausted by reasoning.
// Criterion ① carries this even though Ollama omits
// `completion_tokens_details.reasoning_tokens`.
func TestOllamaReasoningOnlyBodyAttribution(t *testing.T) {
	reason, usage := InspectEmptyBody([]byte(ollamaReasoningOnlyBody))
	if reason != EmptyReasonBudgetExhausted {
		t.Fatalf("EmptyReason = %q, want %q", reason, EmptyReasonBudgetExhausted)
	}
	if usage.FinishReason != "length" {
		t.Fatalf("FinishReason = %q, want %q", usage.FinishReason, "length")
	}
	if usage.CompletionTokens != 128 {
		t.Fatalf("CompletionTokens = %d, want 128", usage.CompletionTokens)
	}
}

// TestOllamaReasoningOnlyStreamChunkStaysEmpty pins the same rule on the
// streaming half of the Ollama /v1 dialect: a delta carrying only
// `reasoning` is not assistant output.
func TestOllamaReasoningOnlyStreamChunkStaysEmpty(t *testing.T) {
	payload := `{"id":"chatcmpl-214","object":"chat.completion.chunk","model":"qwen3.5:4b",
	  "choices":[{"index":0,"delta":{"role":"assistant","content":"","reasoning":"Thinking"},"finish_reason":null}]}`
	if ChatChunkHasOutput(payload) {
		t.Fatal("ChatChunkHasOutput = true, want false — a reasoning-only delta is not an answer")
	}
}
