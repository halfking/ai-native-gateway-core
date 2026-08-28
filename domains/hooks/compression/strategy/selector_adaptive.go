// Package strategy - selector_adaptive.go (GW-10 Phase 2)
//
// 自适应上下文预算选择器。把 OmniRoute 的 "adaptive escalation ladder"
// （resolveAdaptivePlan / ladder.ts）翻译成 Go 纯函数实现：
//
//  1. 给定 body + 上下文字节预算（BudgetFn 注入），估算当前体量。
//  2. 若已 <= 预算且 NeverOverCompress → 返回 nil（绝不"过度压缩"，
//     对应 OmniRoute headroomBefore >= 0 → no compression）。
//  3. 把全部已启用 strategy 按 CostTier 升序（最便宜/无损 在前）排成升级阶梯。
//  4. 沿阶梯逐个累加"预估压缩率"（REDUCTION_FACTOR 等价物），纳入策略
//     直到预估体量 <= 预算或达到 MaxStages 上限。
//  5. 返回该有序子集，Runner 按序执行（每段仍过 NeverWorse 守卫）。
//
// 关键不变量：
//   - 不调用任何 Strategy.Apply：只包含"决策"，由 Runner 执行；
//     Runner 的 NeverWorse 守卫保证最终输出不增字节（即使预估失准）。
//   - 预估压缩率是启发式（策略自己声明，见 EscalationProfile），用于
//     "该升级到第几档"的廉价决策，不要求 dry-run。
//   - BudgetFn 返回 <=0（未知预算）→ 不压缩，安全降级（绝不冒险删内容）。
//
// 与 ManualSelector 关系：两者都实现 Selector，Compressor 按
// LLM_GATEWAY_COMPRESSION_SELECTOR 在运行时二选一（详见 compressor.go）。
package strategy

import (
	"context"
	"log/slog"
	"sort"
)

// EscalationProfile 是 Strategy 可选实现的接口，用于向自适应选择器暴露
// 成本/压缩强度信息。不实现该接口的策略在升级阶梯中被当作 "无预期压缩"
// （ReductionFactor=1.0）跳过，绝不阻塞其他策略。
//
// 设计取舍：用可选接口而非给 Strategy 加必填方法，避免破坏 Phase 1 已稳定的
// 接口；未来新策略（如 OmniRoute 风格的 sliding-window / relevance）只需同时
// 实现 EscalationProfile 即可被自适应选择器编排。
type EscalationProfile interface {
	// ReductionFactor 返回该策略在典型 body 上的预估 len(out)/len(in)，
	// 取值 (0, 1.0]。1.0 表示无预期压缩，0.5 表示约减半。OmniRoute 的
	// REDUCTION_FACTOR 等价物（lite 0.92 / rtk 0.85 / caveman 0.70 / ...）。
	ReductionFactor() float64
	// CostTier 成本档位：0 = 最便宜/无损，数值越大越激进/越有损。
	// 升级阶梯按 CostTier 升序纳入。
	CostTier() int
}

// ReductionFactorOf 安全读取策略的预估压缩率。未实现 EscalationProfile 时
// 返回 1.0（无预期压缩）。值被钳制到 (0, 1.0]。
func ReductionFactorOf(s Strategy) float64 {
	if ep, ok := s.(EscalationProfile); ok {
		f := ep.ReductionFactor()
		if f <= 0 {
			return 1.0
		}
		if f > 1.0 {
			return 1.0
		}
		return f
	}
	return 1.0
}

// CostTierOf 安全读取策略的成本档位。未实现 EscalationProfile 时返回 0。
func CostTierOf(s Strategy) int {
	if ep, ok := s.(EscalationProfile); ok {
		return ep.CostTier()
	}
	return 0
}

// AdaptiveConfig 配置自适应升级选择器。
type AdaptiveConfig struct {
	// TargetRatio 是 body 必须压缩到的"预算占比"。例如 0.8 表示沿阶梯升级
	// 直到预估体量 <= 0.8 × 预算。仅在 BudgetFn 未显式给绝对字节预算时，
	// 由 BudgetFn 内部结合上下文窗口使用（见 Compressor 注入）。
	TargetRatio float64
	// MaxStages 限制升级阶梯最多纳入多少策略（安全阀）。0 = 不限制。
	MaxStages int
	// NeverOverCompress 为 true 时，若预估 body 已 <= 预算，返回 nil（不压缩）。
	// 对应 OmniRoute "headroomBefore >= 0 → no compression"：绝不浪费一次
	// LLM/规则压缩在已经合适的上下文上。
	NeverOverCompress bool
	// Estimator 估算 body 体量（字节）。默认 len(body)。允许调用方注入更准的
	// token 估计（如 tiktoken 等价物）。
	Estimator func(body []byte) int
	// BudgetFn 返回当前请求的绝对字节预算。自适应选择的核心输入。Compressor
	// 注入一个闭包，解析 contextWindow × fraction 的字节阈值。返回 <=0 表示
	// 未知预算 → 选择器安全返回 nil（不压缩）。
	BudgetFn func(body []byte) int
}

