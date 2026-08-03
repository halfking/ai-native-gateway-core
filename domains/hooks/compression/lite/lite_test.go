package lite

import (
	"encoding/json"
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

// TestStage_Whitespace 验证换行折叠 + 行尾空白清理。
func TestStage_Whitespace(t *testing.T) {
	in := mustBody(t, []any{
		map[string]any{"role": "user", "content": "line1   \n\n\n\nline2\t\n"},
	})
	out, _, applied := Apply(in, Options{})
	if !applied {
		t.Fatal("expected applied")
	}
	msgs := parseBody(t, out)
	c := msgs[0].(map[string]any)["content"].(string)
	// 输入 "line1   \n\n\n\nline2\t\n"：4 个 \n 折叠为 2，
	// 行尾空格/tab 去掉，末尾 \n 产生一个空行（与 TS split/join 一致）。
	want := "line1\n\nline2\n"
	if c != want {
		t.Errorf("whitespace normalize = %q, want %q", c, want)
	}
}

func TestStage_Whitespace_PreserveSystem(t *testing.T) {
	in := mustBody(t, []any{
		map[string]any{"role": "system", "content": "sys   \n\n\n"},
		map[string]any{"role": "user", "content": "u   "},
	})
	out, _, applied := Apply(in, Options{PreserveSystemPrompt: true})
	if !applied {
		t.Fatal("user message should still trigger applied")
	}
	msgs := parseBody(t, out)
	if msgs[0].(map[string]any)["content"].(string) != "sys   \n\n\n" {
		t.Error("system prompt should be preserved untouched")
	}
	if msgs[1].(map[string]any)["content"].(string) != "u" {
		t.Error("user trailing whitespace should be trimmed")
	}
}

// TestStage_SystemDedup 验证前 200 字符 trim key 重复删除。
func TestStage_SystemDedup(t *testing.T) {
	in := mustBody(t, []any{
		map[string]any{"role": "system", "content": "You are helpful.   "},
		map[string]any{"role": "system", "content": "You are helpful."}, // trim 后同 key
		map[string]any{"role": "user", "content": "hi"},
	})
	out, _, applied := Apply(in, Options{})
	if !applied {
		t.Fatal("expected applied")
	}
	msgs := parseBody(t, out)
	if len(msgs) != 2 {
		t.Errorf("dedup should drop 1 duplicate system, got %d messages", len(msgs))
	}
}

// TestStage_ToolCompress 验证 >2000 字符的 tool result 截断 + marker。
func TestStage_ToolCompress(t *testing.T) {
	long := strings.Repeat("a", 2500)
	in := mustBody(t, []any{
		map[string]any{"role": "tool", "content": long},
	})
	out, _, applied := Apply(in, Options{})
	if !applied {
		t.Fatal("expected applied")
	}
	msgs := parseBody(t, out)
	c := msgs[0].(map[string]any)["content"].(string)
	if !strings.HasSuffix(c, truncationMarker) {
		t.Errorf("truncated content should end with marker, got suffix %q", tail(c, 30))
	}
	if len(c) >= len(long) {
		t.Errorf("truncated content should be shorter: got %d >= %d", len(c), len(long))
	}
}

// TestStage_ToolCompress_ShortUnchanged 验证 <=2000 不截断。
func TestStage_ToolCompress_ShortUnchanged(t *testing.T) {
	in := mustBody(t, []any{
		map[string]any{"role": "tool", "content": strings.Repeat("a", 2000)},
	})
	out, res, applied := Apply(in, Options{})
	// 2000 不超阈值；无 stage 触发（content 无空白问题）。
	_ = out
	if applied {
		t.Errorf("2000-char tool result should not be truncated; techniques=%v", res.Techniques)
	}
}

// TestStage_RedundantRemove 验证相邻同 role 同 content 删除。
func TestStage_RedundantRemove(t *testing.T) {
	in := mustBody(t, []any{
		map[string]any{"role": "user", "content": "dup"},
		map[string]any{"role": "user", "content": "dup"}, // 与前一条完全相同 → 删
		map[string]any{"role": "user", "content": "other"},
	})
	out, _, applied := Apply(in, Options{})
	if !applied {
		t.Fatal("expected applied")
	}
	msgs := parseBody(t, out)
	if len(msgs) != 2 {
		t.Errorf("redundant remove should drop 1, got %d", len(msgs))
	}
}

// TestStage_ImagePlaceholder 验证非 vision 模型 data:image/ → 占位符。
func TestStage_ImagePlaceholder(t *testing.T) {
	noVision := false
	in := mustBody(t, []any{
		map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{
					"type": "image_url",
					"image_url": map[string]any{
						"url": "data:image/png;base64,iVBOR...",
					},
				},
			},
		},
	})
	out, _, applied := Apply(in, Options{SupportsVision: &noVision})
	if !applied {
		t.Fatal("expected applied")
	}
	msgs := parseBody(t, out)
	content := msgs[0].(map[string]any)["content"].([]any)
	part := content[0].(map[string]any)
	if part["type"] != "text" {
		t.Errorf("image part should become text, got type=%v", part["type"])
	}
	if part["text"] != "[image: png]" {
		t.Errorf("placeholder = %q, want [image: png]", part["text"])
	}
}

