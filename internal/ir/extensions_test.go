package ir
import ("encoding/json"; "testing")
func TestParseOpenAI_Extensions(t *testing.T) {
	ir, _ := ParseOpenAI([]byte(`{"model":"deepseek-chat","messages":[{"role":"user","content":"test"}],"reasoning_effort":"high"}`))
	if len(ir.Extensions) == 0 { t.Error("Extensions empty") }
}
func TestSerializeOpenAI_Extensions(t *testing.T) {
	ir := &InternalRequest{Model: "test", Messages: []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}}}, Extensions: map[string]json.RawMessage{"reasoning_effort": json.RawMessage(`"high"`)}}
	body, _ := SerializeOpenAI(ir)
	var out map[string]any
	json.Unmarshal(body, &out)
	if out["reasoning_effort"] != "high" { t.Error("Extensions not restored") }
}
func TestImageDetail(t *testing.T) {
	ir, _ := ParseOpenAI([]byte(`{"model":"gpt-4-vision","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"http://a.com/i.jpg","detail":"high"}}]}]}`))
	if ir.Messages[0].Content[0].Image.Detail != "high" { t.Error("detail not parsed") }
	body, _ := SerializeOpenAI(ir)
	var out map[string]any
	json.Unmarshal(body, &out)
	content := out["messages"].([]any)[0].(map[string]any)["content"].([]any)
	imageURL := content[0].(map[string]any)["image_url"].(map[string]any)
	if imageURL["detail"] != "high" { t.Error("detail not serialized") }
}
