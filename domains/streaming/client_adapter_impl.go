package streaming

import (
	"context"
	"encoding/json"
	"fmt"
)

// CursorAdapter Cursor 客户端适配器
// Cursor 特点：
// - 频繁的多轮对话
// - 依赖精确的 tool_call_id 匹配
// - 发送完整对话历史
// - 需要严格的协议遵循
type CursorAdapter struct {
	BaseClientAdapter
}

// NewCursorAdapter 创建 Cursor 适配器
func NewCursorAdapter() *CursorAdapter {
	return &CursorAdapter{
		BaseClientAdapter: BaseClientAdapter{name: "cursor"},
	}
}

// PreprocessRequest Cursor 请求预处理
func (a *CursorAdapter) PreprocessRequest(ctx context.Context, reqBody map[string]any) (map[string]any, error) {
	// Cursor 经常发送很长的对话历史，检查是否需要压缩
	if messages, ok := reqBody["messages"].([]any); ok {
		if len(messages) > 20 {
			// 记录长对话，但不做自动压缩（保持完整上下文）
			// 让路由层决定是否启用上下文压缩
			reqBody["_cursor_long_context"] = true
		}
	}

	// 确保 tool_calls 有 ID
	if toolCalls, ok := reqBody["tool_calls"].([]any); ok {
		for i, tc := range toolCalls {
			tcMap, _ := tc.(map[string]any)
			if _, hasID := tcMap["id"]; !hasID {
				// 生成确定性 ID
				tcMap["id"] = fmt.Sprintf("cursor_call_%d", i)
			}
		}
	}

	return reqBody, nil
}

// ValidateRequest Cursor 请求验证
func (a *CursorAdapter) ValidateRequest(ctx context.Context, reqBody map[string]any) []error {
	var errors []error

	// 检查 messages 格式
	messages, ok := reqBody["messages"].([]any)
	if !ok || len(messages) == 0 {
		errors = append(errors, fmt.Errorf("cursor requires non-empty messages array"))
	}

	// 检查 tool_calls 的 ID
	for i, msg := range messages {
		msgMap, _ := msg.(map[string]any)
		if role, _ := msgMap["role"].(string); role == "assistant" {
			if toolCalls, ok := msgMap["tool_calls"].([]any); ok {
				for j, tc := range toolCalls {
					tcMap, _ := tc.(map[string]any)
					if id, hasID := tcMap["id"].(string); !hasID || id == "" {
						errors = append(errors, fmt.Errorf("cursor requires tool_call_id at messages[%d].tool_calls[%d]", i, j))
					}
				}
			}
		}
	}

	return errors
}

// GetOptimizationHints Cursor 优化提示
func (a *CursorAdapter) GetOptimizationHints() OptimizationHints {
	return OptimizationHints{
		PreferLowLatency:      false,
		PreferHighQuality:     true,
		ExpectsLongContext:    true, // Cursor 经常有长上下文
		ExpectsMultiTurn:      true, // 多轮对话频繁
		ExpectsToolCalls:      true, // 频繁使用工具
		CacheEnabled:          true,
		MaxConcurrentRequests: 5,
	}
}

// ShouldEnableToolCallTracking Cursor 需要严格的 tool call 追踪
func (a *CursorAdapter) ShouldEnableToolCallTracking() bool {
	return true
}

// ShouldEnableStrictProtocol Cursor 需要严格协议检查
func (a *CursorAdapter) ShouldEnableStrictProtocol() bool {
	return true
}

// GetTimeout Cursor 适合较长的超时时间（因为长上下文）
func (a *CursorAdapter) GetTimeout() int {
	return 90
}

// ─────────────────────────────────────────────────────────────────────────────

// WindsurfAdapter Windsurf 客户端适配器
// Windsurf 特点：
// - 类似 Cursor 的行为
// - 严格的 OpenAI 协议兼容性要求
// - 新兴客户端，需要最佳体验
type WindsurfAdapter struct {
	BaseClientAdapter
}

// NewWindsurfAdapter 创建 Windsurf 适配器
func NewWindsurfAdapter() *WindsurfAdapter {
	return &WindsurfAdapter{
		BaseClientAdapter: BaseClientAdapter{name: "windsurf"},
	}
}

