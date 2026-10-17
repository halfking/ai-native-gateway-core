package streaming

import (
	"encoding/json"
	"strings"
	"testing"
)

// Production shape captured on 154 (minimax-m3 via new-provider-2): empty
// tool_calls slot at index 0, then a real Bash/read call. Magpie/ZCode
// Zod-fails on the empty name and reports empty_model_response.
func TestStripPhantomToolCallsKeepsNamedDropsEmpty(t *testing.T) {
	payload := `{"id":"chatcmpl-x","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"","tool_calls":[{"index":0,"id":"","type":"function","function":{"name":"","arguments":""}},{"index":1,"id":"call_bash_1","type":"function","function":{"name":"Bash","arguments":"{\"command\":\"ls\"}"}}]},"finish_reason":null}]}`
	out := stripPhantomToolCallsInPayload(payload)
	if strings.Contains(out, `"name":""`) {
		t.Fatalf("phantom empty name still present: %s", out)
	}
	if !strings.Contains(out, `"name":"Bash"`) {
		t.Fatalf("named Bash call was stripped: %s", out)
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &root); err != nil {
		t.Fatal(err)
	}
	var choices []map[string]json.RawMessage
	if err := json.Unmarshal(root["choices"], &choices); err != nil {
		t.Fatal(err)
	}
	var delta map[string]json.RawMessage
	if err := json.Unmarshal(choices[0]["delta"], &delta); err != nil {
		t.Fatal(err)
	}
	var tcs []map[string]any
	if err := json.Unmarshal(delta["tool_calls"], &tcs); err != nil {
		t.Fatal(err)
	}
	if len(tcs) != 1 {
		t.Fatalf("want 1 tool_call after strip, got %d: %s", len(tcs), out)
	}
}

func TestStripPhantomToolCallsAllPhantomRemovesKey(t *testing.T) {
	payload := `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"","function":{"name":"","arguments":""}}]}}]}`
	out := stripPhantomToolCallsInPayload(payload)
	if strings.Contains(out, "tool_calls") {
		t.Fatalf("all-phantom should drop tool_calls key: %s", out)
	}
}

func TestStripPhantomToolCallsSSELine(t *testing.T) {
	line := `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"","arguments":""}},{"index":1,"function":{"name":"read","arguments":"{}"}}]}}]}` + "\n"
	out := stripPhantomToolCallsFromSSELine(line)
	if strings.Contains(out, `"name":""`) {
		t.Fatalf("phantom remains: %s", out)
	}
	if !strings.Contains(out, `"name":"read"`) {
		t.Fatalf("named tool lost: %s", out)
	}
}

func TestStripPhantomKeepsArgumentsOnlyContinuation(t *testing.T) {
	payload := `{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"q\":1}"}}]}}]}`
	out := stripPhantomToolCallsInPayload(payload)
	if !strings.Contains(out, `"arguments":"{\"q\":1}"`) && !strings.Contains(out, `"arguments":"{\\\"q\\\":1}"`) {
		// After re-marshal, escaping may differ — just require arguments key with q
		if !strings.Contains(out, `"arguments"`) || !strings.Contains(out, `q`) {
			t.Fatalf("arguments-only continuation was stripped: %s", out)
		}
	}
}

func TestStripChunkFieldsForVendorStripsPhantomWithoutStripFn(t *testing.T) {
	line := `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"","arguments":""}},{"index":1,"function":{"name":"Bash","arguments":"{}"}}]}}]}`
	out, code, _ := stripChunkFieldsForVendor(line, "", nil)
	if code != 0 {
		t.Fatalf("unexpected error code %d", code)
	}
	if strings.Contains(out, `"name":""`) {
		t.Fatalf("phantom not stripped via stripChunkFieldsForVendor: %s", out)
	}
	if !strings.Contains(out, `"name":"Bash"`) {
		t.Fatalf("Bash lost: %s", out)
	}
}
