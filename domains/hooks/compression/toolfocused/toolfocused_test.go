package toolfocused

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// mustBody 把 messages 封成 chat body JSON。
func mustBody(t *testing.T, messages []any) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{"messages": messages})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func parseBody(t *testing.T, body []byte) []any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return doc["messages"].([]any)
}

// ── 策略单测：fileContent ──────────────────────────────────────────

func TestCompressFileContent_ElidesMiddle(t *testing.T) {
	var lines []string
	for i := 0; i < 40; i++ {
		lines = append(lines, fmt.Sprintf("const x%d = %d", i, i))
	}
	content := strings.Join(lines, "\n")
	out, ok := compressFileContent(content)
	if !ok {
		t.Fatal("expected hit")
	}
	if !strings.Contains(out, "[15 lines elided]") {
		t.Errorf("missing elision marker: %q", out)
	}
	if !strings.Contains(out, "const x0 = 0") || !strings.Contains(out, "const x39 = 39") {
		t.Errorf("head/tail lines lost: %q", out)
	}
}

func TestCompressFileContent_ShortFileUntouched(t *testing.T) {
	if _, ok := compressFileContent("import a\nimport b"); ok {
		t.Error("2 lines should not hit")
	}
	// 30 行但 ≤ keep+tail(25)？不，30 > 25，会命中；用 24 行验证不命中。
	var lines []string
	for i := 0; i < 24; i++ {
		lines = append(lines, fmt.Sprintf("var v%d = %d", i, i))
	}
	if _, ok := compressFileContent(strings.Join(lines, "\n")); ok {
		t.Error("24 lines (<= keep+tail) should not hit")
	}
}

func TestCompressFileContent_NonCodeSkipped(t *testing.T) {
	var lines []string
	for i := 0; i < 40; i++ {
		lines = append(lines, fmt.Sprintf("prose line %d without code", i))
	}
	if _, ok := compressFileContent(strings.Join(lines, "\n")); ok {
		t.Error("non-code content should not hit")
	}
}

// ── 策略单测：grepSearch ───────────────────────────────────────────

func TestCompressGrepSearch_TruncatesAndListsFiles(t *testing.T) {
	var lines []string
	for i := 0; i < 40; i++ {
		lines = append(lines, fmt.Sprintf("/src/file%d.go:1%d:match text here", i%5, i))
	}
	out, ok := compressGrepSearch(strings.Join(lines, "\n"))
	if !ok {
		t.Fatal("expected hit")
	}
	if !strings.Contains(out, "[10 more matches]") {
		t.Errorf("missing more-matches marker: %q", out)
	}
	if !strings.Contains(out, "Files: /src/file0.go") {
		t.Errorf("missing deduped file list: %q", out)
	}
	// 5 个文件去重后清单只列 5 个路径。
	if got := strings.Count(out, "/src/file"); got != 5+30 { // 30 行匹配 + 5 个 Files 项
		t.Errorf("unexpected path occurrences %d", got)
	}
}

func TestCompressGrepSearch_NonGrepSkipped(t *testing.T) {
	if _, ok := compressGrepSearch("just prose\nno colon paths"); ok {
		t.Error("non-grep content should not hit")
	}
}

// ── 策略单测：shellOutput ──────────────────────────────────────────

func TestCompressShellOutput_StripsANSIAndDedups(t *testing.T) {
	var b strings.Builder
	b.WriteString("\x1b[32mOK\x1b[0m\n")
	for i := 0; i < 60; i++ {
		b.WriteString("$ echo hi\n")
	}
	b.WriteString("result line\n")
	out, ok := compressShellOutput(b.String())
	if !ok {
		t.Fatal("expected hit (has ANSI)")
	}
	if strings.Contains(out, "\x1b[") {
		t.Errorf("ANSI not stripped: %q", out)
	}
	// 连续重复的 "$ echo hi" 只保留一行。
	if got := strings.Count(out, "$ echo hi"); got != 1 {
		t.Errorf("dedup failed, %d occurrences", got)
	}
	if !strings.Contains(out, "result line") {
		t.Error("tail line lost")
	}
	// 末 50 行窗口：开头绿色 OK 在窗口外被丢弃。
	if strings.Contains(out, "OK") {
		t.Error("line outside last-50 window should be dropped")
	}
}

func TestCompressShellOutput_PlainProseSkipped(t *testing.T) {
	if _, ok := compressShellOutput("plain\noutput\nno markers"); ok {
		t.Error("content without ANSI/prompt should not hit")
	}
}

// ── 策略单测：json ─────────────────────────────────────────────────

