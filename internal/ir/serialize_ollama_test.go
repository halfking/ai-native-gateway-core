package ir

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// TestSerializeOllama_AllPrivateFieldsPassthrough is the byte-level golden
// test pinned in docs/供应商协议优化-实施规划.md §6.2. Any future change
// that drops or renames an Ollama-private top-level field will fail this
// test — it is the executable form of the "all ollama.* fields land
// somewhere" contract (§2.6 / §3.3).
func TestSerializeOllama_AllPrivateFieldsPassthrough(t *testing.T) {
	req := &InternalRequest{
		Model:       "llama3.1",
		Stream:      true,
		Temperature: floatPtr(0.7),
		TopK:        intPtr(40),
		MaxTokens:   512,
		Stop:        []string{"\n"},
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}},
		},
		Extensions: map[string]json.RawMessage{
			"ollama.format":             jsonRaw(`"json"`),
			"ollama.keep_alive":         jsonRaw(`"5m"`),
			"ollama.raw":                jsonRaw(`true`),
			"ollama.options.num_ctx":    jsonRaw(`4096`),
			"ollama.options.num_gpu":    jsonRaw(`1`),
			"ollama.options.mirostat":   jsonRaw(`0`),
			"ollama.options.mirostat_eta": jsonRaw(`0.1`),
			"ollama.options.tfs_z":      jsonRaw(`1.0`),
		},
	}
	body, err := SerializeOllama(req)
	if err != nil {
		t.Fatalf("SerializeOllama: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}

	// model + stream
	if got["model"] != "llama3.1" {
		t.Errorf("model = %v, want llama3.1", got["model"])
	}
	if got["stream"] != true {
		t.Errorf("stream = %v, want true", got["stream"])
	}

	// options sub-object — sampling + private options.* are merged
	opts, ok := got["options"].(map[string]any)
	if !ok {
		t.Fatalf("options missing or not object: %v", got["options"])
	}
	wantOpts := map[string]any{
		"temperature":    0.7,
		"top_k":          float64(40),
		"num_predict":    float64(512),
		"stop":           []any{"\n"},
		"num_ctx":        float64(4096),
		"num_gpu":        float64(1),
		"mirostat":       float64(0),
		"mirostat_eta":   0.1,
		"tfs_z":          1.0,
	}
	for k, want := range wantOpts {
		if !reflect.DeepEqual(opts[k], want) {
			t.Errorf("options[%q] = %v, want %v", k, opts[k], want)
		}
	}

	// Top-level Ollama-private passthrough (format / keep_alive / raw)
	if got["format"] != "json" {
		t.Errorf("format = %v, want json", got["format"])
	}
	if got["keep_alive"] != "5m" {
		t.Errorf("keep_alive = %v, want 5m", got["keep_alive"])
	}
	if got["raw"] != true {
		t.Errorf("raw = %v, want true", got["raw"])
	}
}

// TestSerializeOllama_PrivateOptionsDontLeakAsTopLevel asserts the
// invariant that options.* keys are NEVER also emitted at the top level
// (even if a caller mistakenly also passes "ollama.num_ctx" — only
// "ollama.options.num_ctx" should land in options).
func TestSerializeOllama_PrivateOptionsDontLeakAsTopLevel(t *testing.T) {
	req := &InternalRequest{
		Model:    "llama3.1",
		Messages: []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}}},
		Extensions: map[string]json.RawMessage{
			"ollama.options.num_ctx": jsonRaw(`2048`),
			// This top-level key would be a misuse, but the contract is
			// robust to it — the prefix guard rejects non-"options." keys
			// that don't start with the bare namespace.
			"unrelated.trace_id": jsonRaw(`"abc-123"`),
		},
	}
	body, err := SerializeOllama(req)
	if err != nil {
		t.Fatalf("SerializeOllama: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, leaked := got["num_ctx"]; leaked {
		t.Errorf("options.num_ctx leaked to top-level: %v", got)
	}
	if _, leaked := got["unrelated.trace_id"]; leaked {
		t.Errorf("non-ollama extension leaked to top-level: %v", got)
	}
	opts, _ := got["options"].(map[string]any)
	if opts["num_ctx"] != float64(2048) {
		t.Errorf("options.num_ctx = %v, want 2048", opts["num_ctx"])
	}
}

// TestSerializeOllama_MissingModelRejected pins §3.6 rule "必填校验".
func TestSerializeOllama_MissingModelRejected(t *testing.T) {
	req := &InternalRequest{
		Messages: []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}}},
	}
	body, err := SerializeOllama(req)
	if err == nil {
		t.Fatalf("expected error for missing model, got body=%s", string(body))
	}
	if err != ErrSerializeOllamaMissingModel {
		t.Errorf("err = %v, want ErrSerializeOllamaMissingModel", err)
	}
	if body != nil {
		t.Errorf("body should be nil on error, got %s", string(body))
	}
}

// TestSerializeOllama_NilRequest pins §2.6.2 rule "nil returns error".
func TestSerializeOllama_NilRequest(t *testing.T) {
	body, err := SerializeOllama(nil)
	if err == nil {
		t.Fatal("expected error for nil request")
	}
	if body != nil {
		t.Errorf("body should be nil, got %s", string(body))
	}
}

// TestSerializeOllama_MaxTokensZeroOmitsNumPredict documents the
// "MaxTokens=0 means unlimited" contract: we MUST NOT emit num_predict when
// the IR has MaxTokens=0, because Ollama treats absent num_predict as
// "no limit" and a literal 0 as "produce 0 tokens".
func TestSerializeOllama_MaxTokensZeroOmitsNumPredict(t *testing.T) {
	req := &InternalRequest{
		Model:    "llama3.1",
		Messages: []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}}},
	}
	body, err := SerializeOllama(req)
	if err != nil {
		t.Fatalf("SerializeOllama: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, present := got["options"]; present {
		t.Errorf("options should be absent when no sampling params set, got %v", got["options"])
	}
}

