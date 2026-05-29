package relay

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseXMLToolCalls(t *testing.T) {
	text := `<tool_call><function=read_file><parameter=file_path>README.md</parameter></function></tool_call>`
	remaining, calls := parseXMLToolCalls(text)

	if remaining != "" {
		t.Fatalf("expected empty remaining text, got %q", remaining)
	}
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	function := calls[0]["function"].(map[string]any)
	if function["name"] != "read_file" {
		t.Fatalf("expected read_file, got %#v", function["name"])
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(function["arguments"].(string)), &args); err != nil {
		t.Fatalf("invalid arguments json: %v", err)
	}
	if args["file_path"] != "README.md" {
		t.Fatalf("expected README.md, got %#v", args["file_path"])
	}
}

func TestCoerceXMLToolCallsInChatResponseRequiresTools(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"role":"assistant","content":"<tool_call><function=read_file><parameter=file_path>README.md</parameter></function></tool_call>"},"finish_reason":"stop"}]}`)

	unchanged := coerceXMLToolCallsInChatResponse(body, false)
	if string(unchanged) != string(body) {
		t.Fatal("response should not change when tools were not requested")
	}

	changed := coerceXMLToolCallsInChatResponse(body, true)
	var resp map[string]any
	if err := json.Unmarshal(changed, &resp); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	choice := resp["choices"].([]any)[0].(map[string]any)
	if choice["finish_reason"] != "tool_calls" {
		t.Fatalf("expected tool_calls finish, got %#v", choice["finish_reason"])
	}
	message := choice["message"].(map[string]any)
	if _, ok := message["tool_calls"].([]any); !ok {
		t.Fatalf("expected structured tool_calls, got %#v", message)
	}
}

func TestCoerceXMLToolCallsInStreamLine(t *testing.T) {
	line := `data: {"choices":[{"delta":{"content":"<tool_call><function=read_file><parameter=file_path>README.md</parameter></function></tool_call>"},"finish_reason":null}]}` + "\n\n"

	changed := coerceXMLToolCallsInStreamLine(line, true)
	if !strings.Contains(changed, `"tool_calls"`) || !strings.Contains(changed, `"finish_reason":"tool_calls"`) {
		t.Fatalf("expected stream line to contain structured tool_calls, got %s", changed)
	}
}

func TestRequestHasTools(t *testing.T) {
	if !requestHasTools([]byte(`{"tools":[{"type":"function","function":{"name":"read_file"}}]}`)) {
		t.Fatal("expected tools request")
	}
	if requestHasTools([]byte(`{"messages":[]}`)) {
		t.Fatal("expected no tools request")
	}
}
