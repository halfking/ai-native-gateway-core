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

// TestDiagnoseMiniMaxParamDialectStripsThinking（2026-10-29，211 原厂直通
// 生产验证轮实测回归钉）：MiniMax 参数 400 方言
// "invalid params, invalid thinking.type: \"enabled\" (allowed: adaptive,
// disabled) (2013)" 此前在 namedParamPatterns 七条 + paramRejectionHints
// 全断链 ⇒ named 路径判不出 param_rejected ⇒ 原生对（首发全参直通）被
// MiniMax 拒绝后无剥参重试、整流 503；同请求非原生对反而被首发前改写
// 保 200 —— 直通特性倒挂伤到它服务的客户端。
func TestDiagnoseMiniMaxParamDialectStripsThinking(t *testing.T) {
	outbound, _ := json.Marshal(map[string]any{
		"model":    "MiniMax-M3",
		"messages": []any{map[string]any{"role": "user", "content": "hi"}},
		"thinking": map[string]any{"type": "enabled"},
	})
	// 线上实捕错误体（request_id=e360e8a0590f34094c23eb93152d8032，211）。
	errBody := []byte(`{"type":"error","error":{"type":"bad_request_error","message":"invalid params, invalid thinking.type: \"enabled\" (allowed: adaptive, disabled) (2013)","http_code":"400"}}`)
	in := Input{
		HTTPStatus:   400,
		ErrorBody:    errBody,
		OutboundBody: outbound,
		ErrorKind:    "client_bug",
		Protocol:     "openai-chat",
	}
	diag, ok := Diagnose(in)
	if !ok {
		t.Fatalf("expected diagnosis for MiniMax param 400 dialect")
	}
	if diag.Trigger != TriggerParamRejected {
		t.Fatalf("trigger = %q, want param_rejected", diag.Trigger)
	}
	if diag.Param != "thinking.type" {
		t.Fatalf("param = %q, want thinking.type (upstream namespace form)", diag.Param)
	}
	// 剥离落点必须是线体顶层键 thinking（wireKeyAliases 解析）。
	stripped, removed := StripParamsForWire(outbound, diag.Param, "openai-chat")
	if len(removed) != 1 || removed[0] != "thinking" {
		t.Fatalf("removed = %v, want [thinking]", removed)
	}
	var check map[string]json.RawMessage
	_ = json.Unmarshal(stripped, &check)
	if _, has := check["thinking"]; has {
		t.Fatalf("thinking must be stripped from wire body")
	}
	if _, has := check["messages"]; !has {
		t.Fatalf("messages skeleton must survive the strip")
	}
}

// 负臂：同款 400 文案、但出站体没有可剥离参数 ⇒ 不得判 param_rejected
//（成员判定走别名扩展后仍不允许无中生有）。
func TestDiagnoseMiniMaxParamDialectWithoutStrippableNotParamRejected(t *testing.T) {
	outbound, _ := json.Marshal(map[string]any{
		"model":    "MiniMax-M3",
		"messages": []any{map[string]any{"role": "user", "content": "hi"}},
	})
	errBody := []byte(`{"type":"error","error":{"type":"bad_request_error","message":"invalid params, invalid thinking.type: \"enabled\" (allowed: adaptive, disabled) (2013)","http_code":"400"}}`)
	in := Input{
		HTTPStatus:   400,
		ErrorBody:    errBody,
		OutboundBody: outbound,
		ErrorKind:    "client_bug",
		Protocol:     "openai-chat",
	}
	diag, ok := Diagnose(in)
	if ok && diag.Trigger == TriggerParamRejected {
		t.Fatalf("must not diagnose param_rejected when outbound has no strippable param, got %+v", diag)
	}
}

// TestDiagnoseAdaptiveOnlyThinkingRequired（2026-11-13 opus-5-5 事故轮）：
// adaptive-only 模型族拒绝 enabled/disabled 形态的文案
// "<model> requires adaptive thinking; omit thinking or use
// thinking.type=adaptive and output_config.effort"——参数名 "thinking.type"
// 裸写在补救句里（无引号包裹）⇒ 八条 namedParamPatterns 全断链 ⇒ 此前判
// 不出 param_rejected ⇒ anthropic 腿零记录、chat 腿零剥参重试（211 活体
// 02:23~02:34 三起全候选失败）。补 "requires adaptive thinking" hint 后，
// 出站体带 thinking 时必须判 param_rejected 并点名剔除（清单未覆盖新模型
// 时的第二道兜底；首发防线在 paramguard.adaptiveOnlyModels）。
func TestDiagnoseAdaptiveOnlyThinkingRequired(t *testing.T) {
	outbound, _ := json.Marshal(map[string]any{
		"model":      "claude-somefuture-9",
		"messages":   []any{map[string]any{"role": "user", "content": "hi"}},
		"max_tokens": 2000,
		"thinking":   map[string]any{"type": "enabled", "budget_tokens": 1024},
	})
	in := Input{
		HTTPStatus:   400,
		ErrorBody:    []byte(`{"error":{"message":"claude-somefuture-9 requires adaptive thinking; omit thinking or use thinking.type=adaptive and output_config.effort","type":"invalid_request_error"}}`),
		OutboundBody: outbound,
		ErrorKind:    "request_rejected",
	}
	diag, ok := Diagnose(in)
	if !ok {
		t.Fatalf("expected diagnosis for adaptive-thinking-required 400")
	}
	if diag.Trigger != TriggerParamRejected {
		t.Fatalf("trigger = %q, want param_rejected", diag.Trigger)
	}
	if diag.Param != "thinking" {
		t.Fatalf("param = %q, want thinking", diag.Param)
	}
	stripped, removed := StripParamsForWire(outbound, diag.Param, "")
	if len(removed) != 1 || removed[0] != "thinking" {
		t.Fatalf("removed = %v, want [thinking]", removed)
	}
	var check map[string]json.RawMessage
	_ = json.Unmarshal(stripped, &check)
	if _, has := check["thinking"]; has {
		t.Fatalf("thinking must be stripped from wire body")
	}
}

// 反向：文案命中但出站体无 thinking ⇒ 不判 param_rejected（无物可剥）。
func TestDiagnoseAdaptiveOnlyThinkingRequiredWithoutParam(t *testing.T) {
	outbound, _ := json.Marshal(map[string]any{
		"model":      "claude-somefuture-9",
		"messages":   []any{map[string]any{"role": "user", "content": "hi"}},
		"max_tokens": 100,
	})
	in := Input{
		HTTPStatus:   400,
		ErrorBody:    []byte(`{"error":{"message":"claude-somefuture-9 requires adaptive thinking; omit thinking"}}`),
		OutboundBody: outbound,
		ErrorKind:    "request_rejected",
	}
	diag, ok := Diagnose(in)
	if ok && diag.Trigger == TriggerParamRejected {
		t.Fatalf("must not diagnose param_rejected when no thinking param present, got %+v", diag)
	}
}
