package ir

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// R72 §3.4 回归：OpenAI 消息的 content 只允许 string / array / null。旧
// switch 只覆盖这三型，content 为 number / object / bool 时**解析成功**、
// 消息内容落空，随后被 validate_and_fix.go 的 removeEmptyMessages 整条
// 删除——请求带着空会话发上游并返回 200，客户端 JSON 缺陷被转成「模型对
// 空输入作答」，计费 / 缓存 / 对话历史全被污染。
//
// 修法：在解析源头直接返回 *ParseError{Kind: KindClientBug}（仓库现成的
// 分类机制，同 parse_error.go / errorsx.FinishReasonVendorFailureKind 形态），
// 让请求在进 fix 层之前就被拒为 4xx 语义。断言要点：
//  1. 三种异常类型都必须解析失败（不再产出空消息）；
//  2. 错误可 errors.As 为 *ParseError 且 Kind == KindClientBug；
//  3. 解析失败即不进 fix 层——不存在「先解析成空、再靠 removeEmptyMessages
//     兜底删掉」的路径（该路径由变异验证守护：撤掉 default 分支本测试转红，
//     且转红形态正是历史缺陷「parsed OK + 消息被删」）。
func TestParseOpenAI_ContentNonStringArrayNullIsClientBug(t *testing.T) {
	for _, content := range []string{`123`, `{"x":1}`, `true`} {
		body := fmt.Sprintf(`{"model":"gpt-4o","messages":[{"role":"user","content":%s}]}`, content)
		req, err := ParseOpenAI([]byte(body))
		if err != nil {
			var parseErr *ParseError
			if !errors.As(err, &parseErr) {
				t.Fatalf("content=%s: error = %T (%v), want *ParseError", content, err, err)
			}
			if parseErr.Kind != errorsx.KindClientBug {
				t.Fatalf("content=%s: kind = %q, want %q", content, parseErr.Kind, errorsx.KindClientBug)
			}
			continue
		}
		// 历史缺陷形态：解析「成功」但消息无内容，随后被 fix 层整条删除。
		blocks := 0
		if len(req.Messages) > 0 {
			blocks = len(req.Messages[0].Content)
		}
		t.Fatalf("content=%s: ParseOpenAI accepted an illegal content type (msgs=%d blocks=%d) — must be rejected as %s, not silently emptied",
			content, len(req.Messages), blocks, errorsx.KindClientBug)
	}
}

// 合法形态不得被误伤：string / blocks 数组 / 显式 null / 缺省 仍照常解析。
func TestParseOpenAI_LegalContentShapesStillParse(t *testing.T) {
	cases := []string{
		`"hello"`,
		`[{"type":"text","text":"hi"}]`,
		`null`,
	}
	for _, content := range cases {
		body := fmt.Sprintf(`{"model":"gpt-4o","messages":[{"role":"user","content":%s}]}`, content)
		req, err := ParseOpenAI([]byte(body))
		if err != nil {
			t.Fatalf("content=%s: ParseOpenAI: %v", content, err)
		}
		if len(req.Messages) != 1 {
			t.Fatalf("content=%s: msgs = %d, want 1", content, len(req.Messages))
		}
	}
	// content 键整体缺省同样容忍（与既有 case nil 行为一致）。
	body := []byte(`{"model":"gpt-4o","messages":[{"role":"user"}]}`)
	if _, err := ParseOpenAI(body); err != nil {
		t.Fatalf("absent content: ParseOpenAI: %v", err)
	}
}

// 同族缺陷（同构修法）：parse_anthropic.go 的 content switch 同样只有
// string / []any 两型，number / object / bool 静默落空 → 空消息 → 被
// removeEmptyMessages 删除。Anthropic 入向同样必须在源头拒为 KindClientBug。
func TestParseAnthropic_ContentNonStringArrayNullIsClientBug(t *testing.T) {
	for _, content := range []string{`123`, `{"x":1}`, `true`} {
		body := fmt.Sprintf(`{"model":"claude-x","max_tokens":16,"messages":[{"role":"user","content":%s}]}`, content)
		req, err := ParseAnthropic([]byte(body))
		if err != nil {
			var parseErr *ParseError
			if !errors.As(err, &parseErr) {
				t.Fatalf("content=%s: error = %T (%v), want *ParseError", content, err, err)
			}
			if parseErr.Kind != errorsx.KindClientBug {
				t.Fatalf("content=%s: kind = %q, want %q", content, parseErr.Kind, errorsx.KindClientBug)
			}
			continue
		}
		blocks := 0
		if len(req.Messages) > 0 {
			blocks = len(req.Messages[0].Content)
		}
		t.Fatalf("content=%s: ParseAnthropic accepted an illegal content type (msgs=%d blocks=%d) — must be rejected as %s, not silently emptied",
			content, len(req.Messages), blocks, errorsx.KindClientBug)
	}
}

// Anthropic 合法形态不得被误伤：string / blocks 数组 / 缺省（既有容忍行为）
// 仍照常解析。显式 null 与缺省同走既有 content==nil 分支，本轮不收紧。
func TestParseAnthropic_LegalContentShapesStillParse(t *testing.T) {
	cases := []string{
		`"hello"`,
		`[{"type":"text","text":"hi"}]`,
	}
	for _, content := range cases {
		body := fmt.Sprintf(`{"model":"claude-x","max_tokens":16,"messages":[{"role":"user","content":%s}]}`, content)
		req, err := ParseAnthropic([]byte(body))
		if err != nil {
			t.Fatalf("content=%s: ParseAnthropic: %v", content, err)
		}
		if len(req.Messages) != 1 {
			t.Fatalf("content=%s: msgs = %d, want 1", content, len(req.Messages))
		}
	}
	body := []byte(`{"model":"claude-x","max_tokens":16,"messages":[{"role":"user"}]}`)
	if _, err := ParseAnthropic(body); err != nil {
		t.Fatalf("absent content: ParseAnthropic: %v", err)
	}
}

// 错误信息必须可行动：指出坏值的 JSON 类型与所在字段，而不是笼统的
// "parse messages failed"。
func TestParseOpenAI_ClientBugErrorMessageNamesFieldAndType(t *testing.T) {
	body := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":123}]}`)
	_, err := ParseOpenAI(body)
	var parseErr *ParseError
	if !errors.As(err, &parseErr) {
		t.Fatalf("error = %T (%v), want *ParseError", err, err)
	}
	for _, want := range []string{"content", "number"} {
		if !strings.Contains(parseErr.Message, want) {
			t.Errorf("ParseError.Message = %q, want it to mention %q", parseErr.Message, want)
		}
	}
}
