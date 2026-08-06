package v2

import (
	"context"
	"testing"
)

// TestOutboundBuilder_BuildFromDeltas_EmptySession 测试空会话
// 注意：此测试需要数据库连接，在没有 TEST_DB_URL 时跳过
func TestOutboundBuilder_BuildFromDeltas_EmptySession(t *testing.T) {
	t.Skip("Skipping test that requires database connection")

	reader := &TurnReader{} // nil db, will return empty
	builder := NewOutboundBuilder(reader)

	messages, meta, err := builder.BuildFromDeltas(
		context.Background(),
		"tenant1", "session1",
		0, true,
	)

	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if len(messages) != 0 {
		t.Errorf("expected 0 messages, got %d", len(messages))
	}

	if meta.TotalMessages != 0 {
		t.Errorf("expected TotalMessages=0, got %d", meta.TotalMessages)
	}
}

// TestOutboundBuilder_BuildFromDeltas_PreserveCompression 测试保留压缩 marker
func TestOutboundBuilder_BuildFromDeltas_PreserveCompression(t *testing.T) {
	// Mock messages with compression marker
	messages := []Message{
		{Role: "system", Content: "You are a helpful assistant"},
		{Role: "user", Content: "Hello"},
		{Role: "assistant", Content: "[smm_v1:abc123] Previous conversation summarized..."},
		{Role: "user", Content: "Tell me more"},
	}

	builder := NewOutboundBuilder(&TurnReader{})

	// Test: preserve compression
	hasCompression := builder.hasCompressionMarker(messages)
	if !hasCompression {
		t.Error("expected compression marker detected")
	}

	filtered := builder.filterCompressionMarkers(messages)
	if len(filtered) != 3 {
		t.Errorf("expected 3 messages after filtering, got %d", len(filtered))
	}

	// Verify compression marker was removed
	for _, msg := range filtered {
		if builder.isCompressionMarker(msg) {
			t.Errorf("compression marker should be filtered out: %s", msg.Content)
		}
	}
}

// TestOutboundBuilder_IsCompressionMarker 测试压缩 marker 识别
func TestOutboundBuilder_IsCompressionMarker(t *testing.T) {
	builder := NewOutboundBuilder(&TurnReader{})

	tests := []struct {
		name     string
		message  Message
		expected bool
	}{
		{
			name:     "smm_v1 marker",
			message:  Message{Role: "assistant", Content: "[smm_v1:abc123] Summary..."},
			expected: true,
		},
		{
			name:     "legacy summary marker",
			message:  Message{Role: "assistant", Content: "[summary:xyz] Summary..."},
			expected: true,
		},
		{
			name:     "normal message",
			message:  Message{Role: "user", Content: "Hello world"},
			expected: false,
		},
		{
			name:     "message containing marker but not at start",
			message:  Message{Role: "assistant", Content: "Text before [smm_v1:abc]"},
			expected: false,
		},
		{
			name:     "empty content",
			message:  Message{Role: "user", Content: ""},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := builder.isCompressionMarker(tt.message)
			if result != tt.expected {
				t.Errorf("expected %v, got %v for message: %v", tt.expected, result, tt.message.Content)
			}
		})
	}
}

// TestOutboundBuilder_EstimateTokens 测试 token 估算
func TestOutboundBuilder_EstimateTokens(t *testing.T) {
	tests := []struct {
		name      string
		messages  []Message
		minTokens int
		maxTokens int
	}{
		{
			name:      "empty messages",
			messages:  []Message{},
			minTokens: 0,
			maxTokens: 0,
		},
		{
			name: "simple conversation",
			messages: []Message{
				{Role: "user", Content: "Hello"},
				{Role: "assistant", Content: "Hi there!"},
			},
			minTokens: 5,
			maxTokens: 15,
		},
		{
			name: "long message",
			messages: []Message{
				{Role: "user", Content: "This is a longer message with more content that should result in more tokens being estimated"},
			},
			minTokens: 20,
			maxTokens: 35,
		},
		{
			name: "with tool calls",
			messages: []Message{
				{
					Role:    "assistant",
					Content: "",
					ToolCalls: []map[string]interface{}{
						{
							"id":        "call_123",
							"type":      "function",
							"name":      "get_weather",
							"arguments": `{"location":"San Francisco"}`,
						},
					},
				},
			},
			minTokens: 10,
			maxTokens: 30,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tokens := estimateTokens(tt.messages)
			if tokens < tt.minTokens || tokens > tt.maxTokens {
				t.Errorf("expected tokens between %d and %d, got %d", tt.minTokens, tt.maxTokens, tokens)
			}
		})
	}
}

// TestOutboundBuilder_NilSafety 测试 nil 安全性
func TestOutboundBuilder_NilSafety(t *testing.T) {
	ctx := context.Background()

	// Test nil builder
	var builder *OutboundBuilder
	_, _, err := builder.BuildFromDeltas(ctx, "tenant", "session", 0, true)
	if err == nil {
		t.Error("expected error for nil builder")
	}

	// Test nil reader
	builder = &OutboundBuilder{reader: nil}
	_, _, err = builder.BuildFromDeltas(ctx, "tenant", "session", 0, true)
	if err == nil {
		t.Error("expected error for nil reader")
	}
}

// TestOutboundBuilder_BuildMeta 测试元数据构建
func TestOutboundBuilder_BuildMeta(t *testing.T) {
	messages := []Message{
		{Role: "system", Content: "System prompt"},
		{Role: "user", Content: "User message"},
		{Role: "assistant", Content: "[smm_v1:xyz] Compressed history"},
		{Role: "user", Content: "Follow-up question"},
		{Role: "assistant", Content: "Answer"},
	}

	builder := NewOutboundBuilder(&TurnReader{})

	// Build meta with compression preserved
	meta := &BuildMeta{
		TotalMessages:  len(messages),
		TokenEstimate:  estimateTokens(messages),
		HasCompression: builder.hasCompressionMarker(messages),
	}

	if meta.TotalMessages != 5 {
		t.Errorf("expected 5 messages, got %d", meta.TotalMessages)
	}

	if !meta.HasCompression {
		t.Error("expected HasCompression=true")
	}

	if meta.TokenEstimate <= 0 {
		t.Error("expected positive token estimate")
	}
}

// TestOutboundBuilder_FilterPreservesOrder 测试过滤保持顺序
func TestOutboundBuilder_FilterPreservesOrder(t *testing.T) {
	messages := []Message{
		{Role: "user", Content: "First"},
		{Role: "assistant", Content: "[smm_v1:abc] Summary"},
		{Role: "user", Content: "Second"},
		{Role: "assistant", Content: "Response"},
	}

	builder := NewOutboundBuilder(&TurnReader{})
	filtered := builder.filterCompressionMarkers(messages)

	if len(filtered) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(filtered))
	}

	// Verify order preserved
	if filtered[0].Content != "First" {
		t.Error("order not preserved: expected 'First' at index 0")
	}
	if filtered[1].Content != "Second" {
		t.Error("order not preserved: expected 'Second' at index 1")
	}
	if filtered[2].Content != "Response" {
		t.Error("order not preserved: expected 'Response' at index 2")
	}
}
