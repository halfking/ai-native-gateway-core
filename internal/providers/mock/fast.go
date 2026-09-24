// fast.go —— mock-fast 供应商：零延迟立即返回（docs/design/2026-09-23-
// mock-probe-channel §3.4"fast 立即返回"）。用于验证网关链路的基线时延。
package mock

import (
	"net/http"
	"time"
)

// CodeFast 是 mock-fast 供应商 code（admin 过滤与 auth scope 白名单
// 均以 mock- 前缀/精确 code 识别）。
const CodeFast = "mock-fast"

// fastDelay 恒 0：立即返回。
func fastDelay() time.Duration { return 0 }

// ChatCompletionsFast 返回 mock-fast 的 OpenAI Chat Completions handler
// （挂 /mock/v1/chat/completions/fast）。
func ChatCompletionsFast() http.HandlerFunc { return ChatCompletions(CodeFast, fastDelay) }

// MessagesFast 返回 mock-fast 的 Anthropic Messages handler
// （挂 /mock/v1/messages/fast）。
func MessagesFast() http.HandlerFunc { return AnthropicMessages(CodeFast, fastDelay) }
