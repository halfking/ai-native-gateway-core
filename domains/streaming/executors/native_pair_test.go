package executors

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/transformation"
	"github.com/kaixuan/llm-gateway-go/internal/reqprobe"
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
		cand   provider.Candidate
		want   bool
	}{
		{"header zcode + glm", newParams("zcode", "", `{"messages":[]}`), provider.Candidate{CatalogCode: "zhipu", RawModel: "glm-5.3", Protocol: "openai-completions"}, true},
		{"header zcode + host-prefixed glm", newParams("zcode", "", `{"messages":[]}`), provider.Candidate{CatalogCode: "zhipu", RawModel: "z-ai/glm-5.2", Protocol: "openai-completions"}, true},
		{"header zcode + deepseek model → 非原生对", newParams("zcode", "", `{"messages":[]}`), provider.Candidate{CatalogCode: "zhipu", RawModel: "deepseek-chat", Protocol: "openai-completions"}, false},
		{"UA minimax-code + minimax", newParams("", "minimax-code/1.0", `{"messages":[]}`), provider.Candidate{CatalogCode: "minimax", RawModel: "minimax-m2", Protocol: "openai-completions"}, true},
		{"body marker zcode_version（弱名回退）", newParams("", "", `{"metadata":{"zcode_version":"1.2"},"messages":[]}`), provider.Candidate{CatalogCode: "zhipu", RawModel: "glm-5.3", Protocol: "openai-completions"}, true},
		{"X-Code-Session-Id → minimax-code", newParamsWithSession("sess-1", `{"messages":[]}`), provider.Candidate{CatalogCode: "minimax", RawModel: "minimax-m2", Protocol: "openai-completions"}, true},
		{"强名冲突：header cursor + body zcode 标记 → 保守不直通", newParams("cursor", "cursor/1.0", `{"metadata":{"zcode_version":"1.2"},"messages":[]}`), provider.Candidate{CatalogCode: "zhipu", RawModel: "glm-5.3", Protocol: "openai-completions"}, false},
		{"未知客户端 + glm", newParams("", "", `{"messages":[]}`), provider.Candidate{CatalogCode: "zhipu", RawModel: "glm-5.3", Protocol: "openai-completions"}, false},
		// 2026-10-29 厂商感知（nvidia 直通事故轮）：zcode → nvidia 托管的
		// z-ai/glm-5.3-flash 不构成原厂对 ⇒ 学习式预剥离/paramguard 照常
		// 生效（事故形态：每请求两轮 400 剥参重试且学习永不回应用）。
		{"事故回归：zcode + nvidia 宿主 glm", newParams("zcode", "", `{"messages":[]}`), provider.Candidate{CatalogCode: "nvidia", RawModel: "z-ai/glm-5.3-flash", Protocol: "openai-completions"}, false},
		{"zcode + 空 catalog 中继 glm", newParams("zcode", "", `{"messages":[]}`), provider.Candidate{RawModel: "glm-5.3", Protocol: "openai-completions"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := nativePairFor(tc.params, tc.cand, tc.params.BodyBytes); got != tc.want {
				t.Fatalf("nativePairFor = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestFinalizeNative_LegacyWithIR_NativePairStampsPassthrough（2026-10-28
// 生产验证轮）：legacy_with_ir（OpenAI 入向、e.IR != nil —— 211 生产日志里的
// 实际 path）此前只盖 TargetProvider、不盖 NativePassthrough ⇒ 直通腿的
// enable_thinking /thinking 被 paramreg 的 RejectedByUnknown 政策裁掉。
// 本测试钉住 finalize 的 NativePassthrough 盖章链路本身（native 参数直灌，
// 绕过 nativePairFor）。2026-10-29 厂商感知修正后，nvidia 宿主在生产的
// native 恒为 false（见 TestNativePairFor_Detection 事故回归臂）——这里
// 用 nvidia 候选是为了 dst=Unknown 的对照臂形状；对照臂（native=false）
// 同时就是 nvidia 宿主今天在生产走到的真实形态。
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

// TestPrepareRequestBodyWithNative_StripBypassLoggedAtInfo（RISK-3 收口，
// 2026-10-29）：原厂直通绕过 admin 配置的 strip_request_fields 时必须在
// **Info** 级留痕 —— 此前是 Debug，生产日志 Info 级不可见 ⇒ 留痕机制默认
// 不可观测（2026-10-28 生产验证轮 §7 观察盲区）。消息文本逐字不变
// （211 取证的 grep 口径依赖它）。正臂断言级别、字段与条数；两条对照臂
// 钉住「不该留痕的形态不留痕」：strip 为空（当前生产实态）与
// nativePair=false（白名单通道正常执行 strip，不打绕过行）。
func TestPrepareRequestBodyWithNative_StripBypassLoggedAtInfo(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	const bypassMsg = "native pair bypasses transform strip_request_fields"
	run := func(nativePair bool, strip []string) []map[string]any {
		t.Helper()
		buf.Reset()
		params := &ExecParams{
			R:              httptest.NewRequest("POST", "/v1/chat/completions", nil),
			RequestID:      "risk3-strip-bypass",
			BodyBytes:      []byte(`{"model":"glm-5.3","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"high"}`),
			ClientModel:    "glm-5.3",
			ClientProtocol: "openai-completions",
			Transform:      &transformation.TransformResult{StripRequestFields: strip},
		}
		cand := nativePairCandidate("zhipu", "glm-5.3")
		prepareRequestBodyWithNative(params, cand, nativePair)

		var matched []map[string]any
		dec := json.NewDecoder(strings.NewReader(buf.String()))
		for dec.More() {
			var rec map[string]any
			if err := dec.Decode(&rec); err != nil {
				t.Fatalf("decode slog line: %v\nbuf: %s", err, buf.String())
			}
			if rec["msg"] == bypassMsg {
				matched = append(matched, rec)
			}
		}
		return matched
	}

	// 正臂：原生对 × 非空 strip ⇒ 恰好一行，且必须是 Info 级。
	native := run(true, []string{"reasoning_effort", "thinking"})
	if len(native) != 1 {
		t.Fatalf("native pair with strip config: got %d bypass log lines, want exactly 1\nbuf: %s", len(native), buf.String())
	}
	rec := native[0]
	if rec["level"] != "INFO" {
		t.Fatalf("bypass log level = %v, want INFO (Debug is invisible on production Info-level logs — the whole point of RISK-3)", rec["level"])
	}
	if rec["request_id"] != "risk3-strip-bypass" {
		t.Errorf("request_id = %v, want risk3-strip-bypass", rec["request_id"])
	}
	if rec["catalog_code"] != "zhipu" {
		t.Errorf("catalog_code = %v, want zhipu", rec["catalog_code"])
	}
	if rec["raw_model"] != "glm-5.3" {
		t.Errorf("raw_model = %v, want glm-5.3", rec["raw_model"])
	}
	if rec["stripped_fields"] != "reasoning_effort,thinking" {
		t.Errorf("stripped_fields = %v, want \"reasoning_effort,thinking\"", rec["stripped_fields"])
	}

	// 对照臂 1：原生对 × 空 strip（当前生产实态）⇒ 不得留痕。
	if got := run(true, nil); len(got) != 0 {
		t.Fatalf("native pair without strip config: got %d bypass log lines, want 0", len(got))
	}
	// 对照臂 2：非原生对 × 非空 strip ⇒ 白名单通道正常执行，无绕过行。
	if got := run(false, []string{"reasoning_effort"}); len(got) != 0 {
		t.Fatalf("non-native pair with strip config: got %d bypass log lines, want 0 (whitelist path applies strip, not a bypass)", len(got))
	}
}

// TestNvidiaHostIncident_LearnedStripsApply（2026-10-29 nvidia 直通事故轮）：
// 事故链：zcode → nvidia 托管的 z-ai/glm-5.3-flash 被误判原厂对 ⇒ 直通腿
// 豁免学习式预剥离 ⇒ NVIDIA 拒 reasoning/enable_thinking（一次点名一个）
// ⇒ 每请求两轮 400 剥参重试（211 实测 53 请求、TTFB 29s+），Redis 学习
// 记录（recovered_count=1）永不回应用。厂商感知修正后 nvidia 候选不再直通
// （nativePairFor=false，见 TestNativePairFor_Detection），本测试钉住
// 「学习记录 → 首发前置剥离」在 nvidia 候选上确实生效，且事故错误文案
// （"Validation: Unsupported parameter(s): `reasoning`"）能被 Diagnose
// 点名提取。
func TestNvidiaHostIncident_LearnedStripsApply(t *testing.T) {
	store := reqprobe.NewMemoryStore()
	coord := reqprobe.NewCoordinator(store)
	defer coord.Close()

	// 事故错误文案（211 生产 2026-10-29 00:04–01:32Z 实测原文）必须能被
	// Diagnose 判为 param_rejected 并点名 reasoning。
	incidentBody := []byte(`{"message":"Validation: Unsupported parameter(s): ` + "`reasoning`" + `","type":"Bad Request","code":400}`)
	outbound := []byte(`{"model":"z-ai/glm-5.3-flash","messages":[{"role":"user","content":"hi"}],` +
		`"reasoning":{"effort":"max"},"enable_thinking":true}`)
	diag, ok := reqprobe.Diagnose(reqprobe.Input{
		HTTPStatus:   400,
		ErrorBody:    incidentBody,
		OutboundBody: outbound,
	})
	if !ok || diag.Trigger != reqprobe.TriggerParamRejected {
		t.Fatalf("Diagnose(incident body) ok=%v trigger=%q, want param_rejected", ok, diag.Trigger)
	}
	if !strings.Contains(diag.Param, "reasoning") {
		t.Fatalf("Diagnose param = %q, want it to name reasoning", diag.Param)
	}

	// 学习记录（Redis 实测形态：剥后成功，param=enable_thinking,reasoning）。
	coord.RecordTerminal(
		reqprobe.Input{HTTPStatus: 400, ErrorBody: incidentBody, Protocol: "openai-completions"},
		reqprobe.Diagnosis{Trigger: reqprobe.TriggerParamRejected, Param: "enable_thinking,reasoning"},
		reqprobe.TerminalMeta{ProviderID: 18, ProviderCode: "nvidia", ClientModel: "glm-5.3-flash", OutboundModel: "z-ai/glm-5.3-flash"},
		true,
	)
	deadline := time.Now().Add(2 * time.Second)
	visible := false
	for time.Now().Before(deadline) {
		recs, _, err := store.List(context.Background(), reqprobe.Filter{ProviderCode: "nvidia", Model: "z-ai/glm-5.3-flash"})
		if err == nil && len(recs) > 0 {
			visible = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !visible {
		t.Fatalf("record never landed in store — worker did not drain the queue")
	}

	// nvidia 候选（厂商感知修正后非原厂对）首发前必须前置剥离学习参数。
	e := &Executor{RequestProbe: coord}
	cand := provider.Candidate{
		ProviderID:  18,
		CatalogCode: "nvidia",
		RawModel:    "z-ai/glm-5.3-flash",
		Protocol:    "openai-completions",
	}
	if nativePairFor(&ExecParams{ClientType: "zcode"}, cand, outbound) {
		t.Fatalf("zcode × nvidia host must not be a native pair after the 2026-10-29 vendor gate")
	}
	out := e.reqprobeApplyLearned(outbound, cand, "openai-completions", "nvidia-incident")
	obj := mustJSON(t, out)
	if _, present := obj["reasoning"]; present {
		t.Fatalf("learned strip did not remove reasoning from nvidia outbound:\n%s", out)
	}
	if _, present := obj["enable_thinking"]; present {
		t.Fatalf("learned strip did not remove enable_thinking from nvidia outbound:\n%s", out)
	}
	if _, present := obj["messages"]; !present {
		t.Fatalf("learned strip lost core field messages:\n%s", out)
	}
}