// AdaptiveSelector 是 Phase 2 落地的自动选择器。
type AdaptiveSelector struct {
	cfg AdaptiveConfig
}

// NewAdaptiveSelector 构造自适应选择器。cfg.BudgetFn 为 nil 时行为退化为
// "永不压缩"（安全默认）；调用方（Compressor）必须注入有意义的 BudgetFn。
func NewAdaptiveSelector(cfg AdaptiveConfig) *AdaptiveSelector {
	if cfg.TargetRatio <= 0 || cfg.TargetRatio > 1.0 {
		cfg.TargetRatio = 0.8
	}
	return &AdaptiveSelector{cfg: cfg}
}

func (a *AdaptiveSelector) estimator() func([]byte) int {
	if a.cfg.Estimator != nil {
		return a.cfg.Estimator
	}
	return func(b []byte) int { return len(b) }
}

func (a *AdaptiveSelector) budget(body []byte) int {
	if a.cfg.BudgetFn == nil {
		return 0
	}
	b := a.cfg.BudgetFn(body)
	if b <= 0 {
		return 0
	}
	return b
}

// Select 实现 Selector 接口（自适应升级版）。
//
// 行为：
//   - 预算未知（BudgetFn 返回 <=0）→ nil（不压缩，安全）。
//   - 预估体量 <= 预算 且 NeverOverCompress → nil（不压缩）。
//   - 否则按 CostTier 升序排阶梯，逐个累加预估压缩率纳入策略，直到
//     预估体量 <= 预算或达到 MaxStages。返回有序子集（可能为空 → 不压缩）。
func (a *AdaptiveSelector) Select(_ context.Context, all []Strategy, body []byte) []Strategy {
	est := a.estimator()
	size := est(body)
	budget := a.budget(body)

	// 预算未知或空 body：绝不冒险压缩（可能误删上下文）。
	if budget <= 0 || size <= 0 {
		return nil
	}
	target := int(float64(budget) * a.cfg.TargetRatio)
	if target <= 0 {
		return nil
	}

	// 已经适配：不压缩（NeverOverCompress 是自适应选择器的核心纪律）。
	// target 是明确的字节启发式目标，不应被表述为 tokenizer 的精确 token 预算。
	if size <= target {
		if a.cfg.NeverOverCompress {
			slog.Debug("strategy.AdaptiveSelector: body within target; skip",
				"bytes", size, "target_bytes", target, "budget_bytes", budget)
			return nil
		}
		// NeverOverCompress=false：仍允许跑无损最便宜档（如 lite 清理空白），
		// 但只有在其确实能缩减时才纳入（reduction<1）。下面阶梯逻辑会处理。
	}

	// 按 CostTier 升序排成升级阶梯（稳定排序，同档位保持注册顺序）。
	ordered := make([]Strategy, 0, len(all))
	for _, s := range all {
		if s == nil {
			continue
		}
		ordered = append(ordered, s)
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		return CostTierOf(ordered[i]) < CostTierOf(ordered[j])
	})

	// 沿阶梯升级：累加预估压缩率，纳入直到预估体量 <= target 或达 MaxStages。
	var chosen []Strategy
	estSize := size
	stages := 0
	for _, s := range ordered {
		if !s.Enabled() {
			continue
		}
		if a.cfg.MaxStages > 0 && stages >= a.cfg.MaxStages {
			break
		}
		f := ReductionFactorOf(s)
		if f >= 1.0 {
			// 无预期压缩，跳过后继续看更激进档（它可能有压缩）。
			continue
		}
		estSize = int(float64(estSize) * f)
		chosen = append(chosen, s)
		stages++
		if estSize <= target {
			break
		}
	}

	if len(chosen) == 0 {
		return nil
	}
	return chosen
}

// Compile-time interface check.
var _ Selector = (*AdaptiveSelector)(nil)
