package relay

import (
	"encoding/json"
	"testing"
)

func TestChatToAnthropic_SimpleMessage(t *testing.T) {
	in := []byte(`{
        "model":"MiniMax-M2.7",
        "max_tokens":256,
        "messages":[{"role":"user","content":"hi"}]
    }`)
	out, err := ConvertChatRequestToAnthropic(in)
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	json.Unmarshal(out, &v)
	if v["model"] != "MiniMax-M2.7" {
		t.Error("model not preserved")
	}
	if int(v["max_tokens"].(float64)) != 256 {
		t.Error("max_tokens not preserved")
	}
	msgs := v["messages"].([]any)
	if len(msgs) != 1 {
		t.Errorf("messages len = %d, want 1", len(msgs))
	}
	if msgs[0].(map[string]any)["role"] != "user" {
		t.Error("role lost")
	}
}

func TestChatToAnthropic_SystemMessageExtracted(t *testing.T) {
	in := []byte(`{
        "model":"MiniMax-M2.7","max_tokens":10,
        "messages":[
            {"role":"system","content":"you are a poet"},
            {"role":"user","content":"hi"}
        ]
    }`)
	out, _ := ConvertChatRequestToAnthropic(in)
	var v map[string]any
	json.Unmarshal(out, &v)
	if v["system"] != "you are a poet" {
		t.Errorf("system not extracted to top-level: %v", v)
	}
	msgs := v["messages"].([]any)
	if len(msgs) != 1 {
		t.Error("system should be removed from messages")
	}
	if msgs[0].(map[string]any)["role"] != "user" {
		t.Error("user role lost")
	}
}

func TestChatToAnthropic_ToolsConverted(t *testing.T) {
	in := []byte(`{
        "model":"MiniMax-M2.7","max_tokens":256,
        "tools":[{
            "type":"function",
            "function":{"name":"get_weather","description":"weather","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}
        }],
        "messages":[{"role":"user","content":"北京?"}]
    }`)
	out, _ := ConvertChatRequestToAnthropic(in)
	var v map[string]any
	json.Unmarshal(out, &v)
	tools := v["tools"].([]any)
	if len(tools) != 1 {
		t.Fatal("tools lost")
	}
	tool := tools[0].(map[string]any)
	if tool["name"] != "get_weather" {
		t.Error("name lost")
	}
	if tool["description"] != "weather" {
		t.Error("description lost")
	}
	if _, ok := tool["input_schema"]; !ok {
		t.Error("parameters should become input_schema")
	}
}

func TestChatToAnthropic_StopRenamedToStopSequences(t *testing.T) {
	in := []byte(`{"model":"x","max_tokens":10,"stop":["END"],"messages":[{"role":"user","content":"hi"}]}`)
	out, _ := ConvertChatRequestToAnthropic(in)
	var v map[string]any
	json.Unmarshal(out, &v)
	if v["stop_sequences"] == nil {
		t.Error("stop not renamed to stop_sequences")
	}
	if _, ok := v["stop"]; ok {
		t.Error("stop should be removed")
	}
}

func TestChatToAnthropic_ImageURLConvertedToImageBlock(t *testing.T) {
	in := []byte(`{"model":"x","max_tokens":10,
        "messages":[{"role":"user","content":[
            {"type":"text","text":"what is this?"},
            {"type":"image_url","image_url":{"url":"https://example.com/cat.png"}}
        ]}]
    }`)
	out, _ := ConvertChatRequestToAnthropic(in)
	var v map[string]any
	json.Unmarshal(out, &v)
	msgs := v["messages"].([]any)
	user := msgs[0].(map[string]any)
	content := user["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("content len = %d", len(content))
	}
	if content[1].(map[string]any)["type"] != "image" {
		t.Error("image_url not converted to image block")
	}
}

func TestChatToAnthropic_ToolCallIDToToolUseID(t *testing.T) {
	in := []byte(`{"model":"x","max_tokens":10,
        "messages":[
            {"role":"user","content":"北京?"},
            {"role":"assistant","content":null,"tool_calls":[{
                "id":"call_abc","type":"function",
                "function":{"name":"get_weather","arguments":"{\"city\":\"北京\"}"}
            }]},
            {"role":"tool","tool_call_id":"call_abc","content":"sunny"}
        ]
    }`)
	out, _ := ConvertChatRequestToAnthropic(in)
	var v map[string]any
	json.Unmarshal(out, &v)
	msgs := v["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("messages len = %d", len(msgs))
	}
	asst := msgs[1].(map[string]any)
	asstContent := asst["content"].([]any)
	var foundToolUse bool
	for _, blk := range asstContent {
		b := blk.(map[string]any)
		if b["type"] == "tool_use" && b["id"] == "call_abc" {
			foundToolUse = true
		}
	}
	if !foundToolUse {
		t.Error("tool_use block not created from tool_calls")
	}
	tool := msgs[2].(map[string]any)
	toolContent := tool["content"].([]any)
	tr := toolContent[0].(map[string]any)
	if tr["type"] != "tool_result" {
		t.Error("tool role not converted to tool_result")
	}
	if tr["tool_use_id"] != "call_abc" {
		t.Error("tool_use_id lost")
	}
}
