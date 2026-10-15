package ir

import (
	"encoding/json"
	"testing"
)

// thinking 翻译轮 IR 层判据（2026-10-13）：unknown-relay（空 catalog 哨兵）
// 上 banked thinking 的处理从 r1021 的「RejectedByUnknown 裁剪」升级为
// 「按目标协议翻译」——数据矩阵见
// docs/audit/2026-10-13-Vapeur全量测试轮.md §七。
func TestUnknownRelayThinkingTranslatedPerProtocol(t *testing.T) {
	mk := func(targetProvider string) *InternalRequest {
		req, err := ParseOpenAI([]byte(`{"model":"grok-4.6","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled","budget_tokens":2048}}`))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		req.TargetProvider = targetProvider
		return req
	}

	// responses 协议（Vapeur #36 出站形态）：thinking → reasoning{effort}。
	req := mk("vapeur") // 未登记 catalog → DialectUnknown
	out, err := SerializeResponsesRequest(req)
	if err != nil {
		t.Fatalf("serialize responses: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("out invalid: %v\n%s", err, out)
	}
	if _, has := m["thinking"]; has {
		t.Fatal("responses 出站不得再含 thinking")
	}
	r, ok := m["reasoning"].(map[string]any)
	if !ok || r["effort"] != "medium" {
		t.Fatalf("responses 出站应含 reasoning{effort:medium}（budget 2048→medium），got %v", m["reasoning"])
	}

	// chat 协议：thinking → reasoning_effort 字符串。
	req2 := mk("vapeur")
	out2, err2 := SerializeOpenAI(req2)
	if err2 != nil {
		t.Fatalf("serialize chat: %v", err2)
	}
	var m2 map[string]any
	if err := json.Unmarshal(out2, &m2); err != nil {
		t.Fatalf("chat out invalid: %v", err)
	}
	if _, has := m2["thinking"]; has {
		t.Fatal("chat 出站不得再含 thinking")
	}
	if m2["reasoning_effort"] != "medium" {
		t.Fatalf("chat 出站应含 reasoning_effort=medium，got %v", m2["reasoning_effort"])
	}

	// 客户端显式 effort 优先：已有键不覆盖（IR 渲染的 effort 不被 thinking 换算顶掉）。
	req3, err := ParseOpenAI([]byte(`{"model":"grok-4.6","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"high","thinking":{"type":"enabled","budget_tokens":1024}}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	req3.TargetProvider = "vapeur"
	out3, err3 := SerializeOpenAI(req3)
	if err3 != nil {
		t.Fatalf("serialize chat: %v", err3)
	}
	var m3 map[string]any
	_ = json.Unmarshal(out3, &m3)
	if m3["reasoning_effort"] != "high" {
		t.Fatalf("显式 reasoning_effort=high 应保留（不被 thinking 换算的 low 顶掉），got %v", m3["reasoning_effort"])
	}
}
