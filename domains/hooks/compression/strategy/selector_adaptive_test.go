// Package strategy tests (GW-10 Phase 2) - AdaptiveSelector
//
// 覆盖自适应升级选择器的核心不变量：预算未知→不压缩、预算内→绝不过度压缩、
// 升级阶梯按 CostTier 升序、预算满足即停止、MaxStages 上限、reduction=1.0 跳过。
// 另含与 Runner 的真实集成测试（用三个真实 adapter）。
package strategy

import (
	"context"
	"strings"
	"testing"
)

// profileStub 在 stubStrategy 基础上实现 EscalationProfile，便于精确控制
// reduction factor 与 cost tier（stubStrategy 本身不实现该接口）。
type profileStub struct {
	*stubStrategy
	rf   float64
	tier int
}

func (p *profileStub) ReductionFactor() float64 { return p.rf }
func (p *profileStub) CostTier() int            { return p.tier }

func newProfileStub(name string, enabled bool, rf float64, tier int) *profileStub {
	return &profileStub{stubStrategy: &stubStrategy{name: name, enabled: enabled}, rf: rf, tier: tier}
}

// ── 基础不变量 ─────────────────────────────────────────────────────────

func TestAdaptiveSelector_NoBudgetReturnsNil(t *testing.T) {
	sel := NewAdaptiveSelector(AdaptiveConfig{
		NeverOverCompress: true,
		BudgetFn:          func([]byte) int { return 0 }, // 预算未知
	})
	all := []Strategy{newProfileStub("a", true, 0.5, 0)}
	got := sel.Select(context.Background(), all, []byte(strings.Repeat("x", 1000)))
	if got != nil {
		t.Errorf("no-budget adaptive selector must return nil; got %v", namesOf(got))
	}
}

func TestAdaptiveSelector_WithinBudgetNeverOverCompress(t *testing.T) {
	sel := NewAdaptiveSelector(AdaptiveConfig{
		NeverOverCompress: true,
		BudgetFn:          func([]byte) int { return 100 }, // 预算 100 字节
	})
	// body 仅 40 字节，已 <= 预算 → 绝不压缩。
	all := []Strategy{newProfileStub("a", true, 0.5, 0)}
	got := sel.Select(context.Background(), all, []byte(strings.Repeat("x", 40)))
	if got != nil {
		t.Errorf("within-budget + never_over_compress must return nil; got %v", namesOf(got))
	}
}

func TestAdaptiveSelector_WithinBudgetAllowsLosslessWhenDisabled(t *testing.T) {
	// NeverOverCompress=false 且预算内：仍不应纳入任何 reduction>=1 的策略。
	sel := NewAdaptiveSelector(AdaptiveConfig{
		NeverOverCompress: false,
		BudgetFn:          func([]byte) int { return 100 },
	})
	// 只有 reduction=1.0 的策略（无预期压缩）→ 不应被纳入。
	all := []Strategy{newProfileStub("a", true, 1.0, 0)}
	got := sel.Select(context.Background(), all, []byte(strings.Repeat("x", 40)))
	if len(got) != 0 {
		t.Errorf("only reduction=1.0 strategy should be skipped; got %v", namesOf(got))
	}
}

// ── 升级阶梯 ───────────────────────────────────────────────────────────

func TestAdaptiveSelector_EscalationOrderCheapestFirst(t *testing.T) {
	// 三个策略：caveman(tier2,0.7) / toolfocused(tier1,0.85) / lite(tier0,0.92)。
	// body=1000，预算=100。需要逐级累加压缩直到预估 <= 100。
	//   1000 → lite 920 → toolfocused 782 → caveman 547 → 仍未到 100，全纳入。
	// 顺序必须按 tier 升序：lite → toolfocused → caveman。
	sel := NewAdaptiveSelector(AdaptiveConfig{
		NeverOverCompress: true,
		BudgetFn:          func([]byte) int { return 100 },
	})
	all := []Strategy{
		newProfileStub("caveman", true, 0.70, 2),
		newProfileStub("toolfocused", true, 0.85, 1),
		newProfileStub("lite", true, 0.92, 0),
	}
	got := sel.Select(context.Background(), all, []byte(strings.Repeat("x", 1000)))
	want := []string{"lite", "toolfocused", "caveman"}
	if len(got) != len(want) {
		t.Fatalf("ladder size = %d, want %d (%v)", len(got), len(want), namesOf(got))
	}
	for i, w := range want {
		if got[i].Name() != w {
			t.Errorf("ladder[%d] = %q, want %q (full=%v)", i, got[i].Name(), w, namesOf(got))
		}
	}
}

func TestAdaptiveSelector_StopsWhenBudgetMet(t *testing.T) {
	// body=1000，预算=550，默认 targetRatio=0.8，因此目标为 440。
	// lite(0.4) 把 1000→400 <=440，应只选 lite。
	sel := NewAdaptiveSelector(AdaptiveConfig{
		NeverOverCompress: true,
		BudgetFn:          func([]byte) int { return 550 },
	})
	all := []Strategy{
		newProfileStub("lite", true, 0.4, 0),
		newProfileStub("toolfocused", true, 0.85, 1),
		newProfileStub("caveman", true, 0.70, 2),
	}
	got := sel.Select(context.Background(), all, []byte(strings.Repeat("x", 1000)))
	if len(got) != 1 || got[0].Name() != "lite" {
		t.Errorf("should stop at lite only; got %v", namesOf(got))
	}
}

