package executors

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/provider"
)

// 原厂直通轮（2026-10-28）：zcode↔glm / minimax-code↔minimax /
// deepseek-code↔deepseek 三条内置原生对，首发全参数透传；非原生对行为
// 完全不变。正臂证明「参数保留」，对照臂（nativePair=false）证明三道
// 既有钳制仍在（门没有因本轮被拔掉牙）。

func nativePairCandidate(catalogCode, rawModel string) provider.Candidate {
	return provider.Candidate{
		CredentialID: 55,
		ProviderID:   7,
		BaseURL:      "https://upstream.example/v1",
		Protocol:     "openai-completions",
		CatalogCode:  catalogCode,
		RawModel:     rawModel,
		Tier:         2,
	}
}

func mustJSON(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var obj map[string]any
	if err := json.Unmarshal(body, &obj); err != nil {
		t.Fatalf("output not valid JSON: %v\nbody: %s", err, body)
	}
	return obj
}

// TestPrepareRequestBodyWithNative_GLMToolChoicePreserved：zcode→glm 时
// 客户端显式 tool_choice 不再被 paramguard 强制归一为 "auto"。
func TestPrepareRequestBodyWithNative_GLMToolChoicePreserved(t *testing.T) {
	clientBody := `{"model":"glm-5.3","messages":[{"role":"user","content":"hi"}],"tool_choice":{"type":"function","function":{"name":"read_file"}},"reasoning_effort":"high"}`
	cand := nativePairCandidate("zhipu", "glm-5.3")
	params := &ExecParams{
		BodyBytes:      []byte(clientBody),
		ClientModel:    "glm-5.3",
		IsStream:       false,
		ClientProtocol: "openai-completions",
	}

	native := prepareRequestBodyWithNative(params, cand, true)
	obj := mustJSON(t, native)
	tc, ok := obj["tool_choice"].(map[string]any)
	if !ok || tc["function"] == nil {
		t.Fatalf("native glm: tool_choice rewritten, got %v (want client's explicit object)", obj["tool_choice"])
	}
	if obj["reasoning_effort"] != "high" {
		t.Fatalf("native glm: reasoning_effort = %v, want \"high\" (verbatim)", obj["reasoning_effort"])
	}

	// 对照臂：非原生对（nativePair=false）维持既有钳制 —— tool_choice 归一。
	legacy := prepareRequestBody(params, cand)
	lobj := mustJSON(t, legacy)
	if lobj["tool_choice"] != "auto" {
		t.Fatalf("non-native glm: tool_choice = %v, want \"auto\" (paramguard must keep its teeth)", lobj["tool_choice"])
	}
}

// TestPrepareRequestBodyWithNative_MiniMaxNAndToolsPreserved：minimax-code→
// minimax 时 n 不再强制 1、非规范形态的工具定义不再被 SimplifyTools 重建。
func TestPrepareRequestBodyWithNative_MiniMaxNAndToolsPreserved(t *testing.T) {
	// 工具字段在顶层（无 function 包装）—— SimplifyTools 会重建为规范形；
	// 原生直通必须原样保留客户端写的形状。
	clientBody := `{"model":"minimax-m2","messages":[{"role":"user","content":"hi"}],"n":2,"tools":[{"name":"f","description":"d","parameters":{"type":"object","properties":{}}}]}`
	cand := nativePairCandidate("minimax", "minimax-m2")
	params := &ExecParams{
		BodyBytes:      []byte(clientBody),
		ClientModel:    "minimax-m2",
		IsStream:       false,
		ClientProtocol: "openai-completions",
	}

	native := prepareRequestBodyWithNative(params, cand, true)
	obj := mustJSON(t, native)
	if obj["n"] != float64(2) {
		t.Fatalf("native minimax: n = %v, want 2 (verbatim)", obj["n"])
	}
	if strings.Contains(string(native), `"function":`) {
		t.Fatalf("native minimax: tool shape rewritten by SimplifyTools:\n%s", native)
	}

	// 对照臂：非原生对维持既有钳制（n=1 / 工具重建为规范形）。
	legacy := prepareRequestBody(params, cand)
	lobj := mustJSON(t, legacy)
	if lobj["n"] != float64(1) {
		t.Fatalf("non-native minimax: n = %v, want 1 (fixMiniMaxN must keep its teeth)", lobj["n"])
	}
	if !strings.Contains(string(legacy), `"function":`) {
		t.Fatalf("non-native minimax: SimplifyTools did not rebuild tool shape:\n%s", legacy)
	}
}

