package executors

import (
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
