package compressor

import (
	"context"
	"strings"
	"testing"
)

// TestExtractConversationText_OpenAISmoke ensures the migration from
// routing/context_summarize.go preserved the contract.
func TestExtractConversationText_OpenAISmoke(t *testing.T) {
	body := []byte(`{"model":"m","messages":[
		{"role":"system","content":"You are helpful."},
		{"role":"user","content":"Hello"},
		{"role":"assistant","content":"Hi there"}
	]}`)
	got, err := extractConversationText(body, "openai-completions")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"[system]", "You are helpful.", "[user]", "Hello", "[assistant]", "Hi there"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in extracted text:\n%s", want, got)
		}
	}
}

// TestExtractConversationText_AnthropicSmoke checks the Anthropic
// branch (which also folds the top-level system field into the stream).
func TestExtractConversationText_AnthropicSmoke(t *testing.T) {
	body := []byte(`{"model":"m","system":"Be helpful.","messages":[
		{"role":"user","content":"Hello"}
	]}`)
	got, err := extractConversationText(body, "anthropic-messages")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"[system]", "Be helpful.", "[user]", "Hello"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in extracted text:\n%s", want, got)
		}
	}
}

// TestRawJSONTextContent_Shapes covers the three content shapes
// (string, text blocks, no content).
func TestRawJSONTextContent_Shapes(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"empty", "", ""},
		{"null", "null", ""},
		{"string_shape", `"hello"`, "hello"},
		{"blocks_shape", `[{"type":"text","text":"a"},{"type":"tool_use","name":"x"}]`, "a"},
		{"non_text_blocks", `[{"type":"image","src":"..."}]`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := rawJSONTextContent([]byte(tc.raw))
			if got != tc.want {
				t.Errorf("want %q, got %q", tc.want, got)
			}
		})
	}
}

// TestFormatOpenAIToolCalls verifies tool_call rendering.
func TestFormatOpenAIToolCalls(t *testing.T) {
	raw := []byte(`[{"id":"call_1","function":{"name":"search","arguments":"{\"q\":\"x\"}"}}]`)
	got := formatOpenAIToolCalls(raw)
	if !strings.Contains(got, "tool_call(search)") {
		t.Errorf("missing tool_call(search) in: %s", got)
	}
	if !strings.Contains(got, "q") {
		t.Errorf("missing args in: %s", got)
	}
	// Empty / null inputs should return empty.
	if got := formatOpenAIToolCalls(nil); got != "" {
		t.Errorf("nil input: want empty, got %q", got)
	}
	if got := formatOpenAIToolCalls([]byte("null")); got != "" {
		t.Errorf("null input: want empty, got %q", got)
	}
}

// TestFormatAnthropicToolBlocks covers both tool_use and tool_result
// block types.
func TestFormatAnthropicToolBlocks(t *testing.T) {
	raw := []byte(`[
		{"type":"tool_use","name":"search","input":{"q":"x"}},
		{"type":"tool_result","tool_use_id":"call_1","text":"ok"},
		{"type":"text","text":"ignored"}
	]`)
	got := formatAnthropicToolBlocks(raw)
	if !strings.Contains(got, "tool_use(search)") {
		t.Errorf("missing tool_use in: %s", got)
	}
	if !strings.Contains(got, "tool_result(call_1)") {
		t.Errorf("missing tool_result in: %s", got)
	}
	// Plain text blocks should NOT appear (we only fold tools).
	if strings.Contains(got, "ignored") {
		t.Errorf("text block should be ignored, got: %s", got)
	}
}

// TestMessageRoleAndSummary_ToolResult ensures tool-role messages get
// the "tool_result(<label>)" prefix.
func TestMessageRoleAndSummary_ToolResult(t *testing.T) {
	raw := []byte(`{"role":"tool","tool_call_id":"call_1","content":"42"}`)
	role, text := messageRoleAndSummary(raw)
	if role != "tool" {
		t.Errorf("role: want tool, got %q", role)
	}
	if !strings.Contains(text, "tool_result(call_1)") {
		t.Errorf("missing tool_result label in: %s", text)
	}
	if !strings.Contains(text, "42") {
		t.Errorf("missing content in: %s", text)
	}
}

// TestTruncateForLog checks the small bounds helper.
func TestTruncateForLog(t *testing.T) {
	if got := truncateForLog([]byte("hello"), 10); got != "hello" {
		t.Errorf("short: got %q", got)
	}
	if got := truncateForLog([]byte("hello world"), 5); got != "hello" {
		t.Errorf("long: got %q", got)
	}
}

// TestCompactionDisabled covers the env kill-switch.
func TestCompactionDisabled(t *testing.T) {
	t.Setenv("LLM_GATEWAY_COMPACTION_DISABLE", "")
	if compactionDisabled() {
		t.Error("empty env should mean enabled")
	}
	t.Setenv("LLM_GATEWAY_COMPACTION_DISABLE", "1")
	if !compactionDisabled() {
		t.Error("1 should disable")
	}
	t.Setenv("LLM_GATEWAY_COMPACTION_DISABLE", "true")
	if !compactionDisabled() {
		t.Error("true should disable")
	}
}

// TestPickCompactionCandidates_NilDeps ensures graceful no-op when
// dependencies are absent (e.g. tests, single-tenant mode).
func TestPickCompactionCandidates_NilDeps(t *testing.T) {
	if got := pickCompactionCandidates(context.Background(), nil, ""); got != nil {
		t.Errorf("nil deps: want nil, got %v", got)
	}
	if got := pickCompactionCandidates(context.Background(), &Dependencies{}, ""); got != nil {
		t.Errorf("nil Provider: want nil, got %v", got)
	}
}

// TestTryMemoraCompression_NilDeps ensures the function is nil-safe.
func TestTryMemoraCompression_NilDeps(t *testing.T) {
	if got, ok := tryMemoraCompression(context.Background(), nil, "", 0, []byte("x"), "task-1", "openai-completions"); ok || got != nil {
		t.Errorf("nil deps: want (nil, false), got (%v, %v)", got, ok)
	}
}

// TestTryLLMContextCompaction_Disabled ensures compactionDisabled env
// short-circuits the LLM pass.
func TestTryLLMContextCompaction_Disabled(t *testing.T) {
	t.Setenv("LLM_GATEWAY_COMPACTION_DISABLE", "1")
	body := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	got, ok := tryLLMContextCompaction(context.Background(), &Dependencies{}, "default", "openai-completions", body)
	if ok {
		t.Error("compaction should be disabled")
	}
	if string(got) != string(body) {
		t.Error("body should be returned unchanged")
	}
}