// PreprocessRequest Windsurf 请求预处理
func (a *WindsurfAdapter) PreprocessRequest(ctx context.Context, reqBody map[string]any) (map[string]any, error) {
	// 与 Cursor 类似的处理
	if messages, ok := reqBody["messages"].([]any); ok {
		if len(messages) > 20 {
			reqBody["_windsurf_long_context"] = true
		}
	}
	return reqBody, nil
}

// GetOptimizationHints Windsurf 优化提示
func (a *WindsurfAdapter) GetOptimizationHints() OptimizationHints {
	return OptimizationHints{
		PreferLowLatency:      false,
		PreferHighQuality:     true,
		ExpectsLongContext:    true,
		ExpectsMultiTurn:      true,
		ExpectsToolCalls:      true,
		CacheEnabled:          true,
		MaxConcurrentRequests: 5,
	}
}

// ShouldEnableToolCallTracking Windsurf 需要 tool call 追踪
func (a *WindsurfAdapter) ShouldEnableToolCallTracking() bool {
	return true
}

// ShouldEnableStrictProtocol Windsurf 需要严格协议
func (a *WindsurfAdapter) ShouldEnableStrictProtocol() bool {
	return true
}

// ─────────────────────────────────────────────────────────────────────────────

// CopilotAdapter GitHub Copilot 适配器
// Copilot 特点：
// - 主要用于代码补全
// - 短上下文请求
// - 低延迟优先
// - 较少使用 tool calls
type CopilotAdapter struct {
	BaseClientAdapter
}

// NewCopilotAdapter 创建 Copilot 适配器
func NewCopilotAdapter() *CopilotAdapter {
	return &CopilotAdapter{
		BaseClientAdapter: BaseClientAdapter{name: "copilot"},
	}
}

// PreprocessRequest Copilot 请求预处理
func (a *CopilotAdapter) PreprocessRequest(ctx context.Context, reqBody map[string]any) (map[string]any, error) {
	// Copilot 偏好快速响应，可以降低 max_tokens
	if _, hasMaxTokens := reqBody["max_tokens"]; !hasMaxTokens {
		reqBody["max_tokens"] = 1024 // 默认较小的 token 限制
	}

	// 优先使用流式响应以降低首字节延迟
	if _, hasStream := reqBody["stream"]; !hasStream {
		reqBody["stream"] = true
	}

	return reqBody, nil
}

// GetOptimizationHints Copilot 优化提示
func (a *CopilotAdapter) GetOptimizationHints() OptimizationHints {
	return OptimizationHints{
		PreferLowLatency:      true,  // 低延迟优先
		PreferHighQuality:     false, // 速度比质量更重要
		ExpectsLongContext:    false, // 短上下文
		ExpectsMultiTurn:      false, // 较少多轮
		ExpectsToolCalls:      false, // 很少工具调用
		CacheEnabled:          true,
		MaxConcurrentRequests: 10, // 支持更多并发
	}
}

// GetTimeout Copilot 需要较短超时
func (a *CopilotAdapter) GetTimeout() int {
	return 30
}

// ─────────────────────────────────────────────────────────────────────────────

// VSCodeAdapter VSCode 扩展适配器
// VSCode 特点：
// - 多种扩展（Continue, CodeGPT 等）
// - 行为差异大，需要灵活适配
// - 混合使用场景
type VSCodeAdapter struct {
	BaseClientAdapter
}

// NewVSCodeAdapter 创建 VSCode 适配器
func NewVSCodeAdapter() *VSCodeAdapter {
	return &VSCodeAdapter{
		BaseClientAdapter: BaseClientAdapter{name: "vscode"},
	}
}

// PreprocessRequest VSCode 请求预处理
func (a *VSCodeAdapter) PreprocessRequest(ctx context.Context, reqBody map[string]any) (map[string]any, error) {
	// VSCode 扩展多样，采用宽松策略
	// 不做强制性修改，保持兼容性
	return reqBody, nil
}

// GetOptimizationHints VSCode 优化提示
func (a *VSCodeAdapter) GetOptimizationHints() OptimizationHints {
	return OptimizationHints{
		PreferLowLatency:      false,
		PreferHighQuality:     true,
		ExpectsLongContext:    false,
		ExpectsMultiTurn:      true, // 中等程度的多轮
		ExpectsToolCalls:      true, // 中等程度的工具调用
		CacheEnabled:          true,
		MaxConcurrentRequests: 5,
	}
}

