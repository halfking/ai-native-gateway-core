package ir

import (
	"encoding/json"
	"testing"
)

// R69 24h 审计轮：非流式响应的原生 stop_reason 保留（R68 流式修复在
// 非流式面的对偶）。parse 侧 mapAnthropicFinishReason / mapGeminiFinishReason
// 有损折叠（pause_turn→stop、RECITATION→content_filter），IR 原生槽
// StopReason 让同协议往返不丢失具体终态；序列化仅在同协议时透传。

func TestParseAnthropicResponse_StopReasonNativePreserved(t *testing.T) {
	body := []byte(`{"id":"msg_1","type":"message","role":"assistant","model":"claude-x",` +
		`"content":[{"type":"text","text":"hi"}],"stop_reason":"pause_turn",` +
		`"usage":{"input_tokens":1,"output_tokens":2}}`)
	ir, err := ParseAnthropicResponse(body)
	if err != nil {
		t.Fatalf("ParseAnthropicResponse: %v", err)
	}
	if ir.StopReason != "pause_turn" {
		t.Errorf("StopReason = %q, want native pause_turn", ir.StopReason)
	}
	if ir.FinishReason != "stop" {
		t.Errorf("FinishReason = %q, want normalized stop", ir.FinishReason)
	}
}

func TestSerializeAnthropicResponse_StopReasonSameProtocolPassthrough(t *testing.T) {
	ir := &InternalResponse{
		ID: "msg_1", Role: "assistant", Model: "claude-x",
		SourceProtocol: ProtocolAnthropicMessages,
		FinishReason:   "stop",
		StopReason:     "pause_turn",
	}
	out, err := SerializeAnthropicResponse(ir, "")
	if err != nil {
		t.Fatalf("SerializeAnthropicResponse: %v", err)
	}
	var wire struct {
		StopReason string `json:"stop_reason"`
	}
	if err := json.Unmarshal(out, &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if wire.StopReason != "pause_turn" {
		t.Errorf("wire stop_reason = %q, want native pause_turn (lossless same-protocol round-trip)", wire.StopReason)
	}
}

func TestSerializeAnthropicResponse_StopReasonCrossProtocolGuard(t *testing.T) {
	// Gemini 来源的原生值（RECITATION）绝不上 Anthropic wire。
	ir := &InternalResponse{
		ID: "msg_1", Role: "assistant", Model: "gemini-x",
		SourceProtocol: ProtocolGeminiGenerate,
		FinishReason:   "stop",
		StopReason:     "RECITATION",
	}
	out, err := SerializeAnthropicResponse(ir, "")
	if err != nil {
		t.Fatalf("SerializeAnthropicResponse: %v", err)
	}
	var wire struct {
		StopReason string `json:"stop_reason"`
	}
	if err := json.Unmarshal(out, &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if wire.StopReason == "RECITATION" {
		t.Error("cross-protocol native StopReason leaked onto Anthropic wire")
	}
	if wire.StopReason != "end_turn" {
		t.Errorf("wire stop_reason = %q, want mapped end_turn", wire.StopReason)
	}
}

func TestParseGeminiResponse_StopReasonNativePreserved(t *testing.T) {
	body := []byte(`{"modelVersion":"gemini-x","candidates":[{"content":{"role":"model",` +
		`"parts":[{"text":"hi"}]},"finishReason":"RECITATION"}],` +
		`"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":2,"totalTokenCount":3}}`)
	ir, err := ParseGeminiResponse(body)
	if err != nil {
		t.Fatalf("ParseGeminiResponse: %v", err)
	}
	if ir.StopReason != "RECITATION" {
		t.Errorf("StopReason = %q, want native RECITATION", ir.StopReason)
	}
	if ir.FinishReason != "content_filter" {
		t.Errorf("FinishReason = %q, want normalized content_filter", ir.FinishReason)
	}
}

func TestSerializeGeminiResponse_StopReasonSameProtocolPassthrough(t *testing.T) {
	// R68 钉桩 TestSerializeGemini_StopReasonNativePassthrough 的非流式对偶：
	// RECITATION 不得被折叠成 SAFETY。
	ir := &InternalResponse{
		Model: "gemini-x", Role: "assistant",
		SourceProtocol: ProtocolGeminiGenerate,
		FinishReason:   "content_filter",
		StopReason:     "RECITATION",
	}
	out, err := SerializeGeminiResponse(ir, "")
	if err != nil {
		t.Fatalf("SerializeGeminiResponse: %v", err)
	}
	var wire struct {
		Candidates []struct {
			FinishReason string `json:"finishReason"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(out, &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(wire.Candidates) != 1 {
		t.Fatalf("candidates = %d, want 1", len(wire.Candidates))
	}
	if got := wire.Candidates[0].FinishReason; got != "RECITATION" {
		t.Errorf("wire finishReason = %q, want native RECITATION (not collapsed SAFETY)", got)
	}
}

func TestSerializeGeminiResponse_StopReasonCrossProtocolGuard(t *testing.T) {
	// Anthropic 来源的原生值（end_turn）绝不上 Gemini wire —— 与 R68
	// TestSerializeGemini_StopReasonSourceProtocolGuard 同一守卫语义。
	ir := &InternalResponse{
		Model: "claude-x", Role: "assistant",
		SourceProtocol: ProtocolAnthropicMessages,
		FinishReason:   "stop",
		StopReason:     "end_turn",
	}
	out, err := SerializeGeminiResponse(ir, "")
	if err != nil {
		t.Fatalf("SerializeGeminiResponse: %v", err)
	}
	var wire struct {
		Candidates []struct {
			FinishReason string `json:"finishReason"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(out, &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(wire.Candidates) != 1 {
		t.Fatalf("candidates = %d, want 1", len(wire.Candidates))
	}
	if got := wire.Candidates[0].FinishReason; got != "STOP" {
		t.Errorf("wire finishReason = %q, want mapped STOP (no native end_turn leak)", got)
	}
}
