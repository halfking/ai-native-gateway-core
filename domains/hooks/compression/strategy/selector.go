// Package strategy - selector.go (GW-10 Phase 1)
//
// 选择器从 Registry 全部 strategy 中挑出当前请求应执行的子集。
// Phase 1 仅实现 ManualSelector：根据显式 Policy（name 列表）筛选。
// 后续 Phase 落地 RuleBasedSelector / MLSlector / AdaptiveSelector。
package strategy

import (
	"context"
	"fmt"
)

// Selector 决策接口。给定 Registry 全部 strategy + 决策上下文（Phase 1 暂只需
// ctx），返回按执行顺序排列的 strategy 列表。返回 nil 表示不压缩。
//
// 设计取舍：
//   - 返回 []Strategy 而非 []string：避免 runner 再做一次 Get(name) 查找，
//     也允许 selector 在返回前对 strategy 做状态注入（如注入 per-request hint）。
//   - 不抛 error：selector 失败 = 退化为"全部不跑"，比 panic 安全；
//     真要日志告警，由 selector 内部 slog 而非抛上来。
type Selector interface {
	Select(ctx context.Context, all []Strategy) []Strategy
}

// Policy 手动策略：name 列表 + 是否按注册顺序。Phase 1 唯一支持的 selector 输入。
// 对应未来 docs/design/compression-strategy-selector.md §3.3 描述的
// tenant/session/global 三级配置（Phase 1 暂只支持 global 一级）。
type Policy struct {
	// Names 是按用户期望顺序排列的 strategy 名。空列表 = 全部关闭。
	Names []string

	// UnknownMode 遇到未注册 name 时的处理：
	//   "ignore" (默认): 跳过未知项
	//   "fail":         返回 error（Phase 1 不暴露 error 接口，先保留字段）
	UnknownMode string
}

// ResolvePolicy 把 Policy 字符串表达式解析为 Policy。
// 当前支持：
//   - "off" / ""  → Policy{}（关闭）
//   - "all"        → Policy{} 但 UnknownMode="ignore"（特殊语义见 ManualSelector）
//   - "lite,caveman" 等逗号分隔名 → Policy{Names: [...]}
//
// Phase 1 故意不引入完整表达式解析器；后续 Phase 引入 CEL 时另开 Resolver。
//
// 错误：当 off/all 与其他名称混用时返回 error，避免 "lite,off" → 静默 no-op。
// operator 若把 off 当成注释写进 policy 列表，必须早 fail，而不是 silently 关压缩。
func ResolvePolicy(spec string) (Policy, error) {
	if spec == "" || spec == "off" {
		return Policy{}, nil
	}
	if spec == "all" {
		// "all" 不预先展开，由 ManualSelector 在运行时对 Registry.Snapshot() 展开。
		return Policy{UnknownMode: "ignore"}, nil
	}
	parts := splitTrimNonEmpty(spec, ",")
	if len(parts) == 0 {
		return Policy{}, nil
	}
	// 校验：off/all 是关键字，只能作为整体 spec 出现，不能混在 names 列表中。
	for _, p := range parts {
		if p == "off" || p == "all" {
			return Policy{}, fmt.Errorf("%w: %q is a reserved keyword and cannot be mixed with other names; use it as the entire spec instead", ErrInvalidPolicySpec, p)
		}
	}
	return Policy{Names: parts, UnknownMode: "ignore"}, nil
}

// splitTrimNonEmpty 手工 split（避免 strings.Fields 的多空白语义 + 减少分配）。
func splitTrimNonEmpty(s, sep string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if i+len(sep) <= len(s) && s[i:i+len(sep)] == sep {
			if i > start {
				tok := s[start:i]
				tok = trimASCII(tok)
				if tok != "" {
					out = append(out, tok)
				}
			}
			start = i + len(sep)
		}
	}
	if start < len(s) {
		tok := trimASCII(s[start:])
		if tok != "" {
			out = append(out, tok)
		}
	}
	return out
}

func trimASCII(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t') {
		s = s[:len(s)-1]
	}
	return s
}

// ManualSelector 是 Phase 1 唯一实现的选择器。给定 Policy，输出：
//   - "off"/""            → nil（不压缩）
//   - "all"               → Registry 全部 strategy，按注册顺序
//   - "lite,caveman,..."  → 只跑列表中的策略，列表顺序即执行顺序
type ManualSelector struct {
	policy Policy
}

// NewManualSelector 构造一个 ManualSelector。policy 来自 ResolvePolicy(env)
// 或 settings.Global.Spec("compression.policy")。
func NewManualSelector(policy Policy) *ManualSelector {
	return &ManualSelector{policy: policy}
}

// Select 实现 Selector 接口。
//
// 行为：
//   - 若 policy.Names 为空且 policy.UnknownMode != "ignore"  → nil（off）
//   - 若 policy 为 "all" (UnknownMode=="ignore" 且 Names 为空)
//     → 全部 strategy，按注册顺序（不过滤 Enabled；
//     Runner 负责 disable 跳过，便于动态启用）
//   - 否则                    → 按 policy.Names 顺序过滤，未注册项跳过
func (m *ManualSelector) Select(_ context.Context, all []Strategy) []Strategy {
	// off 语义：空 Names 且 UnknownMode 非 "ignore" 标记 → 关闭。
	if len(m.policy.Names) == 0 && m.policy.UnknownMode != "ignore" {
		return nil
	}
	// "all" 语义：空 Names + UnknownMode=="ignore" → 返回注册顺序的全部。
	if len(m.policy.Names) == 0 {
		out := make([]Strategy, len(all))
		copy(out, all)
		return out
	}

	// Named policy: 严格按 policy 列表顺序返回，过滤掉 unknown。
	// Selector 不做 Enabled() 过滤 — Runner 会在 Apply 入口判断跳过，
	// 这样 Selector 的输出是"声明意图"，Runner 才是"动态执行集合"。
	index := make(map[string]Strategy, len(all))
	for _, s := range all {
		index[s.Name()] = s
	}
	seen := make(map[string]bool, len(m.policy.Names))
	out := make([]Strategy, 0, len(m.policy.Names))
	for _, name := range m.policy.Names {
		if seen[name] {
			continue
		}
		seen[name] = true
		s, ok := index[name]
		if !ok {
			// Unknown strategy: Phase 1 跳过，保留接口稳定性。
			continue
		}
		out = append(out, s)
	}
	return out
}

// Compile-time interface check.
var _ Selector = (*ManualSelector)(nil)

// errPolicyFmt is exported so future error wrapping can be tested.
var errPolicyFmt = fmt.Errorf("strategy: invalid policy spec")

// Sentinel for error matching in tests.
var ErrInvalidPolicySpec = errPolicyFmt
