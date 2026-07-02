package streaming

import (
	"context"
	"fmt"
	"net/http"
	"testing"
)

// TestCursorAdapter 测试 Cursor 适配器
func TestCursorAdapter(t *testing.T) {
	adapter := NewCursorAdapter()

	// 测试基本属性
	if adapter.Name() != "cursor" {
		t.Errorf("expected name 'cursor', got '%s'", adapter.Name())
	}

	// 测试优化提示
	hints := adapter.GetOptimizationHints()
	if !hints.ExpectsLongContext {
		t.Error("Cursor should expect long context")
	}
	if !hints.ExpectsMultiTurn {
		t.Error("Cursor should expect multi-turn")
	}
	if !hints.ExpectsToolCalls {
		t.Error("Cursor should expect tool calls")
	}

	// 测试 tool call 追踪
	if !adapter.ShouldEnableToolCallTracking() {
		t.Error("Cursor should enable tool call tracking")
	}

	// 测试请求预处理
	reqBody := map[string]any{
		"model":    "claude-sonnet-4-6",
		"messages": make([]any, 25), // 长对话
	}

	processed, err := adapter.PreprocessRequest(context.Background(), reqBody)
	if err != nil {
		t.Errorf("PreprocessRequest failed: %v", err)
	}

	if _, hasFlag := processed["_cursor_long_context"]; !hasFlag {
		t.Error("Should mark long context")
	}
}

// TestCopilotAdapter 测试 Copilot 适配器
func TestCopilotAdapter(t *testing.T) {
	adapter := NewCopilotAdapter()

	hints := adapter.GetOptimizationHints()
	if !hints.PreferLowLatency {
		t.Error("Copilot should prefer low latency")
	}
	if hints.ExpectsLongContext {
		t.Error("Copilot should not expect long context")
	}

	// 测试超时
	if adapter.GetTimeout() >= 60 {
		t.Error("Copilot should have shorter timeout")
	}

	// 测试请求预处理
	reqBody := map[string]any{
		"model": "gpt-4o-mini",
	}

	processed, err := adapter.PreprocessRequest(context.Background(), reqBody)
	if err != nil {
		t.Errorf("PreprocessRequest failed: %v", err)
	}

	// 应该添加默认 max_tokens
	if maxTokens, ok := processed["max_tokens"].(int); !ok || maxTokens == 0 {
		t.Error("Should set default max_tokens for Copilot")
	}

	// 应该启用流式
	if stream, ok := processed["stream"].(bool); !ok || !stream {
		t.Error("Should enable streaming for Copilot")
	}
}

// TestClientAdapterRegistry 测试适配器注册表
func TestClientAdapterRegistry(t *testing.T) {
	registry := GetRegistry()

	// 测试获取已注册的适配器
	cursor := registry.Get("cursor")
	if cursor == nil {
		t.Error("Should have cursor adapter")
	}
	if cursor.Name() != "cursor" {
		t.Errorf("Expected 'cursor', got '%s'", cursor.Name())
	}

	// 测试获取未注册的适配器（应返回通用适配器）
	unknown := registry.Get("unknown-client")
	if unknown == nil {
		t.Error("Should return generic adapter for unknown client")
	}
	if unknown.Name() != "generic" {
		t.Errorf("Expected 'generic' for unknown client, got '%s'", unknown.Name())
	}
}

// TestGetClientAdapter 测试从 HTTP 请求获取适配器
func TestGetClientAdapter(t *testing.T) {
	tests := []struct {
		userAgent    string
		expectedName string
	}{
		{"cursor/0.1", "cursor"},
		{"windsurf/1.0", "windsurf"},
		{"github-copilot/1.0", "copilot"},
		{"vscode/1.0", "vscode"},
		{"unknown-client", "generic"},
	}

	for _, tt := range tests {
		t.Run(tt.userAgent, func(t *testing.T) {
			req, _ := http.NewRequest("POST", "/v1/chat/completions", nil)
			req.Header.Set("User-Agent", tt.userAgent)

			adapter := GetClientAdapter(req)
			if adapter.Name() != tt.expectedName {
				t.Errorf("Expected adapter '%s' for User-Agent '%s', got '%s'",
					tt.expectedName, tt.userAgent, adapter.Name())
			}
		})
	}
}

// BenchmarkCursorPreprocess 性能测试
func BenchmarkCursorPreprocess(b *testing.B) {
	adapter := NewCursorAdapter()
	reqBody := map[string]any{
		"model": "claude-sonnet-4-6",
		"messages": []any{
			map[string]any{"role": "user", "content": "test"},
		},
	}
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = adapter.PreprocessRequest(ctx, reqBody)
	}
}

// TestAdapterOptimizationHintsConsistency 测试优化提示一致性
func TestAdapterOptimizationHintsConsistency(t *testing.T) {
	adapters := []ClientAdapter{
		NewCursorAdapter(),
		NewWindsurfAdapter(),
		NewCopilotAdapter(),
		NewVSCodeAdapter(),
		NewZedAdapter(),
		NewJetBrainsAdapter(),
		NewGenericAdapter(),
	}

	for _, adapter := range adapters {
		t.Run(adapter.Name(), func(t *testing.T) {
			hints := adapter.GetOptimizationHints()

			// 验证提示的逻辑一致性
			if hints.PreferLowLatency && hints.ExpectsLongContext {
				t.Logf("Note: %s prefers low latency but expects long context (may conflict)",
					adapter.Name())
			}

			if hints.MaxConcurrentRequests < 0 {
				t.Errorf("MaxConcurrentRequests should not be negative for %s",
					adapter.Name())
			}

			// 记录配置供审查
			t.Logf("%s optimization hints: lowLatency=%v, longContext=%v, toolCalls=%v",
				adapter.Name(),
				hints.PreferLowLatency,
				hints.ExpectsLongContext,
				hints.ExpectsToolCalls)
		})
	}
}

// Example_usage 使用示例
func Example_usage() {
	// 1. 从 HTTP 请求获取适配器
	req, _ := http.NewRequest("POST", "/v1/chat/completions", nil)
	req.Header.Set("User-Agent", "cursor/0.1")

	adapter := GetClientAdapter(req)
	fmt.Printf("Using adapter: %s\n", adapter.Name())

	// 2. 预处理请求
	reqBody := map[string]any{
		"model": "claude-sonnet-4-6",
		"messages": []any{
			map[string]any{"role": "user", "content": "Hello"},
		},
	}

	processedReq, _ := adapter.PreprocessRequest(context.Background(), reqBody)

	// 3. 获取优化提示用于路由决策
	hints := adapter.GetOptimizationHints()
	if hints.PreferLowLatency {
		fmt.Println("Route to low-latency model")
	}

	// 4. 验证请求
	errors := adapter.ValidateRequest(context.Background(), processedReq)
	if len(errors) > 0 {
		fmt.Printf("Validation errors: %v\n", errors)
	}

	// 5. 检查是否需要启用特殊功能
	if adapter.ShouldEnableToolCallTracking() {
		fmt.Println("Enable tool call ID tracking")
	}

	// Output:
	// Using adapter: cursor
	// Enable tool call ID tracking
}
