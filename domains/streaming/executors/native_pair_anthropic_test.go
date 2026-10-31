package executors

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/kaixuan/llm-gateway-go/provider"
)

// 批判式审计轮（2026-10-28）补测：上一轮的端到端只覆盖了 openai 协议
// 入向（legacy 路径）；zcode 的实际形态是 anthropic 协议入向（/v1/messages）
// → IR 分支。该路径上 nativePair 只经 `irReq.NativePassthrough` 影响
// Extensions 还原；IR-handled 字段（thinking 等）走方言渲染（必需的协议
// 转换，非「参数剥离」）。本测试把这条路径钉死：
//
//	正臂（nativePair=true）  ：Extensions 原样透传 + thinking 渲染为 glm 方言
//	对照臂（nativePair=false）：Extensions 被方言守卫裁剪（门有牙）
//
// Extensions 靶字段选 `moderation`：KindDialectOnly 且登记方言
// [openai-chat, responses] 不含 glm ⇒ dst=glm 时 paramreg 判 Drop ——
// 这是「非原生对必然被裁、原生对必须保留」的可区分字段。
// （`enable_thinking` 是 KindPortable，Portable 不按方言裁 ⇒ 区分不出；
//
//	IR-handled 字段不走 Extensions，不受 NativePassthrough 影响。）
func TestFinalizeNativePair_AnthropicInbound_VendorExtensionSurvives(t *testing.T) {
	// zcode 实际形态：anthropic 协议入向（/v1/messages）→ GLM 候选。
	anthropicBody := `{
		"model": "glm-5.3",
		"max_tokens": 1024,
		"messages": [{"role":"user","content":"hi"}],
		"thinking": {"type":"enabled","budget_tokens":4096},
		"search_parameters": {"mode": "auto"},
		"zcode_vendor_flag": {"a": 1}
	}`
	executor := &Executor{IR: &irAdapterForTest{}}
	cand := provider.Candidate{
		ProviderID:   7,
		CredentialID: 55,
		Protocol:     "openai-completions",
		CatalogCode:  "zhipu",
		RawModel:     "glm-5.3",
	}
	params := &ExecParams{
		R:              httptest.NewRequest("POST", "/v1/messages", nil),
		ClientModel:    "glm-5.3",
		OutboundModel:  "glm-5.3",
		ClientProtocol: "anthropic-messages",
	}

	t.Run("nativePair=true: extension verbatim + thinking rendered for glm", func(t *testing.T) {
		body, err := executor.finalizeOpenAIUpstreamBodyNative(params, cand, []byte(anthropicBody), true)
		if err != nil {
			t.Fatalf("finalize: %v", err)
		}
		m := decodeUpstreamBody(t, body)
		if _, ok := m["search_parameters"]; !ok {
			t.Fatalf("native pair: search_parameters dropped by dialect guard (NativePassthrough not wired?):\n%s", body)
		}
		if _, ok := m["zcode_vendor_flag"]; !ok {
			t.Fatalf("native pair: unregistered vendor field dropped:\n%s", body)
		}
		// thinking 是 IR-handled 字段：协议转换（anthropic → glm 方言）必须
		// 仍然生效 —— 直通不等于丢掉协议语义。
		th, ok := m["thinking"].(map[string]any)
		if !ok {
			t.Fatalf("native pair: thinking missing (glm dialect rendering broken):\n%s", body)
		}
		if th["type"] != "enabled" {
			t.Fatalf("native pair: thinking.type = %v, want \"enabled\" (glm dialect keeps enabled)\n%s", th["type"], body)
		}
	})

	t.Run("nativePair=false: dialect guard still crops search_parameters", func(t *testing.T) {
		body, err := executor.finalizeOpenAIUpstreamBodyNative(params, cand, []byte(anthropicBody), false)
		if err != nil {
			t.Fatalf("finalize: %v", err)
		}
		m := decodeUpstreamBody(t, body)
		if _, ok := m["search_parameters"]; ok {
			t.Fatalf("non-native: dialect guard did NOT crop search_parameters:\n%s", body)
		}
		if _, ok := m["zcode_vendor_flag"]; !ok {
			t.Fatalf("non-native: unregistered field must stay fail-open:\n%s", body)
		}
	})
}

// ─── RISK-6 收口（2026-10-29）：Claude Code↔Claude 原厂对在 Anthropic 腿 ───
//
// 生效点按「谁做序列化」盘点（executor_anthropic.go 本轮接线处注释 S1~S6）：
//   S1 IR 序列化站点（Q3）盖 NativePassthrough；S2 SanitizeAnthropicTools
//   （两分支）原生对跳过；S3 paramguard(DialectAnthropic)+reqprobeApplyLearned
//   原生对跳过；S4/S5 结构性变换保留；S6 响应侧无关。
// 三个测试各钉一个生效点，均带非原生对照臂（门必须有牙）。

