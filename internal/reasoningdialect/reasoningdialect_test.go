package reasoningdialect_test

// 外部测试包：需要 import internal/ir 做协议键对齐断言（内部测试包会与
// ir → reasoningdialect 形成循环）。模式同 Slice 1 的 tooldialect 契约门。
//
// ★ 门清单（Slice 2，2026-11-12）：
//   ① 键对齐契约门（家族键 ↔ reasoncap 常量；协议键 ↔ ir 常量）
//   ② 仲裁优先级矩阵门（Arbitrate 单源判表——取代四份平行实现的反漂移锚）
//   ③ 跨家族仲裁一致性门（anthropic/gemini/openai 三方言对同一输入的
//     「谁赢」结论必须一致；wire 形状差异是方言语义，不是漂移）
//   ④ OCP 演示门（虚构家族注册即生效；核心零改动）
//   ⑤ ir.anomaly 语义化投影门（loss 载体带稳定 reason + family/disposition
//     metadata；去重键不含 metadata ⇒ 富化不改变去重）
//   ⑥ noop 兜底 fail-closed 门（未注册族不发射、不发明 wire、不供投影）
//   ⑦ 内置种子完整性门（九个家族键全部有种子——未装配即正确）
import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/ir"
	"github.com/kaixuan/llm-gateway-go/internal/reasoncap"
	"github.com/kaixuan/llm-gateway-go/internal/reasoningdialect"
	"github.com/kaixuan/llm-gateway-go/internal/reasonnorm"
)

// ① 家族键 ↔ reasoncap.Dialect 常量同值。
func TestFamilyKeysAlignWithReasonCap(t *testing.T) {
	cases := []struct {
		key    string
		const_ reasoncap.Dialect
		name   string
	}{
		{reasoningdialect.FamilyAnthropic, reasoncap.DialectAnthropic, "anthropic"},
		{reasoningdialect.FamilyGemini25, reasoncap.DialectGemini25, "gemini25"},
		{reasoningdialect.FamilyOpenAI, reasoncap.DialectOpenAI, "openai"},
		{reasoningdialect.FamilyOllama, reasoncap.DialectOllama, "ollama"},
		{reasoningdialect.FamilyMiniMax, reasoncap.DialectMiniMax, "minimax"},
		{reasoningdialect.FamilyDeepSeek, reasoncap.DialectDeepSeek, "deepseek"},
		{reasoningdialect.FamilyGLM, reasoncap.DialectGLM, "glm"},
		{reasoningdialect.FamilyArk, reasoncap.DialectArk, "ark"},
		{reasoningdialect.FamilyQwen, reasoncap.DialectQwen, "qwen"},
	}
	for _, c := range cases {
		if c.key != string(c.const_) {
			t.Fatalf("家族键漂移：%s=%q reasoncap=%q（两侧必须同值）", c.name, c.key, string(c.const_))
		}
	}
}

// ①b 协议键 ↔ internal/ir 常量同值（Loss.TargetProtocol 载体）。
func TestProtocolConstantsAlignWithIR(t *testing.T) {
	if reasoningdialect.ProtocolAnthropicMessages != ir.ProtocolAnthropicMessages {
		t.Fatalf("协议键漂移：reasoningdialect=%q ir=%q", reasoningdialect.ProtocolAnthropicMessages, ir.ProtocolAnthropicMessages)
	}
	if reasoningdialect.ProtocolGeminiGenerate != ir.ProtocolGeminiGenerate {
		t.Fatalf("协议键漂移：reasoningdialect=%q ir=%q", reasoningdialect.ProtocolGeminiGenerate, ir.ProtocolGeminiGenerate)
	}
	if reasoningdialect.ProtocolOllamaChat != ir.ProtocolOllamaChat {
		t.Fatalf("协议键漂移：reasoningdialect=%q ir=%q", reasoningdialect.ProtocolOllamaChat, ir.ProtocolOllamaChat)
	}
	if reasoningdialect.ProtocolOpenAIChat != ir.ProtocolOpenAIChat {
		t.Fatalf("协议键漂移：reasoningdialect=%q ir=%q", reasoningdialect.ProtocolOpenAIChat, ir.ProtocolOpenAIChat)
	}
}

