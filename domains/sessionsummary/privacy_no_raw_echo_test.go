// privacy_no_raw_echo_test.go — 2026-11-25 12h 审计轮（0700）#18 落地的防回归钉测。
//
// 缺陷面（0400 审计轮遗留 #18）：parseSummaryResponse 的两条错误路径把
// LLM 原始响应的前 20/100 rune 以 %q 回显进 error 文本；worker
// （session_summary_worker.go）把 error 原样 slog 落盘 ⇒ 用户会话衍生内容
// （姓名 / 证件号 / 密钥样式串）进生产日志（0400 轮探针实测 LEAK=true ×2）。
//
// 本钉测判据：两条错误路径的 error 文本里**不得出现**响应正文的任何原文片段
// （只允许形态描述：runes=N / first=<类别> / first_key=len=N / html=true|false）。
package sessionsummary

import (
	"fmt"
	"strings"
	"testing"
)

// leakyResponse 模拟一条含用户敏感片段（密钥样式串 + 中文姓名）的非 JSON 响应。
var leakyResponse = `好的，以下是会话内容：张三的密钥是 sk-ant-api03-AAAA-BBBB-CCCC-DDDD ` +
	`更多正文 ` + strings.Repeat("x", 300)

func TestParseSummaryResponse_NotJSON_NoRawEcho(t *testing.T) {
	s := &Summarizer{}
	_, err := s.parseSummaryResponse(leakyResponse, "test-session-key")
	if err == nil {
		t.Fatalf("非 JSON 响应必须返回错误")
	}
	msg := err.Error()
	for _, fragment := range []string{"张三", "sk-ant-api03", "AAAA-BBBB", "会话内容", "以下是"} {
		if strings.Contains(msg, fragment) {
			t.Fatalf("错误文本泄漏了响应原文片段 %q：\n%s", fragment, msg)
		}
	}
	// 形态描述必须在场（可定位解析阶段）。
	for _, want := range []string{"response_shape", "runes="} {
		if !strings.Contains(msg, want) {
			t.Fatalf("错误文本缺少形态描述 %q：\n%s", want, msg)
		}
	}
}

func TestParseSummaryResponse_MalformedJSON_NoRawEcho(t *testing.T) {
	// 形态合法（{} 开头）但内容含敏感片段且解析失败（trailing garbage 前置冒号错）。
	malformed := `{ "title": "张三的密钥是 sk-ant-api03-XXXX", summary: 没有引号的值 }`
	s := &Summarizer{}
	_, err := s.parseSummaryResponse(malformed, "test-session-key")
	if err == nil {
		t.Fatalf("坏 JSON 必须返回错误")
	}
	msg := err.Error()
	for _, fragment := range []string{"张三", "sk-ant-api03", "没有引号的值"} {
		if strings.Contains(msg, fragment) {
			t.Fatalf("错误文本泄漏了 extracted 原文片段 %q：\n%s", fragment, msg)
		}
	}
	// 新口径：extracted 只回形态（runes / first_key 长度）。
	if !strings.Contains(msg, "extracted_shape") {
		t.Fatalf("错误文本缺少 extracted_shape 形态描述：\n%s", msg)
	}
}

func TestFirstRuneClass(t *testing.T) {
	cases := []struct{ in, want string }{
		{"{\"a\":1}", "symbol"},
		{"  json", "letter"},
		{"42 是答案", "digit"},
		{"", "empty"},
		{"   ", "empty"},
	}
	for _, c := range cases {
		if got := firstRuneClass(c.in); got != c.want {
			t.Errorf("firstRuneClass(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestExtractedFirstKey(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{`{ "title": "x"`, "len=5"},            // 正常键 → 只回长度
		{`{"密集键名": 1}`, "-"},                   // 非 ASCII 键名 → 不回内容
		{`{no-quote-here`, "-"},                // 无引号 → "-"
		{`[1,2,3]`, "-"},                       // 非 object → "-"
		{fmt.Sprintf(`{"k%0129d":1}`, 1), "-"}, // 键名 130 > 128 上限 → "-"
	}
	for _, c := range cases {
		if got := extractedFirstKey(c.in); got != c.want {
			t.Errorf("extractedFirstKey(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
