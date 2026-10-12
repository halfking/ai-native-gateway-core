package ir

// 请求格式交叉测试矩阵（单元层判据）。
//
// 对应 docs/audit/2026-10-13-请求格式交叉测试矩阵.md §二A。每个用例都
// 复刻 executor 的生产转换路径（而非只测单个函数），使断言与线上行为
// 同层：
//
//	Q1 executor_chat.go finalizeOpenAIUpstreamBody：
//	   ParseOpenAI → ValidateAndFixRequest → Model=出站名 →
//	   TargetProvider=catalogCode → SerializeOpenAI
//	Q2 同上，入向 ParseAnthropic（anthropic 客户端 → openai 形态上游）
//	Q3 executor_anthropic.go prepareAnthropicRequestBody：
//	   ParseOpenAI → ValidateAndFixRequest → SerializeAnthropic
//	Q4 生产是字节级透传（executor_anthropic.go needsConversion=false），
//	   此处测 IR 往返保真作为纵深网。
//
// 核心断言来自需求：同格式（协议相等）时会话扩展信息必须全样保留；
// 方言私有字段按 paramreg 策略（目标不认识 → 裁剪 + ir_protocol_loss
// 台账，不得静默丢）。
import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/paramguard"
	"github.com/kaixuan/llm-gateway-go/internal/paramreg"
)

// matrixOpenAIOutbound 复刻 Q1 生产路径并返回出站体。
func matrixOpenAIOutbound(t *testing.T, clientBody, catalogCode, outboundModel string) map[string]json.RawMessage {
	t.Helper()
	req, err := ParseOpenAI([]byte(clientBody))
	if err != nil {
		t.Fatalf("ParseOpenAI: %v", err)
	}
	req = ValidateAndFixRequest(req, "matrix-q1")
	req.Model = outboundModel
	req.TargetProvider = catalogCode
	out, err := SerializeOpenAI(req)
	if err != nil {
		t.Fatalf("SerializeOpenAI: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("outbound body not json: %v\n%s", err, out)
	}
	return m
}

func mustHave(t *testing.T, m map[string]json.RawMessage, key, wantRaw string) {
	t.Helper()
	got, ok := m[key]
	if !ok {
		t.Errorf("outbound body missing %q (full keys: %v)", key, matrixKeys(m))
		return
	}
	if wantRaw == "" {
		return
	}
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Errorf("outbound %q not valid json: %v", key, err)
		return
	}
	if err := json.Unmarshal([]byte(wantRaw), &w); err != nil {
		t.Fatalf("test bug: wantRaw invalid: %v", err)
	}
	gj, _ := json.Marshal(g)
	wj, _ := json.Marshal(w)
	if string(gj) != string(wj) {
		t.Errorf("outbound %q = %s, want %s", key, gj, wj)
	}
}

func mustNotHave(t *testing.T, m map[string]json.RawMessage, key string) {
	t.Helper()
	if _, ok := m[key]; ok {
		t.Errorf("outbound body unexpectedly contains %q", key)
	}
}

