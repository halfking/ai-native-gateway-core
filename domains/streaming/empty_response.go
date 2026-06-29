package streaming

import "encoding/json"

// isEmptyUpstreamChatResponse 检测非流式 chat completion 响应 body
// 是否为"空响应"——即上游返回了 HTTP 200 但 choices/message 中
// 没有任何实质内容。用于 /v1/messages 和 /v1/responses 的
// writeNonStreamResponse，在检测到空响应时返回 502 而非 200。
//
// 判定条件（全部满足才视为空）：
//  1. body 能成功 JSON 解析
//  2. choices 为空 / 缺失，或
//     choices[0].finish_reason 为空，且
//     choices[0].message 没有 content / reasoning_content / tool_calls
func isEmptyUpstreamChatResponse(body []byte) bool {
	if len(body) == 0 {
		return false // 上游读取失败由 len(body)==0 分支处理
	}

	var chatResp struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
				ToolCalls        []any  `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &chatResp); err != nil {
		return false // 格式异常不归类为空响应
	}

	// choices 缺失或为空 → 空响应
	if len(chatResp.Choices) == 0 {
		return true
	}

	choice := chatResp.Choices[0]

	// 有 finish_reason 且 message 有实质内容 → 不是空响应
	hasContent := choice.Message.Content != "" ||
		choice.Message.ReasoningContent != "" ||
		len(choice.Message.ToolCalls) > 0

	if choice.FinishReason != "" && hasContent {
		return false
	}

	// finish_reason 为空且无任何内容 → 空响应
	if choice.FinishReason == "" && !hasContent {
		return true
	}

	// 有 finish_reason 但无内容，或有内容但无 finish_reason → 空响应
	return !hasContent || choice.FinishReason == ""
}