func TestCompressJSON_ArraySummary(t *testing.T) {
	arr := make([]any, 0, 20)
	for i := 0; i < 20; i++ {
		arr = append(arr, map[string]any{"id": i, "name": fmt.Sprintf("item-%02d-%s", i, strings.Repeat("x", 120))})
	}
	content := string(mustJSON(t, arr))
	if len(content) <= 2000 {
		t.Fatalf("fixture under 2000 chars (%d); enlarge", len(content))
	}
	out, ok := compressJSON(content)
	if !ok {
		t.Fatal("expected hit")
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("output not valid JSON: %v", err)
	}
	if parsed["type"] != "array" || parsed["total"] != float64(20) {
		t.Errorf("array envelope wrong: %v", parsed)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func TestCompressJSON_ObjectKeySummary(t *testing.T) {
	obj := map[string]any{}
	for i := 0; i < 30; i++ {
		obj[fmt.Sprintf("key_%02d", i)] = strings.Repeat(fmt.Sprintf("value %d ", i), 10)
	}
	content := string(mustJSON(t, obj))
	if len(content) <= 2000 {
		t.Fatalf("fixture under 2000 chars (%d); enlarge", len(content))
	}
	out, ok := compressJSON(content)
	if !ok {
		t.Fatal("expected hit")
	}
	if !strings.Contains(out, "_remaining_10_keys") {
		t.Errorf("missing remaining-keys marker: %q", out)
	}
	if strings.Contains(out, "value 25") {
		t.Error("key 25 (beyond first 20) should be elided")
	}
}

func TestCompressJSON_SmallPayloadSkipped(t *testing.T) {
	if _, ok := compressJSON(`{"a":1}`); ok {
		t.Error("small payload should not hit")
	}
}

// ── 策略单测：errorMessage ─────────────────────────────────────────

func TestCompressErrorMessage_KeepsHeadAndTail(t *testing.T) {
	lines := []string{"Error: something failed"}
	for i := 0; i < 20; i++ {
		lines = append(lines, fmt.Sprintf("  at frame%d (file%d.go:10)", i, i))
	}
	out, ok := compressErrorMessage(strings.Join(lines, "\n"))
	if !ok {
		t.Fatal("expected hit")
	}
	if !strings.HasPrefix(out, "Error: something failed") {
		t.Error("error line lost")
	}
	if !strings.Contains(out, "frame0") {
		t.Error("head frame lost")
	}
	if !strings.Contains(out, "frame19") {
		t.Error("tail frame lost")
	}
	if !strings.Contains(out, "[7 frames elided]") {
		t.Errorf("missing elision marker: %q", out)
	}
	if strings.Contains(out, "frame11") {
		t.Error("middle frame should be elided")
	}
}

func TestCompressErrorMessage_PlainTextSkipped(t *testing.T) {
	if _, ok := compressErrorMessage("all good here\nnothing wrong"); ok {
		t.Error("non-error content should not hit")
	}
}

// ── CompressToolResult 优先级 ──────────────────────────────────────

func TestCompressToolResult_StrategyOrder(t *testing.T) {
	// 既是代码文件又有错误关键词：fileContent 先命中。
	var lines []string
	for i := 0; i < 40; i++ {
		lines = append(lines, "import mod")
	}
	_, strategy, _, hit := CompressToolResult(strings.Join(lines, "\n"), DefaultStrategies())
	if !hit || strategy != "fileContent" {
		t.Errorf("expected fileContent first, got %s hit=%v", strategy, hit)
	}
}

func TestCompressToolResult_DisabledStrategySkipped(t *testing.T) {
	var lines []string
	for i := 0; i < 40; i++ {
		lines = append(lines, "import mod")
	}
	opts := DefaultStrategies()
	opts.FileContent = false
	_, strategy, _, hit := CompressToolResult(strings.Join(lines, "\n"), opts)
	if hit {
		t.Errorf("disabled fileContent should skip, got %s", strategy)
	}
}

// ── Apply 集成：OpenAI shape ───────────────────────────────────────

func TestApply_OpenAIToolMessage(t *testing.T) {
	var lines []string
	for i := 0; i < 40; i++ {
		lines = append(lines, fmt.Sprintf("const line%d = %d", i, i))
	}
	in := mustBody(t, []any{
		map[string]any{"role": "system", "content": "sys prompt"},
		map[string]any{"role": "user", "content": "run the tool"},
		map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{
			map[string]any{"id": "call_1", "type": "function",
				"function": map[string]any{"name": "read_file", "arguments": `{}`}},
		}},
		map[string]any{"role": "tool", "tool_call_id": "call_1", "content": strings.Join(lines, "\n")},
		map[string]any{"role": "user", "content": "thanks"},
	})
	out, res, applied := Apply(in, DefaultStrategies())
	if !applied {
		t.Fatal("expected applied")
	}
	msgs := parseBody(t, out)
	toolMsg := msgs[3].(map[string]any)
	c := toolMsg["content"].(string)
	if !strings.Contains(c, "[15 lines elided]") {
		t.Errorf("tool content not compressed: %q", c)
	}
	if toolMsg["tool_call_id"] != "call_1" {
		t.Error("tool_call_id must be preserved")
	}
	// system/user 消息原样。
	if msgs[0].(map[string]any)["content"] != "sys prompt" {
		t.Error("system message must be untouched")
	}
	if msgs[4].(map[string]any)["content"] != "thanks" {
		t.Error("user message must be untouched")
	}
	if len(res.Techniques) == 0 || res.Techniques[0] != "fileContent" {
		t.Errorf("techniques = %v", res.Techniques)
	}
	if res.SavedTokens <= 0 {
		t.Error("saved tokens must be positive")
	}
}

func TestApply_IdempotentCompressedMarker(t *testing.T) {
	in := mustBody(t, []any{
		map[string]any{"role": "tool", "content": "[COMPRESSED:summary] already done"},
	})
	out, _, applied := Apply(in, DefaultStrategies())
	if applied {
		t.Error("already-compressed content must be skipped")
	}
	if string(out) != string(in) {
		t.Error("body must be returned unchanged")
	}
}

func TestApply_FailOpenOnInvalidJSON(t *testing.T) {
	out, _, applied := Apply([]byte(`{not json`), DefaultStrategies())
	if applied {
		t.Error("invalid JSON must fail-open")
	}
	if string(out) != `{not json` {
		t.Error("original body must be returned")
	}
}

func TestApply_EmptyMessages(t *testing.T) {
	out, _, applied := Apply([]byte(`{"messages":[]}`), DefaultStrategies())
	if applied {
		t.Error("empty messages must be a no-op")
	}
	if string(out) != `{"messages":[]}` {
		t.Error("body must be unchanged")
	}
}

// ── Apply 集成：Anthropic shape ────────────────────────────────────

func TestApply_AnthropicToolResultBlock_StringContent(t *testing.T) {
	var lines []string
	for i := 0; i < 40; i++ {
		lines = append(lines, fmt.Sprintf("export const v%d = %d", i, i))
	}
	in := mustBody(t, []any{
		map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": "tu_1",
				"content": strings.Join(lines, "\n")},
		}},
	})
	out, res, applied := Apply(in, DefaultStrategies())
	if !applied {
		t.Fatal("expected applied")
	}
	msgs := parseBody(t, out)
	blocks := msgs[0].(map[string]any)["content"].([]any)
	block := blocks[0].(map[string]any)
	if block["type"] != "tool_result" || block["tool_use_id"] != "tu_1" {
		t.Error("block structure must be preserved")
	}
	if !strings.Contains(block["content"].(string), "lines elided") {
		t.Errorf("block content not compressed: %q", block["content"])
	}
	if res.SavedTokens <= 0 {
		t.Error("saved tokens must be positive")
	}
}

