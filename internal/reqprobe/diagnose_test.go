package reqprobe

// diagnose_test.go — Diagnose 的判据锁定。
// 2026-10-27 多模态标准接口轮新增：thinking 能力族（ollama 对非 thinking
// 模型收到 reasoning_effort/think 回 400 "does not support thinking"）——
// 判 param_rejected 并点名剔除 thinking 族参数，出站体没带这些参数时
// **不得**误判（保持原分类路径）。

import (
	"encoding/json"
	"testing"
)

func TestDiagnoseThinkingUnsupportedStripsReasoningParams(t *testing.T) {
	outbound, _ := json.Marshal(map[string]any{
		"model":            "qwen2.5:7b",
		"messages":         []any{map[string]any{"role": "user", "content": "hi"}},
		"max_tokens":       3000,
		"reasoning_effort": "low",
	})
	in := Input{
		HTTPStatus:   400,
		ErrorBody:    []byte(`{"error":{"message":"\"qwen2.5:7b\" does not support thinking","type":"invalid_request_error","param":null}}`),
		OutboundBody: outbound,
		ErrorKind:    "client_bug",
	}
	diag, ok := Diagnose(in)
	if !ok {
		t.Fatalf("expected diagnosis for thinking-unsupported 400")
	}
	if diag.Trigger != TriggerParamRejected {
		t.Fatalf("trigger = %q, want param_rejected", diag.Trigger)
	}
	if diag.Param != "reasoning_effort" {
		t.Fatalf("param = %q, want reasoning_effort", diag.Param)
	}
	stripped, removed := StripParamsForWire(outbound, diag.Param, "")
	if len(removed) != 1 || removed[0] != "reasoning_effort" {
		t.Fatalf("removed = %v", removed)
	}
	var check map[string]json.RawMessage
	_ = json.Unmarshal(stripped, &check)
	if _, has := check["reasoning_effort"]; has {
		t.Fatalf("reasoning_effort must be stripped from wire body")
	}
	if _, has := check["max_tokens"]; !has {
		t.Fatalf("max_tokens must survive the strip")
	}
}

// 反向：文案命中但出站体没有 thinking 族参数 ⇒ 不判 param_rejected
// （没有可剔除的东西，交给后续分类），也不崩。
func TestDiagnoseThinkingUnsupportedWithoutParamsNotParamRejected(t *testing.T) {
	outbound, _ := json.Marshal(map[string]any{
		"model":      "qwen2.5:7b",
		"messages":   []any{map[string]any{"role": "user", "content": "hi"}},
		"max_tokens": 100,
	})
	in := Input{
		HTTPStatus:   400,
		ErrorBody:    []byte(`{"error":{"message":"model does not support thinking"}}`),
		OutboundBody: outbound,
		ErrorKind:    "client_bug",
	}
	diag, ok := Diagnose(in)
	if ok && diag.Trigger == TriggerParamRejected {
		t.Fatalf("must not diagnose param_rejected when no reasoning params present, got %+v", diag)
	}
}
