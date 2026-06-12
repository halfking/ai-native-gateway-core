package relay

import (
	"encoding/json"
	"testing"
)

func TestAnthropicToChat_ThinkingBlocksDropped(t *testing.T) {
	in := []byte(`{
        "id":"msg_1","type":"message","role":"assistant","model":"MiniMax-M2.7",
        "content":[
            {"type":"thinking","thinking":"deep thought","signature":"abc123"},
            {"type":"text","text":"hi there"}
        ],
        "usage":{"input_tokens":12,"output_tokens":5},
        "stop_reason":"end_turn"
    }`)
	out, err := ConvertAnthropicResponseToChat(in, "MiniMax-M2.7")
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	json.Unmarshal(out, &v)
	choice := v["choices"].([]any)[0].(map[string]any)
	msg := choice["message"].(map[string]any)
	if msg["content"] != "hi there" {
		t.Errorf("content = %v, want 'hi there'", msg["content"])
	}
}

func TestAnthropicToChat_ToolUseToToolCalls(t *testing.T) {
	in := []byte(`{
        "id":"msg_1","type":"message","role":"assistant","model":"x",
        "content":[{"type":"tool_use","id":"call_abc","name":"get_weather","input":{"city":"北京"}}],
        "usage":{"input_tokens":12,"output_tokens":5},
        "stop_reason":"tool_use"
    }`)
	out, _ := ConvertAnthropicResponseToChat(in, "x")
	var v map[string]any
	json.Unmarshal(out, &v)
	choice := v["choices"].([]any)[0].(map[string]any)
	msg := choice["message"].(map[string]any)
	tcs := msg["tool_calls"].([]any)
	if len(tcs) != 1 {
		t.Fatal("tool_calls lost")
	}
	tc := tcs[0].(map[string]any)
	if tc["id"] != "call_abc" {
		t.Error("id lost")
	}
	fn := tc["function"].(map[string]any)
	if fn["name"] != "get_weather" {
		t.Error("name lost")
	}
	if choice["finish_reason"] != "tool_calls" {
		t.Error("stop_reason not mapped")
	}
}

func TestAnthropicToChat_StopReasonMapping(t *testing.T) {
	cases := []struct{ from, want string }{
		{"end_turn", "stop"},
		{"tool_use", "tool_calls"},
		{"max_tokens", "length"},
		{"stop_sequence", "stop"},
	}
	for _, tc := range cases {
		in := []byte(`{"content":[{"type":"text","text":"x"}],"usage":{},"stop_reason":"` + tc.from + `"}`)
		out, _ := ConvertAnthropicResponseToChat(in, "x")
		var v map[string]any
		json.Unmarshal(out, &v)
		choice := v["choices"].([]any)[0].(map[string]any)
		if choice["finish_reason"] != tc.want {
			t.Errorf("%s -> %s, want %s", tc.from, choice["finish_reason"], tc.want)
		}
	}
}

func TestAnthropicToChat_UsageMapped(t *testing.T) {
	in := []byte(`{"content":[{"type":"text","text":"x"}],"usage":{"input_tokens":42,"output_tokens":17},"stop_reason":"end_turn"}`)
	out, _ := ConvertAnthropicResponseToChat(in, "x")
	var v map[string]any
	json.Unmarshal(out, &v)
	usage := v["usage"].(map[string]any)
	if int(usage["prompt_tokens"].(float64)) != 42 {
		t.Errorf("prompt_tokens = %v, want 42", usage["prompt_tokens"])
	}
	if int(usage["completion_tokens"].(float64)) != 17 {
		t.Errorf("completion_tokens = %v, want 17", usage["completion_tokens"])
	}
	if int(usage["total_tokens"].(float64)) != 59 {
		t.Errorf("total_tokens = %v, want 59", usage["total_tokens"])
	}
}
