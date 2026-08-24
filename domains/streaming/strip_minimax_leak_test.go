package streaming

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestStripMinimaxFieldsBody_RemovesLeakedFunctionCalls(t *testing.T) {
	body := []byte(`{"model":"glm-5.1","choices":[{"message":{"role":"assistant","content":"answer","reasoning_content":"plan\n<function_calls>\n...\n<]minimax[>\n<tool_call>"},"finish_reason":"stop"}]}`)

	got := StripMinimaxFieldsBody(body)
	if strings.Contains(string(got), "<function_calls>") || strings.Contains(string(got), "<]minimax[>") {
		t.Fatalf("MiniMax private function-call marker leaked: %s", got)
	}

	var response map[string]any
	if err := json.Unmarshal(got, &response); err != nil {
		t.Fatalf("cleaned response is invalid JSON: %v", err)
	}
	message := response["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)
	if got := message["reasoning_content"]; got != "plan" {
		t.Fatalf("reasoning_content = %q, want preserved prefix", got)
	}
	if got := message["content"]; got != "answer" {
		t.Fatalf("content = %q, want answer", got)
	}
}

func TestStripMinimaxFieldsBody_CleansStreamingDelta(t *testing.T) {
	body := []byte(`{"choices":[{"delta":{"content":"visible","reasoning_content":"<function_calls>internal"}}]}`)

	got := StripMinimaxFieldsBody(body)
	var response map[string]any
	if err := json.Unmarshal(got, &response); err != nil {
		t.Fatalf("cleaned stream chunk is invalid JSON: %v", err)
	}
	delta := response["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)
	if got := delta["reasoning_content"]; got != "" {
		t.Fatalf("reasoning_content = %q, want empty after private marker", got)
	}
	if got := delta["content"]; got != "visible" {
		t.Fatalf("content = %q, want visible", got)
	}
}

func TestStripMinimaxFieldsBody_PreservesTextAfterClosedMarker(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"content":"before <function_calls>private</tool_call> after"}}]}`)

	got := StripMinimaxFieldsBody(body)
	var response map[string]any
	if err := json.Unmarshal(got, &response); err != nil {
		t.Fatalf("cleaned response is invalid JSON: %v", err)
	}
	content := response["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["content"]
	if content != "before after" {
		t.Fatalf("content = %q, want text outside private marker", content)
	}
}

func TestStripMinimaxFieldsBody_PreservesOrdinaryXMLText(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"content":"literal <function_call> example"}}]}`)

	got := StripMinimaxFieldsBody(body)
	if string(got) != string(body) {
		t.Fatalf("ordinary XML text changed: got %s, want %s", got, body)
	}
}
