package ir

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGLM_ImageBase64 验证 GLM 支持 base64 图片输入
func TestGLM_ImageBase64(t *testing.T) {
	req := &InternalRequest{
		Model: "glm-4v",
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

	// Serialize to OpenAI format (GLM uses OpenAI-compatible)
	serialized, err := SerializeOpenAI(req)
	require.NoError(t, err)

	// Parse back
	parsed, err := ParseOpenAI(serialized)
	require.NoError(t, err)

	// Verify round-trip
	assert.Equal(t, req.Model, parsed.Model)
	require.Len(t, parsed.Messages, 1)
	require.Len(t, parsed.Messages[0].Content, 2)
	assert.Equal(t, "text", parsed.Messages[0].Content[0].Type)
	assert.Equal(t, "描述这张图片", parsed.Messages[0].Content[0].Text)
	assert.Equal(t, "image", parsed.Messages[0].Content[1].Type)
	assert.NotNil(t, parsed.Messages[0].Content[1].Image)
	assert.Contains(t, parsed.Messages[0].Content[1].Image.Data, "iVBORw0KG")
}

// TestGLM_ImageURL 验证 GLM 支持 HTTP/HTTPS 图片 URL
func TestGLM_ImageURL(t *testing.T) {
	req := &InternalRequest{
		Model: "glm-4v-plus",
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

	assert.Equal(t, req.Model, parsed.Model)
	require.Len(t, parsed.Messages[0].Content, 2)
	assert.Equal(t, "image", parsed.Messages[0].Content[1].Type)
	assert.Equal(t, "https://example.com/image.jpg", parsed.Messages[0].Content[1].Image.URL)
}

// TestGLM_ImageOnly 验证仅图片消息(无文本)
func TestGLM_ImageOnly(t *testing.T) {
	req := &InternalRequest{
		Model: "glm-4v",
		Messages: []Message{
			{
				Role: "user",
				Content: []ContentBlock{
					{Type: "image", Image: &ImageSource{
						Type: "url",
						URL:  "https://example.com/diagram.png",
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

	require.Len(t, parsed.Messages[0].Content, 1)
	assert.Equal(t, "image", parsed.Messages[0].Content[0].Type)
	assert.Equal(t, "https://example.com/diagram.png", parsed.Messages[0].Content[0].Image.URL)
}

// TestGLM_ToolsWithImage 验证工具调用 + 图片混合场景
func TestGLM_ToolsWithImage(t *testing.T) {
	params, _ := json.Marshal(map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"chart_type": map[string]interface{}{
				"type":        "string",
				"description": "图表类型",
			},
		},
	})

	req := &InternalRequest{
		Model: "glm-4v",
		Messages: []Message{
			{
				Role: "user",
				Content: []ContentBlock{
					{Type: "image", Image: &ImageSource{Type: "url", URL: "https://example.com/chart.png"}},
					{Type: "text", Text: "分析这张图表的数据"},
				},
			},
		},
		Tools: []ToolDefinition{
			{
				Name:        "analyze_chart",
				Description: "分析图表数据",
				Parameters:  params,
			},
		},
		SourceProtocol: ProtocolOpenAIChat,
	}

	serialized, err := SerializeOpenAI(req)
	require.NoError(t, err)

	parsed, err := ParseOpenAI(serialized)
	require.NoError(t, err)

	// Verify multimodal content preserved
	require.Len(t, parsed.Messages[0].Content, 2)
	assert.Equal(t, "image", parsed.Messages[0].Content[0].Type)
	assert.Equal(t, "text", parsed.Messages[0].Content[1].Type)

	// Verify tools preserved
	require.Len(t, parsed.Tools, 1)
	assert.Equal(t, "analyze_chart", parsed.Tools[0].Name)
}

// TestGLM_ToolResultWithImage 验证工具结果 + 图片响应
func TestGLM_ToolResultWithImage(t *testing.T) {
	req := &InternalRequest{
		Model: "glm-4v",
		Messages: []Message{
			{
				Role:    "user",
				Content: []ContentBlock{{Type: "text", Text: "生成一个图表"}},
			},
			{
				Role:    "assistant",
				Content: []ContentBlock{},
				ToolCalls: []ToolCall{
					{
						ID:   "call_123",
						Type: "function",
						Function: struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						}{
							Name:      "generate_chart",
							Arguments: `{"type": "bar"}`,
						},
					},
				},
			},
			{
				Role:       "tool",
				ToolCallID: "call_123",
				Content: []ContentBlock{
					{Type: "text", Text: "Chart generated: https://example.com/generated.png"},
				},
			},
		},
		SourceProtocol: ProtocolOpenAIChat,
	}

	serialized, err := SerializeOpenAI(req)
	require.NoError(t, err)

	parsed, err := ParseOpenAI(serialized)
	require.NoError(t, err)

	// Verify tool call ID round-trip
	require.Len(t, parsed.Messages, 3)
	assert.Equal(t, "call_123", parsed.Messages[1].ToolCalls[0].ID)
	assert.Equal(t, "call_123", parsed.Messages[2].ToolCallID)

	// Verify tool result content preserved
	require.Len(t, parsed.Messages[2].Content, 1)
	assert.Equal(t, "text", parsed.Messages[2].Content[0].Type)
	assert.Contains(t, parsed.Messages[2].Content[0].Text, "Chart generated")
}

// TestGLM_PrivateFields_WebSearch 验证 GLM 私有字段 web_search 通过 Extensions 保留
func TestGLM_PrivateFields_WebSearch(t *testing.T) {
	glmTools, _ := json.Marshal([]map[string]interface{}{
		{
			"type": "web_search",
			"web_search": map[string]interface{}{
				"enable":       true,
				"search_query": "latest news",
			},
		},
	})

	req := &InternalRequest{
		Model: "glm-4-alltools",
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "最近有什么新闻?"}}},
		},
		Extensions: map[string]json.RawMessage{
			"glm_web_search_enabled": json.RawMessage(`true`),
			"glm_tools":              glmTools,
		},
		SourceProtocol: ProtocolOpenAIChat,
	}

	serialized, err := SerializeOpenAI(req)
	require.NoError(t, err)

	parsed, err := ParseOpenAI(serialized)
	require.NoError(t, err)

	// Verify Extensions preserved
	assert.NotNil(t, parsed.Extensions)
	assert.Contains(t, parsed.Extensions, "glm_web_search_enabled")
}

// TestGLM_PrivateFields_Retrieval 验证 GLM 私有字段 retrieval 通过 Extensions 保留
func TestGLM_PrivateFields_Retrieval(t *testing.T) {
	glmTools, _ := json.Marshal([]map[string]interface{}{
		{
			"type": "retrieval",
			"retrieval": map[string]interface{}{
				"knowledge_id":    "kb_123456",
				"prompt_template": "{{knowledge}}\n{{question}}",
			},
		},
	})

	req := &InternalRequest{
		Model: "glm-4",
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "查询知识库"}}},
		},
		Extensions: map[string]json.RawMessage{
			"glm_retrieval_knowledge_id": json.RawMessage(`"kb_123456"`),
			"glm_tools":                  glmTools,
		},
		SourceProtocol: ProtocolOpenAIChat,
	}

	serialized, err := SerializeOpenAI(req)
	require.NoError(t, err)

	parsed, err := ParseOpenAI(serialized)
	require.NoError(t, err)

	// Verify Extensions preserved
	assert.Contains(t, parsed.Extensions, "glm_retrieval_knowledge_id")
}

