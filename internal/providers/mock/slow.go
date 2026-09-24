// slow.go —— mock-slow 供应商：模拟真实上游延迟 800ms±200ms 抖动
// （docs/design/2026-09-23-mock-probe-channel §3.4）。用于验证网关对
// 慢上游的流式/超时处理路径。
package mock

import (
	"net/http"
	"time"
)

// CodeSlow 是 mock-slow 供应商 code。
const CodeSlow = "mock-slow"

// SlowDelayBase / SlowDelayJitterMs 定义 slow 的延迟分布：base±jitter
// 均匀抖动，默认 800ms±200ms（即 [600ms, 1000ms]）。
const (
	SlowDelayBase     = 800 * time.Millisecond
	SlowDelayJitterMs = 200
)

// slowDelay 返回 [600ms, 1000ms] 均匀分布延迟（base 800ms ± 200ms）。
func slowDelay() time.Duration {
	jitter := time.Duration(randRange(-SlowDelayJitterMs, SlowDelayJitterMs)) * time.Millisecond
	return SlowDelayBase + jitter
}

// ChatCompletionsSlow 返回 mock-slow 的 OpenAI Chat Completions handler
// （挂 /mock/v1/chat/completions/slow）。
func ChatCompletionsSlow() http.HandlerFunc { return ChatCompletions(CodeSlow, slowDelay) }

// MessagesSlow 返回 mock-slow 的 Anthropic Messages handler
// （挂 /mock/v1/messages/slow）。
func MessagesSlow() http.HandlerFunc { return AnthropicMessages(CodeSlow, slowDelay) }