func TestStage_ImagePlaceholder_VisionModelSkipped(t *testing.T) {
	yesVision := true
	in := mustBody(t, []any{
		map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,xxx"}},
			},
		},
	})
	_, _, applied := Apply(in, Options{SupportsVision: &yesVision})
	if applied {
		t.Error("vision-capable model should skip image placeholder")
	}
}

func TestStage_ImagePlaceholder_UnknownVisionSkipped(t *testing.T) {
	// SupportsVision=nil（未知）→ 不运行 image placeholder（与 TS !== false 一致）。
	in := mustBody(t, []any{
		map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,xxx"}},
			},
		},
	})
	_, _, applied := Apply(in, Options{})
	if applied {
		t.Error("unknown vision (nil) should skip image placeholder")
	}
}

// TestApply_NoMessages 验证无 messages 字段 fail-open。
func TestApply_NoMessages(t *testing.T) {
	in := []byte(`{"model":"x"}`)
	out, _, applied := Apply(in, Options{})
	if applied || string(out) != string(in) {
		t.Error("body without messages should fail-open unchanged")
	}
}

// TestApply_InvalidJSON 验证非合法 JSON fail-open。
func TestApply_InvalidJSON(t *testing.T) {
	in := []byte(`not json`)
	out, _, applied := Apply(in, Options{})
	if applied || string(out) != string(in) {
		t.Error("invalid JSON should fail-open unchanged")
	}
}

// TestApply_Ordering 验证 5 stage 顺序与 lite.ts 一致。
func TestApply_Ordering(t *testing.T) {
	long := strings.Repeat("a", 2500)
	noVision := false
	in := mustBody(t, []any{
		map[string]any{"role": "system", "content": "sys   \n\n\n\n"}, // whitespace
		map[string]any{"role": "system", "content": "sys"},            // dedup (trim key 同 "sys")
		map[string]any{"role": "tool", "content": long},               // tool-compress
		map[string]any{"role": "user", "content": "dup   "},           // whitespace + redundant
		map[string]any{"role": "user", "content": "dup"},              // redundant-remove (whitespace 后变 "dup")
		map[string]any{ // image-placeholder
			"role": "assistant",
			"content": []any{
				map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/jpeg;base64,xxx"}},
			},
		},
	})
	_, res, applied := Apply(in, Options{SupportsVision: &noVision})
	if !applied {
		t.Fatal("expected applied")
	}
	// 至少触发 whitespace（第一条 system）。system-dedup 取决于 trim 后 key 是否同；
	// 这里 "sys   \n\n\n\n" trim="sys" 与 "sys" 同 → dedup 删第二条。
	if !contains(res.Techniques, "whitespace") {
		t.Errorf("expected whitespace stage, got %v", res.Techniques)
	}
	if !contains(res.Techniques, "tool-compress") {
		t.Errorf("expected tool-compress stage, got %v", res.Techniques)
	}
}

func TestExtractImageFormat(t *testing.T) {
	cases := map[string]string{
		"data:image/png;base64,xxx":  "png",
		"data:image/jpeg;base64,xxx": "jpeg",
		"data:image/webp;foo":        "webp",
		"data:image/;":               "unknown",
		"bogus":                      "unknown",
	}
	for in, want := range cases {
		if got := extractImageFormat(in); got != want {
			t.Errorf("extractImageFormat(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBackOffToWordBoundary(t *testing.T) {
	// 在 2000 处切，前面 80 字符内有空格 → 回退到词边界。
	content := strings.Repeat("a", 1990) + " " + strings.Repeat("b", 100)
	cut := backOffToWordBoundary(content, 2000)
	// 1990 处是空格；findWhitespaceBackward 应返回 1990（空格位置）。
	if cut != 1990 {
		t.Errorf("backOff cut = %d, want 1990", cut)
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
