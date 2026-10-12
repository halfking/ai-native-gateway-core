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
