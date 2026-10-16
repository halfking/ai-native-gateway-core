package streaming

import (
	"github.com/kaixuan/llm-gateway-go/internal/tooloffer"
	"testing"
)

// Unnamed vendor prose is no longer executable; successful parsing needs a real offer.
func TestParseXMLToolCalls_UnwrapsMiniMaxTokenLeak(t *testing.T) {
	text := `minimax[>[<tool_call> ]<]minimax[>[]<]minimax[>[ssh ls]<]minimax[>[</tool_call>`
	remaining, calls := parseXMLToolCallsWithNames(text, offeredSearchNames())
	if remaining != text || len(calls) != 0 {
		t.Fatalf("unnamed vendor prose changed: %q %#v", remaining, calls)
	}
}

func TestCoerceXMLToolCallsInChatResponse_MiniMaxLeak(t *testing.T) {
	body := offeredResponse(t, `minimax[>[<tool_call> ls /etc]<]minimax[>[</tool_call>`)
	got := CoerceXMLToolCallsInChatResponseWithNames(body, offeredSearchNames())
	if string(got) != string(body) {
		t.Fatalf("unknown vendor text changed: %s", got)
	}
}

func TestParseXMLToolCalls_PreservesNamedFunctionShape(t *testing.T) {
	text := `<tool_call><function=search><parameter=q>nginx</parameter></function></tool_call>`
	_, calls := parseXMLToolCallsWithNames(text, offeredSearchNames())
	if len(calls) != 1 {
		t.Fatalf("calls = %d", len(calls))
	}
	fn := calls[0]["function"].(map[string]any)
	if fn["name"] != "search" {
		t.Fatalf("name = %q, want search", fn["name"])
	}
}

// Complete Hermes JSON retains its offered name and exact arguments.
func TestParseLooseToolCalls_ExtractsJSONNameAndArgs(t *testing.T) {
	text := `<tool_call>{"name": "bash", "arguments": {"cmd": "ls -la"}}</tool_call>`
	_, calls := parseXMLToolCallsWithNames(text, tooloffer.FromRequest([]byte(`{"tools":[{"type":"function","function":{"name":"bash"}}]}`)))
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
	remaining, calls := parseXMLToolCallsWithNames(text, offeredSearchNames())
	if remaining != text || len(calls) != 0 {
		t.Fatalf("plain prose became executable: %q %#v", remaining, calls)
	}
}
