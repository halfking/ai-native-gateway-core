package relay

import (
	"encoding/json"
	"testing"
)

func TestAnthropicRequestToChat_SimpleMessage(t *testing.T) {
	in := []byte(`{
        "model":"gpt-4",
        "max_tokens":100,
        "messages":[
            {"role":"user","content":"hello"}
        ]
    }`)
	out, err := ConvertAnthropicRequestToChat(in)
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	json.Unmarshal(out, &v)

	if v["model"] != "gpt-4" {
		t.Errorf("model = %v, want 'gpt-4'", v["model"])
	}
	if int(v["max_tokens"].(float64)) != 100 {
		t.Errorf("max_tokens = %v, want 100", v["max_tokens"])
	}

	msgs := v["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("messages len = %d, want 1", len(msgs))
	}

	msg := msgs[0].(map[string]any)
	if msg["role"] != "user" {
		t.Errorf("role = %v, want 'user'", msg["role"])
	}
	if msg["content"] != "hello" {
		t.Errorf("content = %v, want 'hello'", msg["content"])
	}
}

func TestAnthropicRequestToChat_SystemMessage(t *testing.T) {
	in := []byte(`{
        "model":"gpt-4",
        "max_tokens":50,
        "system":"You are a helpful assistant",
        "messages":[
            {"role":"user","content":"hi"}
        ]
    }`)
	out, err := ConvertAnthropicRequestToChat(in)
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	json.Unmarshal(out, &v)

	msgs := v["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("messages len = %d, want 2 (system + user)", len(msgs))
	}

	sysMsg := msgs[0].(map[string]any)
	if sysMsg["role"] != "system" {
		t.Errorf("first message role = %v, want 'system'", sysMsg["role"])
	}
	if sysMsg["content"] != "You are a helpful assistant" {
		t.Errorf("system content = %v", sysMsg["content"])
	}

	userMsg := msgs[1].(map[string]any)
	if userMsg["role"] != "user" {
		t.Errorf("second message role = %v, want 'user'", userMsg["role"])
	}
}

func TestAnthropicRequestToChat_ContentBlocks(t *testing.T) {
	in := []byte(`{
        "model":"gpt-4",
        "max_tokens":200,
        "messages":[
            {
                "role":"user",
                "content":[
                    {"type":"text","text":"first part"},
                    {"type":"text","text":"second part"}
                ]
            }
        ]
    }`)
	out, err := ConvertAnthropicRequestToChat(in)
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	json.Unmarshal(out, &v)

	msgs := v["messages"].([]any)
	msg := msgs[0].(map[string]any)
	
	// Multiple text blocks should be joined with newline
	want := "first part\nsecond part"
	if msg["content"] != want {
		t.Errorf("content = %q, want %q", msg["content"], want)
	}
}

func TestAnthropicRequestToChat_ToolUse(t *testing.T) {
	in := []byte(`{
        "model":"gpt-4",
        "max_tokens":150,
        "messages":[
            {
                "role":"assistant",
                "content":[
                    {"type":"text","text":"Let me check"},
                    {
                        "type":"tool_use",
                        "id":"call_123",
                        "name":"get_weather",
                        "input":{"city":"Paris"}
                    }
                ]
            }
        ]
    }`)
	out, err := ConvertAnthropicRequestToChat(in)
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	json.Unmarshal(out, &v)

	msgs := v["messages"].([]any)
	msg := msgs[0].(map[string]any)

	if msg["content"] != "Let me check" {
		t.Errorf("content = %v", msg["content"])
	}

	toolCalls, ok := msg["tool_calls"].([]any)
	if !ok || len(toolCalls) != 1 {
		t.Fatalf("tool_calls = %v, want 1 call", msg["tool_calls"])
	}

	tc := toolCalls[0].(map[string]any)
	if tc["id"] != "call_123" {
		t.Errorf("tool_call id = %v", tc["id"])
	}
	if tc["type"] != "function" {
		t.Errorf("tool_call type = %v", tc["type"])
	}

	fn := tc["function"].(map[string]any)
	if fn["name"] != "get_weather" {
		t.Errorf("function name = %v", fn["name"])
	}
}

func TestAnthropicRequestToChat_ToolResult(t *testing.T) {
	in := []byte(`{
        "model":"gpt-4",
        "max_tokens":100,
        "messages":[
            {
                "role":"user",
                "content":[
                    {
                        "type":"tool_result",
                        "tool_use_id":"call_123",
                        "content":"sunny, 22°C"
                    }
                ]
            }
        ]
    }`)
	out, err := ConvertAnthropicRequestToChat(in)
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	json.Unmarshal(out, &v)

	msgs := v["messages"].([]any)
	msg := msgs[0].(map[string]any)

	// tool_result should become tool role message
	if msg["role"] != "tool" {
		t.Errorf("role = %v, want 'tool'", msg["role"])
	}
	if msg["tool_call_id"] != "call_123" {
		t.Errorf("tool_call_id = %v", msg["tool_call_id"])
	}
	if msg["content"] != "sunny, 22°C" {
		t.Errorf("content = %v", msg["content"])
	}
}