// TestPrepareRequestBodyWithNative_DeepSeekMaxTokensPreserved：deepseek-code→
// deepseek 时 max_tokens 不再被砍到 8192。
func TestPrepareRequestBodyWithNative_DeepSeekMaxTokensPreserved(t *testing.T) {
	clientBody := `{"model":"deepseek-chat","messages":[{"role":"user","content":"hi"}],"max_tokens":16384}`
	cand := nativePairCandidate("deepseek", "deepseek-chat")
	params := &ExecParams{
		BodyBytes:      []byte(clientBody),
		ClientModel:    "deepseek-chat",
		IsStream:       false,
		ClientProtocol: "openai-completions",
	}

	native := prepareRequestBodyWithNative(params, cand, true)
	obj := mustJSON(t, native)
	if obj["max_tokens"] != float64(16384) {
		t.Fatalf("native deepseek: max_tokens = %v, want 16384 (verbatim)", obj["max_tokens"])
	}

	legacy := prepareRequestBody(params, cand)
	lobj := mustJSON(t, legacy)
	if lobj["max_tokens"] != float64(8192) {
		t.Fatalf("non-native deepseek: max_tokens = %v, want 8192 (cap must keep its teeth)", lobj["max_tokens"])
	}
}

// TestApplyInlineValidation_NativePairKeepsVendorParams：legacy 路径
// （e.IR == nil，LLM_GATEWAY_IR_CONVERTER=false 的紧急回退形态）把 body
// 整体经 IR 往返一次 —— 原生对必须在这条路径上也保住厂商私有参数
// （否则「IR 路径修了、legacy 路径漏了」= 原生对保证只在半条路上成立）。
func TestApplyInlineValidation_NativePairKeepsVendorParams(t *testing.T) {
	// thinking{type:enabled} 在 MiniMax 目标上会被 paramreg 改写为
	// {"type":"adaptive"}（IR 层的对照臂已钉）；原生对必须原样。
	body := []byte(`{"model":"minimax-m2","messages":[{"role":"user","content":"hi"}],` +
		`"thinking":{"type":"enabled","budget_tokens":1024},"zcode_vendor_flag":true}`)

	native := applyInlineValidation(body, "np-inline", "minimax", context.Background(), true)
	var nObj map[string]any
	if err := json.Unmarshal(native, &nObj); err != nil {
		t.Fatalf("unmarshal native: %v\n%s", err, native)
	}
	thinking, ok := nObj["thinking"].(map[string]any)
	if !ok {
		t.Fatalf("native legacy path: thinking missing/not an object:\n%s", native)
	}
	if thinking["type"] != "enabled" {
		t.Fatalf("native legacy path: thinking.type = %v, want \"enabled\" (verbatim)\n%s", thinking["type"], native)
	}
	if thinking["budget_tokens"] != float64(1024) {
		t.Fatalf("native legacy path: thinking.budget_tokens = %v, want 1024 (verbatim)\n%s", thinking["budget_tokens"], native)
	}
	if _, present := nObj["zcode_vendor_flag"]; !present {
		t.Fatalf("native legacy path: vendor param dropped:\n%s", native)
	}

	// 对照臂：非原生对仍走既有方言策略（thinking 被改写）。
	legacy := applyInlineValidation(body, "np-inline", "minimax", context.Background(), false)
	var lObj map[string]any
	if err := json.Unmarshal(legacy, &lObj); err != nil {
		t.Fatalf("unmarshal legacy: %v\n%s", err, legacy)
	}
	lThinking, ok := lObj["thinking"].(map[string]any)
	if !ok {
		t.Fatalf("non-native legacy path: thinking missing/not an object:\n%s", legacy)
	}
	if lThinking["type"] != "adaptive" {
		t.Fatalf("non-native legacy path: thinking.type = %v, want \"adaptive\" (dialect policy must keep its teeth)\n%s", lThinking["type"], legacy)
	}
}

