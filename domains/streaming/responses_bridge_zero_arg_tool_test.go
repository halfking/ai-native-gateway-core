// responses_bridge_zero_arg_tool_test.go —— 零参工具调用的终态 arguments 必须是**合法 JSON**
// （★ 2026-11-11 审计 F-A1 的钉测）
//
// ★ 缺陷：★ 根修 `9381d13a4` 把 anthropic `content_block_start` 里的 `{}` **占位符**排除掉
// ★ ★（★ internal/ir/stream.go:587）★★ ⇒ ★★ **零参工具调用**的终态 arguments 变成**空串**
// ★ ★⇒ ★★ `json.Valid("")` = **false** ⇒ ★★ 客户端 `JSON.parse(arguments)` 抛错 ⇒ ★★ **工具不可用**
// ★ ★⇒ ★★ 而**修复前**同一输入得到 `"{}"`（★ 合法）★★ ⇒ ★★ 这是本轮引入的**回归**。
// ★ ★⇒ ★★ 且守卫生不了它（★ domains/hooks/outputcompliance 对空串直接 `continue`）。
//
// ★ 这条门守的是**不变量**，★ 而不是某条具体路径：
// ★ ★⇒ ★★ 「任何终态 `arguments` 都必须是合法 JSON」⇒ ★★ 不管空串是 anthropic 占位符、
// ★ ★ 还是 chat 上游的空 delta 造成的，★ ★ 这条门都会红。

package streaming

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestResponsesBridgeZeroArgToolArgumentsAreValidJSON 钉住 F-A1。
//
// ★ 不变量（★ 只管**终态**帧）：★ `response.output_item.done` /
// ★ `response.function_call_arguments.done` / `response.completed.output[]` 里的
// ★ `arguments` 必须是**合法 JSON**。
//
// ★★ **为什么 `response.output_item.added` 不在管辖内**：★ 它是**进行中**的 item，
// ★★ 它的 `arguments` 按 OpenAI 协议就是**空串**（★ 实测 `internal/ir/stream.go:1266`
// ★★ 自 2026-07-01 起**写死** `""`，★ 本窗口**未改**）★★ ⇒ ★★ 真正的参数走
// ★★ `response.function_call_arguments.delta` 逐片送达 ⇒ ★★ 该帧不是缺陷。
// ★★ ⇒ ★★ **断言必须落到「终态帧」**，★ 否则会把**合规**的进行中帧判成红。
func TestResponsesBridgeZeroArgToolArgumentsAreValidJSON(t *testing.T) {
	// ★ 零参工具调用：★ 上游**不携带任何参数**（★ arguments 全程为空串）
	frames := strings.Join([]string{
		`data: {"id":"chatcmpl-zeroarg","object":"chat.completion.chunk","created":1792039047,"model":"claude-sonnet-5","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_zero","type":"function","function":{"name":"get_time","arguments":""}}]}}]}` + "\n",
		`data: {"id":"chatcmpl-zeroarg","object":"chat.completion.chunk","created":1792039047,"model":"claude-sonnet-5","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":""}}]}}]}}` + "\n",
		`data: {"id":"chatcmpl-zeroarg","object":"chat.completion.chunk","created":1792039047,"model":"claude-sonnet-5","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":100,"completion_tokens":5,"total_tokens":105}}` + "\n",
		"\n",
	}, "")

	resp := &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(frames)), Header: http.Header{}}
	rec := httptest.NewRecorder()
	outcome := StreamOpenAIToResponsesSSE(t.Context(), rec, resp, "claude-sonnet-5", "claude-sonnet-5", "req-zeroarg", nil, nil)
	body := rec.Body.String()

	if outcome.Interrupted {
		t.Fatalf("bridge interrupted: %+v", outcome)
	}
	if !strings.Contains(body, `"type":"function_call"`) {
		t.Fatalf("no function_call item frame; body=%s", body)
	}

	// ★ 不变量：★ 每一帧里出现的 `arguments` 都必须是**合法 JSON**
	checked := 0
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var frame map[string]any
		if err := json.Unmarshal([]byte(payload), &frame); err != nil {
			continue // 非 JSON 帧（keep-alive 等）⇒ 跳过
		}
		// ★ 只管**终态**帧（★ 进行中的 `output_item.added` 按协议就是空串）
		switch frame["type"] {
		case "response.output_item.done", "response.function_call_arguments.done", "response.completed":
		default:
			continue
		}
		for _, args := range collectArgumentFields(frame) {
			checked++
			if !json.Valid([]byte(args)) {
				t.Fatalf("terminal arguments must be valid JSON, got %q (frame=%s)", args, payload)
			}
		}
	}
	if checked == 0 {
		t.Fatalf("no terminal arguments field observed at all (★ ⇒ 断言无意义); body=%s", body)
	}
}

// collectArgumentFields 从一帧里收集所有 `arguments` 字段（含嵌套 item / output[]）。
func collectArgumentFields(frame map[string]any) []string {
	var out []string
	var walk func(any)
	walk = func(v any) {
		switch node := v.(type) {
		case map[string]any:
			for k, child := range node {
				if k == "arguments" {
					if s, ok := child.(string); ok {
						out = append(out, s)
					}
					continue
				}
				walk(child)
			}
		case []any:
			for _, child := range node {
				walk(child)
			}
		}
	}
	walk(frame)
	return out
}
