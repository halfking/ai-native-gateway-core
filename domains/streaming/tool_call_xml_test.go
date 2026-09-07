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

// 2026-09-08 audit: Qwen-style vendors emit a bare JSON object inside
// <tool_call>. The loose parser must surface the REAL tool name/arguments —
// a fixed name="tool" makes downstream agents fail with "no such tool" and
// aborts their loop. Non-JSON payloads keep the historical {"input": ...}
// wrap with name="tool".
func TestParseLooseToolCalls_ExtractsJSONNameAndArgs(t *testing.T) {
	text := `<tool_call>{"name": "bash", "arguments": {"cmd": "ls -la"}}</tool_call>`
	_, calls := parseLooseToolCalls(text)
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1: %#v", len(calls), calls)
	}
	fn := calls[0]["function"].(map[string]any)
	if fn["name"] != "bash" {
		t.Fatalf("name = %q, want bash", fn["name"])
	}
	if fn["arguments"].(string) != `{"cmd":"ls -la"}` {
		t.Fatalf("arguments = %v, want {\"cmd\":\"ls -la\"}", fn["arguments"])
	}
	// id must stay in the [A-Za-z0-9] alphabet even past index 25
	for idx, c := range calls {
		id := c["id"].(string)
		for _, ch := range id {
			if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '_') {
				t.Fatalf("call %d id %q contains illegal char %q", idx, id, string(ch))
			}
		}
	}
}

func TestParseLooseToolCalls_PlainTextKeepsInputWrap(t *testing.T) {
	text := `<tool_call> ssh -o ConnectTimeout=10 ls </tool_call>`
	_, calls := parseLooseToolCalls(text)
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(calls))
	}
	fn := calls[0]["function"].(map[string]any)
	if fn["name"] != "tool" {
		t.Fatalf("name = %q, want tool (fallback)", fn["name"])
	}
	if !strings.Contains(fn["arguments"].(string), "ssh") {
		t.Fatalf("arguments missing ssh: %v", fn["arguments"])
	}
}
