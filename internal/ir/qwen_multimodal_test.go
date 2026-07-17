package ir

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestQwen_ImageBase64 验证 Qwen 支持 base64 图片输入
func TestQwen_ImageBase64(t *testing.T) {
	req := &InternalRequest{
		Model: "qwen-vl-plus",
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

// TestQwen_ImageURL 验证 Qwen 支持 HTTP/HTTPS 图片 URL
func TestQwen_ImageURL(t *testing.T) {
	req := &InternalRequest{
		Model: "qwen-vl-max",
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

// TestQwen_LongContext 验证 Qwen Long 超长上下文
func TestQwen_LongContext(t *testing.T) {
	longText := make([]byte, 10000)
	for i := range longText {
		longText[i] = 'A'
	}

	req := &InternalRequest{
		Model: "qwen-long",
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: string(longText)}}},
		},
		SourceProtocol: ProtocolOpenAIChat,
	}

	serialized, err := SerializeOpenAI(req)
	require.NoError(t, err)

	parsed, err := ParseOpenAI(serialized)
	require.NoError(t, err)

	assert.Equal(t, "qwen-long", parsed.Model)
	assert.Greater(t, len(parsed.Messages[0].Content[0].Text), 5000)
}

// TestQwen_DashScopeFields 验证 Qwen DashScope 特有字段通过 Extensions 保留
func TestQwen_DashScopeFields(t *testing.T) {
	req := &InternalRequest{
		Model: "qwen-plus",
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "最近有什么新闻?"}}},
		},
		Extensions: map[string]json.RawMessage{
			"qwen_enable_search":      json.RawMessage(`true`),
			"qwen_seed":               json.RawMessage(`42`),
			"qwen_repetition_penalty": json.RawMessage(`1.1`),
		},
		SourceProtocol: ProtocolOpenAIChat,
	}

	serialized, err := SerializeOpenAI(req)
	require.NoError(t, err)

	parsed, err := ParseOpenAI(serialized)
	require.NoError(t, err)

	assert.Contains(t, parsed.Extensions, "qwen_enable_search")
	assert.Contains(t, parsed.Extensions, "qwen_seed")
	assert.Contains(t, parsed.Extensions, "qwen_repetition_penalty")
}

// TestQwen_AudioInput 验证 Qwen Audio 音频输入
func TestQwen_AudioInput(t *testing.T) {
	req := &InternalRequest{
		Model: "qwen-audio-turbo",
		Messages: []Message{
			{
				Role: "user",
				Content: []ContentBlock{
					{Type: "audio", Audio: &MediaSource{
						Kind:      "audio",
						Type:      "url",
						URL:       "https://example.com/audio.mp3",
						MediaType: "audio/mp3",
					}},
					{Type: "text", Text: "转录这段音频"},
				},
			},
		},
		SourceProtocol: ProtocolOpenAIChat,
	}

	serialized, err := SerializeOpenAI(req)
	require.NoError(t, err)

	parsed, err := ParseOpenAI(serialized)
	require.NoError(t, err)

	require.Len(t, parsed.Messages[0].Content, 2)
	assert.Equal(t, "audio", parsed.Messages[0].Content[0].Type)
}

// TestQwen_ToolsWithImage 验证工具调用 + 图片混合
func TestQwen_ToolsWithImage(t *testing.T) {
	params, _ := json.Marshal(map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"description": map[string]interface{}{
				"type": "string",
			},
		},
	})

	req := &InternalRequest{
		Model: "qwen-vl-plus",
		Messages: []Message{
			{
				Role: "user",
				Content: []ContentBlock{
					{Type: "image", Image: &ImageSource{Type: "url", URL: "https://example.com/diagram.png"}},
					{Type: "text", Text: "分析这个图"},
				},
			},
		},
		Tools: []ToolDefinition{
			{
				Name:        "analyze_diagram",
				Description: "分析图表",
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

// TestQwen_CrossVendorIsolation 验证 Qwen 私有字段跨厂隔离
func TestQwen_CrossVendorIsolation(t *testing.T) {
	req := &InternalRequest{
		Model: "qwen-plus",
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "测试"}}},
		},
		Extensions: map[string]json.RawMessage{
			"qwen_enable_search":        json.RawMessage(`true`),
			"qwen_seed":                 json.RawMessage(`42`),
			"qwen_repetition_penalty":   json.RawMessage(`1.2`),
			"qwen_result_format":        json.RawMessage(`"message"`),
		},
		SourceProtocol: ProtocolOpenAIChat,
	}

	serialized, err := SerializeOpenAI(req)
	require.NoError(t, err)

	parsed, err := ParseOpenAI(serialized)
	require.NoError(t, err)

	assert.Contains(t, parsed.Extensions, "qwen_enable_search")
	assert.Contains(t, parsed.Extensions, "qwen_seed")
	assert.Contains(t, parsed.Extensions, "qwen_repetition_penalty")
	assert.Contains(t, parsed.Extensions, "qwen_result_format")
}
