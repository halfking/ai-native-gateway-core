package protocolformat

import (
	"strings"
	"testing"
)

func TestValidateAnthropicRequest(t *testing.T) {
	valid := []byte(`{"model":"claude-opus-5-5","max_tokens":64,"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
	if err := ValidateAnthropicRequest(valid); err != nil {
		t.Fatalf("valid body rejected: %v", err)
	}

	cases := []struct {
		name string
		body string
	}{
		{"not json", `this is not json`},
		{"missing model", `{"max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`},
		{"missing max_tokens", `{"model":"m","messages":[{"role":"user","content":"hi"}]}`},
		{"messages not array", `{"model":"m","max_tokens":10,"messages":{"a":1}}`},
		{"bad role", `{"model":"m","max_tokens":10,"messages":[{"role":"tool","content":"x"}]}`},
		{"foreign block type", `{"model":"m","max_tokens":10,"messages":[{"role":"user","content":[{"type":"reasoning","id":"rs_1"}]}]}`},
		{"raw block type", `{"model":"m","max_tokens":10,"messages":[{"role":"assistant","content":[{"type":"raw","raw_content":"x"}]}]}`},
		{"tool_result without id", `{"model":"m","max_tokens":10,"messages":[{"role":"user","content":[{"type":"tool_result","content":"x"}]}]}`},
		{"tool_result in assistant", `{"model":"m","max_tokens":10,"messages":[{"role":"assistant","content":[{"type":"tool_result","tool_use_id":"t1"}]}]}`},
		{"tool_use missing name", `{"model":"m","max_tokens":10,"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"t1","input":{}}]}]}`},
		{"trailing json", `{"model":"m","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}{}`},
	}
	for _, tc := range cases {
		if err := ValidateAnthropicRequest([]byte(tc.body)); err == nil {
			t.Fatalf("%s: accepted", tc.name)
		} else if strings.Contains(err.Error(), "internal") {
			t.Fatalf("%s: leaky error text: %v", tc.name, err)
		}
	}

	// Legal shapes that MUST keep passing (per official docs).
	legal := []string{
		`{"model":"m","max_tokens":10,"messages":[{"role":"user","content":"plain string"}]}`,
		`{"model":"m","max_tokens":10,"system":"sys","messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"...","signature":"sig"}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}]}`,
		`{"model":"m","max_tokens":10,"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"f","input":{}}]}]}`,
	}
	for _, b := range legal {
		if err := ValidateAnthropicRequest([]byte(b)); err != nil {
			t.Fatalf("legal body rejected: %s: %v", b, err)
		}
	}
}

func TestValidateOpenAIChatRequest(t *testing.T) {
	valid := []byte(`{"model":"gpt","messages":[{"role":"user","content":"hi"}]}`)
	if err := ValidateOpenAIChatRequest(valid); err != nil {
		t.Fatalf("valid body rejected: %v", err)
	}

	cases := []struct {
		name string
		body string
	}{
		{"not json", `nope`},
		{"missing messages", `{"model":"m"}`},
		{"bad role", `{"model":"m","messages":[{"role":"engine","content":"x"}]}`},
		{"tool without call id", `{"model":"m","messages":[{"role":"tool","content":"x"}]}`},
		{"tool_call missing name", `{"model":"m","messages":[{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"arguments":"{}"}}],"content":null}]}`},
		{"trailing json", `{"model":"m","messages":[{"role":"user","content":"hi"}]}[]`},
	}
	for _, tc := range cases {
		if err := ValidateOpenAIChatRequest([]byte(tc.body)); err == nil {
			t.Fatalf("%s: accepted", tc.name)
		}
	}

	legal := []string{
		`{"model":"m","messages":[{"role":"system","content":"s"},{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]},{"role":"tool","tool_call_id":"c1","content":"ok"}]}`,
		`{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"hi"},{"type":"image_url","image_url":{"url":"https://x.invalid/a.png"}}]}]}`,
	}
	for _, b := range legal {
		if err := ValidateOpenAIChatRequest([]byte(b)); err != nil {
			t.Fatalf("legal body rejected: %s: %v", b, err)
		}
	}
}
