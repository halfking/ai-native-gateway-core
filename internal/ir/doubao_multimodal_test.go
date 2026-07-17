package ir

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDoubao_ImageBase64 验证 Doubao 支持 base64 图片输入
func TestDoubao_ImageBase64(t *testing.T) {
	req := &InternalRequest{
		Model: "doubao-vision-pro",
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

// TestDoubao_ImageURL 验证 Doubao 支持 HTTP/HTTPS 图片 URL
func TestDoubao_ImageURL(t *testing.T) {
	req := &InternalRequest{
		Model: "doubao-vision-pro",
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

// TestDoubao_PrivateFields_Plugins 验证 Doubao 私有字段 plugins 通过 Extensions 保留
func TestDoubao_PrivateFields_Plugins(t *testing.T) {
	plugins, _ := json.Marshal([]map[string]interface{}{
		{
			"name": "web_search",
			"config": map[string]interface{}{
				"enable": true,
			},
		},
	})

	req := &InternalRequest{
		Model: "doubao-pro-32k",
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "最近有什么新闻?"}}},
		},
		Extensions: map[string]json.RawMessage{
			"doubao_plugins": plugins,
		},
		SourceProtocol: ProtocolOpenAIChat,
	}

	serialized, err := SerializeOpenAI(req)
	require.NoError(t, err)

	parsed, err := ParseOpenAI(serialized)
	require.NoError(t, err)

	assert.Contains(t, parsed.Extensions, "doubao_plugins")
}

// TestDoubao_PrivateFields_BotID 验证 Doubao 私有字段 bot_id 通过 Extensions 保留
func TestDoubao_PrivateFields_BotID(t *testing.T) {
	req := &InternalRequest{
		Model: "doubao-pro-32k",
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "帮我写文章"}}},
		},
		Extensions: map[string]json.RawMessage{
			"doubao_bot_id": json.RawMessage(`"bot_abc123"`),
		},
		SourceProtocol: ProtocolOpenAIChat,
	}

	serialized, err := SerializeOpenAI(req)
	require.NoError(t, err)

	parsed, err := ParseOpenAI(serialized)
	require.NoError(t, err)

	assert.Contains(t, parsed.Extensions, "doubao_bot_id")
}

// TestDoubao_PrivateFields_CustomBotID 验证 Doubao 私有字段 custom_bot_id 通过 Extensions 保留
func TestDoubao_PrivateFields_CustomBotID(t *testing.T) {
	req := &InternalRequest{
		Model: "doubao-pro-128k",
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "测试"}}},
		},
		Extensions: map[string]json.RawMessage{
			"doubao_custom_bot_id": json.RawMessage(`"custom_bot_xyz789"`),
		},
		SourceProtocol: ProtocolOpenAIChat,
	}

	serialized, err := SerializeOpenAI(req)
	require.NoError(t, err)

	parsed, err := ParseOpenAI(serialized)
	require.NoError(t, err)

	assert.Contains(t, parsed.Extensions, "doubao_custom_bot_id")
}

// TestDoubao_ToolsWithImage 验证工具调用 + 图片混合
func TestDoubao_ToolsWithImage(t *testing.T) {
	params, _ := json.Marshal(map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"description": map[string]interface{}{
				"type": "string",
			},
		},
	})

	req := &InternalRequest{
		Model: "doubao-vision-pro",
		Messages: []Message{
			{
				Role: "user",
				Content: []ContentBlock{
					{Type: "image", Image: &ImageSource{Type: "url", URL: "https://example.com/chart.png"}},
					{Type: "text", Text: "分析这张图"},
				},
			},
		},
		Tools: []ToolDefinition{
			{
				Name:        "analyze_image",
				Description: "分析图片内容",
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

// TestDoubao_CrossVendorIsolation 验证 Doubao 私有字段跨厂隔离
func TestDoubao_CrossVendorIsolation(t *testing.T) {
	req := &InternalRequest{
		Model: "doubao-pro-32k",
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "测试"}}},
		},
		Extensions: map[string]json.RawMessage{
			"doubao_plugins":        json.RawMessage(`[{"name":"web_search"}]`),
			"doubao_bot_id":         json.RawMessage(`"bot_123"`),
			"doubao_custom_bot_id":  json.RawMessage(`"custom_456"`),
			"doubao_logprobs":       json.RawMessage(`true`),
			"doubao_top_logprobs":   json.RawMessage(`5`),
		},
		SourceProtocol: ProtocolOpenAIChat,
	}

	serialized, err := SerializeOpenAI(req)
	require.NoError(t, err)

	parsed, err := ParseOpenAI(serialized)
	require.NoError(t, err)

	assert.Contains(t, parsed.Extensions, "doubao_plugins")
	assert.Contains(t, parsed.Extensions, "doubao_bot_id")
	assert.Contains(t, parsed.Extensions, "doubao_custom_bot_id")
	assert.Contains(t, parsed.Extensions, "doubao_logprobs")
	assert.Contains(t, parsed.Extensions, "doubao_top_logprobs")
}
