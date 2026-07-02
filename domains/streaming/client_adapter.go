package streaming

import (
	"context"
	"net/http"
)

// ClientAdapter 定义客户端适配器接口。
// 使用适配器模式（Adapter Pattern）为不同的 AI 编程助手提供统一的接口，
// 同时允许针对每个客户端的特殊需求进行定制化处理。
//
// 设计原则：
// 1. 开闭原则（Open-Closed）: 对扩展开放，对修改关闭
// 2. 单一职责原则：每个适配器只负责一个客户端的特性
// 3. 依赖倒置原则：高层模块依赖抽象接口，不依赖具体实现
//
// 使用场景：
// - Cursor: 需要完整的 tool_call_id 追踪
// - Windsurf: 严格的协议检查
// - Copilot: 优化低延迟响应
// - VSCode: 灵活的协议兼容
type ClientAdapter interface {
	// Name 返回客户端标识名称
	Name() string

	// PreprocessRequest 在请求发送到上游前进行预处理
	// 可以修改请求体、添加特殊参数、调整超时等
	PreprocessRequest(ctx context.Context, reqBody map[string]any) (map[string]any, error)

	// PostprocessResponse 在响应返回客户端前进行后处理
	// 可以修改响应体、补充缺失字段、格式化等
	PostprocessResponse(ctx context.Context, respBody map[string]any) (map[string]any, error)

	// ProcessStreamChunk 处理流式响应的单个数据块
	// 返回处理后的数据块，如果需要跳过该块则返回 nil
	ProcessStreamChunk(ctx context.Context, chunk []byte) ([]byte, error)

	// ValidateRequest 验证请求是否符合客户端要求
	// 返回验证错误列表，空列表表示验证通过
	ValidateRequest(ctx context.Context, reqBody map[string]any) []error

	// GetOptimizationHints 返回该客户端的优化提示
	// 用于路由决策、缓存策略等
	GetOptimizationHints() OptimizationHints

	// ShouldEnableToolCallTracking 是否需要启用 Tool Call ID 追踪
	ShouldEnableToolCallTracking() bool

	// ShouldEnableStrictProtocol 是否需要启用严格协议检查
	ShouldEnableStrictProtocol() bool

	// GetMaxRetries 获取该客户端建议的最大重试次数
	GetMaxRetries() int

	// GetTimeout 获取该客户端建议的超时时间（秒）
	GetTimeout() int
}

// OptimizationHints 优化提示
type OptimizationHints struct {
	// PreferLowLatency 优先选择低延迟模型
	PreferLowLatency bool

	// PreferHighQuality 优先选择高质量模型
	PreferHighQuality bool

	// ExpectsLongContext 期望长上下文支持
	ExpectsLongContext bool

	// ExpectsMultiTurn 期望多轮对话
	ExpectsMultiTurn bool

	// ExpectsToolCalls 期望工具调用
	ExpectsToolCalls bool

	// CacheEnabled 是否启用缓存
	CacheEnabled bool

	// MaxConcurrentRequests 最大并发请求数（0表示无限制）
	MaxConcurrentRequests int
}

// BaseClientAdapter 提供默认实现，具体适配器可以继承并覆盖特定方法
type BaseClientAdapter struct {
	name string
}

// Name 返回客户端名称
func (b *BaseClientAdapter) Name() string {
	return b.name
}

// PreprocessRequest 默认不做任何处理
func (b *BaseClientAdapter) PreprocessRequest(ctx context.Context, reqBody map[string]any) (map[string]any, error) {
	return reqBody, nil
}

// PostprocessResponse 默认不做任何处理
func (b *BaseClientAdapter) PostprocessResponse(ctx context.Context, respBody map[string]any) (map[string]any, error) {
	return respBody, nil
}

// ProcessStreamChunk 默认不做任何处理
func (b *BaseClientAdapter) ProcessStreamChunk(ctx context.Context, chunk []byte) ([]byte, error) {
	return chunk, nil
}

// ValidateRequest 默认不做验证
func (b *BaseClientAdapter) ValidateRequest(ctx context.Context, reqBody map[string]any) []error {
	return nil
}

// GetOptimizationHints 返回默认优化提示
func (b *BaseClientAdapter) GetOptimizationHints() OptimizationHints {
	return OptimizationHints{
		PreferLowLatency:      false,
		PreferHighQuality:     true,
		ExpectsLongContext:    false,
		ExpectsMultiTurn:      false,
		ExpectsToolCalls:      false,
		CacheEnabled:          true,
		MaxConcurrentRequests: 0,
	}
}

// ShouldEnableToolCallTracking 默认不启用
func (b *BaseClientAdapter) ShouldEnableToolCallTracking() bool {
	return false
}

// ShouldEnableStrictProtocol 默认不启用
func (b *BaseClientAdapter) ShouldEnableStrictProtocol() bool {
	return false
}

// GetMaxRetries 默认重试3次
func (b *BaseClientAdapter) GetMaxRetries() int {
	return 3
}

// GetTimeout 默认超时60秒
func (b *BaseClientAdapter) GetTimeout() int {
	return 60
}

// ClientAdapterRegistry 客户端适配器注册表（单例模式）
type ClientAdapterRegistry struct {
	adapters map[string]ClientAdapter
}

var defaultRegistry *ClientAdapterRegistry

func init() {
	defaultRegistry = NewClientAdapterRegistry()
	// 注册默认适配器
	defaultRegistry.Register(NewCursorAdapter())
	defaultRegistry.Register(NewWindsurfAdapter())
	defaultRegistry.Register(NewCopilotAdapter())
	defaultRegistry.Register(NewVSCodeAdapter())
	defaultRegistry.Register(NewZedAdapter())
	defaultRegistry.Register(NewJetBrainsAdapter())
	defaultRegistry.Register(NewGenericAdapter())
}

// NewClientAdapterRegistry 创建新的注册表
func NewClientAdapterRegistry() *ClientAdapterRegistry {
	return &ClientAdapterRegistry{
		adapters: make(map[string]ClientAdapter),
	}
}

// Register 注册客户端适配器
func (r *ClientAdapterRegistry) Register(adapter ClientAdapter) {
	r.adapters[adapter.Name()] = adapter
}

// Get 获取客户端适配器
func (r *ClientAdapterRegistry) Get(clientType string) ClientAdapter {
	if adapter, ok := r.adapters[clientType]; ok {
		return adapter
	}
	// 返回通用适配器作为后备
	return r.adapters["generic"]
}

// GetRegistry 获取默认注册表
func GetRegistry() *ClientAdapterRegistry {
	return defaultRegistry
}

// GetClientAdapter 根据HTTP请求获取对应的客户端适配器
// 使用工厂模式（Factory Pattern）创建适配器
func GetClientAdapter(r *http.Request) ClientAdapter {
	clientType := extractClientType(r)
	if clientType == "" {
		clientType = "generic"
	}
	return defaultRegistry.Get(clientType)
}
