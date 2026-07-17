package ir

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDeepSeek_ImageBase64 验证 DeepSeek 支持 base64 图片输入
func TestDeepSeek_ImageBase64(t *testing.T) {
	req := &InternalRequest{
		Model: "deepseek-chat",
		Messages: []Message{
			{
				Role: "user",
				Content: []ContentBlock{
					{Type: "text", Text: "What's in this image?"},
					{Type: "image", Image: &ImageSource{
						Type:      "base64",
						MediaType: "image/png",
						Data:      "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==",
					}},
				},
			},
		},
		SourceProtocol: ProtocolOpenAIChat,
	}

	serialized, err := SerializeOpenAI(req)
	require.NoError(t, err)

	parsed, err := ParseOpenAI(serialized)
	require.NoError(t, err)

	assert.Equal(t, req.Model, parsed.Model)
	require.Len(t, parsed.Messages[0].Content, 2)
	assert.Equal(t, "image", parsed.Messages[0].Content[1].Type)
}

// TestDeepSeek_ImageURL 验证 DeepSeek 支持 HTTP/HTTPS 图片 URL
func TestDeepSeek_ImageURL(t *testing.T) {
	req := &InternalRequest{
		Model: "deepseek-chat",
		Messages: []Message{
			{
				Role: "user",
				Content: []ContentBlock{
					{Type: "text", Text: "Describe this image"},
					{Type: "image", Image: &ImageSource{
						Type: "url",
						URL:  "https://example.com/image.jpg",
					}},
				},
			},
		},
		SourceProtocol: ProtocolOpenAIChat,
	}

	serialized, err := SerializeOpenAI(req)
	require.NoError(t, err)

	parsed, err := ParseOpenAI(serialized)
	require.NoError(t, err)

	assert.Equal(t, "https://example.com/image.jpg", parsed.Messages[0].Content[1].Image.URL)
}

// TestDeepSeek_ReasoningMode 验证 DeepSeek R1 推理模式
func TestDeepSeek_ReasoningMode(t *testing.T) {
	req := &InternalRequest{
		Model: "deepseek-reasoner",
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "Solve: 2x + 5 = 13"}}},
		},
		Reasoning: &ReasoningConfig{
			Type:   "enabled",
			Effort: "medium",
		},
		SourceProtocol: ProtocolOpenAIChat,
	}

	serialized, err := SerializeOpenAI(req)
	require.NoError(t, err)

	parsed, err := ParseOpenAI(serialized)
	require.NoError(t, err)

	assert.Equal(t, "deepseek-reasoner", parsed.Model)
	assert.NotNil(t, parsed.Reasoning)
}

// TestDeepSeek_PrefixCaching 验证 DeepSeek prefix caching 字段
func TestDeepSeek_PrefixCaching(t *testing.T) {
	req := &InternalRequest{
		Model: "deepseek-chat",
		Messages: []Message{
			{Role: "system", Content: []ContentBlock{{Type: "text", Text: "Long system prompt..."}}},
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "Question"}}},
		},
		Extensions: map[string]json.RawMessage{
			"deepseek_cache_hint": json.RawMessage(`true`),
		},
		SourceProtocol: ProtocolOpenAIChat,
	}

	serialized, err := SerializeOpenAI(req)
	require.NoError(t, err)

	parsed, err := ParseOpenAI(serialized)
	require.NoError(t, err)

	assert.Contains(t, parsed.Extensions, "deepseek_cache_hint")
}

// TestDeepSeek_ToolsWithImage 验证工具调用 + 图片混合
func TestDeepSeek_ToolsWithImage(t *testing.T) {
	params, _ := json.Marshal(map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"analysis": map[string]interface{}{
				"type": "string",
			},
		},
	})

	req := &InternalRequest{
		Model: "deepseek-chat",
		Messages: []Message{
			{
				Role: "user",
				Content: []ContentBlock{
					{Type: "image", Image: &ImageSource{Type: "url", URL: "https://example.com/chart.png"}},
					{Type: "text", Text: "Analyze this chart"},
				},
			},
		},
		Tools: []ToolDefinition{
			{
				Name:        "analyze_data",
				Description: "Analyze data from chart",
				Parameters:  params,
			},
		},
		SourceProtocol: ProtocolOpenAIChat,
	}

	serialized, err := SerializeOpenAI(req)
	require.NoError(t, err)

	parsed, err := ParseOpenAI(serialized)
	require.NoError(t, err)

	require.Len(t, parsed.Messages[0].Content, 2)
	require.Len(t, parsed.Tools, 1)
}

// TestDeepSeek_CrossVendorIsolation 验证 DeepSeek 私有字段跨厂隔离
func TestDeepSeek_CrossVendorIsolation(t *testing.T) {
	req := &InternalRequest{
		Model: "deepseek-chat",
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "测试"}}},
		},
		Extensions: map[string]json.RawMessage{
			"deepseek_cache_hint":         json.RawMessage(`true`),
			"deepseek_max_reasoning_tokens": json.RawMessage(`8000`),
		},
		SourceProtocol: ProtocolOpenAIChat,
	}

	serialized, err := SerializeOpenAI(req)
	require.NoError(t, err)

	parsed, err := ParseOpenAI(serialized)
	require.NoError(t, err)

	assert.Contains(t, parsed.Extensions, "deepseek_cache_hint")
	assert.Contains(t, parsed.Extensions, "deepseek_max_reasoning_tokens")
}
