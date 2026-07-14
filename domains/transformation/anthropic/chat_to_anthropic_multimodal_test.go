package anthropic

import (
	"encoding/json"
	"testing"
)

func TestChatRequestToAnthropic_DataURIUsesBase64Source(t *testing.T) {
	in := []byte(`{"model":"claude","messages":[{"role":"user","content":[{"type":"text","text":"what is this?"},{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,Zm9v"}}]}]}`)
	out, err := ConvertChatRequestToAnthropic(in)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(out, &body); err != nil {
		t.Fatal(err)
	}
	parts := body["messages"].([]any)[0].(map[string]any)["content"].([]any)
	image := parts[1].(map[string]any)
	source := image["source"].(map[string]any)
	if source["type"] != "base64" || source["media_type"] != "image/jpeg" || source["data"] != "Zm9v" {
		t.Errorf("data URI was not converted to Anthropic base64 source: %v", source)
	}
}
