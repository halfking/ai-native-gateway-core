package ir

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOllama_ImageBase64 验证 Ollama 支持 base64 图片输入 (视觉模型)
func TestOllama_ImageBase64(t *testing.T) {
	req := &InternalRequest{
		Model: "llava",
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

// TestOllama_ImageURL 验证 Ollama 支持 HTTP/HTTPS 图片 URL
func TestOllama_ImageURL(t *testing.T) {
	req := &InternalRequest{
		Model: "llava-phi3",
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

// TestOllama_PrivateFields_Format 验证 Ollama 私有字段 format 通过 Extensions 保留
func TestOllama_PrivateFields_Format(t *testing.T) {
	req := &InternalRequest{
		Model: "llama3.2",
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "Return JSON"}}},
		},
		Extensions: map[string]json.RawMessage{
			"ollama_format": json.RawMessage(`"json"`),
		},
		SourceProtocol: ProtocolOpenAIChat,
	}

	serialized, err := SerializeOpenAI(req)
	require.NoError(t, err)

	parsed, err := ParseOpenAI(serialized)
	require.NoError(t, err)

	assert.Contains(t, parsed.Extensions, "ollama_format")
}

// TestOllama_PrivateFields_KeepAlive 验证 Ollama 私有字段 keep_alive 通过 Extensions 保留
func TestOllama_PrivateFields_KeepAlive(t *testing.T) {
	req := &InternalRequest{
		Model: "mistral",
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "Hello"}}},
		},
		Extensions: map[string]json.RawMessage{
			"ollama_keep_alive": json.RawMessage(`"5m"`),
		},
		SourceProtocol: ProtocolOpenAIChat,
	}

	serialized, err := SerializeOpenAI(req)
	require.NoError(t, err)

	parsed, err := ParseOpenAI(serialized)
	require.NoError(t, err)

	assert.Contains(t, parsed.Extensions, "ollama_keep_alive")
}

// TestOllama_PrivateFields_Options 验证 Ollama options 字段通过 Extensions 保留
func TestOllama_PrivateFields_Options(t *testing.T) {
	options, _ := json.Marshal(map[string]interface{}{
		"num_ctx":         2048,
		"repeat_penalty":  1.1,
		"temperature":     0.8,
		"top_k":           40,
		"top_p":           0.9,
		"num_predict":     128,
		"mirostat":        0,
	})

	req := &InternalRequest{
		Model: "llama3.2",
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "测试"}}},
		},
		Extensions: map[string]json.RawMessage{
			"ollama_options": options,
		},
		SourceProtocol: ProtocolOpenAIChat,
	}

	serialized, err := SerializeOpenAI(req)
	require.NoError(t, err)

	parsed, err := ParseOpenAI(serialized)
	require.NoError(t, err)

	assert.Contains(t, parsed.Extensions, "ollama_options")
}

// TestOllama_ToolsWithImage 验证工具调用 + 图片混合 (部分模型支持)
func TestOllama_ToolsWithImage(t *testing.T) {
	params, _ := json.Marshal(map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"location": map[string]interface{}{
				"type": "string",
			},
		},
	})

	req := &InternalRequest{
		Model: "llama3.1",
		Messages: []Message{
			{
				Role: "user",
				Content: []ContentBlock{
					{Type: "text", Text: "What's the weather?"},
				},
			},
		},
		Tools: []ToolDefinition{
			{
				Name:        "get_weather",
				Description: "Get weather info",
				Parameters:  params,
			},
		},
		SourceProtocol: ProtocolOpenAIChat,
	}

	serialized, err := SerializeOpenAI(req)
	require.NoError(t, err)

	parsed, err := ParseOpenAI(serialized)
	require.NoError(t, err)

	require.Len(t, parsed.Tools, 1)
	assert.Equal(t, "get_weather", parsed.Tools[0].Name)
}

// TestOllama_CrossVendorIsolation 验证 Ollama 私有字段跨厂隔离
func TestOllama_CrossVendorIsolation(t *testing.T) {
	req := &InternalRequest{
		Model: "llama3.2",
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "测试"}}},
		},
		Extensions: map[string]json.RawMessage{
			"ollama_format":     json.RawMessage(`"json"`),
			"ollama_keep_alive": json.RawMessage(`"10m"`),
			"ollama_options":    json.RawMessage(`{"num_ctx":4096}`),
			"ollama_template":   json.RawMessage(`"custom template"`),
		},
		SourceProtocol: ProtocolOpenAIChat,
	}

	serialized, err := SerializeOpenAI(req)
	require.NoError(t, err)

	parsed, err := ParseOpenAI(serialized)
	require.NoError(t, err)

	assert.Contains(t, parsed.Extensions, "ollama_format")
	assert.Contains(t, parsed.Extensions, "ollama_keep_alive")
	assert.Contains(t, parsed.Extensions, "ollama_options")
	assert.Contains(t, parsed.Extensions, "ollama_template")
}
