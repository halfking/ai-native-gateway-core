package contextbudget

import (
	"encoding/json"
	"strings"
	"testing"
)

// r1050（2026-11-13，opus-4-8 反复 422 根修）钉测。
//
// 事故形状（211 生产实证，gw_f9fdc2de 10:23 失败 → 10:52 重试同败）：
// ZCode/3.18 对 claude-opus-4-8（200k 窗）发 max_tokens=32000 的会话，固定
// 载荷 tools ≈ 225KB + system ≈ 15KB（≈70k 估计 token，不可裁——Fit 只裁
// messages）+ 会话历史 ~85KB ⇒ 全 body ≈ 336KB ⇒ 校准估计 109,337 token。
// 旧预算：输入半窗帽 100,000 ⇒ TokensExceeded ⇒ fit 字节目标 300KB 装不下
// tools+system+受保护区 ⇒ ErrProtectedContext ⇒ 422 retryable=false；会话
// 缓存重放同一出站体 ⇒ 重试同败（会话被「砖化」）。
// 新预算：W − 预留（body 侧半窗封顶后 32000）= 168,000 ⇒ 放行。
func TestR1049ZCodeFixedPayloadAdmitted(t *testing.T) {
	body := r1050IncidentBody(t, 85*1024, 225*1024, 15*1024, 32000)
	spec := Spec{ContextWindow: 200000}
	got, err := Check(body, spec)
	if err != nil {
		t.Fatalf("r1050 incident shape must be admitted (est=%d budget=%+v): %v", got.Tokens, got.Budget, err)
	}
	if got.Budget.InputTokens != 168000 {
		t.Fatalf("budget = %d, want 168000 (200000-32000, no half-window cap)", got.Budget.InputTokens)
	}
	if got.Tokens < 100000 {
		t.Fatalf("incident premise broken: est %d should exceed the old 100k cap to be a valid replica", got.Tokens)
	}
}

// 半窗帽时代的核心病灶：max_tokens ≥ 半窗时新旧语义恰好同值（封顶预留 =
// 半窗 ⇒ W−W/2 = W/2），钉住这个交界防止未来改动把输出保底弄丢。
func TestR1049OutputReserveCeilingStillGuaranteesOutput(t *testing.T) {
	incident := []byte(`{"model":"m","max_tokens":128000,"messages":[{"role":"user","content":"hi"}]}`)
	got, err := Check(incident, Spec{ContextWindow: 200000})
	if err != nil || got.Budget.InputTokens != 100000 {
		t.Fatalf("ask ≥ half window keeps reserve at half (input = 100000), got %+v, %v", got.Budget, err)
	}
}

// 真正超窗（估计 > W − 预留）仍必须被拒——r1050 放宽的是半窗帽，不是
// 容量等式本身；超窗请求走 fit/恢复链，不是放行。
func TestR1049GenuinelyOverWindowStillRejected(t *testing.T) {
	body := r1050IncidentBody(t, 500*1024, 225*1024, 15*1024, 32000)
	_, err := Check(body, Spec{ContextWindow: 200000})
	if err == nil {
		t.Fatal("body far over window-reserve must still be rejected")
	}
	var budgetErr *Error
	if !asBudgetErr(err, &budgetErr) || budgetErr.Kind != TokensExceeded {
		t.Fatalf("want TokensExceeded, got %v", err)
	}
	if budgetErr.Limit != 168000 {
		t.Fatalf("limit = %d, want 168000", budgetErr.Limit)
	}
}

func asBudgetErr(err error, target **Error) bool {
	if e, ok := err.(*Error); ok {
		*target = e
		return true
	}
	return false
}

// r1050IncidentBody builds a ZCode-shaped request: `history` bytes of session
// messages + `toolsBytes` of tool schema text + `systemBytes` of system text.
func r1050IncidentBody(t *testing.T, history, toolsBytes, systemBytes, maxTokens int) []byte {
	t.Helper()
	msgs := []any{}
	size := 0
	// conversation history: user/assistant/tool turns of plain text
	for i := 0; size < history; i++ {
		role := "user"
		if i%3 == 1 {
			role = "assistant"
		} else if i%3 == 2 {
			role = "tool"
		}
		chunk := map[string]any{"role": role, "content": strings.Repeat("conversation text payload. ", 80)}
		b, _ := json.Marshal(chunk)
		size += len(b)
		if role == "tool" {
			chunk["tool_call_id"] = "call_1"
		}
		msgs = append(msgs, chunk)
	}
	// ZCode tool suite: ~90 tools with text descriptions (the 225KB measured
	// on 2026-11-13: body 970373 − cache 253365 − client msgs 491477).
	tools := []any{}
	tsize := 0
	for i := 0; tsize < toolsBytes; i++ {
		tool := map[string]any{
			"name":        "tool_" + string(rune('a'+i%26)) + string(rune('a'+i/26%26)),
			"description": "Run this tool against the workspace and return the output. " + strings.Repeat("schema text ", 120),
			"input_schema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path":  map[string]any{"type": "string", "description": "target path"},
					"extra": map[string]any{"type": "string", "description": "extra argument"},
				},
				"required": []string{"path"},
			},
		}
		b, _ := json.Marshal(tool)
		tsize += len(b)
		tools = append(tools, tool)
	}
	body, err := json.Marshal(map[string]any{
		"model":      "claude-opus-4-8",
		"max_tokens": maxTokens,
		"system":     strings.Repeat("system instructions. ", systemBytes/22),
		"tools":      tools,
		"messages":   msgs,
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
}
