package admin

import (
	"encoding/json"
	"testing"
)

// R64（2026-09-25）：session-ping 响应的 Probe 字段曾是死字段——
// handleCredentialSessionPing 的内联字面量从未赋值，恒 false，审计无法把
// ping 流量从生产统计剥离。修复把响应构造抽为纯函数 newCredentialSessionPingResponse
// （单点 Probe=true），本测试不连库锁定该契约。
func TestCredentialSessionPingResponseProbeFlag(t *testing.T) {
	resp := newCredentialSessionPingResponse(42, "gpt-test", "healthy", "", "", 123)
	if !resp.Probe {
		t.Fatal("Probe = false, want true (session-ping response must be marked as probe traffic)")
	}
	if resp.CredentialID != 42 || resp.Model != "gpt-test" || resp.Status != "healthy" ||
		resp.LatencyMs != 123 || resp.ErrorCode != "" || resp.Error != "" {
		t.Fatalf("passthrough fields wrong: %+v", resp)
	}
	if resp.TestedAt == "" {
		t.Fatal("TestedAt must be populated (RFC3339)")
	}

	// 走真实 JSON 编码路径断言响应体 probe==true（与 writeJSON 的编码形状一致）。
	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var wire struct {
		Probe bool `json:"probe"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !wire.Probe {
		t.Fatalf("wire probe = false, want true; body=%s", raw)
	}

	// 错误路径同样必须带 Probe=true（unreachable/auth_failed 等仍是探测流量）。
	errResp := newCredentialSessionPingResponse(7, "m", "unreachable", "transport_error", "provider could not be reached", 5)
	if !errResp.Probe || errResp.ErrorCode != "transport_error" {
		t.Fatalf("error-path response wrong: probe=%v code=%q", errResp.Probe, errResp.ErrorCode)
	}
}
