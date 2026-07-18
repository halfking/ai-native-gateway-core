package unified

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOpenAIAdapter_ToProviderRequest 测试 OpenAI 请求转换
func TestOpenAIAdapter_ToProviderRequest(t *testing.T) {
	adapter := NewOpenAIAdapter()

	maxTokens := 100
	temperature := 0.7

	req := &UnifiedRequest{
		Model: "gpt-4",
		Messages: []Message{
			{Role: "user", Content: "Hello"},
		},
		MaxTokens:   &maxTokens,
		Temperature: &temperature,
	}

	result, err := adapter.ToProviderRequest(req)
	require.NoError(t, err)

	reqMap := result.(map[string]interface{})
	assert.Equal(t, "gpt-4", reqMap["model"])
	assert.Equal(t, 100, reqMap["max_tokens"])
	assert.Equal(t, 0.7, reqMap["temperature"])

	messages := reqMap["messages"].([]map[string]interface{})
	assert.Len(t, messages, 1)
	assert.Equal(t, "user", messages[0]["role"])
	assert.Equal(t, "Hello", messages[0]["content"])
}

// TestOpenAIAdapter_FromProviderResponse 测试 OpenAI 响应转换
func TestOpenAIAdapter_FromProviderResponse(t *testing.T) {
	adapter := NewOpenAIAdapter()

	resp := map[string]interface{}{
		"id":      "chatcmpl-123",
		"object":  "chat.completion",
		"created": int64(1234567890),
		"model":   "gpt-4",
		"choices": []interface{}{
			map[string]interface{}{
				"index": 0,
				"message": map[string]interface{}{
					"role":    "assistant",
					"content": "Hello! How can I help?",
				},
				"finish_reason": "stop",
			},
		},
		"usage": map[string]interface{}{
			"prompt_tokens":     10,
			"completion_tokens": 20,
			"total_tokens":      30,
		},
	}

	result, err := adapter.FromProviderResponse(resp)
	require.NoError(t, err)

	assert.Equal(t, "chatcmpl-123", result.ID)
	assert.Equal(t, "gpt-4", result.Model)
	assert.Len(t, result.Choices, 1)
	assert.Equal(t, "assistant", result.Choices[0].Message.Role)
	assert.Equal(t, "Hello! How can I help?", result.Choices[0].Message.Content)
	assert.Equal(t, "stop", result.Choices[0].FinishReason)
	assert.Equal(t, 10, result.Usage.PromptTokens)
	assert.Equal(t, 20, result.Usage.CompletionTokens)
	assert.Equal(t, 30, result.Usage.TotalTokens)
}

// TestAnthropicAdapter_ExtractSystemMessage 测试 system 消息提取
func TestAnthropicAdapter_ExtractSystemMessage(t *testing.T) {
	adapter := NewAnthropicAdapter()

	maxTokens := 100
	req := &UnifiedRequest{
		Model: "claude-3-opus-20240229",
		Messages: []Message{
			{Role: "system", Content: "You are helpful"},
			{Role: "user", Content: "Hello"},
		},
		MaxTokens: &maxTokens,
	}

	result, err := adapter.ToProviderRequest(req)
	require.NoError(t, err)

	reqMap := result.(map[string]interface{})
	assert.Equal(t, "You are helpful", reqMap["system"])

	messages := reqMap["messages"].([]map[string]interface{})
	assert.Len(t, messages, 1)
	assert.Equal(t, "user", messages[0]["role"])
}

// TestAnthropicAdapter_FromProviderResponse 测试 Anthropic 响应转换
func TestAnthropicAdapter_FromProviderResponse(t *testing.T) {
	adapter := NewAnthropicAdapter()

	resp := map[string]interface{}{
		"id":    "msg_123",
		"model": "claude-3-opus-20240229",
		"content": []interface{}{
			map[string]interface{}{
				"type": "text",
				"text": "Hello! How can I assist you?",
			},
		},
		"stop_reason": "end_turn",
		"usage": map[string]interface{}{
			"input_tokens":  10,
			"output_tokens": 20,
		},
	}

	result, err := adapter.FromProviderResponse(resp)
	require.NoError(t, err)

	assert.Equal(t, "msg_123", result.ID)
	assert.Equal(t, "claude-3-opus-20240229", result.Model)
	assert.Len(t, result.Choices, 1)
	assert.Equal(t, "assistant", result.Choices[0].Message.Role)
	assert.Equal(t, "Hello! How can I assist you?", result.Choices[0].Message.Content)
	assert.Equal(t, "stop", result.Choices[0].FinishReason)
	assert.Equal(t, 10, result.Usage.PromptTokens)
	assert.Equal(t, 20, result.Usage.CompletionTokens)
	assert.Equal(t, 30, result.Usage.TotalTokens)
}

