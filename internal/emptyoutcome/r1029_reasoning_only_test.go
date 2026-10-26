package emptyoutcome

import "testing"

// r1029 (2026-10-17): reasoning-only exhaustion — 154 上 glm-5.3 的真实样本。
//
//	request_id=623e36495abf1de578592fa90c78417a
//	upstream: z-ai/glm-5.3-flash, max_tokens=8, stream=true
//	8 个 SSE 帧，全部只带 delta.reasoning_content（"The"/" user"/" has"/…
//	  直到 " ping".），content 一次都没有出现；
//	usage: completion_tokens=8, reasoning_tokens=6
//	finish_reason=length，且上游在 finish_reason 后直接 EOF，从不发 [DONE]。
//
// 判定失误的后果：ChatChunkHasOutput 把 ReasoningContent 计为输出，于是
// sawSemanticOutput=true，空流 gate 认为「已见语义输出」，走 r1028 的
// benign-close 分支补一个 [DONE] 判成功 —— 客户端收到 HTTP 200 +
// finish_reason=length + content 全空，正是 empty_model_response。
//
// 修复口径（owner 裁决 2026-10-17）：agent-visible 输出 = content +
// 具名 tool_calls。reasoning 走 thinking/thinking_delta 通道单独透传，
// 不参与空回复判定 —— 推理模型的思考过程再长也不是助手答案。

func TestR1029ReasoningOnlyStreamChunkIsNotOutput(t *testing.T) {
	// 真实现场逐帧形态（截取自 154 raw_data）。
	reasoningFrames := []string{
		`{"id":"x","created":1,"object":"chat.completion.chunk","model":"glm-5.3","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"The"}}]}`,
		`{"id":"x","created":1,"object":"chat.completion.chunk","model":"glm-5.3","choices":[{"index":0,"delta":{"reasoning_content":" user"}}]}`,
		`{"id":"x","created":1,"object":"chat.completion.chunk","model":"glm-5.3","choices":[{"index":0,"delta":{"reasoning_content":" has"}}]}`,
		`{"id":"x","created":1,"object":"chat.completion.chunk","model":"glm-5.3","choices":[{"index":0,"delta":{"reasoning_content":" sent"}}]}`,
		`{"id":"x","created":1,"object":"chat.completion.chunk","model":"glm-5.3","choices":[{"index":0,"delta":{"reasoning_content":" \"ping\"."}}]}`,
	}
	for i, f := range reasoningFrames {
		if ChatChunkHasOutput(f) {
			t.Errorf("frame %d: reasoning-only chunk must NOT count as output: %s", i, f)
		}
	}

	// finish_reason=length 的收尾帧同样不算输出。
	finishLen := `{"id":"x","created":1,"object":"chat.completion.chunk","model":"glm-5.3","choices":[{"index":0,"delta":{},"finish_reason":"length"}]}`
	if ChatChunkHasOutput(finishLen) {
		t.Errorf("finish_reason=length with empty delta must NOT count as output: %s", finishLen)
	}

	// usage 帧不算输出。
	usage := `{"id":"x","object":"chat.completion.chunk","model":"glm-5.3","choices":[],"usage":{"prompt_tokens":13,"completion_tokens":8,"total_tokens":21,"completion_tokens_details":{"reasoning_tokens":6}}}`
	if ChatChunkHasOutput(usage) {
		t.Errorf("usage-only frame must NOT count as output: %s", usage)
	}
}

