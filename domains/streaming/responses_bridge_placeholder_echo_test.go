// responses_bridge_placeholder_echo_test.go —— delta 位置 "{}" 占位符回声的终态归一化钉测
// （★ 2026-11-11 48h 审计 P2）
//
// ★ 缺陷：★ 非标准中继把 content_block_start 的 `{}` 占位符**回声**进
// ★ input_json_delta ⇒ ★ 累计终态 = `{}{...}` 非法 JSON ⇒ ★ 与 9381d13a4 修的
// ★ 原始事故（start 位置占位符）同形态、但发生在 **delta 位置** ⇒ ★
// ★ 211 实测 1 次/24h：`anthropic.block.33.partial_json` 车道被
// ★ builtin.guard.tool_json_invalid fail-closed 拦断（整流中断）。
//
// ★ 修法：★ toolArgumentsJSON 在终态剥掉开头的 `{}` 回声（与
// ★ tooldialect.AnthropicDialect.MergeContinuation 的回声剥离同族）；
// ★ "{}" 单独出现是零参工具合法终态 ⇒ 保留。
//
// ★ 变异臂：★ 去掉 toolArgumentsJSON 里的回声剥离 ⇒ 用例红。

package streaming

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestResponsesBridgePlaceholderEchoTerminalArgsNormalized 钉住 P2：
// chat 上游以 "{}" 作为首个参数增量（回声），随后跟真实参数 ⇒
// 终态 arguments 必须是剥掉回声后的合法 JSON，且整流不中断。
func TestResponsesBridgePlaceholderEchoTerminalArgsNormalized(t *testing.T) {
	frames := strings.Join([]string{
		`data: {"id":"chatcmpl-echo","object":"chat.completion.chunk","created":1792039047,"model":"claude-sonnet-5","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_echo","type":"function","function":{"name":"get_weather","arguments":"{}"}}]}}]}` + "\n",
		`data: {"id":"chatcmpl-echo","object":"chat.completion.chunk","created":1792039047,"model":"claude-sonnet-5","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"city\":\"Tokyo\"}"}}]}}]}` + "\n",
		`data: {"id":"chatcmpl-echo","object":"chat.completion.chunk","created":1792039047,"model":"claude-sonnet-5","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}` + "\n",
		"\n",
	}, "")

	resp := &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(frames)), Header: http.Header{}}
	rec := httptest.NewRecorder()
	outcome := StreamOpenAIToResponsesSSE(t.Context(), rec, resp, "claude-sonnet-5", "claude-sonnet-5", "req-echo", nil, nil)
	body := rec.Body.String()

	if outcome.Interrupted {
		t.Fatalf("bridge interrupted (P2: 回声必须被终态归一化而不是中断): %+v", outcome)
	}
	// ★ 终态必须是剥掉回声后的真实参数
	if !strings.Contains(body, `"arguments":"{\"city\":\"Tokyo\"}"`) {
		t.Fatalf("terminal arguments must be the real args (echo stripped); body=%s", body)
	}
	// ★ 回声拼接出的非法形状不得出现在终态
	if strings.Contains(body, `"arguments":"{}{\"city\"`) {
		t.Fatalf("terminal arguments still carry the {} echo prefix; body=%s", body)
	}
}
