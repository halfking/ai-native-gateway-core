package ir

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMiniMax_ImageBase64 验证 MiniMax 支持 base64 图片输入
func TestMiniMax_ImageBase64(t *testing.T) {
	req := &InternalRequest{
		Model: "abab6.5g-chat",
		Messages: []Message{
			{
				Role: "user",
				Content: []ContentBlock{
					{Type: "text", Text: "描述这张图片"},
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

// TestMiniMax_ImageURL 验证 MiniMax 支持 HTTP/HTTPS 图片 URL
func TestMiniMax_ImageURL(t *testing.T) {
	req := &InternalRequest{
		Model: "abab6.5g-chat",
		Messages: []Message{
			{
				Role: "user",
				Content: []ContentBlock{
					{Type: "text", Text: "What's in this image?"},
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

// TestMiniMax_PrivateFields_BotSetting 验证 MiniMax 私有字段 bot_setting 通过 Extensions 保留
func TestMiniMax_PrivateFields_BotSetting(t *testing.T) {
	botSetting, _ := json.Marshal([]map[string]interface{}{
		{
			"bot_name": "专业助手",
			"content":  "你是一个专业的技术顾问",
		},
	})

	req := &InternalRequest{
		Model: "abab6.5s-chat",
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "测试"}}},
		},
		Extensions: map[string]json.RawMessage{
			"minimax_bot_setting": botSetting,
		},
		SourceProtocol: ProtocolOpenAIChat,
	}

	serialized, err := SerializeOpenAI(req)
	require.NoError(t, err)

	parsed, err := ParseOpenAI(serialized)
	require.NoError(t, err)

	assert.Contains(t, parsed.Extensions, "minimax_bot_setting")
}

// TestMiniMax_PrivateFields_Plugins 验证 MiniMax 私有字段 plugins 通过 Extensions 保留
func TestMiniMax_PrivateFields_Plugins(t *testing.T) {
	plugins, _ := json.Marshal([]string{"plugin:web_search", "plugin:calculator"})

	req := &InternalRequest{
		Model: "abab6.5s-chat",
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "搜索最新新闻"}}},
		},
		Extensions: map[string]json.RawMessage{
			"minimax_plugins": plugins,
		},
		SourceProtocol: ProtocolOpenAIChat,
	}

	serialized, err := SerializeOpenAI(req)
	require.NoError(t, err)

	parsed, err := ParseOpenAI(serialized)
	require.NoError(t, err)

	assert.Contains(t, parsed.Extensions, "minimax_plugins")
}

// TestMiniMax_AnthropicCompatible 验证 MiniMax Anthropic 兼容路径
func TestMiniMax_AnthropicCompatible(t *testing.T) {
	req := &InternalRequest{
		Model: "abab6.5s-chat",
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "你好"}}},
		},
		MaxTokens:      1024,
		SourceProtocol: ProtocolAnthropicMessages,
		TargetProvider: "minimax",
	}

	serialized, err := SerializeAnthropic(req)
	require.NoError(t, err)

	parsed, err := ParseAnthropic(serialized)
	require.NoError(t, err)

	assert.Equal(t, req.Model, parsed.Model)
	assert.Equal(t, req.MaxTokens, parsed.MaxTokens)
}

// TestMiniMax_ToolsWithImage 验证工具调用 + 图片混合场景
func TestMiniMax_ToolsWithImage(t *testing.T) {
	params, _ := json.Marshal(map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"description": map[string]interface{}{
				"type": "string",
			},
		},
	})

	req := &InternalRequest{
		Model: "abab6.5g-chat",
		Messages: []Message{
			{
				Role: "user",
				Content: []ContentBlock{
					{Type: "image", Image: &ImageSource{Type: "url", URL: "https://example.com/chart.png"}},
					{Type: "text", Text: "描述这张图"},
				},
			},
		},
		Tools: []ToolDefinition{
			{
				Name:        "describe_image",
				Description: "描述图片内容",
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
	assert.Equal(t, "describe_image", parsed.Tools[0].Name)
}

// TestMiniMax_CrossVendorIsolation 验证 MiniMax 私有字段跨厂隔离
func TestMiniMax_CrossVendorIsolation(t *testing.T) {
	req := &InternalRequest{
		Model: "abab6.5s-chat",
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "测试"}}},
		},
		Extensions: map[string]json.RawMessage{
			"minimax_bot_setting":      json.RawMessage(`[{"bot_name":"助手"}]`),
			"minimax_plugins":          json.RawMessage(`["plugin:web_search"]`),
			"minimax_mask_sensitive":   json.RawMessage(`true`),
			"minimax_continous_mode":   json.RawMessage(`true`),
		},
		SourceProtocol: ProtocolOpenAIChat,
	}

	serialized, err := SerializeOpenAI(req)
	require.NoError(t, err)

	parsed, err := ParseOpenAI(serialized)
	require.NoError(t, err)

	// Verify MiniMax fields are in Extensions with minimax_ prefix
	assert.Contains(t, parsed.Extensions, "minimax_bot_setting")
	assert.Contains(t, parsed.Extensions, "minimax_plugins")
	assert.Contains(t, parsed.Extensions, "minimax_mask_sensitive")
	assert.Contains(t, parsed.Extensions, "minimax_continous_mode")
}