func TestApply_AnthropicToolResultBlock_TextParts(t *testing.T) {
	var lines []string
	for i := 0; i < 40; i++ {
		lines = append(lines, fmt.Sprintf("function f%d(){{}}", i))
	}
	in := mustBody(t, []any{
		map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": "tu_2", "content": []any{
				map[string]any{"type": "text", "text": strings.Join(lines, "\n")},
				map[string]any{"type": "image", "source": map[string]any{"url": "x"}},
			}},
		}},
	})
	out, _, applied := Apply(in, DefaultStrategies())
	if !applied {
		t.Fatal("expected applied")
	}
	msgs := parseBody(t, out)
	blocks := msgs[0].(map[string]any)["content"].([]any)
	parts := blocks[0].(map[string]any)["content"].([]any)
	text := parts[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "lines elided") {
		t.Errorf("text part not compressed: %q", text)
	}
	// 非 text part 原样保留。
	if parts[1].(map[string]any)["type"] != "image" {
		t.Error("non-text part must be preserved")
	}
}

func TestApply_AnthropicNonToolBlocksUntouched(t *testing.T) {
	in := mustBody(t, []any{
		map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "text", "text": strings.Repeat("import a\n", 40)},
		}},
	})
	out, _, applied := Apply(in, DefaultStrategies())
	if applied {
		t.Error("non-tool_result blocks must not be compressed")
	}
	if string(out) != string(in) {
		t.Error("body must be unchanged")
	}
}

// ── NeverWorse 兼容：输出不得膨胀 ──────────────────────────────────

func TestApply_NeverBloatsSmallToolResults(t *testing.T) {
	// 短内容不会命中任何策略（各策略有最小阈值），Apply 应为 no-op。
	in := mustBody(t, []any{
		map[string]any{"role": "tool", "content": "ok"},
	})
	out, _, applied := Apply(in, DefaultStrategies())
	if applied || len(out) != len(in) {
		t.Errorf("small result must be untouched: applied=%v len=%d vs %d", applied, len(out), len(in))
	}
}