// 反向判据：真实答案（含 content）必须仍然算输出，否则这条修复会把正常
// 推理模型打成空回复 —— 那是从一个故障换另一个故障。
func TestR1029ContentAfterReasoningStillCounts(t *testing.T) {
	// 推理若干帧后才吐 content 的正常形态。
	contentFrame := `{"id":"x","created":1,"object":"chat.completion.chunk","model":"glm-5.3","choices":[{"index":0,"delta":{"content":"pong"}}]}`
	if !ChatChunkHasOutput(contentFrame) {
		t.Errorf("plain content chunk MUST count as output: %s", contentFrame)
	}
	// 同一帧内 reasoning 与 content 并存。
	bothFrame := `{"id":"x","created":1,"object":"chat.completion.chunk","model":"glm-5.3","choices":[{"index":0,"delta":{"reasoning_content":"thinking","content":"answer"}}]}`
	if !ChatChunkHasOutput(bothFrame) {
		t.Errorf("frame carrying content MUST count as output even with reasoning: %s", bothFrame)
	}
	// 具名 tool_calls 仍算输出。
	toolFrame := `{"id":"x","created":1,"object":"chat.completion.chunk","model":"glm-5.3","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"search","arguments":"{}"}}]}}]}`
	if !ChatChunkHasOutput(toolFrame) {
		t.Errorf("named tool_call MUST count as output: %s", toolFrame)
	}
	// 纯 arguments 增量（name 上一帧已到）仍算输出。
	argsFrame := `{"id":"x","created":1,"object":"chat.completion.chunk","model":"glm-5.3","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"q\":1}"}}]}}]}`
	if !ChatChunkHasOutput(argsFrame) {
		t.Errorf("tool_call arguments delta MUST count as output: %s", argsFrame)
	}
}

// 非流式腿同口径：reasoning-only 的 chat body 必须判空。
func TestR1029ReasoningOnlyNonStreamBodyIsEmpty(t *testing.T) {
	body := `{"choices":[{"index":0,"message":{"role":"assistant","reasoning_content":"The user has sent just \"ping\"."},"finish_reason":"length"}],"usage":{"prompt_tokens":13,"completion_tokens":8,"completion_tokens_details":{"reasoning_tokens":6}}}`
	if !ChatBodyIsEmpty([]byte(body)) {
		t.Errorf("reasoning-only non-stream body MUST be classified empty: %s", body)
	}
	if _, empty := ClassifyBody([]byte(body)); !empty {
		t.Errorf("ClassifyBody MUST report empty for reasoning-only body: %s", body)
	}
}

// 反向：非流式正常答案不被误伤。
func TestR1029NonStreamContentStillNotEmpty(t *testing.T) {
	cases := []string{
		`{"choices":[{"message":{"role":"assistant","content":"pong"}}]}`,
		`{"choices":[{"message":{"reasoning_content":"think","content":"answer"}}]}`,
		`{"choices":[{"message":{"tool_calls":[{"id":"1","function":{"name":"f","arguments":"{}"}}]}}]}`,
	}
	for _, body := range cases {
		if ChatBodyIsEmpty([]byte(body)) {
			t.Errorf("body carrying real output must NOT be classified empty: %s", body)
		}
	}
}

// 幻觉检测：Anthropic 形态的 thinking block 同样是思考通道，不算助手答案。
// AnthropicContentIsEmpty 已经把 thinking/thinking_delta 排除，这里锁住它。
func TestR1029AnthropicThinkingOnlyIsEmpty(t *testing.T) {
	body := `{"content":[{"type":"thinking","thinking":"let me think about it"}],"stop_reason":"max_tokens"}`
	if _, empty := ClassifyBody([]byte(body)); !empty {
		t.Errorf("Anthropic thinking-only body MUST be classified empty: %s", body)
	}
}

// Responses 腿（{"type":"message","content":[...]}）走另一个分类器
// （ResponsesContentIsEmpty 系），它有自己的 thinking 分支 —— 修 Anthropic
// 那处时漏掉过它，这里单独钉住，防止同一判据在三处漂移。
func TestR1029ResponsesThinkingOnlyIsEmpty(t *testing.T) {
	body := `{"type":"message","content":[{"type":"thinking","thinking":"still deliberating"}],"stop_reason":"max_tokens"}`
	if _, empty := ClassifyBody([]byte(body)); !empty {
		t.Errorf("Responses thinking-only body MUST be classified empty: %s", body)
	}
}

// 反向：Responses 腿的正常答案不被误伤。
func TestR1029ResponsesRealOutputNotEmpty(t *testing.T) {
	cases := []string{
		`{"type":"message","content":[{"type":"text","text":"pong"}]}`,
		`{"type":"message","content":[{"type":"thinking","thinking":"hmm"},{"type":"text","text":"pong"}]}`,
		`{"type":"message","content":[{"type":"tool_use","id":"t1","name":"f","input":{}}]}`,
	}
	for _, body := range cases {
		if _, empty := ClassifyBody([]byte(body)); empty {
			t.Errorf("Responses body carrying real output must NOT be classified empty: %s", body)
		}
	}
}