// TestPrepareAnthropicRequestBody_NativePair_ParamguardSkipped（S3）：
// claude-code→claude（Q4 直传，无协议转换）。非原生对被 paramguard 的
// removeSamplingIfThinking 裁掉 temperature（Anthropic 硬 400 预防）；
// 原生对首发全参 —— temperature 原样保留。
func TestPrepareAnthropicRequestBody_NativePair_ParamguardSkipped(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-5","max_tokens":4096,"temperature":0.7,` +
		`"thinking":{"type":"enabled","budget_tokens":2048},` +
		`"messages":[{"role":"user","content":"hi"}]}`)
	cand := provider.Candidate{
		ProviderID:   7,
		CredentialID: 55,
		Protocol:     "anthropic-messages",
		CatalogCode:  "anthropic",
		RawModel:     "claude-sonnet-5",
	}
	newParams := func(clientType string) *ExecParams {
		r := httptest.NewRequest("POST", "/v1/messages", nil)
		r.Header.Set("X-Gw-Client-Type", clientType)
		return &ExecParams{
			R:              r,
			ClientType:     clientType,
			ClientModel:    "claude-sonnet-5",
			OutboundModel:  "claude-sonnet-5",
			ClientProtocol: "anthropic-messages",
		}
	}

	executor := &Executor{}
	native, err := executor.prepareAnthropicRequestBody(newParams("claude-code"), cand, body)
	if err != nil {
		t.Fatalf("native prepare: %v", err)
	}
	nm := decodeUpstreamBody(t, native)
	if nm["temperature"] != float64(0.7) {
		t.Fatalf("native claude pair: temperature = %#v, want 0.7 verbatim (paramguard must not touch native pair first send)\n%s", nm["temperature"], native)
	}
	th, ok := nm["thinking"].(map[string]any)
	if !ok || th["type"] != "enabled" || th["budget_tokens"] != float64(2048) {
		t.Fatalf("native claude pair: thinking = %#v, want verbatim\n%s", nm["thinking"], native)
	}

	// 对照臂：非原生（cursor→claude）维持既有钳制 —— thinking 启用时
	// sampling 参数被裁（Anthropic 硬 400 预防必须保住牙齿）。
	legacy, err := executor.prepareAnthropicRequestBody(newParams("cursor"), cand, body)
	if err != nil {
		t.Fatalf("non-native prepare: %v", err)
	}
	lm := decodeUpstreamBody(t, legacy)
	if _, present := lm["temperature"]; present {
		t.Fatalf("non-native claude: temperature must be stripped by paramguard (teeth must stay)\n%s", legacy)
	}
	if _, ok := lm["thinking"]; !ok {
		t.Fatalf("non-native claude: thinking must survive (only sampling is stripped)\n%s", legacy)
	}
}

