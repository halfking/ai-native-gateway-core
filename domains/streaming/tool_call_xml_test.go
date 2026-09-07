package streaming

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseXMLToolCalls_UnwrapsMiniMaxTokenLeak(t *testing.T) {
	text := `minimax[>[<tool_call> ]<]minimax[>[]<]minimax[>[ssh -o ConnectTimeout=10 ls]<]minimax[>[]<]minimax[>[Check nginx]<]minimax[>[</tool_call>`
	remaining, calls := parseXMLToolCalls(text)
	if remaining != "" {
		t.Fatalf("remaining = %q, want empty", remaining)
	}
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1: %#v", len(calls), calls)
	}
	fn := calls[0]["function"].(map[string]any)
	if fn["name"] != "tool" {
		t.Fatalf("name = %q, want tool", fn["name"])
	}
	if !strings.Contains(fn["arguments"].(string), "ssh") {
		t.Fatalf("arguments missing ssh: %v", fn["arguments"])
	}
}

func TestCoerceXMLToolCallsInChatResponse_MiniMaxLeak(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"role":"assistant","content":"minimax[>[<tool_call> ls /etc]<]minimax[>[</tool_call>"}}]}`)
	got := CoerceXMLToolCallsInChatResponse(body, true)
	var resp map[string]any
	if err := json.Unmarshal(got, &resp); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	msg := resp["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)
	if msg["tool_calls"] == nil {
		t.Fatalf("tool_calls missing: %s", got)
	}
	if strings.Contains(string(got), "minimax[>[") {
		t.Fatalf("wrapper leaked: %s", got)
	}
}

func TestParseXMLToolCalls_PreservesNamedFunctionShape(t *testing.T) {
	text := `<tool_call><function=search><parameter=q>nginx</parameter></function></tool_call>`
	_, calls := parseXMLToolCalls(text)
	if len(calls) != 1 {
		t.Fatalf("calls = %d", len(calls))
	}
	fn := calls[0]["function"].(map[string]any)
	if fn["name"] != "search" {
		t.Fatalf("name = %q, want search", fn["name"])
	}
}
