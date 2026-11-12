package harness

import (
	"context"
	"strings"
	"testing"

	optimizer "github.com/kaixuan/llm-gateway-go/internal/prompt-optimizer"
)

// TestInjectorAddsAllElements 钉住 Harness 注入器产出全部要素。
func TestInjectorAddsAllElements(t *testing.T) {
	inj := NewInjector(DefaultInjectorConfig(), NewMockToolRegistry(), NewMockSafetyRules())
	ctx := context.Background()

	out, injected, err := inj.Inject(ctx, "基础提示词", optimizer.StrategyContext{})
	if err != nil {
		t.Fatalf("Inject: %v", err)
	}

	for _, want := range []string{"# 可用工具", "# 安全规则", "# 错误处理"} {
		if !strings.Contains(out, want) {
			t.Errorf("输出缺少章节 %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "基础提示词") {
		t.Errorf("原始提示词应被保留:\n%s", out)
	}

	if len(injected) == 0 {
		t.Error("应记录已注入的要素")
	}
	for _, want := range []string{"tools", "safety", "error_handling"} {
		found := false
		for _, got := range injected {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("注入记录应含 %q，实际 %v", want, injected)
		}
	}
}

// TestInjectorRespectsDisabledFlags 钉住关闭的开关真的不注入。
func TestInjectorRespectsDisabledFlags(t *testing.T) {
	cfg := InjectorConfig{InjectTools: false, InjectSafety: false, InjectErrors: false}
	inj := NewInjector(cfg, NewMockToolRegistry(), NewMockSafetyRules())
	ctx := context.Background()

	original := "基础提示词"
	out, injected, err := inj.Inject(ctx, original, optimizer.StrategyContext{})
	if err != nil {
		t.Fatalf("Inject: %v", err)
	}
	if out != original {
		t.Errorf("全部关闭时不应改动提示词:\ngot  %q\nwant %q", out, original)
	}
	if len(injected) != 0 {
		t.Errorf("全部关闭时注入记录应为空，实际 %v", injected)
	}
}

// TestInjectorUsesStrategyContextTools 钉住优先使用上下文里给的工具。
func TestInjectorUsesStrategyContextTools(t *testing.T) {
	inj := NewInjector(DefaultInjectorConfig(), NewMockToolRegistry(), NewMockSafetyRules())
	ctx := context.Background()

	out, _, err := inj.Inject(ctx, "提示词", optimizer.StrategyContext{
		Tools: []optimizer.ToolDefinition{
			{Name: "custom_tool", Description: "自定义工具"},
		},
	})
	if err != nil {
		t.Fatalf("Inject: %v", err)
	}
	if !strings.Contains(out, "custom_tool") {
		t.Errorf("应使用上下文提供的工具:\n%s", out)
	}
	if strings.Contains(out, "### calculate") {
		t.Errorf("不应混入注册表里的其它工具:\n%s", out)
	}
}

// TestSafetyRulesAreDomainSpecific 钉住安全规则随领域变化。
func TestSafetyRulesAreDomainSpecific(t *testing.T) {
	rules := NewMockSafetyRules()
	ctx := context.Background()

	medical, err := rules.GetRules(ctx, "medical")
	if err != nil {
		t.Fatalf("GetRules: %v", err)
	}
	general, err := rules.GetRules(ctx, "general")
	if err != nil {
		t.Fatalf("GetRules: %v", err)
	}

	if len(medical) <= len(general) {
		t.Errorf("医疗领域规则应更多: medical=%d general=%d", len(medical), len(general))
	}
	if strings.Join(medical, "|") == strings.Join(general, "|") {
		t.Error("不同领域的安全规则应不同")
	}
}

// TestToolRegistryReturnsTools 钉住工具注册表返回带约束的工具。
func TestToolRegistryReturnsTools(t *testing.T) {
	reg := NewMockToolRegistry()

	tools, err := reg.GetAvailableTools(context.Background())
	if err != nil {
		t.Fatalf("GetAvailableTools: %v", err)
	}
	if len(tools) == 0 {
		t.Fatal("应返回至少一个工具")
	}
	for _, tool := range tools {
		if tool.Name == "" || tool.Description == "" {
			t.Errorf("工具缺少名称或描述: %+v", tool)
		}
	}
}

// TestBuilderOrdersSections 钉住章节按 Order 排序（安全规则在工具之前）。
func TestBuilderOrdersSections(t *testing.T) {
	b := &HarnessBuilder{basePrompt: "BASE"}
	b.AddTools([]optimizer.ToolDefinition{{Name: "t", Description: "d"}})
	b.AddSafetyRules([]string{"规则1"})

	out := b.Build()

	toolIdx := strings.Index(out, "# 可用工具")
	safetyIdx := strings.Index(out, "# 安全规则")
	if toolIdx < 0 || safetyIdx < 0 {
		t.Fatalf("章节缺失:\n%s", out)
	}
	if safetyIdx > toolIdx {
		t.Errorf("安全规则(5)应排在工具(10)之前:\n%s", out)
	}
	if !strings.HasPrefix(out, "BASE") {
		t.Errorf("基础提示词应在最前:\n%s", out)
	}
}

// TestBuilderEmptySectionsReturnsBase 钉住无章节时原样返回。
func TestBuilderEmptySectionsReturnsBase(t *testing.T) {
	b := &HarnessBuilder{basePrompt: "BASE"}
	if got := b.Build(); got != "BASE" {
		t.Errorf("应原样返回，实际 %q", got)
	}
}
