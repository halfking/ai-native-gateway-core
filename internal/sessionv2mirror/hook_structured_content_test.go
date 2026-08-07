package sessionv2mirror

import (
	"encoding/json"
	"testing"
)

func TestParseMessagesJSON_PreservesStructuredContent(t *testing.T) {
	raw := json.RawMessage(`{"messages":[
		{"role":"user","content":[{"type":"input_text","text":"describe this"},{"type":"input_image","image_url":"https://example.test/image.png"}]},
		{"role":"assistant","content":[{"type":"thinking","thinking":"internal"},{"type":"tool_use","id":"tool_1","name":"lookup","input":{"q":"x"}}]}
	]}`)

	messages := parseMessagesJSON(raw)
	if len(messages) != 2 {
		t.Fatalf("message count = %d, want 2", len(messages))
	}
	for i, message := range messages {
		if len(message.ContentRaw) == 0 {
			t.Fatalf("messages[%d] lost structured content", i)
		}
		encoded, err := json.Marshal(message)
		if err != nil {
			t.Fatalf("marshal messages[%d]: %v", i, err)
		}
		var probe struct {
			Content json.RawMessage `json:"content"`
		}
		if err := json.Unmarshal(encoded, &probe); err != nil {
			t.Fatalf("unmarshal messages[%d]: %v", i, err)
		}
		if string(probe.Content) != string(message.ContentRaw) {
			t.Fatalf("messages[%d] content changed: got %s want %s", i, probe.Content, message.ContentRaw)
		}
	}
}

func TestParseMessagesJSON_PreservesNullContent(t *testing.T) {
	messages := parseMessagesJSON(json.RawMessage(`[{"role":"assistant","content":null,"tool_calls":[{"id":"call_1"}]}]`))
	if len(messages) != 1 {
		t.Fatalf("message count = %d, want 1", len(messages))
	}
	if string(messages[0].ContentRaw) != "null" {
		t.Fatalf("null content = %s, want null", messages[0].ContentRaw)
	}
}
