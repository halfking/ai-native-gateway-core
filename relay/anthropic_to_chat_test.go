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
	// Enhanced (2026-06-20): thinking blocks should now be preserved in reasoning_content
	if reasoning, ok := msg["reasoning_content"].(string); !ok || reasoning != "deep thought" {
		t.Errorf("reasoning_content = %v, want 'deep thought'", msg["reasoning_content"])
	}
	// Check metadata
	meta := v["_kxg_meta"].(map[string]any)
	if meta["has_thinking"] != true {
		t.Errorf("_kxg_meta.has_thinking = %v, want true", meta["has_thinking"])
	}
	if int(meta["thinking_blocks_count"].(float64)) != 1 {
		t.Errorf("_kxg_meta.thinking_blocks_count = %v, want 1", meta["thinking_blocks_count"])
	}
}

func TestAnthropicToChat_MultipleThinkingBlocks(t *testing.T) {
	in := []byte(`{
        "id":"msg_2","type":"message","role":"assistant","model":"claude-opus-4",
        "content":[
            {"type":"thinking","thinking":"first thought"},
            {"type":"thinking","thinking":"second thought"},
            {"type":"text","text":"final answer"}
        ],
        "usage":{"input_tokens":20,"output_tokens":10},
        "stop_reason":"end_turn"
    }`)
	out, err := ConvertAnthropicResponseToChat(in, "claude-opus-4")
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	json.Unmarshal(out, &v)
	choice := v["choices"].([]any)[0].(map[string]any)
	msg := choice["message"].(map[string]any)
	
	// Content should only have text blocks
	if msg["content"] != "final answer" {
		t.Errorf("content = %v, want 'final answer'", msg["content"])
	}
	
	// Reasoning content should have both thinking blocks joined
	reasoning, ok := msg["reasoning_content"].(string)
	if !ok {
		t.Fatal("reasoning_content should exist")
	}
	want := "first thought\nsecond thought"
	if reasoning != want {
		t.Errorf("reasoning_content = %q, want %q", reasoning, want)
	}
	
	// Check metadata
	meta := v["_kxg_meta"].(map[string]any)
	if int(meta["thinking_blocks_count"].(float64)) != 2 {
		t.Errorf("_kxg_meta.thinking_blocks_count = %v, want 2", meta["thinking_blocks_count"])
	}
	if int(meta["reasoning_content_chars"].(float64)) != len(want) {
		t.Errorf("_kxg_meta.reasoning_content_chars = %v, want %d", meta["reasoning_content_chars"], len(want))
	}
}

func TestAnthropicToChat_NoThinkingBlocks(t *testing.T) {
	in := []byte(`{
        "id":"msg_3","type":"message","role":"assistant","model":"test",
        "content":[
            {"type":"text","text":"just text"}
        ],
        "usage":{"input_tokens":5,"output_tokens":3},
        "stop_reason":"end_turn"
    }`)
	out, err := ConvertAnthropicResponseToChat(in, "test")
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	json.Unmarshal(out, &v)
	choice := v["choices"].([]any)[0].(map[string]any)
	msg := choice["message"].(map[string]any)
	
	// Should not have reasoning_content
	if _, ok := msg["reasoning_content"]; ok {
		t.Errorf("reasoning_content should not exist when no thinking blocks")
	}
	
	// Should not have _kxg_meta when no thinking
	if _, ok := v["_kxg_meta"]; ok {
		t.Errorf("_kxg_meta should not exist when no thinking blocks")
	}
}

func TestAnthropicToChat_ThinkingWithToolCalls(t *testing.T) {
	in := []byte(`{
        "id":"msg_4","type":"message","role":"assistant","model":"claude",
        "content":[
            {"type":"thinking","thinking":"reasoning about tool usage"},
            {"type":"tool_use","id":"call_1","name":"calculator","input":{"expr":"2+2"}}
        ],
        "usage":{"input_tokens":30,"output_tokens":15},
        "stop_reason":"tool_use"
    }`)
	out, err := ConvertAnthropicResponseToChat(in, "claude")
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	json.Unmarshal(out, &v)
	choice := v["choices"].([]any)[0].(map[string]any)
	msg := choice["message"].(map[string]any)
	
	// Should have reasoning_content
	if reasoning, ok := msg["reasoning_content"].(string); !ok || reasoning != "reasoning about tool usage" {
		t.Errorf("reasoning_content = %v, want 'reasoning about tool usage'", msg["reasoning_content"])
	}
	
	// Should have tool_calls
	toolCalls, ok := msg["tool_calls"].([]any)
	if !ok || len(toolCalls) != 1 {
		t.Errorf("tool_calls = %v, want 1 tool call", msg["tool_calls"])
	}
	
	// Finish reason should be tool_calls
	if choice["finish_reason"] != "tool_calls" {
		t.Errorf("finish_reason = %v, want 'tool_calls'", choice["finish_reason"])
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
