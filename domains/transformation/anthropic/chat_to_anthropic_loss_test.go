package anthropic

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestChatRequestToAnthropic_PreservesSystemTextContentBlocks(t *testing.T) {
	request := []byte(`{
		"model":"claude-sonnet-5",
		"messages":[
			{
				"role":"system",
				"content":[
					{"type":"text","text":"Follow the policy."},
					{"type":"text","text":"Answer in Chinese."}
				]
			},
			{"role":"user","content":"continue"}
		]
	}`)

	out, err := ConvertChatRequestToAnthropic(request)
	if err != nil {
		t.Fatalf("ConvertChatRequestToAnthropic() error = %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(out, &body); err != nil {
		t.Fatalf("unmarshal converted body: %v", err)
	}
	if body["system"] != "Follow the policy.\nAnswer in Chinese." {
		t.Fatalf("system = %q, want both text blocks", body["system"])
	}
}

func TestChatRequestToAnthropic_RejectsUnsupportedContentBlocks(t *testing.T) {
	request := []byte(`{
		"model":"claude-sonnet-5",
		"messages":[{
			"role":"user",
			"content":[
				{"type":"text","text":"keep this prompt"},
				{"type":"refusal","refusal":"safety context must not disappear"}
			]
		}]
	}`)

	_, err := ConvertChatRequestToAnthropic(request)
	if err == nil {
		t.Fatal("ConvertChatRequestToAnthropic() error = nil, want unsupported content block error")
	}
	if !strings.Contains(err.Error(), "unsupported OpenAI content block") {
		t.Fatalf("ConvertChatRequestToAnthropic() error = %q, want unsupported block context", err)
	}
}

func TestChatRequestToAnthropic_RejectsInvalidToolArguments(t *testing.T) {
	request := []byte(`{
		"model":"claude-sonnet-5",
		"messages":[
			{"role":"user","content":"look this up"},
			{
				"role":"assistant",
				"content":null,
				"tool_calls":[{
					"id":"call_1",
					"type":"function",
					"function":{"name":"lookup","arguments":"{not-json"}
				}]
			}
		]
	}`)

	_, err := ConvertChatRequestToAnthropic(request)
	if err == nil {
		t.Fatal("ConvertChatRequestToAnthropic() error = nil, want invalid tool arguments error")
	}
	if !strings.Contains(err.Error(), "invalid tool arguments") {
		t.Fatalf("ConvertChatRequestToAnthropic() error = %q, want invalid arguments context", err)
	}
}

func TestChatRequestToAnthropic_RejectsImageBlockWithoutURL(t *testing.T) {
	request := []byte(`{
		"model":"claude-sonnet-5",
		"messages":[{
			"role":"user",
			"content":[{"type":"image_url","image_url":{}}]
		}]
	}`)

	_, err := ConvertChatRequestToAnthropic(request)
	if err == nil {
		t.Fatal("ConvertChatRequestToAnthropic() error = nil, want invalid image error")
	}
	if !strings.Contains(err.Error(), "invalid image_url content block") {
		t.Fatalf("ConvertChatRequestToAnthropic() error = %q, want invalid image context", err)
	}
}

func TestChatRequestToAnthropic_RejectsNonObjectToolArguments(t *testing.T) {
	request := []byte(`{"model":"claude-sonnet-5","messages":[{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"[]"}}]}]}`)

	_, err := ConvertChatRequestToAnthropic(request)
	if err == nil || !strings.Contains(err.Error(), "expected JSON object") {
		t.Fatalf("ConvertChatRequestToAnthropic() error = %v, want object validation error", err)
	}
}

func TestChatRequestToAnthropic_RejectsMalformedContentBlock(t *testing.T) {
	request := []byte(`{"model":"claude-sonnet-5","messages":[{"role":"user","content":["keep this prompt"]}]}`)

	_, err := ConvertChatRequestToAnthropic(request)
	if err == nil || !strings.Contains(err.Error(), "expected object") {
		t.Fatalf("ConvertChatRequestToAnthropic() error = %v, want object validation error", err)
	}
}