// TestAnthropicAdapter_ValidateRequest 测试请求验证
func TestAnthropicAdapter_ValidateRequest(t *testing.T) {
	adapter := NewAnthropicAdapter()

	tests := []struct {
		name    string
		req     *UnifiedRequest
		wantErr bool
	}{
		{
			name: "valid request",
			req: &UnifiedRequest{
				Model: "claude-3-opus",
				Messages: []Message{
					{Role: "user", Content: "Hello"},
				},
			},
			wantErr: false,
		},
		{
			name: "missing model",
			req: &UnifiedRequest{
				Messages: []Message{
					{Role: "user", Content: "Hello"},
				},
			},
			wantErr: true,
		},
		{
			name: "empty messages",
			req: &UnifiedRequest{
				Model:    "claude-3-opus",
				Messages: []Message{},
			},
			wantErr: true,
		},
		{
			name: "first message not user",
			req: &UnifiedRequest{
				Model: "claude-3-opus",
				Messages: []Message{
					{Role: "assistant", Content: "Hello"},
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := adapter.ValidateRequest(tt.req)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestRegistry_RegisterAndGet 测试注册表
func TestRegistry_RegisterAndGet(t *testing.T) {
	registry := NewRegistry()

	adapter := NewOpenAIAdapter()
	registry.Register(adapter)

	// 获取已注册的 adapter
	retrieved, err := registry.Get("openai")
	require.NoError(t, err)
	assert.Equal(t, "openai", retrieved.Name())

	// 获取不存在的 adapter
	_, err = registry.Get("nonexistent")
	assert.Error(t, err)
}

// TestRegistry_List 测试列出所有 adapter
func TestRegistry_List(t *testing.T) {
	registry := NewRegistry()

	registry.Register(NewOpenAIAdapter())
	registry.Register(NewAnthropicAdapter())

	names := registry.List()
	assert.Len(t, names, 2)
	assert.Contains(t, names, "openai")
	assert.Contains(t, names, "anthropic")
}

// TestRegistry_Has 测试检查 adapter 存在性
func TestRegistry_Has(t *testing.T) {
	registry := NewRegistry()
	registry.Register(NewOpenAIAdapter())

	assert.True(t, registry.Has("openai"))
	assert.False(t, registry.Has("nonexistent"))
}

// TestRegistry_Unregister 测试注销 adapter
func TestRegistry_Unregister(t *testing.T) {
	registry := NewRegistry()
	registry.Register(NewOpenAIAdapter())

	assert.True(t, registry.Has("openai"))

	err := registry.Unregister("openai")
	require.NoError(t, err)

	assert.False(t, registry.Has("openai"))

	// 注销不存在的 adapter
	err = registry.Unregister("nonexistent")
	assert.Error(t, err)
}

// TestGlobalRegistry 测试全局注册表
func TestGlobalRegistry(t *testing.T) {
	// 全局注册表在 init 时已注册 OpenAI 和 Anthropic
	adapters := ListAdapters()
	assert.GreaterOrEqual(t, len(adapters), 2)
	assert.True(t, HasAdapter("openai"))
	assert.True(t, HasAdapter("anthropic"))
}

// TestValidateAndGetAdapter 测试验证并获取 adapter
func TestValidateAndGetAdapter(t *testing.T) {
	req := &UnifiedRequest{
		Model: "gpt-4",
		Messages: []Message{
			{Role: "user", Content: "Hello"},
		},
	}

	adapter, err := ValidateAndGetAdapter("openai", req)
	require.NoError(t, err)
	assert.Equal(t, "openai", adapter.Name())

	// 无效请求
	invalidReq := &UnifiedRequest{
		Model: "",
	}
	_, err = ValidateAndGetAdapter("openai", invalidReq)
	assert.Error(t, err)

	// 不存在的 provider
	_, err = ValidateAndGetAdapter("nonexistent", req)
	assert.Error(t, err)
}

// TestAdapter_RoundTrip 测试请求-响应往返
func TestAdapter_RoundTrip(t *testing.T) {
	tests := []struct {
		name     string
		adapter  Adapter
		model    string
		messages []Message
	}{
		{
			name:    "OpenAI",
			adapter: NewOpenAIAdapter(),
			model:   "gpt-4",
			messages: []Message{
				{Role: "user", Content: "test"},
			},
		},
		{
			name:    "Anthropic",
			adapter: NewAnthropicAdapter(),
			model:   "claude-3-opus",
			messages: []Message{
				{Role: "user", Content: "test"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Unified Request
			req := &UnifiedRequest{
				Model:    tt.model,
				Messages: tt.messages,
			}

			// To Provider
			providerReq, err := tt.adapter.ToProviderRequest(req)
			require.NoError(t, err)
			assert.NotNil(t, providerReq)

			// 验证转换后的请求是 map 类型
			reqMap, ok := providerReq.(map[string]interface{})
			require.True(t, ok)
			assert.Equal(t, tt.model, reqMap["model"])
		})
	}
}