func TestAnthropicRequestToChat_Tools(t *testing.T) {
	in := []byte(`{
        "model":"gpt-4",
        "max_tokens":100,
        "messages":[{"role":"user","content":"hi"}],
        "tools":[
            {
                "name":"search",
                "description":"Search the web",
                "input_schema":{
                    "type":"object",
                    "properties":{"query":{"type":"string"}},
                    "required":["query"]
                }
            }
        ]
    }`)
	out, err := ConvertAnthropicRequestToChat(in)
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	json.Unmarshal(out, &v)

	tools, ok := v["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools = %v, want 1 tool", v["tools"])
	}

	tool := tools[0].(map[string]any)
	if tool["type"] != "function" {
		t.Errorf("tool type = %v", tool["type"])
	}

	fn := tool["function"].(map[string]any)
	if fn["name"] != "search" {
		t.Errorf("function name = %v", fn["name"])
	}
	if fn["description"] != "Search the web" {
		t.Errorf("function description = %v", fn["description"])
	}

	// input_schema should become parameters
	params := fn["parameters"].(map[string]any)
	if params["type"] != "object" {
		t.Errorf("parameters type = %v", params["type"])
	}
}

func TestAnthropicRequestToChat_ToolChoice(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want any
	}{
		{"auto", `{"type":"auto"}`, "auto"},
		{"none", `{"type":"none"}`, "none"},
		{"any", `{"type":"any"}`, "required"},
		{"specific tool", `{"type":"tool","name":"search"}`, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name": "search",
			},
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := []byte(`{
                "model":"gpt-4",
                "max_tokens":50,
                "messages":[{"role":"user","content":"hi"}],
                "tool_choice":` + tc.in + `
            }`)
			out, err := ConvertAnthropicRequestToChat(in)
			if err != nil {
				t.Fatal(err)
			}
			var v map[string]any
			json.Unmarshal(out, &v)

			switch want := tc.want.(type) {
			case string:
				if v["tool_choice"] != want {
					t.Errorf("tool_choice = %v, want %v", v["tool_choice"], want)
				}
			case map[string]any:
				got := v["tool_choice"].(map[string]any)
				if got["type"] != want["type"] {
					t.Errorf("tool_choice.type = %v, want %v", got["type"], want["type"])
				}
			}
		})
	}
}

func TestAnthropicRequestToChat_MetadataUserID(t *testing.T) {
	in := []byte(`{
        "model":"gpt-4",
        "max_tokens":50,
        "messages":[{"role":"user","content":"hi"}],
        "metadata":{"user_id":"user-789"}
    }`)
	out, err := ConvertAnthropicRequestToChat(in)
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	json.Unmarshal(out, &v)

	if v["user"] != "user-789" {
		t.Errorf("user = %v, want 'user-789'", v["user"])
	}

	// metadata should not be in output
	if _, ok := v["metadata"]; ok {
		t.Error("metadata should not exist in OpenAI request")
	}
}

func TestAnthropicRequestToChat_StopSequences(t *testing.T) {
	in := []byte(`{
        "model":"gpt-4",
        "max_tokens":50,
        "messages":[{"role":"user","content":"hi"}],
        "stop_sequences":["END","STOP"]
    }`)
	out, err := ConvertAnthropicRequestToChat(in)
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	json.Unmarshal(out, &v)

	stop, ok := v["stop"].([]any)
	if !ok || len(stop) != 2 {
		t.Fatalf("stop = %v, want 2 sequences", v["stop"])
	}
	if stop[0] != "END" || stop[1] != "STOP" {
		t.Errorf("stop = %v, want [END, STOP]", stop)
	}
}

func TestAnthropicRequestToChat_TopKDropped(t *testing.T) {
	in := []byte(`{
        "model":"gpt-4",
        "max_tokens":50,
        "messages":[{"role":"user","content":"hi"}],
        "top_k":40
    }`)
	out, err := ConvertAnthropicRequestToChat(in)
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	json.Unmarshal(out, &v)

	// top_k should be silently dropped (OpenAI doesn't support it)
	if _, ok := v["top_k"]; ok {
		t.Error("top_k should not exist in OpenAI request")
	}
}
