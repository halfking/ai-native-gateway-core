// Package strategy - runner_parallel.go (2026-09-01)
//
// 并行 runner：把每个 strategy 当作独立的"压缩候选"从原始 body 并行 fan-out，
// 用信息量评分（cheapest-byte-level heuristic，本地实现避免与 compression
// 包形成循环依赖）挑最优结果。SequentialRunner.RunWithBody 行为完全不变，
// 可通过 RunParallelWithBody 显式启用。
//
// 关键不变量：
//   - 输出 body 字节数 ≤ 输入 body 字节数（per-stage guard 与 runner 末尾兜底保证）。
//   - 失败 fail-open：任何 strategy 抛 err 都独立跳过，不影响兄弟策略。
//   - 评分打平：信息量分数相同 → 取字节数更短者（更省 token）。
//   - 所有策略都失败或未 applied → 返回原 body + 累计 stats（never-worse）。

package strategy

import (
	"context"
	"regexp"
	"strings"
	"sync"
)

// RunParallelWithBody 并行执行 chosen 中所有 strategy，从原始 body 出发
// （非链式 current），按信息量评分选取输出。新增 WinnerName / CandidateCount
// 字段便于观测；AppliedNames / FailedNames / SkippedNames / TruncatedBy
// 沿用顺序链语义（统计每个候选的执行结果）。
//
// 约束：选中的 strategy 数 > 1 时才有"并行挑最优"的语义；若 sel 只选 1 个，
// 直接执行该 strategy 并返回（避免 goroutine 开销）。
func (r *Runner) RunParallelWithBody(ctx context.Context, sel Selector, body []byte) ([]byte, RunStats, error) {
	stats := RunStats{BytesIn: len(body), BytesOut: len(body)}
	if r == nil || r.registry == nil {
		return body, stats, errStrategyRunnerNilRegistry
	}
	if sel == nil {
		return body, stats, nil
	}

	guardFn, _ := r.guardNeverWorse.Load().(guardFuncType)

	all := r.registry.Snapshot()
	chosen := sel.Select(ctx, all, body)
	stats.CandidateCount = len(chosen)

	// 0 strategy → noop
	if len(chosen) == 0 {
		return body, stats, nil
	}

	// 单 strategy → 退化为顺序执行（节省 goroutine）。
	if len(chosen) == 1 {
		s := chosen[0]
		if s == nil || !s.Enabled() {
			stats.SkippedNames = append(stats.SkippedNames, safeName(s))
			return body, stats, nil
		}
		out, applied, err := s.Apply(ctx, body)
		if err != nil {
			stats.FailedNames = append(stats.FailedNames, s.Name())
			return body, stats, nil
		}
		if !applied || len(out) == 0 || len(out) > len(body) {
			stats.SkippedNames = append(stats.SkippedNames, s.Name())
			return body, stats, nil
		}
		if stage := s.GuardStage(); stage != "" && guardFn != nil {
			if guarded, regressed := guardFn(body, out, stage); !regressed {
				out = guarded
			} else {
				stats.TruncatedBy = append(stats.TruncatedBy, s.Name())
				return body, stats, nil
			}
		}
		stats.AppliedNames = append(stats.AppliedNames, s.Name())
		stats.WinnerName = s.Name()
		stats.BytesOut = len(out)
		return out, stats, nil
	}

	// 多 strategy → 并行 fan-out。索引化结果便于无锁读。
	outs := make([][]byte, len(chosen))
	errs := make([]error, len(chosen))
	applied := make([]bool, len(chosen))

	var wg sync.WaitGroup
	for i, s := range chosen {
		if s == nil || !s.Enabled() {
			// 不为这种 case 启动 goroutine；统计为 SkippedNames。
			stats.SkippedNames = append(stats.SkippedNames, safeName(s))
			continue
		}
		wg.Add(1)
		go func(idx int, strat Strategy) {
			defer wg.Done()
			o, a, e := strat.Apply(ctx, body)
			outs[idx] = o
			errs[idx] = e
			applied[idx] = a
		}(i, s)
	}
	wg.Wait()

	// 评分 + 选最优。
	type candidate struct {
		idx      int
		out      []byte
		density  float64
		shortLen int // 字节数，打平时作为 tiebreaker
	}
	var candidates []candidate
	for i := range chosen {
		if errs[i] != nil {
			stats.FailedNames = append(stats.FailedNames, chosen[i].Name())
			continue
		}
		if !applied[i] || len(outs[i]) == 0 {
			stats.SkippedNames = append(stats.SkippedNames, chosen[i].Name())
			continue
		}
		// NeverWorse 守卫
		stage := chosen[i].GuardStage()
		var candidateOut = outs[i]
		if stage != "" && guardFn != nil {
			guarded, regressed := guardFn(body, candidateOut, stage)
			if regressed {
				stats.TruncatedBy = append(stats.TruncatedBy, chosen[i].Name())
				stats.SkippedNames = append(stats.SkippedNames, chosen[i].Name())
				continue
			}
			candidateOut = guarded
		}
		if len(candidateOut) > len(body) {
			stats.TruncatedBy = append(stats.TruncatedBy, "aggregate")
			continue
		}
		stats.AppliedNames = append(stats.AppliedNames, chosen[i].Name())
		candidates = append(candidates, candidate{
			idx:      i,
			out:      candidateOut,
			density:  byteLevelDensity(candidateOut),
			shortLen: len(candidateOut),
		})
	}

	if len(candidates) == 0 {
		// 所有候选都失败/被跳过 → 返回原 body + 累计 stats。
		return body, stats, nil
	}

	// 选 argmax(density)；平局取 len(out) 更小者。
	best := candidates[0]
	for _, c := range candidates[1:] {
		if c.density > best.density || (c.density == best.density && c.shortLen < best.shortLen) {
			best = c
		}
	}
	stats.WinnerName = chosen[best.idx].Name()
	stats.BytesOut = best.shortLen
	return best.out, stats, nil
}

