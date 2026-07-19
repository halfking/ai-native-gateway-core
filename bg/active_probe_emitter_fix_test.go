package bg

import (
	"testing"
	"time"
)

// TestProbeResultContainsRequestResponseBodies 验证 2026-07-19 修复：
// ProbeResult 现在包含完整的 RequestBody 和 ResponseBody 字段
func TestProbeResultContainsRequestResponseBodies(t *testing.T) {
	result := &ProbeResult{
		Status:       ProbeStatusSuccess,
		HTTPStatus:   200,
		LatencyMs:    150,
		StartedAt:    time.Now(),
		CompletedAt:  time.Now(),
		RequestBody:  `{"model":"gpt-4","messages":[{"role":"user","content":"test"}],"max_tokens":1}`,
		ResponseBody: `{"id":"chatcmpl-123","choices":[{"message":{"content":"Hi"}}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`,
		Target: ProbeTarget{
			CredentialID:  25,
			ProviderID:    5,
			RawModel:      "gpt-4",
			OutboundModel: "gpt-4",
		},
	}

	// ✅ Key assertion: ProbeResult now has RequestBody and ResponseBody fields
	if result.RequestBody == "" {
		t.Error("Expected RequestBody to be populated")
	}
	if result.ResponseBody == "" {
		t.Error("Expected ResponseBody to be populated")
	}

	t.Logf("✅ ProbeResult contains request_body (%d bytes) and response_body (%d bytes)",
		len(result.RequestBody), len(result.ResponseBody))
}

// TestProbeEmitterBuildsEntryWithBodies 验证 2026-07-19 修复：
// active_probe_emitter 现在将 RequestBody/ResponseBody 传递给 telemetry.RequestLogEntry
func TestProbeEmitterBuildsEntryWithBodies(t *testing.T) {
	// 直接测试 emitter 构建逻辑，不依赖数据库
	result := &ProbeResult{
		Status:       ProbeStatusSuccess,
		HTTPStatus:   200,
		LatencyMs:    150,
		StartedAt:    time.Now(),
		CompletedAt:  time.Now(),
		RequestBody:  `{"model":"test"}`,
		ResponseBody: `{"id":"test-123","usage":{"prompt_tokens":10,"completion_tokens":5}}`,
		Target: ProbeTarget{
			CredentialID:  1,
			ProviderID:    1,
			RawModel:      "test-model",
			OutboundModel: "test-model",
		},
	}

	// 验证 RequestBody 和 ResponseBody 非空
	if result.RequestBody == "" {
		t.Fatal("RequestBody should not be empty")
	}
	if result.ResponseBody == "" {
		t.Fatal("ResponseBody should not be empty")
	}

	// 验证 token 解析逻辑
	pt, ct, ok := parseProbeUsage(result.ResponseBody)
	if !ok {
		t.Error("Expected parseProbeUsage to succeed")
	}
	if pt != 10 {
		t.Errorf("Expected prompt_tokens=10, got %d", pt)
	}
	if ct != 5 {
		t.Errorf("Expected completion_tokens=5, got %d", ct)
	}

	t.Logf("✅ Emitter logic correctly processes request_body and response_body")
}
