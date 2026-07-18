package unified

import (
	"errors"
	"fmt"
)

// OpenAIAdapter 是 OpenAI 适配器
type OpenAIAdapter struct {
	version string
}

// NewOpenAIAdapter 创建 OpenAI 适配器
func NewOpenAIAdapter() *OpenAIAdapter {
	return &OpenAIAdapter{
		version: "v1.0",
	}
}

// Name 返回提供商名称
func (a *OpenAIAdapter) Name() string {
	return "openai"
}

// ToProviderRequest 将统一请求转换为 OpenAI 请求
func (a *OpenAIAdapter) ToProviderRequest(req *UnifiedRequest) (interface{}, error) {
	// OpenAI 格式基本与 Unified 一致，直接转换
	oaiReq := map[string]interface{}{
		"model":    req.Model,
		"messages": convertMessages(req.Messages),
	}

	if req.Stream {
		oaiReq["stream"] = true
	}

	if req.MaxTokens != nil {
		oaiReq["max_tokens"] = *req.MaxTokens
	}

	if req.Temperature != nil {
		oaiReq["temperature"] = *req.Temperature
	}

	if req.TopP != nil {
		oaiReq["top_p"] = *req.TopP
	}

	if req.PresencePenalty != nil {
		oaiReq["presence_penalty"] = *req.PresencePenalty
	}

	if req.FrequencyPenalty != nil {
		oaiReq["frequency_penalty"] = *req.FrequencyPenalty
	}

	if len(req.Stop) > 0 {
		oaiReq["stop"] = req.Stop
	}

	if req.ResponseFormat != nil {
		oaiReq["response_format"] = convertResponseFormat(req.ResponseFormat)
	}

	if len(req.Tools) > 0 {
		oaiReq["tools"] = convertTools(req.Tools)
	}

	if req.ToolChoice != nil {
		oaiReq["tool_choice"] = req.ToolChoice
	}

	if req.User != "" {
		oaiReq["user"] = req.User
	}

	return oaiReq, nil
}

// FromProviderResponse 将 OpenAI 响应转换为统一响应
func (a *OpenAIAdapter) FromProviderResponse(resp interface{}) (*UnifiedResponse, error) {
	respMap, ok := resp.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("invalid response type: %T", resp)
	}

	unified := &UnifiedResponse{
		ID:      getString(respMap, "id"),
		Object:  getString(respMap, "object"),
		Created: getInt64(respMap, "created"),
		Model:   getString(respMap, "model"),
	}

	// 转换 choices
	if choicesData, ok := respMap["choices"].([]interface{}); ok {
		unified.Choices = make([]Choice, len(choicesData))
		for i, choiceData := range choicesData {
			if choiceMap, ok := choiceData.(map[string]interface{}); ok {
				unified.Choices[i] = convertChoice(choiceMap)
			}
		}
	}

	// 转换 usage
	if usageData, ok := respMap["usage"].(map[string]interface{}); ok {
		unified.Usage = convertUsage(usageData)
	}

	return unified, nil
}

// SupportedModels 返回支持的模型列表
func (a *OpenAIAdapter) SupportedModels() []string {
	return []string{
		"gpt-4",
		"gpt-4-turbo",
		"gpt-4o",
		"gpt-4o-mini",
		"gpt-3.5-turbo",
		"gpt-3.5-turbo-16k",
	}
}

// ValidateRequest 验证请求参数
func (a *OpenAIAdapter) ValidateRequest(req *UnifiedRequest) error {
	if req.Model == "" {
		return errors.New("model is required")
	}
	if len(req.Messages) == 0 {
		return errors.New("messages cannot be empty")
	}
	return nil
}

// Helper functions

func convertMessages(messages []Message) []map[string]interface{} {
	result := make([]map[string]interface{}, len(messages))
	for i, msg := range messages {
		m := map[string]interface{}{
			"role":    msg.Role,
			"content": msg.Content,
		}
		if msg.Name != "" {
			m["name"] = msg.Name
		}
		if len(msg.ToolCalls) > 0 {
			m["tool_calls"] = msg.ToolCalls
		}
		if msg.ToolCallID != "" {
			m["tool_call_id"] = msg.ToolCallID
		}
		result[i] = m
	}
	return result
}

func convertResponseFormat(rf *ResponseFormat) map[string]interface{} {
	result := map[string]interface{}{
		"type": rf.Type,
	}
	if rf.JSONSchema != nil {
		result["json_schema"] = rf.JSONSchema
	}
	return result
}

func convertTools(tools []Tool) []map[string]interface{} {
	result := make([]map[string]interface{}, len(tools))
	for i, tool := range tools {
		result[i] = map[string]interface{}{
			"type":     tool.Type,
			"function": tool.Function,
		}
	}
	return result
}

func convertChoice(choiceMap map[string]interface{}) Choice {
	choice := Choice{
		Index:        getInt(choiceMap, "index"),
		FinishReason: getString(choiceMap, "finish_reason"),
	}

	if msgData, ok := choiceMap["message"].(map[string]interface{}); ok {
		choice.Message = Message{
			Role:    getString(msgData, "role"),
			Content: msgData["content"],
		}
		if toolCallsData, ok := msgData["tool_calls"].([]interface{}); ok {
			choice.Message.ToolCalls = convertToolCalls(toolCallsData)
		}
	}

	return choice
}

func convertToolCalls(data []interface{}) []ToolCall {
	result := make([]ToolCall, len(data))
	for i, tcData := range data {
		if tcMap, ok := tcData.(map[string]interface{}); ok {
			result[i] = ToolCall{
				ID:   getString(tcMap, "id"),
				Type: getString(tcMap, "type"),
			}
			if fnData, ok := tcMap["function"].(map[string]interface{}); ok {
				result[i].Function = Function{
					Name:      getString(fnData, "name"),
					Arguments: getString(fnData, "arguments"),
				}
			}
		}
	}
	return result
}

func convertUsage(usageMap map[string]interface{}) Usage {
	return Usage{
		PromptTokens:     getInt(usageMap, "prompt_tokens"),
		CompletionTokens: getInt(usageMap, "completion_tokens"),
		TotalTokens:      getInt(usageMap, "total_tokens"),
	}
}

func getString(m map[string]interface{}, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func getInt(m map[string]interface{}, key string) int {
	if v, ok := m[key].(float64); ok {
		return int(v)
	}
	if v, ok := m[key].(int); ok {
		return v
	}
	return 0
}

func getInt64(m map[string]interface{}, key string) int64 {
	if v, ok := m[key].(float64); ok {
		return int64(v)
	}
	if v, ok := m[key].(int64); ok {
		return v
	}
	return 0
}
