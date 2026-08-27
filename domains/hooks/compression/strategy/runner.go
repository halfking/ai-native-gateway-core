// Package strategy - runner.go (GW-10 Phase 1)
//
// Runner 把 Selector 选出的 strategy 链按顺序应用到 body 上，每段之间套
// NeverWorse 守卫（如果 strategy 提供 GuardStage()）。任何一段抛 fatal error
// 都会让 runner 终止并返回当前 body + 累积的 trace + error。
//
// 设计取舍：
//   - 与现有 Compressor.Compress 平行：Compressor.Compress 路径不变（直接走
//     feature-flag if 链），新增 Compressor.RunStrategies 路径让 main.go 可以
//     选择迁移。Phase 2 再决定是否合并。
//   - strategy 包不 import compression 包（避免 import cycle），因此自带
//     defaultNeverWorse 复刻核心语义。Production 路径通过 SetGuard 注入
//     compression.NeverWorse 即可拿到 Prometheus 计数。
package strategy

import (
	"context"
	"errors"
	"log/slog"
)

// Runner 持有 Registry + 可选守卫实现。无状态，可在 Compressor 内多次复用。
type Runner struct {
	registry *Registry
	// guardNeverWorse 注入式依赖：prod 路径走 defaultNeverWorse（与 compression
	// 包的 NeverWorse 行为对齐：len(processed) < len(raw) 才接受，否则回退 raw）。
	guardNeverWorse func(raw, processed []byte, stage string) (out []byte, regressed bool)
}

// NewRunner 构造 Runner。默认挂载 defaultNeverWorse 作为守卫实现。
func NewRunner(reg *Registry) *Runner {
	return &Runner{
		registry:        reg,
		guardNeverWorse: defaultNeverWorse,
	}
}

// SetGuard 注入守卫实现（典型调用：Runner.SetGuard(compression.NeverWorse)）。
// 这样 production 可以复用 compression 包的 Prometheus 计数器，
// 测试可以替换为 stub 验证 regressed 触发路径。
func (r *Runner) SetGuard(g func(raw, processed []byte, stage string) ([]byte, bool)) {
	r.guardNeverWorse = g
}

// defaultNeverWorse 复刻 compression.NeverWorse 的核心语义：
//   - 空 raw / 空 processed → 直接放过
//   - len(processed) < len(raw) → 接受 processed
//   - 否则 → 回退 raw 并记录 regressed=true
//
// 不计入 Prometheus（那是 compression 包的事）；这里只记录到 slog 便于
// 排查 strategy 自身 bug 触发的膨胀。
func defaultNeverWorse(raw, processed []byte, stage string) ([]byte, bool) {
	if len(raw) == 0 {
		return processed, false
	}
	if len(processed) == 0 {
		return processed, false
	}
	if len(processed) < len(raw) {
		return processed, false
	}
	slog.Warn("strategy.Runner: never_worse guard regression (fallback to raw)",
		"stage", stage,
		"raw_bytes", len(raw),
		"processed_bytes", len(processed),
		"delta_bytes", len(processed)-len(raw),
	)
	return raw, true
}

// RunStats 单次 Run 的累积统计。
type RunStats struct {
	// BytesIn 输入 body 字节数。
	BytesIn int
	// BytesOut 输出 body 字节数（可能等于 BytesIn = 未压缩）。
	BytesOut int
	// AppliedNames 顺序：实际改变了 body 的 strategy 名列表。
	AppliedNames []string
	// SkippedNames 顺序：策略不适用 / skipped（[COMPRESSED: 幂等、不命中策略），
	// 仅作可观测，不算 regression。
	SkippedNames []string
	// TruncatedBy 守卫触发的 strategy 名（输出 >= 输入，被回退到原 body）。
	// 通常意味着该 strategy 异常，需要排查。
	TruncatedBy string
}

// RunWithBody 是实际的执行入口。返回最终 body + stats + error。
//
// 参数：
//   - ctx: 透传给各 Strategy.Apply
//   - sel: 选择器；nil = 不压缩（返回原 body 零变化）
//   - body: 待压缩 body
//
// 关键不变量：
//   - 输出 body 字节数 <= 输入 body 字节数（每段 NeverWorse 守卫保证）。
//   - 任何 Strategy.Apply 抛 error → 终止，返回当前 body + error。
//   - Strategy.Enabled() == false → 跳过（记入 SkippedNames，不算错误）。
//   - Strategy.Apply 返回 applied=false → 跳过，记入 SkippedNames。
func (r *Runner) RunWithBody(ctx context.Context, sel Selector, body []byte) ([]byte, RunStats, error) {
	stats := RunStats{BytesIn: len(body), BytesOut: len(body)}
	if r == nil || r.registry == nil {
		return body, stats, errors.New("strategy.Runner: nil registry")
	}
	if sel == nil {
		return body, stats, nil
	}
	all := r.registry.Snapshot()
	chosen := sel.Select(ctx, all)
	current := body
	for _, s := range chosen {
		if s == nil || !s.Enabled() {
			continue
		}
		out, applied, err := s.Apply(ctx, current)
		if err != nil {
			return current, stats, err
		}
		if !applied || len(out) == 0 {
			stats.SkippedNames = append(stats.SkippedNames, s.Name())
			continue
		}
		// NeverWorse 守卫：若 strategy 自带 GuardStage()，套统一守卫。
		if stage := s.GuardStage(); stage != "" && r.guardNeverWorse != nil {
			guarded, regressed := r.guardNeverWorse(current, out, stage)
			if regressed {
				stats.TruncatedBy = s.Name()
				stats.SkippedNames = append(stats.SkippedNames, s.Name())
				continue
			}
			out = guarded
		}
		stats.AppliedNames = append(stats.AppliedNames, s.Name())
		current = out
	}
	stats.BytesOut = len(current)
	return current, stats, nil
}