// ShouldEnableToolCallTracking VSCode 需要基本的 tool call 追踪
func (a *VSCodeAdapter) ShouldEnableToolCallTracking() bool {
	return true
}

// ─────────────────────────────────────────────────────────────────────────────

// ZedAdapter Zed 编辑器适配器
// Zed 特点：
// - 高性能编辑器
// - Rust 实现，注重性能
// - 现代化的 AI 集成
type ZedAdapter struct {
	BaseClientAdapter
}

// NewZedAdapter 创建 Zed 适配器
func NewZedAdapter() *ZedAdapter {
	return &ZedAdapter{
		BaseClientAdapter: BaseClientAdapter{name: "zed"},
	}
}

// GetOptimizationHints Zed 优化提示
func (a *ZedAdapter) GetOptimizationHints() OptimizationHints {
	return OptimizationHints{
		PreferLowLatency:      true, // 高性能编辑器，重视速度
		PreferHighQuality:     true,
		ExpectsLongContext:    false,
		ExpectsMultiTurn:      false,
		ExpectsToolCalls:      true,
		CacheEnabled:          true,
		MaxConcurrentRequests: 8,
	}
}

// ─────────────────────────────────────────────────────────────────────────────

// JetBrainsAdapter JetBrains IDE 适配器
// JetBrains 特点：
// - 多语言 IDE 支持（IntelliJ, PyCharm, WebStorm 等）
// - 深度集成项目上下文
// - 可能包含大量项目文件信息
type JetBrainsAdapter struct {
	BaseClientAdapter
}

// NewJetBrainsAdapter 创建 JetBrains 适配器
func NewJetBrainsAdapter() *JetBrainsAdapter {
	return &JetBrainsAdapter{
		BaseClientAdapter: BaseClientAdapter{name: "jetbrains"},
	}
}

// PreprocessRequest JetBrains 请求预处理
func (a *JetBrainsAdapter) PreprocessRequest(ctx context.Context, reqBody map[string]any) (map[string]any, error) {
	// JetBrains 可能发送大量项目上下文
	// 标记以便路由层优化
	if messages, ok := reqBody["messages"].([]any); ok {
		totalSize := 0
		for _, msg := range messages {
			if msgBytes, err := json.Marshal(msg); err == nil {
				totalSize += len(msgBytes)
			}
		}
		if totalSize > 100000 { // > 100KB
			reqBody["_jetbrains_large_context"] = true
		}
	}
	return reqBody, nil
}

// GetOptimizationHints JetBrains 优化提示
func (a *JetBrainsAdapter) GetOptimizationHints() OptimizationHints {
	return OptimizationHints{
		PreferLowLatency:      false,
		PreferHighQuality:     true,
		ExpectsLongContext:    true, // 深度项目集成
		ExpectsMultiTurn:      true,
		ExpectsToolCalls:      true,
		CacheEnabled:          true,
		MaxConcurrentRequests: 3,
	}
}

// ShouldEnableToolCallTracking JetBrains 需要 tool call 追踪
func (a *JetBrainsAdapter) ShouldEnableToolCallTracking() bool {
	return true
}

// GetTimeout JetBrains 适合较长超时（大上下文）
func (a *JetBrainsAdapter) GetTimeout() int {
	return 120
}

// ─────────────────────────────────────────────────────────────────────────────

// GenericAdapter 通用适配器
// 用于未识别的客户端或直接 API 调用
type GenericAdapter struct {
	BaseClientAdapter
}

// NewGenericAdapter 创建通用适配器
func NewGenericAdapter() *GenericAdapter {
	return &GenericAdapter{
		BaseClientAdapter: BaseClientAdapter{name: "generic"},
	}
}

// GetOptimizationHints 通用优化提示（平衡策略）
func (a *GenericAdapter) GetOptimizationHints() OptimizationHints {
	return OptimizationHints{
		PreferLowLatency:      false,
		PreferHighQuality:     true,
		ExpectsLongContext:    false,
		ExpectsMultiTurn:      false,
		ExpectsToolCalls:      false,
		CacheEnabled:          true,
		MaxConcurrentRequests: 0, // 无限制
	}
}
