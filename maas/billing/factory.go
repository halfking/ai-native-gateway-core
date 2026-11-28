package billing

import (
	"fmt"
	"sync"
)

// BillingEngineFactory 是计费引擎的工厂。
// 负责管理所有已注册的计费引擎，并根据计费模式创建相应的引擎实例。
//
// 设计特点：
// - 单例模式：全局共享一个工厂实例
// - 线程安全：支持并发注册和创建
// - 插件化：通过 Register 方法动态注册新引擎
type BillingEngineFactory struct {
	engines map[BillingMode]BillingEngine
	mu      sync.RWMutex
}

// DefaultFactory 是全局默认工厂实例
var DefaultFactory = NewBillingEngineFactory()

// NewBillingEngineFactory 创建一个新的计费引擎工厂
func NewBillingEngineFactory() *BillingEngineFactory {
	return &BillingEngineFactory{
		engines: make(map[BillingMode]BillingEngine),
	}
}

// Register 注册一个计费引擎到工厂
//
// 参数:
//   engine: 要注册的计费引擎实例
//
// 返回:
//   error: 如果已存在相同模式的引擎，返���错误
//
// 示例:
//   factory.Register(&PayAsYouGoEngine{})
//
// 注意:
//   - 同一个计费模式只能注册一次
//   - 通常在 init() 函数中调用以实现插件自动注册
func (f *BillingEngineFactory) Register(engine BillingEngine) error {
	if engine == nil {
		return fmt.Errorf("cannot register nil engine")
	}

	mode := engine.SupportedMode()
	if mode == "" {
		return fmt.Errorf("engine %s has empty billing mode", engine.Name())
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	if _, exists := f.engines[mode]; exists {
		return fmt.Errorf("billing engine for mode %s already registered", mode)
	}

	f.engines[mode] = engine
	return nil
}

// MustRegister 注册计费引擎，如果失败则panic
//
// 用于 init() 函数中的强制注册，简化错误处理。
//
// 示例:
//   func init() {
//       DefaultFactory.MustRegister(&PayAsYouGoEngine{})
//   }
func (f *BillingEngineFactory) MustRegister(engine BillingEngine) {
	if err := f.Register(engine); err != nil {
		panic(fmt.Sprintf("failed to register billing engine: %v", err))
	}
}

// Create 根据计费模式创建计费引擎实例
//
// 参数:
//   mode: 计费模式（如 ModePayAsYouGo）
//
// 返回:
//   engine: 对应的计费引擎实例
//   error: 如果模式未注册，返回错误
//
// 示例:
//   engine, err := factory.Create(billing.ModePayAsYouGo)
//   if err != nil {
//       return err
//   }
//   result, err := engine.Calculate(ctx, consumption)
//
// 注意:
//   - 返回的是已注册的引擎实例（不是新建）
//   - 引擎应设计为无状态，可安全并发调用
func (f *BillingEngineFactory) Create(mode BillingMode) (BillingEngine, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	engine, exists := f.engines[mode]
	if !exists {
		return nil, fmt.Errorf("no billing engine registered for mode: %s", mode)
	}

	return engine, nil
}

// Get 是 Create 的别名，提供更语义化的方法名
func (f *BillingEngineFactory) Get(mode BillingMode) (BillingEngine, error) {
	return f.Create(mode)
}

// Has 检查指定的计费模式是否已注册
//
// 参数:
//   mode: 要检查的计费模式
//
// 返回:
//   bool: true 表示已注册，false 表示未注册
func (f *BillingEngineFactory) Has(mode BillingMode) bool {
	f.mu.RLock()
	defer f.mu.RUnlock()

	_, exists := f.engines[mode]
	return exists
}

// ListModes 列出所有已注册的计费模式
//
// 返回:
//   []BillingMode: 已注册的计费模式列表
//
// 用途:
//   - Admin API 展示可用的计费模式
//   - 配置验证
//   - 诊断和调试
func (f *BillingEngineFactory) ListModes() []BillingMode {
	f.mu.RLock()
	defer f.mu.RUnlock()

	modes := make([]BillingMode, 0, len(f.engines))
	for mode := range f.engines {
		modes = append(modes, mode)
	}

	return modes
}

// ListEngines 列出所有已注册的计费引擎
//
// 返回:
//   []BillingEngine: 已注册的计费引擎列表
//
// 用途:
//   - 获取引擎的详细信息（Name等）
//   - 诊断和调试
func (f *BillingEngineFactory) ListEngines() []BillingEngine {
	f.mu.RLock()
	defer f.mu.RUnlock()

	engines := make([]BillingEngine, 0, len(f.engines))
	for _, engine := range f.engines {
		engines = append(engines, engine)
	}

	return engines
}

// Unregister 注销指定计费模式的引擎
//
// 参数:
//   mode: 要注销的计费模式
//
// 返回:
//   error: 如果模式未注册，返回错误
//
// 注意:
//   - 主要用于测试场景
//   - 生产环境不建议动态注销引擎
func (f *BillingEngineFactory) Unregister(mode BillingMode) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if _, exists := f.engines[mode]; !exists {
		return fmt.Errorf("billing mode %s not registered", mode)
	}

	delete(f.engines, mode)
	return nil
}

// Reset 清空所有已注册的引擎
//
// 注意:
//   - 仅用于测试场景
//   - 生产环境不要调用此方法
func (f *BillingEngineFactory) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.engines = make(map[BillingMode]BillingEngine)
}

// Count 返回已注册的引擎数量
func (f *BillingEngineFactory) Count() int {
	f.mu.RLock()
	defer f.mu.RUnlock()

	return len(f.engines)
}

// 便捷函数：使用默认工厂

// Register 在默认工厂上注册引擎
func Register(engine BillingEngine) error {
	return DefaultFactory.Register(engine)
}

// MustRegister 在默认工厂上强制注册引擎
func MustRegister(engine BillingEngine) {
	DefaultFactory.MustRegister(engine)
}

// Create 使用默认工厂创建引擎
func Create(mode BillingMode) (BillingEngine, error) {
	return DefaultFactory.Create(mode)
}

// Get 使用默认工厂获取引擎
func Get(mode BillingMode) (BillingEngine, error) {
	return DefaultFactory.Get(mode)
}

// Has 使用默认工厂检查模式是否已注册
func Has(mode BillingMode) bool {
	return DefaultFactory.Has(mode)
}

// ListModes 使用默认工厂列出所有模式
func ListModes() []BillingMode {
	return DefaultFactory.ListModes()
}