// ② 仲裁优先级矩阵门。判表即单源语义（取代 reasoningDisabled 死权威 +
// anthropic/gemini 两份内联镜像）：
//
//	A1 Type=enabled 命中指令（哪怕无预算）；
//	A2 Type=disabled 命中指令；
//	A3 Type=adaptive 命中指令（summary 词汇除外——见 A4）；
//	A4 Type=auto（Responses summary 词汇撞进 Type）**不是**指令 ⇒ 落预算/effort；
//	B1 显式预算 0 = 显式关闭；
//	B2 显式正预算赢过关闭族 effort（budget 在 effort 之前）；
//	C1 关闭族 effort（none/disabled，含大小写/空白变体）；
//	C2 可换算 effort（high→4096）；
//	C3 不可换算 effort（无预算）；
//	D1 全空 = 无意图。
func TestArbitratePrecedenceMatrix(t *testing.T) {
	i42 := 42
	i0 := 0
	cases := []struct {
		name              string
		typ               string
		budget            *int
		effort            string
		wantKind          reasoningdialect.Kind
		wantDirective     string
		wantExplicit      bool
		wantBudget        int
		wantDisableEffort bool
		wantFromEffort    bool
	}{
		{"A1_enabled_directive", "enabled", nil, "", reasoningdialect.KindDirective, "enabled", false, -1, false, false},
		{"A1b_enabled_with_budget", "enabled", &i42, "", reasoningdialect.KindDirective, "enabled", true, 42, false, false},
		// disabled+预算：Type 赢，预算作为原始事实随行（方言层对 disabled
		// 不发射 budget_tokens——形状断言在门 ③）。
		{"A2_disabled_directive", "disabled", &i42, "", reasoningdialect.KindDirective, "disabled", true, 42, false, false},
		{"A3_adaptive_directive", "adaptive", nil, "", reasoningdialect.KindDirective, "adaptive", false, -1, false, false},
		{"A4_summary_vocab_not_directive", "auto", &i42, "", reasoningdialect.KindBudget, "", true, 42, false, false},
		{"B1_budget_zero_disables", "", &i0, "none", reasoningdialect.KindBudget, "", true, 0, false, false},
		{"B2_budget_beats_disable_effort", "", &i42, "none", reasoningdialect.KindBudget, "", true, 42, false, false},
		{"C1_disable_effort", "", nil, "none", reasoningdialect.KindEffort, "", false, -1, true, false},
		{"C1b_disable_effort_spaced", "", nil, " DISABLED ", reasoningdialect.KindEffort, "", false, -1, true, false},
		{"C2_effort_to_budget", "", nil, "high", reasoningdialect.KindEffort, "", false, 4096, false, true},
		{"C3_effort_unmapped", "", nil, "turbo", reasoningdialect.KindEffort, "", false, -1, false, false},
		{"D1_none", "", nil, "", reasoningdialect.KindNone, "", false, 0, false, false}, // KindNone 无消费方，Budget 为零值
	}
	for _, tc := range cases {
		got := reasoningdialect.Arbitrate(tc.typ, tc.budget, tc.effort)
		if got.Kind != tc.wantKind || got.Directive != tc.wantDirective ||
			got.ExplicitBudget != tc.wantExplicit || got.Budget != tc.wantBudget ||
			got.DisableEffort != tc.wantDisableEffort || got.BudgetFromEffort != tc.wantFromEffort {
			t.Fatalf("%s: got %+v want {kind=%d directive=%q explicit=%v budget=%d disable=%v fromEffort=%v}",
				tc.name, got, tc.wantKind, tc.wantDirective, tc.wantExplicit, tc.wantBudget, tc.wantDisableEffort, tc.wantFromEffort)
		}
	}
}

