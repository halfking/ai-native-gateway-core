package unified

import (
	"errors"
	"fmt"
	"time"
)

// AnthropicAdapter 是 Anthropic 适配器
type AnthropicAdapter struct {
	version string
}

// NewAnthropicAdapter 创建 Anthropic 适配器
func NewAnthropicAdapter() *AnthropicAdapter {
	return &AnthropicAdapter{
		version: "v1.0",
	}
}

// Name 返回提供商名称
func (a *AnthropicAdapter) Name() string {
	return "anthropic"
}

// ToProviderRequest 将统一请求转换为 Anthropic 请求
func (a *AnthropicAdapter) ToProviderRequest(req *UnifiedRequest) (interface{}, error) {
	// Anthropic 特殊处理

	// 1. max_tokens 是必填的
	maxTokens := 4096 // 默认值
	if req.MaxTokens != nil {
		maxTokens = *req.MaxTokens
	}

	// 2. system 消息需要单独提取
	systemMsg, messages := extractSystemMessage(req.Messages)

	// 3. 构造 Anthropic 请求
	anthReq := map[string]interface{}{
		"model":      req.Model,
		"messages":   convertAnthropicMessages(messages),
		"max_tokens": maxTokens,
	}

	if systemMsg != "" {
		anthReq["system"] = systemMsg
	}

	if req.Stream {
		anthReq["stream"] = true
	}

	if req.Temperature != nil {
		anthReq["temperature"] = *req.Temperature
	}

	if req.TopP != nil {
		anthReq["top_p"] = *req.TopP
	}

	if len(req.Stop) > 0 {
		anthReq["stop_sequences"] = req.Stop
	}

	return anthReq, nil
}

// FromProviderResponse 将 Anthropic 响应转换为统一响应
func (a *AnthropicAdapter) FromProviderResponse(resp interface{}) (*UnifiedResponse, error) {
	respMap, ok := resp.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("invalid response type: %T", resp)
	}

	unified := &UnifiedResponse{
		ID:      getString(respMap, "id"),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   getString(respMap, "model"),
	}

	// Anthropic 的响应格式与 OpenAI 不同
	// content 是一个数组，包含 text blocks
	var messageContent string
	if contentData, ok := respMap["content"].([]interface{}); ok {
		for _, block := range contentData {
			if blockMap, ok := block.(map[string]interface{}); ok {
				if blockMap["type"] == "text" {
					messageContent += getString(blockMap, "text")
				}
			}
		}
	}

	unified.Choices = []Choice{
		{
			Index: 0,
			Message: Message{
				Role:    "assistant",
				Content: messageContent,
			},
			FinishReason: mapAnthropicFinishReason(getString(respMap, "stop_reason")),
		},
	}

	// 转换 usage
	if usageData, ok := respMap["usage"].(map[string]interface{}); ok {
		unified.Usage = Usage{
			PromptTokens:     getInt(usageData, "input_tokens"),
			CompletionTokens: getInt(usageData, "output_tokens"),
			TotalTokens:      getInt(usageData, "input_tokens") + getInt(usageData, "output_tokens"),
		}
	}

	return unified, nil
}

// SupportedModels 返回支持的模型列表
func (a *AnthropicAdapter) SupportedModels() []string {
	return []string{
		"claude-3-5-sonnet-20241022",
		"claude-3-5-haiku-20241022",
		"claude-3-opus-20240229",
		"claude-3-sonnet-20240229",
		"claude-3-haiku-20240307",
		"claude-2.1",
		"claude-2.0",
	}
}

// ValidateRequest 验证请求参数
func (a *AnthropicAdapter) ValidateRequest(req *UnifiedRequest) error {
	if req.Model == "" {
		return errors.New("model is required")
	}
	if len(req.Messages) == 0 {
		return errors.New("messages cannot be empty")
	}

	// Anthropic 要求第一条消息必须是 user（除了 system）
	nonSystemMessages := filterSystemMessages(req.Messages)
	if len(nonSystemMessages) > 0 && nonSystemMessages[0].Role != "user" {
		return errors.New("first message must be from user")
	}

	return nil
}

// extractSystemMessage 提取 system 消息
func extractSystemMessage(messages []Message) (string, []Message) {
	var systemMsg string
	var filtered []Message

	for _, msg := range messages {
		if msg.Role == "system" {
			if content, ok := msg.Content.(string); ok {
				if systemMsg != "" {
					systemMsg += "\n\n"
				}
				systemMsg += content
			}
		} else {
			filtered = append(filtered, msg)
		}
	}

	return systemMsg, filtered
}

// filterSystemMessages 过滤掉 system 消息
func filterSystemMessages(messages []Message) []Message {
	var filtered []Message
	for _, msg := range messages {
		if msg.Role != "system" {
			filtered = append(filtered, msg)
		}
	}
	return filtered
}

// convertAnthropicMessages 转换为 Anthropic 消息格式
func convertAnthropicMessages(messages []Message) []map[string]interface{} {
	result := make([]map[string]interface{}, len(messages))
	for i, msg := range messages {
		m := map[string]interface{}{
			"role": msg.Role,
		}

		// Anthropic 的 content 格式
		if content, ok := msg.Content.(string); ok {
			m["content"] = content
		} else {
			m["content"] = msg.Content
		}

		result[i] = m
	}
	return result
}

// mapAnthropicFinishReason 映射 Anthropic 的 finish_reason
func mapAnthropicFinishReason(stopReason string) string {
	switch stopReason {
	case "end_turn":
		return "stop"
	case "max_tokens":
		return "length"
	case "stop_sequence":
		return "stop"
	default:
		return stopReason
	}
}
