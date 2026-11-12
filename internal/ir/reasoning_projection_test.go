package ir

// reasoning_projection_test.go — Slice 2（2026-11-12）消费生效门。
//
// 与 reasoningdialect 包的契约门互补：这里钉「ir 生产路径真的从注册表
// 取方言并生效」——种子被破坏 / 映射表漂移 / loss 转发断裂，此处红。
import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/paramreg"
	"github.com/kaixuan/llm-gateway-go/internal/reasoningdialect"
	"github.com/kaixuan/llm-gateway-go/internal/reasonnorm"
)

// ① openAIWireFamilyKey 映射表钉：五族开放，其余保持 loss 路径
// （kimi/grok/mistral/vllm/ollama/unknown——2026-09-18 事故口径）。
func TestOpenAIWireFamilyKeyTable(t *testing.T) {
	open := map[paramreg.Dialect]string{
		paramreg.DialectMiniMax:  reasoningdialect.FamilyMiniMax,
		paramreg.DialectDeepSeek: reasoningdialect.FamilyDeepSeek,
		paramreg.DialectGLM:      reasoningdialect.FamilyGLM,
		paramreg.DialectArk:      reasoningdialect.FamilyArk,
		paramreg.DialectQwen:     reasoningdialect.FamilyQwen,
	}
	for dst, want := range open {
		got, ok := openAIWireFamilyKey(dst)
		if !ok || got != want {
			t.Fatalf("族 %q 映射漂移：got (%q,%v) want (%q,true)", dst, got, ok, want)
		}
	}
	for _, dst := range []paramreg.Dialect{
		paramreg.DialectUnknown, paramreg.DialectNVIDIA, "kimi", "grok", "mistral", "vllm", "ollama", "",
	} {
		if _, ok := openAIWireFamilyKey(dst); ok {
			t.Fatalf("族 %q 不得映射（无已验证 openai-wire 投影，必须保持 loss 路径）", dst)
		}
	}
}

// ② 消费生效门：注册表种子的 RenderIntent 真实产出 qwen 形态
// （enable_thinking + thinking_budget；已存在键不覆盖）。种子退化 ⇒ 此门红。
func TestRegistrySeedRendersQwenShape(t *testing.T) {
	ip, ok := reasoningdialect.ForVendorFamily(reasoningdialect.FamilyQwen).(reasoningdialect.IntentProjector)
	if !ok {
		t.Fatalf("qwen 种子必须实现 IntentProjector（生产 applyThinkingToOpenAIChat 依赖）")
	}
	out := map[string]any{"reasoning_effort": "low"} // IR 既有输出不被覆盖
	rendered := ip.RenderIntent(reasonnorm.Intent{Mode: reasonnorm.ModeEnabled, BudgetTokens: 2048}, 4096, out)
	if !rendered {
		t.Fatalf("qwen 渲染必须生效")
	}
	if v, has := out["enable_thinking"]; !has || v != true {
		t.Fatalf("enable_thinking 必须写入 true，got %v (has=%v)", out["enable_thinking"], has)
	}
	if v, has := out["thinking_budget"]; !has || v != 2048 {
		t.Fatalf("thinking_budget 必须写入 2048，got %v", out["thinking_budget"])
	}
	if out["reasoning_effort"] != "low" {
		t.Fatalf("已存在键不得覆盖（IR 输出赢），got %v", out["reasoning_effort"])
	}
}

// ③ loss 转发生效门：reportProjectionLosses 把方言 Loss 逐条转发为
// ir_protocol_loss 事件（fieldPath/reason/target 与历史内联逐字一致）。
func TestReportProjectionLossesForwardsToProtocolLoss(t *testing.T) {
	var got []AnomalyEvent
	prev := SetAnomalyReporter(func(ev AnomalyEvent) { got = append(got, ev) })
	defer SetAnomalyReporter(prev)

	req := &InternalRequest{
		Model:          "qwen3:4b",
		SourceProtocol: ProtocolOpenAIChat,
		Reasoning:      &ReasoningConfig{Type: "enabled", BudgetTokens: reasonBudgetPtr(8192)},
	}
	fam := reasoningdialect.ForVendorFamily(reasoningdialect.FamilyOllama)
	dp := fam.(reasoningdialect.DerivedProjector)
	reportProjectionLosses(req, dp.ProjectDerived(reasoningdialect.DerivedRequest{
		Type: "enabled", BudgetTokens: req.Reasoning.BudgetTokens, Effort: "high",
		Model: req.Model, SourceProtocol: req.SourceProtocol,
	}))

	// type + effort + budget 三条按设计丢弃（r.Type/effort 非空、budget 非nil）。
	if len(got) != 3 {
		t.Fatalf("ollama 丢弃面披露条数 = %d（want 3：type/effort/budget）", len(got))
	}
	wantPaths := map[string]bool{"reasoning.type": false, "reasoning.effort": false, "reasoning.budget_tokens": false}
	for _, ev := range got {
		if ev.AnomalyType != AnomalyProtocolLoss {
			t.Fatalf("必须是 ir_protocol_loss，got %q", ev.AnomalyType)
		}
		if ev.Reason != "ollama_reasoning_drop" {
			t.Fatalf("reason 必须逐字保持，got %q", ev.Reason)
		}
		if ev.TargetProtocol != ProtocolOllamaChat {
			t.Fatalf("target 必须 ollama-chat，got %q", ev.TargetProtocol)
		}
		if _, known := wantPaths[ev.FieldPath]; !known {
			t.Fatalf("意外 fieldPath %q", ev.FieldPath)
		}
		wantPaths[ev.FieldPath] = true
		if ev.Metadata["disposition"] != "by_design_no_request_side_surface" || ev.Metadata["family"] != "ollama" {
			t.Fatalf("语义化 metadata 缺失（family/disposition），got %v", ev.Metadata)
		}
	}
	for fp, seen := range wantPaths {
		if !seen {
			t.Fatalf("字段 %s 的披露缺失", fp)
		}
	}
}

func reasonBudgetPtr(i int) *int { return &i }