// ③ 跨家族仲裁一致性门：三方言对同一输入的「谁赢」结论一致
// （wire 形状差异——anthropic 8192 兜底 vs gemini -1 动态默认 vs openai
// effort 原生——是家族策略，允许不同；「关闭判定」必须一致）。
func TestFamiliesAgreeOnDisableVerdict(t *testing.T) {
	zero := 0
	i9999 := 9999
	// 输入：显式零预算（= 关）。三方言都必须按「关」处置：
	//   anthropic → thinking.type=disabled（无 budget_tokens）
	//   gemini    → 支持 0 哨兵的模型发射 thinkingBudget=0
	//   openai    → 不发射 reasoning_effort
	anth := reasoningdialect.ForVendorFamily(reasoningdialect.FamilyAnthropic).(reasoningdialect.DerivedProjector)
	gem := reasoningdialect.ForVendorFamily(reasoningdialect.FamilyGemini25).(reasoningdialect.DerivedProjector)
	oai := reasoningdialect.ForVendorFamily(reasoningdialect.FamilyOpenAI).(reasoningdialect.DerivedProjector)

	pAnth := anth.ProjectDerived(reasoningdialect.DerivedRequest{BudgetTokens: &zero, Model: "claude-sonnet-5", SourceProtocol: ir.ProtocolOpenAIChat})
	tv, ok := pAnth.Wire["thinking"].(map[string]any)
	if !ok || tv["type"] != "disabled" {
		t.Fatalf("anthropic 零预算必须按 disabled 处置，got %v", pAnth.Wire)
	}
	if _, has := tv["budget_tokens"]; has {
		t.Fatalf("disabled thinking 不得携带 budget_tokens（TestReasoningDisabledConflictPrecedence 语义）")
	}
	pGem := gem.ProjectDerived(reasoningdialect.DerivedRequest{BudgetTokens: &zero, Model: "gemini-2.5-flash", SourceProtocol: ir.ProtocolGeminiGenerate})
	gc, ok := pGem.Wire["thinkingConfig"].(map[string]any)
	if !ok || gc["thinkingBudget"] != 0 {
		t.Fatalf("gemini flash 零预算必须发射 0 哨兵，got %v", pGem.Wire)
	}
	pGemPro := gem.ProjectDerived(reasoningdialect.DerivedRequest{BudgetTokens: &zero, Model: "gemini-2.5-pro", SourceProtocol: ir.ProtocolGeminiGenerate})
	if len(pGemPro.Wire) != 0 || len(pGemPro.Losses) != 1 {
		t.Fatalf("gemini pro 拒 0 哨兵：必须省略 + 恰一条 unsupported loss，got wire=%v losses=%d", pGemPro.Wire, len(pGemPro.Losses))
	}
	pOai := oai.ProjectDerived(reasoningdialect.DerivedRequest{Type: "disabled", Effort: "high", SourceProtocol: ir.ProtocolOpenAIChat})
	if len(pOai.Wire) != 0 {
		t.Fatalf("R25-U4：disabled Type 必须压倒 effort（不发射 reasoning_effort），got %v", pOai.Wire)
	}

	// 正预算在各家族的形状（R23-A：enabled 指令不得丢显式预算）。
	// 显式 max_tokens ⇒ 预算逐字透传（2026-10-01 审计钳制只作用于省略形态）。
	pAnth2 := anth.ProjectDerived(reasoningdialect.DerivedRequest{Type: "enabled", BudgetTokens: &i9999, Model: "claude-sonnet-5", SourceProtocol: ir.ProtocolOpenAIChat, FinalMaxTokens: 16384, ClientSetMaxTokens: true})
	tv2, _ := pAnth2.Wire["thinking"].(map[string]any)
	if tv2 == nil || tv2["budget_tokens"] != 9999 {
		t.Fatalf("R23-A：显式正预算必须保留（显式 max_tokens 不钳），got %v", pAnth2.Wire)
	}
	pGem2 := gem.ProjectDerived(reasoningdialect.DerivedRequest{Type: "enabled", BudgetTokens: &i9999, Model: "gemini-2.5-flash", SourceProtocol: ir.ProtocolGeminiGenerate})
	gc2, _ := pGem2.Wire["thinkingConfig"].(map[string]any)
	if gc2 == nil || gc2["thinkingBudget"] != 9999 {
		t.Fatalf("R23-A：gemini 显式正预算必须保留，got %v", pGem2.Wire)
	}

	// 2026-10-01 审计钳制（省略 max_tokens 形态）：budget 9999 × 最终
	// max_tokens 4096 ⇒ cap 到 4095（可发出的请求优先于必 400 的请求）。
	pAnth3 := anth.ProjectDerived(reasoningdialect.DerivedRequest{Type: "enabled", BudgetTokens: &i9999, Model: "claude-sonnet-5", SourceProtocol: ir.ProtocolOpenAIChat, FinalMaxTokens: 4096})
	tv3, _ := pAnth3.Wire["thinking"].(map[string]any)
	if tv3 == nil || tv3["budget_tokens"] != 4095 {
		t.Fatalf("钳制：budget 必须 cap 到 finalMax-1，got %v", pAnth3.Wire)
	}
}

// ④ OCP 演示门：虚构家族注册即生效（核心零改动）。
func TestRegistryOpenForExtension(t *testing.T) {
	const fictional = "acmewire-reasoning"
	registered := false
	reasoningdialect.Register(fictional, acmeFamily{&registered})
	fam := reasoningdialect.ForVendorFamily(fictional)
	if fam.Name() != fictional {
		t.Fatalf("注册后必须取回本方言，got %q", fam.Name())
	}
	ip, ok := fam.(reasoningdialect.IntentProjector)
	if !ok {
		t.Fatalf("虚构家族必须可作 IntentProjector 取用")
	}
	out := map[string]any{}
	if !ip.RenderIntent(reasonnorm.Intent{}, 0, out) {
		t.Fatalf("虚构方言的渲染必须生效")
	}
	if !registered {
		t.Fatalf("调用必须抵达方言实现（注册即生效）")
	}
}

// acmeFamily：测试桩（RenderIntent 形参是 reasonnorm.Intent 具体类型）。
type acmeFamily struct{ flag *bool }

func (f acmeFamily) Name() string { return "acmewire-reasoning" }
func (f acmeFamily) RenderIntent(_ reasonnorm.Intent, _ int, _ map[string]any) bool {
	*f.flag = true
	return true
}

