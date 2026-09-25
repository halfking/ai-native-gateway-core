// Package bg — active_probe_executor_responses_test.go
//
// 2026-09-25 vapeur/hxt-local gpt-5.6-terra 回归：openai-responses 凭据的
// 直连探针必须打 /v1/responses 且携带 {"input","max_output_tokens"}——
// 旧实现（anthropic 字符串前缀分发）把 responses 凭据探成 chat +
// max_tokens=1，被 vapeur 以 400 "Could not finish the message because
// max_tokens or model output limit was reached" 拒绝，节点被误报失败。
package bg

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBuildEndpoint_ResponsesProtocol(t *testing.T) {
	e := &ActiveProbeExecutor{}
	cases := []struct {
		protocol string
		want     string
	}{
		{"openai-responses", "https://api.vapeur.ai/v1/responses"},
		{"openai-response", "https://api.vapeur.ai/v1/responses"}, // 历史错误拼写，归一后同上
		{"openai-completions", "https://api.vapeur.ai/v1/chat/completions"},
		{"anthropic-messages", "https://api.vapeur.ai/v1/messages"},
	}
	for _, tc := range cases {
		target := &ProbeTarget{BaseURL: "https://api.vapeur.ai/v1", Protocol: tc.protocol}
		desc := probeDescriptorFor(target.Protocol)
		got, err := e.buildEndpoint(target, desc)
		if err != nil {
			t.Fatalf("buildEndpoint(%q) error = %v", tc.protocol, err)
		}
		if got != tc.want {
			t.Errorf("buildEndpoint(%q) = %q, want %q", tc.protocol, got, tc.want)
		}
	}
}

func TestBuildProbePingBody_ResponsesProtocol(t *testing.T) {
	desc := probeDescriptorFor("openai-responses")
	body, err := buildProbePingBody("gpt-5.6-terra", desc)
	if err != nil {
		t.Fatalf("buildProbePingBody error = %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("body not JSON: %v", err)
	}
	if payload["input"] != "ping" {
		t.Fatalf("input = %#v, want ping", payload["input"])
	}
	mot, ok := payload["max_output_tokens"].(float64)
	if !ok || mot < 16 {
		t.Fatalf("max_output_tokens = %#v, want >= 16 (Responses API floor)", payload["max_output_tokens"])
	}
	if _, has := payload["messages"]; has {
		t.Fatalf("responses ping must not carry chat \"messages\" field: %s", body)
	}
	if payload["model"] != "gpt-5.6-terra" {
		t.Fatalf("model = %#v", payload["model"])
	}
}

func TestBuildProbePingBody_ChatProtocolUnchanged(t *testing.T) {
	desc := probeDescriptorFor("openai-completions")
	body, err := buildProbePingBody("m-1", desc)
	if err != nil {
		t.Fatalf("buildProbePingBody error = %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("body not JSON: %v", err)
	}
	if payload["max_tokens"] != float64(1) || payload["temperature"] != float64(0) {
		t.Fatalf("chat ping payload drifted: %s", body)
	}
}

func TestSingleResponsesPing_ClassifiesSuccess(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","status":"completed","output":[]}`))
	}))
	t.Cleanup(srv.Close)

	desc := probeDescriptorFor("openai-responses")
	result := singleResponsesPing(t.Context(), srv.URL+"/responses", "test-key", "gpt-5.6-terra", desc, 126, "gpt-5.6-terra")
	if result.status != "ok" || result.category != probeCategoryOK {
		t.Fatalf("status = %q category = %v errMsg = %q", result.status, result.category, result.errMsg)
	}
	if gotPath != "/responses" {
		t.Fatalf("probe path = %q, want /responses", gotPath)
	}
}

func TestResolveProbeEndpoint_ResponsesMode(t *testing.T) {
	tgt := probeTarget{BaseURL: "https://api.vapeur.ai/v1"}
	desc := probeDescriptorFor("openai-responses")
	got := resolveProbeEndpoint(tgt, desc, ProbeModeResponses)
	if got != "https://api.vapeur.ai/v1/responses" {
		t.Fatalf("resolveProbeEndpoint(Responses) = %q", got)
	}
}

// 与 TestProbeWithRetry_AnthropicMessagesWireShape 同型：Layer 4 对
// openai-responses 凭据的完整调用链必须落 /v1/responses + Bearer +
// Responses 载荷，2xx 归类 ok。
func TestProbeWithRetry_ResponsesWireShape(t *testing.T) {
	var gotPath, gotAuthz string
	var gotMaxOutput float64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuthz = r.Header.Get("Authorization")
		var body map[string]any
		raw := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(raw)
		_ = json.Unmarshal(raw, &body)
		gotMaxOutput, _ = body["max_output_tokens"].(float64)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_9","object":"response","status":"completed","output":[]}`))
	}))
	t.Cleanup(srv.Close)

	target := probeTarget{
		CredentialID: 126,
		RawModel:     "gpt-5.6-terra",
		BaseURL:      srv.URL,
		Protocol:     "openai-responses",
		APIKey:       "sk-test",
	}
	desc := probeDescriptorFor(target.Protocol)
	result := probeWithRetry(t.Context(), desc, target, ProbeModeResponses)

	if gotPath != "/v1/responses" {
		t.Fatalf("probe path = %q, want /v1/responses", gotPath)
	}
	if gotAuthz != "Bearer sk-test" {
		t.Fatalf("Authorization = %q, want Bearer", gotAuthz)
	}
	if gotMaxOutput < 16 {
		t.Fatalf("max_output_tokens = %v, want >= 16", gotMaxOutput)
	}
	if result.status != "ok" || result.category != probeCategoryOK {
		t.Fatalf("status = %q category = %q, want ok", result.status, result.category)
	}
}

// node_probe 直连轮（probeDirect）的同款分发：openai-responses 走
// /v1/responses + Responses 载荷；openai-completions / anthropic 行为不变。
func TestDirectProbe_ResponsesDispatch(t *testing.T) {
	if got := directProbeEndpoint("https://api.vapeur.ai/v1", "openai-responses"); got != "https://api.vapeur.ai/v1/responses" {
		t.Fatalf("directProbeEndpoint = %q", got)
	}
	var payload map[string]any
	body := directProbeBody("gpt-5.6-terra", "openai-responses")
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("body not JSON: %v", err)
	}
	if payload["input"] != "ping" {
		t.Fatalf("input = %#v", payload["input"])
	}
	if mot, _ := payload["max_output_tokens"].(float64); mot < 16 {
		t.Fatalf("max_output_tokens = %#v, want >= 16", payload["max_output_tokens"])
	}
	// 既有行为钉住：chat / anthropic 不变
	if got := directProbeEndpoint("https://apigpt.cc", "openai-completions"); got != "https://apigpt.cc/v1/chat/completions" {
		t.Fatalf("chat endpoint drifted: %q", got)
	}
	if got := directProbeEndpoint("https://apiclaude.cc", "anthropic-messages"); got != "https://apiclaude.cc/v1/messages" {
		t.Fatalf("anthropic endpoint drifted: %q", got)
	}
}