// TestNativePairFor_Detection：客户端识别三路（X-Gw-Client-Type / UA /
// body 标记）与按候选模型的匹配。
func TestNativePairFor_Detection(t *testing.T) {
	newParams := func(clientType, ua string, body string) *ExecParams {
		r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
		if clientType != "" {
			r.Header.Set("X-Gw-Client-Type", clientType)
		}
		if ua != "" {
			r.Header.Set("User-Agent", ua)
		}
		return &ExecParams{R: r, BodyBytes: []byte(body), ClientProtocol: "openai-completions"}
	}
	newParamsWithSession := func(sessionID, body string) *ExecParams {
		p := newParams("", "", body)
		p.R.Header.Set("X-Code-Session-Id", sessionID)
		return p
	}

	cases := []struct {
		name   string
		params *ExecParams
		model  string
		want   bool
	}{
		{"header zcode + glm", newParams("zcode", "", `{"messages":[]}`), "glm-5.3", true},
		{"header zcode + host-prefixed glm", newParams("zcode", "", `{"messages":[]}`), "z-ai/glm-5.2", true},
		{"header zcode + deepseek model → 非原生对", newParams("zcode", "", `{"messages":[]}`), "deepseek-chat", false},
		{"UA minimax-code + minimax", newParams("", "minimax-code/1.0", `{"messages":[]}`), "minimax-m2", true},
		{"body marker zcode_version（弱名回退）", newParams("", "", `{"metadata":{"zcode_version":"1.2"},"messages":[]}`), "glm-5.3", true},
		{"X-Code-Session-Id → minimax-code", newParamsWithSession("sess-1", `{"messages":[]}`), "minimax-m2", true},
		{"强名冲突：header cursor + body zcode 标记 → 保守不直通", newParams("cursor", "cursor/1.0", `{"metadata":{"zcode_version":"1.2"},"messages":[]}`), "glm-5.3", false},
		{"未知客户端 + glm", newParams("", "", `{"messages":[]}`), "glm-5.3", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cand := provider.Candidate{RawModel: tc.model, Protocol: "openai-completions"}
			if got := nativePairFor(tc.params, cand, tc.params.BodyBytes); got != tc.want {
				t.Fatalf("nativePairFor = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestFinalizeNative_LegacyWithIR_NativePairStampsPassthrough（2026-10-28
// 生产验证轮）：legacy_with_ir（OpenAI 入向、e.IR != nil —— 211 生产日志里的
// 实际 path）此前只盖 TargetProvider、不盖 NativePassthrough ⇒ zcode→glm
// 路由到第三方宿主（catalog=nvidia，或空 catalog 的中继）时，enable_thinking
// /thinking 被 paramreg 的 RejectedByUnknown 政策裁掉。生产实测 87 个
// zcode→glm 请求里 86 个保真靠的是 GLM 原厂（zhipu 方言）恰好认这些键，
// 不是直通机制在起作用。本测试把第三方宿主形态钉死。
func TestFinalizeNative_LegacyWithIR_NativePairStampsPassthrough(t *testing.T) {
	body := []byte(`{"model":"glm-5.3","messages":[{"role":"user","content":"hi"}],` +
		`"enable_thinking":true,"thinking":{"type":"enabled","budget_tokens":4096},` +
		`"reasoning_effort":"max","tool_choice":"auto"}`)

	run := func(native bool) map[string]any {
		executor := &Executor{IR: &irAdapterForTest{}}
		params := &ExecParams{
			R:              httptest.NewRequest("POST", "/v1/chat/completions", nil),
			RequestID:      "npv-legacy-ir",
			BodyBytes:      body,
			ClientModel:    "glm-5.3",
			OutboundModel:  "z-ai/glm-5.3-flash",
			ClientProtocol: "openai-completions",
			Model:          "glm-5.3",
		}
		cand := nativePairCandidate("nvidia", "z-ai/glm-5.3-flash")
		out, err := executor.finalizeOpenAIUpstreamBodyNative(params, cand, body, native)
		if err != nil {
			t.Fatalf("finalize (native=%v): %v", native, err)
		}
		return mustJSON(t, out)
	}

	native := run(true)
	if v, ok := native["enable_thinking"].(bool); !ok || !v {
		t.Fatalf("native legacy_with_ir: enable_thinking = %#v, want true（第三方宿主上原厂对首发全参）\nout: %s", native["enable_thinking"], mustJSONRaw(t, native))
	}
	th, ok := native["thinking"].(map[string]any)
	if !ok || th["type"] != "enabled" || th["budget_tokens"] != float64(4096) {
		t.Fatalf("native legacy_with_ir: thinking = %#v, want verbatim {type:enabled,budget_tokens:4096}", native["thinking"])
	}

	// 对照臂：非原生对在同一未知方言目标上仍被 RejectedByUnknown 裁剪
	//（r1021 政策必须保住牙齿——本修复只影响原生对）。
	legacy := run(false)
	if _, present := legacy["enable_thinking"]; present {
		t.Fatalf("non-native 对照: enable_thinking 应被裁（未知方言 RejectedByUnknown）\nout: %s", mustJSONRaw(t, legacy))
	}
	if _, present := legacy["thinking"]; present {
		t.Fatalf("non-native 对照: thinking 应被裁/翻译走（未知方言）\nout: %s", mustJSONRaw(t, legacy))
	}
}

func mustJSONRaw(t *testing.T, obj map[string]any) []byte {
	t.Helper()
	raw, err := json.Marshal(obj)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}

// TestFinalizeNative_CircuitOpenFallback_NativePairStampsPassthrough：
// 断路器兜底分支（e.IR.ParseOpenAI 返回 ErrConverterCircuitOpen ⇒ 走
// breaker-independent 的包级 ir.ParseOpenAI/SerializeOpenAI）与
// legacy_with_ir 主分支同族，NativePassthrough 补盖（65d24d0df）必须在
// 这条分支上也成立 —— 本测试钉住它；对照臂断言非原生对在该分支仍被裁。
func TestFinalizeNative_CircuitOpenFallback_NativePairStampsPassthrough(t *testing.T) {
	body := []byte(`{"model":"glm-5.3","messages":[{"role":"user","content":"hi"}],` +
		`"enable_thinking":true,"thinking":{"type":"enabled","budget_tokens":2048},` +
		`"reasoning_effort":"max"}`)

	run := func(native bool) map[string]any {
		executor := &Executor{IR: &circuitOpenIRAdapter{}}
		params := &ExecParams{
			R:              httptest.NewRequest("POST", "/v1/chat/completions", nil),
			RequestID:      "npv-circuit-open",
			BodyBytes:      body,
			ClientModel:    "glm-5.3",
			OutboundModel:  "z-ai/glm-5.3-flash",
			ClientProtocol: "openai-completions",
			Model:          "glm-5.3",
		}
		cand := nativePairCandidate("nvidia", "z-ai/glm-5.3-flash")
		out, err := executor.finalizeOpenAIUpstreamBodyNative(params, cand, body, native)
		if err != nil {
			t.Fatalf("finalize (native=%v): %v", native, err)
		}
		return mustJSON(t, out)
	}

	native := run(true)
	if v, ok := native["enable_thinking"].(bool); !ok || !v {
		t.Fatalf("native circuit-open fallback: enable_thinking = %#v, want true", native["enable_thinking"])
	}
	th, ok := native["thinking"].(map[string]any)
	if !ok || th["type"] != "enabled" || th["budget_tokens"] != float64(2048) {
		t.Fatalf("native circuit-open fallback: thinking = %#v, want verbatim {type:enabled,budget_tokens:2048}", native["thinking"])
	}

	legacy := run(false)
	if _, present := legacy["enable_thinking"]; present {
		t.Fatalf("non-native 对照（断路器兜底）: enable_thinking 应被裁")
	}
	if _, present := legacy["thinking"]; present {
		t.Fatalf("non-native 对照（断路器兜底）: thinking 应被裁/翻译走")
	}
}