// ⑤ ir.anomaly 语义化投影门：按设计丢弃必须带稳定 reason + family/
// disposition metadata（谁、哪个字段、为何丢——噪声变语义）。
func TestAnomalySemanticProjection(t *testing.T) {
	oll := reasoningdialect.ForVendorFamily(reasoningdialect.FamilyOllama).(reasoningdialect.DerivedProjector)
	budget := 8192
	p := oll.ProjectDerived(reasoningdialect.DerivedRequest{
		Type: "enabled", BudgetTokens: &budget, Effort: "high",
		Model: "qwen3:4b", SourceProtocol: ir.ProtocolOpenAIChat,
	})
	reasons := map[string][]string{}
	for _, l := range p.Losses {
		if l.Reason != "ollama_reasoning_drop" {
			t.Fatalf("ollama loss reason 必须逐字保持 %q，got %q", "ollama_reasoning_drop", l.Reason)
		}
		if l.TargetProtocol != ir.ProtocolOllamaChat {
			t.Fatalf("target protocol 必须是 ollama-chat，got %q", l.TargetProtocol)
		}
		if l.Metadata["family"] != "ollama" || l.Metadata["disposition"] != "by_design_no_request_side_surface" {
			t.Fatalf("loss 必须携带 family/disposition 语义 metadata，got %v", l.Metadata)
		}
		reasons[l.FieldPath] = append(reasons[l.FieldPath], l.Reason)
	}
	for _, fp := range []string{"reasoning.type", "reasoning.effort", "reasoning.budget_tokens"} {
		if len(reasons[fp]) != 1 {
			t.Fatalf("字段 %s 必须恰一条按设计丢弃披露，got %d", fp, len(reasons[fp]))
		}
	}

	anth := reasoningdialect.ForVendorFamily(reasoningdialect.FamilyAnthropic).(reasoningdialect.DerivedProjector)
	p2 := anth.ProjectDerived(reasoningdialect.DerivedRequest{Type: "disabled", Model: "claude-opus-5-5", SourceProtocol: ir.ProtocolOpenAIChat})
	if len(p2.Losses) != 1 || p2.Losses[0].Reason != "unsupported" {
		t.Fatalf("opus-5-5 disabled 必须恰一条 unsupported loss，got %+v", p2.Losses)
	}
	if p2.Losses[0].Metadata["disposition"] != "by_design_model_gate" || p2.Losses[0].Metadata["family"] != "anthropic" {
		t.Fatalf("模型门 loss 必须携带 family/disposition，got %v", p2.Losses[0].Metadata)
	}
	if len(p2.Wire) != 0 {
		t.Fatalf("opus-5-5 disabled 必须省略整块，got %v", p2.Wire)
	}
}

// ⑥ noop 兜底 fail-closed 门：未注册族不供任何投影能力（调用方保持
// 既有 loss/跳过路径——kimi/grok/mistral 等 openai-wire 未验证族的
// loss 上报行为不得被兜底吞掉）。
func TestNoopFallbackFailsClosed(t *testing.T) {
	fam := reasoningdialect.ForVendorFamily("kimi-unverified")
	if fam.Name() != "noop/reasoning" {
		t.Fatalf("未注册族必须回落 noop，got %q", fam.Name())
	}
	if _, ok := fam.(reasoningdialect.NativeProjector); ok {
		t.Fatalf("noop 不得提供 NativeProjector（不发明 wire 形态）")
	}
	if _, ok := fam.(reasoningdialect.DerivedProjector); ok {
		t.Fatalf("noop 不得提供 DerivedProjector")
	}
	if _, ok := fam.(reasoningdialect.IntentProjector); ok {
		t.Fatalf("noop 不得提供 IntentProjector（否则未验证族静默获得投影）")
	}
}

// ⑦ 内置种子完整性门：九个家族键全部有种子且不是 noop（未装配即正确）。
func TestSeedRegistryCompleteness(t *testing.T) {
	for _, key := range []string{
		reasoningdialect.FamilyAnthropic, reasoningdialect.FamilyGemini25,
		reasoningdialect.FamilyOpenAI, reasoningdialect.FamilyOllama,
		reasoningdialect.FamilyMiniMax, reasoningdialect.FamilyDeepSeek,
		reasoningdialect.FamilyGLM, reasoningdialect.FamilyArk, reasoningdialect.FamilyQwen,
	} {
		fam := reasoningdialect.ForVendorFamily(key)
		if fam.Name() == "noop/reasoning" {
			t.Fatalf("家族 %s 缺内置种子（未装配即正确被破坏）", key)
		}
		if fam.Name() != key {
			t.Fatalf("家族 %s 的方言名必须等于注册键，got %q", key, fam.Name())
		}
	}
}