func matrixKeys(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sorted := out
	for i := 0; i < len(sorted); i++ {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[j] < sorted[i] {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}
	return sorted
}

// ── A1：openai 入向 × openai 形态上游（协议相同 = 同格式）───────────────

// A1.1 minimax-m3（MiniMax 官方，catalog minimax）：方言私有字段 +
// IR 一等字段 + 未登记新字段全部保留。
func TestMatrix_Q1_MiniMax_ExtensionsFullyPreserved(t *testing.T) {
	body := `{"model":"minimax-m3","messages":[{"role":"user","content":"hi"}],"temperature":0.3,` +
		`"reasoning_split":true,` +
		`"mask_sensitive_info":true,` +
		`"bot_setting":[{"bot_name":"MM","content":"You are helpful"}],` +
		`"minimax_brand_new_param":{"nested":[1,2,3]}}`
	m := matrixOpenAIOutbound(t, body, "minimax", "MiniMax-M3")
	mustHave(t, m, "reasoning_split", `true`)
	mustHave(t, m, "mask_sensitive_info", `true`)
	mustHave(t, m, "bot_setting", `[{"bot_name":"MM","content":"You are helpful"}]`)
	mustHave(t, m, "minimax_brand_new_param", `{"nested":[1,2,3]}`)
	mustHave(t, m, "temperature", `0.3`)
	mustHave(t, m, "model", `"MiniMax-M3"`)
}

// A1.2 glm（智谱官方，catalog zhipu）：GLM 方言字段全保留。
func TestMatrix_Q1_GLM_ExtensionsFullyPreserved(t *testing.T) {
	body := `{"model":"glm-5.3","messages":[{"role":"user","content":"hi"}],` +
		`"do_sample":false,"tool_stream":false,"user_id":"u-123",` +
		`"glm_future_param":"v1"}`
	m := matrixOpenAIOutbound(t, body, "zhipu", "glm-5.3")
	mustHave(t, m, "do_sample", `false`)
	mustHave(t, m, "tool_stream", `false`)
	mustHave(t, m, "user_id", `"u-123"`)
	mustHave(t, m, "glm_future_param", `"v1"`)
}

// A1.3 kimi（moonshot 无任何已登记私有字段）：一切扩展走「未知→无条件
// 还原」分支，必须全样保留（前向兼容的核心路径）。
func TestMatrix_Q1_Kimi_UnknownBranchFullyPreserved(t *testing.T) {
	body := `{"model":"kimi-k3","messages":[{"role":"user","content":"hi"}],` +
		`"web_search":true,` +
		`"kimi_future_param":{"mode":"auto"}}`
	m := matrixOpenAIOutbound(t, body, "moonshot", "kimi-k3")
	mustHave(t, m, "web_search", `true`)
	mustHave(t, m, "kimi_future_param", `{"mode":"auto"}`)
}

// A1.4 grok（xai）：方言字段保留；frequency_penalty 被 RejectedBy +
// paramguard 裁剪（防 400 的设计行为，非丢失事故）。
func TestMatrix_Q1_Grok_KeepDialectStripRejected(t *testing.T) {
	cap := resetDedupAndInstall(t)
	body := `{"model":"grok-4.6","messages":[{"role":"user","content":"hi"}],` +
		`"search_parameters":{"mode":"on","return_citations":true},` +
		`"deferred":true,` +
		`"frequency_penalty":0.5}`
	req, err := ParseOpenAI([]byte(body))
	if err != nil {
		t.Fatalf("ParseOpenAI: %v", err)
	}
	req = ValidateAndFixRequest(req, "matrix-q1-grok")
	req.Model = "grok-4.6"
	req.TargetProvider = "xai"
	out, err := SerializeOpenAI(req)
	if err != nil {
		t.Fatalf("SerializeOpenAI: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("outbound not json: %v", err)
	}
	mustHave(t, m, "search_parameters", `{"mode":"on","return_citations":true}`)
	mustHave(t, m, "deferred", `true`)

	// executor 生产链在 IR 序列化之后还有 paramguard（executor_chat.go
	// applyOpenAITailTransforms 段）——grok 的 penalty 剥离在那里。
	guarded, reports := paramguard.ApplyReported(out, paramreg.DialectGrok)
	var gm map[string]json.RawMessage
	if err := json.Unmarshal(guarded, &gm); err != nil {
		t.Fatalf("guarded outbound not json: %v", err)
	}
	mustNotHave(t, gm, "frequency_penalty")
	if len(reports) == 0 {
		t.Errorf("paramguard stripped frequency_penalty without a report")
	}
	_ = cap // loss 台账在此用例不设强制断言（penalty 属 RejectedBy 设计裁剪）
}

// A1.5 openai 官方方言：标准 + 未知字段全保留。
func TestMatrix_Q1_OpenAI_ExtensionsFullyPreserved(t *testing.T) {
	body := `{"model":"gpt-5.6-luna","messages":[{"role":"user","content":"hi"}],` +
		`"store":true,"seed":42,"openai_future_param":{"x":1}}`
	m := matrixOpenAIOutbound(t, body, "openai", "gpt-5.6-luna")
	mustHave(t, m, "store", `true`)
	mustHave(t, m, "seed", `42`)
	mustHave(t, m, "openai_future_param", `{"x":1}`)
}

// A1.6 catalog_code 为空的供应商（suyun/apigpt/apinext 形态）：
// DialectUnknown → fail-open，连别家方言私有字段也放行（宁可多传）。
func TestMatrix_Q1_UnknownCatalog_FailOpen(t *testing.T) {
	body := `{"model":"gpt-5.6-luna","messages":[{"role":"user","content":"hi"}],` +
		`"prefix":"续写",` + // DeepSeek 方言私有
		`"relay_future_param":7}`
	m := matrixOpenAIOutbound(t, body, "suyun", "gpt-5.6-luna")
	mustHave(t, m, "prefix", `"续写"`)
	mustHave(t, m, "relay_future_param", `7`)
}

// A1.6b catalog_code 为空串的中转（联界/智码等本地主力形态）：executor
// 盖 paramreg.CatalogCodeUnknownRelay 哨兵（路由已定、厂商未知）→
// fail-open。R2.D1 修复判据（修复前红）。注意与「路由前序列化点」的
// TargetProvider 空串 fail-closed 契约相区分（见
// serialize_openai_minimax_dialect_test.go 的 pre-routing 用例）。
func TestMatrix_Q1_EmptyCatalog_FailOpen(t *testing.T) {
	body := `{"model":"kimi-k3","messages":[{"role":"user","content":"hi"}],` +
		`"search_parameters":{"mode":"off"},` + // grok 方言私有
		`"relay_future_param":7}`
	req, err := ParseOpenAI([]byte(body))
	if err != nil {
		t.Fatalf("ParseOpenAI: %v", err)
	}
	req = ValidateAndFixRequest(req, "matrix-q1-empty-cat")
	req.Model = "kimi-k3"
	req.TargetProvider = "unknown-relay" // executor 对空 catalog 候选盖的哨兵
	out, err := SerializeOpenAI(req)
	if err != nil {
		t.Fatalf("SerializeOpenAI: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("outbound not json: %v", err)
	}
	mustHave(t, m, "search_parameters", `{"mode":"off"}`)
	mustHave(t, m, "relay_future_param", `7`)
}

// A1.7 数值保真：>2^53 整数在 IR 字段与 Extensions 两条路上都不变形。
func TestMatrix_Q1_BigIntFidelity(t *testing.T) {
	body := `{"model":"grok-4.6","messages":[{"role":"user","content":"hi"}],` +
		`"seed":9007199254740993,` +
		`"vendor_big_int":9007199254740995}`
	m := matrixOpenAIOutbound(t, body, "xai", "grok-4.6")
	mustHave(t, m, "seed", `9007199254740993`)
	mustHave(t, m, "vendor_big_int", `9007199254740995`)
}

// ── A2：anthropic 入向 × openai 形态上游（Q2 跨格式）──────────────────

// A2.1 anthropic 私有字段（目标方言不认识）→ 裁剪且必须上 loss 台账；
// 未知字段 → 保留。
func TestMatrix_Q2_AnthropicToMiniMax_DialectScopedDroppedWithLoss(t *testing.T) {
	cap := resetDedupAndInstall(t)
	body := `{"model":"minimax-m3","max_tokens":64,` +
		`"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}],` +
		`"context_management":{"edits":[{"type":"clear_tool_acks"}]},` +
		`"anthropic_future_beta":{"z":1}}`
	req, err := ParseAnthropic([]byte(body))
	if err != nil {
		t.Fatalf("ParseAnthropic: %v", err)
	}
	req = ValidateAndFixRequest(req, "matrix-q2")
	req.Model = "MiniMax-M3"
	req.TargetProvider = "minimax"
	out, err := SerializeOpenAI(req)
	if err != nil {
		t.Fatalf("SerializeOpenAI: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("outbound not json: %v", err)
	}
	mustNotHave(t, m, "context_management")
	mustHave(t, m, "anthropic_future_beta", `{"z":1}`)
	mustHave(t, m, "max_tokens", `64`)
	found := false
	for _, ev := range cap.snapshot() {
		if ev.AnomalyType == AnomalyProtocolLoss && strings.Contains(ev.FieldPath, "context_management") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("context_management dropped without ir_protocol_loss ledger entry; events=%d", len(cap.snapshot()))
	}
}

// A2.2 anthropic thinking 意图按目标方言渲染：minimax/glm 必须落地，
// kimi/grok 按设计走 loss 上报（推理面未验证，宁可不猜）。
func TestMatrix_Q2_ThinkingPerDialect(t *testing.T) {
	cases := []struct {
		name         string
		catalog      string
		outbound     string
		wantThinking bool
	}{
		{"minimax", "minimax", "MiniMax-M3", true},
		{"glm", "zhipu", "glm-5.3", true},
		{"kimi", "moonshot", "kimi-k3", false},
		{"grok", "xai", "grok-4.6", false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			cap := resetDedupAndInstall(t)
			body := `{"model":"x","max_tokens":64,` +
				`"thinking":{"type":"enabled","budget_tokens":1024},` +
				`"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`
			req, err := ParseAnthropic([]byte(body))
			if err != nil {
				t.Fatalf("ParseAnthropic: %v", err)
			}
			req = ValidateAndFixRequest(req, "matrix-q2-think")
			req.Model = tc.outbound
			req.TargetProvider = tc.catalog
			out, err := SerializeOpenAI(req)
			if err != nil {
				t.Fatalf("SerializeOpenAI: %v", err)
			}
			var m map[string]json.RawMessage
			if err := json.Unmarshal(out, &m); err != nil {
				t.Fatalf("outbound not json: %v", err)
			}
			_, hasThinking := m["thinking"]
			_, hasEnable := m["enable_thinking"]
			rendered := hasThinking || hasEnable
			if rendered != tc.wantThinking {
				t.Errorf("thinking rendered=%v, want %v (keys=%v)", rendered, tc.wantThinking, matrixKeys(m))
			}
			if !tc.wantThinking {
				// 不渲染必须伴随 loss 台账，不得静默。
				found := false
				for _, ev := range cap.snapshot() {
					if ev.AnomalyType == AnomalyProtocolLoss && strings.Contains(ev.FieldPath, "thinking") {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("thinking dropped without ir_protocol_loss ledger entry")
				}
			}
		})
	}
}

// ── A3：openai 入向 × anthropic 上游（Q3，claude-sonnet-5）────────────

func TestMatrix_Q3_OpenAIToAnthropic_ConversionAndExtensions(t *testing.T) {
	cap := resetDedupAndInstall(t)
	body := `{"model":"claude-sonnet-5","max_tokens":128,` +
		`"messages":[{"role":"system","content":"be brief"},{"role":"user","content":"hi"}],` +
		`"temperature":0.2,` +
		`"tools":[{"type":"function","function":{"name":"get_weather","description":"w","parameters":{"type":"object","properties":{}}}}],` +
		`"tool_choice":{"type":"function","function":{"name":"get_weather"}},` +
		`"claude_relay_future_param":{"a":[true]}}`
	req, err := ParseOpenAI([]byte(body))
	if err != nil {
		t.Fatalf("ParseOpenAI: %v", err)
	}
	req = ValidateAndFixRequest(req, "matrix-q3")
	req.Model = "claude-sonnet-5"
	// apiclaude 的 catalog_code 为空（DB 实测），方言由目标协议推导。
	req.TargetProvider = ""
	out, err := SerializeAnthropic(req)
	if err != nil {
		t.Fatalf("SerializeAnthropic: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("outbound not json: %v", err)
	}
	mustHave(t, m, "system", `"be brief"`)
	mustHave(t, m, "max_tokens", `128`)
	if _, ok := m["tools"]; !ok {
		t.Errorf("outbound missing tools (keys=%v)", matrixKeys(m))
	}
	if _, ok := m["tool_choice"]; !ok {
		t.Errorf("outbound missing tool_choice (keys=%v)", matrixKeys(m))
	}
	mustHave(t, m, "claude_relay_future_param", `{"a":[true]}`)
	_ = cap
}

// ── A4：anthropic 入向 × anthropic 上游（同格式）───────────────────────

// 生产 Q4 是字节级透传（executor_anthropic.go needsConversion=false →
// legacyAnthropicBody 原样转发），扩展保留天然成立。本用例钉住 IR 纵深网：
// 即便未来某路径改走 IR 往返，anthropic 扩展也不丢。
func TestMatrix_Q4_AnthropicRoundtrip_ExtensionsPreserved(t *testing.T) {
	cap := resetDedupAndInstall(t)
	body := `{"model":"claude-sonnet-5","max_tokens":64,` +
		`"betas":["context-1m-2025-08-07"],` +
		`"context_management":{"edits":[{"type":"clear_tool_acks","trigger":{"input_tokens":1000}}]},` +
		`"mcp_servers":[{"type":"url","url":"https://mcp.example/s","name":"srv"}],` +
		`"anthropic_future_beta":{"z":1},` +
		`"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`
	req, err := ParseAnthropic([]byte(body))
	if err != nil {
		t.Fatalf("ParseAnthropic: %v", err)
	}
	req = ValidateAndFixRequest(req, "matrix-q4")
	req.Model = "claude-sonnet-5"
	out, err := SerializeAnthropic(req)
	if err != nil {
		t.Fatalf("SerializeAnthropic: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("outbound not json: %v", err)
	}
	mustHave(t, m, "betas", `["context-1m-2025-08-07"]`)
	mustHave(t, m, "anthropic_future_beta", `{"z":1}`)
	mustHave(t, m, "max_tokens", `64`)
	// context_management / mcp_servers 若被 IR 消费为一等字段再发射，
	// 键仍在；若走 Extensions 还原，键也必须在。只断言在场 + 结构可解析。
	for _, k := range []string{"context_management", "mcp_servers"} {
		raw, ok := m[k]
		if !ok {
			t.Errorf("outbound missing %q (keys=%v)", k, matrixKeys(m))
			continue
		}
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			t.Errorf("outbound %q not valid json: %v", k, err)
		}
	}
	_ = cap
}

// ── A5.1：responses 入向 × openai 形态上游（R2）───────────────────────

func TestMatrix_R2_ResponsesToChat_ConversionAndExtensions(t *testing.T) {
	cap := resetDedupAndInstall(t)
	body := `{"model":"gpt-5.6-luna",` +
		`"instructions":"be brief",` +
		`"input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}],` +
		`"temperature":0.4,` +
		`"responses_future_param":{"k":"v"}}`
	req, err := ParseResponses([]byte(body))
	if err != nil {
		t.Fatalf("ParseResponses: %v", err)
	}
	req = ValidateAndFixRequest(req, "matrix-r2")
	req.Model = "gpt-5.6-luna"
	req.TargetProvider = "openai"
	out, err := SerializeOpenAI(req)
	if err != nil {
		t.Fatalf("SerializeOpenAI: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("outbound not json: %v", err)
	}
	if _, ok := m["messages"]; !ok {
		t.Errorf("outbound missing messages (keys=%v)", matrixKeys(m))
	}
	mustHave(t, m, "temperature", `0.4`)
	mustHave(t, m, "responses_future_param", `{"k":"v"}`)
	_ = cap
}

// ── A6：gemini 入向 × openai 形态上游（G1）────────────────────────────

func TestMatrix_G1_GeminiToChat_GenerationConfigFidelity(t *testing.T) {
	cap := resetDedupAndInstall(t)
	body := `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],` +
		`"systemInstruction":{"parts":[{"text":"be brief"}]},` +
		`"generationConfig":{"temperature":0.7,"maxOutputTokens":96,"topK":5,` +
		`"thinkingConfig":{"thinkingBudget":512}},` +
		`"gemini_future_field":{"g":1}}`
	req, err := ParseGemini([]byte(body))
	if err != nil {
		t.Fatalf("ParseGemini: %v", err)
	}
	req = ValidateAndFixRequest(req, "matrix-g1")
	req.Model = "glm-5.3"
	req.TargetProvider = "zhipu"
	out, err := SerializeOpenAI(req)
	if err != nil {
		t.Fatalf("SerializeOpenAI: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("outbound not json: %v", err)
	}
	mustHave(t, m, "temperature", `0.7`)
	mustHave(t, m, "max_tokens", `96`)
	mustHave(t, m, "top_k", `5`)
	mustHave(t, m, "gemini_future_field", `{"g":1}`)
	// thinkingConfig 的 budget 意图：glm 方言应渲染思考开关（thinking 对象）。
	if _, ok := m["thinking"]; !ok {
		if _, ok2 := m["enable_thinking"]; !ok2 {
			t.Errorf("thinkingConfig intent lost for glm target (keys=%v)", matrixKeys(m))
		}
	}
	_ = cap
}
