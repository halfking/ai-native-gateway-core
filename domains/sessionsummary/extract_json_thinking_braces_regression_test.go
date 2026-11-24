package sessionsummary

import (
	"encoding/json"
	"strings"
	"testing"
)

// ★★★ 2026-11-25（12h 修订审计轮 P1 的防回归钉测）★★★
//
// ★★ 为什么需要这道钉测：★★
// ★★ 2026-11-24 22:53 的 c255b95df 给「thinking 误剥回退」加的判据是
// ★★ **「剥离后大括号计数下降 ⇒ 判成误剥 ⇒ 回退原文」**。★★
// ★★ 但**合法地移除整个 thinking 块**同样会让大括号计数下降（只要 thinking 正文里
// ★★ 出现 {...}）⇒ ★★ 判据把「剥离成功」与「误剥」混为一谈 ⇒ ★★
// ★★ `<think>{"draft":"x"}</think>{"title":"t"}` ⇒ 回退到未剥离原文 ⇒
// ★★ JSON 解析必失败。★ 推理模型输出 thinking 里带 JSON 是**常见**形态。
//
// ★★ 本钉测把两类形态**分别**钉住：★★
// ★★   ① thinking 里带花括号 ⇒ **必须**剥离成功并解出 JSON（★ 不许回退）；
// ★★   ② JSON 字符串值内部含字面标签 ⇒ **必须**回退到原文并保留字面量
// ★★      （★ 这是 c255b95df 最初要保护的那一类，★ 不许被"修好"时一起弄丢）。
func TestExtractJSONThinkingWithBracesIsNotMistakenForMisStrip(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"thinking with json object", `<think>{"draft":"x"}</think>{"title":"t","summary":"s"}`},
		{"thinking with multiple json fragments", `<think>Let me think about {"a":1} and {"b":2}</think>{"title":"t"}`},
		{"thinking variant tag with braces", `<thinking>{"draft":"x"}</thinking>{"title":"t"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractJSON(tc.in)
			if !json.Valid([]byte(got)) {
				t.Fatalf("extractJSON returned invalid JSON: %q", got)
			}
			if strings.Contains(got, "draft") {
				t.Fatalf("thinking content leaked into the result: %q", got)
			}
			var v map[string]any
			if err := json.Unmarshal([]byte(got), &v); err != nil {
				t.Fatalf("unmarshal failed: %v", err)
			}
			if v["title"] != "t" {
				t.Fatalf("unexpected title: got %#v, want %q", v["title"], "t")
			}
		})
	}
}

// TestExtractJSONLiteralTagsInsideStringValueStillFallBack 钉住 c255b95df
// 最初要保护的那一类：★ JSON **字符串值内部**出现字面标签 ⇒ 剥离会把骨架切坏
// ⇒ ★ 必须回退到原文，且**字面量本身不得被改写**。
func TestExtractJSONLiteralTagsInsideStringValueStillFallBack(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"output literal", `{"summary":"a</output>b","title":"t"}`, "a</output>b"},
		{"answer literal", `{"summary":"user asked <answer> what","title":"t"}`, "user asked <answer> what"},
		{"output literal with braces", `{"s":"<output>{x}","t":"y"}`, "<output>{x}"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractJSON(tc.in)
			if !json.Valid([]byte(got)) {
				t.Fatalf("extractJSON returned invalid JSON: %q", got)
			}
			var v map[string]any
			if err := json.Unmarshal([]byte(got), &v); err != nil {
				t.Fatalf("unmarshal failed: %v", err)
			}
			// 字面量必须逐字保留（不许被"摘标签"改写掉）。
			blob := got
			if !strings.Contains(blob, tc.want) {
				t.Fatalf("literal tag was rewritten away\n got: %q\nwant substring: %q", got, tc.want)
			}
		})
	}
}

// TestExtractJSONUnclosedThinkingKeepsStrippedCandidate 钉住既有语义：
// ★ 未闭合 thinking 且内容本身不是 JSON ⇒ 仍返回**剥离后**的候选
// ★ （旧行为：剥掉开标签、保留其余；回退到未剥离原文是错的）。
func TestExtractJSONUnclosedThinkingKeepsStrippedCandidate(t *testing.T) {
	in := `<think>{"not json"`
	got := extractJSON(in)
	if strings.HasPrefix(got, "<think>") {
		t.Fatalf("unclosed thinking prefix was not stripped: %q", got)
	}
}

// TestExtractJSONPlainJSONUnchanged 钉住最常见形态不受影响。
func TestExtractJSONPlainJSONUnchanged(t *testing.T) {
	in := `{"title":"t","summary":"s"}`
	got := extractJSON(in)
	if got != in {
		t.Fatalf("plain JSON was modified: got %q, want %q", got, in)
	}
}
