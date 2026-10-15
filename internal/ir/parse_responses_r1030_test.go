package ir

import (
	"encoding/json"
	"testing"
)

// 2026-10-14 — r1030：Responses input 的同轮 assistant 文本 + function_call
// 必须合并为一条 IR Message。
//
// 实测（本地网关 :8782，MiniMax-M3 经 minimax，/v1/responses）：规范客户端
// （AI-SDK/ZCode）回放历史时，同一轮 assistant 输出是相邻的
// [message item, function_call item]，parse_responses 逐项转 Message 后
// chat 出站出现两条相邻 assistant 消息（先 content 后 tool_calls）。
// MiniMax-M3 读到这种历史后停止发起 tool call（散文模仿工具调用或直接
// 叙述「我将执行步骤 2」后收尾），多轮 tool 循环断裂。
//
// 修复：function_call item 紧跟 assistant 消息（无 tool_calls）时并入
// 该消息——chat 线格式的标准历史形状。

func r1030Input(t *testing.T, items []map[string]any) *InternalRequest {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"model": "m", "input": items})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req, err := ParseResponses(raw)
	if err != nil {
		t.Fatalf("ParseResponses: %v", err)
	}
	return req
}

func TestR1030SameTurnMessageAndFunctionCallMerge(t *testing.T) {
	req := r1030Input(t, []map[string]any{
		{"role": "user", "content": "goal"},
		{"type": "message", "role": "assistant", "content": "我先拿配置"},
		{"type": "function_call", "call_id": "call_1", "name": "get_deploy_config", "arguments": `{"project":"x"}`},
		{"type": "function_call_output", "call_id": "call_1", "output": "config"},
	})
	if len(req.Messages) != 3 {
		t.Fatalf("messages = %d, want 3 (user / merged assistant / tool): %+v", len(req.Messages), req.Messages)
	}
	asst := req.Messages[1]
	if asst.Role != "assistant" {
		t.Fatalf("merged message role = %q, want assistant", asst.Role)
	}
	if len(asst.ToolCalls) != 1 || asst.ToolCalls[0].Function.Name != "get_deploy_config" {
		t.Fatalf("merged message missing tool_calls: %+v", asst.ToolCalls)
	}
	if len(asst.Content) != 1 || asst.Content[0].Text != "我先拿配置" {
		t.Fatalf("merged message lost its content: %+v", asst.Content)
	}
	if req.Messages[2].Role != "tool" {
		t.Fatalf("tool result role = %q, want tool", req.Messages[2].Role)
	}
}

// TestR1030ParallelFunctionCallsAllMerge — 并行 tool：同轮多个 function_call
// 全部并入前一条 assistant 消息。
func TestR1030ParallelFunctionCallsAllMerge(t *testing.T) {
	req := r1030Input(t, []map[string]any{
		{"role": "user", "content": "goal"},
		{"type": "message", "role": "assistant", "content": "并行取两台"},
		{"type": "function_call", "call_id": "c1", "name": "a", "arguments": "{}"},
		{"type": "function_call", "call_id": "c2", "name": "b", "arguments": "{}"},
	})
	if len(req.Messages) != 2 {
		t.Fatalf("messages = %d, want 2: %+v", len(req.Messages), req.Messages)
	}
	if got := len(req.Messages[1].ToolCalls); got != 2 {
		t.Fatalf("merged tool_calls = %d, want 2", got)
	}
}

// TestR1030FunctionCallWithoutAssistantStaysOwn — 前提在场（反方向）：
// 前面不是 assistant 消息（或前一条已带 tool_calls）时不合并，
// 避免吞掉独立轮次。
func TestR1030FunctionCallWithoutAssistantStaysOwn(t *testing.T) {
	req := r1030Input(t, []map[string]any{
		{"role": "user", "content": "goal"},
		{"type": "function_call", "call_id": "c0", "name": "solo", "arguments": "{}"},
		{"type": "function_call_output", "call_id": "c0", "output": "ok"},
		{"type": "function_call", "call_id": "c1", "name": "next", "arguments": "{}"},
	})
	if len(req.Messages) != 4 {
		t.Fatalf("messages = %d, want 4 (no merge across tool boundary): %+v", len(req.Messages), req.Messages)
	}
	if req.Messages[1].Role != "assistant" || len(req.Messages[1].ToolCalls) != 1 {
		t.Fatalf("solo function_call should stay its own message: %+v", req.Messages[1])
	}
	if req.Messages[1].ToolCalls[0].ID != "c0" {
		t.Fatalf("solo call id = %q, want c0", req.Messages[1].ToolCalls[0].ID)
	}
}

// TestR1030MergedAssistantSerializesAsOneChatMessage — 合并结果在 chat 出站
// 形状上是单条 assistant(content+tool_calls)。
func TestR1030MergedAssistantSerializesAsOneChatMessage(t *testing.T) {
	req := r1030Input(t, []map[string]any{
		{"role": "user", "content": "goal"},
		{"type": "message", "role": "assistant", "content": "我先拿配置"},
		{"type": "function_call", "call_id": "call_1", "name": "get_deploy_config", "arguments": `{"project":"x"}`},
	})
	body, err := SerializeOpenAI(req)
	if err != nil {
		t.Fatalf("SerializeOpenAI: %v", err)
	}
	var out struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal serialized chat body: %v", err)
	}
	assistants := 0
	for _, m := range out.Messages {
		if m["role"] == "assistant" {
			assistants++
		}
	}
	if assistants != 1 {
		t.Fatalf("serialized chat body carries %d assistant messages, want 1: %s", assistants, body)
	}
}