// TestGLM_CrossVendorIsolation 验证 GLM 私有字段跨厂隔离
func TestGLM_CrossVendorIsolation(t *testing.T) {
	// GLM request with private fields
	glmReq := &InternalRequest{
		Model: "glm-4",
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "测试"}}},
		},
		Extensions: map[string]json.RawMessage{
			"glm_web_search_enabled":     json.RawMessage(`true`),
			"glm_retrieval_knowledge_id": json.RawMessage(`"kb_123"`),
			"glm_do_sample":              json.RawMessage(`true`),
		},
		SourceProtocol: ProtocolOpenAIChat,
	}

	// Simulate cross-vendor scenario: serialize for a different provider
	// In real scenario, TransportIRConverter would strip vendor-specific extensions
	// Here we verify that Extensions are present and vendor-specific

	serialized, err := SerializeOpenAI(glmReq)
	require.NoError(t, err)

	parsed, err := ParseOpenAI(serialized)
	require.NoError(t, err)

	// Extensions should be preserved in OpenAI format (they go to root level or ignored)
	// The key point is that GLM-specific fields don't leak to other vendors
	// TransportIRConverter is responsible for filtering Extensions by catalog code

	// Verify GLM fields are in Extensions
	assert.Contains(t, parsed.Extensions, "glm_web_search_enabled")
	assert.Contains(t, parsed.Extensions, "glm_retrieval_knowledge_id")
	assert.Contains(t, parsed.Extensions, "glm_do_sample")

	// When converting to another vendor (e.g., OpenAI), these should be stripped
	// This is tested in domains/transformation/ir_converter_test.go
	// Here we just verify they are namespaced with "glm_" prefix
	for key := range parsed.Extensions {
		if key != "glm_web_search_enabled" && key != "glm_retrieval_knowledge_id" && key != "glm_do_sample" {
			t.Errorf("Unexpected extension key without glm_ prefix: %s", key)
		}
	}
}
