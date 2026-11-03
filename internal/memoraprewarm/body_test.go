// body_test.go — W3 JSON body 提取/注入的契约（fail-open 矩阵）。
package memoraprewarm

import (
	"strings"
	"testing"
)

const (
	openAIBody = `{"model":"gpt-x","messages":[` +
		`{"role":"system","content":"你是助手"},{"role":"user","content":"帮我配色"},` +
		`{"role":"assistant","content":"好的"},{"role":"user","content":"偏好深色主题"}]}`
	anthropicBody = `{"model":"claude-x","system":"你是助手","messages":[` +
		`{"role":"user","content":[{"type":"text","text":"数组形态首问"}]}]}`
)

// 1) 提取：最后一条 user 消息文本。
func TestExtractQueryLastUser(t *testing.T) {
	if got := ExtractQueryFromBody([]byte(openAIBody)); got != "偏好深色主题" {
		t.Fatalf("got %q", got)
	}
	if got := ExtractQueryFromBody([]byte(anthropicBody)); got != "数组形态首问" {
		t.Fatalf("anthropic array-content: got %q", got)
	}
}

// 2) 提取 fail-open：坏 JSON / 无 user / 无 messages ⇒ 空。
func TestExtractQueryFailOpen(t *testing.T) {
	for name, body := range map[string]string{
		"bad json":  `{not json`,
		"no user":   `{"messages":[{"role":"assistant","content":"hi"}]}`,
		"no fields": `{"model":"m"}`,
		"empty":     ``,
	} {
		if got := ExtractQueryFromBody([]byte(body)); got != "" {
			t.Fatalf("%s: got %q want empty", name, got)
		}
	}
}

// 3) 注入：string content 尾拼，其余消息字节不动（稳定前缀保证）。
func TestInjectIntoStringContent(t *testing.T) {
	out, ok := InjectIntoMessages([]byte(openAIBody), "【记忆】用户偏好深色")
	if !ok {
		t.Fatal("must inject")
	}
	s := string(out)
	if !strings.Contains(s, "偏好深色主题\\n\\n【记忆】用户偏好深色") {
		t.Fatalf("injection not appended to last user: %s", s)
	}
	if !strings.Contains(s, `"content":"你是助手"`) {
		t.Fatalf("system message must stay byte-identical: %s", s)
	}
	if strings.Count(s, "【记忆】") != 1 {
		t.Fatal("injection must appear exactly once")
	}
}

// 4) 注入：块数组 content 追加 text block。
func TestInjectIntoBlockContent(t *testing.T) {
	out, ok := InjectIntoMessages([]byte(anthropicBody), "【记忆】x")
	if !ok {
		t.Fatal("must inject")
	}
	s := string(out)
	if !strings.Contains(s, `"text":"数组形态首问"`) || !strings.Contains(s, `"text":"【记忆】x"`) {
		t.Fatalf("text block not appended: %s", s)
	}
	if strings.Contains(s, `"system":"【记忆】`) {
		t.Fatal("top-level system must not be touched")
	}
}

// 5) 注入 fail-open：空注入 / 坏 JSON / 无 user / 无 messages ⇒ 原样。
func TestInjectFailOpen(t *testing.T) {
	orig := []byte(openAIBody)
	cases := map[string]struct {
		body      []byte
		injection string
	}{
		"empty injection": {orig, ""},
		"bad json":        {[]byte(`{oops`), "x"},
		"no user":         {[]byte(`{"messages":[{"role":"assistant","content":"hi"}]}`), "x"},
		"no messages":     {[]byte(`{"model":"m"}`), "x"},
		"nil content":     {[]byte(`{"messages":[{"role":"user"}]}`), "x"},
	}
	for name, tc := range cases {
		out, ok := InjectIntoMessages(tc.body, tc.injection)
		if ok {
			t.Fatalf("%s: must not inject", name)
		}
		if string(out) != string(tc.body) {
			t.Fatalf("%s: body must be returned unchanged", name)
		}
	}
}

// 6) 空 injection 与字节等价（对照组保证的 body 层面复核）。
func TestInjectEmptyIsIdentity(t *testing.T) {
	out, ok := InjectIntoMessages([]byte(openAIBody), "")
	if ok || string(out) != openAIBody {
		t.Fatal("empty injection must be identity")
	}
}
