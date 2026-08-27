// compressor_strategy_test.go (GW-10 Phase 1)
//
// 验证 Compressor.RunStrategies 把现有 LiteStageEnabled/CavemanStageEnabled/
// ToolFocusedStageEnabled 三个 feature flag 正确传到 strategy.Adapter 上，
// 并走完整压缩链（Adapter → stage 包 → Runner）。
package compression

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/compression/strategy"
)

func mustMarshalStrategy(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// TestCompressor_RunStrategies_AllFlagsOff_NoOp 验证三个 flag 都关时
// RunStrategies 不会触发任何 adapter（全部 AppliedNames 为空）。
func TestCompressor_RunStrategies_AllFlagsOff_NoOp(t *testing.T) {
	c := NewCompressor()
	c.LiteStageEnabled = false
	c.CavemanStageEnabled = false
	c.ToolFocusedStageEnabled = false

	body := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	out, stats, err := c.RunStrategies(context.Background(), nil, body)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if string(out) != string(body) {
		t.Errorf("nil selector must return original body")
	}
	if stats.BytesOut != len(body) {
		t.Errorf("BytesOut should equal BytesIn on off path; got %d vs %d", stats.BytesOut, len(body))
	}
}

// TestCompressor_RunStrategies_ToolFocusedFlagPropagates 验证开启
// ToolFocusedStageEnabled 后，selector 选到 toolfocused，Runner 跑通 Adapter
// 调用 toolfocused 包纯函数，body 真的被压缩。
func TestCompressor_RunStrategies_ToolFocusedFlagPropagates(t *testing.T) {
	c := NewCompressor()
	c.ToolFocusedStageEnabled = true

	var lines []string
	for i := 0; i < 40; i++ {
		lines = append(lines, "const x = 1")
	}
	body := mustMarshalStrategy(t, map[string]any{
		"messages": []any{
			map[string]any{"role": "tool", "content": strings.Join(lines, "\n")},
		},
	})

	sel, err := c.NewManualSelectorFromSpec("toolfocused")
	if err != nil {
		t.Fatalf("selector spec: %v", err)
	}
	out, stats, err := c.RunStrategies(context.Background(), sel, body)
	if err != nil {
		t.Fatalf("RunStrategies: %v", err)
	}
	if len(stats.AppliedNames) != 1 || stats.AppliedNames[0] != strategy.NameToolFocused {
		t.Errorf("AppliedNames = %v, want [%s]", stats.AppliedNames, strategy.NameToolFocused)
	}
	if !strings.Contains(string(out), "lines elided") {
		t.Errorf("toolfocused should fire and elide middle; got %q", out)
	}
	if len(out) >= len(body) {
		t.Errorf("output should be smaller; before=%d after=%d", len(body), len(out))
	}
}

// TestCompressor_RunStrategies_LiteFlagPropagates 验证 LiteStageEnabled flag
// 传到 LiteAdapter 后能触发 lite 折叠空白。
func TestCompressor_RunStrategies_LiteFlagPropagates(t *testing.T) {
	c := NewCompressor()
	c.LiteStageEnabled = true

	body := mustMarshalStrategy(t, map[string]any{
		"messages": []any{
			map[string]any{"role": "user", "content": "hello world   \n\n\n\n"},
		},
	})
	sel, err := c.NewManualSelectorFromSpec("lite")
	if err != nil {
		t.Fatalf("selector spec: %v", err)
	}
	out, stats, err := c.RunStrategies(context.Background(), sel, body)
	if err != nil {
		t.Fatalf("RunStrategies: %v", err)
	}
	if len(stats.AppliedNames) != 1 || stats.AppliedNames[0] != strategy.NameLite {
		t.Errorf("AppliedNames = %v, want [%s]", stats.AppliedNames, strategy.NameLite)
	}
	if strings.Contains(string(out), "\n\n\n\n") {
		t.Errorf("lite should fold 4+ newlines; got %q", out)
	}
}

// TestCompressor_RunStrategies_AllFlagChainOrder 验证三 flag 全开时 Runner
// 严格按注册顺序（lite → caveman → toolfocused）跑，每段 AppliedNames 都被记入。
func TestCompressor_RunStrategies_AllFlagChainOrder(t *testing.T) {
	c := NewCompressor()
	c.LiteStageEnabled = true
	c.CavemanStageEnabled = true
	c.ToolFocusedStageEnabled = true

	// 构造一个既含大量空白（lite 命中）又含 pleasantries（caveman 命中）
	// 又含 40 行 tool result（toolfocused 命中）的复合 body。
	var lines []string
	for i := 0; i < 40; i++ {
		lines = append(lines, "import mod")
	}
	body := mustMarshalStrategy(t, map[string]any{
		"messages": []any{
			map[string]any{"role": "user", "content": "Sure, I would be happy to help. Please could you kindly   \n\n\n\n look at this?"},
			map[string]any{"role": "tool", "content": strings.Join(lines, "\n")},
		},
	})

	sel, err := c.NewManualSelectorFromSpec("lite,caveman,toolfocused")
	if err != nil {
		t.Fatalf("selector spec: %v", err)
	}
	out, stats, err := c.RunStrategies(context.Background(), sel, body)
	if err != nil {
		t.Fatalf("RunStrategies: %v", err)
	}
	if len(stats.AppliedNames) != 3 {
		t.Errorf("expected 3 AppliedNames; got %v", stats.AppliedNames)
	}
	if len(out) >= len(body) {
		t.Errorf("output should be smaller after 3-stage chain; before=%d after=%d", len(body), len(out))
	}
}

// TestCompressor_ParsePolicySpec_Forwarding 验证 Compressor.ParsePolicySpec
// 与 strategy.ResolvePolicy 行为一致（薄封装，无破坏性差异）。
func TestCompressor_ParsePolicySpec_Forwarding(t *testing.T) {
	c := NewCompressor()
	p, err := c.ParsePolicySpec("lite,caveman")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(p.Names) != 2 || p.Names[0] != "lite" || p.Names[1] != "caveman" {
		t.Errorf("policy.Names = %v", p.Names)
	}

	p2, _ := c.ParsePolicySpec("")
	if len(p2.Names) != 0 {
		t.Errorf("empty spec should yield empty Names; got %v", p2.Names)
	}
}
