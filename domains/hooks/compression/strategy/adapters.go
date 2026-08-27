// Package strategy - adapters.go (GW-10 Phase 1)
//
// 把现有 lite/caveman/toolfocused 三个 stage 包（已是纯函数 Apply/Compress）
// 包装成 strategy.Strategy 接口，让 Compressor 可以用 strategy.Runner 编排
// 而非手写 if feature-flag 链。
//
// 关键设计：
//   - LiteAdapter / CavemanAdapter / ToolFocusedAdapter 三个零依赖 wrapper。
//   - 每个 adapter 持有 On 字段（替代 Compressor.LiteStageEnabled 等）。
//   - adapter.Apply 内部直接调用对应 stage 包的纯函数；签名差异通过
//     各自的 Handle() 字段抹平。
//
// 与现有 dispatcher 的关系：
//   - 现有 Compressor.Compress 内部仍走 c.LiteStageEnabled / c.CavemanStageEnabled /
//     c.ToolFocusedStageEnabled 这条路径，行为不变。
//   - Adapter 仅供新路径（Compressor.RunStrategies）使用；两条路径并存，
//     main.go 可选择挂接其中一条。
//   - Phase 2 决策：是否将 Compressor.Compress 重构为内部统一走 Runner。
package strategy

import (
	"context"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/compression/caveman"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/compression/lite"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/compression/toolfocused"
)

// NameLite / NameCaveman / NameToolFocused 字符串常量，对齐 compressor.go 中
// CompressionStrategy enum 的字段值，让 Strategy.Name() 与现有 metrics label 一致。
const (
	NameLite        = "lite"
	NameCaveman     = "caveman"
	NameToolFocused = "toolfocused"
)

// liteHandle / cavemanHandle / toolfocusedHandle 三种 Apply 形态抹平为同一签名
// (input []byte) → (out []byte, applied bool)。所有 stage 包都是 fail-open 语义：
// 错误时返回 (input, false)，panic 由 stage 包内部 recover。
type (
	liteHandle        func(input []byte) (out []byte, applied bool)
	cavemanHandle     func(input []byte) (out []byte, applied bool)
	toolfocusedHandle func(input []byte) (out []byte, applied bool)
)

func defaultLiteHandle() liteHandle {
	return func(input []byte) ([]byte, bool) {
		out, _, applied := lite.Apply(input, lite.Options{})
		return out, applied
	}
}

func defaultCavemanHandle() cavemanHandle {
	return func(input []byte) ([]byte, bool) {
		out, _, applied := caveman.Compress(input, caveman.DefaultConfig())
		return out, applied
	}
}

func defaultToolFocusedHandle() toolfocusedHandle {
	return func(input []byte) ([]byte, bool) {
		out, _, applied := toolfocused.Apply(input, toolfocused.DefaultStrategies())
		return out, applied
	}
}

// LiteAdapter 包装 lite.Apply 为 Strategy。
//
// 字段：
//   - On: 替代 Compressor.LiteStageEnabled（避免与 Strategy.Enabled() 方法重名）
//   - Handle: 测试可注入 mock；nil → 落到 defaultLiteHandle
//   - Options: lite 包参数；nil → lite.Options{} 默认
type LiteAdapter struct {
	On      bool
	Handle  liteHandle
	Options lite.Options
}

func (a *LiteAdapter) Name() string { return NameLite }
func (a *LiteAdapter) Description() string {
	return "lite 5-stage pre-trim (whitespace/dedup/tool/redundant/image)"
}
func (a *LiteAdapter) Enabled() bool      { return a != nil && a.On }
func (a *LiteAdapter) GuardStage() string { return "lite" }

func (a *LiteAdapter) Apply(ctx context.Context, input []byte) ([]byte, bool, error) {
	if a == nil || !a.Enabled() {
		return input, false, nil
	}
	h := a.Handle
	if h == nil {
		h = defaultLiteHandle()
	}
	out, applied := h(input)
	return out, applied, nil
}

// Compile-time interface check.
var _ Strategy = (*LiteAdapter)(nil)

// CavemanAdapter 包装 caveman.Compress 为 Strategy。
type CavemanAdapter struct {
	On     bool
	Handle cavemanHandle
	Config *caveman.Config
}

func (a *CavemanAdapter) Name() string        { return NameCaveman }
func (a *CavemanAdapter) Description() string { return "caveman 8-language rule-based compression" }
func (a *CavemanAdapter) Enabled() bool       { return a != nil && a.On }
func (a *CavemanAdapter) GuardStage() string  { return "caveman" }

func (a *CavemanAdapter) Apply(ctx context.Context, input []byte) ([]byte, bool, error) {
	if a == nil || !a.Enabled() {
		return input, false, nil
	}
	h := a.Handle
	if h == nil {
		h = defaultCavemanHandle()
	}
	out, applied := h(input)
	return out, applied, nil
}

var _ Strategy = (*CavemanAdapter)(nil)

// ToolFocusedAdapter 包装 toolfocused.Apply 为 Strategy。
type ToolFocusedAdapter struct {
	On         bool
	Handle     toolfocusedHandle
	Strategies *toolfocused.Strategies
}

func (a *ToolFocusedAdapter) Name() string { return NameToolFocused }
func (a *ToolFocusedAdapter) Description() string {
	return "toolfocused 5-type tool-result compression (file/grep/shell/json/error)"
}
func (a *ToolFocusedAdapter) Enabled() bool      { return a != nil && a.On }
func (a *ToolFocusedAdapter) GuardStage() string { return "toolfocused" }

func (a *ToolFocusedAdapter) Apply(ctx context.Context, input []byte) ([]byte, bool, error) {
	if a == nil || !a.Enabled() {
		return input, false, nil
	}
	h := a.Handle
	if h == nil {
		h = defaultToolFocusedHandle()
	}
	out, applied := h(input)
	return out, applied, nil
}

var _ Strategy = (*ToolFocusedAdapter)(nil)
