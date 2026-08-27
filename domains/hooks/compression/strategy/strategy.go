// Package strategy (GW-10 Phase 1) 算法选择器基础设施。
//
// 设计目标：把现有 "lite/caveman/toolfocused/mechanical trim" 分散阶段收敛到
// 单一 Strategy 接口，让 dispatcher 通过 Registry + Selector 编排，而不是写死的
// if-feature-flag 链。Phase 1 落地最小可行版：手动选择器（Policy 列表）+ 单个
// Strategy 接口 + 命名 Registry。
//
// 与现有 dispatcher 的关系（非破坏性新增）：
//   - 现有 LiteStageEnabled/CavemanStageEnabled/ToolFocusedStageEnabled 仍生效，
//     Compressor.Compress 内部顺序不变。
//   - 新增 Compressor.RunStrategies 提供"基于 Selector 输出的 strategy 列表顺序
//     执行"的新路径，main.go 可选择挂接或保持原 Compressor.Compress 路径。
//
// Phase 2+ 计划（不本轮实现）：
//   - CEL 表达式规则引擎
//   - tenant-level 配置覆盖
//   - 自适应上下文预算选择器
package strategy

import (
	"context"
	"fmt"
	"sync"
)

// Strategy 一个压缩引擎的最小契约。所有现有 lite/caveman/toolfocused 阶段都
// 可以包装为 Strategy。Phase 1 不要求打破现有 dispatcher（参见 package 注释）。
//
// 设计取舍：
//   - Apply 接收/返回 []byte，与现有 Compressor.Compress 一致；不强求 JSON 类型，
//     留给 strategy 内部解析（lite/caveman/toolfocused 都是先 json.Unmarshal）。
//   - 失败一律返回原 body + false + err；registry.Runner 已知每一段 fail-open，
//     整个 pipeline 不会因单段 panic 终止。
//   - 不在 Apply 内做 NeverWorse 守卫（保留策略自治），Runner 在 chain 时按
//     Strategy.GuardStage() 套统一守卫，保证输出不增字节。
type Strategy interface {
	// Name 返回唯一标识符，用于 Registry 索引、日志、metrics。
	Name() string

	// Description 一行人类可读描述，仅用于调试/log/metrics label。
	Description() string

	// Enabled 返回该 strategy 在当前 policy 下是否启用。Phase 1 简化为固定
	// true（注册 = 启用）；后续 Phase 通过 Selector.Enable() 控制。
	Enabled() bool

	// GuardStage 返回该 strategy 对应的 NeverWorse 守卫标签（compression 包），
	// Runner 在 chain 时使用。无守卫的 strategy 返回空字符串（runner 跳过守卫）。
	GuardStage() string

	// Apply 执行该 strategy 的压缩逻辑。
	//   - input: 原始 body bytes
	//   - output: 压缩后 body（未压缩时等于 input）
	//   - applied: true 表示发生了有效压缩
	//   - err: 任何 fatal 错误（fail-open：非 fatal 错误应返回 input, nil）
	Apply(ctx context.Context, input []byte) (output []byte, applied bool, err error)
}

// Registry 进程级单例：name → Strategy。注册时若 name 重复，返回 error。
//
// 线程安全：sync.RWMutex 保护。Apply 热路径读多写少，写只发生在 main.go 初始化阶段。
type Registry struct {
	mu    sync.RWMutex
	items map[string]Strategy
	order []string // 注册顺序，决定 Runner 默认执行顺序
}

// NewRegistry 返回空 Registry。
func NewRegistry() *Registry {
	return &Registry{items: make(map[string]Strategy)}
}

// Register 注册一个 strategy。已存在同名时返回 error（不覆盖）。
func (r *Registry) Register(s Strategy) error {
	if s == nil {
		return fmt.Errorf("strategy: nil")
	}
	name := s.Name()
	if name == "" {
		return fmt.Errorf("strategy: empty Name()")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.items[name]; ok {
		return fmt.Errorf("strategy: %q already registered", name)
	}
	r.items[name] = s
	r.order = append(r.order, name)
	return nil
}

// MustRegister 是 Register 的 panic 版本，用于 init() / 启动期 wiring。
func (r *Registry) MustRegister(s Strategy) {
	if err := r.Register(s); err != nil {
		panic(err)
	}
}

// Get 按名查询。未找到时返回 nil。
func (r *Registry) Get(name string) Strategy {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.items[name]
}

// Names 按注册顺序返回所有 strategy 名（用于调试与 metrics）。
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, len(r.order))
	copy(out, r.order)
	return out
}

// Snapshot 返回当前所有 strategy 的快照列表（按注册顺序）。
// 主要给 Selector 读取 — Selector 拿到的是不变快照，外部 Register 不影响
// 该 Selector 之后的决策。
func (r *Registry) Snapshot() []Strategy {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Strategy, 0, len(r.order))
	for _, name := range r.order {
		out = append(out, r.items[name])
	}
	return out
}