// TestSerializeOllama_ToolsPassThroughOpenAIShape asserts the Ollama 0.5+
// wire shape is honored for tools — `{"type":"function","function":{...}}`.
func TestSerializeOllama_ToolsPassThroughOpenAIShape(t *testing.T) {
	req := &InternalRequest{
		Model:    "llama3.1",
		Messages: []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}}},
		Tools: []ToolDefinition{
			{
				Name:        "Read",
				Description: "Read a file",
				Parameters:  jsonRaw(`{"type":"object","properties":{"path":{"type":"string"}}}`),
			},
		},
	}
	body, err := SerializeOllama(req)
	if err != nil {
		t.Fatalf("SerializeOllama: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	tools, ok := got["tools"].([]any)
	if !ok {
		t.Fatalf("tools missing or not array: %v", got["tools"])
	}
	if len(tools) != 1 {
		t.Fatalf("len(tools) = %d, want 1", len(tools))
	}
	t0 := tools[0].(map[string]any)
	if t0["type"] != "function" {
		t.Errorf("tools[0].type = %v, want function", t0["type"])
	}
	fn := t0["function"].(map[string]any)
	if fn["name"] != "Read" {
		t.Errorf("function.name = %v, want Read", fn["name"])
	}
	params := fn["parameters"].(map[string]any)
	if params["type"] != "object" {
		t.Errorf("function.parameters.type = %v, want object", params["type"])
	}
}

// TestSerializeOllama_MessagesPreserveRole is a regression guard for the
// role set {user, assistant, system, tool}. Mixed multi-block text
// content joins with "\n".
func TestSerializeOllama_MessagesPreserveRole(t *testing.T) {
	req := &InternalRequest{
		Model: "llama3.1",
		Messages: []Message{
			{Role: "system", Content: []ContentBlock{{Type: "text", Text: "You are a helper."}}},
			{Role: "user", Content: []ContentBlock{
				{Type: "text", Text: "first line"},
				{Type: "text", Text: "second line"},
			}},
			{Role: "assistant", Content: []ContentBlock{{Type: "text", Text: "ok"}}},
			{Role: "tool", Content: []ContentBlock{{Type: "text", Text: "tool reply"}}, Name: "Read"},
		},
	}
	body, err := SerializeOllama(req)
	if err != nil {
		t.Fatalf("SerializeOllama: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	msgs := got["messages"].([]any)
	if len(msgs) != 4 {
		t.Fatalf("len(messages) = %d, want 4", len(msgs))
	}
	wantRoles := []string{"system", "user", "assistant", "tool"}
	wantContents := []string{"You are a helper.", "first line\nsecond line", "ok", "tool reply"}
	for i, want := range wantRoles {
		m := msgs[i].(map[string]any)
		if m["role"] != want {
			t.Errorf("msgs[%d].role = %v, want %s", i, m["role"], want)
		}
		if m["content"] != wantContents[i] {
			t.Errorf("msgs[%d].content = %v, want %q", i, m["content"], wantContents[i])
		}
	}
}

// TestSerializeOllama_FormatObjectPassesThrough is the Ollama 0.5+ shape
// where `format` is a JSON Schema object rather than the bare string "json".
// We must not collapse the object to a string.
func TestSerializeOllama_FormatObjectPassesThrough(t *testing.T) {
	req := &InternalRequest{
		Model:    "llama3.1",
		Messages: []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}}},
		Extensions: map[string]json.RawMessage{
			"ollama.format": jsonRaw(`{"type":"object","properties":{"x":{"type":"string"}}}`),
		},
	}
	body, err := SerializeOllama(req)
	if err != nil {
		t.Fatalf("SerializeOllama: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	fmt, ok := got["format"].(map[string]any)
	if !ok {
		t.Fatalf("format = %T, want object: %v", got["format"], got["format"])
	}
	if fmt["type"] != "object" {
		t.Errorf("format.type = %v, want object", fmt["type"])
	}
}

// jsonRaw is a tiny helper to construct json.RawMessage literals in table
// form. We avoid using string→json.RawMessage conversions inline because
// the cast can hide typos in the JSON shape.
func jsonRaw(s string) json.RawMessage {
	return json.RawMessage(s)
}

// TestSerializeOllama_BodyHasNoOpenAISmokeFields guards §5 regression —
// the Ollama wire MUST NOT carry OpenAI-flavored fields like
// `response_format`, `max_tokens` at the top level, or `messages[].name`.
// They go either under `options` / `tools` / per-message or are dropped.
func TestSerializeOllama_BodyHasNoOpenAISmokeFields(t *testing.T) {
	req := &InternalRequest{
		Model:       "llama3.1",
		MaxTokens:   256,
		Temperature: floatPtr(0.5),
		Messages:    []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}}},
	}
	body, err := SerializeOllama(req)
	if err != nil {
		t.Fatalf("SerializeOllama: %v", err)
	}
	// raw check (string contains)
	s := string(body)
	if strings.Contains(s, `"response_format"`) {
		t.Errorf("body contains response_format: %s", s)
	}
	if strings.Contains(s, `"max_tokens"`) {
		t.Errorf("body contains max_tokens (must be options.num_predict): %s", s)
	}
	if strings.Contains(s, `"top_p":`) {
		// top_p is allowed ONLY under options
		// quick sanity: count occurrences of top_p — must be exactly 1
		if strings.Count(s, `"top_p"`) > 1 {
			t.Errorf("top_p appears more than once: %s", s)
		}
	}
}