// TestPrepareAnthropicRequestBody_NativePair_ToolShapeVerbatim（S2）：
// 原生对跳过 SanitizeAnthropicTools —— 工具顶层自定义键不再被重建丢弃
// （chat 腿 SimplifyTools 同族豁免）。Sanitize 钩子用桩代替（钩子由
// cmd/gateway/main.go 注入，本测试钉的是门控接线，不是 sanitize 实现）。
func TestPrepareAnthropicRequestBody_NativePair_ToolShapeVerbatim(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-5","max_tokens":1024,` +
		`"tools":[{"name":"f","description":"d","input_schema":{"type":"object"},"vendor_tool_flag":true}],` +
		`"messages":[{"role":"user","content":"hi"}]}`)
	cand := provider.Candidate{
		ProviderID:   7,
		CredentialID: 55,
		Protocol:     "anthropic-messages",
		CatalogCode:  "anthropic",
		RawModel:     "claude-sonnet-5",
	}
	newParams := func(clientType string) *ExecParams {
		r := httptest.NewRequest("POST", "/v1/messages", nil)
		r.Header.Set("X-Gw-Client-Type", clientType)
		return &ExecParams{
			R:              r,
			ClientType:     clientType,
			ClientModel:    "claude-sonnet-5",
			OutboundModel:  "claude-sonnet-5",
			ClientProtocol: "anthropic-messages",
		}
	}
	// 桩：重建工具、只留 name/description/input_schema（与
	// streaming.SanitizeAnthropicToolDefinitions 的丢弃行为同形）。
	sanitizeStub := func(b []byte) []byte {
		var obj map[string]any
		if json.Unmarshal(b, &obj) != nil {
			return b
		}
		tools, _ := obj["tools"].([]any)
		out := make([]any, 0, len(tools))
		for _, item := range tools {
			tool, ok := item.(map[string]any)
			if !ok {
				out = append(out, item)
				continue
			}
			out = append(out, map[string]any{
				"name":         tool["name"],
				"description":  tool["description"],
				"input_schema": tool["input_schema"],
			})
		}
		obj["tools"] = out
		raw, err := json.Marshal(obj)
		if err != nil {
			return b
		}
		return raw
	}
	executor := &Executor{SanitizeAnthropicTools: sanitizeStub}

	native, err := executor.prepareAnthropicRequestBody(newParams("claude-code"), cand, body)
	if err != nil {
		t.Fatalf("native prepare: %v", err)
	}
	nm := decodeUpstreamBody(t, native)
	ntools, _ := nm["tools"].([]any)
	if len(ntools) != 1 {
		t.Fatalf("native: tools = %v, want 1 entry\n%s", nm["tools"], native)
	}
	if ntool, ok := ntools[0].(map[string]any); !ok || ntool["vendor_tool_flag"] != true {
		t.Fatalf("native claude pair: vendor tool key dropped (sanitize must be skipped for native pair):\n%s", native)
	}

	legacy, err := executor.prepareAnthropicRequestBody(newParams("cursor"), cand, body)
	if err != nil {
		t.Fatalf("non-native prepare: %v", err)
	}
	lm := decodeUpstreamBody(t, legacy)
	ltools, _ := lm["tools"].([]any)
	if len(ltools) != 1 {
		t.Fatalf("non-native: tools = %v\n%s", lm["tools"], legacy)
	}
	if ltool, ok := ltools[0].(map[string]any); !ok {
		t.Fatalf("non-native: tool not an object\n%s", legacy)
	} else if _, present := ltool["vendor_tool_flag"]; present {
		t.Fatalf("non-native: sanitize hook did not run (teeth must stay)\n%s", legacy)
	}
}

// TestPrepareAnthropicRequestBody_NativePair_IRStamp（S1）：Q3 转换路径
// （openai 入向 → anthropic-messages 上游）—— IR 序列化站点漏盖
// NativePassthrough 时厂商 Extensions 被 paramreg 方言裁剪（65d24d0df 在
// chat 腿修的同族缺陷）。靶字段 moderation：KindDialectOnly 注册方言
// [openai-chat, responses]；目标 CatalogCode=anthropic（DialectAnthropic，
// BaseProtocol 不继承 openai-chat）⇒ 非原生必裁、原生必须原样。★ 靶字段
// 必须按目标方言的 knownBy 真值选 —— minimax 目录继承 openai-chat 基协议，
// moderation 对 dst=minimax 是放行的（本轮实测踩过）。
// 2026-10-29 厂商感知修正：anthropic 方言宿主上唯一的原厂对是
// claude-code×claude（minimax-code×anthropic 宿主按模型名误判的老正臂
// 已随厂商闸门退役——第三方宿主就该走方言裁剪 + 学习预剥离）。
func TestPrepareAnthropicRequestBody_NativePair_IRStamp(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-5","max_tokens":1024,` +
		`"messages":[{"role":"user","content":"hi"}],"moderation":true}`)
	cand := provider.Candidate{
		ProviderID:   7,
		CredentialID: 55,
		Protocol:     "anthropic-messages",
		CatalogCode:  "anthropic",
		RawModel:     "claude-sonnet-5",
	}
	newParams := func(clientType string) *ExecParams {
		r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
		r.Header.Set("X-Gw-Client-Type", clientType)
		return &ExecParams{
			R:              r,
			ClientType:     clientType,
			ClientModel:    "claude-sonnet-5",
			OutboundModel:  "claude-sonnet-5",
			ClientProtocol: "openai-completions",
		}
	}
	executor := &Executor{IR: &irAdapterForTest{}}

	native, err := executor.prepareAnthropicRequestBody(newParams("claude-code"), cand, body)
	if err != nil {
		t.Fatalf("native prepare: %v", err)
	}
	nm := decodeUpstreamBody(t, native)
	if _, ok := nm["moderation"]; !ok {
		t.Fatalf("native claude-code pair (Q3): moderation dropped — IR serialize site missing NativePassthrough stamp:\n%s", native)
	}

	legacy, err := executor.prepareAnthropicRequestBody(newParams("cursor"), cand, body)
	if err != nil {
		t.Fatalf("non-native prepare: %v", err)
	}
	lm := decodeUpstreamBody(t, legacy)
	if _, ok := lm["moderation"]; ok {
		t.Fatalf("non-native (Q3): dialect guard must still crop moderation on anthropic target (teeth must stay):\n%s", legacy)
	}
}