func TestAdaptiveSelector_MaxStagesCap(t *testing.T) {
	sel := NewAdaptiveSelector(AdaptiveConfig{
		NeverOverCompress: true,
		MaxStages:         1,
		BudgetFn:          func([]byte) int { return 10 }, // 极紧预算，理论上需全档
	})
	all := []Strategy{
		newProfileStub("lite", true, 0.92, 0),
		newProfileStub("toolfocused", true, 0.85, 1),
		newProfileStub("caveman", true, 0.70, 2),
	}
	got := sel.Select(context.Background(), all, []byte(strings.Repeat("x", 1000)))
	if len(got) != 1 {
		t.Errorf("MaxStages=1 must cap ladder at 1; got %v", namesOf(got))
	}
}

func TestAdaptiveSelector_SkipsReductionOne(t *testing.T) {
	// 一个 reduction=1.0（无压缩）的已启用策略应被跳过，不阻塞阶梯。
	sel := NewAdaptiveSelector(AdaptiveConfig{
		NeverOverCompress: true,
		BudgetFn:          func([]byte) int { return 100 },
	})
	all := []Strategy{
		newProfileStub("noopish", true, 1.0, 0), // 跳过
		newProfileStub("lite", true, 0.5, 1),    // 纳入
		newProfileStub("caveman", true, 0.7, 2), // 纳入
	}
	got := sel.Select(context.Background(), all, []byte(strings.Repeat("x", 1000)))
	if len(got) != 2 || got[0].Name() != "lite" || got[1].Name() != "caveman" {
		t.Errorf("reduction=1.0 must be skipped; got %v", namesOf(got))
	}
}

func TestAdaptiveSelector_DisabledSkipped(t *testing.T) {
	sel := NewAdaptiveSelector(AdaptiveConfig{
		NeverOverCompress: true,
		BudgetFn:          func([]byte) int { return 100 },
	})
	all := []Strategy{
		newProfileStub("lite", false, 0.5, 0), // disabled → 跳过
		newProfileStub("caveman", true, 0.7, 2),
	}
	got := sel.Select(context.Background(), all, []byte(strings.Repeat("x", 1000)))
	if len(got) != 1 || got[0].Name() != "caveman" {
		t.Errorf("disabled strategy must be skipped; got %v", namesOf(got))
	}
}

// ── Adapter EscalationProfile 契约 ─────────────────────────────────────

func TestAdapters_ExposeEscalationProfile(t *testing.T) {
	cases := []struct {
		s    Strategy
		rf   float64
		tier int
	}{
		{&LiteAdapter{On: true}, 0.92, 0},
		{&ToolFocusedAdapter{On: true}, 0.85, 1},
		{&CavemanAdapter{On: true}, 0.70, 2},
	}
	for _, c := range cases {
		ep, ok := c.s.(EscalationProfile)
		if !ok {
			t.Fatalf("%s must implement EscalationProfile", c.s.Name())
		}
		if got := ep.ReductionFactor(); got != c.rf {
			t.Errorf("%s.ReductionFactor() = %.2f, want %.2f", c.s.Name(), got, c.rf)
		}
		if got := ep.CostTier(); got != c.tier {
			t.Errorf("%s.CostTier() = %d, want %d", c.s.Name(), got, c.tier)
		}
	}
}

// ── 与 Runner 的真实集成 ───────────────────────────────────────────────

func TestAdaptiveSelector_IntegrationThroughRunner(t *testing.T) {
	// 构造真实注册表（三个 adapter 全启用），自适应选择器预算收紧到很小，
	// 给定同时含空白噪声 + 工具结果 + 客套话的 body。验证：
	//   1. Runner 选中非空子集；2. 输出字节 <= 输入字节；3. 无 panic。
	reg := NewRegistry()
	reg.MustRegister(&LiteAdapter{On: true})
	reg.MustRegister(&CavemanAdapter{On: true})
	reg.MustRegister(&ToolFocusedAdapter{On: true})
	body := mustMarshal(t, map[string]any{
		"messages": []any{
			map[string]any{"role": "system", "content": "You are a helpful coding assistant."},
			map[string]any{"role": "user", "content": "Sure, I would be happy to help. Could you please tell me more?"},
			map[string]any{"role": "assistant", "content": "Let me check the file."},
			map[string]any{"role": "tool", "tool_call_id": "call_1", "content": strings.Repeat("import mod\n", 40)},
		},
	})
	// 把 budget 设为 body 的 50%，强制升级到 caveman 档。
	sel := NewAdaptiveSelector(AdaptiveConfig{
		NeverOverCompress: true,
		BudgetFn:          func(b []byte) int { return len(b) / 2 },
	})
	runner := NewRunner(reg)
	out, stats, err := runner.RunWithBody(context.Background(), sel, body)
	if err != nil {
		t.Fatalf("RunWithBody: %v", err)
	}
	if len(stats.AppliedNames) == 0 {
		t.Errorf("adaptive selector should have applied at least one strategy")
	}
	if len(out) > len(body) {
		t.Errorf("output %d bytes > input %d bytes; NeverWorse guard failed", len(out), len(body))
	}
	// 工具结果应被压缩（出现 elision 标记或明显缩短）。
	if !strings.Contains(string(out), "lines elided") && len(out) >= len(body) {
		t.Errorf("expected tool result compression; out=%q", out)
	}
}

func TestManualSelector_IgnoresBody(t *testing.T) {
	// ManualSelector 不读 body：空 body 也应按 Policy 返回。
	sel := NewManualSelector(Policy{Names: []string{"a"}, UnknownMode: "ignore"})
	all := []Strategy{&stubStrategy{name: "a", enabled: true}}
	got := sel.Select(context.Background(), all, nil)
	if len(got) != 1 || got[0].Name() != "a" {
		t.Errorf("manual selector must ignore body; got %v", namesOf(got))
	}
}