// applyOne 是单 strategy 的执行 + 守卫 + 统计更新，被 RunWithBody（顺序链）
// 与可能的未来 internal 调用方共用。RunParallelWithBody 直接内联是为了统计
// 字段（AppliedNames/FailedNames/SkippedNames/TruncatedBy）在并行 fan-out 下
// 的语义边界更清晰（每个候选独立计分，不与 winner 冲突）。
func applyOne(ctx context.Context, s Strategy, in []byte, guardFn guardFuncType, stats *RunStats) ([]byte, bool, error) {
	if s == nil || !s.Enabled() {
		stats.SkippedNames = append(stats.SkippedNames, safeName(s))
		return in, false, nil
	}
	out, a, e := s.Apply(ctx, in)
	if e != nil {
		stats.FailedNames = append(stats.FailedNames, s.Name())
		return in, false, e
	}
	if !a || len(out) == 0 {
		stats.SkippedNames = append(stats.SkippedNames, s.Name())
		return in, false, nil
	}
	if stage := s.GuardStage(); stage != "" && guardFn != nil {
		guarded, regressed := guardFn(in, out, stage)
		if regressed {
			stats.TruncatedBy = append(stats.TruncatedBy, s.Name())
			stats.SkippedNames = append(stats.SkippedNames, s.Name())
			return in, false, nil
		}
		out = guarded
	}
	stats.AppliedNames = append(stats.AppliedNames, s.Name())
	return out, true, nil
}

func safeName(s Strategy) string {
	if s == nil {
		return "<nil>"
	}
	return s.Name()
}

// errStrategyRunnerNilRegistry 与 runner.go 中的 errors.New 字符串保持一致。
var errStrategyRunnerNilRegistry = &runnerError{msg: "strategy.Runner: nil registry"}

type runnerError struct{ msg string }

func (e *runnerError) Error() string { return e.msg }

// byteLevelDensity 是与 compression.ComputeInformationDensity 等价的轻量版。
// 这里重新实现是为了避免 strategy 包导入 compression 包（会形成 import cycle：
// compression/compressor.go 已经依赖 strategy）。语义尽量对齐：
//   - 基础 0.5
//   - 代码片段（```/function/class ）+0.3
//   - error/exception 关键字 +0.3
//   - http(s) URL +0.2
//   - 数字 +0.1
//   - 截断到 [0, 1]
//
// 注意：和 compression 包版本共享同一组字符串匹配模式，但在 strategy 包内
// 完全独立运行，避免 import cycle。
var densityDigitRE = regexp.MustCompile(`\d+`)

func computeByteLevelDensity(content string) float64 {
	if len(content) == 0 {
		return 0
	}
	density := 0.5
	if strings.Contains(content, "```") || strings.Contains(content, "function") || strings.Contains(content, "class ") {
		density += 0.3
	}
	if strings.Contains(content, "error") || strings.Contains(content, "Error") || strings.Contains(content, "exception") {
		density += 0.3
	}
	if strings.Contains(content, "http://") || strings.Contains(content, "https://") {
		density += 0.2
	}
	if densityDigitRE.MatchString(content) {
		density += 0.1
	}
	if density > 1.0 {
		density = 1.0
	}
	return density
}

// byteLevelDensity 是导出别名（供同包 runner_parallel_test.go 测试）。
func byteLevelDensity(b []byte) float64 {
	return computeByteLevelDensity(string(b))
}