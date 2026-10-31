package modelcredibility

import (
	"encoding/json"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/upstreamurl"
)

func TestParseProbeResponseOpenAIChat(t *testing.T) {
	body := []byte(`{
	  "model": "claude-sonnet-4-5",
	  "choices": [{"finish_reason": "stop", "message": {"role": "assistant", "content": "ready"}}],
	  "usage": {"prompt_tokens": 23, "completion_tokens": 2, "total_tokens": 25}
	}`)
	var obs Observed
	parseProbeResponse(upstreamurl.EpChatCompletions, body, &obs)
	if obs.ReportedModel != "claude-sonnet-4-5" {
		t.Errorf("reported model = %q", obs.ReportedModel)
	}
	if !obs.HasUsage || obs.PromptTokens != 23 {
		t.Errorf("usage: has=%v tokens=%d", obs.HasUsage, obs.PromptTokens)
	}
	if obs.Content != "ready" {
		t.Errorf("content = %q", obs.Content)
	}
}

func TestParseProbeResponseAnthropic(t *testing.T) {
	body := []byte(`{
	  "model": "claude-3-5-haiku-20241022",
	  "content": [{"type": "text", "text": "ready"}],
	  "usage": {"input_tokens": 19, "output_tokens": 3}
	}`)
	var obs Observed
	parseProbeResponse(upstreamurl.EpMessages, body, &obs)
	if obs.ReportedModel != "claude-3-5-haiku-20241022" {
		t.Errorf("reported model = %q", obs.ReportedModel)
	}
	if !obs.HasUsage || obs.PromptTokens != 19 {
		t.Errorf("usage: has=%v tokens=%d", obs.HasUsage, obs.PromptTokens)
	}
	if obs.Content != "ready" {
		t.Errorf("content = %q", obs.Content)
	}
}

func TestParseProbeResponseOpenAIResponses(t *testing.T) {
	// ★★ 2026-10-30 12h 审计轮订正：★★ 夹具原先写成
	//   {"output":[{"type":"message","text":"ready"}]} —— ★★ 这个形状在真实
	//   Responses 响应里**不存在** ⇒ ★★ 单测绿而生产恒取不到正文 ⇒ ★★ 假绿。
	//   ★ 现在用本仓权威形状（cmd/gateway-v2/main.go 的格式注释 + e2e_test.go
	//   的 Output[0].Content[0].Type == "output_text"）★。
	body := []byte(`{
	  "model": "gpt-4o",
	  "output": [{"type": "message", "role": "assistant",
	              "content": [{"type": "output_text", "text": "ready"}]}],
	  "usage": {"input_tokens": 17, "output_tokens": 1, "total_tokens": 18}
	}`)
	var obs Observed
	parseProbeResponse(upstreamurl.EpResponses, body, &obs)
	if obs.ReportedModel != "gpt-4o" {
		t.Errorf("reported model = %q", obs.ReportedModel)
	}
	if !obs.HasUsage || obs.PromptTokens != 17 {
		t.Errorf("usage: has=%v tokens=%d", obs.HasUsage, obs.PromptTokens)
	}
	if obs.Content != "ready" {
		t.Errorf("content = %q", obs.Content)
	}
}

func TestParseProbeResponseMalformed(t *testing.T) {
	// 畸形 JSON ⇒ 全部字段保持零值，不 panic。
	var obs Observed
	parseProbeResponse(upstreamurl.EpChatCompletions, []byte("{not json"), &obs)
	if obs.HasUsage || obs.ReportedModel != "" || obs.Content != "" {
		t.Errorf("malformed body must yield zero observation: %+v", obs)
	}
	// 空 body 同理。
	obs = Observed{}
	parseProbeResponse(upstreamurl.EpChatCompletions, nil, &obs)
	if obs.HasUsage || obs.ReportedModel != "" {
		t.Errorf("empty body must yield zero observation: %+v", obs)
	}
}

func TestBuildProbeBody(t *testing.T) {
	// chat 形态：messages + max_tokens，且**不带 temperature**
	// （推理模型对 temperature≠1 会直接 400）。
	raw, err := buildProbeBody(upstreamurl.EpChatCompletions, "m")
	if err != nil {
		t.Fatalf("buildProbeBody: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := body["messages"]; !ok {
		t.Error("chat body must carry messages")
	}
	if _, ok := body["temperature"]; ok {
		t.Error("chat body must NOT carry temperature (reasoning models 400 on it)")
	}
	if body["max_tokens"] != float64(probeMaxTokens) {
		t.Errorf("max_tokens = %v, want %d", body["max_tokens"], probeMaxTokens)
	}

	// anthropic 形态：messages + max_tokens 必填。
	raw, err = buildProbeBody(upstreamurl.EpMessages, "m")
	if err != nil {
		t.Fatalf("buildProbeBody(messages): %v", err)
	}
	body = map[string]any{}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body["max_tokens"] == nil {
		t.Error("anthropic body must carry max_tokens")
	}

	// responses 形态：input 而非 messages。
	raw, err = buildProbeBody(upstreamurl.EpResponses, "m")
	if err != nil {
		t.Fatalf("buildProbeBody(responses): %v", err)
	}
	body = map[string]any{}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := body["input"]; !ok {
		t.Error("responses body must carry input")
	}
	if _, ok := body["messages"]; ok {
		t.Error("responses body must NOT carry messages")
	}
}
