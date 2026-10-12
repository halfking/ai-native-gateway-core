package reqprobe

// 请求格式交叉测试矩阵 A7（docs/audit/2026-10-13-请求格式交叉测试矩阵.md）：
// 「出错 → 给节点打标 → 下次不全量传输」的端到端链路。
//
// 生产接线：executor_chat.go:1274（4xx 时 Diagnose）→ :1335（StripParams
// 剥参免费重试）→ 重试成功后 RecordTerminal(recovered=true) 学习 →
// executor_chat.go:606 reqprobeApplyLearned 在**下一请求首次出站前**前置
// 剥离已学习参数（24h 滑窗）。
import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestMatrix_LearnThenApplyNextRequest(t *testing.T) {
	store := NewMemoryStore()
	coord := NewCoordinator(store)
	defer coord.Close()

	// 1) 第一次请求：携带某厂商新参数（未登记 → 全样透传），上游 4xx。
	firstBody := []byte(`{"model":"grok-4.6","messages":[{"role":"user","content":"hi"}],` +
		`"reasoning_effort":"x-high","vendor_new_param":true}`)
	diag, ok := Diagnose(Input{
		HTTPStatus:   400,
		ErrorBody:    []byte(`{"error":{"message":"Unrecognized request argument supplied: reasoning_effort","type":"invalid_request_error"}}`),
		OutboundBody: firstBody,
	})
	if !ok {
		t.Fatalf("Diagnose did not classify the 400")
	}
	if diag.Trigger != TriggerParamRejected {
		t.Fatalf("Diagnose trigger = %q, want param_rejected (reason=%s)", diag.Trigger, diag.Reason)
	}
	if !strings.Contains(diag.Param, "reasoning_effort") {
		t.Fatalf("Diagnose param = %q, want reasoning_effort", diag.Param)
	}

	// 2) 剥参重试：出站体剔除该参数后仍是合法 JSON，核心字段不动。
	retried, removed := StripParams(firstBody, diag.Param)
	if len(removed) == 0 {
		t.Fatalf("StripParams removed nothing")
	}
	var rm map[string]json.RawMessage
	if err := json.Unmarshal(retried, &rm); err != nil {
		t.Fatalf("stripped body not json: %v", err)
	}
	if _, ok := rm["reasoning_effort"]; ok {
		t.Errorf("stripped body still has reasoning_effort")
	}
	if _, ok := rm["messages"]; !ok {
		t.Errorf("stripped body lost core field messages")
	}

	// 3) 重试成功 → 学习（RecordTerminal recovered=true）。
	coord.RecordTerminal(
		Input{HTTPStatus: 400, ErrorBody: []byte("Unrecognized request argument supplied: reasoning_effort"), OutboundBody: firstBody, Protocol: "openai-completions"},
		diag,
		TerminalMeta{ProviderID: 314, ProviderCode: "apigpt", ClientModel: "grok-4.6", OutboundModel: "grok-4.6"},
		true, // recovered
	)
	// Record 走异步队列（安全红线），先等记录在 store 落库可见——
	// LearnedParams 有 60s 进程内缓存，空结果抢先缓存会让落库后的
	// 读取一直拿到空，必须先确认落库再首次调用。
	deadline := time.Now().Add(2 * time.Second)
	visible := false
	for time.Now().Before(deadline) {
		recs, _, err := store.List(context.Background(), Filter{ProviderCode: "apigpt", Model: "grok-4.6"})
		if err == nil && len(recs) > 0 {
			visible = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !visible {
		t.Fatalf("record never landed in store — worker did not drain the queue")
	}
	learned := coord.LearnedParams(context.Background(), "apigpt", "grok-4.6")
	if len(learned) == 0 {
		t.Fatalf("LearnedParams empty after recovered terminal record — 节点没有被打标")
	}
	found := false
	for _, p := range learned {
		if p == "reasoning_effort" {
			found = true
		}
	}
	if !found {
		t.Errorf("learned params %v missing reasoning_effort", learned)
	}

	// 4) 第二次请求：同节点同模型，出站前前置剥离（reqprobeApplyLearned
	//    的核心两行：LearnedParams → StripParams）。
	secondBody := []byte(`{"model":"grok-4.6","messages":[{"role":"user","content":"again"}],` +
		`"reasoning_effort":"high","other_stable_param":1}`)
	out, stripped := StripParams(secondBody, strings.Join(learned, ","))
	if len(stripped) == 0 {
		t.Fatalf("second request did not strip learned params — 下次仍在全量传输")
	}
	var sm map[string]json.RawMessage
	if err := json.Unmarshal(out, &sm); err != nil {
		t.Fatalf("final outbound not json: %v", err)
	}
	if _, ok := sm["reasoning_effort"]; ok {
		t.Errorf("final outbound still carries reasoning_effort")
	}
	mustJSONKey(t, sm, "other_stable_param")
	mustJSONKey(t, sm, "messages")
}

func mustJSONKey(t *testing.T, m map[string]json.RawMessage, key string) {
	t.Helper()
	if _, ok := m[key]; !ok {
		t.Errorf("outbound lost %q", key)
	}
}

// TestMatrix_DiagnoseParentheticalPluralParam 钉住缺陷③（2026-10-13
// kimi-k3 六候选全灭的根因）：中转文案 "Validation: Unsupported
// parameter(s): `web_search`" 的括号复数形态击穿了点名提取——param_rejected
// 判得出来、参数名提不出来，剥参重试静默不触发。正则修复后必须能提出
// web_search 并完成剥参。
func TestMatrix_DiagnoseParentheticalPluralParam(t *testing.T) {
	outbound := []byte(`{"model":"kimi-k3","messages":[{"role":"user","content":"hi"}],"max_tokens":32,"web_search":false,"kimi_future_param":{"m":"a"}}`)
	diag, ok := Diagnose(Input{
		HTTPStatus:   400,
		ErrorBody:    []byte(`{"error":{"message":"Validation: Unsupported parameter(s): ` + "`web_search`" + `","type":"invalid_request_error","param":"","code":400}}`),
		OutboundBody: outbound,
	})
	if !ok || diag.Trigger != TriggerParamRejected {
		t.Fatalf("Diagnose trigger = %q ok=%v, want param_rejected (reason=%s)", diag.Trigger, ok, diag.Reason)
	}
	if diag.Param != "web_search" {
		t.Fatalf("Diagnose param = %q, want web_search —— 括号复数形态仍提不出参数名", diag.Param)
	}
	stripped, removed := StripParams(outbound, diag.Param)
	if len(removed) == 0 || removed[0] != "web_search" {
		t.Fatalf("StripParams removed = %v, want [web_search]", removed)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(stripped, &m); err != nil {
		t.Fatalf("stripped body not json: %v", err)
	}
	if _, exists := m["web_search"]; exists {
		t.Errorf("stripped body still has web_search")
	}
	mustJSONKey(t, m, "messages")
	mustJSONKey(t, m, "max_tokens")
}

// TestMatrix_StripFallbackCoversWebSearch 未点名参数（"Extra inputs are
// not permitted" 形态）时，uncommon 兜底清单必须覆盖 web_search——否则
// 同一个 4xx 在无点名路径仍然无参可剥。
func TestMatrix_StripFallbackCoversWebSearch(t *testing.T) {
	outbound := []byte(`{"model":"kimi-k3","messages":[{"role":"user","content":"hi"}],"web_search":true}`)
	_, removed := StripParams(outbound, "")
	for _, r := range removed {
		if r == "web_search" {
			return
		}
	}
	t.Fatalf("unnamed-strip fallback did not cover web_search (removed=%v)", removed)
}
