package admin

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// ★ 2026-10-25：`:response` 槽位收下 content 分片数组。
//
// ★ 背景（实测，不是推断）：会话族把「带工具调用的那一轮」的回答写成
//
//	OpenAI 风格的 content 数组 —— `[{"role":"assistant",
//	"content":[{"type":"text","text":"…"}],"tool_calls":[…]}]`。
//	端点原来只收纯字符串 ⇒ 那一类回答一律 404 ⇒ 「答案在那儿，却没人端得出来」。
//
// ★ 治理域的实测规模：域内 399 次使用里 184 次是这种形状（46.1%），
//
//	37 次是纯字符串 ⇒ 可评估比例从 9.3% 变成 55.4%（上界 221/399，
//	剩下 178 次是网关压根没写答案，那种不是形状问题）。
//
// ★ ★★ 每条断言都自带一句说明 —— ★ 因为变异台要按【断言的那句话】定位
//
//	「红在哪」：★ 一条没有消息的断言红了，别的会话只看到「Should be true」，
//	查不出是哪一条判据被咬住了。
func TestNormalizeControlledResponseServesContentArray(t *testing.T) {
	// ★ 生产真实形状：1 个 chunk、content 数组里 1 个 text 部件、旁边挂着 tool_calls。
	production := `[{"role":"assistant","content":[{"type":"text","text":"看完了，构建通过。"}],"tool_calls":[{"id":"call_1","type":"function","function":{"name":"bash","arguments":"{\"cmd\":\"ls\"}"}}]}]`
	content, ok := normalizeControlledResponse(&production)
	require.True(t, ok, "a content array with a text part must be served")
	require.Equal(t, "看完了，构建通过。", content, "the production shape's text is served verbatim")

	// ★ 多个 text 部件 ⇒ 拼起来（两块，不是两个半截词）。
	multi := `[{"role":"assistant","content":[{"type":"text","text":"第一块"},{"type":"text","text":"第二块"}]}]`
	content, ok = normalizeControlledResponse(&multi)
	require.True(t, ok, "a content array with several text parts must be served")
	require.Equal(t, "第一块\n第二块", content, "two text parts are joined with a newline")

	// ★ 非 text 部件被跳过，不进正文。
	mixed := `[{"role":"assistant","content":[{"type":"image","url":"x"},{"type":"text","text":"只有这段是文字"}]}]`
	content, ok = normalizeControlledResponse(&mixed)
	require.True(t, ok, "a content array with a text part and an image part must be served")
	require.Equal(t, "只有这段是文字", content, "a part without text contributes nothing")

	// ★ OpenAI 非流式形状：choices[].message.content 是数组。
	choices := `{"choices":[{"message":{"content":[{"type":"text","text":"choices 里的文字"}]}}]}`
	content, ok = normalizeControlledResponse(&choices)
	require.True(t, ok, "the choices[].message.content array must be served")
	require.Equal(t, "choices 里的文字", content, "the choices path serves the text part")
}

func TestNormalizeControlledResponseStillRefusesEmptyShapes(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"an array with no text part", `[{"role":"assistant","content":[{"type":"image","url":"x"}]}]`},
		{"an array whose text part is empty", `[{"role":"assistant","content":[{"type":"text","text":""}]}]`},
		{"an array whose text part is blank", `[{"role":"assistant","content":[{"type":"text","text":"   "}]}]`},
		{"an empty array", `[{"role":"assistant","content":[]}]`},
		{"a null content", `[{"role":"assistant","content":null}]`},
		{"a plain empty string", `[{"role":"assistant","content":""}]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			content, ok := normalizeControlledResponse(&tc.raw)
			require.False(t, ok, "this shape carries no text ⇒ it must stay unreadable")
			require.Empty(t, content, "this shape carries no text ⇒ nothing is served")
		})
	}
}

// ★ 纯字符串那条路一个字都没变（回归护栏）。
func TestNormalizeControlledResponseKeepsThePlainStringPath(t *testing.T) {
	raw := `[{"role":"assistant","content":"就是一段纯文字"}]`
	content, ok := normalizeControlledResponse(&raw)
	require.True(t, ok, "a plain string content must still be served")
	require.Equal(t, "就是一段纯文字", content, "the plain string path is unchanged")
}

// ★ 角色过滤没被这个改动带偏：非 assistant 的消息仍然跳过。
func TestNormalizeControlledResponseStillSkipsOtherRoles(t *testing.T) {
	raw := `[{"role":"user","content":[{"type":"text","text":"这是用户说的"}]}]`
	content, ok := normalizeControlledResponse(&raw)
	require.False(t, ok, "a user message is still not the assistant's answer")
	require.Empty(t, content, "a user message contributes nothing to the response slot")
}

// ★ ★ prompt 侧**没有**被这次改动带进来 —— 那边「content 数组被拒」是
//
//	既有语义（body_resolver_test.go 已钉住），★ 本次只动 `:response`。
func TestNormalizeControlledPromptStillRefusesContentArrays(t *testing.T) {
	raw := `{"messages":[{"role":"user","content":[{"type":"text","text":"数组形态的提问"}]}]}`
	content, ok := normalizeControlledPrompt(&raw)
	require.False(t, ok, "the prompt side is a different question; it did not change")
	require.Empty(t, content, "the prompt side still refuses a content array")
}

// ★ controlledText 本身的行为（不含上层形状）。
func TestControlledText(t *testing.T) {
	text, ok := controlledText(json.RawMessage(`"plain"`))
	require.True(t, ok, "a plain string is text")
	require.Equal(t, "plain", text, "the plain string is returned verbatim")

	text, ok = controlledText(json.RawMessage(`[{"type":"text","text":"a"},{"type":"text","text":"b"}]`))
	require.True(t, ok, "an array of text parts is text")
	require.Equal(t, "a\nb", text, "the text parts are joined with a newline")

	_, ok = controlledText(json.RawMessage(`[{"type":"text","text":"  "}]`))
	require.False(t, ok, "a blank text part is not text")

	_, ok = controlledText(json.RawMessage(`null`))
	require.False(t, ok, "null is not text")

	_, ok = controlledText(json.RawMessage(`123`))
	require.False(t, ok, "a number is not text")

	_, ok = controlledText(json.RawMessage(``))
	require.False(t, ok, "an empty raw message is not text")
}
